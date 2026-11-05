package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestHealthcheckHelperProcess(t *testing.T) {
	if os.Getenv("PCVUE_HEALTH_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"pcvue-exporter"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing helper arguments")
}

func healthcheckTestCommand(args ...string) *exec.Cmd {
	testArgs := append([]string{"-test.run=^TestHealthcheckHelperProcess$", "--"}, args...)
	cmd := exec.Command(os.Args[0], testArgs...)
	cmd.Env = append(os.Environ(), "PCVUE_HEALTH_TEST_HELPER=1")
	return cmd
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestHealthcheckCommandExitAndOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL, http.StatusFound)
	}))
	defer redirect.Close()

	noContent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer noContent.Close()

	for _, tc := range []struct {
		name    string
		address string
		wantErr string
	}{
		{name: "ok", address: server.Listener.Addr().String()},
		{name: "refused", address: unusedLoopbackAddress(t), wantErr: "healthcheck failed:"},
		{name: "non-200", address: unhealthy.Listener.Addr().String(), wantErr: "503"},
		{name: "redirect", address: redirect.Listener.Addr().String(), wantErr: "302"},
		{name: "other 2xx", address: noContent.Listener.Addr().String(), wantErr: "204"},
		{name: "invalid address", address: "invalid", wantErr: "invalid listen address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := healthcheckTestCommand("healthcheck", "--web.listen-address="+tc.address)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if tc.wantErr == "" {
				if err != nil || stderr.Len() != 0 {
					t.Fatalf("healthcheck error = %v, stderr = %q", err, stderr.String())
				}
				return
			}
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 1 {
				t.Fatalf("healthcheck error = %v, want exit code 1", err)
			}
			if !strings.Contains(stderr.String(), tc.wantErr) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

func TestHealthcheckCommandTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	cmd := healthcheckTestCommand("healthcheck", "--web.listen-address="+server.Listener.Addr().String())
	start := time.Now()
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("healthcheck error = %v, output = %q, want exit code 1", err, output)
	}
	if !strings.Contains(string(output), "healthcheck failed:") {
		t.Errorf("output = %q, want healthcheck error", output)
	}
	if elapsed < 1800*time.Millisecond || elapsed > 5*time.Second {
		t.Errorf("healthcheck took %v, want approximately 2 seconds", elapsed)
	}
}

func TestExporterHealthyWhenPCVueUnavailable(t *testing.T) {
	var requests atomic.Int32
	pcvue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer pcvue.Close()

	configPath := filepath.Join(t.TempDir(), "config.yml")
	config := fmt.Sprintf(`pcvue:
  base_url: %s
  timeout: 1s
  auth:
    type: oauth2_client_credentials
    token_url: %s/token
    client_id: test-client
    client_secret: test-secret
variables:
  - name: TEST
alarms:
  aggregate: false
`, pcvue.URL, pcvue.URL)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	address := unusedLoopbackAddress(t)
	cmd := healthcheckTestCommand("--config.file="+configPath, "--web.listen-address="+address)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Errorf("signal exporter: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			t.Errorf("wait for exporter: %v", err)
		}
	})

	client := &http.Client{Timeout: time.Second}
	url := "http://" + address
	var response *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get(url + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("exporter did not start: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthy endpoint status = %d, want 200", response.StatusCode)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("health endpoint made %d PCVue requests", got)
	}

	response, err = client.Get(url + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read metrics: %v; close: %v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "pcvue_up 0\n") {
		t.Fatalf("metrics status = %d; want pcvue_up 0 in %s", response.StatusCode, body)
	}
	if requests.Load() == 0 {
		t.Fatal("metrics scrape did not attempt to contact PCVue")
	}
	if output, err := healthcheckTestCommand("healthcheck", "--web.listen-address="+address).CombinedOutput(); err != nil {
		t.Fatalf("healthcheck after PCVue failure: %v; output = %q", err, output)
	}
}
