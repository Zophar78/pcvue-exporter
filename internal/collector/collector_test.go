package collector

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/Zophar78/pcvue-exporter/internal/config"
	"github.com/Zophar78/pcvue-exporter/internal/pcvue"
)

func TestWhitelistedAlarmMetricsUseConfiguredArea(t *testing.T) {
	cfg := &config.Config{
		Alarms: config.AlarmConfig{
			Whitelist: []config.AlarmFilter{{
				ID:        "UPS_BATTERY_FAULT",
				PCVueName: "POWER.UPS_BATTERY_FAULT",
				Severity:  "critical",
				Area:      "power",
			}},
		},
	}
	c := New(cfg, nil, slog.Default())
	metrics := make(chan prometheus.Metric, 3)

	c.collectWhitelistedAlarms(metrics, nil)

	for range 3 {
		metric := <-metrics
		labels := metricLabelMap(t, metric)
		if labels["area"] != "power" {
			t.Errorf("expected area label power, got %q", labels["area"])
		}
	}
}

func TestAlarmAggregateMetricsUseAssocLabel(t *testing.T) {
	c := New(&config.Config{}, nil, slog.Default())
	metrics := make(chan prometheus.Metric, 3)

	c.collectAlarmAggregates(metrics, []pcvue.Alarm{{
		Severity:   "critical",
		AssocLabel: "plant-a",
		Active:     true,
	}})

	for range 2 {
		labels := metricLabelMap(t, <-metrics)
		if labels["assoc_label"] != "plant-a" {
			t.Errorf("expected assoc_label plant-a, got %q", labels["assoc_label"])
		}
		if _, exists := labels["area"]; exists {
			t.Error("aggregate metric should not expose assoc_label as area")
		}
	}
}

func TestCollectVariablesEmitsValidValuesFromPartialRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
		case "/RealTimeData/v2/BulkRead":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `[{"Description":"PUMP","value":12},{"Description":"BROKEN","result":{"code":{"value":5,"label":"E_Failed"}}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pcvueClient, err := pcvue.NewClient(config.PCVueConfig{
		BaseURL: server.URL,
		Timeout: time.Second,
		Auth: config.AuthConfig{
			Type:         "oauth2_client_credentials",
			TokenURL:     server.URL + "/token",
			ClientID:     "test-client",
			ClientSecret: "test-secret",
		},
	}, slog.Default())
	if err != nil {
		t.Fatalf("create PCVue client: %v", err)
	}

	cfg := &config.Config{
		PCVue: config.PCVueConfig{Timeout: time.Second},
		Variables: []config.VariableConfig{
			{Name: "PUMP", Area: "plant-a"},
			{Name: "BROKEN", Area: "plant-a"},
		},
	}
	collector := New(cfg, pcvueClient, slog.Default())
	metrics := make(chan prometheus.Metric, len(cfg.Variables))

	err = collector.collectVariables(t.Context(), metrics)
	if err == nil {
		t.Fatal("collectVariables() error = nil, want partial-read error")
	}
	if len(metrics) != 1 {
		t.Fatalf("collectVariables() emitted %d metrics, want valid metric only", len(metrics))
	}

	metric := <-metrics
	var dtoMetric dto.Metric
	if err := metric.Write(&dtoMetric); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	if got := dtoMetric.GetGauge().GetValue(); got != 12 {
		t.Fatalf("emitted variable value = %v, want 12", got)
	}
	if labels := metricLabelMap(t, metric); labels["name"] != "PUMP" || labels["area"] != "plant-a" {
		t.Fatalf("emitted metric labels = %v, want name=PUMP and area=plant-a", labels)
	} else if _, exists := labels["alias"]; exists {
		t.Fatalf("emitted metric labels = %v, alias label should be absent", labels)
	}
}

func metricLabelMap(t *testing.T, metric prometheus.Metric) map[string]string {
	t.Helper()

	var dtoMetric dto.Metric
	if err := metric.Write(&dtoMetric); err != nil {
		t.Fatalf("write metric: %v", err)
	}

	labels := make(map[string]string, len(dtoMetric.GetLabel()))
	for _, label := range dtoMetric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}

	return labels
}
