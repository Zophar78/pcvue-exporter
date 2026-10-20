package collector

import (
	"log/slog"
	"testing"

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
