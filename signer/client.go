package signer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/proposal"
)

// DefaultTimeout bounds one call to the engine; a poll adds its wait.
const DefaultTimeout = 10 * time.Second

// maxResponse bounds what the client reads back: a page of decisions is the
// largest answer.
const maxResponse = 8 << 20

// Client talks to one engine as one agent. Everything under /agent is signed
// per request with the agent's key (the contract's agentAuth headers over the
// method, the path without the server prefix, the raw query and the body);
// POST /proposals carries its signature in the body instead. A bot can
// function with it and cannot act on the account.
type Client struct {
	// Engine is the API base URL including /v1.
	Engine string
	KeyID  string
	Key    *agentkey.Key
	// HTTP is the client used; nil means http.DefaultClient. Every call sets
	// its own deadline (Timeout, plus the wait for a poll).
	HTTP    *http.Client
	Timeout time.Duration
	// Now is the clock the signed-at header uses; nil means time.Now.
	Now func() time.Time
	// Retries is how a call the engine did nothing with (503 while it
	// restarts, 429) goes again; the zero value is the default policy.
	Retries Retries

	instance atomic.Value // string: the engine instance its last answer named
	readOnly atomic.Bool  // a read agent's: nothing but DataRead is sent
	sleep    func(context.Context, time.Duration) error
}

// SetReadOnly makes the client a read agent's: from then on it refuses
// (ErrReadAgent), before anything is sent, every call but the reads of
// connected-account data (DataRead). Run sets it from GET /agent/me.
func (c *Client) SetReadOnly(on bool) { c.readOnly.Store(on) }

// ReadOnly says whether the client sends only reads of connected-account data.
func (c *Client) ReadOnly() bool { return c.readOnly.Load() }

// HeaderEngineInstance names the engine instance that answered; it changes
// each time the engine starts.
const HeaderEngineInstance = "Mishmar-Engine-Instance"

// EngineInstance is the engine instance the last answer that named one came
// from, or "" before any did.
func (c *Client) EngineInstance() string {
	s, _ := c.instance.Load().(string)
	return s
}

// NewClient makes a client for the engine with the agent's key.
func NewClient(engine, keyID string, key *agentkey.Key) *Client {
	return &Client{Engine: engine, KeyID: keyID, Key: key}
}

// Error is the engine's answer when it refused a call: the HTTP status and
// the code and message of its Error body (code "http_error" when the body
// was not one).
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("engine refused (%d %s): %s", e.Status, e.Code, e.Message)
}

// ErrRequestGone is Answer's error when the sign request is no longer open:
// already answered, expired, or never made. Nothing is left to retry.
var ErrRequestGone = errors.New("signer: the sign request is no longer open")

// DecisionQuery filters Decisions. Zero values are left out.
type DecisionQuery struct {
	ConnectedAccountID string
	Verdict            string
	From               time.Time
	To                 time.Time
	Cursor             string
	Limit              int
}

// Poll waits up to wait for requests this agent's signer must sign and
// returns them, possibly none.
func (c *Client) Poll(ctx context.Context, wait time.Duration) ([]api.SignRequest, error) {
	// The contract allows 0 to 30 seconds.
	seconds := min(max(int(wait/time.Second), 0), 30)
	var out []api.SignRequest
	err := c.agentCall(ctx, http.MethodPost, "/agent/signer/poll", nil, map[string]int{"waitSeconds": seconds}, &out, c.timeout()+wait)
	return out, err
}

// Answer delivers the signer's answer to a request: a share, a signature, a
// venue response or a refusal. A request that is no longer open is
// ErrRequestGone.
func (c *Client) Answer(ctx context.Context, requestID string, a api.SignerAnswer) error {
	err := c.agentCall(ctx, http.MethodPost, "/agent/signer/requests/"+url.PathEscape(requestID)+"/answer", nil, a, nil, c.timeout())
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return ErrRequestGone
	}
	return err
}

// Propose submits a proposal to POST /proposals and returns the decision.
// An unsigned proposal (empty signature) is signed with the client's key
// first, and an empty keyId filled in with the client's.
func (c *Client) Propose(ctx context.Context, p api.ProposalSubmit) (api.Decision, error) {
	if p.KeyId == "" {
		p.KeyId = c.KeyID
	}
	if p.Signature == "" {
		s := submitOf(p)
		if err := s.Sign(c.Key); err != nil {
			return api.Decision{}, err
		}
		// Sign defaults an order's reduceOnly; send what was signed.
		p.ReduceOnly, p.Signature = s.ReduceOnly, s.Signature
	}
	p.ProposedAt, p.ExpiresAt = p.ProposedAt.UTC(), p.ExpiresAt.UTC()
	raw, err := json.Marshal(p)
	if err != nil {
		return api.Decision{}, err
	}
	var d api.Decision
	err = c.send(ctx, http.MethodPost, "/proposals", "", raw, nil, &d, c.timeout()+20*time.Second)
	return d, err
}

// Decisions pages this agent's decisions, newest first.
func (c *Client) Decisions(ctx context.Context, q DecisionQuery) (api.DecisionPage, error) {
	v := url.Values{}
	if q.ConnectedAccountID != "" {
		v.Set("connectedAccountId", q.ConnectedAccountID)
	}
	if q.Verdict != "" {
		v.Set("verdict", q.Verdict)
	}
	if !q.From.IsZero() {
		v.Set("from", q.From.UTC().Format(time.RFC3339Nano))
	}
	if !q.To.IsZero() {
		v.Set("to", q.To.UTC().Format(time.RFC3339Nano))
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var page api.DecisionPage
	err := c.agentCall(ctx, http.MethodGet, "/agent/decisions", v, nil, &page, c.timeout())
	return page, err
}

// Decision reads one of this agent's decisions; another agent's is not found.
func (c *Client) Decision(ctx context.Context, decisionID string) (api.Decision, error) {
	var d api.Decision
	err := c.agentCall(ctx, http.MethodGet, "/agent/decisions/"+url.PathEscape(decisionID), nil, nil, &d, c.timeout())
	return d, err
}

// Approvals lists this agent's proposals that wait for, or were decided by,
// a person; status narrows them (pending, approved, rejected, expired).
func (c *Client) Approvals(ctx context.Context, status string) ([]api.Approval, error) {
	v := url.Values{}
	if status != "" {
		v.Set("status", status)
	}
	var out []api.Approval
	err := c.agentCall(ctx, http.MethodGet, "/agent/approvals", v, nil, &out, c.timeout())
	return out, err
}

// Positions lists this agent's open positions.
func (c *Client) Positions(ctx context.Context) ([]api.Position, error) {
	var out []api.Position
	err := c.agentCall(ctx, http.MethodGet, "/agent/positions", nil, nil, &out, c.timeout())
	return out, err
}

// Me is who the key belongs to: the agent, its account and the key id. The
// engine answers only a key it knows, so this is the read that proves one.
func (c *Client) Me(ctx context.Context) (api.AgentIdentity, error) {
	var out api.AgentIdentity
	err := c.agentCall(ctx, http.MethodGet, "/agent/me", nil, nil, &out, c.timeout())
	return out, err
}

// VenueKeys lists this agent's venue keys as the engine holds them: which
// one is active on each connected account, and which were revoked or expired.
func (c *Client) VenueKeys(ctx context.Context) ([]api.VenueKey, error) {
	var out []api.VenueKey
	err := c.agentCall(ctx, http.MethodGet, "/agent/venue-keys", nil, nil, &out, c.timeout())
	return out, err
}

// VenueRules reads what the connected account's venue accepts (tick, step,
// minimums), for building orders; symbol narrows it to one market.
func (c *Client) VenueRules(ctx context.Context, connectedAccountID, symbol string) (api.VenueRules, error) {
	v := url.Values{}
	if symbol != "" {
		v.Set("symbol", symbol)
	}
	var out api.VenueRules
	err := c.agentCall(ctx, http.MethodGet, "/agent/connected-accounts/"+url.PathEscape(connectedAccountID)+"/venue-rules", v, nil, &out, c.timeout())
	return out, err
}

// Commitments takes up to count of the engine's single-use commitments for
// one-shot signing with a venue key (count 0: as many as the account allows).
func (c *Client) Commitments(ctx context.Context, venueKeyID string, count int) (api.CommitmentBatch, error) {
	body := api.CommitmentRequest{VenueKeyId: venueKeyID}
	if count > 0 {
		body.Count = &count
	}
	var out api.CommitmentBatch
	err := c.agentCall(ctx, http.MethodPost, "/agent/signer/commitments", nil, body, &out, c.timeout())
	return out, err
}

// KeygenRound runs one round of two-party key generation for a venue key
// the engine created without a share (signerKeygen).
func (c *Client) KeygenRound(ctx context.Context, in api.KeygenRound) (api.KeygenRoundResult, error) {
	var out api.KeygenRoundResult
	err := c.agentCall(ctx, http.MethodPost, "/agent/signer/keygen", nil, in, &out, c.timeout())
	return out, err
}

// PresignRound runs one round of presigning for a two-party ECDSA venue key
// (signerPresign).
func (c *Client) PresignRound(ctx context.Context, in api.PresignRound) (api.PresignRoundResult, error) {
	var out api.PresignRoundResult
	err := c.agentCall(ctx, http.MethodPost, "/agent/signer/presign", nil, in, &out, c.timeout())
	return out, err
}

// agentCall signs the request with the agent's key and sends it. The path is
// the contract's, without the server prefix; the query is signed exactly as
// sent, and so is the body. Each attempt is signed anew, with its own nonce
// and time, so a retry is never a replay.
func (c *Client) agentCall(ctx context.Context, method, path string, query url.Values, body, out any, timeout time.Duration) error {
	if c.Key == nil {
		return errors.New("signer: the client has no agent key")
	}
	rawQuery := query.Encode()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	sign := func() (map[string]string, error) {
		return c.Key.SignRequest(agentkey.Request{
			Method: method, Path: path, Query: rawQuery, Body: raw, KeyID: c.KeyID, SignedAt: agentkey.SignedAtString(c.now()),
		})
	}
	return c.send(ctx, method, path, rawQuery, raw, sign, out, timeout)
}

// send sends the request, again after an answer that did nothing (Retries),
// and decodes the final answer into out. sign, when given, makes the
// headers of each attempt. Each attempt has its own timeout.
func (c *Client) send(ctx context.Context, method, path, rawQuery string, raw []byte, sign func() (map[string]string, error), out any, timeout time.Duration) error {
	if c.readOnly.Load() && !DataRead(method, path) {
		return fmt.Errorf("%w (%s %s is not one of its reads)", ErrReadAgent, method, path)
	}
	start := time.Now()
	for attempt := 0; ; attempt++ {
		var headers map[string]string
		if sign != nil {
			var err error
			if headers, err = sign(); err != nil {
				return err
			}
		}
		res, data, err := c.once(ctx, method, path, rawQuery, raw, headers, timeout)
		if err != nil {
			return err
		}
		if res.StatusCode >= 200 && res.StatusCode <= 299 {
			if out != nil && len(bytes.TrimSpace(data)) > 0 {
				if err := json.Unmarshal(data, out); err != nil {
					return fmt.Errorf("signer: %s %s: unexpected response: %w", method, path, err)
				}
			}
			return nil
		}
		e := errorOf(res.StatusCode, data)
		wait, again := c.Retries.Wait(attempt, res.StatusCode, res.Header.Get("Retry-After"), time.Since(start))
		if !again {
			return e
		}
		if err := c.pause(ctx, wait); err != nil {
			return errors.Join(e, err)
		}
	}
}

// once sends one request and reads its answer, whatever the status.
func (c *Client) once(ctx context.Context, method, path, rawQuery string, raw []byte, headers map[string]string, timeout time.Duration) (*http.Response, []byte, error) {
	target := strings.TrimRight(c.Engine, "/") + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if raw != nil {
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("signer: %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	if inst := res.Header.Get(HeaderEngineInstance); inst != "" {
		c.instance.Store(inst)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return nil, nil, fmt.Errorf("signer: %s %s: %w", method, path, err)
	}
	return res, data, nil
}

// errorOf is the engine's refusal as an Error: its body's code and message
// when the body is one, the text or the status otherwise.
func errorOf(status int, data []byte) *Error {
	e := &Error{Status: status, Code: "http_error", Message: strings.TrimSpace(string(data))}
	var body api.Error
	if json.Unmarshal(data, &body) == nil && body.Code != "" {
		e.Code, e.Message = body.Code, body.Message
	} else if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

// pause waits before the next attempt, or until ctx ends.
func (c *Client) pause(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// submitOf is the contract's ProposalSubmit as the proposal package signs
// and verifies it, field for field.
func submitOf(p api.ProposalSubmit) proposal.Submit {
	s := proposal.Submit{
		KeyID: p.KeyId, Kind: proposal.Kind(p.Kind), Leverage: p.Leverage, Nonce: p.Nonce,
		OrderType: proposal.OrderType(p.OrderType), Price: p.Price, ProposedAt: p.ProposedAt, ExpiresAt: p.ExpiresAt,
		ReduceOnly: p.ReduceOnly, ConnectedAccountID: p.ConnectedAccountId, Side: proposal.Side(p.Side), Size: p.Size, Symbol: p.Symbol,
		Signature: p.Signature,
	}
	if p.TargetClientOrderId != nil {
		s.TargetClientOrderID = *p.TargetClientOrderId
	}
	if m := p.VenueMessage; m != nil {
		s.VenueMessage = &proposal.VenueMessage{
			VenueKeyID: m.VenueKeyId, Scheme: string(m.Scheme), Message: m.Message,
			EngineCommitmentID: m.EngineCommitmentId, Share: m.Share,
		}
		if m.Commitment != nil {
			s.VenueMessage.Commitment = *m.Commitment
		}
	}
	return s
}

// Pnl is this agent's daily PnL; from and to are dates (YYYY-MM-DD) and
// may be empty.
func (c *Client) Pnl(ctx context.Context, from, to string) (api.PnlSeries, error) {
	v := url.Values{}
	if from != "" {
		v.Set("from", from)
	}
	if to != "" {
		v.Set("to", to)
	}
	var out api.PnlSeries
	err := c.agentCall(ctx, http.MethodGet, "/agent/pnl", v, nil, &out, c.timeout())
	return out, err
}

// ConnectedAccount is a connected account as this agent may see it: its setup
// without any secret.
func (c *Client) ConnectedAccount(ctx context.Context, connectedAccountID string) (api.ConnectedAccount, error) {
	var out api.ConnectedAccount
	err := c.agentCall(ctx, http.MethodGet, connectedAccountPath(connectedAccountID, ""), nil, nil, &out, c.timeout())
	return out, err
}

// The reads of connected-account data. An agent reads each connected account
// it was given whole, other agents' and the owner's activity included, and
// every read is written to the account's access log. On the hosted service
// these are the agent calls a collector answers while the account's engine
// sleeps (with Me and ConnectedAccount), so an agent that only reads never
// wakes it; anything else an agent calls does.

// ConnectedAccounts are the connected accounts this agent was given, in the
// order it got them.
func (c *Client) ConnectedAccounts(ctx context.Context) ([]api.ConnectedAccount, error) {
	var out []api.ConnectedAccount
	err := c.agentCall(ctx, http.MethodGet, "/agent/connected-accounts", nil, nil, &out, c.timeout())
	return out, err
}

// AccountBalances are the connected account's balances at the venue, spot and
// margin, each with its USD value, at the moment at; the zero time means now.
// A moment before the account's history or the plan's retention is not found.
func (c *Client) AccountBalances(ctx context.Context, connectedAccountID string, at time.Time) (api.BalanceSnapshot, error) {
	var out api.BalanceSnapshot
	err := c.agentCall(ctx, http.MethodGet, connectedAccountPath(connectedAccountID, "/balances"), atQuery(at), nil, &out, c.timeout())
	return out, err
}

// AccountPositions are the venue's own net positions on the connected account,
// whoever placed the orders behind them, at the moment at (zero: now). This
// agent's own book is Positions.
func (c *Client) AccountPositions(ctx context.Context, connectedAccountID string, at time.Time) (api.AccountPositionSnapshot, error) {
	var out api.AccountPositionSnapshot
	err := c.agentCall(ctx, http.MethodGet, connectedAccountPath(connectedAccountID, "/positions"), atQuery(at), nil, &out, c.timeout())
	return out, err
}

// TransactionQuery filters AccountTransactions. Zero values are left out.
type TransactionQuery struct {
	From time.Time
	To   time.Time
	// Kind is one of the contract's AccountTransactionKind: trade,
	// transfer_in, transfer_out, funding, fee, liquidation, rebate, other.
	Kind string
	// Cursor is the page's nextCursor to read the page after it.
	Cursor string
	// Limit is 1 to 500; the engine's default is 100.
	Limit int
}

// AccountTransactions pages the connected account's trades, transfers,
// funding and fees as the venue reports them, newest first. A trade's origin
// says who placed it, with Own set on the ones this agent placed.
func (c *Client) AccountTransactions(ctx context.Context, connectedAccountID string, q TransactionQuery) (api.AccountTransactionPage, error) {
	v := rangeQuery(q.From, q.To)
	if q.Kind != "" {
		v.Set("kind", q.Kind)
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var out api.AccountTransactionPage
	err := c.agentCall(ctx, http.MethodGet, connectedAccountPath(connectedAccountID, "/transactions"), v, nil, &out, c.timeout())
	return out, err
}

// EquityQuery is the range and spacing of AccountEquity. Zero values are left
// out: the engine then picks a spacing that gives a few hundred points.
type EquityQuery struct {
	From time.Time
	To   time.Time
	// Resolution is minute, hour or day; never finer than the plan allows.
	Resolution string
}

// AccountEquity is the connected account's value in USD over time, oldest
// point first.
func (c *Client) AccountEquity(ctx context.Context, connectedAccountID string, q EquityQuery) (api.EquitySeries, error) {
	v := rangeQuery(q.From, q.To)
	if q.Resolution != "" {
		v.Set("resolution", q.Resolution)
	}
	var out api.EquitySeries
	err := c.agentCall(ctx, http.MethodGet, connectedAccountPath(connectedAccountID, "/equity"), v, nil, &out, c.timeout())
	return out, err
}

// connectedAccountPath is the agent-facing path of a connected account, and
// of one of its reads when sub is given ("/balances").
func connectedAccountPath(connectedAccountID, sub string) string {
	return "/agent/connected-accounts/" + url.PathEscape(connectedAccountID) + sub
}

func atQuery(at time.Time) url.Values {
	v := url.Values{}
	if !at.IsZero() {
		v.Set("at", at.UTC().Format(time.RFC3339Nano))
	}
	return v
}

func rangeQuery(from, to time.Time) url.Values {
	v := url.Values{}
	if !from.IsZero() {
		v.Set("from", from.UTC().Format(time.RFC3339Nano))
	}
	if !to.IsZero() {
		v.Set("to", to.UTC().Format(time.RFC3339Nano))
	}
	return v
}
