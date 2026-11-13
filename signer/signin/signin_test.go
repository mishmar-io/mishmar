package signin_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer/signin"
	"github.com/mishmar-io/mishmar/signer/signin/signintest"
)

// start begins a sign-in against the stand-in, counting the saves.
func start(t *testing.T, cp *signintest.Server, timeout time.Duration) (*signin.Flow, *atomic.Int32, *signin.Result) {
	t.Helper()
	var saves atomic.Int32
	var saved signin.Result
	f, err := signin.Start(context.Background(), signin.Params{
		API: cp.URL, Environment: signintest.Environment, App: "claude-code", MachineName: "Test Mac", CLIVersion: "v0.5.0-test",
		Timeout: timeout, Save: func(r signin.Result) error { saves.Add(1); saved = r; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Cancel)
	return f, &saves, &saved
}

func wait(t *testing.T, f *signin.Flow) (signin.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return f.Wait(ctx)
}

func body(res *http.Response) string {
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

// The whole sign-in: the link carries the key, the page's code is the
// key's, the person approves, the computer proves the key at the token
// step, checks the answer and the engine, and saves a config whose secret
// is its own key under the id Mishmar gave it; the browser goes on to the
// console's venue step.
func TestSignInApproved(t *testing.T) {
	cp := signintest.New()
	defer cp.Close()
	f, saves, saved := start(t, cp, time.Minute)
	if !strings.HasPrefix(f.URL, cp.URL+"/oauth/authorize?") || !strings.HasPrefix(f.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("link %s, redirect %s", f.URL, f.RedirectURI)
	}
	id, err := cp.Open(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	req := cp.Requests()[0]
	if req.Fingerprint != f.Fingerprint || req.Code != f.Code || req.App != "claude-code" || req.MachineName != "Test Mac" || req.Hint != "" || req.CLIVersion != "v0.5.0-test" {
		t.Fatalf("the request as stored: %+v, the flow's code %s", req, f.Code)
	}
	if st, _ := f.Status(); st != signin.Waiting {
		t.Fatalf("status %s before the approval", st)
	}
	res, err := cp.Approve(id, signintest.Approval{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusFound || !strings.Contains(res.Header.Get("Location"), "/console/agents/agt_") || !strings.HasSuffix(res.Header.Get("Location"), "?setup=1") {
		t.Fatalf("the browser was answered %d %s: %s", res.StatusCode, res.Header.Get("Location"), body(res))
	}
	r, err := wait(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if saves.Load() != 1 || saved.Config == nil || saved.Config.KeyID != r.Grant.KeyID {
		t.Fatalf("saved %d times: %+v", saves.Load(), saved)
	}
	p, err := agentkey.ParseSecret(r.Config.Secret)
	if err != nil || p.KeyID != r.Grant.KeyID || p.Environment != signintest.Environment || p.AccountID != signintest.AccountID || p.Key.Fingerprint() != f.Fingerprint {
		t.Fatalf("the secret %+v %v", p, err)
	}
	if r.Config.Engine != cp.URL+"/a/"+signintest.AccountID+"/v1" || r.Config.AgentID != r.Grant.AgentID || len(r.Config.VenueKeys) != 0 {
		t.Fatalf("config %+v", r.Config)
	}
	if r.Me.KeyScope == nil || *r.Me.KeyScope != api.AgentKeyScopeTrade || r.Me.KeyId != r.Grant.KeyID {
		t.Fatalf("the engine's answer %+v", r.Me)
	}
	if st, err := f.Status(); st != signin.SignedIn || err != nil {
		t.Fatalf("status %s %v", st, err)
	}
	// The loopback address is closed once the sign-in is decided.
	eventually(t, "the loopback listener closes", func() bool {
		_, err := http.Get(f.RedirectURI)
		return err != nil
	})
	// The code was spent by the token step: a second redemption is refused.
	if res, err := cp.Approve(id, signintest.Approval{}); err == nil {
		t.Fatalf("the closed flow answered %d", res.StatusCode)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSignInDenied(t *testing.T) {
	cp := signintest.New()
	defer cp.Close()
	f, saves, _ := start(t, cp, time.Minute)
	id, err := cp.Open(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cp.Deny(id)
	if err != nil {
		t.Fatal(err)
	}
	if b := body(res); res.StatusCode != http.StatusOK || !strings.Contains(b, "You declined") {
		t.Fatalf("browser %d %s", res.StatusCode, b)
	}
	if _, err := wait(t, f); !errors.Is(err, signin.ErrDenied) {
		t.Fatalf("err %v", err)
	}
	if st, _ := f.Status(); st != signin.Denied || saves.Load() != 0 {
		t.Fatalf("status %s, %d saves", st, saves.Load())
	}
}

func TestSignInExpires(t *testing.T) {
	cp := signintest.New()
	defer cp.Close()
	f, saves, _ := start(t, cp, 200*time.Millisecond)
	if _, err := cp.Open(f.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := wait(t, f); !errors.Is(err, signin.ErrExpired) {
		t.Fatalf("err %v", err)
	}
	if st, _ := f.Status(); st != signin.Expired || saves.Load() != 0 {
		t.Fatalf("status %s, %d saves", st, saves.Load())
	}
}

// What the computer refuses, each before anything is saved: an answer for
// another key (the control plane registered the wrong one), a token step
// that refuses the proof, an answer from another issuer, a bearer token, an
// engine elsewhere, and an engine that does not take the key.
func TestSignInRefusals(t *testing.T) {
	other := agentkey.FromSeed(make([]byte, 32))
	cases := []struct {
		name     string
		approval func(cp *signintest.Server) signintest.Approval
		want     string
	}{
		{"the control plane registered another key (wrong fingerprint)", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{RegisterKey: other.PublicKeyB64()}
		}, "not this computer's"},
		{"the answer claims this key's fingerprint, but the engine holds another key", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{RegisterKey: other.PublicKeyB64(), Grant: func(g *signin.Grant) { g.Fingerprint = "" }}
		}, "engine does not take this computer's key"},
		{"the token step checks the proof against another key", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{ProofKey: other.PublicKeyB64()}
		}, "key_proof is not the key's signature over this code"},
		{"the answer comes from another issuer (a mix-up)", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{Issuer: "https://api.example.com"}
		}, "the Mishmar this computer asked"},
		{"a bearer token in the answer", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{Answer: func(a signin.TokenAnswer) any { a.AccessToken = "mat_x"; return a }}
		}, "bearer token"},
		{"an engine on another host", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{Grant: func(g *signin.Grant) { g.Engine = "https://elsewhere.example/a/acc_standin/v1" }}
		}, "is not"},
		{"a key of another account", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{Grant: func(g *signin.Grant) { g.KeyID = "key_other_000000000001" }}
		}, "is not a key of account"},
		{"another environment than asked", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{Grant: func(g *signin.Grant) { g.Environment = "prod" }}
		}, "environment"},
		{"a key that lives over a year", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{Grant: func(g *signin.Grant) { g.KeyExpiresAt = time.Now().Add(400 * 24 * time.Hour) }}
		}, "longer than a year"},
		{"a read key", func(*signintest.Server) signintest.Approval {
			return signintest.Approval{Scope: api.AgentKeyScopeRead}
		}, "scope read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cp := signintest.New()
			defer cp.Close()
			f, saves, _ := start(t, cp, time.Minute)
			id, err := cp.Open(f.URL)
			if err != nil {
				t.Fatal(err)
			}
			a := c.approval(cp)
			if a.Grant != nil && a.RegisterKey != "" {
				// The forged answer names this computer's fingerprint.
				fp := f.Fingerprint
				a.Grant = func(g *signin.Grant) { g.Fingerprint = fp }
			}
			res, err := cp.Approve(id, a)
			if err != nil {
				t.Fatal(err)
			}
			b := body(res)
			_, err = wait(t, f)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q (browser %d: %s)", err, c.want, res.StatusCode, b)
			}
			if st, _ := f.Status(); st != signin.Failed || saves.Load() != 0 {
				t.Fatalf("status %s, %d saves", st, saves.Load())
			}
			if res.StatusCode == http.StatusFound {
				t.Fatal("the browser was sent on after a refusal")
			}
		})
	}
}

// An answer with another state is not this flow's: it is turned away and
// the flow goes on waiting for its own.
func TestSignInIgnoresAnotherState(t *testing.T) {
	cp := signintest.New()
	defer cp.Close()
	f, _, _ := start(t, cp, time.Minute)
	id, err := cp.Open(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cp.Approve(id, signintest.Approval{State: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("another state answered %d", res.StatusCode)
	}
	if st, _ := f.Status(); st != signin.Waiting {
		t.Fatalf("status %s", st)
	}
	if _, err := cp.Approve(id, signintest.Approval{}); err != nil {
		t.Fatal(err)
	}
	if _, err := wait(t, f); err != nil {
		t.Fatal(err)
	}
}

// A machine that looks remote does not start a sign-in at all.
func TestSignInRefusesRemoteHints(t *testing.T) {
	_, err := signin.Start(context.Background(), signin.Params{API: "https://api.mishmar.io", App: "claude-code", Hints: []string{"ssh"}})
	if err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("err %v", err)
	}
	for _, api := range []string{"http://api.mishmar.io", "https://api.mishmar.io/v1", "ftp://x", ""} {
		if _, err := signin.Start(context.Background(), signin.Params{API: api, App: "claude-code"}); err == nil {
			t.Fatalf("API %q taken", api)
		}
	}
	if _, err := signin.Start(context.Background(), signin.Params{API: "https://api.mishmar.io", App: "Claude Code"}); err == nil {
		t.Fatal("an app name that is not a slug")
	}
}

// The stand-in's authorize and token steps by hand, as the control plane's
// must behave: authorize takes only the built-in client with a loopback
// redirect, the trade resource and a 32-byte key; the token step spends the
// code on the first try, and takes only a fresh proof over this code by the
// key on the request, the right verifier, redirect and resource. A 200
// answer has no bearer token.
func TestStandInSteps(t *testing.T) {
	cp := signintest.New()
	defer cp.Close()
	key := agentkey.FromSeed([]byte("0123456789abcdef0123456789abcdef"))
	got := make(chan url.Values, 1)
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.URL.Query() }))
	defer loop.Close()
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	authorize := url.Values{
		"response_type": {"code"}, "client_id": {signin.ClientID}, "redirect_uri": {loop.URL + "/callback"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "state": {"st"},
		"resource": {signin.Resource}, "scope": {signin.Scope}, "agent_key": {base64.RawURLEncoding.EncodeToString(key.PublicKey())},
		"machine_name": {"Test Mac"}, "app": {"claude-code"}, "machine_hint": {""}, "cli_version": {"dev"},
	}
	for name, mod := range map[string]func(url.Values){
		"another client":       func(q url.Values) { q.Set("client_id", "mcl_x") },
		"a redirect elsewhere": func(q url.Values) { q.Set("redirect_uri", "https://evil.example/callback") },
		"the MCP resource":     func(q url.Values) { q.Set("resource", "https://mcp.mishmar.io") },
		"a short key":          func(q url.Values) { q.Set("agent_key", base64.RawURLEncoding.EncodeToString(make([]byte, 31))) },
		"an unknown hint":      func(q url.Values) { q.Set("machine_hint", "vpn") },
		"no PKCE":              func(q url.Values) { q.Del("code_challenge") },
	} {
		q := url.Values{}
		for k, v := range authorize {
			q[k] = v
		}
		mod(q)
		if _, err := cp.Open(cp.URL + "/oauth/authorize?" + q.Encode()); err == nil {
			t.Fatalf("authorize took %s", name)
		}
	}
	issue := func() string {
		id, err := cp.Open(cp.URL + "/oauth/authorize?" + authorize.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cp.Approve(id, signintest.Approval{}); err != nil {
			t.Fatal(err)
		}
		return (<-got).Get("code")
	}
	form := func(code string, mod func(url.Values)) url.Values {
		proof, at, err := key.SignMachineProof(code, cp.URL, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		f := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "client_id": {signin.ClientID},
			"redirect_uri": {loop.URL + "/callback"}, "resource": {signin.Resource}, "key_proof": {proof}, "key_signed_at": {at}}
		if mod != nil {
			mod(f)
		}
		return f
	}
	other := agentkey.FromSeed(make([]byte, 32))
	for name, c := range map[string]struct {
		mod  func(url.Values)
		want string
	}{
		"a proof by another key": {func(f url.Values) {
			p, at, _ := other.SignMachineProof(f.Get("code"), cp.URL, time.Now())
			f.Set("key_proof", p)
			f.Set("key_signed_at", at)
		}, "key_proof is not the key's signature over this code"},
		"a proof for another code": {func(f url.Values) {
			p, at, _ := key.SignMachineProof("mac_other", cp.URL, time.Now())
			f.Set("key_proof", p)
			f.Set("key_signed_at", at)
		}, "key_proof is not the key's signature over this code"},
		"a stale proof": {func(f url.Values) {
			p, at, _ := key.SignMachineProof(f.Get("code"), cp.URL, time.Now().Add(-2*time.Minute))
			f.Set("key_proof", p)
			f.Set("key_signed_at", at)
		}, "more than 60 seconds"},
		"no proof":           {func(f url.Values) { f.Del("key_proof") }, "key_proof and key_signed_at are required"},
		"another verifier":   {func(f url.Values) { f.Set("code_verifier", strings.Repeat("w", 43)) }, "code_verifier"},
		"another redirect":   {func(f url.Values) { f.Set("redirect_uri", "http://127.0.0.1:1/callback") }, "redirect_uri"},
		"the MCP resource":   {func(f url.Values) { f.Set("resource", "https://mcp.mishmar.io") }, "resource"},
		"a client secret":    {func(f url.Values) { f.Set("client_secret", "x") }, "no secret"},
		"another grant type": {func(f url.Values) { f.Set("grant_type", "refresh_token") }, "grant_type"},
	} {
		code := issue()
		res, err := cp.Token(form(code, c.mod))
		if err != nil {
			t.Fatal(err)
		}
		b := body(res)
		if res.StatusCode == http.StatusOK || !strings.Contains(b, c.want) {
			t.Fatalf("%s: %d %s", name, res.StatusCode, b)
		}
		// Spent by the refused try: the right request is refused too.
		if res, _ := cp.Token(form(code, nil)); res.StatusCode == http.StatusOK && !strings.Contains(name, "grant type") && !strings.Contains(name, "secret") {
			t.Fatalf("%s: the code worked after a refused try", name)
		}
	}
	code := issue()
	res, err := cp.Token(form(code, nil))
	if err != nil {
		t.Fatal(err)
	}
	b := body(res)
	if res.StatusCode != http.StatusOK || !strings.Contains(b, `"token_type":"mishmar_agent_key"`) || strings.Contains(b, "access_token") || !strings.Contains(b, key.Fingerprint()) {
		t.Fatalf("token %d %s", res.StatusCode, b)
	}
	if res, _ := cp.Token(form(code, nil)); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a code used twice: %d", res.StatusCode)
	}
}
