// Package config loads, defaults, and validates exporter configuration.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config contains the complete exporter configuration.
type Config struct {
	PCVue     PCVueConfig       `yaml:"pcvue"`
	Labels    map[string]string `yaml:"labels"`
	Variables []VariableConfig  `yaml:"variables"`
	Alarms    AlarmConfig       `yaml:"alarms"`
}

// PCVueConfig contains connection and timeout settings for the PCVue API.
type PCVueConfig struct {
	BaseURL            string        `yaml:"base_url"`
	Auth               AuthConfig    `yaml:"auth"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
	Timeout            time.Duration `yaml:"timeout"`
}

// AuthConfig contains credentials and scopes for the configured OAuth2 flow.
type AuthConfig struct {
	Type         string   `yaml:"type"`
	TokenURL     string   `yaml:"token_url"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	Username     string   `yaml:"username"`
	Password     string   `yaml:"password"`
	Scopes       []string `yaml:"scopes"`
}

// VariableConfig selects a PCVue variable and defines its Prometheus labels and type.
type VariableConfig struct {
	Name string `yaml:"name"`
	Area string `yaml:"area"`
	Type string `yaml:"type"`
}

// AlarmConfig controls aggregate alarm metrics and the alarm whitelist.
type AlarmConfig struct {
	Aggregate bool          `yaml:"aggregate"`
	Whitelist []AlarmFilter `yaml:"whitelist"`
}

// AlarmFilter identifies a PCVue alarm and its Prometheus labels.
type AlarmFilter struct {
	ID        string `yaml:"id"`
	PCVueName string `yaml:"pcvue_name"`
	Severity  string `yaml:"severity"`
	Area      string `yaml:"area"`
}

// Load reads a YAML configuration file, expands environment variables, applies defaults,
// and validates required fields. It returns an error if the file cannot be read or parsed,
// or if required configuration is missing.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	setDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func setDefaults(cfg *Config) {
	cfg.PCVue.BaseURL = strings.TrimRight(cfg.PCVue.BaseURL, "/")

	if cfg.PCVue.Timeout == 0 {
		cfg.PCVue.Timeout = 10 * time.Second
	}

	if cfg.Labels == nil {
		cfg.Labels = map[string]string{}
	}

	// Par défaut, on active les agrégats d’alarmes.
	// Si tu veux rendre ça strictement explicite dans le YAML, supprime cette ligne.
	if !cfg.Alarms.Aggregate {
		cfg.Alarms.Aggregate = true
	}

	for i := range cfg.Variables {
		if cfg.Variables[i].Area == "" {
			cfg.Variables[i].Area = "unknown"
		}

		if cfg.Variables[i].Type == "" {
			cfg.Variables[i].Type = "gauge"
		}
	}

	for i := range cfg.Alarms.Whitelist {
		if cfg.Alarms.Whitelist[i].Area == "" {
			cfg.Alarms.Whitelist[i].Area = "unknown"
		}

		if cfg.Alarms.Whitelist[i].Severity == "" {
			cfg.Alarms.Whitelist[i].Severity = "unknown"
		}
	}
}

func validate(cfg *Config) error {
	if cfg.PCVue.BaseURL == "" {
		return fmt.Errorf("pcvue.base_url is required")
	}

	for i, variable := range cfg.Variables {
		if variable.Name == "" {
			return fmt.Errorf("variables[%d].name is required", i)
		}
	}

	for i, alarm := range cfg.Alarms.Whitelist {
		if alarm.ID == "" {
			return fmt.Errorf("alarms.whitelist[%d].id is required", i)
		}

		if alarm.PCVueName == "" {
			return fmt.Errorf("alarms.whitelist[%d].pcvue_name is required", i)
		}
	}

	return nil
}
