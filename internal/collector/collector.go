// Package collector exposes configured PCVue values and alarms as Prometheus metrics.
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
	scrapeErrorsTotal     *prometheus.Desc

	variableValue *prometheus.Desc

	alarmActiveTotal       *prometheus.Desc
	alarmUnackTotal        *prometheus.Desc
	alarmOldestActiveAge   *prometheus.Desc
	alarmWhitelistedActive *prometheus.Desc
	alarmWhitelistedUnack  *prometheus.Desc
	alarmWhitelistedAge    *prometheus.Desc
}

// New creates a collector using the supplied configuration, PCVue client, and logger.
func New(cfg *config.Config, client *pcvue.Client, logger *slog.Logger) *Collector {
	const namespace = "pcvue"

	commonLabels := prometheus.Labels{}
	maps.Copy(commonLabels, cfg.Labels)

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

		scrapeErrorsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "scrape_errors_total"),
			"Number of PCVue scrape errors.",
			[]string{"type"},
			commonLabels,
		),

		variableValue: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "variable", "value"),
			"Current PCVue variable value.",
			[]string{"name", "alias", "area"},
			commonLabels,
		),

		alarmActiveTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "alarm", "active_total"),
			"Number of active PCVue alarms.",
			[]string{"severity", "assoc_label"},
			commonLabels,
		),

		alarmUnackTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "alarm", "unack_total"),
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

		alarmWhitelistedActive: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "alarm", "active"),
			"Whether a whitelisted PCVue alarm is active.",
			[]string{"alarm_id", "pcvue_name", "severity", "area"},
			commonLabels,
		),

		alarmWhitelistedUnack: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "alarm", "unack"),
			"Whether a whitelisted PCVue alarm is active and unacknowledged.",
			[]string{"alarm_id", "pcvue_name", "severity", "area"},
			commonLabels,
		),

		alarmWhitelistedAge: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "alarm", "age_seconds"),
			"Age of a whitelisted active PCVue alarm in seconds.",
			[]string{"alarm_id", "pcvue_name", "severity", "area"},
			commonLabels,
		),
	}
}

// Describe sends the descriptors for every metric the collector can emit.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.scrapeDurationSeconds
	ch <- c.scrapeErrorsTotal
	ch <- c.variableValue
	ch <- c.alarmActiveTotal
	ch <- c.alarmUnackTotal
	ch <- c.alarmOldestActiveAge
	ch <- c.alarmWhitelistedActive
	ch <- c.alarmWhitelistedUnack
	ch <- c.alarmWhitelistedAge
}

// Collect fetches configured PCVue data and sends the resulting metrics to ch.
// A failed variable or alarm read sets pcvue_up to 0 and emits a scrape error metric.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.PCVue.Timeout)
	defer cancel()

	ok := 1.0

	if err := c.collectVariables(ctx, ch); err != nil {
		ok = 0
		c.logger.Error("failed to collect PCVue variables", "error", err)
		ch <- prometheus.MustNewConstMetric(
			c.scrapeErrorsTotal,
			prometheus.CounterValue,
			1,
			"variables",
		)
	}

	if err := c.collectAlarms(ctx, ch); err != nil {
		ok = 0
		c.logger.Error("failed to collect PCVue alarms", "error", err)
		ch <- prometheus.MustNewConstMetric(
			c.scrapeErrorsTotal,
			prometheus.CounterValue,
			1,
			"alarms",
		)
	}

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
			variableCfg.Alias,
			variableCfg.Area,
		)
	}

	return err
}

func (c *Collector) collectAlarms(ctx context.Context, ch chan<- prometheus.Metric) error {
	alarms, err := c.client.ReadActiveAlarms(ctx)
	if err != nil {
		return err
	}

	if c.cfg.Alarms.Aggregate {
		c.collectAlarmAggregates(ch, alarms)
	}

	c.collectWhitelistedAlarms(ch, alarms)

	return nil
}

func (c *Collector) collectAlarmAggregates(ch chan<- prometheus.Metric, alarms []pcvue.Alarm) {
	now := time.Now()

	activeBySeverityAssocLabel := map[string]float64{}
	unackBySeverityAssocLabel := map[string]float64{}

	var oldestAge float64

	for _, alarm := range alarms {
		if !alarm.Active {
			continue
		}

		severity := normalizeLabelValue(alarm.Severity)
		assocLabel := normalizeLabelValue(alarm.AssocLabel)
		key := severity + "\xff" + assocLabel

		activeBySeverityAssocLabel[key]++

		if !alarm.Acknowledged {
			unackBySeverityAssocLabel[key]++
		}

		if !alarm.Timestamp.IsZero() {
			age := now.Sub(alarm.Timestamp).Seconds()
			if age > oldestAge {
				oldestAge = age
			}
		}
	}

	for key, value := range activeBySeverityAssocLabel {
		parts := strings.Split(key, "\xff")
		ch <- prometheus.MustNewConstMetric(
			c.alarmActiveTotal,
			prometheus.GaugeValue,
			value,
			parts[0],
			parts[1],
		)
	}

	for key, value := range unackBySeverityAssocLabel {
		parts := strings.Split(key, "\xff")
		ch <- prometheus.MustNewConstMetric(
			c.alarmUnackTotal,
			prometheus.GaugeValue,
			value,
			parts[0],
			parts[1],
		)
	}

	ch <- prometheus.MustNewConstMetric(
		c.alarmOldestActiveAge,
		prometheus.GaugeValue,
		oldestAge,
	)
}

func (c *Collector) collectWhitelistedAlarms(ch chan<- prometheus.Metric, alarms []pcvue.Alarm) {
	now := time.Now()

	alarmByName := map[string]pcvue.Alarm{}
	for _, alarm := range alarms {
		alarmByName[alarm.Name] = alarm
	}

	for _, expected := range c.cfg.Alarms.Whitelist {
		alarm, found := alarmByName[expected.PCVueName]

		active := 0.0
		unack := 0.0
		age := 0.0

		if found && alarm.Active {
			active = 1
			if !alarm.Acknowledged {
				unack = 1
			}
			if !alarm.Timestamp.IsZero() {
				age = now.Sub(alarm.Timestamp).Seconds()
			}
		}

		ch <- prometheus.MustNewConstMetric(
			c.alarmWhitelistedActive,
			prometheus.GaugeValue,
			active,
			expected.ID,
			expected.PCVueName,
			expected.Severity,
			expected.Area,
		)

		ch <- prometheus.MustNewConstMetric(
			c.alarmWhitelistedUnack,
			prometheus.GaugeValue,
			unack,
			expected.ID,
			expected.PCVueName,
			expected.Severity,
			expected.Area,
		)

		ch <- prometheus.MustNewConstMetric(
			c.alarmWhitelistedAge,
			prometheus.GaugeValue,
			age,
			expected.ID,
			expected.PCVueName,
			expected.Severity,
			expected.Area,
		)
	}
}

func normalizeLabelValue(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "unknown"
	}
	return value
}
