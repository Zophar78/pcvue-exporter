package pcvue

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		tokenSource: oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		}),
	}

	_, err := client.ReadActiveAlarms(t.Context())
	if err == nil || !strings.Contains(err.Error(), "refusing partial results") {
		t.Fatalf("expected truncated response error, got %v", err)
	}
}

func TestReadVariablesTimestamp(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		wantError string
	}{
		{
			name:     "missing timestamp",
			response: `{"PUMP":{"value":12}}`,
		},
		{
			name:      "malformed timestamp",
			response:  `{"PUMP":{"value":12,"Timestamp":"invalid"}}`,
			wantError: `parse timestamp for variable "PUMP"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, test.response)
			values, err := client.ReadVariables(t.Context(), []string{"PUMP"})

			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("ReadVariables() error = %v, want error containing %q", err, test.wantError)
				}
				return
			}

			if err != nil {
				t.Fatalf("ReadVariables() error = %v", err)
			}
			if len(values) != 1 || !values[0].Timestamp.IsZero() {
				t.Fatalf("ReadVariables() = %+v, want one value with zero timestamp", values)
			}
		})
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
		tokenSource: oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		}),
	}

	if _, err := client.ReadVariables(t.Context(), []string{"PUMP"}); err != nil {
		t.Fatalf("ReadVariables() error = %v", err)
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
		tokenSource: oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		}),
	}
}
