package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExpandsEnvironmentAndAppliesDefaults(t *testing.T) {
	t.Setenv("PCVUE_TEST_BASE_URL", "https://pcvue.example.local/")
	t.Setenv("PCVUE_TEST_SECRET", "test-secret")

	path := writeConfig(t, `pcvue:
  base_url: "${PCVUE_TEST_BASE_URL}"
  timeout: "3s"
  auth:
    client_secret: "${PCVUE_TEST_SECRET}"
labels:
  site: "lab"
variables:
  - name: "BMS.PUMP-1"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.PCVue.BaseURL != "https://pcvue.example.local" {
		t.Errorf("BaseURL = %q, want trailing slash removed", cfg.PCVue.BaseURL)
	}
	if cfg.PCVue.Timeout != 3*time.Second {
		t.Errorf("Timeout = %s, want 3s", cfg.PCVue.Timeout)
	}
	if cfg.PCVue.Auth.ClientSecret != "test-secret" {
		t.Errorf("ClientSecret = %q, want environment variable value", cfg.PCVue.Auth.ClientSecret)
	}
	if cfg.Labels["site"] != "lab" {
		t.Errorf("Labels[site] = %q, want lab", cfg.Labels["site"])
	}
	if len(cfg.Variables) != 1 {
		t.Fatalf("got %d variables, want 1", len(cfg.Variables))
	}
	if variable := cfg.Variables[0]; variable.Area != "unknown" || variable.Type != "gauge" {
		t.Errorf("variable defaults = %+v, want area unknown and type gauge", variable)
	}
	if !cfg.Alarms.Aggregate {
		t.Error("alarm aggregates should be enabled by default")
	}
}

func TestLoadAlarmAggregateSetting(t *testing.T) {
	for _, test := range []struct {
		name        string
		alarms      string
		wantEnabled bool
	}{
		{name: "missing defaults to enabled", wantEnabled: true},
		{name: "explicit true stays enabled", alarms: "alarms:\n  aggregate: true\n", wantEnabled: true},
		{name: "explicit false stays disabled", alarms: "alarms:\n  aggregate: false\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := "pcvue:\n  base_url: http://pcvue\n" + test.alarms
			cfg, err := Load(writeConfig(t, content))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Alarms.Aggregate != test.wantEnabled {
				t.Errorf("Aggregate = %t, want %t", cfg.Alarms.Aggregate, test.wantEnabled)
			}
		})
	}
}

func TestLoadRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "base URL",
			content: "pcvue: {}\n",
			wantErr: "pcvue.base_url is required",
		},
		{
			name:    "variable name",
			content: "pcvue:\n  base_url: http://pcvue\nvariables:\n  - area: plant-a\n",
			wantErr: "variables[0].name is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.content))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadRejectsRemovedAlarmWhitelist(t *testing.T) {
	for _, test := range []struct {
		name      string
		whitelist string
	}{
		{name: "configured alarms", whitelist: "\n    - id: PUMP_ALARM\n      pcvue_name: PUMP.ALARM\n"},
		{name: "empty list", whitelist: " []\n"},
		{name: "null", whitelist: " null\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := "pcvue:\n  base_url: http://pcvue\nalarms:\n  aggregate: true\n  whitelist:" + test.whitelist
			_, err := Load(writeConfig(t, content))
			if err == nil || !strings.Contains(err.Error(), "alarms.whitelist is no longer supported") {
				t.Fatalf("Load() error = %v, want removed whitelist error", err)
			}
		})
	}
}

func TestLoadExampleConfigs(t *testing.T) {
	t.Setenv("PCVUE_BASE_URL", "https://pcvue.example.local")
	t.Setenv("PCVUE_WSO2_BASE_URL", "https://wso2.example.local/pcvue")
	for _, name := range []string{"config.api.example.yml", "config.wso2.example.yml"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !cfg.Alarms.Aggregate {
				t.Error("example should enable alarm aggregates")
			}
		})
	}
}

func TestLoadRejectsDuplicateVariableNames(t *testing.T) {
	tests := []struct {
		name      string
		variables string
		wantErr   string
	}{
		{
			name:      "same name",
			variables: "  - name: PUMP\n  - name: PUMP\n",
			wantErr:   `API name "PUMP" at variables[0] ("PUMP"), variables[1] ("PUMP")`,
		},
		{
			name:      "same API name after path normalization",
			variables: "  - name: PUMP/ROOM/TEMP\n  - name: PUMP.ROOM.TEMP\n",
			wantErr:   `API name "PUMP.ROOM.TEMP" at variables[0] ("PUMP/ROOM/TEMP"), variables[1] ("PUMP.ROOM.TEMP")`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := "pcvue:\n  base_url: http://pcvue\nvariables:\n" + test.variables
			_, err := Load(writeConfig(t, content))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadReportsAllDuplicateVariableNames(t *testing.T) {
	content := `pcvue:
  base_url: http://pcvue
variables:
  - name: PUMP
  - name: PUMP/ROOM/TEMP
  - name: FAN
  - name: PUMP.ROOM.TEMP
  - name: PUMP
  - name: PUMP
`

	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("Load() error = nil, want duplicate variable names error")
	}

	wantErr := `duplicate PCVue variable names after normalizing '/' to '.': API name "PUMP" at variables[0] ("PUMP"), variables[4] ("PUMP"), variables[5] ("PUMP"); API name "PUMP.ROOM.TEMP" at variables[1] ("PUMP/ROOM/TEMP"), variables[3] ("PUMP.ROOM.TEMP")`
	if err.Error() != wantErr {
		t.Fatalf("Load() error = %q, want %q", err, wantErr)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	return path
}
