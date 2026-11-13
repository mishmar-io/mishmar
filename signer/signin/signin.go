// Package signin is the trade sign-in's client: how a computer gets its own
// agent key registered at Mishmar for an AI app that is to trade, with the
// person approving in the browser (contract 0.21.0; the wiki's
// design/app-trading-plan.md, section 2.2; the bytes it left open are
// settled in docs/trade-sign-in.md).
//
// The computer makes an Ed25519 key and keeps it. It opens
// {api}/oauth/authorize for the built-in public client mishmar-cli, with a
// loopback redirect on 127.0.0.1 at a random port, PKCE (S256), a state, the
// trade resource and scope, and the key's public half with the computer's
// name, the app and this build's version. The person sees the key's code on
// the approval page and compares it with the one the app showed. The
// callback carries a code; the computer redeems it at {api}/oauth/token with
// a proof that it holds the key (agentkey.MachineProof), and the answer
// names the agent, the key id Mishmar registered the public half under and
// the engine. The computer checks the answer against what it asked for,
// checks the key with the engine (GET /agent/me signed with it), renders its
// own API key from the key id and its seed, and only then hands the config
// to be saved. No bearer token is ever issued: the engine key is the grant.
//
// Not built: signing in by typing a code on another device. A machine that
// looks remote (Detect) is refused before anything starts.
package signin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
)

// The trade sign-in's fixed values.
const (
	// ClientID is the built-in public client, the only one that may ask for
	// the trade resource.
	ClientID = "mishmar-cli"
	// Resource and Scope are what it asks for.
	Resource = "urn:mishmar:trade"
	Scope    = "trade"
	// TokenType is the token step's token_type: the grant is the agent key
	// itself, so no access or refresh token comes with it.
	TokenType = "mishmar_agent_key"
	// DefaultTimeout is how long a sign-in waits for the person: the
	// control plane's request lives ten minutes.
	DefaultTimeout = 10 * time.Minute
	// MaxKeyLife is the longest a registered trade key may live
	// (AgentKeyRegister.expiresAt: at most 366 days ahead).
	MaxKeyLife = 366 * 24 * time.Hour
	// tokenTimeout bounds the token step and the engine's check.
	tokenTimeout = signer.SetupTimeout
)

// Params is one sign-in.
type Params struct {
	// API is the hosting API's base URL (https://api.mishmar.io): where
	// /oauth/authorize and /oauth/token are, the issuer the callback's iss
	// must name, and the host the engine is reached through.
	API string
	// Environment is the hosted environment the key's secret names (prod,
	// testnet, stage); empty takes the token answer's.
	Environment string
	// App is the app's slug (claude-code), sent as app.
	App string
	// MachineName is this computer's name as the person knows it; empty
	// takes MachineName().
	MachineName string
	// CLIVersion is this build's version, sent as cli_version.
	CLIVersion string
	// Hints is machine_hint: what makes this machine look remote. A sign-in
	// with any is refused in this version (Detect, Remote.Refused).
	Hints []string
	// Timeout is how long the flow waits for the callback (DefaultTimeout).
	Timeout time.Duration
	// Save is called with the result once everything is checked, before the
	// browser is sent on; an error fails the sign-in.
	Save func(Result) error
	// HTTP makes the token step and the engine's check; nil is a client
	// that follows no redirect.
	HTTP *http.Client
	// Now is the clock; nil is time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// Grant is what the token step answers about the computer's grant.
type Grant struct {
	MachineID          string    `json:"machineId"`
	AccountID          string    `json:"accountId"`
	AgentID            string    `json:"agentId"`
	AgentName          string    `json:"agentName"`
	KeyID              string    `json:"keyId"`
	KeyExpiresAt       time.Time `json:"keyExpiresAt"`
	Environment        string    `json:"environment"`
	Engine             string    `json:"engine"`
	ConnectedAccountID string    `json:"connectedAccountId"`
	VenueKeyID         string    `json:"venueKeyId,omitempty"`
	// Fingerprint is the fingerprint of the public key the control plane
	// registered: this computer's, or the answer is refused.
	Fingerprint string `json:"fingerprint"`
	// Next is the console's page for what is left (the venue step).
	Next string `json:"next,omitempty"`
}

// TokenAnswer is the token step's 200 answer.
type TokenAnswer struct {
	TokenType string `json:"token_type"`
	Scope     string `json:"scope"`
	Mishmar   Grant  `json:"mishmar"`
	// A trade grant has no bearer token; one in the answer is refused.
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// Result is a finished sign-in: the signer config to save and what the
// control plane said about the grant.
type Result struct {
	Config      *signer.Config
	Grant       Grant
	Fingerprint string
	API         string
	App         string
	MachineName string
	SignedInAt  time.Time
	// Me is the engine's GET /agent/me with the new key; zero when the
	// engine could not be reached then (its signer asks again).
	Me api.AgentIdentity
}

// Status is where a sign-in stands.
type Status string

// The statuses. Waiting is the only one that is not final.
const (
	Waiting  Status = "waiting"
	SignedIn Status = "signed_in"
	Denied   Status = "denied"
	Expired  Status = "expired"
	Failed   Status = "failed"
)

// ErrDenied is a sign-in the person declined; ErrExpired one nobody
// answered in time.
var (
	ErrDenied  = errors.New("signin: the person declined this computer's sign-in")
	ErrExpired = errors.New("signin: nobody approved this computer's sign-in in time")
)

// Flow is a sign-in under way: the link to open, the code to compare, and
// its outcome once the callback came (or the time ran out).
type Flow struct {
	// URL is the authorize link the browser opens.
	URL string
	// Fingerprint is the new key's; Code its first two groups, what the
	// approval page must show.
	Fingerprint string
	Code        string
	// ExpiresAt is when the flow stops waiting.
	ExpiresAt time.Time
	// RedirectURI is the loopback address the callback comes to.
	RedirectURI string

	p        Params
	key      *agentkey.Key
	seed     []byte
	verifier string
	state    string
	ln       net.Listener
	srv      *http.Server

	mu      sync.Mutex
	claimed bool // a callback with the right state is being handled
	status  Status
	err     error
	result  Result
	done    chan struct{}
	once    sync.Once
}

// Start makes the key, listens on the loopback address and returns the
// flow, waiting for the callback. Nothing is written until the sign-in
// succeeds (Params.Save).
func Start(ctx context.Context, p Params) (*Flow, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	if p.Timeout <= 0 {
		p.Timeout = DefaultTimeout
	}
	if p.Logger == nil {
		p.Logger = slog.New(slog.DiscardHandler)
	}
	if p.MachineName == "" {
		p.MachineName = MachineName()
	}
	p.MachineName = CleanName(p.MachineName)
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	verifier, err := randomString(32)
	if err != nil {
		return nil, err
	}
	state, err := randomString(24)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("signin: listening on 127.0.0.1 for the browser's return: %w", err)
	}
	key := agentkey.FromSeed(seed)
	f := &Flow{
		Fingerprint: key.Fingerprint(),
		ExpiresAt:   p.now().Add(p.Timeout),
		RedirectURI: "http://127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port) + "/callback",
		p:           p, key: key, seed: seed, verifier: verifier, state: state, ln: ln,
		status: Waiting, done: make(chan struct{}),
	}
	f.Code = agentkey.FingerprintCode(f.Fingerprint)
	f.URL = f.authorizeURL()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", f.callback)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page(w, http.StatusNotFound, "Nothing here", "This address only takes Mishmar's answer to this computer's sign-in.")
	})
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	go f.watch(ctx)
	p.Logger.Info("signin: waiting for the person to approve this computer", "app", p.App, "code", f.Code, "expiresAt", f.ExpiresAt.UTC().Format(time.RFC3339))
	return f, nil
}

func (p Params) check() error {
	u, err := url.Parse(p.API)
	switch {
	case p.API == "" || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Path != "" && u.Path != "/" || u.RawQuery != "":
		return fmt.Errorf("signin: the API address %q is not a base URL (https://api.mishmar.io)", p.API)
	case u.Scheme == "http" && !loopbackHost(u.Hostname()):
		return fmt.Errorf("signin: the API address %q is plain http on a host that is not this computer; only https is taken", p.API)
	case !ValidApp(p.App):
		return fmt.Errorf("signin: app %q is not a slug of up to 32 lowercase letters, digits and dashes", p.App)
	case len(p.Hints) > 0:
		return fmt.Errorf("signin: this machine looks remote (%s), and signing in from a remote machine is not supported yet", strings.Join(p.Hints, ", "))
	case p.Environment != "" && !agentkey.ValidEnvironment(p.Environment):
		return fmt.Errorf("signin: environment %q is not a lowercase word", p.Environment)
	}
	return nil
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Issuer is the API base URL as the callback's iss and the proof name it:
// without a trailing slash.
func (p Params) Issuer() string { return strings.TrimRight(p.API, "/") }

func (p Params) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p Params) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// authorizeURL is the link the person opens.
func (f *Flow) authorizeURL() string {
	sum := sha256.Sum256([]byte(f.verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {f.RedirectURI},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {f.state},
		"resource":              {Resource},
		"scope":                 {Scope},
		"agent_key":             {base64.RawURLEncoding.EncodeToString(f.key.PublicKey())},
		"machine_name":          {f.p.MachineName},
		"app":                   {f.p.App},
		"machine_hint":          {strings.Join(f.p.Hints, ",")},
		"cli_version":           {f.p.CLIVersion},
	}
	return f.p.Issuer() + "/oauth/authorize?" + q.Encode()
}

// watch ends a flow nobody answered: at its expiry, or when ctx ends.
func (f *Flow) watch(ctx context.Context) {
	t := time.NewTimer(time.Until(f.ExpiresAt))
	defer t.Stop()
	select {
	case <-f.done:
	case <-t.C:
		f.finish(Expired, ErrExpired, Result{})
	case <-ctx.Done():
		f.finish(Expired, fmt.Errorf("signin: stopped before the person answered: %w", ctx.Err()), Result{})
	}
}

// finish records the outcome once, closes the listener once the handler in
// flight has answered, and wakes the waiters.
func (f *Flow) finish(st Status, err error, r Result) {
	f.once.Do(func() {
		f.mu.Lock()
		f.status, f.err, f.result = st, err, r
		f.mu.Unlock()
		close(f.done)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = f.srv.Shutdown(ctx)
		}()
		// The key's seed is in the config now, or no longer needed.
		if st != SignedIn {
			clear(f.seed)
		}
	})
}

// Done is closed when the flow has an outcome.
func (f *Flow) Done() <-chan struct{} { return f.done }

// Status is where the flow stands, and why it failed when it did.
func (f *Flow) Status() (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.err
}

// Wait waits for the outcome, or until ctx ends (the flow goes on).
func (f *Flow) Wait(ctx context.Context) (Result, error) {
	select {
	case <-f.done:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.result, f.err
}

// Cancel ends a flow that waits; the callback is no longer taken.
func (f *Flow) Cancel() { f.finish(Failed, errors.New("signin: cancelled"), Result{}) }

// callback is the browser coming back from the approval page.
func (f *Flow) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := r.URL.Query()
	for k, v := range q {
		if len(v) > 1 {
			page(w, http.StatusBadRequest, "Not taken", "The answer named "+k+" twice. Close this tab and ask the app again.")
			return
		}
	}
	got := q.Get("state")
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(f.state)) != 1 {
		// Not this flow's answer: whoever sent it learns nothing and the flow goes on.
		page(w, http.StatusBadRequest, "Not this computer's sign-in", "This answer is not for the sign-in this computer started. Close this tab and use the link the app gave.")
		return
	}
	f.mu.Lock()
	if f.claimed || f.status != Waiting {
		f.mu.Unlock()
		page(w, http.StatusConflict, "Already answered", "This computer's sign-in has been answered already. Close this tab and go back to the app.")
		return
	}
	f.claimed = true
	f.mu.Unlock()
	// RFC 9207: the answer must say it is from the Mishmar this computer asked.
	if iss := q.Get("iss"); iss != f.p.Issuer() {
		err := fmt.Errorf("signin: the answer came from %q, not %q, the Mishmar this computer asked; nothing was kept", iss, f.p.Issuer())
		f.finish(Failed, err, Result{})
		page(w, http.StatusBadRequest, "Not taken", "The answer did not come from the Mishmar this computer asked, so nothing was kept. Close this tab and ask the app again.")
		return
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			f.finish(Denied, ErrDenied, Result{})
			page(w, http.StatusOK, "Not signed in", "You declined, and this computer was not signed in. You can close this tab.")
			return
		}
		err := fmt.Errorf("signin: Mishmar answered %s: %s", e, q.Get("error_description"))
		f.finish(Failed, err, Result{})
		page(w, http.StatusOK, "Not signed in", "Mishmar did not sign this computer in ("+e+"). Close this tab and ask the app again.")
		return
	}
	code := q.Get("code")
	if code == "" || len(code) > 512 {
		f.finish(Failed, errors.New("signin: the answer carried no code"), Result{})
		page(w, http.StatusBadRequest, "Not taken", "The answer carried no code. Close this tab and ask the app again.")
		return
	}
	// The browser may go before the answer does; the code is single use, so
	// the token step finishes either way.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), tokenTimeout)
	defer cancel()
	res, err := f.redeem(ctx, code)
	if err == nil && f.p.Save != nil {
		if err = f.p.Save(res); err != nil {
			err = fmt.Errorf("signin: saving this computer's key: %w", err)
		}
	}
	if err != nil {
		f.p.Logger.Warn("signin: refused", "app", f.p.App, "err", err)
		f.finish(Failed, err, Result{})
		page(w, http.StatusOK, "Not signed in", "This computer could not finish signing in: "+err.Error()+". Close this tab and ask the app again.")
		return
	}
	f.finish(SignedIn, nil, res)
	f.p.Logger.Info("signin: signed in", "app", f.p.App, "agent", res.Grant.AgentID, "keyId", res.Grant.KeyID, "code", f.Code)
	if next := res.Grant.Next; validNext(next) {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	page(w, http.StatusOK, "Signed in", "This computer is signed in to Mishmar for "+f.p.App+". You can close this tab and go back to the app.")
}

// validNext says whether the console link may take the browser on: an
// absolute https URL (http only for this computer, in development).
func validNext(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http" && loopbackHost(u.Hostname())
}

// redeem is the token step with the proof, then the checks.
func (f *Flow) redeem(ctx context.Context, code string) (Result, error) {
	proof, signedAt, err := f.key.SignMachineProof(code, f.p.Issuer(), f.p.now())
	if err != nil {
		return Result{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {f.verifier},
		"client_id":     {ClientID},
		"redirect_uri":  {f.RedirectURI},
		"resource":      {Resource},
		"key_proof":     {proof},
		"key_signed_at": {signedAt},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.p.Issuer()+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := f.p.client().Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("signin: the token step: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("signin: the token step: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return Result{}, fmt.Errorf("signin: Mishmar refused the token step (%s): %s", e.Error, e.Description)
		}
		return Result{}, fmt.Errorf("signin: the token step answered %d", res.StatusCode)
	}
	var tok TokenAnswer
	if err := json.Unmarshal(body, &tok); err != nil {
		return Result{}, fmt.Errorf("signin: the token step's answer: %w", err)
	}
	cfg, err := f.check(tok)
	if err != nil {
		return Result{}, err
	}
	out := Result{Config: cfg, Grant: tok.Mishmar, Fingerprint: f.Fingerprint, API: f.p.Issuer(), App: f.p.App, MachineName: f.p.MachineName, SignedInAt: f.p.now().UTC()}
	out.Me, err = f.checkEngine(ctx, cfg, tok.Mishmar)
	if err != nil {
		return Result{}, err
	}
	return out, nil
}

// check holds the token answer against what this computer asked for, and
// renders the config from it.
func (f *Flow) check(tok TokenAnswer) (*signer.Config, error) {
	g := tok.Mishmar
	accountSuffix, _ := strings.CutPrefix(g.AccountID, "acc_")
	keyParts := strings.Split(g.KeyID, "_")
	wantEngine := f.p.Issuer() + "/a/" + g.AccountID + "/v1"
	now := f.p.now()
	switch {
	case tok.TokenType != TokenType:
		return nil, fmt.Errorf("signin: the token step answered token_type %q, not %s", tok.TokenType, TokenType)
	case tok.Scope != Scope:
		return nil, fmt.Errorf("signin: the token step answered scope %q, not %s", tok.Scope, Scope)
	case tok.AccessToken != "" || tok.RefreshToken != "":
		return nil, errors.New("signin: the token step answered a bearer token, which a trade sign-in never has; nothing was kept")
	case g.Fingerprint != f.Fingerprint:
		return nil, fmt.Errorf("signin: Mishmar registered a key with fingerprint %q, not this computer's %s; nothing was kept", g.Fingerprint, f.Fingerprint)
	case g.AgentID == "" || g.AccountID == "" || accountSuffix == "" || g.ConnectedAccountID == "":
		return nil, errors.New("signin: the token step's answer names no agent, account or connected account")
	case len(keyParts) != 3 || keyParts[0] != "key" || keyParts[1] != accountSuffix:
		return nil, fmt.Errorf("signin: key %q is not a key of account %s", g.KeyID, g.AccountID)
	case f.p.Environment != "" && g.Environment != f.p.Environment:
		return nil, fmt.Errorf("signin: the key is for environment %q, not %q", g.Environment, f.p.Environment)
	case g.Engine != wantEngine:
		return nil, fmt.Errorf("signin: the engine %q is not %s, where this Mishmar serves account %s", g.Engine, wantEngine, g.AccountID)
	case !g.KeyExpiresAt.After(now):
		return nil, errors.New("signin: the key Mishmar registered has expired already")
	case g.KeyExpiresAt.After(now.Add(MaxKeyLife)):
		return nil, errors.New("signin: the key Mishmar registered lives longer than a year, which no trade key may")
	}
	secret, err := agentkey.NewSecret(g.Environment, g.KeyID, f.seed)
	if err != nil {
		return nil, fmt.Errorf("signin: %w", err)
	}
	cfg := &signer.Config{Engine: g.Engine, KeyID: g.KeyID, Secret: secret, AgentID: g.AgentID, VenueKeys: map[string]signer.VenueKey{}}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("signin: %w", err)
	}
	return cfg, nil
}

// checkEngine asks the engine who the new key is, signed with it: the
// engine verifies the signature with the public half registered under the
// key id, so an answer proves Mishmar holds this computer's key. A refusal
// fails the sign-in; an engine that cannot be reached does not (the key is
// registered, and the signer asks again when it starts).
func (f *Flow) checkEngine(ctx context.Context, cfg *signer.Config, g Grant) (api.AgentIdentity, error) {
	c := signer.NewClient(cfg.Engine, cfg.KeyID, f.key)
	c.Timeout, c.HTTP = tokenTimeout, f.p.HTTP
	me, err := c.Me(ctx)
	var refused *signer.Error
	switch {
	case errors.As(err, &refused) && refused.Status < 500 && refused.Status != http.StatusTooManyRequests:
		return api.AgentIdentity{}, fmt.Errorf("signin: Mishmar's engine does not take this computer's key (%d %s: %s); nothing was kept", refused.Status, refused.Code, refused.Message)
	case err != nil:
		f.p.Logger.Warn("signin: the engine could not be asked about the new key; the signer asks when it starts", "err", err)
		return api.AgentIdentity{}, nil
	case me.KeyId != g.KeyID || me.AgentId != g.AgentID || me.AccountId != g.AccountID:
		return api.AgentIdentity{}, fmt.Errorf("signin: the engine answered for key %s of agent %s, not key %s of agent %s; nothing was kept", me.KeyId, me.AgentId, g.KeyID, g.AgentID)
	case me.KeyScope != nil && *me.KeyScope != api.AgentKeyScopeTrade:
		return api.AgentIdentity{}, fmt.Errorf("signin: the engine registered the key with scope %s, not trade; nothing was kept", *me.KeyScope)
	}
	return me, nil
}

// randomString is n random bytes, base64url without padding.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// page answers the browser with a plain page.
func page(w http.ResponseWriter, status int, title, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Mishmar · %s</title>`+
		`<body style="font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem"><h1 style="font-size:1.25rem">%s</h1><p>%s</p>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
}
