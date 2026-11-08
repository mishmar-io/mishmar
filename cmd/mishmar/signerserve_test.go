package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/signer/service"
)

// freePort is a loopback port nothing listens on right now.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// serveWithMetrics runs `signer serve` on a store of its own until stop is
// called, with the metrics listener as the environment says.
func serveWithMetrics(t *testing.T, metricsListen string) (apiAddr string, stop func() error) {
	t.Helper()
	// A test master key: 64 hex characters of nothing, never a real one.
	t.Setenv(service.MasterKeyEnv, strings.Repeat("01", 32))
	t.Setenv(service.MetricsListenEnv, metricsListen)
	apiAddr = freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		root := newRootCmd()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"signer", "serve", "--listen", apiAddr, "--store", filepath.Join(t.TempDir(), "keys.json"), "--log-level", "error"})
		done <- root.ExecuteContext(ctx)
	}()
	stop = func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("signer serve did not stop")
			return nil
		}
	}
	return apiAddr, stop
}

func waitFor(t *testing.T, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := http.Get(url)
		if err == nil {
			return res
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never answered: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSignerServeExposesMetricsOnItsOwnListener(t *testing.T) {
	metricsAddr := freePort(t)
	apiAddr, stop := serveWithMetrics(t, metricsAddr)

	res := waitFor(t, "http://"+metricsAddr+"/metrics")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: %d\n%s", res.StatusCode, body)
	}
	for _, want := range []string{"\nmishmar_signer_ready 1\n", "\nmishmar_signer_pending_requests 0\n", "\nmishmar_signer_oldest_pending_seconds 0\n", "mishmar_signer_build_info{version=\"dev\"} 1\n"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
	// The API is not on the metrics listener, and the metrics are not on the API's.
	if res := waitFor(t, "http://"+metricsAddr+"/health"); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /health on the metrics listener: %d, want 404", res.StatusCode)
	}
	if res := waitFor(t, "http://"+apiAddr+"/metrics"); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /metrics on the API: %d, want 404", res.StatusCode)
	}
	if res := waitFor(t, "http://"+apiAddr+"/health"); res.StatusCode != http.StatusOK {
		t.Errorf("GET /health on the API: %d, want 200", res.StatusCode)
	}
	if err := stop(); err != nil {
		t.Fatalf("signer serve: %v", err)
	}
	if _, err := http.Get("http://" + metricsAddr + "/metrics"); err == nil {
		t.Error("the metrics listener outlived the service")
	}
}

func TestSignerServeWithoutAMetricsListener(t *testing.T) {
	apiAddr, stop := serveWithMetrics(t, "")
	if res := waitFor(t, "http://"+apiAddr+"/health"); res.StatusCode != http.StatusOK {
		t.Errorf("GET /health: %d", res.StatusCode)
	}
	if err := stop(); err != nil {
		t.Fatalf("signer serve: %v", err)
	}
}

func TestSignerServeRefusesAMetricsPortInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	t.Setenv(service.MasterKeyEnv, strings.Repeat("01", 32))
	t.Setenv(service.MetricsListenEnv, taken.Addr().String())
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"signer", "serve", "--listen", freePort(t), "--store", filepath.Join(t.TempDir(), "keys.json"), "--log-level", "error"})
	err = root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), service.MetricsListenEnv) {
		t.Fatalf("got %v, want an error naming %s", err, service.MetricsListenEnv)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(taken.Addr())) {
		t.Errorf("the error does not name the address: %v", err)
	}
}
