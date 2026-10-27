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
alarms:
  whitelist:
    - id: "PUMP_ALARM"
      pcvue_name: "BMS.PUMP_ALARM"
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
	if len(cfg.Alarms.Whitelist) != 1 {
		t.Fatalf("got %d whitelisted alarms, want 1", len(cfg.Alarms.Whitelist))
	}
	if alarm := cfg.Alarms.Whitelist[0]; alarm.Area != "unknown" || alarm.Severity != "unknown" {
		t.Errorf("alarm defaults = %+v, want area and severity unknown", alarm)
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
		{
			name:    "alarm ID",
			content: "pcvue:\n  base_url: http://pcvue\nalarms:\n  whitelist:\n    - pcvue_name: PUMP.ALARM\n",
			wantErr: "alarms.whitelist[0].id is required",
		},
		{
			name:    "PCVue alarm name",
			content: "pcvue:\n  base_url: http://pcvue\nalarms:\n  whitelist:\n    - id: PUMP_ALARM\n",
			wantErr: "alarms.whitelist[0].pcvue_name is required",
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
