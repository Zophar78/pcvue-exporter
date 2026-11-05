package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zophar78/pcvue-exporter/internal/config"
	"github.com/Zophar78/pcvue-exporter/internal/pcvue"
	"github.com/prometheus/common/promslog"
)

func TestServeUntilShutdownCallsLogout(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	server := &http.Server{Addr: "127.0.0.1:0"}
	logoutCalled := false
	err := serveUntilShutdown(ctx, server, func(ctx context.Context) error {
		logoutCalled = true
		if _, ok := ctx.Deadline(); !ok {
			t.Error("logout context has no deadline")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("serveUntilShutdown() error = %v", err)
	}
	if !logoutCalled {
		t.Fatal("logout callback was not called")
	}
}

func TestApplicationParsesDefaults(t *testing.T) {
	app, options := newApplication()
	if _, err := app.Parse(nil); err != nil {
		t.Fatalf("parse default flags: %v", err)
	}
	if options.configFile != "config.yml" || options.webListenAddress != ":9718" || options.webMetricsPath != "/metrics" {
		t.Errorf("unexpected defaults: %+v", options)
	}
}

func TestApplicationParsesFlagsAndLoadsConfig(t *testing.T) {
	t.Setenv("PCVUE_TEST_URL", "https://pcvue.example.local/")
	configPath := filepath.Join(t.TempDir(), "config.yml")
	content := `pcvue:
  base_url: "${PCVUE_TEST_URL}"
  timeout: 3s
  auth:
    type: oauth2_client_credentials
    token_url: https://pcvue.example.local/token
    client_id: test-client
    client_secret: test-secret
variables:
  - name: TEST.TEMP
    area: lab
alarms:
  aggregate: true
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	app, options := newApplication()
	_, err := app.Parse([]string{
		"--config.file=" + configPath,
		"--web.listen-address=127.0.0.1:9876",
		"--web.metrics-path=/custom-metrics",
		"--log.level=debug",
		"--log.format=json",
	})
	if err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if options.configFile != configPath || options.webListenAddress != "127.0.0.1:9876" || options.webMetricsPath != "/custom-metrics" {
		t.Fatalf("parsed options = %+v, want explicit flag values", options)
	}
	logger := promslog.New(&options.logging)
	if !logger.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("--log.level=debug did not enable debug logging")
	}
	cfg, err := config.Load(options.configFile)
	if err != nil {
		t.Fatalf("load config selected by flag: %v", err)
	}
	if cfg.PCVue.BaseURL != "https://pcvue.example.local" || cfg.PCVue.Timeout != 3*time.Second {
		t.Errorf("unexpected PCVue settings: %+v", cfg.PCVue)
	}
	if len(cfg.Variables) != 1 || cfg.Variables[0].Name != "TEST.TEMP" || cfg.Variables[0].Area != "lab" {
		t.Errorf("unexpected variables: %+v", cfg.Variables)
	}
	if !cfg.Alarms.Aggregate {
		t.Error("alarm aggregates should be enabled")
	}
	if _, err := pcvue.NewClient(cfg.PCVue, logger); err != nil {
		t.Fatalf("initialize client from config: %v", err)
	}
}

func TestApplicationRejectsInvalidFlags(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown flag", args: []string{"--unknown-flag"}, want: "unknown long flag"},
		{name: "missing flag value", args: []string{"--config.file"}, want: "expected argument"},
		{name: "invalid log level", args: []string{"--log.level=invalid"}, want: "unrecognized log level"},
		{name: "invalid log format", args: []string{"--log.format=invalid"}, want: "unrecognized log format"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, _ := newApplication()
			_, err := app.Parse(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse flags error = %v, want %q", err, test.want)
			}
		})
	}
}
