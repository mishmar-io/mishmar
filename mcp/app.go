package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/daemon"
	"github.com/mishmar-io/mishmar/signer/signin"
)

// An app's server (`mishmar mcp --app <slug>`): the same tools, over the
// background signer (signer/daemon) instead of a signer in this process. It
// holds no key. The first tool that needs Mishmar signs this computer in;
// get_trading_status says where things stand and starts nothing.

// AppTrading is what an app's server adds to Instructions.
const AppTrading = `This server trades for the person from their own computer: Mishmar's
signer runs here and holds this computer's key and its half of each trading
key; Mishmar never holds them. The first Mishmar tool you call signs this
computer in: it opens Mishmar in the browser and answers with a code. Tell
the person to approve only if the page shows the same code, then call the
tool again. get_trading_status says where things stand (signed in or not,
the trading key's step at the exchange, whether the signer runs) and starts
nothing. A proposal from this app stands 15 minutes unless you set
validForSeconds (at most 86400): that is how long the person has to approve
it, and an approved order goes out once this computer's signer signs it,
until then. The signer keeps running after the app closes, but not after
the computer restarts; it starts again with the next Mishmar tool. Trading
from a remote machine (over SSH, in a container or a cloud workspace) is not
supported yet.`

// The status tool's name and words.
const (
	StatusToolName  = "get_trading_status"
	StatusToolTitle = "Trading status on this computer"
	StatusToolDesc  = "Where trading through Mishmar stands on this computer, in plain words: whether it is signed in (and the link and code while the person approves), the agent and its key's expiry, the trading key's step at the exchange and what the person does there, whether the background signer runs and its version. Starts nothing and signs nothing in."
)

// AppValidForDesc is propose_order's validForSeconds in an app's server.
const AppValidForDesc = "How long the proposal stands, at most 86400; default 900 (15 minutes). It is how long the person has to approve an order parked for them, and how long an approved order waits for this computer's signer."

// The sign-in's words, which the model passes on.
const (
	// SignInOpened is the pending text when this server opened the browser:
	// the link, then the code. Plan section 2.3.
	SignInOpened = "This computer is not signed in to Mishmar yet. I opened Mishmar in your browser to approve it: %s. Check the page shows this code: %s. Approve only if it matches and you started this just now. Then ask me again. The link works until %s."
	// SignInLink is the same when no browser could be opened here.
	SignInLink = "This computer is not signed in to Mishmar yet. Open this link in your browser to approve it: %s. Check the page shows this code: %s. Approve only if it matches and you started this just now. Then ask me again. The link works until %s."
	// SignInDenied, SignInExpired and SignInFailed end a sign-in.
	SignInDenied  = "The person declined this computer's sign-in to Mishmar, so nothing was set up. Ask them before trying again: the next Mishmar tool starts a new sign-in."
	SignInExpired = "Nobody approved this computer's sign-in to Mishmar in time, so nothing was set up. The next Mishmar tool starts a new sign-in."
	SignInFailed  = "This computer could not sign in to Mishmar: %s. Nothing was kept. The next Mishmar tool starts a new sign-in."
	// RemoteRefused is a machine that looks remote; nothing is started.
	RemoteRefused = "Trading through Mishmar from this machine is not supported yet: it looks like %s. Mishmar's signer keeps the person's half of each trading key on the computer it runs on, so it has to be the person's own computer, with a browser to approve it in; signing in by typing a code on another device is not built yet. Nothing was started. The person can trade from Claude Code, Claude Desktop, Cursor or VS Code on their own computer, and read their accounts through Mishmar's hosted address from anywhere."
	// OtherMishmar is an app signed in at another Mishmar (prod, testnet)
	// than this server is set up for; nothing is sent.
	OtherMishmar = "This computer is signed in for %s at %s, not at %s, which this server is set up for, so nothing was sent. To use both, give this server another --app name (like %s-testnet), which signs in beside the other; or set its --env back."
	// KeyEnded is an app whose key Mishmar no longer takes.
	KeyEnded = "Mishmar no longer takes this computer's key for %s (%s): it was disconnected in the console, or it expired, and its signer stopped. Nothing more is signed with it. The next Mishmar tool signs this computer in again with a new key, which the person approves in the browser."
)

// The status's words.
const (
	StatusNotSignedIn = "This computer is not signed in to Mishmar for %s. The first Mishmar tool you use opens Mishmar in the browser to approve it; nothing is kept before the person approves."
	StatusSignedIn    = "Signed in to Mishmar for %s as agent %s (%s) on account %s, from '%s' with key code %s, signed in %s. The key stops working on %s; signing in again then makes a new one."
	StatusReading     = "The agent reads only, so it cannot propose orders: the person turns trading on for it in the console."
	StatusNoKey       = "No trading key yet: the person finishes the setup in the console: %s"
	StatusKeygen      = "Mishmar's signer on this computer is making the trading key on %s with Mishmar's; this takes a few seconds."
	StatusHyperliquid = "The trading key on %s waits at Hyperliquid: the person approves API wallet %s under the name %s at %s with their master wallet (or from the console: %s). Until then no order can be signed."
	StatusBinance     = "The trading key on %s waits at Binance: the person registers this public key at Binance as a self-generated Ed25519 API key with trading on and withdrawals off, then enters the API key Binance gives in the console (%s). The public key:\n%s"
	StatusChecking    = "The trading key on %s is at the exchange; Mishmar is checking it with a signed read."
	StatusOtherKey    = "The trading key on %s belongs to another of this agent's keys (another computer, or a bot): this computer cannot sign with it."
	StatusReady       = "Ready to trade on %s: every order you propose is checked against the person's rules, and one that needs their approval waits for them in the console and goes out when they approve, even after this chat closes."
	StatusExpiredKey  = "The trading key on %s has expired or was revoked: the person sets up a new one in the console (%s)."
	StatusBackground  = "Mishmar's signer runs in the background on this computer (mishmar %s, pid %d). It keeps running after the app closes, but not after the computer restarts: then it starts again with the next Mishmar tool."
	StatusInProcess   = "Mishmar's signer runs inside this app (mishmar %s): an order the person approves goes out only while the app is open."
	StatusNotRunning  = "Mishmar's signer for this app is not running: %s"
	StatusNoDaemon    = "Mishmar's background signer is not running on this computer; the next Mishmar tool starts it."
)

// Gate is a Backend that must be ready before a tool runs: an app's, which
// signs this computer in first. Ready answers "" when the tool may run, or
// the text to give instead.
type Gate interface {
	Ready(ctx context.Context) string
}

// StatusReporter is a Backend with a status tool.
type StatusReporter interface {
	TradingStatus(ctx context.Context) (string, error)
}

// AppOptions is what an app's server is told.
type AppOptions struct {
	// Slug is --app.
	Slug string
	// API is the hosting API's base URL; Environment the hosted one (or
	// empty with --api).
	API         string
	Environment string
	// Connect reaches the background signer: started when start is set and
	// none runs; with start unset, a nil client when none runs.
	Connect func(ctx context.Context, start bool) (*daemon.Client, daemon.Health, error)
	// Open opens a link in the person's browser; nil opens none.
	Open func(link string) error
	// Remote is this process's look at the machine.
	Remote signin.Remote
	// Wait is how long a tool waits for the person to approve (30 s).
	Wait time.Duration
	// HasKey says whether this computer holds a key for the app; the status
	// then starts the background signer if it is not running (after a
	// restart), since the app's signer should run.
	HasKey func() bool
}

// AppBackend is an app's Backend over the background signer.
func AppBackend(o AppOptions) Backend {
	if o.Wait <= 0 {
		o.Wait = 30 * time.Second
	}
	return &appBackend{o: o, told: map[time.Time]bool{}}
}

type appBackend struct {
	o    AppOptions
	mu   sync.Mutex
	c    *daemon.Client
	h    daemon.Health
	told map[time.Time]bool
}

func (b *appBackend) client(ctx context.Context) (*daemon.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c != nil {
		return b.c, nil
	}
	c, h, err := b.o.Connect(ctx, true)
	if err != nil {
		return nil, err
	}
	b.c, b.h = c, h
	return c, nil
}

func (b *appBackend) path(p string) string { return "/apps/" + url.PathEscape(b.o.Slug) + p }

func (b *appBackend) do(ctx context.Context, method, p string, body, out any) error {
	c, err := b.client(ctx)
	if err != nil {
		return err
	}
	return c.Do(ctx, method, b.path(p), body, out)
}

// Ready signs this computer in when it is not: a remote machine is refused
// first, a key Mishmar no longer takes is told once, then a sign-in starts
// (the browser opened when this call started it) and the call waits for the
// person up to Wait.
func (b *appBackend) Ready(ctx context.Context) string {
	if b.o.Remote.Refused() {
		return fmt.Sprintf(RemoteRefused, b.o.Remote.Describe())
	}
	c, err := b.client(ctx)
	if err != nil {
		return "Error: Mishmar's signer could not start on this computer: " + err.Error()
	}
	st, err := c.BriefStatus(ctx, b.o.Slug)
	if err != nil {
		return "Error: Mishmar's signer did not answer: " + err.Error()
	}
	switch st.State {
	case daemon.SignedIn:
		if r := st.Record; r != nil && r.API != b.o.API {
			return fmt.Sprintf(OtherMishmar, b.o.Slug, r.API, b.o.API, b.o.Slug)
		}
		return ""
	case daemon.Ended:
		b.mu.Lock()
		told := b.told[st.EndedAt]
		b.told[st.EndedAt] = true
		b.mu.Unlock()
		if !told {
			return fmt.Sprintf(KeyEnded, b.o.Slug, st.Ended)
		}
	}
	si, err := c.SignIn(ctx, b.o.Slug, daemon.SignInRequest{API: b.o.API, Environment: b.o.Environment, Hints: b.o.Remote.Hints})
	if err != nil {
		var e *signer.Error
		if errors.As(err, &e) && e.Code == "remote" {
			return fmt.Sprintf(RemoteRefused, strings.TrimPrefix(e.Message, "this machine looks like "))
		}
		return "Error: Mishmar's sign-in could not start: " + err.Error()
	}
	if si.State == string(signin.SignedIn) {
		return ""
	}
	opened := false
	if si.Started && b.o.Open != nil {
		opened = b.o.Open(si.URL) == nil
	}
	pending := si
	if got, err := c.SignInState(ctx, b.o.Slug, b.o.Wait); err == nil {
		pending = got
	}
	return signInText(pending, si, opened)
}

// signInText is what a sign-in that has not ended signed in says.
func signInText(now, started daemon.SignIn, opened bool) string {
	switch now.State {
	case string(signin.SignedIn):
		return ""
	case string(signin.Denied):
		return SignInDenied
	case string(signin.Expired):
		return SignInExpired
	case string(signin.Failed):
		return fmt.Sprintf(SignInFailed, strings.TrimPrefix(now.Error, "signin: "))
	}
	link, code, until := started.URL, started.Code, started.ExpiresAt
	if now.URL != "" {
		link, code, until = now.URL, now.Code, now.ExpiresAt
	}
	format := SignInLink
	if opened {
		format = SignInOpened
	}
	return fmt.Sprintf(format, link, code, until.UTC().Format("15:04 UTC"))
}

// TradingStatus is the status tool's answer. It starts the background
// signer only for an app with a key here (whose signer should run anyway).
func (b *appBackend) TradingStatus(ctx context.Context) (string, error) {
	var lines []string
	if b.o.Remote.Refused() {
		return fmt.Sprintf(RemoteRefused, b.o.Remote.Describe()), nil
	}
	b.mu.Lock()
	c, h := b.c, b.h
	b.mu.Unlock()
	if c == nil {
		start := b.o.HasKey != nil && b.o.HasKey()
		var err error
		if start {
			c, err = b.client(ctx)
			b.mu.Lock()
			h = b.h
			b.mu.Unlock()
		} else {
			c, h, err = b.o.Connect(ctx, false)
		}
		if err != nil {
			return "", err
		}
		if c == nil {
			return fmt.Sprintf(StatusNotSignedIn, b.o.Slug) + "\n" + StatusNoDaemon, nil
		}
	}
	st, err := c.Status(ctx, b.o.Slug)
	if err != nil {
		return "", err
	}
	switch st.State {
	case daemon.SignedOut:
		lines = append(lines, fmt.Sprintf(StatusNotSignedIn, b.o.Slug))
		if st.SignIn != nil {
			lines = append(lines, "The last sign-in: "+signInText(*st.SignIn, *st.SignIn, false))
		}
	case daemon.SigningIn:
		lines = append(lines, signInText(*st.SignIn, *st.SignIn, false))
	case daemon.Ended:
		lines = append(lines, fmt.Sprintf(KeyEnded, b.o.Slug, st.Ended))
	case daemon.SignedIn:
		if r := st.Record; r != nil && r.API != b.o.API {
			lines = append(lines, fmt.Sprintf(OtherMishmar, b.o.Slug, r.API, b.o.API, b.o.Slug))
		}
		lines = append(lines, signedInLines(st)...)
	}
	switch {
	case st.Daemon.InProcess || h.InProcess:
		lines = append(lines, fmt.Sprintf(StatusInProcess, st.Daemon.Version))
	case st.State == daemon.SignedIn && !st.Signer.Running:
		lines = append(lines, fmt.Sprintf(StatusNotRunning, st.Signer.Error))
	default:
		lines = append(lines, fmt.Sprintf(StatusBackground, st.Daemon.Version, st.Daemon.PID))
	}
	return strings.Join(lines, "\n"), nil
}

// signedInLines describe a signed-in app: who it is, then each trading key's
// step.
func signedInLines(st daemon.Status) []string {
	r := st.Record
	if r == nil {
		return []string{"Signed in to Mishmar for " + st.App + "."}
	}
	lines := []string{fmt.Sprintf(StatusSignedIn, st.App, r.AgentName, r.AgentID, r.AccountID, r.MachineName, st.Code,
		r.SignedInAt.UTC().Format("2 Jan 2006"), r.KeyExpiresAt.UTC().Format("2 Jan 2006"))}
	if st.Permission == string(api.AgentPermissionRead) {
		return append(lines, StatusReading)
	}
	if st.ReadError != "" {
		return append(lines, "Mishmar could not be read just now: "+st.ReadError)
	}
	account := func(id string) string {
		if a := st.ConnectedAccount; a != nil && a.ID == id && a.Name != "" {
			name := a.Name
			if a.ConnectionName != "" {
				name = a.ConnectionName + " · " + name
			}
			return name + " (" + id + ")"
		}
		return id
	}
	if len(st.VenueKeys) == 0 {
		return append(lines, fmt.Sprintf(StatusNoKey, r.Next))
	}
	for _, k := range st.VenueKeys {
		acct := account(k.ConnectedAccountID)
		switch {
		case !k.Mine:
			lines = append(lines, fmt.Sprintf(StatusOtherKey, acct))
		case k.Status == string(api.VenueKeyStatusExpired) || k.Status == string(api.VenueKeyStatusRevoked):
			lines = append(lines, fmt.Sprintf(StatusExpiredKey, acct, r.Next))
		case !k.Held || k.Keygen == string(api.VenueKeyKeygenPending):
			lines = append(lines, fmt.Sprintf(StatusKeygen, acct))
		case k.Status == string(api.VenueKeyStatusActive):
			lines = append(lines, fmt.Sprintf(StatusReady, acct))
		case k.Venue == string(api.VenueHyperliquid) && k.HyperliquidStatus != string(api.VenueKeyHyperliquidApprovalStatusApproved):
			page := "https://app.hyperliquid.xyz/API"
			if k.Network == "testnet" {
				page = "https://app.hyperliquid-testnet.xyz/API"
			}
			lines = append(lines, fmt.Sprintf(StatusHyperliquid, acct, k.PublicKey, k.Name, page, r.Next))
		case k.Venue == string(api.VenueBinance) && !k.APIKeyRegistered:
			lines = append(lines, fmt.Sprintf(StatusBinance, acct, r.Next, k.PublicKey))
		default:
			lines = append(lines, fmt.Sprintf(StatusChecking, acct))
		}
	}
	return lines
}

// ---- the Backend over the local API ----

func (b *appBackend) Submit(ctx context.Context, r signer.ProposeRequest) (api.Decision, error) {
	var d api.Decision
	return d, b.do(ctx, http.MethodPost, "/propose", r, &d)
}

func (b *appBackend) Decision(ctx context.Context, id string) (api.Decision, error) {
	var d api.Decision
	return d, b.do(ctx, http.MethodGet, "/decisions/"+url.PathEscape(id), nil, &d)
}

func (b *appBackend) Decisions(ctx context.Context, q signer.DecisionQuery) (api.DecisionPage, error) {
	v := url.Values{}
	set(v, "connectedAccountId", q.ConnectedAccountID)
	set(v, "verdict", q.Verdict)
	set(v, "cursor", q.Cursor)
	setTime(v, "from", q.From)
	setTime(v, "to", q.To)
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var out api.DecisionPage
	return out, b.do(ctx, http.MethodGet, "/decisions"+query(v), nil, &out)
}

func (b *appBackend) Approvals(ctx context.Context, status string) ([]api.Approval, error) {
	v := url.Values{}
	set(v, "status", status)
	var out []api.Approval
	return out, b.do(ctx, http.MethodGet, "/approvals"+query(v), nil, &out)
}

func (b *appBackend) Positions(ctx context.Context) ([]api.Position, error) {
	var out []api.Position
	return out, b.do(ctx, http.MethodGet, "/positions", nil, &out)
}

func (b *appBackend) Pnl(ctx context.Context, from, to string) (api.PnlSeries, error) {
	v := url.Values{}
	set(v, "from", from)
	set(v, "to", to)
	var out api.PnlSeries
	return out, b.do(ctx, http.MethodGet, "/pnl"+query(v), nil, &out)
}

func (b *appBackend) ConnectedAccount(ctx context.Context, id string) (api.ConnectedAccount, error) {
	var out api.ConnectedAccount
	return out, b.do(ctx, http.MethodGet, "/connected-accounts/"+url.PathEscape(id), nil, &out)
}

func (b *appBackend) VenueRules(ctx context.Context, id, symbol string) (api.VenueRules, error) {
	v := url.Values{}
	set(v, "symbol", symbol)
	var out api.VenueRules
	return out, b.do(ctx, http.MethodGet, "/connected-accounts/"+url.PathEscape(id)+"/venue-rules"+query(v), nil, &out)
}

func (b *appBackend) Me(ctx context.Context) (api.AgentIdentity, error) {
	var out api.AgentIdentity
	return out, b.do(ctx, http.MethodGet, "/me", nil, &out)
}

// Permission waits until the app's signer has learnt whether it trades.
func (b *appBackend) Permission(ctx context.Context) (api.AgentPermission, error) {
	c, err := b.client(ctx)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(PermissionWait)
	for {
		st, err := c.BriefStatus(ctx, b.o.Slug)
		if err != nil {
			return "", err
		}
		if st.Permission != "" {
			return api.AgentPermission(st.Permission), nil
		}
		if time.Now().After(deadline) {
			return "", errors.New("the signer has not heard from Mishmar yet whether this agent trades")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (b *appBackend) ConnectedAccounts(ctx context.Context) ([]api.ConnectedAccount, error) {
	var out []api.ConnectedAccount
	return out, b.do(ctx, http.MethodGet, "/connected-accounts", nil, &out)
}

func (b *appBackend) AccountBalances(ctx context.Context, id string, at time.Time) (api.BalanceSnapshot, error) {
	v := url.Values{}
	setTime(v, "at", at)
	var out api.BalanceSnapshot
	return out, b.do(ctx, http.MethodGet, "/connected-accounts/"+url.PathEscape(id)+"/balances"+query(v), nil, &out)
}

func (b *appBackend) AccountPositions(ctx context.Context, id string, at time.Time) (api.AccountPositionSnapshot, error) {
	v := url.Values{}
	setTime(v, "at", at)
	var out api.AccountPositionSnapshot
	return out, b.do(ctx, http.MethodGet, "/connected-accounts/"+url.PathEscape(id)+"/positions"+query(v), nil, &out)
}

func (b *appBackend) AccountTransactions(ctx context.Context, id string, q signer.TransactionQuery) (api.AccountTransactionPage, error) {
	v := url.Values{}
	set(v, "kind", q.Kind)
	set(v, "cursor", q.Cursor)
	setTime(v, "from", q.From)
	setTime(v, "to", q.To)
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var out api.AccountTransactionPage
	return out, b.do(ctx, http.MethodGet, "/connected-accounts/"+url.PathEscape(id)+"/transactions"+query(v), nil, &out)
}

func (b *appBackend) AccountEquity(ctx context.Context, id string, q signer.EquityQuery) (api.EquitySeries, error) {
	v := url.Values{}
	set(v, "resolution", q.Resolution)
	setTime(v, "from", q.From)
	setTime(v, "to", q.To)
	var out api.EquitySeries
	return out, b.do(ctx, http.MethodGet, "/connected-accounts/"+url.PathEscape(id)+"/equity"+query(v), nil, &out)
}

func set(v url.Values, k, val string) {
	if val != "" {
		v.Set(k, val)
	}
}

func setTime(v url.Values, k string, t time.Time) {
	if !t.IsZero() {
		v.Set(k, t.UTC().Format(time.RFC3339))
	}
}

func query(v url.Values) string {
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}
