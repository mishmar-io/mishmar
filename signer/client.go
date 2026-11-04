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
	sleep    func(context.Context, time.Duration) error
}

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
	TradingAccountID string
	Verdict          string
	From             time.Time
	To               time.Time
	Cursor           string
	Limit            int
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
	if q.TradingAccountID != "" {
		v.Set("tradingAccountId", q.TradingAccountID)
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

// VenueKeys lists this agent's venue keys as the engine holds them: which
// one is active on each trading account, and which were revoked or expired.
func (c *Client) VenueKeys(ctx context.Context) ([]api.VenueKey, error) {
	var out []api.VenueKey
	err := c.agentCall(ctx, http.MethodGet, "/agent/venue-keys", nil, nil, &out, c.timeout())
	return out, err
}

// VenueRules reads what the trading account's venue accepts (tick, step,
// minimums), for building orders; symbol narrows it to one market.
func (c *Client) VenueRules(ctx context.Context, tradingAccountID, symbol string) (api.VenueRules, error) {
	v := url.Values{}
	if symbol != "" {
		v.Set("symbol", symbol)
	}
	var out api.VenueRules
	err := c.agentCall(ctx, http.MethodGet, "/agent/trading-accounts/"+url.PathEscape(tradingAccountID)+"/venue-rules", v, nil, &out, c.timeout())
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
		ReduceOnly: p.ReduceOnly, TradingAccountID: p.TradingAccountId, Side: proposal.Side(p.Side), Size: p.Size, Symbol: p.Symbol,
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

// TradingAccount is a trading account as this agent may see it: its setup
// without any secret.
func (c *Client) TradingAccount(ctx context.Context, tradingAccountID string) (api.TradingAccount, error) {
	var out api.TradingAccount
	err := c.agentCall(ctx, http.MethodGet, "/agent/trading-accounts/"+url.PathEscape(tradingAccountID), nil, nil, &out, c.timeout())
	return out, err
}
