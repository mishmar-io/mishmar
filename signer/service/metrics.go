package service

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The metrics listener. A cell's signer answers GET /metrics on it in the
// Prometheus text format, and GKE's managed collection scrapes it for the
// alerts in infra-core's tf/monitoring. It is a listener of its own, plain
// HTTP, apart from the API: what it says is counts and ages, never a share,
// a key id or a request's body, so it needs neither mTLS nor the socket.
const (
	// MetricsListenEnv names the metrics listener's address. Unset means
	// DefaultMetricsListen; set and empty means no metrics listener.
	MetricsListenEnv = "MISHMAR_SIGNER_METRICS_LISTEN"
	// DefaultMetricsListen is the port the cell's PodMonitoring expects.
	DefaultMetricsListen = ":9103"
)

// MetricsListen is the metrics listener's address from the environment, and
// whether there is to be one.
func MetricsListen() (string, bool) {
	v, set := os.LookupEnv(MetricsListenEnv)
	if !set {
		return DefaultMetricsListen, true
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

// Metrics is what the signer service says about itself: four gauges, written
// by hand in the Prometheus text format, so the binary takes on no client
// library for them.
//
//	mishmar_signer_ready                   1 while the API is being served, 0 before and while it shuts down
//	mishmar_signer_pending_requests        the API requests being served right now
//	mishmar_signer_oldest_pending_seconds  how long the oldest of them has been in progress; 0 with none
//	mishmar_signer_build_info{version}     always 1; the version is the label
//
// A request is pending from the moment the API handler takes it until it
// answers: a finish that waits on KMS or a slow key generation shows up here
// and nowhere else.
type Metrics struct {
	version string
	now     func() time.Time
	ready   atomic.Bool

	mu   sync.Mutex
	next uint64
	open map[uint64]time.Time
}

// NewMetrics makes the gauges for a build. now is for tests; nil means time.Now.
func NewMetrics(version string, now func() time.Time) *Metrics {
	if now == nil {
		now = time.Now
	}
	return &Metrics{version: version, now: now, open: map[uint64]time.Time{}}
}

// SetReady says whether the API is being served.
func (m *Metrics) SetReady(ready bool) { m.ready.Store(ready) }

// Ready is mishmar_signer_ready.
func (m *Metrics) Ready() bool { return m.ready.Load() }

// Track wraps the API so that every request in flight is counted, from the
// handler taking it to its answer.
func (m *Metrics) Track(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := m.begin()
		defer m.end(id)
		h.ServeHTTP(w, r)
	})
}

func (m *Metrics) begin() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	m.open[m.next] = m.now()
	return m.next
}

func (m *Metrics) end(id uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.open, id)
}

// Pending is how many requests are in flight and how long the oldest has
// been; zero with none.
func (m *Metrics) Pending() (int, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var oldest time.Duration
	for _, started := range m.open {
		if age := now.Sub(started); age > oldest {
			oldest = age
		}
	}
	return len(m.open), oldest
}

// WriteTo writes the exposition.
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	pending, oldest := m.Pending()
	ready := 0.0
	if m.Ready() {
		ready = 1
	}
	var b strings.Builder
	gauge(&b, "mishmar_signer_ready", "1 while the signer service is serving its API.", "", ready)
	gauge(&b, "mishmar_signer_pending_requests", "The API requests being served right now.", "", float64(pending))
	gauge(&b, "mishmar_signer_oldest_pending_seconds", "How long the oldest request being served has been in progress; 0 with none.", "", oldest.Seconds())
	gauge(&b, "mishmar_signer_build_info", "The running build; the version is the label.", `{version="`+escapeLabel(m.version)+`"}`, 1)
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// Handler serves GET /metrics, and nothing else.
func (m *Metrics) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = m.WriteTo(w)
	})
	return mux
}

func gauge(b *strings.Builder, name, help, labels string, value float64) {
	b.WriteString("# HELP " + name + " " + help + "\n")
	b.WriteString("# TYPE " + name + " gauge\n")
	b.WriteString(name + labels + " " + strconv.FormatFloat(value, 'g', -1, 64) + "\n")
}

// escapeLabel is the text format's escaping of a label value: backslash,
// double quote and line feed.
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}
