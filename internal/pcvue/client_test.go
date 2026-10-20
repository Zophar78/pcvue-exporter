package pcvue

import (
	"context"
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

	_, err := client.ReadActiveAlarms(context.Background())
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
			values, err := client.ReadVariables(context.Background(), []string{"PUMP"})

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
			alarms, err := client.ReadActiveAlarms(context.Background())

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
