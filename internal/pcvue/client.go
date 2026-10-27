package pcvue

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Zophar78/pcvue-exporter/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	authTypeOAuth2ClientCredentials = "oauth2_client_credentials"
	authTypeOAuth2Password          = "oauth2_password"
	alarmReadSize                   = 500
)

type Client struct {
	baseURL string
	authType string

	httpClient  *http.Client
	tokenSource *cachedTokenSource
	logger      *slog.Logger
}

// NewClient creates a PCVue API client and validates its OAuth2 configuration.
func NewClient(cfg config.PCVueConfig, logger *slog.Logger) (*Client, error) {
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

	tokenSource, err := newTokenSource(cfg, httpClient, logger)
	if err != nil {
		return nil, err
	}

	return &Client{
		baseURL:     baseURL,
		authType:    cfg.Auth.Type,
		httpClient:  httpClient,
		tokenSource: tokenSource,
		logger:      logger,
	}, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	started := time.Now()
	if c.logger != nil {
		c.logger.DebugContext(req.Context(), "sending PCVue API request",
			"method", req.Method,
			"path", req.URL.Path,
		)
	}

	resp, err := c.httpClient.Do(req)
	duration := time.Since(started).String()
	if c.logger != nil {
		attributes := []any{
			"method", req.Method,
			"path", req.URL.Path,
			"duration", duration,
		}
		if err != nil {
			attributes = append(attributes, "error", err)
			c.logger.DebugContext(req.Context(), "PCVue API request failed", attributes...)
		} else {
			attributes = append(attributes, "status", resp.StatusCode)
			c.logger.DebugContext(req.Context(), "PCVue API response received", attributes...)
		}
	}

	return resp, err
}

// Logout closes the PCVue session for password-grant authentication.
func (c *Client) Logout(ctx context.Context) error {
	if c.authType != authTypeOAuth2Password {
		return nil
	}

	accessToken := c.tokenSource.cachedAccessToken()
	if accessToken == "" {
		return nil
	}

	url := c.baseURL + "/OAuth/Account/logout"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create PCVue logout request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("execute PCVue logout request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("PCVue logout failed: status=%d", resp.StatusCode)
	}

	return nil
}

func newTokenSource(cfg config.PCVueConfig, httpClient *http.Client, logger *slog.Logger) (*cachedTokenSource, error) {
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

		return newCachedTokenSource(&clientCredentialsTokenSource{
			ctx:    ctx,
			config: oauthConfig,
		}, logger), nil

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

		return newCachedTokenSource(&passwordTokenSource{
			ctx:        ctx,
			config:     &oauthConfig,
			username:   cfg.Auth.Username,
			password:   cfg.Auth.Password,
			httpClient: httpClient,
		}, logger), nil

	default:
		return nil, fmt.Errorf("unsupported pcvue.auth.type: %q", cfg.Auth.Type)
	}
}

func newCachedTokenSource(source oauth2.TokenSource, logger *slog.Logger) *cachedTokenSource {
	return &cachedTokenSource{
		source: source,
		logger: logger,
	}
}

type cachedTokenSource struct {
	mu     sync.Mutex
	source oauth2.TokenSource
	logger *slog.Logger
	token  *oauth2.Token
}

func (s *cachedTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != nil && s.token.Valid() {
		return s.token, nil
	}

	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	s.token = token

	if s.logger != nil {
		attributes := []any{
			"token_type", token.TokenType,
			"expiry_known", !token.Expiry.IsZero(),
		}
		if !token.Expiry.IsZero() {
			attributes = append(attributes, "expires_in", time.Until(token.Expiry).Round(time.Second).String())
		}
		s.logger.Debug("PCVue OAuth token acquired", attributes...)
	}

	return token, nil
}

func (s *cachedTokenSource) invalidate(token *oauth2.Token) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != nil && token != nil && s.token.AccessToken == token.AccessToken {
		s.token = nil
	}
}

func (s *cachedTokenSource) cachedAccessToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token == nil {
		return ""
	}

	return s.token.AccessToken
}

type clientCredentialsTokenSource struct {
	ctx    context.Context
	config clientcredentials.Config
}

func (s *clientCredentialsTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.config.Token(s.ctx)
	if err != nil {
		return nil, fmt.Errorf("get oauth2 client credentials token: %w", err)
	}

	return token, nil
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

// ReadVariables retrieves the named variables. An empty names slice returns no values
// without making an API request. It returns an error if the request fails, the response
// is malformed or does not contain a numeric value.
func (c *Client) ReadVariables(ctx context.Context, names []string) ([]VariableValue, error) {
	if len(names) == 0 {
		return nil, nil
	}

	apiNames := make([]string, len(names))
	configuredNameByAPIName := make(map[string]string, len(names))
	for index, name := range names {
		apiName := strings.ReplaceAll(name, "/", ".")
		apiNames[index] = apiName
		configuredNameByAPIName[apiName] = name
	}

	payload := map[string]any{
		"Variables": apiNames,
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

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.doAuthenticated(req)
	if err != nil {
		return nil, fmt.Errorf("execute variable bulk read request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pcvue variable bulk read failed: status=%d", resp.StatusCode)
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read variable bulk read response: %w", err)
	}

	values, err := decodeVariableBulkReadResponse(responseBody)
	for index := range values {
		if configuredName, ok := configuredNameByAPIName[values[index].Name]; ok {
			values[index].Name = configuredName
		}
	}
	c.logVariableValues(ctx, values)
	if err != nil {
		return values, fmt.Errorf("decode variable bulk read response (%s): %w", describePCVueJSONShape(responseBody), err)
	}

	return values, nil
}

func (c *Client) logVariableValues(ctx context.Context, values []VariableValue) {
	if c.logger == nil || !c.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}

	const maxLoggedVariableValues = 50
	loggedValues := min(len(values), maxLoggedVariableValues)
	for _, value := range values[:loggedValues] {
		c.logger.DebugContext(ctx, "PCVue variable value received",
			"name", value.Name,
			"value", value.Value,
		)
	}
	if omittedValues := len(values) - loggedValues; omittedValues > 0 {
		c.logger.DebugContext(ctx, "PCVue variable value logs truncated", "omitted", omittedValues)
	}
}

func decodeVariableBulkReadResponse(body []byte) ([]VariableValue, error) {
	bom := []byte{0xef, 0xbb, 0xbf}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, bom))
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var decoded []struct {
			Description string `json:"Description"`
			Value       any    `json:"value"`
		}
		if err := decodePCVueJSON(bytes.NewReader(body), &decoded); err != nil {
			return nil, err
		}

		result := make([]VariableValue, 0, len(decoded))
		var itemErrors []error
		for index, item := range decoded {
			if item.Description == "" {
				itemErrors = append(itemErrors, fmt.Errorf("array item %d is missing Description", index))
				continue
			}

			value, ok := toFloat64(item.Value)
			if !ok {
				itemErrors = append(itemErrors, fmt.Errorf("array item %d for variable %q has no numeric value", index, item.Description))
				continue
			}

			result = append(result, VariableValue{
				Name:  item.Description,
				Value: value,
			})
		}

		return result, errors.Join(itemErrors...)
	}

	var decoded map[string]struct {
		Value any `json:"value"`
	}
	if err := decodePCVueJSON(bytes.NewReader(body), &decoded); err != nil {
		return nil, err
	}

	result := make([]VariableValue, 0, len(decoded))
	var itemErrors []error

	names := make([]string, 0, len(decoded))
	for name := range decoded {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		item := decoded[name]
		value, ok := toFloat64(item.Value)
		if !ok {
			itemErrors = append(itemErrors, fmt.Errorf("variable %q has no numeric value", name))
			continue
		}

		result = append(result, VariableValue{
			Name:  name,
			Value: value,
		})
	}

	return result, errors.Join(itemErrors...)
}

// ReadActiveAlarms retrieves active alarm events. It returns an error if the request fails,
// the API response is truncated, or a non-empty timestamp cannot be parsed.
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

	query := req.URL.Query()

	// À ajuster selon les propriétés d’alarme réellement voulues dans la doc PCVue.
	query.Set("Language", "1")
	query.Set("Size", strconv.Itoa(alarmReadSize))
	query.Set("MinLevel", "0")
	query.Set("MaxLevel", "4")
	query.Set("PriorityFilter", "All")

	for index, event := range []string{"AlarmOnNotAck", "AlarmOnAck", "AlarmUnavailable"} {
		query.Set(fmt.Sprintf("Events[%d]", index), event)
	}

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

	resp, err := c.doAuthenticated(req)
	if err != nil {
		return nil, fmt.Errorf("execute alarm values request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

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

	if err := decodePCVueJSON(resp.Body, &decoded); err != nil {
		return nil, fmt.Errorf("decode alarm values response: %w", err)
	}
	if decoded.MaxNumberExceeded {
		return nil, fmt.Errorf(
			"pcvue alarm values response exceeded the maximum size of %d; refusing partial results",
			alarmReadSize,
		)
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

		timestamp, err := parsePCVueTime(item.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp for alarm %q: %w", name, err)
		}

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

	if c.logger != nil {
		c.logger.DebugContext(ctx, "PCVue alarms decoded", "count", len(alarms))
	}

	return alarms, nil
}

func (c *Client) doAuthenticated(req *http.Request) (*http.Response, error) {
	token, err := c.addAuthHeaders(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}

	c.tokenSource.invalidate(token)
	if c.logger != nil {
		c.logger.DebugContext(req.Context(), "PCVue API rejected OAuth token; refreshing and retrying once",
			"method", req.Method,
			"path", req.URL.Path,
		)
	}

	retryReq := req.Clone(req.Context())
	if req.Body != nil {
		if req.GetBody == nil {
			return nil, fmt.Errorf("cannot retry unauthorized %s request with a non-replayable body", req.Method)
		}
		retryReq.Body, err = req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("recreate request body for OAuth retry: %w", err)
		}
	}

	if _, err := c.addAuthHeaders(retryReq); err != nil {
		return nil, fmt.Errorf("refresh OAuth token after unauthorized response: %w", err)
	}

	return c.do(retryReq)
}

func (c *Client) addAuthHeaders(req *http.Request) (*oauth2.Token, error) {
	token, err := c.tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("get oauth2 token: %w", err)
	}

	token.SetAuthHeader(req)

	return token, nil
}

func decodePCVueJSON(body io.Reader, target any) error {
	reader := bufio.NewReader(body)
	bom := []byte{0xef, 0xbb, 0xbf}
	if prefix, err := reader.Peek(len(bom)); err == nil && bytes.Equal(prefix, bom) {
		if _, err := reader.Discard(len(bom)); err != nil {
			return err
		}
	}

	return json.NewDecoder(reader).Decode(target)
}

func describePCVueJSONShape(body []byte) string {
	bom := []byte{0xef, 0xbb, 0xbf}
	var root any
	if err := json.Unmarshal(bytes.TrimSpace(bytes.TrimPrefix(body, bom)), &root); err != nil {
		return "JSON shape unavailable"
	}

	switch value := root.(type) {
	case []any:
		if len(value) == 0 {
			return "JSON root=array, items=0"
		}
		firstItem, ok := value[0].(map[string]any)
		if !ok {
			return fmt.Sprintf("JSON root=array, items=%d, first item type=%T", len(value), value[0])
		}

		fields := make([]string, 0, len(firstItem))
		for field := range firstItem {
			fields = append(fields, field)
		}
		sort.Strings(fields)

		details := []string{
			fmt.Sprintf("JSON root=array, items=%d, first item fields=%v", len(value), fields),
		}
		if code, ok := firstItem["code"]; ok {
			details = append(details, fmt.Sprintf("first item code shape=%s", describePCVueJSONValue(code, 0)))
		}
		if description, ok := firstItem["Description"]; ok {
			details = append(details, fmt.Sprintf("first item Description type=%s", describePCVueJSONValue(description, 0)))
		}

		return strings.Join(details, ", ")
	case map[string]any:
		return fmt.Sprintf("JSON root=object, properties=%d", len(value))
	default:
		return fmt.Sprintf("JSON root type=%T", root)
	}
}

func describePCVueJSONValue(value any, depth int) string {
	switch typed := value.(type) {
	case map[string]any:
		if depth >= 2 || len(typed) == 0 {
			return "object"
		}

		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		const maxFields = 8
		limit := min(len(keys), maxFields)
		fields := make([]string, 0, limit+1)
		for _, key := range keys[:limit] {
			fields = append(fields, fmt.Sprintf("%s:%s", key, describePCVueJSONValue(typed[key], depth+1)))
		}
		if len(keys) > maxFields {
			fields = append(fields, fmt.Sprintf("...+%d fields", len(keys)-maxFields))
		}
		return fmt.Sprintf("object{%s}", strings.Join(fields, ","))
	case []any:
		return fmt.Sprintf("array(len=%d)", len(typed))
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("<%T>", value)
	}
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
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}

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
