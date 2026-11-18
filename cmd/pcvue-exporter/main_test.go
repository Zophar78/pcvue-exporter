package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

// TestLandingPageProxy verifies the browser-visible links through a proxy that
// strips its external prefix, including a configured nested metrics path.
func TestLandingPageProxy(t *testing.T) {
	for _, tc := range []struct{ name, prefix, metricsPath string }{
		{"root", "", "/metrics"},
		{"prefixed", "/pcvue", "/metrics"},
		{"custom metrics", "/pcvue", "/custom/metrics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			landing, err := newLandingPage(tc.metricsPath)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.Handle("/", landing)
			mux.HandleFunc(tc.metricsPath, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("metrics endpoint")) })
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("health endpoint")) })
			handler := http.Handler(mux)
			if tc.prefix != "" {
				handler = http.StripPrefix(tc.prefix, handler)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			pageURL := server.URL + tc.prefix + "/"
			response, err := http.Get(pageURL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("landing page: status %d, error %v", response.StatusCode, err)
			}
			base, _ := url.Parse(pageURL)
			for _, link := range []struct{ text, path, body string }{
				{"Metrics", tc.metricsPath, "metrics endpoint"},
				{"Healthz", "/healthz", "health endpoint"},
			} {
				pattern := regexp.MustCompile(`href="([^"]+)"[^>]*>` + link.text + `</a>`)
				match := pattern.FindSubmatch(body)
				if match == nil {
					t.Fatalf("link %s missing from page: %s", link.text, body)
				}
				target, err := url.Parse(string(match[1]))
				if err != nil {
					t.Fatal(err)
				}
				resolved := base.ResolveReference(target)
				if resolved.Path != tc.prefix+link.path {
					t.Fatalf("%s resolved to %q, want %q", link.text, resolved.Path, tc.prefix+link.path)
				}
				got, err := http.Get(resolved.String())
				if err != nil {
					t.Fatal(err)
				}
				content, err := io.ReadAll(got.Body)
				got.Body.Close()
				if err != nil || got.StatusCode != http.StatusOK || string(content) != link.body {
					t.Fatalf("%s endpoint: status %d, body %q, error %v", link.text, got.StatusCode, content, err)
				}
			}
		})
	}
}
