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
