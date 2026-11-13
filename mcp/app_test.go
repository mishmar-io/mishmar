package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/daemon"
	"github.com/mishmar-io/mishmar/signer/signin"
)

// fakeDaemon answers the local API's status and sign-in from what the test
// sets, through daemon.Local.
type fakeDaemon struct {
	status daemon.Status
	signIn daemon.SignIn
	wait   daemon.SignIn
	posts  int
}

func (f *fakeDaemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apps/{slug}/status", func(w http.ResponseWriter, r *http.Request) { writeTestJSON(w, f.status) })
	mux.HandleFunc("POST /apps/{slug}/sign-in", func(w http.ResponseWriter, r *http.Request) { f.posts++; writeTestJSON(w, f.signIn) })
	mux.HandleFunc("GET /apps/{slug}/sign-in", func(w http.ResponseWriter, r *http.Request) { writeTestJSON(w, f.wait) })
	return mux
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, mustJSON(v))
}

func mustJSON(v any) string {
	s, err := asJSON(v)
	if err != nil {
		panic(err)
	}
	return s
}

func appBackendOver(f *fakeDaemon, remote signin.Remote, opened *[]string) Backend {
	c := daemon.Local(f.handler())
	return AppBackend(AppOptions{
		Slug: "claude-code", API: "https://api.testnet.mishmar.io", Environment: "testnet", Remote: remote, Wait: time.Millisecond,
		Connect: func(context.Context, bool) (*daemon.Client, daemon.Health, error) {
			return c, daemon.Health{Version: "v0.5.0", API: daemon.APIVersion}, nil
		},
		Open: func(link string) error { *opened = append(*opened, link); return nil },
	})
}

var signedInRecord = &daemon.Record{
	App: "claude-code", API: "https://api.testnet.mishmar.io", MachineName: "Ishai's MacBook Pro", AgentID: "agt_1", AgentName: "Claude Code", AccountID: "acc_1", KeyID: "key_1_aaaaaaaaaaaa",
	KeyExpiresAt: time.Date(2027, 5, 12, 0, 0, 0, 0, time.UTC), Fingerprint: "AS3N-N9TM-CD3M-R0M5", ConnectedAccountID: "ta_1",
	Next: "https://app.testnet.mishmar.io/agents/agt_1?setup=1", SignedInAt: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC),
}

// The status tool in each state, in plain words.
func TestTradingStatusSaysWhereThingsStand(t *testing.T) {
	bg := daemon.DaemonInfo{Version: "v0.5.0", PID: 4242}
	hlKey := daemon.VenueKeyState{ID: "vk_1", Venue: "hyperliquid", ConnectedAccountID: "ta_1", Status: "pending", Keygen: "done", PublicKey: "0xabc", HyperliquidStatus: "pending", Mine: true, Held: true, Network: "testnet", Name: "mishmar-agt_1"}
	bnKey := daemon.VenueKeyState{ID: "vk_2", Venue: "binance", ConnectedAccountID: "ta_1", Status: "pending", Keygen: "done", PublicKey: "-----BEGIN PUBLIC KEY-----\nMCow\n-----END PUBLIC KEY-----", Mine: true, Held: true}
	active := bnKey
	active.Status, active.APIKeyRegistered = "active", true
	theirs := active
	theirs.Mine, theirs.Held = false, false
	making := bnKey
	making.Keygen, making.Held, making.PublicKey = "pending", false, ""
	account := &daemon.AccountBrief{ID: "ta_1", Name: "Perps", Venue: "hyperliquid", ConnectionName: "Main"}
	cases := []struct {
		name string
		st   daemon.Status
		want []string
	}{
		{"not signed in", daemon.Status{App: "claude-code", State: daemon.SignedOut, Daemon: bg},
			[]string{"This computer is not signed in to Mishmar for claude-code.", "nothing is kept before the person approves", "runs in the background on this computer (mishmar v0.5.0, pid 4242)", "not after the computer restarts"}},
		{"not signed in, the last one declined", daemon.Status{App: "claude-code", State: daemon.SignedOut, SignIn: &daemon.SignIn{State: "denied"}, Daemon: bg},
			[]string{"The last sign-in: " + SignInDenied}},
		{"waiting for the person", daemon.Status{App: "claude-code", State: daemon.SigningIn, SignIn: &daemon.SignIn{State: "waiting", URL: "https://api.testnet.mishmar.io/oauth/authorize?x", Code: "AS3N-N9TM", ExpiresAt: time.Date(2026, 11, 13, 9, 10, 0, 0, time.UTC)}, Daemon: bg},
			[]string{"Open this link in your browser to approve it: https://api.testnet.mishmar.io/oauth/authorize?x", "Check the page shows this code: AS3N-N9TM", "works until 09:10 UTC"}},
		{"signed in, reading", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Permission: "read", Signer: daemon.SignerState{Running: true}, Daemon: bg},
			[]string{"Signed in to Mishmar for claude-code as agent Claude Code (agt_1) on account acc_1, from 'Ishai's MacBook Pro' with key code AS3N-N9TM, signed in 13 Nov 2026. The key stops working on 12 May 2027", StatusReading}},
		{"no trading key yet", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Permission: "trade", Signer: daemon.SignerState{Running: true}, Daemon: bg},
			[]string{"No trading key yet: the person finishes the setup in the console: https://app.testnet.mishmar.io/agents/agt_1?setup=1"}},
		{"the key being made", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Permission: "trade", VenueKeys: []daemon.VenueKeyState{making}, ConnectedAccount: account, Signer: daemon.SignerState{Running: true}, Daemon: bg},
			[]string{"is making the trading key on Main · Perps (ta_1)"}},
		{"waiting at Hyperliquid", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Permission: "trade", VenueKeys: []daemon.VenueKeyState{hlKey}, ConnectedAccount: account, Signer: daemon.SignerState{Running: true}, Daemon: bg},
			[]string{"waits at Hyperliquid: the person approves API wallet 0xabc under the name mishmar-agt_1 at https://app.hyperliquid-testnet.xyz/API"}},
		{"waiting at Binance", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Permission: "trade", VenueKeys: []daemon.VenueKeyState{bnKey}, Signer: daemon.SignerState{Running: true}, Daemon: bg},
			[]string{"waits at Binance", "withdrawals off", "-----BEGIN PUBLIC KEY-----"}},
		{"ready to trade", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Permission: "trade", VenueKeys: []daemon.VenueKeyState{active}, ConnectedAccount: account, Signer: daemon.SignerState{Running: true}, Daemon: bg},
			[]string{"Ready to trade on Main · Perps (ta_1)", "even after this chat closes"}},
		{"another key holds it", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Permission: "trade", VenueKeys: []daemon.VenueKeyState{theirs}, Signer: daemon.SignerState{Running: true}, Daemon: bg},
			[]string{"belongs to another of this agent's keys"}},
		{"the signer in the app's process", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Permission: "trade", VenueKeys: []daemon.VenueKeyState{active}, Signer: daemon.SignerState{Running: true}, Daemon: daemon.DaemonInfo{Version: "v0.5.0", InProcess: true}},
			[]string{"runs inside this app (mishmar v0.5.0): an order the person approves goes out only while the app is open"}},
		{"the signer could not start", daemon.Status{App: "claude-code", State: daemon.SignedIn, Record: signedInRecord, Code: "AS3N-N9TM", Signer: daemon.SignerState{Error: "another signer is running"}, Daemon: bg},
			[]string{"Mishmar's signer for this app is not running: another signer is running"}},
		{"ended", daemon.Status{App: "claude-code", State: daemon.Ended, Ended: "Key revoked", Daemon: bg},
			[]string{"Mishmar no longer takes this computer's key for claude-code (Key revoked)"}},
	}
	for _, c := range cases {
		var opened []string
		b := appBackendOver(&fakeDaemon{status: c.st}, signin.Remote{}, &opened)
		text, err := b.(StatusReporter).TradingStatus(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for _, want := range c.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: lacks %q:\n%s", c.name, want, text)
			}
		}
		if len(opened) != 0 {
			t.Errorf("%s: the status opened the browser", c.name)
		}
	}
}

// The gate before a tool: a remote machine is refused before anything; a
// signed-in app runs; a sign-in this call started opens the browser once
// and answers the link and the code; an end is told once, then a sign-in.
func TestReadyGate(t *testing.T) {
	var opened []string
	remote := signin.Remote{Hints: []string{"cloud"}, Signals: []string{"CODESPACES"}}
	f := &fakeDaemon{status: daemon.Status{State: daemon.SignedOut}}
	if text := appBackendOver(f, remote, &opened).(Gate).Ready(context.Background()); !strings.Contains(text, "it looks like a cloud machine (CODESPACES)") || f.posts != 0 {
		t.Fatalf("remote: %s", text)
	}
	f = &fakeDaemon{status: daemon.Status{State: daemon.SignedIn, Record: &daemon.Record{API: "https://api.testnet.mishmar.io"}}}
	if text := appBackendOver(f, signin.Remote{}, &opened).(Gate).Ready(context.Background()); text != "" {
		t.Fatalf("signed in: %s", text)
	}
	// Signed in at another Mishmar than this server's: nothing is sent.
	f = &fakeDaemon{status: daemon.Status{State: daemon.SignedIn, Record: &daemon.Record{API: "https://api.mishmar.io"}}}
	if text := appBackendOver(f, signin.Remote{}, &opened).(Gate).Ready(context.Background()); !strings.Contains(text, "signed in for claude-code at https://api.mishmar.io, not at https://api.testnet.mishmar.io") || f.posts != 0 {
		t.Fatalf("another Mishmar: %s", text)
	}
	until := time.Date(2026, 11, 13, 9, 10, 0, 0, time.UTC)
	f = &fakeDaemon{
		status: daemon.Status{State: daemon.SignedOut},
		signIn: daemon.SignIn{State: "waiting", URL: "https://x/oauth/authorize?a", Code: "AS3N-N9TM", ExpiresAt: until, Started: true},
		wait:   daemon.SignIn{State: "waiting", URL: "https://x/oauth/authorize?a", Code: "AS3N-N9TM", ExpiresAt: until},
	}
	b := appBackendOver(f, signin.Remote{}, &opened).(Gate)
	want := fmt.Sprintf(SignInOpened, "https://x/oauth/authorize?a", "AS3N-N9TM", "09:10 UTC")
	if text := b.Ready(context.Background()); text != want || len(opened) != 1 || opened[0] != "https://x/oauth/authorize?a" {
		t.Fatalf("started: %s (opened %v)", text, opened)
	}
	f.signIn.Started = false // a sign-in another call started: no second browser
	if text := b.Ready(context.Background()); !strings.Contains(text, "Open this link") || len(opened) != 1 {
		t.Fatalf("waiting: %s (opened %v)", text, opened)
	}
	for state, want := range map[string]string{"denied": SignInDenied, "expired": SignInExpired} {
		f.wait = daemon.SignIn{State: state}
		if text := b.Ready(context.Background()); text != want {
			t.Fatalf("%s: %s", state, text)
		}
	}
	f.wait = daemon.SignIn{State: "failed", Error: "signin: Mishmar refused the token step (invalid_grant): key_proof is not the key's signature over this code"}
	if text := b.Ready(context.Background()); !strings.HasPrefix(text, "This computer could not sign in to Mishmar: Mishmar refused the token step") {
		t.Fatalf("failed: %s", text)
	}
	f.wait = daemon.SignIn{State: "signed_in"}
	if text := b.Ready(context.Background()); text != "" {
		t.Fatalf("approved during the wait: %s", text)
	}
	ended := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	f.status = daemon.Status{State: daemon.Ended, Ended: "Key revoked", EndedAt: ended}
	posts := f.posts
	if text := b.Ready(context.Background()); !strings.Contains(text, "(Key revoked)") || f.posts != posts {
		t.Fatalf("ended, first: %s", text)
	}
	if text := b.Ready(context.Background()); text != "" || f.posts != posts+1 {
		t.Fatalf("ended, second: %s (%d posts)", text, f.posts-posts)
	}
}

// An app's server says what the new refusals mean, and that an approved
// order waits for the signer.
func TestNewRefusalsAndTheWait(t *testing.T) {
	scope := refusedTrade(&signer.Error{Status: http.StatusForbidden, Code: "key_scope", Message: "this key reads only"})
	if !strings.Contains(scope.Error(), KeyScopeRefusal) {
		t.Fatalf("key_scope: %v", scope)
	}
	holder := refusedTrade(&signer.Error{Status: http.StatusConflict, Code: "conflict", Message: "another key holds it", Details: map[string]any{"venueKeyId": "vk_1"}})
	if !strings.Contains(holder.Error(), OtherHolderRefusal) {
		t.Fatalf("holder: %v", holder)
	}
	other := refusedTrade(&signer.Error{Status: http.StatusConflict, Code: "conflict", Message: "nonce used"})
	if strings.Contains(other.Error(), OtherHolderRefusal) {
		t.Fatalf("another conflict: %v", other)
	}
	if !errors.Is(scope, scope) {
		t.Fatal("unwrap")
	}
	until := time.Date(2026, 11, 13, 9, 15, 0, 0, time.UTC)
	text, err := describeDecision(api.Decision{Id: "dec_1", Verdict: api.VerdictRequireApproval, Approval: &api.ApprovalOutcome{Status: api.ApprovalStatusApproved},
		Signing: &api.SigningStatus{Status: api.SigningStatusStatusWaiting, WaitUntil: &until}})
	if err != nil || !strings.Contains(text, "Approved; the order waits for the signer that holds the trading key until 2026-11-13T09:15:00Z") {
		t.Fatalf("waiting: %s %v", text, err)
	}
}

// The server without --app is the bot's, as before: no status tool, the
// instructions and the proposal's default life unchanged.
func TestTheBotsServerIsUnchanged(t *testing.T) {
	s := New(&fakeBackend{}, "test", nil)
	if s.app || s.instructions() != Instructions || len(s.tools) != 16 || strings.Contains(s.validForDesc(), "900") {
		t.Fatal("the bot's server changed")
	}
	var opened []string
	a := New(appBackendOver(&fakeDaemon{}, signin.Remote{}, &opened), "test", nil)
	if !a.app || !strings.HasSuffix(a.instructions(), AppTrading) || len(a.tools) != 17 || a.validForDesc() != AppValidForDesc {
		t.Fatal("the app's server")
	}
}
