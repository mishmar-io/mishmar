package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/configlock"
	"github.com/mishmar-io/mishmar/signer/signin"
)

// AppProposalLife is how long a proposal from an app lives when the model
// does not say: long enough for a person to see and approve it, and the
// longest an approved order waits for this signer.
const AppProposalLife = 15 * time.Minute

// DrainTimeout is how long a stopping signer lets the answers in flight
// finish.
const DrainTimeout = 10 * time.Second

// DefaultUnknownGrace is how long the engine must keep saying "Unknown key"
// before an app's key is taken as gone: an engine that lost the key ends
// it, a router that does not know it yet does not.
const DefaultUnknownGrace = 5 * time.Minute

// Manager holds the apps of one Mishmar home: their signers, their
// sign-ins and the local API over them. The daemon serves it on the
// socket; on Windows, or where no daemon can run, the MCP server runs it in
// its own process (InProcess).
type Manager struct {
	Home    Home
	Version string
	Logger  *slog.Logger
	// HTTP is what the sign-ins and the signers call Mishmar with; nil is
	// the default.
	HTTP *http.Client
	// Detect is this machine's remote check, run before every sign-in; nil
	// is signin.Detect(signin.Here()).
	Detect func() signin.Remote
	// MachineName names this computer on sign-in; empty is signin.MachineName.
	MachineName string
	// SignInTimeout bounds a sign-in (signin.DefaultTimeout).
	SignInTimeout time.Duration
	// Tune is called on each app's signer before it starts (tests shorten
	// its intervals).
	Tune func(*signer.Signer)
	// InProcess says the Manager runs inside the MCP server, which signs
	// only while the app is open.
	InProcess bool
	// UnknownGrace is DefaultUnknownGrace when zero.
	UnknownGrace time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	startedAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	apps      map[string]*app
	stopping  bool
	wg        sync.WaitGroup
}

// track counts a goroutine Stop waits for; false once Stop has begun, when
// nothing new may start.
func (m *Manager) track() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return false
	}
	m.wg.Add(1)
	return true
}

// app is one app on this computer.
type app struct {
	m    *Manager
	slug string

	mu      sync.Mutex
	flow    *signin.Flow
	last    *SignIn // the last sign-in, when it did not end signed in
	run     *running
	runErr  string
	record  *Record
	ended   string
	endedAt time.Time
}

// running is an app's signer.
type running struct {
	s      *signer.Signer
	local  http.Handler
	cancel context.CancelFunc
	done   chan struct{}
	lock   *configlock.Lock

	mu           sync.Mutex
	unknownSince time.Time
}

// Start loads every app with a key in the home and starts its signer.
func (m *Manager) Start(ctx context.Context) error {
	if m.Logger == nil {
		m.Logger = slog.New(slog.DiscardHandler)
	}
	if err := PrivateDir(m.Home.AppsDir()); err != nil {
		return err
	}
	m.mu.Lock()
	m.startedAt = m.now()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.apps = map[string]*app{}
	m.mu.Unlock()
	entries, err := os.ReadDir(m.Home.AppsDir())
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || !signin.ValidApp(e.Name()) {
			continue
		}
		if _, err := os.Stat(m.Home.Config(e.Name())); err == nil {
			a := m.app(e.Name())
			a.mu.Lock()
			a.startLocked()
			a.mu.Unlock()
		}
	}
	return nil
}

// Stop stops every app's signer, letting the answers in flight finish
// (DrainTimeout), and every sign-in waiting.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopping = true
	if m.cancel != nil {
		m.cancel()
	}
	apps := make([]*app, 0, len(m.apps))
	for _, a := range m.apps {
		apps = append(apps, a)
	}
	m.mu.Unlock()
	for _, a := range apps {
		a.mu.Lock()
		if a.flow != nil {
			a.flow.Cancel()
		}
		a.mu.Unlock()
	}
	m.wg.Wait()
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) app(slug string) *app {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.apps[slug]
	if !ok {
		a = &app{m: m, slug: slug}
		m.apps[slug] = a
	}
	return a
}

func (m *Manager) detect() signin.Remote {
	if m.Detect != nil {
		return m.Detect()
	}
	return signin.Detect(signin.Here())
}

func (m *Manager) unknownGrace() time.Duration {
	if m.UnknownGrace > 0 {
		return m.UnknownGrace
	}
	return DefaultUnknownGrace
}

// startLocked starts the app's signer from its config, under a.mu. A
// config another process holds (a signer run by hand on it, or an MCP
// server running it in its own process) is left to it.
func (a *app) startLocked() {
	if a.run != nil || a.ended != "" {
		return
	}
	if !a.m.track() {
		a.runErr = "the signer is stopping"
		return
	}
	started := false
	defer func() {
		if !started {
			a.m.wg.Done()
		}
	}()
	path := a.m.Home.Config(a.slug)
	cfg, err := signer.Load(path)
	if err != nil {
		a.runErr = err.Error()
		return
	}
	lock, err := configlock.Acquire(path)
	if err != nil {
		a.runErr = err.Error()
		return
	}
	key, err := cfg.Key()
	if err != nil {
		lock.Close()
		a.runErr = err.Error()
		return
	}
	r := &running{lock: lock, done: make(chan struct{})}
	client := signer.NewClient(cfg.Engine, cfg.KeyID, key)
	base := http.DefaultTransport
	if a.m.HTTP != nil && a.m.HTTP.Transport != nil {
		base = a.m.HTTP.Transport
	}
	client.HTTP = &http.Client{Transport: &watch{base: base, a: a, r: r}}
	s, err := signer.New(cfg, client, a.m.Logger.With("app", a.slug))
	if err != nil {
		lock.Close()
		a.runErr = err.Error()
		return
	}
	s.ConfigPath, s.CommitmentBatch, s.DrainTimeout = path, 8, DrainTimeout
	if a.m.Tune != nil {
		a.m.Tune(s)
	}
	r.s, r.local = s, s.LocalHandler("")
	var ctx context.Context
	ctx, r.cancel = context.WithCancel(a.m.ctx)
	a.run, a.runErr = r, ""
	if rec, err := readRecord(a.m.Home.Record(a.slug)); err == nil {
		a.record = rec
	}
	started = true
	go func() {
		defer a.m.wg.Done()
		defer close(r.done)
		defer r.lock.Close()
		_ = s.Run(ctx)
	}()
	a.m.Logger.Info("daemon: signer started", "app", a.slug, "agent", cfg.AgentID, "keyId", cfg.KeyID)
}

// end stops the app's signer because Mishmar no longer takes its key, and
// moves its files aside: a new sign-in makes a new key.
func (a *app) end(r *running, why string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run != r {
		return
	}
	r.cancel()
	<-r.done
	a.run = nil
	a.ended, a.endedAt = why, a.m.now()
	stamp := strconv.FormatInt(a.endedAt.Unix(), 10)
	for _, p := range []string{a.m.Home.Config(a.slug), a.m.Home.Record(a.slug)} {
		if err := os.Rename(p, p+".ended-"+stamp); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.m.Logger.Warn("daemon: moving an ended app's file aside", "path", p, "err", err)
		}
	}
	a.m.Logger.Warn("daemon: Mishmar no longer takes this app's key; its signer stopped", "app", a.slug, "why", why)
}

// watch sees the engine's answers to an app's signer: a key revoked or
// expired ends the app at once, one unknown for UnknownGrace too.
type watch struct {
	base http.RoundTripper
	a    *app
	r    *running
}

func (w *watch) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := w.base.RoundTrip(req)
	if err != nil {
		return res, err
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(body))
		var e api.Error
		_ = json.Unmarshal(body, &e)
		w.saw401(e.Message)
	case res.StatusCode < 300:
		w.r.mu.Lock()
		w.r.unknownSince = time.Time{}
		w.r.mu.Unlock()
	}
	return res, nil
}

func (w *watch) saw401(msg string) {
	switch msg {
	case "Key revoked", "Key expired":
		go w.a.end(w.r, msg)
	case "Unknown key":
		w.r.mu.Lock()
		now := w.a.m.now()
		if w.r.unknownSince.IsZero() {
			w.r.unknownSince = now
		}
		gone := now.Sub(w.r.unknownSince) >= w.a.m.unknownGrace()
		w.r.mu.Unlock()
		if gone {
			go w.a.end(w.r, "Unknown key")
		}
	}
}

// save writes what a sign-in made and starts the app's signer: the config
// and the record, each 0600 in the app's 0700 directory. An app with a key
// here already is not overwritten.
func (a *app) save(r signin.Result) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := PrivateDir(a.m.Home.AppDir(a.slug)); err != nil {
		return err
	}
	path := a.m.Home.Config(a.slug)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s holds a key for %s already", path, a.slug)
	}
	if err := r.Config.Save(path); err != nil {
		return err
	}
	g := r.Grant
	rec := &Record{
		App: r.App, API: r.API, Environment: g.Environment, MachineName: r.MachineName, MachineID: g.MachineID, AccountID: g.AccountID,
		AgentID: g.AgentID, AgentName: g.AgentName, KeyID: g.KeyID, KeyExpiresAt: g.KeyExpiresAt.UTC(), Fingerprint: r.Fingerprint,
		ConnectedAccountID: g.ConnectedAccountID, VenueKeyID: g.VenueKeyID, Next: g.Next, SignedInAt: r.SignedInAt,
	}
	if err := writePrivateJSON(a.m.Home.Record(a.slug), rec); err != nil {
		return err
	}
	a.record, a.ended, a.endedAt, a.last = rec, "", time.Time{}, nil
	a.startLocked()
	if a.run == nil {
		return fmt.Errorf("the key is saved but its signer did not start: %s", a.runErr)
	}
	return nil
}

func readRecord(path string) (*Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// writePrivateJSON writes v to path with mode 0600, atomically.
func writePrivateJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ---- the local API ----

// localError is an answer that is not a success, in the engine's error shape.
type localError struct {
	status        int
	code, message string
}

func (e *localError) Error() string { return e.code + ": " + e.message }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, api.Error{Code: code, Message: msg})
}

// Handler is the local API over the apps, without the transport's checks
// (the daemon's peer and version checks, Serve).
func (m *Manager) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, m.health()) })
	mux.HandleFunc("POST /apps/{slug}/sign-in", m.withApp(func(w http.ResponseWriter, r *http.Request, a *app) {
		var in SignInRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "validation", "body: "+err.Error())
			return
		}
		out, err := a.signIn(in)
		if err != nil {
			var le *localError
			if errors.As(err, &le) {
				writeErr(w, le.status, le.code, le.message)
				return
			}
			writeErr(w, http.StatusBadGateway, "unavailable", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("GET /apps/{slug}/sign-in", m.withApp(func(w http.ResponseWriter, r *http.Request, a *app) {
		wait := 0
		if v := r.URL.Query().Get("wait"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 60 {
				writeErr(w, http.StatusUnprocessableEntity, "validation", "wait is 0 to 60 seconds")
				return
			}
			wait = n
		}
		writeJSON(w, http.StatusOK, a.signInState(r.Context(), time.Duration(wait)*time.Second))
	}))
	mux.HandleFunc("GET /apps/{slug}/status", m.withApp(func(w http.ResponseWriter, r *http.Request, a *app) {
		writeJSON(w, http.StatusOK, a.status(r.Context(), r.URL.Query().Get("brief") == "1"))
	}))
	mux.HandleFunc("/apps/{slug}/{rest...}", m.withApp(forward))
	return mux
}

func (m *Manager) withApp(h func(http.ResponseWriter, *http.Request, *app)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		if !signin.ValidApp(slug) {
			writeErr(w, http.StatusNotFound, "not_found", "no app "+strconv.Quote(slug)+": an app is up to 32 lowercase letters, digits and dashes")
			return
		}
		h(w, r, m.app(slug))
	}
}

// forward serves an app's own local API (signer.LocalHandler: propose,
// the decisions, the reads) under /apps/{slug}/, with a proposal's life
// AppProposalLife when it names none.
func forward(w http.ResponseWriter, r *http.Request, a *app) {
	a.mu.Lock()
	a.reconcileLocked()
	run, ended := a.run, a.ended
	a.mu.Unlock()
	if run == nil {
		if ended != "" {
			writeErr(w, http.StatusConflict, "ended", "Mishmar no longer takes this computer's key for "+a.slug+" ("+ended+"); sign in again")
			return
		}
		writeErr(w, http.StatusConflict, "not_signed_in", "this computer is not signed in to Mishmar for "+a.slug)
		return
	}
	rest := "/" + r.PathValue("rest")
	if r.Method == http.MethodPost && rest == "/propose" {
		var in signer.ProposeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "validation", "body: "+err.Error())
			return
		}
		if in.ValidForSeconds == 0 {
			in.ValidForSeconds = int(AppProposalLife / time.Second)
		}
		raw, _ := json.Marshal(in)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path, r2.URL.RawPath = rest, ""
	run.local.ServeHTTP(w, r2)
}

// reconcileLocked starts the signer of an app with a key here that does
// not run (its config freed by another process since).
func (a *app) reconcileLocked() {
	if a.run != nil || a.ended != "" || a.flow != nil {
		return
	}
	if _, err := os.Stat(a.m.Home.Config(a.slug)); err == nil {
		a.startLocked()
	}
}

func (m *Manager) health() Health {
	h := Health{Version: m.Version, API: APIVersion, PID: os.Getpid(), StartedAt: m.startedAt, InProcess: m.InProcess, Apps: []AppBrief{}}
	m.mu.Lock()
	apps := make([]*app, 0, len(m.apps))
	for _, a := range m.apps {
		apps = append(apps, a)
	}
	m.mu.Unlock()
	for _, a := range apps {
		a.mu.Lock()
		h.Apps = append(h.Apps, AppBrief{Slug: a.slug, State: a.stateLocked()})
		a.mu.Unlock()
	}
	sort.Slice(h.Apps, func(i, j int) bool { return h.Apps[i].Slug < h.Apps[j].Slug })
	return h
}

func (a *app) stateLocked() AppState {
	switch {
	case a.run != nil:
		return SignedIn
	case a.flow != nil:
		return SigningIn
	case a.ended != "":
		return Ended
	}
	return SignedOut
}

// signIn starts the app's sign-in, or answers the one waiting. A machine
// that looks remote, by the caller's hints or this process's own look, is
// refused before anything starts.
func (a *app) signIn(in SignInRequest) (SignIn, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reconcileLocked()
	if a.run != nil {
		return SignIn{State: string(signin.SignedIn)}, nil
	}
	if a.flow != nil {
		return flowState(a.flow, false), nil
	}
	remote := a.m.detect()
	for _, h := range in.Hints {
		remote.Hints = append(remote.Hints, h)
		remote.Signals = append(remote.Signals, "the app's process: "+h)
	}
	if remote.Refused() {
		return SignIn{}, &localError{http.StatusConflict, "remote", "this machine looks like " + remote.Describe() + ", and signing in to trade from a remote machine is not supported yet"}
	}
	if _, err := os.Stat(a.m.Home.Config(a.slug)); err == nil {
		return SignIn{}, &localError{http.StatusConflict, "in_use", "this app's key is here but its signer could not start: " + a.runErr}
	}
	f, err := signin.Start(a.m.ctx, signin.Params{
		API: in.API, Environment: in.Environment, App: a.slug, MachineName: firstNonEmpty(in.MachineName, a.m.MachineName), CLIVersion: a.m.Version,
		Timeout: a.m.SignInTimeout, Save: a.save, HTTP: a.m.HTTP, Now: a.m.Now, Logger: a.m.Logger,
	})
	if err != nil {
		return SignIn{}, &localError{http.StatusUnprocessableEntity, "validation", err.Error()}
	}
	if !a.m.track() {
		f.Cancel()
		return SignIn{}, &localError{http.StatusServiceUnavailable, "unavailable", "the background signer is stopping"}
	}
	a.flow, a.last, a.ended, a.endedAt = f, nil, "", time.Time{}
	go func() {
		defer a.m.wg.Done()
		<-f.Done()
		a.mu.Lock()
		defer a.mu.Unlock()
		if st, _ := f.Status(); st != signin.SignedIn {
			last := flowState(f, false)
			a.last = &last
		}
		if a.flow == f {
			a.flow = nil
		}
	}()
	return flowState(f, true), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func flowState(f *signin.Flow, started bool) SignIn {
	st, err := f.Status()
	out := SignIn{State: string(st), URL: f.URL, Code: f.Code, Fingerprint: f.Fingerprint, ExpiresAt: f.ExpiresAt.UTC(), Started: started}
	if err != nil {
		out.Error = err.Error()
	}
	if st != signin.Waiting {
		out.URL = ""
	}
	return out
}

// signInState is the app's sign-in as it stands, after waiting up to wait
// for a waiting one to end.
func (a *app) signInState(ctx context.Context, wait time.Duration) SignIn {
	a.mu.Lock()
	f := a.flow
	a.mu.Unlock()
	if f != nil && wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-f.Done():
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	if f != nil {
		if st, _ := f.Status(); st != signin.Waiting {
			// The watcher records it a moment later; answer from the flow.
			return flowState(f, false)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.run != nil:
		return SignIn{State: string(signin.SignedIn)}
	case a.flow != nil:
		return flowState(a.flow, false)
	case a.last != nil:
		return *a.last
	}
	return SignIn{State: "none"}
}

// status is everything the status tool says about the app; brief leaves
// out what needs the engine (the trading keys, the account's name), for a
// check before each tool.
func (a *app) status(ctx context.Context, brief bool) Status {
	a.mu.Lock()
	a.reconcileLocked()
	out := Status{
		App: a.slug, State: a.stateLocked(), Ended: a.ended, EndedAt: a.endedAt,
		Signer: SignerState{Running: a.run != nil, Error: a.runErr},
		Daemon: DaemonInfo{Version: a.m.Version, PID: os.Getpid(), InProcess: a.m.InProcess},
	}
	if a.flow != nil {
		st := flowState(a.flow, false)
		out.SignIn = &st
	} else if a.last != nil {
		last := *a.last
		out.SignIn = &last
	}
	if a.record != nil {
		rec := *a.record
		out.Record, out.Code = &rec, agentkey.FingerprintCode(rec.Fingerprint)
	}
	run := a.run
	a.mu.Unlock()
	if run == nil || out.Record == nil {
		return out
	}
	if p, known := run.s.Permission(); known {
		out.Permission = string(p)
	}
	if brief {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if ca, err := run.s.Client.ConnectedAccount(ctx, out.Record.ConnectedAccountID); err == nil {
		out.ConnectedAccount = &AccountBrief{ID: ca.Id, Name: ca.Name, Venue: string(ca.Venue), ConnectionName: ca.Connection.Name}
	}
	if run.s.Client.ReadOnly() {
		return out
	}
	keys, err := run.s.Client.VenueKeys(ctx)
	if err != nil {
		out.ReadError = err.Error()
		return out
	}
	held := run.s.HeldVenueKeys()
	for _, k := range keys {
		if k.Status == api.VenueKeyStatusRevoked {
			continue
		}
		v := VenueKeyState{ID: k.Id, Venue: string(k.Venue), ConnectedAccountID: k.ConnectedAccountId, Status: string(k.Status), APIKeyRegistered: k.ApiKeyRegistered, ExpiresAt: k.ExpiresAt}
		if k.Keygen != nil {
			v.Keygen = string(*k.Keygen)
		}
		if k.PublicKey != nil {
			v.PublicKey = *k.PublicKey
		}
		if k.Hyperliquid != nil {
			v.HyperliquidStatus = string(k.Hyperliquid.ApprovalStatus)
		}
		v.Mine = k.AgentKeyId == nil || *k.AgentKeyId == "" || *k.AgentKeyId == out.Record.KeyID
		if h, ok := held[k.Id]; ok {
			v.Held, v.Network = true, h.Network
		}
		if k.Venue == api.VenueHyperliquid {
			v.Name = signer.HyperliquidAgentName(out.Record.AgentID)
		}
		out.VenueKeys = append(out.VenueKeys, v)
	}
	return out
}

// SortedApps is the apps with a directory in the home, for a person's
// status command.
func SortedApps(h Home) []string {
	entries, err := os.ReadDir(h.AppsDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && signin.ValidApp(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
