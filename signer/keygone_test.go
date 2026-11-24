package signer

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

// The engine's answers in the scripts of goneEngine.
const (
	ok       = "ok"
	unknown  = "Unknown key"
	revoked  = "Key revoked"
	expired  = "Key expired"
	badSig   = "Invalid signature"
	trouble  = "500"
	warnLine = "signer: the engine refused this key (Unknown key). If the agent or its key was removed in the console, that is why. The signer stops if this goes on for five minutes."
)

// goneEngine answers the signer channel and GET /agent/me from scripts: each
// poll takes the next answer of polls, then the answers of cycle in turn
// (none: ok); each GET /agent/me the next of mes, then meCycle (none: ok,
// a trade agent). An ok poll is an empty list; a 401 carries the answer as
// its message; "500" is the engine failing.
type goneEngine struct {
	mu            sync.Mutex
	polls, cycle  []string
	mes, meCycle  []string
	polled, asked int
	goodPolls     int
	seen          []string
}

func next(script, cycle []string, i int) string {
	switch {
	case i < len(script):
		return script[i]
	case len(cycle) > 0:
		return cycle[(i-len(script))%len(cycle)]
	}
	return ok
}

func (g *goneEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	g.mu.Lock()
	g.seen = append(g.seen, r.Method+" "+path)
	answer, body := "404", any(api.Error{Code: "not_found", Message: "no"})
	switch path {
	case "/agent/signer/poll":
		answer = next(g.polls, g.cycle, g.polled)
		g.polled++
		if answer == ok {
			g.goodPolls++
		}
		body = []api.SignRequest{}
	case "/agent/me":
		answer = next(g.mes, g.meCycle, g.asked)
		g.asked++
		body = json.RawMessage(tradeMe)
	case "/agent/venue-keys":
		answer, body = ok, []api.VenueKey{}
	}
	g.mu.Unlock()
	status := http.StatusOK
	switch answer {
	case ok:
	case "404":
		status = http.StatusNotFound
	case trouble:
		status, body = http.StatusInternalServerError, api.Error{Code: "internal", Message: "engine trouble"}
	default:
		status, body = http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: answer}
	}
	if path == "/agent/signer/poll" && status == http.StatusOK {
		time.Sleep(5 * time.Millisecond)
	}
	writeJSON(w, status, body)
}

func (g *goneEngine) counts() (polled, asked, seen int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.polled, g.asked, len(g.seen)
}

// fakeClock is the signer's clock, moved by the test.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// lines keeps the messages logged at WARN.
type lines struct {
	mu   sync.Mutex
	warn []string
}

func (l *lines) Enabled(context.Context, slog.Level) bool { return true }
func (l *lines) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *lines) WithGroup(string) slog.Handler            { return l }
func (l *lines) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		l.mu.Lock()
		l.warn = append(l.warn, r.Message)
		l.mu.Unlock()
	}
	return nil
}

func (l *lines) count(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.warn {
		if m == msg {
			n++
		}
	}
	return n
}

type goneRun struct {
	s      *Signer
	g      *goneEngine
	clock  *fakeClock
	log    *lines
	done   chan error
	cancel context.CancelFunc
}

// runGone starts Run for a signer on g, with a short backoff, the key sync
// on, a fake clock and GET /agent/me asked again only hourly; tune, when
// set, changes the signer before Run.
func runGone(t *testing.T, g *goneEngine, tune func(*Signer)) *goneRun {
	t.Helper()
	lo, hi := minBackoff, maxBackoff
	minBackoff, maxBackoff = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { minBackoff, maxBackoff = lo, hi })
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	log := &lines{}
	s, err := New(&Config{Engine: srv.URL + "/v1", KeyID: "key_1", Secret: agentkey.Secret("msk_abc", testSeed(4)), AgentID: "agt_1", VenueKeys: map[string]VenueKey{}}, nil, slog.New(log))
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	s.Now = clock.Now
	s.ConfigPath = filepath.Join(t.TempDir(), "agent.json")
	s.PollWait, s.KeySyncInterval, s.PermissionInterval = time.Second, 5*time.Millisecond, time.Hour
	if tune != nil {
		tune(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(cancel)
	return &goneRun{s: s, g: g, clock: clock, log: log, done: done, cancel: cancel}
}

func (r *goneRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

// running checks Run has not returned.
func (r *goneRun) running(t *testing.T) {
	t.Helper()
	select {
	case err := <-r.done:
		t.Fatalf("Run returned while it should go on: %v", err)
	default:
	}
}

// pollsOn waits for n more polls, then checks Run has not returned.
func (r *goneRun) pollsOn(t *testing.T, n int) {
	t.Helper()
	from, _, _ := r.g.counts()
	eventually(t, "the signer polls on", func() bool { p, _, _ := r.g.counts(); return p >= from+n })
	r.running(t)
}

// stops cancels Run and checks it ends with no error.
func (r *goneRun) stops(t *testing.T) {
	t.Helper()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run after cancel: %v", err)
	}
}

func (r *goneRun) unknownFor5m() string {
	return "signer: stopped. " + r.s.Client.Engine + " has refused this key for five minutes (Unknown key). The agent or its key was most likely removed in the console. Add the agent again or issue a new key, then start the signer with it."
}

func (r *goneRun) refusedText(msg string) string {
	return "signer: stopped. " + r.s.Client.Engine + " refused this key (" + msg + "). Issue a new key in the console, then start the signer with it."
}

// nothingAfter checks the signer sends the engine nothing once Run has
// returned.
func (r *goneRun) nothingAfter(t *testing.T) {
	t.Helper()
	// A call sent just before Run returned (the key sync's first, cancelled
	// in flight) can still be on its way to the engine: let it land first.
	time.Sleep(100 * time.Millisecond)
	_, _, before := r.g.counts()
	time.Sleep(100 * time.Millisecond)
	if _, _, after := r.g.counts(); after != before {
		r.g.mu.Lock()
		defer r.g.mu.Unlock()
		t.Fatalf("%d calls after Run returned: %v", after-before, r.g.seen[before:])
	}
}

func TestUnknownKeyForFiveMinutesStopsTheSigner(t *testing.T) {
	r := runGone(t, &goneEngine{cycle: []string{unknown}}, nil)
	r.pollsOn(t, 3)
	if n := r.log.count(warnLine); n != 1 {
		t.Fatalf("WARN line logged %d times, want 1", n)
	}
	r.clock.add(5*time.Minute - time.Second)
	r.pollsOn(t, 3)
	r.clock.add(time.Second)
	err := r.wait(t)
	if want := r.unknownFor5m(); err == nil || err.Error() != want {
		t.Fatalf("Run = %v, want %q", err, want)
	}
	if n := r.log.count(warnLine); n != 1 {
		t.Fatalf("WARN line logged %d times, want 1", n)
	}
	r.nothingAfter(t)
}

func TestAGoodPollForgetsUnknownKey(t *testing.T) {
	g := &goneEngine{cycle: []string{unknown}}
	r := runGone(t, g, nil)
	r.pollsOn(t, 3)
	r.clock.add(4 * time.Minute)
	r.pollsOn(t, 2)
	// The next poll answers, then the refusals go on.
	g.mu.Lock()
	g.polls = make([]string, g.polled+1)
	for i := range g.polls {
		g.polls[i] = unknown
	}
	g.polls[g.polled] = ok
	g.mu.Unlock()
	eventually(t, "a good poll", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.goodPolls == 1 })
	r.pollsOn(t, 3)
	if n := r.log.count(warnLine); n != 2 {
		t.Fatalf("WARN line logged %d times, want 2", n)
	}
	r.clock.add(4 * time.Minute)
	r.pollsOn(t, 3)
	r.stops(t)
}

func TestARevokedOrExpiredKeyStopsTheSignerAtOnce(t *testing.T) {
	for _, msg := range []string{revoked, expired} {
		t.Run(msg, func(t *testing.T) {
			r := runGone(t, &goneEngine{cycle: []string{msg}}, nil)
			err := r.wait(t)
			if want := r.refusedText(msg); err == nil || err.Error() != want {
				t.Fatalf("Run = %v, want %q", err, want)
			}
			if p, _, _ := r.g.counts(); p != 1 {
				t.Fatalf("polled %d times, want 1", p)
			}
			r.nothingAfter(t)
		})
	}
}

func TestUnknownKeyAtStartStopsTheSigner(t *testing.T) {
	r := runGone(t, &goneEngine{meCycle: []string{unknown}}, nil)
	eventually(t, "GET /agent/me asked again", func() bool { _, a, _ := r.g.counts(); return a >= 3 })
	r.running(t)
	if n := r.log.count(warnLine); n != 1 {
		t.Fatalf("WARN line logged %d times, want 1", n)
	}
	r.clock.add(5 * time.Minute)
	err := r.wait(t)
	if want := r.unknownFor5m(); err == nil || err.Error() != want {
		t.Fatalf("Run = %v, want %q", err, want)
	}
	if p, _, _ := r.g.counts(); p != 0 {
		t.Fatalf("polled %d times, want 0", p)
	}
	r.nothingAfter(t)
}

func TestARevokedKeyAtStartStopsTheSignerAtOnce(t *testing.T) {
	r := runGone(t, &goneEngine{meCycle: []string{revoked}}, nil)
	err := r.wait(t)
	if want := r.refusedText(revoked); err == nil || err.Error() != want {
		t.Fatalf("Run = %v, want %q", err, want)
	}
	if p, a, _ := r.g.counts(); p != 0 || a != 1 {
		t.Fatalf("polled %d times and asked GET /agent/me %d times, want 0 and 1", p, a)
	}
}

func TestKeepPollingWhenRefused(t *testing.T) {
	r := runGone(t, &goneEngine{cycle: []string{unknown}}, func(s *Signer) { s.KeepPollingWhenRefused = true })
	r.pollsOn(t, 3)
	r.clock.add(10 * time.Minute)
	r.pollsOn(t, 3)
	if n := r.log.count(warnLine); n != 0 {
		t.Fatalf("WARN line logged %d times, want 0", n)
	}
	r.stops(t)
}

func TestAnotherRefusalDoesNotStopTheSigner(t *testing.T) {
	r := runGone(t, &goneEngine{cycle: []string{badSig}}, nil)
	r.pollsOn(t, 3)
	r.clock.add(10 * time.Minute)
	r.pollsOn(t, 3)
	if n := r.log.count(warnLine); n != 0 {
		t.Fatalf("WARN line logged %d times, want 0", n)
	}
	r.stops(t)
}

func TestFailedPollsDoNotForgetUnknownKey(t *testing.T) {
	r := runGone(t, &goneEngine{cycle: []string{unknown, trouble, trouble}}, nil)
	r.pollsOn(t, 4)
	r.clock.add(5 * time.Minute)
	err := r.wait(t)
	if want := r.unknownFor5m(); err == nil || err.Error() != want {
		t.Fatalf("Run = %v, want %q", err, want)
	}
	if n := r.log.count(warnLine); n != 1 {
		t.Fatalf("WARN line logged %d times, want 1", n)
	}
}
