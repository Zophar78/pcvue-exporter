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

// AlarmConfig controls aggregate alarm metrics.
type AlarmConfig struct {
	Aggregate bool `yaml:"aggregate"`
}

// UnmarshalYAML rejects the removed individual alarm configuration.
func (c *AlarmConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode {
		for i := 0; i < len(value.Content); i += 2 {
			if value.Content[i].Value == "whitelist" {
				return fmt.Errorf("alarms.whitelist is no longer supported; remove it and define alerting rules from exported variable metrics")
			}
		}
	}

	type plainAlarmConfig AlarmConfig
	return value.Decode((*plainAlarmConfig)(c))
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

}

func validate(cfg *Config) error {
	if cfg.PCVue.BaseURL == "" {
		return fmt.Errorf("pcvue.base_url is required")
	}

	variableIndicesByAPIName := make(map[string][]int, len(cfg.Variables))
	for i, variable := range cfg.Variables {
		if variable.Name == "" {
			return fmt.Errorf("variables[%d].name is required", i)
		}

		apiName := strings.ReplaceAll(variable.Name, "/", ".")
		variableIndicesByAPIName[apiName] = append(variableIndicesByAPIName[apiName], i)
	}

	var duplicateNames []string
	reportedAPIName := make(map[string]bool)
	for _, variable := range cfg.Variables {
		apiName := strings.ReplaceAll(variable.Name, "/", ".")
		indices := variableIndicesByAPIName[apiName]
		if len(indices) < 2 || reportedAPIName[apiName] {
			continue
		}

		occurrences := make([]string, 0, len(indices))
		for _, index := range indices {
			occurrences = append(occurrences, fmt.Sprintf("variables[%d] (%q)", index, cfg.Variables[index].Name))
		}
		duplicateNames = append(duplicateNames, fmt.Sprintf("API name %q at %s", apiName, strings.Join(occurrences, ", ")))
		reportedAPIName[apiName] = true
	}
	if len(duplicateNames) > 0 {
		return fmt.Errorf("duplicate PCVue variable names after normalizing '/' to '.': %s", strings.Join(duplicateNames, "; "))
	}

	return nil
}
