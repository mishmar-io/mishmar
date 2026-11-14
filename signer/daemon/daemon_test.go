package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/signin"
	"github.com/mishmar-io/mishmar/signer/signin/signintest"
)

// shortHome is a Mishmar home in a short temporary directory: a Unix
// socket's path takes about a hundred bytes, which t.TempDir's can pass.
func shortHome(t *testing.T) Home {
	t.Helper()
	dir, err := os.MkdirTemp("", "mm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return Home{Root: dir}
}

// local is this machine as a sign-in sees it in a test: not remote,
// whatever runs the test (a CI machine has no display).
func local() signin.Remote { return signin.Remote{} }

func logger() *slog.Logger {
	if os.Getenv("MISHMAR_TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.DiscardHandler)
}

func fast(s *signer.Signer) {
	s.PollWait, s.KeySyncInterval, s.PermissionInterval = time.Second, 20*time.Millisecond, 50*time.Millisecond
}

// served is a daemon serving in this test's process.
type served struct {
	d    *Daemon
	stop context.CancelFunc
	done chan error
}

func newDaemon(home Home, version string) *Daemon {
	return &Daemon{Manager: &Manager{Home: home, Version: version, Logger: logger(), Detect: local, Tune: fast}}
}

// serve runs d until the test ends; the test fails if it does not stop.
func serve(t *testing.T, d *Daemon) *served {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &served{d: d, stop: cancel, done: make(chan error, 1)}
	go func() { r.done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(20 * time.Second):
			t.Error("a daemon did not stop")
		}
	})
	waitHealthy(t, Dial(d.Manager.Home.Socket()))
	return r
}

func waitHealthy(t *testing.T, c *Client) Health {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		h, err := c.Health(ctx)
		cancel()
		if err == nil {
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// The local API answers only its own user's processes and its own API
// version; the socket is 0600 in a directory only the user can enter.
func TestLocalAPIChecksWhoAsks(t *testing.T) {
	home := shortHome(t)
	serve(t, newDaemon(home, "v0.5.0"))
	if m := mode(t, home.Socket()); m != 0o600 {
		t.Fatalf("socket mode %o", m)
	}
	if m := mode(t, home.RunDir()); m != 0o700 {
		t.Fatalf("run directory mode %o", m)
	}
	if m := mode(t, home.Info()); m != 0o600 {
		t.Fatalf("daemon.json mode %o", m)
	}
	c := Dial(home.Socket())
	h, err := c.Health(context.Background())
	if err != nil || h.Version != "v0.5.0" || h.API != APIVersion || h.PID != os.Getpid() {
		t.Fatalf("health %+v %v", h, err)
	}
	if _, err := c.Status(context.Background(), "claude-code"); err != nil {
		t.Fatalf("status: %v", err)
	}
	// A client of another local API version is refused everything but the handshake.
	for _, header := range []string{"", "1", "3"} {
		req, _ := http.NewRequest(http.MethodGet, "http://x/apps/claude-code/status", nil)
		if header != "" {
			req.Header.Set(HeaderAPI, header)
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusUpgradeRequired || !strings.Contains(string(body), "wrong_api_version") {
			t.Fatalf("API %q: %d %s", header, res.StatusCode, body)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "http://x/health", nil)
	if res, err := c.HTTP.Do(req); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("health without the header: %v %v", res, err)
	}
	// An app's name is a slug.
	if _, err := c.Status(context.Background(), "..%2F..%2Fetc"); !IsCode(err, "not_found") {
		t.Fatalf("a path for an app: %v", err)
	}
}

// A process of another user is refused whatever it asks: the daemon checks
// the peer's uid on every connection (here the daemon expects another uid
// than this test's).
func TestLocalAPIRefusesAnotherUser(t *testing.T) {
	home := shortHome(t)
	d := newDaemon(home, "v0.5.0")
	other := os.Getuid() + 1
	d.wantUID = &other
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	c := Dial(home.Socket())
	eventually(t, "the socket answers", func() bool {
		_, err := c.Health(context.Background())
		return IsCode(err, "other_user")
	})
	for _, call := range []func() error{
		func() error { _, err := c.Status(context.Background(), "claude-code"); return err },
		func() error { return c.Shutdown(context.Background(), ShutdownRequest{Reason: "stop"}) },
		func() error {
			_, err := c.SignIn(context.Background(), "claude-code", SignInRequest{API: "https://api.mishmar.io"})
			return err
		},
	} {
		if err := call(); !IsCode(err, "other_user") {
			t.Fatalf("another user's call: %v", err)
		}
	}
	select {
	case err := <-done:
		t.Fatalf("the daemon stopped for another user: %v", err)
	default:
	}
	// The peer's uid as the socket reports it is this process's.
	ln, err := net.Listen("unix", filepath.Join(home.Root, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := net.Dial("unix", filepath.Join(home.Root, "p.sock"))
		if err == nil {
			time.Sleep(200 * time.Millisecond)
			c.Close()
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if uid, err := peerUID(conn.(*net.UnixConn)); err != nil || uid != os.Getuid() {
		t.Fatalf("peer uid %d %v, want %d", uid, err, os.Getuid())
	}
}

// startIn is an EnsureOptions.Start that runs a daemon in this process, the
// way Spawn starts one detached, counting the ones that ran and the ones
// that found another holding the home.
type startIn struct {
	t       *testing.T
	home    Home
	version string
	mu      sync.Mutex
	ran     atomic.Int32
	refused atomic.Int32
	stops   []context.CancelFunc
	done    []chan struct{}
}

func (s *startIn) start(context.Context) error {
	d := newDaemon(s.home, s.version)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	s.stops, s.done = append(s.stops, cancel), append(s.done, done)
	s.mu.Unlock()
	go func() {
		defer close(done)
		err := d.Run(ctx)
		switch {
		case errors.Is(err, ErrRunning):
			s.refused.Add(1)
		case err == nil:
			s.ran.Add(1)
		default:
			s.t.Errorf("daemon: %v", err)
		}
	}()
	return nil
}

func (s *startIn) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, stop := range s.stops {
		stop()
		<-s.done[i]
	}
}

// Two MCP servers starting at once share one daemon: both start one, the
// home's lock lets one run, and both talk to it.
func TestTwoServersShareOneDaemon(t *testing.T) {
	home := shortHome(t)
	s := &startIn{t: t, home: home, version: "v0.5.0"}
	defer s.cleanup()
	var wg sync.WaitGroup
	healths := make([]Health, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, h, err := Ensure(context.Background(), EnsureOptions{Home: home, Version: "v0.5.0", Start: s.start})
			if err != nil {
				t.Errorf("ensure %d: %v", i, err)
			}
			healths[i] = h
		}()
	}
	wg.Wait()
	if !healths[0].StartedAt.Equal(healths[1].StartedAt) || healths[0].StartedAt.IsZero() {
		t.Fatalf("two daemons answered: %v and %v", healths[0].StartedAt, healths[1].StartedAt)
	}
	eventually(t, "every daemon started after the first gave way", func() bool {
		s.mu.Lock()
		n := len(s.stops)
		s.mu.Unlock()
		return n >= 1 && int(s.refused.Load()) == n-1
	})
	// A third server finds the daemon running and starts nothing.
	before := len(s.stops)
	if _, _, err := Ensure(context.Background(), EnsureOptions{Home: home, Version: "v0.5.0", Start: s.start}); err != nil {
		t.Fatal(err)
	}
	if len(s.stops) != before {
		t.Fatal("a daemon was started next to a running one")
	}
}

// A newer binary replaces an older daemon: it asks it to stop, waits until
// it has, and starts its own. An older one uses the newer daemon as it is,
// and may not stop it; a development build replaces nothing.
func TestUpgradeHandshake(t *testing.T) {
	home := shortHome(t)
	old := serve(t, newDaemon(home, "v0.5.0"))
	s := &startIn{t: t, home: home, version: "v0.5.1"}
	defer s.cleanup()
	c, h, err := Ensure(context.Background(), EnsureOptions{Home: home, Version: "v0.5.1", Start: s.start})
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != "v0.5.1" || len(s.stops) != 1 {
		t.Fatalf("after the upgrade: %+v, %d started", h, len(s.stops))
	}
	select {
	case err := <-old.done:
		if err != nil {
			t.Fatalf("the old daemon: %v", err)
		}
		old.done <- nil // for its cleanup
	case <-time.After(5 * time.Second):
		t.Fatal("the old daemon is still running")
	}
	// Never downgraded: an older binary uses the newer daemon.
	older := &startIn{t: t, home: home, version: "v0.5.0"}
	for _, v := range []string{"v0.5.0", "dev", "v0.5.1-rc.1"} {
		if _, h, err := Ensure(context.Background(), EnsureOptions{Home: home, Version: v, Start: older.start}); err != nil || h.Version != "v0.5.1" {
			t.Fatalf("%s: %+v %v", v, h, err)
		}
	}
	if len(older.stops) != 0 {
		t.Fatal("an older binary started a daemon")
	}
	if err := c.Shutdown(context.Background(), ShutdownRequest{Reason: "upgrade", Version: "v0.4.9"}); !IsCode(err, "not_newer") {
		t.Fatalf("an older binary's upgrade: %v", err)
	}
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("the daemon stopped for an older binary: %v", err)
	}
	// A person's stop is taken.
	if stopped, err := Stop(context.Background(), home); err != nil || !stopped {
		t.Fatalf("stop: %v %v", stopped, err)
	}
	if _, err := os.Stat(home.Socket()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket outlived the daemon: %v", err)
	}
	if stopped, err := Stop(context.Background(), home); err != nil || stopped {
		t.Fatalf("stop with none running: %v %v", stopped, err)
	}
}

// A daemon of another local API version is not used: the binary says what
// to do instead of misreading its answers.
func TestEnsureRefusesAnotherAPIVersion(t *testing.T) {
	home := shortHome(t)
	if err := PrivateDir(home.RunDir()); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", home.Socket())
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, Health{Version: "v0.9.0", API: 3})
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	s := &startIn{t: t, home: home, version: "v0.5.0"}
	_, _, err = Ensure(context.Background(), EnsureOptions{Home: home, Version: "v0.5.0", Start: s.start})
	if err == nil || !strings.Contains(err.Error(), "local API 3") {
		t.Fatalf("err %v", err)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.5.1", "v0.5.0", true}, {"v0.6.0", "v0.5.9", true}, {"v1.0.0", "v0.99.99", true}, {"v0.5.10", "v0.5.9", true},
		{"v0.5.0", "v0.5.0", false}, {"v0.5.0", "v0.5.1", false}, {"v0.5.0", "v0.5.0-rc.1", true}, {"v0.5.0-rc.1", "v0.5.0", false},
		{"v0.5.0-rc.2", "v0.5.0-rc.1", true}, {"dev", "v0.5.0", false}, {"v0.5.0", "dev", false}, {"0.5.1", "v0.5.0", false},
		{"v0.5", "v0.4.0", false}, {"v0.05.0", "v0.4.0", false}, {"v0.5.1+build", "v0.5.0", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestPrivateDir(t *testing.T) {
	home := shortHome(t)
	open := filepath.Join(home.Root, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(open); err != nil || mode(t, open) != 0o700 {
		t.Fatalf("an open directory: %v %o", err, mode(t, open))
	}
	link := filepath.Join(home.Root, "link")
	if err := os.Symlink(open, link); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(link); err == nil {
		t.Fatal("a link taken as a private directory")
	}
	nested := filepath.Join(home.Root, "a", "b")
	if err := PrivateDir(nested); err != nil || mode(t, nested) != 0o700 || mode(t, filepath.Dir(nested)) != 0o700 {
		t.Fatalf("nested: %v", err)
	}
}

// signedIn signs claude-code in through the daemon against the stand-in,
// the person approving, and returns the client.
func signedIn(t *testing.T, cp *signintest.Server, home Home) *Client {
	t.Helper()
	c := Dial(home.Socket())
	si, err := c.SignIn(context.Background(), "claude-code", SignInRequest{API: cp.URL, Environment: signintest.Environment, MachineName: "Test Mac"})
	if err != nil {
		t.Fatal(err)
	}
	if si.State != "waiting" || !si.Started || si.URL == "" || si.Code == "" {
		t.Fatalf("sign-in %+v", si)
	}
	again, err := c.SignIn(context.Background(), "claude-code", SignInRequest{API: cp.URL, Environment: signintest.Environment})
	if err != nil || again.Started || again.URL != si.URL || again.Code != si.Code {
		t.Fatalf("a second sign-in started another: %+v %v", again, err)
	}
	if st, _ := c.Status(context.Background(), "claude-code"); st.State != SigningIn || st.SignIn == nil || st.SignIn.Code != si.Code {
		t.Fatalf("status while waiting %+v", st)
	}
	id, err := cp.Open(si.URL)
	if err != nil {
		t.Fatal(err)
	}
	if req := cp.Requests()[len(cp.Requests())-1]; req.Code != si.Code || req.MachineName != "Test Mac" || req.App != "claude-code" {
		t.Fatalf("the page would show %+v", req)
	}
	if _, err := cp.Approve(id, signintest.Approval{}); err != nil {
		t.Fatal(err)
	}
	done, err := c.SignInState(context.Background(), "claude-code", 10*time.Second)
	if err != nil || done.State != string(signin.SignedIn) {
		t.Fatalf("after the approval: %+v %v", done, err)
	}
	return c
}

// The whole of an app's life in the daemon, against the stand-ins: sign-in
// with the person approving, the key's files private, the trading key made
// by the app's signer and reported waiting at the venue, then ready; a
// proposal living fifteen minutes; the key revoked in the console ending
// the app, which a new sign-in starts over.
func TestAnAppsLifeInTheDaemon(t *testing.T) {
	cp := signintest.New()
	defer cp.Close()
	home := shortHome(t)
	serve(t, newDaemon(home, "v0.5.0"))
	c := signedIn(t, cp, home)
	for path, want := range map[string]os.FileMode{home.Config("claude-code"): 0o600, home.Record("claude-code"): 0o600, home.AppDir("claude-code"): 0o700, home.AppsDir(): 0o700} {
		if m := mode(t, path); m != want {
			t.Fatalf("%s mode %o, want %o", path, m, want)
		}
	}
	cfg, err := signer.Load(home.Config("claude-code"))
	if err != nil {
		t.Fatal(err)
	}
	var st Status
	eventually(t, "the trading key is made", func() bool {
		st, err = c.Status(context.Background(), "claude-code")
		return err == nil && len(st.VenueKeys) == 1 && st.VenueKeys[0].Held
	})
	vk := st.VenueKeys[0]
	if st.State != SignedIn || st.Record == nil || st.Record.KeyID != cfg.KeyID || st.Code == "" || st.Permission != "trade" || !st.Signer.Running ||
		vk.Status != "pending" || vk.Keygen != "done" || !vk.Mine || !strings.HasPrefix(vk.PublicKey, "-----BEGIN PUBLIC KEY-----") ||
		st.ConnectedAccount == nil || st.ConnectedAccount.Name != "USDⓈ-M Futures" || st.Daemon.SurvivesReboot || st.Daemon.InProcess {
		t.Fatalf("status %+v", st)
	}
	cp.Engine.Activate(vk.ID)
	eventually(t, "the key is active", func() bool {
		st, _ = c.Status(context.Background(), "claude-code")
		return len(st.VenueKeys) == 1 && st.VenueKeys[0].Status == "active"
	})

	// A proposal through the daemon lives fifteen minutes unless it says.
	var d api.Decision
	price := "50000"
	if err := c.Do(context.Background(), http.MethodPost, "/apps/claude-code/propose", signer.ProposeRequest{
		ConnectedAccountID: signintest.ConnectedAccountID, Symbol: "BTCUSDT", Side: "buy", Size: "0.01", Price: &price,
	}, &d); err != nil {
		t.Fatal(err)
	}
	props := cp.Engine.Proposals()
	if d.Verdict != api.VerdictRequireApproval || len(props) != 1 || props[0].ExpiresAt.Sub(props[0].ProposedAt) != AppProposalLife || props[0].KeyId != cfg.KeyID {
		t.Fatalf("decision %+v, proposals %+v", d, props)
	}
	if err := c.Do(context.Background(), http.MethodPost, "/apps/claude-code/propose", signer.ProposeRequest{
		ConnectedAccountID: signintest.ConnectedAccountID, Symbol: "BTCUSDT", Side: "buy", Size: "0.01", Price: &price, ValidForSeconds: 3600,
	}, &d); err != nil {
		t.Fatal(err)
	}
	if props = cp.Engine.Proposals(); props[1].ExpiresAt.Sub(props[1].ProposedAt) != time.Hour {
		t.Fatalf("a life the model set: %v", props[1].ExpiresAt.Sub(props[1].ProposedAt))
	}
	// Another app is not signed in, and has none of this one's.
	if err := c.Do(context.Background(), http.MethodGet, "/apps/cursor/positions", nil, nil); !IsCode(err, "not_signed_in") {
		t.Fatalf("another app: %v", err)
	}

	// Revoked in the console: the signer's next call is refused, the app
	// ends, and its files are moved aside.
	cp.Engine.Revoke(cfg.KeyID)
	eventually(t, "the app ends", func() bool {
		st, _ = c.Status(context.Background(), "claude-code")
		return st.State == Ended
	})
	if st.Ended != "Key revoked" || st.Signer.Running {
		t.Fatalf("ended %+v", st)
	}
	if _, err := os.Stat(home.Config("claude-code")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the revoked key's config is still in place: %v", err)
	}
	if m, _ := filepath.Glob(home.Config("claude-code") + ".ended-*"); len(m) != 1 {
		t.Fatalf("moved aside: %v", m)
	}
	if err := c.Do(context.Background(), http.MethodGet, "/apps/claude-code/positions", nil, nil); !IsCode(err, "ended") {
		t.Fatalf("a call after the end: %v", err)
	}
	// A new sign-in makes a new key.
	si, err := c.SignIn(context.Background(), "claude-code", SignInRequest{API: cp.URL, Environment: signintest.Environment})
	if err != nil || !si.Started || si.Code == st.Code {
		t.Fatalf("signing in again: %+v %v", si, err)
	}
}

// A machine that looks remote, to the daemon or to the app's process, is
// refused before anything starts: no key, no listener, no file.
func TestRemoteSignInStartsNothing(t *testing.T) {
	home := shortHome(t)
	d := newDaemon(home, "v0.5.0")
	d.Manager.Detect = func() signin.Remote {
		return signin.Remote{Hints: []string{"ssh"}, Signals: []string{"SSH_CONNECTION"}}
	}
	serve(t, d)
	c := Dial(home.Socket())
	_, err := c.SignIn(context.Background(), "claude-code", SignInRequest{API: "https://api.mishmar.io"})
	if !IsCode(err, "remote") || !strings.Contains(err.Error(), "an SSH session (SSH_CONNECTION)") {
		t.Fatalf("err %v", err)
	}
	d.Manager.Detect = local
	if _, err := c.SignIn(context.Background(), "claude-code", SignInRequest{API: "https://api.mishmar.io", Hints: []string{"container"}}); !IsCode(err, "remote") {
		t.Fatalf("the app's own hint: %v", err)
	}
	st, err := c.Status(context.Background(), "claude-code")
	if err != nil || st.State != SignedOut || st.SignIn != nil {
		t.Fatalf("status %+v %v", st, err)
	}
	if entries, _ := os.ReadDir(home.AppsDir()); len(entries) != 0 {
		t.Fatalf("files were made: %v", entries)
	}
}

// The person declining, or a sign-in nobody answers, is reported once it
// ends; nothing is kept.
func TestDeniedSignIn(t *testing.T) {
	cp := signintest.New()
	defer cp.Close()
	home := shortHome(t)
	serve(t, newDaemon(home, "v0.5.0"))
	c := Dial(home.Socket())
	si, err := c.SignIn(context.Background(), "cursor", SignInRequest{API: cp.URL, Environment: signintest.Environment})
	if err != nil {
		t.Fatal(err)
	}
	id, err := cp.Open(si.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cp.Deny(id); err != nil {
		t.Fatal(err)
	}
	got, err := c.SignInState(context.Background(), "cursor", 5*time.Second)
	if err != nil || got.State != string(signin.Denied) || got.URL != "" {
		t.Fatalf("after a denial: %+v %v", got, err)
	}
	st, _ := c.Status(context.Background(), "cursor")
	if st.State != SignedOut || st.SignIn == nil || st.SignIn.State != "denied" {
		t.Fatalf("status %+v", st)
	}
	if _, err := os.Stat(home.Config("cursor")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a denied sign-in left a config")
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "msk_") {
		t.Fatal("a secret in the status")
	}
}
