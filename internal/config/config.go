package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	PCVue     PCVueConfig       `yaml:"pcvue"`
	Labels    map[string]string `yaml:"labels"`
	Variables []VariableConfig  `yaml:"variables"`
	Alarms    AlarmConfig       `yaml:"alarms"`
}

type PCVueConfig struct {
	BaseURL            string        `yaml:"base_url"`
	Auth               AuthConfig    `yaml:"auth"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
	Timeout            time.Duration `yaml:"timeout"`
}

type AuthConfig struct {
	Type         string   `yaml:"type"`
	TokenURL     string   `yaml:"token_url"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	Username     string   `yaml:"username"`
	Password     string   `yaml:"password"`
	Scopes       []string `yaml:"scopes"`
}

type VariableConfig struct {
	Name  string `yaml:"name"`
	Alias string `yaml:"alias"`
	Area  string `yaml:"area"`
	Type  string `yaml:"type"`
}

type AlarmConfig struct {
	Aggregate bool          `yaml:"aggregate"`
	Whitelist []AlarmFilter `yaml:"whitelist"`
}

type AlarmFilter struct {
	ID        string `yaml:"id"`
	PCVueName string `yaml:"pcvue_name"`
	Severity  string `yaml:"severity"`
	Area      string `yaml:"area"`
}

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
		if cfg.Variables[i].Alias == "" {
			cfg.Variables[i].Alias = sanitizeAlias(cfg.Variables[i].Name)
		}

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

func sanitizeAlias(value string) string {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, ".", "_")
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")

	return value
}
