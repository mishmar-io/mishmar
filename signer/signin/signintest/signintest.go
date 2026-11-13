// Package signintest is a stand-in for tests: the hosted control plane's
// trade sign-in (GET /oauth/authorize, POST /oauth/token) and the engine an
// account's agents reach behind it (/a/{account}/v1), written from the
// wiki's design/app-trading-plan.md section 2.2 and docs/trade-sign-in.md,
// which settles the bytes the plan left open. It checks what the real ones
// must check, so the control plane's own tests can be held to the same
// expectations: the built-in client and its loopback redirect, PKCE, the
// public key on the authorize request and its fingerprint, the proof of
// possession at the token step (agentkey.VerifyMachineProof), a single-use
// two-minute code, and a 200 answer with no bearer token. The person is
// played by the test: Open is the browser following the link, Approve and
// Deny the approval page.
//
// The engine part is small: agentAuth on every /agent call (Unknown key,
// Key revoked, Key expired), the key's scope (403 key_scope), GET /agent/me,
// the agent's trading keys with FROST key generation for a Binance key the
// approval set aside naming the computer's key as its holder from creation
// (VenueKeyCreate.agentKeyId; another key's key generation is 409), issued
// keys beside registered ones, an idle signer channel, and proposals
// recorded and parked for approval.
package signintest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/proposal"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/signin"
)

// The stand-in's fixed values: one account, one connected account at
// Binance, the environment its keys name.
const (
	AccountID          = "acc_standin"
	ConnectedAccountID = "ta_standin"
	Environment        = "testnet"
	// CodeTTL is how long a code may wait for the token step.
	CodeTTL = 2 * time.Minute
	// KeyLife is the life of a machine's key (the plan's default).
	KeyLife = 180 * 24 * time.Hour
)

// Request is an authorize request as the stand-in stored it.
type Request struct {
	ID, RedirectURI, State, Challenge string
	// AgentKey is agent_key as sent (base64url); PublicKey the same key as
	// the API writes it (base64 standard).
	AgentKey, PublicKey string
	Fingerprint, Code   string
	MachineName, App    string
	Hint, CLIVersion    string
	CreatedAt           time.Time

	authCode    string
	codeExpires time.Time
	used        bool
	grant       signin.Grant
}

// Server is the stand-in: the control plane and the engine on one address.
type Server struct {
	URL    string
	Engine *Engine

	srv *httptest.Server
	mu  sync.Mutex
	seq int
	// Now is the clock; nil is time.Now.
	Now      func() time.Time
	requests map[string]*Request
	codes    map[string]*Request
	refused  []string
	// What an Approval changed about a code's token step.
	proofKeys map[string]string
	answers   map[string]func(signin.TokenAnswer) any
}

// New starts the stand-in; Close stops it.
func New() *Server {
	s := &Server{requests: map[string]*Request{}, codes: map[string]*Request{}, proofKeys: map[string]string{}, answers: map[string]func(signin.TokenAnswer) any{}}
	s.Engine = newEngine(s.now)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.Handle("/a/"+AccountID+"/v1/", http.StripPrefix("/a/"+AccountID+"/v1", s.Engine))
	mux.HandleFunc("GET /console/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "the console: "+r.URL.Path)
	})
	s.srv = httptest.NewServer(mux)
	s.URL = s.srv.URL
	return s
}

// Close stops the stand-in.
func (s *Server) Close() { s.srv.Close() }

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Refused is why the stand-in refused each request it refused, in order.
func (s *Server) Refused() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.refused...)
}

func (s *Server) refuse(why string) string {
	s.mu.Lock()
	s.refused = append(s.refused, why)
	s.mu.Unlock()
	return why
}

// Requests lists the authorize requests made, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, 0, len(s.requests))
	for i := 1; i <= s.seq; i++ {
		if r, ok := s.requests["oar_"+strconv.Itoa(i)]; ok {
			out = append(out, *r)
		}
	}
	return out
}

var (
	loopbackRedirect = regexp.MustCompile(`^http://(127\.0\.0\.1|\[::1\]):[0-9]{1,5}/callback$`)
	appSlug          = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// authorize is GET /oauth/authorize for the trade resource: every parameter
// once, the built-in client with a loopback redirect, PKCE S256, the trade
// resource and scope, a 32-byte key in base64url, a name, an app slug and
// hints from the known set. Taken, it redirects to the console's approval
// page; refused, it answers a plain 400 page and redirects nowhere.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fail := func(why string) { http.Error(w, s.refuse(why), http.StatusBadRequest) }
	for k, v := range q {
		if len(v) > 1 {
			fail(k + " twice")
			return
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.Get("agent_key"))
	switch {
	case q.Get("client_id") != signin.ClientID:
		fail("client_id must be " + signin.ClientID)
	case !loopbackRedirect.MatchString(q.Get("redirect_uri")):
		fail("redirect_uri must be http://127.0.0.1:<port>/callback or http://[::1]:<port>/callback")
	case q.Get("response_type") != "code":
		fail("response_type must be code")
	case q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43:
		fail("PKCE S256 is required")
	case q.Get("state") == "" || len(q.Get("state")) > 2000:
		fail("state is required")
	case q.Get("resource") != signin.Resource || q.Get("scope") != signin.Scope:
		fail("resource must be " + signin.Resource + " and scope " + signin.Scope)
	case err != nil || len(raw) != ed25519.PublicKeySize:
		fail("agent_key must be a 32-byte Ed25519 public key, base64url without padding")
	case q.Get("machine_name") == "" || len([]rune(q.Get("machine_name"))) > 64 || signin.CleanName(q.Get("machine_name")) != q.Get("machine_name"):
		fail("machine_name must be a clean name of up to 64 characters")
	case !appSlug.MatchString(q.Get("app")):
		fail("app must be a slug of up to 32 characters")
	case !knownHints(q.Get("machine_hint")):
		fail("machine_hint must be empty or a comma list of ssh, container, cloud")
	case q.Get("cli_version") == "":
		fail("cli_version is required")
	default:
		pub := ed25519.PublicKey(raw)
		fp := agentkey.Fingerprint(pub)
		s.mu.Lock()
		s.seq++
		req := &Request{
			ID: "oar_" + strconv.Itoa(s.seq), RedirectURI: q.Get("redirect_uri"), State: q.Get("state"), Challenge: q.Get("code_challenge"),
			AgentKey: q.Get("agent_key"), PublicKey: base64.StdEncoding.EncodeToString(pub), Fingerprint: fp, Code: agentkey.FingerprintCode(fp),
			MachineName: q.Get("machine_name"), App: q.Get("app"), Hint: q.Get("machine_hint"), CLIVersion: q.Get("cli_version"), CreatedAt: s.now(),
		}
		s.requests[req.ID] = req
		s.mu.Unlock()
		http.Redirect(w, r, s.URL+"/console/connect?request="+req.ID, http.StatusFound)
	}
}

func knownHints(v string) bool {
	if v == "" {
		return true
	}
	for _, h := range strings.Split(v, ",") {
		if h != "ssh" && h != "container" && h != "cloud" {
			return false
		}
	}
	return true
}

// Open is the browser following the link the app gave: it answers the
// request's id, or why the authorize step refused it.
func (s *Server) Open(link string) (string, error) {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(link)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusFound {
		return "", fmt.Errorf("authorize answered %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	u, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		return "", err
	}
	return u.Query().Get("request"), nil
}

// Approval is how the person's approval goes, for tests of what the
// computer refuses. The zero value is a proper approval.
type Approval struct {
	// Issuer replaces the iss on the redirect (a mix-up).
	Issuer string
	// State replaces the state on the redirect.
	State string
	// RegisterKey registers this public key (base64) instead of the
	// request's: a control plane that registered the wrong key.
	RegisterKey string
	// ProofKey checks the proof against this key (base64) instead of the
	// one on the request: the token step refuses this computer's proof.
	ProofKey string
	// Grant changes the token answer before it is sent.
	Grant func(*signin.Grant)
	// Answer replaces the token step's 200 body.
	Answer func(signin.TokenAnswer) any
	// Scope is the registered key's scope (trade when empty).
	Scope api.AgentKeyScope
}

// Approve is the person approving the request on the page: the agent is
// made (a trade agent named after the app) with the key registered by its
// public half for 180 days and a Binance trading key set aside, a code is
// issued, and the browser follows the redirect to the computer's loopback
// address. It returns the computer's answer to the browser.
func (s *Server) Approve(id string, a Approval) (*http.Response, error) {
	s.mu.Lock()
	req, ok := s.requests[id]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no request %s", id)
	}
	pub := req.PublicKey
	if a.RegisterKey != "" {
		pub = a.RegisterKey
	}
	scope := a.Scope
	if scope == "" {
		scope = api.AgentKeyScopeTrade
	}
	agent := s.Engine.AddAgent(appName(req.App), api.AgentPermissionTrade)
	expires := s.now().Add(KeyLife).UTC().Truncate(time.Second)
	key := s.Engine.Register(agent, pub, scope, expires)
	vk := s.Engine.AddVenueKey(agent, key)
	fp, _ := agentkey.FingerprintOf(pub)
	code := "mac_" + randomish(id)
	s.mu.Lock()
	req.authCode, req.codeExpires = code, s.now().Add(CodeTTL)
	req.grant = signin.Grant{
		MachineID: "mch_" + strings.TrimPrefix(id, "oar_"), AccountID: AccountID, AgentID: agent, AgentName: appName(req.App),
		KeyID: key, KeyExpiresAt: expires, Environment: Environment, Engine: s.URL + "/a/" + AccountID + "/v1",
		ConnectedAccountID: ConnectedAccountID, VenueKeyID: vk, Fingerprint: fp, Next: s.URL + "/console/agents/" + agent + "?setup=1",
	}
	if a.Grant != nil {
		a.Grant(&req.grant)
	}
	s.codes[code] = req
	s.proofKeys[code], s.answers[code] = a.ProofKey, a.Answer
	s.mu.Unlock()
	p := url.Values{"code": {code}, "state": {req.State}, "iss": {s.URL}}
	if a.Issuer != "" {
		p.Set("iss", a.Issuer)
	}
	if a.State != "" {
		p.Set("state", a.State)
	}
	return browse(req.RedirectURI + "?" + p.Encode())
}

// Deny is the person declining on the page.
func (s *Server) Deny(id string) (*http.Response, error) {
	s.mu.Lock()
	req, ok := s.requests[id]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no request %s", id)
	}
	p := url.Values{"error": {"access_denied"}, "error_description": {"the person declined"}, "state": {req.State}, "iss": {s.URL}}
	return browse(req.RedirectURI + "?" + p.Encode())
}

// browse is the browser following a redirect to the computer, without
// following the computer's own redirect on.
func browse(link string) (*http.Response, error) {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 90 * time.Second}
	res, err := c.Get(link)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	res.Body = io.NopCloser(strings.NewReader(string(body)))
	return res, nil
}

func appName(slug string) string {
	switch slug {
	case "claude-code":
		return "Claude Code"
	case "cursor":
		return "Cursor"
	}
	return slug
}

func randomish(seed string) string {
	sum := sha256.Sum256([]byte(seed + time.Now().String()))
	return base64.RawURLEncoding.EncodeToString(sum[:18])
}

// token is POST /oauth/token for the trade grant: a form, every parameter
// once, no client secret; the code known, unused, under two minutes old,
// issued to this client for this redirect; the PKCE verifier; the trade
// resource; and the proof of possession of the key the authorize request
// named, within 60 seconds. The code is spent by the first try, right or
// wrong.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int, code, why string) {
		s.refuse(code + ": " + why)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": why})
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/x-www-form-urlencoded" {
		fail(http.StatusBadRequest, "invalid_request", "the body must be form-encoded")
		return
	}
	if err := r.ParseForm(); err != nil {
		fail(http.StatusBadRequest, "invalid_request", "the body is not a form")
		return
	}
	f := r.PostForm
	for k, v := range f {
		if len(v) > 1 {
			fail(http.StatusBadRequest, "invalid_request", k+" was sent twice")
			return
		}
	}
	if r.Header.Get("Authorization") != "" || f.Has("client_secret") {
		fail(http.StatusUnauthorized, "invalid_client", "mishmar-cli has no secret")
		return
	}
	if f.Get("grant_type") != "authorization_code" {
		fail(http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code")
		return
	}
	s.mu.Lock()
	req, ok := s.codes[f.Get("code")]
	spent := ok && req.used
	if ok {
		req.used = true
	}
	s.mu.Unlock()
	if !ok || spent || !s.now().Before(req.codeExpires) {
		fail(http.StatusBadRequest, "invalid_grant", "the code is unknown, used or expired")
		return
	}
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	s.mu.Lock()
	proofKey, answer := s.proofKeys[f.Get("code")], s.answers[f.Get("code")]
	s.mu.Unlock()
	if proofKey == "" {
		proofKey = req.PublicKey
	}
	switch {
	case f.Get("client_id") != signin.ClientID:
		fail(http.StatusBadRequest, "invalid_grant", "the code was issued to another client")
	case f.Get("redirect_uri") != req.RedirectURI:
		fail(http.StatusBadRequest, "invalid_grant", "redirect_uri is not the one the code was issued for")
	case f.Get("resource") != signin.Resource:
		fail(http.StatusBadRequest, "invalid_target", "resource must be "+signin.Resource)
	case subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(req.Challenge)) != 1:
		fail(http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
	case f.Get("key_proof") == "" || f.Get("key_signed_at") == "":
		fail(http.StatusBadRequest, "invalid_request", "key_proof and key_signed_at are required")
	default:
		why := agentkey.VerifyMachineProof(agentkey.MachineProof{Code: f.Get("code"), Issuer: s.URL, PublicKey: proofKey, SignedAt: f.Get("key_signed_at")}, f.Get("key_proof"), s.now())
		if why != "" {
			fail(http.StatusBadRequest, "invalid_grant", why)
			return
		}
		out := signin.TokenAnswer{TokenType: signin.TokenType, Scope: signin.Scope, Mishmar: req.grant}
		var body any = out
		if answer != nil {
			body = answer(out)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
}

// Token posts a form to the token step directly, for tests of its refusals.
func (s *Server) Token(form url.Values) (*http.Response, error) {
	return http.PostForm(s.URL+"/oauth/token", form)
}

// ---- the engine ----

// Engine is the stand-in's engine for account AccountID.
type Engine struct {
	now func() time.Time

	mu        sync.Mutex
	seq       int
	agents    map[string]*agent
	keys      map[string]*key
	venueKeys []api.VenueKey
	dkg       map[string]*frost.Keygen
	dkgKey    map[string]string // the key that opened each key generation
	nonces    map[string]bool
	decisions map[string]api.Decision
	proposals []api.ProposalSubmit
	calls     []string
}

type agent struct {
	id, name   string
	permission api.AgentPermission
}

type key struct {
	id, agent, pub string
	scope          api.AgentKeyScope
	expires        time.Time
	revoked        bool
	// issued: the engine made the pair (a bot's API key); else registered
	// by its public half (a trading machine).
	issued bool
}

func newEngine(now func() time.Time) *Engine {
	return &Engine{now: now, agents: map[string]*agent{}, keys: map[string]*key{}, dkg: map[string]*frost.Keygen{}, dkgKey: map[string]string{},
		nonces: map[string]bool{}, decisions: map[string]api.Decision{}}
}

func (e *Engine) next(prefix string) string {
	e.seq++
	return fmt.Sprintf("%s%012d", prefix, e.seq)
}

// AddAgent makes an agent and returns its id.
func (e *Engine) AddAgent(name string, perm api.AgentPermission) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.next("agt_")
	e.agents[id] = &agent{id: id, name: name, permission: perm}
	return id
}

// Register registers a public key (base64) on an agent and returns its id,
// key_<account suffix>_<12 characters> as the engine names keys.
func (e *Engine) Register(agentID, pub string, scope api.AgentKeyScope, expires time.Time) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	id := fmt.Sprintf("key_%s_%012d", strings.TrimPrefix(AccountID, "acc_"), e.seq)
	e.keys[id] = &key{id: id, agent: agentID, pub: pub, scope: scope, expires: expires}
	return id
}

// Issue adds a key the engine made (issueAgentKey: a bot's API key) by its
// public half, with no end, and returns its id.
func (e *Engine) Issue(agentID, pub string, scope api.AgentKeyScope) string {
	id := e.Register(agentID, pub, scope, time.Time{})
	e.mu.Lock()
	e.keys[id].issued = true
	e.mu.Unlock()
	return id
}

// AddVenueKey sets a Binance trading key aside on the agent, waiting for
// key generation (createVenueKey). holder, when not empty, is the
// registered key whose signer must generate it (VenueKeyCreate.agentKeyId):
// named on the key from creation, and another key's keygen is 409.
func (e *Engine) AddVenueKey(agentID, holder string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.next("vk_")
	kg := api.VenueKeyKeygenPending
	vk := api.VenueKey{Id: id, AgentId: agentID, ConnectedAccountId: ConnectedAccountID, Venue: api.VenueBinance,
		Scheme: api.VenueKeySchemeEd25519, Status: api.VenueKeyStatusPending, Keygen: &kg, CreatedAt: e.now().UTC()}
	if holder != "" {
		vk.AgentKeyId = &holder
	}
	e.venueKeys = append(e.venueKeys, vk)
	return id
}

// Keygens is the key ids that ran a key generation round, one per round.
func (e *Engine) Keygens() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, c := range e.calls {
		if strings.HasPrefix(c, "POST /agent/signer/keygen by ") {
			out = append(out, strings.TrimPrefix(c, "POST /agent/signer/keygen by "))
		}
	}
	return out
}

// Activate marks a trading key active, as the venue step does.
func (e *Engine) Activate(venueKeyID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.venueKeys {
		if e.venueKeys[i].Id == venueKeyID {
			e.venueKeys[i].Status = api.VenueKeyStatusActive
			e.venueKeys[i].ApiKeyRegistered = true
		}
	}
}

// VenueKeys is the trading keys as the engine holds them.
func (e *Engine) VenueKeys() []api.VenueKey {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]api.VenueKey(nil), e.venueKeys...)
}

// Revoke revokes an agent key: every call with it is 401 "Key revoked".
func (e *Engine) Revoke(keyID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if k, ok := e.keys[keyID]; ok {
		k.revoked = true
	}
}

// Proposals is what was proposed, in order.
func (e *Engine) Proposals() []api.ProposalSubmit {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]api.ProposalSubmit(nil), e.proposals...)
}

// Calls is every /agent call and proposal made, "METHOD /path by key_…".
func (e *Engine) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if r.Method == http.MethodPost && r.URL.Path == "/proposals" {
		e.propose(w, body)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/agent/") {
		writeJSON(w, http.StatusNotFound, api.Error{Code: "not_found", Message: "no such path"})
		return
	}
	k, a, refusal := e.auth(r, body)
	if refusal != "" {
		writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: refusal})
		return
	}
	path := r.URL.Path
	e.mu.Lock()
	e.calls = append(e.calls, r.Method+" "+path+" by "+k.id)
	e.mu.Unlock()
	if strings.HasPrefix(path, "/agent/signer/") && (k.scope != api.AgentKeyScopeTrade || a.permission != api.AgentPermissionTrade) {
		writeJSON(w, http.StatusForbidden, api.Error{Code: "key_scope", Message: "this key reads only"})
		return
	}
	switch {
	case r.Method == http.MethodGet && path == "/agent/me":
		scope := k.scope
		me := api.AgentIdentity{AgentId: a.id, AgentName: a.name, AccountId: AccountID, KeyId: k.id, Permission: a.permission, KeyScope: &scope}
		if !k.expires.IsZero() {
			exp := k.expires
			me.KeyExpiresAt = &exp
		}
		writeJSON(w, http.StatusOK, me)
	case r.Method == http.MethodGet && path == "/agent/venue-keys":
		e.mu.Lock()
		var out []api.VenueKey
		for _, v := range e.venueKeys {
			if v.AgentId == a.id {
				out = append(out, v)
			}
		}
		e.mu.Unlock()
		if out == nil {
			out = []api.VenueKey{}
		}
		writeJSON(w, http.StatusOK, out)
	case r.Method == http.MethodGet && path == "/agent/connected-accounts/"+ConnectedAccountID:
		writeJSON(w, http.StatusOK, map[string]any{"id": ConnectedAccountID, "name": "USDⓈ-M Futures", "venue": "binance", "connection": map[string]any{"id": "vc_standin", "name": "Main", "accountCount": 2}})
	case r.Method == http.MethodPost && path == "/agent/signer/poll":
		time.Sleep(50 * time.Millisecond)
		writeJSON(w, http.StatusOK, []api.SignRequest{})
	case r.Method == http.MethodPost && path == "/agent/signer/keygen":
		e.keygen(w, k, body)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/agent/decisions/"):
		e.mu.Lock()
		d, ok := e.decisions[strings.TrimPrefix(path, "/agent/decisions/")]
		e.mu.Unlock()
		if !ok || d.AgentId != a.id {
			writeJSON(w, http.StatusNotFound, api.Error{Code: "not_found", Message: "no such decision"})
			return
		}
		writeJSON(w, http.StatusOK, d)
	case r.Method == http.MethodGet && (path == "/agent/positions" || path == "/agent/approvals" || path == "/agent/connected-accounts"):
		writeJSON(w, http.StatusOK, []any{})
	default:
		writeJSON(w, http.StatusNotFound, api.Error{Code: "not_found", Message: "no such path in the stand-in"})
	}
}

// auth is agentAuth: the key known, live and unexpired, its signature over
// the request, a fresh time and an unseen nonce.
func (e *Engine) auth(r *http.Request, body []byte) (*key, *agent, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	k, ok := e.keys[r.Header.Get(agentkey.HeaderKeyID)]
	switch {
	case !ok:
		return nil, nil, "Unknown key"
	case k.revoked:
		return nil, nil, "Key revoked"
	case !k.expires.IsZero() && !e.now().Before(k.expires):
		return nil, nil, "Key expired"
	}
	req := agentkey.Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body, KeyID: k.id,
		Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt)}
	at, err := time.Parse(time.RFC3339Nano, req.SignedAt)
	if err != nil || e.now().Sub(at).Abs() > time.Minute || e.nonces[k.id+req.Nonce] || !agentkey.VerifyRequest(k.pub, req, r.Header.Get(agentkey.HeaderSignature)) {
		return nil, nil, "Invalid signature"
	}
	e.nonces[k.id+req.Nonce] = true
	return k, e.agents[k.agent], ""
}

// keygen is the engine's two rounds of FROST's key generation for a pending
// trading key of the agent, both with one key. A key that names its holder
// (VenueKey.agentKeyId, from creation) is generated by that key only:
// another is 409 conflict with details.venueKeyId. A registered key that
// generates one becomes its holder; an issued key's (a bot's) leaves none.
func (e *Engine) keygen(w http.ResponseWriter, k *key, body []byte) {
	var in api.KeygenRound
	if err := json.Unmarshal(body, &in); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: err.Error()})
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, v := range e.venueKeys {
		if v.Id != in.VenueKeyId {
			continue
		}
		if v.AgentKeyId != nil && *v.AgentKeyId != k.id {
			writeJSON(w, http.StatusConflict, api.Error{Code: "conflict", Message: "another of the agent's keys generates this trading key", Details: &map[string]any{"venueKeyId": v.Id}})
			return
		}
		if v.Keygen == nil || *v.Keygen != api.VenueKeyKeygenPending {
			writeJSON(w, http.StatusConflict, api.Error{Code: "conflict", Message: "this trading key's generation is done"})
			return
		}
	}
	switch in.Round {
	case api.KeygenRoundRoundN1:
		kg, err := frost.NewKeygen(frost.EngineID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, api.Error{Code: "internal", Message: err.Error()})
			return
		}
		first, err := kg.First()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, api.Error{Code: "internal", Message: err.Error()})
			return
		}
		e.dkg["kgs_"+in.VenueKeyId], e.dkgKey["kgs_"+in.VenueKeyId] = kg, k.id
		writeJSON(w, http.StatusOK, api.KeygenRoundResult{SessionId: "kgs_" + in.VenueKeyId, Message: base64.StdEncoding.EncodeToString(first)})
	case api.KeygenRoundRoundN2:
		if in.SessionId == nil || in.Message == nil || e.dkg[*in.SessionId] == nil {
			writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: "no such session"})
			return
		}
		if e.dkgKey[*in.SessionId] != k.id {
			writeJSON(w, http.StatusConflict, api.Error{Code: "conflict", Message: "another key opened this key generation"})
			return
		}
		kg := e.dkg[*in.SessionId]
		reply, _ := base64.StdEncoding.DecodeString(*in.Message)
		last, share, err := kg.Last(reply)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: err.Error()})
			return
		}
		delete(e.dkg, *in.SessionId)
		delete(e.dkgKey, *in.SessionId)
		pub := base64.StdEncoding.EncodeToString(share.PublicKey())
		for i := range e.venueKeys {
			if e.venueKeys[i].Id == in.VenueKeyId {
				done, pem, holder := api.VenueKeyKeygenDone, frost.PublicKeyPEM(share.PublicKey()), k.id
				e.venueKeys[i].Keygen, e.venueKeys[i].PublicKey = &done, &pem
				if !k.issued {
					e.venueKeys[i].AgentKeyId = &holder
				}
			}
		}
		writeJSON(w, http.StatusOK, api.KeygenRoundResult{SessionId: *in.SessionId, Message: base64.StdEncoding.EncodeToString(last), PublicKey: &pub})
	}
}

// propose is POST /proposals: the proposal's signature checked with the key
// it names, the key's scope, then a decision parked for a person.
func (e *Engine) propose(w http.ResponseWriter, body []byte) {
	var p api.ProposalSubmit
	if err := json.Unmarshal(body, &p); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: err.Error()})
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	k, ok := e.keys[p.KeyId]
	switch {
	case !ok:
		writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Unknown key"})
		return
	case k.revoked:
		writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Key revoked"})
		return
	case !proposal.Verify(k.pub, signer.SubmitOf(p)):
		writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Invalid signature"})
		return
	case k.scope != api.AgentKeyScopeTrade:
		writeJSON(w, http.StatusForbidden, api.Error{Code: "key_scope", Message: "this key reads only"})
		return
	}
	a := e.agents[k.agent]
	e.calls = append(e.calls, "POST /proposals by "+k.id)
	e.proposals = append(e.proposals, p)
	id := e.next("dec_")
	approval := e.next("apr_")
	d := api.Decision{
		Id: id, AgentId: a.id, AgentName: a.name, ConnectedAccountId: p.ConnectedAccountId, Verdict: api.VerdictRequireApproval, ApprovalId: &approval,
		Approval:  &api.ApprovalOutcome{Id: approval, Status: api.ApprovalStatusPending, RequestedAt: e.now().UTC(), ExpiresAt: p.ExpiresAt},
		Proposal:  api.Proposal{Symbol: p.Symbol, Side: p.Side, Size: p.Size, Venue: api.VenueBinance, NotionalUsd: "100"},
		CreatedAt: e.now().UTC(), Evaluations: []api.RuleEvaluation{},
	}
	e.decisions[id] = d
	writeJSON(w, http.StatusOK, d)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
