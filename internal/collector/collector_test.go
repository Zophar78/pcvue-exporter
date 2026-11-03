package collector

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/Zophar78/pcvue-exporter/internal/config"
	"github.com/Zophar78/pcvue-exporter/internal/pcvue"
)

func TestCollectAccumulatesScrapeErrors(t *testing.T) {
	var failVariables, failAlarms atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = io.WriteString(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
		case "/RealTimeData/v2/BulkRead":
			if failVariables.Load() {
				_, _ = io.WriteString(w, `{"PUMP":{"value":null},"FAN":{"value":null}}`)
			} else {
				_, _ = io.WriteString(w, `{"PUMP":{"value":12},"FAN":{"value":42}}`)
			}
		case "/RealTimeAlarm/v2/Values":
			if failAlarms.Load() {
				w.WriteHeader(http.StatusInternalServerError)
			} else {
				_, _ = io.WriteString(w, `{"values":[]}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		PCVue: config.PCVueConfig{
			BaseURL: server.URL,
			Timeout: 5 * time.Second,
			Auth: config.AuthConfig{
				Type:         "oauth2_client_credentials",
				TokenURL:     server.URL + "/token",
				ClientID:     "test-client",
				ClientSecret: "test-secret",
			},
		},
		Variables: []config.VariableConfig{{Name: "PUMP"}, {Name: "FAN"}},
		Labels:    map[string]string{"site": "test"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := pcvue.NewClient(cfg.PCVue, logger)
	if err != nil {
		t.Fatalf("create PCVue client: %v", err)
	}
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(New(cfg, client, logger))

	checkMetrics := func(t *testing.T, wantVariables, wantAlarms, wantUp float64) {
		t.Helper()
		families, err := registry.Gather()
		if err != nil {
			t.Fatalf("gather metrics: %v", err)
		}
		counts := map[string]float64{}
		var up *float64
		for _, family := range families {
			switch family.GetName() {
			case "pcvue_scrape_errors_total":
				if family.GetType() != dto.MetricType_COUNTER {
					t.Errorf("scrape errors metric type = %v, want COUNTER", family.GetType())
				}
				for _, metric := range family.GetMetric() {
					labels := map[string]string{}
					for _, label := range metric.GetLabel() {
						labels[label.GetName()] = label.GetValue()
					}
					if labels["site"] != "test" {
						t.Errorf("counter labels = %v, want site=test", labels)
					}
					counts[labels["type"]] = metric.GetCounter().GetValue()
				}
			case "pcvue_up":
				up = family.GetMetric()[0].GetGauge().Value
			}
		}
		for kind, want := range map[string]float64{"variables": wantVariables, "alarms": wantAlarms} {
			if got, exists := counts[kind]; !exists || got != want {
				t.Errorf("scrape errors for %s = %v (present=%v), want %v", kind, got, exists, want)
			}
		}
		if up == nil || *up != wantUp {
			t.Errorf("pcvue_up = %v, want %v", up, wantUp)
		}
	}

	for _, test := range []struct {
		name                      string
		failVariables, failAlarms bool
		wantVariables, wantAlarms float64
		wantUp                    float64
	}{
		{name: "initial success", wantUp: 1},
		{name: "first variable failure", failVariables: true, wantVariables: 1},
		{name: "repeated variable failure", failVariables: true, wantVariables: 2},
		{name: "both reads fail", failVariables: true, failAlarms: true, wantVariables: 3, wantAlarms: 1},
		{name: "only alarms fail", failAlarms: true, wantVariables: 3, wantAlarms: 2},
		{name: "recovery preserves counters", wantVariables: 3, wantAlarms: 2, wantUp: 1},
		{name: "continued success", wantVariables: 3, wantAlarms: 2, wantUp: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			failVariables.Store(test.failVariables)
			failAlarms.Store(test.failAlarms)
			checkMetrics(t, test.wantVariables, test.wantAlarms, test.wantUp)
		})
	}

	t.Run("concurrent scrapes preserve increments", func(t *testing.T) {
		failVariables.Store(true)
		failAlarms.Store(true)
		const scrapes = 5
		var wg sync.WaitGroup
		for range scrapes {
			wg.Go(func() {
				if _, err := registry.Gather(); err != nil {
					t.Errorf("concurrent gather: %v", err)
				}
			})
		}
		wg.Wait()
		failVariables.Store(false)
		failAlarms.Store(false)
		checkMetrics(t, 3+scrapes, 2+scrapes, 1)
	})

	t.Run("new collector resets counters", func(t *testing.T) {
		registry = prometheus.NewPedanticRegistry()
		registry.MustRegister(New(cfg, client, logger))
		checkMetrics(t, 0, 0, 1)
	})
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

func TestCollectExposesOnlyAlarmAggregates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = io.WriteString(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
		case "/RealTimeAlarm/v2/Values":
			_, _ = io.WriteString(w, `{"values":[
				{"eventCode":"AlarmOnAck","properties":["PUMP.ALARM","plant-a","3","Pump fault"]},
				{"eventCode":"AlarmOnNotAck","properties":["FAN.ALARM","plant-a","3","Fan fault"]}
			]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		PCVue: config.PCVueConfig{
			BaseURL: server.URL,
			Timeout: time.Second,
			Auth: config.AuthConfig{
				Type:         "oauth2_client_credentials",
				TokenURL:     server.URL + "/token",
				ClientID:     "test-client",
				ClientSecret: "test-secret",
			},
		},
		Alarms: config.AlarmConfig{Aggregate: true},
	}
	client, err := pcvue.NewClient(cfg.PCVue, slog.Default())
	if err != nil {
		t.Fatalf("create PCVue client: %v", err)
	}
	c := New(cfg, client, slog.Default())
	descriptors := make(chan *prometheus.Desc, 10)
	c.Describe(descriptors)
	close(descriptors)
	for desc := range descriptors {
		for _, removed := range []string{"pcvue_alarm_active", "pcvue_alarm_unack", "pcvue_alarm_age_seconds"} {
			if strings.Contains(desc.String(), `fqName: "`+removed+`"`) {
				t.Errorf("removed alarm metric is still described: %s", desc)
			}
		}
	}

	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(c)
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	wantValues := map[string]float64{
		"pcvue_alarm_active_total":              2,
		"pcvue_alarm_unack_total":               1,
		"pcvue_alarm_oldest_active_age_seconds": 0,
	}
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "pcvue_alarm_") {
			continue
		}
		want, exists := wantValues[family.GetName()]
		if !exists {
			t.Errorf("unexpected alarm metric %s", family.GetName())
			continue
		}
		delete(wantValues, family.GetName())
		if family.GetType() != dto.MetricType_GAUGE || len(family.GetMetric()) != 1 {
			t.Errorf("%s should contain one gauge", family.GetName())
			continue
		}
		metric := family.GetMetric()[0]
		if got := metric.GetGauge().GetValue(); got != want {
			t.Errorf("%s = %v, want %v", family.GetName(), got, want)
		}
		labels := map[string]string{}
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if family.GetName() == "pcvue_alarm_oldest_active_age_seconds" {
			if len(labels) != 0 {
				t.Errorf("oldest age labels = %v, want none", labels)
			}
		} else if len(labels) != 2 || labels["severity"] != "3" || labels["assoc_label"] != "plant-a" {
			t.Errorf("aggregate labels = %v, want severity=3 and assoc_label=plant-a", labels)
		}
	}
	if len(wantValues) != 0 {
		t.Errorf("missing alarm metrics: %v", wantValues)
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
