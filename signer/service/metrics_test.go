package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMetricsListenFromTheEnvironment(t *testing.T) {
	t.Run("unset is the default", func(t *testing.T) {
		t.Setenv(MetricsListenEnv, "x") // restores whatever the environment had
		if err := os.Unsetenv(MetricsListenEnv); err != nil {
			t.Fatal(err)
		}
		addr, on := MetricsListen()
		if !on || addr != DefaultMetricsListen {
			t.Fatalf("got %q %v, want %q on", addr, on, DefaultMetricsListen)
		}
	})
	t.Run("empty turns it off", func(t *testing.T) {
		t.Setenv(MetricsListenEnv, "  ")
		if addr, on := MetricsListen(); on {
			t.Fatalf("got %q on, want off", addr)
		}
	})
	t.Run("an address is trimmed", func(t *testing.T) {
		t.Setenv(MetricsListenEnv, " 127.0.0.1:9199 ")
		if addr, on := MetricsListen(); !on || addr != "127.0.0.1:9199" {
			t.Fatalf("got %q %v", addr, on)
		}
	})
}

func TestMetricsExposition(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	m := NewMetrics(`v0.2.1 "test"`, func() time.Time { return now })

	body := scrape(t, m)
	for _, want := range []string{
		"# TYPE mishmar_signer_ready gauge\nmishmar_signer_ready 0\n",
		"# TYPE mishmar_signer_pending_requests gauge\nmishmar_signer_pending_requests 0\n",
		"# TYPE mishmar_signer_oldest_pending_seconds gauge\nmishmar_signer_oldest_pending_seconds 0\n",
		"mishmar_signer_build_info{version=\"v0.2.1 \\\"test\\\"\"} 1\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition lacks %q:\n%s", want, body)
		}
	}

	m.SetReady(true)
	if body := scrape(t, m); !strings.Contains(body, "\nmishmar_signer_ready 1\n") {
		t.Errorf("ready not reported:\n%s", body)
	}
	m.SetReady(false)
	if body := scrape(t, m); !strings.Contains(body, "\nmishmar_signer_ready 0\n") {
		t.Errorf("shutdown not reported:\n%s", body)
	}
}

func TestMetricsCountRequestsInFlight(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	m := NewMetrics("dev", func() time.Time { return now })

	entered := make(chan struct{})
	release := make(chan struct{})
	h := m.Track(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	done := make(chan struct{})
	for range 2 {
		go func() {
			defer func() { done <- struct{}{} }()
			res, err := http.Post(srv.URL+"/finish", "application/json", nil)
			if err == nil {
				res.Body.Close()
			}
		}()
	}
	<-entered
	<-entered
	now = now.Add(7 * time.Second)
	if n, oldest := m.Pending(); n != 2 || oldest != 7*time.Second {
		t.Fatalf("pending %d oldest %s, want 2 and 7s", n, oldest)
	}
	if body := scrape(t, m); !strings.Contains(body, "\nmishmar_signer_pending_requests 2\n") || !strings.Contains(body, "\nmishmar_signer_oldest_pending_seconds 7\n") {
		t.Errorf("in-flight requests not reported:\n%s", body)
	}
	close(release)
	<-done
	<-done
	if n, oldest := m.Pending(); n != 0 || oldest != 0 {
		t.Fatalf("pending %d oldest %s after the answers, want none", n, oldest)
	}
}

func TestMetricsHandlerServesOnlyMetrics(t *testing.T) {
	m := NewMetrics("dev", nil)
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("GET /metrics: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	for _, path := range []string{"/", "/healthz", "/keys"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, res.StatusCode)
		}
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	var b strings.Builder
	if _, err := m.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
