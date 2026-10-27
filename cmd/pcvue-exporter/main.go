// Package main starts the PCVue Prometheus exporter.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/Zophar78/pcvue-exporter/internal/collector"
	"github.com/Zophar78/pcvue-exporter/internal/config"
	"github.com/Zophar78/pcvue-exporter/internal/pcvue"
	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	promslogflag "github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
)

const (
	name        = "pcvue-exporter"
	namespace   = "pcvue_exporter"
	description = "Prometheus exporter for PCVue SCADA system."
)

func newBuildInfoCollector() prometheus.Collector {
	return prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "pcvue_exporter_build_info",
			Help: "Build information about pcvue-exporter.",
			ConstLabels: prometheus.Labels{
				"version":    version.Version,
				"revision":   version.Revision,
				"branch":     version.Branch,
				"build_user": version.BuildUser,
				"build_date": version.BuildDate,
				"goversion":  runtime.Version(),
			},
		},
		func() float64 {
			return 1
		},
	)
}

func newLandingPage() (http.Handler, error) {
	return web.NewLandingPage(web.LandingConfig{
		Name:        name,
		Description: "Prometheus exporter for PCVue SCADA system.",
		Version:     version.Version,
		Profiling:   "false",
		Links: []web.LandingLinks{
			{Address: "/metrics", Text: "Metrics"},
			{Address: "/healthz", Text: "Healthz"},
		},
	})
}

func serveUntilShutdown(ctx context.Context, server *http.Server, logout func(context.Context) error) error {
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	var shutdownErr error
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdownErr = err
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		shutdownErr = errors.Join(shutdownErr, err)
		if closeErr := server.Close(); closeErr != nil {
			shutdownErr = errors.Join(shutdownErr, closeErr)
		}
	}
	cancelShutdown()

	logoutCtx, cancelLogout := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLogout()
	if logout != nil {
		if err := logout(logoutCtx); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}

	return shutdownErr
}

func main() {
	app := kingpin.New(name, description)
	app.HelpFlag.Short('h')

	configFile := app.Flag(
		"config.file",
		"Path to the exporter configuration file.",
	).Default("config.yml").String()

	webListenAddress := app.Flag(
		"web.listen-address",
		"Address on which to expose metrics and web interface.",
	).Default(":9718").String()

	webMetricsPath := app.Flag(
		"web.metrics-path",
		"Path under which to expose Prometheus metrics.",
	).Default("/metrics").String()

	promslogConfig := &promslog.Config{}
	promslogflag.AddFlags(app, promslogConfig)

	app.Version(version.Print(name))
	kingpin.MustParse(app.Parse(os.Args[1:]))

	logger := promslog.New(promslogConfig)

	cfg, err := config.Load(*configFile)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	client, err := pcvue.NewClient(cfg.PCVue, logger)
	if err != nil {
		logger.Error("failed to create PCVue client", "error", err)
		os.Exit(1)
	}

	pcvueCollector := collector.New(cfg, client, logger)

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		pcvueCollector,
		newBuildInfoCollector(),
	)

	mux := http.NewServeMux()

	landingPage, err := newLandingPage()
	if err != nil {
		logger.Error("failed to create landing page", "err", err)
		os.Exit(1)
	}
	mux.Handle("/", landingPage)

	mux.Handle(*webMetricsPath, promhttp.HandlerFor(
		registry,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]string{
			"status":  "ok",
			"service": name,
			"version": version.Version,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(response); err != nil {
			logger.Error("failed to write health response", "error", err)
		}
	})

	logger.Info(
		"starting pcvue exporter",
		"listen_address", *webListenAddress,
		"metrics_path", *webMetricsPath,
		"config_file", *configFile,
	)

	server := &http.Server{
		Addr:              *webListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.PCVue.Timeout + 5*time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serveUntilShutdown(shutdownCtx, server, client.Logout); err != nil {
		logger.Error("server shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("pcvue exporter stopped")
}
