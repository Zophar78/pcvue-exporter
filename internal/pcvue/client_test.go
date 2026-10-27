package pcvue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Zophar78/pcvue-exporter/internal/config"
	"golang.org/x/oauth2"
)

func TestReadActiveAlarmsRejectsTruncatedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/RealTimeAlarm/v2/Values" {
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}

		if got := r.URL.Query().Get("Size"); got != "500" {
			t.Errorf("unexpected alarm response size: %s", got)
		}
		query := r.URL.Query()
		for index, want := range []string{"AlarmOnNotAck", "AlarmOnAck", "AlarmUnavailable"} {
			if got := query.Get(fmt.Sprintf("Events[%d]", index)); got != want {
				t.Errorf("Events[%d] = %q, want %q", index, got, want)
			}
		}
		for index, want := range []string{"0", "1", "5", "9"} {
			if got := query.Get(fmt.Sprintf("Properties[%d]", index)); got != want {
				t.Errorf("Properties[%d] = %q, want %q", index, got, want)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"values":[],"maxNumberExceeded":true}`)
	}))
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
		tokenSource: newCachedTokenSource(oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		}), nil),
	}

	_, err := client.ReadActiveAlarms(t.Context())
	if err == nil || !strings.Contains(err.Error(), "refusing partial results") {
		t.Fatalf("expected truncated response error, got %v", err)
	}
}

func TestPasswordTokenSourceLogsExpiryWithoutExposingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"sensitive-test-token","token_type":"Bearer"}`)
	}))
	defer server.Close()

	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tokenSource, err := newTokenSource(config.PCVueConfig{
		Auth: config.AuthConfig{
			Type:         authTypeOAuth2Password,
			TokenURL:     server.URL,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			Username:     "test-user",
			Password:     "test-password",
		},
	}, server.Client(), logger)
	if err != nil {
		t.Fatalf("newTokenSource() error = %v", err)
	}

	if _, err := tokenSource.Token(); err != nil {
		t.Fatalf("Token() error = %v", err)
	}

	logs := logOutput.String()
	if !strings.Contains(logs, "expiry_known=false") {
		t.Fatalf("token debug log = %q, want expiry_known=false", logs)
	}
	if strings.Contains(logs, "sensitive-test-token") || strings.Contains(logs, "test-secret") || strings.Contains(logs, "test-password") {
		t.Fatalf("token debug log exposes credentials: %q", logs)
	}
}

func TestReadVariablesRefreshesTokenAndRetriesOnceAfterUnauthorized(t *testing.T) {
	tokenRequests := 0
	bulkReadRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenRequests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer","expires_in":3600}`, tokenRequests)
		case "/RealTimeData/v2/BulkRead":
			bulkReadRequests++
			var request struct {
				Variables []string
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode BulkRead request: %v", err)
			}
			if len(request.Variables) != 1 || request.Variables[0] != "PUMP" {
				t.Errorf("BulkRead variables = %v, want [PUMP]", request.Variables)
			}
			wantToken := fmt.Sprintf("Bearer token-%d", bulkReadRequests)
			if got := r.Header.Get("Authorization"); got != wantToken {
				t.Errorf("Authorization = %q, want %q", got, wantToken)
			}
			if bulkReadRequests == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"PUMP":{"value":12}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(config.PCVueConfig{
		BaseURL: server.URL,
		Timeout: time.Second,
		Auth: config.AuthConfig{
			Type:         authTypeOAuth2Password,
			TokenURL:     server.URL + "/token",
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			Username:     "test-user",
			Password:     "test-password",
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	values, err := client.ReadVariables(t.Context(), []string{"PUMP"})
	if err != nil {
		t.Fatalf("ReadVariables() error = %v", err)
	}
	if len(values) != 1 || values[0].Name != "PUMP" || values[0].Value != 12 {
		t.Fatalf("ReadVariables() = %+v, want PUMP=12", values)
	}
	if tokenRequests != 2 || bulkReadRequests != 2 {
		t.Fatalf("token requests=%d, BulkRead requests=%d, want 2 of each", tokenRequests, bulkReadRequests)
	}
}

func TestReadVariablesDoesNotRetryUnauthorizedTwice(t *testing.T) {
	bulkReadRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/RealTimeData/v2/BulkRead" {
			http.NotFound(w, r)
			return
		}
		bulkReadRequests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
		tokenSource: newCachedTokenSource(oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		}), nil),
	}

	if _, err := client.ReadVariables(t.Context(), []string{"PUMP"}); err == nil {
		t.Fatal("ReadVariables() error = nil, want unauthorized response error")
	}
	if bulkReadRequests != 2 {
		t.Fatalf("BulkRead requests = %d, want exactly 2", bulkReadRequests)
	}
}

func TestReadVariablesAcceptsUTF8BOM(t *testing.T) {
	client := newTestClient(t, "\uFEFF"+`{"PUMP":{"value":12}}`)

	values, err := client.ReadVariables(t.Context(), []string{"PUMP"})
	if err != nil {
		t.Fatalf("ReadVariables() error = %v", err)
	}
	if len(values) != 1 || values[0].Value != 12 {
		t.Fatalf("ReadVariables() = %+v, want one value of 12", values)
	}
}

func TestReadVariablesDecodesArrayResponse(t *testing.T) {
	response := `[{"result":{"code":{"value":1,"label":"S_Ok"},"Description":null},"value":21.5,"Timestamp":"2026-10-26T09:43:35.389","quality":"Good","QualityValue":192,"Description":"PUMP"},{"result":{"code":{"value":1,"label":"S_Ok"},"Description":null},"value":42,"Description":"FAN"}]`
	client := newTestClient(t, response)

	values, err := client.ReadVariables(t.Context(), []string{"PUMP", "FAN"})
	if err != nil {
		t.Fatalf("ReadVariables() error = %v", err)
	}
	if len(values) != 2 {
		t.Fatalf("ReadVariables() returned %d values, want 2", len(values))
	}
	if values[0].Name != "PUMP" || values[0].Value != 21.5 {
		t.Errorf("values[0] = %+v, want PUMP=21.5", values[0])
	}
	if values[1].Name != "FAN" || values[1].Value != 42 {
		t.Errorf("values[1] = %+v, want FAN=42", values[1])
	}
}

func TestReadVariablesUsesTopLevelValueInsteadOfResultCodeValue(t *testing.T) {
	response := `{"PUMP":{"result":{"code":{"value":1,"label":"S_Ok"},"Description":null},"value":42.919998168945313,"Timestamp":"2026-10-26T14:15:59.359","quality":"Good","QualityValue":192}}`
	client := newTestClient(t, response)

	values, err := client.ReadVariables(t.Context(), []string{"PUMP"})
	if err != nil {
		t.Fatalf("ReadVariables() error = %v", err)
	}
	if len(values) != 1 || values[0].Value != 42.919998168945313 {
		t.Fatalf("ReadVariables() = %+v, want PUMP value 42.919998168945313", values)
	}
}

func TestReadVariablesLogsDecodedValuesAtDebugWithoutAuthToken(t *testing.T) {
	client := newTestClient(t, `[{"result":{"code":{"value":1,"label":"S_Ok"}},"value":42.4,"Description":"PUMP"}]`)
	var logOutput bytes.Buffer
	client.logger = slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if _, err := client.ReadVariables(t.Context(), []string{"PUMP"}); err != nil {
		t.Fatalf("ReadVariables() error = %v", err)
	}

	logs := logOutput.String()
	for _, expected := range []string{
		"sending PCVue API request",
		"path=/RealTimeData/v2/BulkRead",
		"PCVue API response received",
		"status=200",
		"PCVue variable value received",
		"name=PUMP",
		"value=42.4",
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("debug logs do not contain %q: %s", expected, logs)
		}
	}
	if strings.Contains(logs, "test-token") {
		t.Fatalf("debug logs expose OAuth token: %s", logs)
	}
}

func TestReadVariablesReturnsValidValuesAndErrorsForPartialResponses(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		invalidName string
	}{
		{
			name:        "array response",
			response:    `[{"Description":"PUMP","value":12},{"Description":"BROKEN","result":{"code":{"value":5,"label":"E_Failed"}}}]`,
			invalidName: "BROKEN",
		},
		{
			name:        "object response",
			response:    `{"PUMP":{"value":12},"BROKEN":{"value":null}}`,
			invalidName: "BROKEN",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, test.response)
			values, err := client.ReadVariables(t.Context(), []string{"PUMP", test.invalidName})
			if err == nil || !strings.Contains(err.Error(), test.invalidName) {
				t.Fatalf("ReadVariables() error = %v, want error for variable %q", err, test.invalidName)
			}
			if len(values) != 1 || values[0].Name != "PUMP" || values[0].Value != 12 {
				t.Fatalf("ReadVariables() = %+v, want valid PUMP=12 alongside the error", values)
			}
		})
	}
}

func TestReadVariablesReportsArrayResponseDetailsWithoutRawBody(t *testing.T) {
	client := newTestClient(t, `true`)

	_, err := client.ReadVariables(t.Context(), []string{"PUMP"})
	if err == nil {
		t.Fatal("ReadVariables() error = nil, want response shape error")
	}
	if !strings.Contains(err.Error(), "JSON root type=bool") {
		t.Fatalf("ReadVariables() error = %v, want root type in error", err)
	}
}

func TestReadVariablesSendsNamedProperties(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables  []string
			Properties []string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.Variables) != 1 || request.Variables[0] != "PUMP" {
			t.Errorf("Variables = %v, want [PUMP]", request.Variables)
		}
		wantProperties := []string{"VariableName", "Description", "Unit"}
		if len(request.Properties) != len(wantProperties) {
			t.Errorf("Properties = %v, want %v", request.Properties, wantProperties)
		} else {
			for index, want := range wantProperties {
				if request.Properties[index] != want {
					t.Errorf("Properties = %v, want %v", request.Properties, wantProperties)
					break
				}
			}
		}

		_, _ = io.WriteString(w, `{"PUMP":{"value":12}}`)
	}))
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
		tokenSource: newCachedTokenSource(oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		}), nil),
	}

	if _, err := client.ReadVariables(t.Context(), []string{"PUMP"}); err != nil {
		t.Fatalf("ReadVariables() error = %v", err)
	}
}

func TestReadVariablesUsesDottedAPINameAndRestoresConfiguredName(t *testing.T) {
	configuredName := "MCR60_01/BUREAU/611/ELS/2652/rHUMI"
	apiName := "MCR60_01.BUREAU.611.ELS.2652.rHUMI"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables []string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.Variables) != 1 || request.Variables[0] != apiName {
			t.Errorf("Variables = %v, want [%s]", request.Variables, apiName)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"MCR60_01.BUREAU.611.ELS.2652.rHUMI":{"value":42.919998168945313,"Timestamp":"2026-10-26T14:15:59.359","quality":"Good","QualityValue":192}}`)
	}))
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
		tokenSource: newCachedTokenSource(oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		}), nil),
	}

	values, err := client.ReadVariables(t.Context(), []string{configuredName})
	if err != nil {
		t.Fatalf("ReadVariables() error = %v", err)
	}
	if len(values) != 1 || values[0].Name != configuredName || values[0].Value != 42.919998168945313 {
		t.Fatalf("ReadVariables() = %+v, want configured name and value 42.919998168945313", values)
	}
}

func TestReadActiveAlarmsTimestamp(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		wantError string
	}{
		{
			name:     "missing timestamp",
			response: `{"values":[{"properties":["PUMP.ALARM","plant-a","critical","description"],"eventCode":"AlarmOnNotAck"}]}`,
		},
		{
			name:      "malformed timestamp",
			response:  `{"values":[{"timestamp":"invalid","properties":["PUMP.ALARM","plant-a","critical","description"],"eventCode":"AlarmOnNotAck"}]}`,
			wantError: `parse timestamp for alarm "PUMP.ALARM"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, test.response)
			alarms, err := client.ReadActiveAlarms(t.Context())

			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("ReadActiveAlarms() error = %v, want error containing %q", err, test.wantError)
				}
				return
			}

			if err != nil {
				t.Fatalf("ReadActiveAlarms() error = %v", err)
			}
			if len(alarms) != 1 || !alarms[0].Timestamp.IsZero() {
				t.Fatalf("ReadActiveAlarms() = %+v, want one alarm with zero timestamp", alarms)
			}
		})
	}
}

func TestReadActiveAlarmsAcceptsUTF8BOM(t *testing.T) {
	response := "\uFEFF" + `{"values":[],"maxNumberExceeded":false}`
	client := newTestClient(t, response)

	alarms, err := client.ReadActiveAlarms(t.Context())
	if err != nil {
		t.Fatalf("ReadActiveAlarms() error = %v", err)
	}
	if len(alarms) != 0 {
		t.Fatalf("ReadActiveAlarms() = %+v, want no alarms", alarms)
	}
}

func newTestClient(t *testing.T, response string) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)

	return &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
		tokenSource: newCachedTokenSource(oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		}), nil),
	}
}
