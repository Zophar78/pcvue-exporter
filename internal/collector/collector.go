// Package collector exposes configured PCVue values and alarm aggregates as Prometheus metrics.
package collector

import (
	"context"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Zophar78/pcvue-exporter/internal/config"
	"github.com/Zophar78/pcvue-exporter/internal/pcvue"
)

// Collector gathers configured PCVue data for Prometheus scrapes.
type Collector struct {
	cfg    *config.Config
	client *pcvue.Client
	logger *slog.Logger

	up                    *prometheus.Desc
	scrapeDurationSeconds *prometheus.Desc
	scrapeErrorsTotal     *prometheus.CounterVec

	variableValue *prometheus.Desc

	alarmActive          *prometheus.Desc
	alarmUnacknowledged  *prometheus.Desc
	alarmOldestActiveAge *prometheus.Desc
}

// New creates a collector using the supplied configuration, PCVue client, and logger.
func New(cfg *config.Config, client *pcvue.Client, logger *slog.Logger) *Collector {
	const namespace = "pcvue"

	commonLabels := prometheus.Labels{}
	maps.Copy(commonLabels, cfg.Labels)

	scrapeErrorsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   namespace,
		Name:        "scrape_errors_total",
		Help:        "Number of PCVue scrape errors.",
		ConstLabels: commonLabels,
	}, []string{"type"})
	scrapeErrorsTotal.WithLabelValues("variables")
	scrapeErrorsTotal.WithLabelValues("alarms")

	return &Collector{
		cfg:    cfg,
		client: client,
		logger: logger,

		up: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "up"),
			"Whether the PCVue API scrape was successful.",
			nil,
			commonLabels,
		),

		scrapeDurationSeconds: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "scrape_duration_seconds"),
			"Duration of the PCVue scrape in seconds.",
			nil,
			commonLabels,
		),

		scrapeErrorsTotal: scrapeErrorsTotal,

		variableValue: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "variable", "value"),
			"Current PCVue variable value.",
			[]string{"name", "area"},
			commonLabels,
		),

		alarmActive: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "alarm", "active"),
			"Number of active PCVue alarms.",
			[]string{"severity", "assoc_label"},
			commonLabels,
		),

		alarmUnacknowledged: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "alarm", "unacknowledged"),
			"Number of active unacknowledged PCVue alarms.",
			[]string{"severity", "assoc_label"},
			commonLabels,
		),

		alarmOldestActiveAge: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "alarm", "oldest_active_age_seconds"),
			"Age of the oldest active PCVue alarm in seconds.",
			nil,
			commonLabels,
		),
	}
}

// Describe sends the descriptors for every metric the collector can emit.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.scrapeDurationSeconds
	c.scrapeErrorsTotal.Describe(ch)
	ch <- c.variableValue
	ch <- c.alarmActive
	ch <- c.alarmUnacknowledged
	ch <- c.alarmOldestActiveAge
}

// Collect fetches configured PCVue data and sends the resulting metrics to ch.
// A failed variable or alarm read sets pcvue_up to 0 and increments its scrape error counter.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.PCVue.Timeout)
	defer cancel()

	ok := 1.0

	if err := c.collectVariables(ctx, ch); err != nil {
		ok = 0
		c.logger.Error("failed to collect PCVue variables", "error", err)
		c.scrapeErrorsTotal.WithLabelValues("variables").Inc()
	}

	if err := c.collectAlarms(ctx, ch); err != nil {
		ok = 0
		c.logger.Error("failed to collect PCVue alarms", "error", err)
		c.scrapeErrorsTotal.WithLabelValues("alarms").Inc()
	}

	c.scrapeErrorsTotal.Collect(ch)
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, ok)
	ch <- prometheus.MustNewConstMetric(
		c.scrapeDurationSeconds,
		prometheus.GaugeValue,
		time.Since(start).Seconds(),
	)
}

func (c *Collector) collectVariables(ctx context.Context, ch chan<- prometheus.Metric) error {
	if len(c.cfg.Variables) == 0 {
		return nil
	}

	names := make([]string, 0, len(c.cfg.Variables))
	variableConfigByName := map[string]config.VariableConfig{}

	for _, variable := range c.cfg.Variables {
		names = append(names, variable.Name)
		variableConfigByName[variable.Name] = variable
	}

	values, err := c.client.ReadVariables(ctx, names)

	for _, value := range values {
		variableCfg := variableConfigByName[value.Name]

		ch <- prometheus.MustNewConstMetric(
			c.variableValue,
			prometheus.GaugeValue,
			value.Value,
			value.Name,
			variableCfg.Area,
		)
	}

	return err
}

func (c *Collector) collectAlarms(ctx context.Context, ch chan<- prometheus.Metric) error {
	if !c.cfg.Alarms.Aggregate {
		return nil
	}

	alarms, err := c.client.ReadActiveAlarms(ctx)
	if err != nil {
		return err
	}

	c.collectAlarmAggregates(ch, alarms)

	return nil
}

func (c *Collector) collectAlarmAggregates(ch chan<- prometheus.Metric, alarms []pcvue.Alarm) {
	now := time.Now()

	activeByGroup := map[alarmGroup]float64{}
	unackByGroup := map[alarmGroup]float64{}

	var oldestAge float64

	for _, alarm := range alarms {
		if !alarm.Active {
			continue
		}

		severity := normalizeLabelValue(alarm.Severity)
		assocLabel := normalizeLabelValue(alarm.AssocLabel)
		group := alarmGroup{severity: severity, assocLabel: assocLabel}

		activeByGroup[group]++

		if !alarm.Acknowledged {
			unackByGroup[group]++
		}

		if !alarm.Timestamp.IsZero() {
			age := now.Sub(alarm.Timestamp).Seconds()
			if age > oldestAge {
				oldestAge = age
			}
		}
	}

	for group, value := range activeByGroup {
		ch <- prometheus.MustNewConstMetric(
			c.alarmActive,
			prometheus.GaugeValue,
			value,
			group.severity,
			group.assocLabel,
		)
	}

	for group, value := range unackByGroup {
		ch <- prometheus.MustNewConstMetric(
			c.alarmUnacknowledged,
			prometheus.GaugeValue,
			value,
			group.severity,
			group.assocLabel,
		)
	}

	ch <- prometheus.MustNewConstMetric(
		c.alarmOldestActiveAge,
		prometheus.GaugeValue,
		oldestAge,
	)
}

type alarmGroup struct {
	severity   string
	assocLabel string
}

func normalizeLabelValue(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "unknown"
	}
	return value
}
