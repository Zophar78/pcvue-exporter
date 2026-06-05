package pcvue

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Zophar78/pcvue-exporter/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	authTypeOAuth2ClientCredentials = "oauth2_client_credentials"
	authTypeOAuth2Password          = "oauth2_password"
)

type Client struct {
	baseURL string

	httpClient  *http.Client
	tokenSource oauth2.TokenSource
}

func NewClient(cfg config.PCVueConfig) (*Client, error) {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec
		},
	}

	httpClient := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
	}

	tokenSource, err := newTokenSource(cfg, httpClient)
	if err != nil {
		return nil, err
	}

	return &Client{
		baseURL:     baseURL,
		httpClient:  httpClient,
		tokenSource: tokenSource,
	}, nil
}

func newTokenSource(cfg config.PCVueConfig, httpClient *http.Client) (oauth2.TokenSource, error) {
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, httpClient)

	switch cfg.Auth.Type {
	case authTypeOAuth2ClientCredentials:
		if cfg.Auth.TokenURL == "" {
			return nil, fmt.Errorf("pcvue.auth.token_url is required for %s", authTypeOAuth2ClientCredentials)
		}
		if cfg.Auth.ClientID == "" {
			return nil, fmt.Errorf("pcvue.auth.client_id is required for %s", authTypeOAuth2ClientCredentials)
		}
		if cfg.Auth.ClientSecret == "" {
			return nil, fmt.Errorf("pcvue.auth.client_secret is required for %s", authTypeOAuth2ClientCredentials)
		}

		oauthConfig := clientcredentials.Config{
			ClientID:     cfg.Auth.ClientID,
			ClientSecret: cfg.Auth.ClientSecret,
			TokenURL:     cfg.Auth.TokenURL,
			Scopes:       cfg.Auth.Scopes,
		}

		return oauth2.ReuseTokenSource(nil, oauthConfig.TokenSource(ctx)), nil

	case authTypeOAuth2Password:
		if cfg.Auth.TokenURL == "" {
			return nil, fmt.Errorf("pcvue.auth.token_url is required for %s", authTypeOAuth2Password)
		}
		if cfg.Auth.ClientID == "" {
			return nil, fmt.Errorf("pcvue.auth.client_id is required for %s", authTypeOAuth2Password)
		}
		if cfg.Auth.Username == "" {
			return nil, fmt.Errorf("pcvue.auth.username is required for %s", authTypeOAuth2Password)
		}
		if cfg.Auth.Password == "" {
			return nil, fmt.Errorf("pcvue.auth.password is required for %s", authTypeOAuth2Password)
		}

		oauthConfig := oauth2.Config{
			ClientID:     cfg.Auth.ClientID,
			ClientSecret: cfg.Auth.ClientSecret,
			Scopes:       cfg.Auth.Scopes,
			Endpoint: oauth2.Endpoint{
				TokenURL: cfg.Auth.TokenURL,
			},
		}

		return oauth2.ReuseTokenSource(nil, &passwordTokenSource{
			ctx:        ctx,
			config:     &oauthConfig,
			username:   cfg.Auth.Username,
			password:   cfg.Auth.Password,
			httpClient: httpClient,
		}), nil

	default:
		return nil, fmt.Errorf("unsupported pcvue.auth.type: %q", cfg.Auth.Type)
	}
}

type passwordTokenSource struct {
	ctx        context.Context
	config     *oauth2.Config
	username   string
	password   string
	httpClient *http.Client
}

func (s *passwordTokenSource) Token() (*oauth2.Token, error) {
	ctx := context.WithValue(s.ctx, oauth2.HTTPClient, s.httpClient)

	token, err := s.config.PasswordCredentialsToken(ctx, s.username, s.password)
	if err != nil {
		return nil, fmt.Errorf("get oauth2 password token: %w", err)
	}

	return token, nil
}

func (c *Client) ReadVariables(ctx context.Context, names []string) ([]VariableValue, error) {
	if len(names) == 0 {
		return nil, nil
	}

	payload := map[string]any{
		"Variables": names,
		"Properties": variablePropertiesToStrings(
			VariablePropertyVariableName,
			VariablePropertyDescription,
			VariablePropertyUnit,
		),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal variable bulk read payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/RealTimeData/v2/BulkRead",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("create variable bulk read request: %w", err)
	}

	if err := c.addAuthHeaders(req); err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute variable bulk read request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pcvue variable bulk read failed: status=%d", resp.StatusCode)
	}

	var decoded map[string]struct {
		Value        any     `json:"value"`
		Timestamp    string  `json:"Timestamp"`
		Quality      string  `json:"quality"`
		QualityValue float64 `json:"QualityValue"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode variable bulk read response: %w", err)
	}

	result := make([]VariableValue, 0, len(decoded))

	for name, item := range decoded {
		value, ok := toFloat64(item.Value)
		if !ok {
			continue
		}

		timestamp, _ := parsePCVueTime(item.Timestamp)

		result = append(result, VariableValue{
			Name:      name,
			Value:     value,
			Quality:   item.QualityValue,
			Timestamp: timestamp,
		})
	}

	return result, nil
}

func (c *Client) ReadActiveAlarms(ctx context.Context) ([]Alarm, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/RealTimeAlarm/v2/Values",
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create alarm values request: %w", err)
	}

	if err := c.addAuthHeaders(req); err != nil {
		return nil, err
	}

	query := req.URL.Query()

	// À ajuster selon les propriétés d’alarme réellement voulues dans la doc PCVue.
	query.Set("Language", "1")
	query.Set("Size", "500")
	query.Set("MinLevel", "0")
	query.Set("MaxLevel", "4")
	query.Set("PriorityFilter", "All")

	query.Add("Events", "AlarmOnNotAck")
	query.Add("Events", "AlarmOnAck")
	query.Add("Events", "AlarmUnavailable")

	// Mapping provisoire basé sur l’exemple de réponse de la doc.
	// À valider avec la section AlarmProperty du manuel PCVue.
	addAlarmProperties(
		query,
		AlarmPropertyVariableName,
		AlarmPropertyAssocLabel,
		AlarmPropertyAlarmLevel,
		AlarmPropertyDescription,
	)

	req.URL.RawQuery = query.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute alarm values request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pcvue alarm values read failed: status=%d", resp.StatusCode)
	}

	var decoded struct {
		Values []struct {
			RemovedFromList bool     `json:"removedFromList"`
			Timestamp       string   `json:"timestamp"`
			EventCode       string   `json:"eventCode"`
			Properties      []string `json:"properties"`
			EventValue      int      `json:"eventValue"`
		} `json:"values"`
		MaxNumberExceeded bool `json:"maxNumberExceeded"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode alarm values response: %w", err)
	}

	alarms := make([]Alarm, 0, len(decoded.Values))

	for _, item := range decoded.Values {
		if item.RemovedFromList {
			continue
		}

		name := propertyAt(item.Properties, 0)        // VariableName
		assocLabel := propertyAt(item.Properties, 1)  // AssocLabel
		alarmLevel := propertyAt(item.Properties, 2)  // AlarmLevel
		description := propertyAt(item.Properties, 3) // Description

		timestamp, _ := parsePCVueTime(item.Timestamp)

		alarms = append(alarms, Alarm{
			Name:         name,
			ID:           sanitizeAlarmID(name),
			Severity:     normalizeLabel(alarmLevel),
			AssocLabel:   assocLabel,
			Description:  description,
			Active:       isActiveAlarmEvent(item.EventCode),
			Acknowledged: isAcknowledgedAlarmEvent(item.EventCode),
			Timestamp:    timestamp,
			EventCode:    item.EventCode,
			EventValue:   item.EventValue,
		})
	}

	return alarms, nil
}

func (c *Client) addAuthHeaders(req *http.Request) error {
	token, err := c.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("get oauth2 token: %w", err)
	}

	token.SetAuthHeader(req)

	return nil
}

func propertyAt(properties []string, index int) string {
	if index < 0 || index >= len(properties) {
		return ""
	}

	return properties[index]
}

func toFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	case bool:
		if v {
			return 1, true
		}

		return 0, true
	default:
		return 0, false
	}
}

func parsePCVueTime(value string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05.999",
		"2006-01-02T15:04:05.9999999",
		"2006-01-02T15:04:05",
	}

	var lastErr error

	for _, layout := range layouts {
		timestamp, err := time.Parse(layout, value)
		if err == nil {
			return timestamp, nil
		}

		lastErr = err
	}

	return time.Time{}, lastErr
}

func sanitizeAlarmID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ToUpper(value)
	value = strings.ReplaceAll(value, ".", "_")
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")

	if value == "" {
		return "UNKNOWN"
	}

	return value
}

func normalizeLabel(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, " ", "_")

	if value == "" {
		return "unknown"
	}

	return value
}

func isActiveAlarmEvent(eventCode string) bool {
	switch eventCode {
	case "AlarmOnNotAck", "AlarmOnAck", "AlarmUnavailable":
		return true
	default:
		return false
	}
}

func isAcknowledgedAlarmEvent(eventCode string) bool {
	switch eventCode {
	case "AlarmOnAck":
		return true
	default:
		return false
	}
}
