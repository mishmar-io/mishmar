// Package mcp serves the agent's tools to a language model over the Model
// Context Protocol (JSON-RPC 2.0 over stdio, newline-delimited): propose an
// order or a cancel, follow a decision, read positions, PnL, approvals and
// the venue's rules. Behind the tools is the agent's signer, in the same
// process, so the model never sees a key and every order it proposes is
// checked by the engine's rules before anything reaches a venue.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
)

// ProtocolVersion is the MCP revision this server speaks; older clients
// get their own revision echoed when it is one the server also knows.
const ProtocolVersion = "2025-06-18"

var knownVersions = map[string]bool{"2025-06-18": true, "2025-03-26": true, "2024-11-05": true}

// Backend is what the tools act on: the signer with the agent's key.
type Backend interface {
	Submit(ctx context.Context, r signer.ProposeRequest) (api.Decision, error)
	Decision(ctx context.Context, id string) (api.Decision, error)
	Decisions(ctx context.Context, q signer.DecisionQuery) (api.DecisionPage, error)
	Approvals(ctx context.Context, status string) ([]api.Approval, error)
	Positions(ctx context.Context) ([]api.Position, error)
	Pnl(ctx context.Context, from, to string) (api.PnlSeries, error)
	TradingAccount(ctx context.Context, id string) (api.TradingAccount, error)
	VenueRules(ctx context.Context, id, symbol string) (api.VenueRules, error)
}

// SetupURL is where a person learns to make the agent's config.
const SetupURL = "https://docs.mishmar.io/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/"

// Unconfigured is the Backend of a server started before the agent's config
// exists: the tools are listed, so a client (or a directory's check) can see
// them, and every call says where the config was looked for and how to make it.
func Unconfigured(path string) Backend { return unconfigured{path} }

type unconfigured struct{ path string }

func (u unconfigured) err() error {
	return fmt.Errorf("no agent config at %s: add an agent and make its trading key first (%s), then restart this server", u.path, SetupURL)
}

func (u unconfigured) Submit(context.Context, signer.ProposeRequest) (api.Decision, error) {
	return api.Decision{}, u.err()
}
func (u unconfigured) Decision(context.Context, string) (api.Decision, error) {
	return api.Decision{}, u.err()
}
func (u unconfigured) Decisions(context.Context, signer.DecisionQuery) (api.DecisionPage, error) {
	return api.DecisionPage{}, u.err()
}
func (u unconfigured) Approvals(context.Context, string) ([]api.Approval, error) { return nil, u.err() }
func (u unconfigured) Positions(context.Context) ([]api.Position, error)         { return nil, u.err() }
func (u unconfigured) Pnl(context.Context, string, string) (api.PnlSeries, error) {
	return api.PnlSeries{}, u.err()
}
func (u unconfigured) TradingAccount(context.Context, string) (api.TradingAccount, error) {
	return api.TradingAccount{}, u.err()
}
func (u unconfigured) VenueRules(context.Context, string, string) (api.VenueRules, error) {
	return api.VenueRules{}, u.err()
}

// SignerBackend is the signer as a Backend.
func SignerBackend(s *signer.Signer) Backend { return signerBackend{s} }

type signerBackend struct{ s *signer.Signer }

func (b signerBackend) Submit(ctx context.Context, r signer.ProposeRequest) (api.Decision, error) {
	return b.s.Submit(ctx, r)
}
func (b signerBackend) Decision(ctx context.Context, id string) (api.Decision, error) {
	return b.s.Client.Decision(ctx, id)
}
func (b signerBackend) Decisions(ctx context.Context, q signer.DecisionQuery) (api.DecisionPage, error) {
	return b.s.Client.Decisions(ctx, q)
}
func (b signerBackend) Approvals(ctx context.Context, status string) ([]api.Approval, error) {
	return b.s.Client.Approvals(ctx, status)
}
func (b signerBackend) Positions(ctx context.Context) ([]api.Position, error) {
	return b.s.Client.Positions(ctx)
}
func (b signerBackend) Pnl(ctx context.Context, from, to string) (api.PnlSeries, error) {
	return b.s.Client.Pnl(ctx, from, to)
}
func (b signerBackend) TradingAccount(ctx context.Context, id string) (api.TradingAccount, error) {
	return b.s.Client.TradingAccount(ctx, id)
}
func (b signerBackend) VenueRules(ctx context.Context, id, symbol string) (api.VenueRules, error) {
	return b.s.Client.VenueRules(ctx, id, symbol)
}

// Server speaks MCP for one agent.
type Server struct {
	backend Backend
	version string
	log     *slog.Logger
	tools   []tool

	wmu sync.Mutex
	w   io.Writer
}

// New makes a server; version names this build in serverInfo. A nil logger
// discards; logging must never go to the protocol's stdout.
func New(b Backend, version string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{backend: b, version: version, log: log}
	s.tools = s.toolTable()
	return s
}

// Instructions is what the server tells the model about itself.
const Instructions = `Mishmar guards a trading agent: every order you propose is checked by the
engine against the account's risk rules before anything reaches the venue,
and signed by two parties, neither of which is you. A decision is one of
allow (sent to the venue; the venue's acknowledgement is on it),
require_approval (parked for a person; use wait_for_decision to follow it,
or carry on and read it later) or block (the breached rule and the observed
and limit values are on it). Sizes and prices are decimal strings as the
venue quotes them (symbol BTC on Hyperliquid, BTCUSDT on Binance). A market
order is priced at the engine's mark. A cancel names the resting order's
clientOrderId, which its decision carries. Read the trading account and
its venue rules first when you do not know the symbols, tick sizes or
minimums.`

// ---- JSON-RPC ----

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Serve reads requests from r and writes answers to w until r ends or ctx
// is done. Requests are served concurrently (a wait_for_decision must not
// hold up a ping); answers are written one at a time.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.w = w
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			select {
			case lines <- []byte(line):
			case <-ctx.Done():
				return
			}
		}
		readErr <- sc.Err()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case line := <-lines:
			var req request
			if err := json.Unmarshal(line, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
				if len(req.ID) > 0 {
					s.reply(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidRequest, Message: "not a JSON-RPC 2.0 request"}})
				} else {
					s.reply(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: codeParse, Message: "not a JSON-RPC 2.0 request"}})
				}
				continue
			}
			wg.Add(1)
			go func(req request) {
				defer wg.Done()
				s.handle(ctx, req)
			}(req)
		}
	}
}

func (s *Server) reply(res response) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	b, err := json.Marshal(res)
	if err != nil {
		return
	}
	_, _ = s.w.Write(append(b, '\n'))
}

func (s *Server) handle(ctx context.Context, req request) {
	notification := len(req.ID) == 0 || string(req.ID) == "null"
	result, rerr := s.dispatch(ctx, req)
	if notification {
		return // notifications get no answer, not even an error
	}
	if rerr != nil {
		s.reply(response{JSONRPC: "2.0", ID: req.ID, Error: rerr})
		return
	}
	s.reply(response{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := ProtocolVersion
		if knownVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "mishmar", "version": s.version},
			"instructions":    Instructions,
		}, nil
	case "notifications/initialized", "notifications/cancelled", "notifications/roots/list_changed":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		out := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			out = append(out, map[string]any{"name": t.name, "title": t.title, "description": t.description, "inputSchema": t.schema, "annotations": t.annotations()})
		}
		return map[string]any{"tools": out}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a name"}
		}
		for _, t := range s.tools {
			if t.name != p.Name {
				continue
			}
			if u, ok := s.backend.(unconfigured); ok {
				return toolResult("Error: "+u.err().Error(), true), nil // before the arguments: the config is what is missing
			}
			args := p.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			text, err := t.run(ctx, args)
			if err != nil {
				s.log.Info("mcp: tool failed", "tool", t.name, "err", err)
				return toolResult("Error: "+err.Error(), true), nil
			}
			return toolResult(text, false), nil
		}
		return nil, &rpcError{Code: codeInvalidParams, Message: "no tool named " + p.Name}
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "no method " + req.Method}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

// ---- tools ----

type tool struct {
	name, title, description string
	schema                   map[string]any
	// readOnly tools only read this agent's own account; the others change
	// what rests or is held at the exchange. idempotent applies to the latter.
	readOnly, idempotent bool
	run                  func(ctx context.Context, args json.RawMessage) (string, error)
}

// annotations are the MCP tool hints a client uses to decide which calls to
// confirm with the person: the read tools change nothing and stay inside the
// agent's own account, while propose_order and cancel_order reach the exchange.
func (t tool) annotations() map[string]any {
	if t.readOnly {
		return map[string]any{"title": t.title, "readOnlyHint": true, "openWorldHint": false}
	}
	return map[string]any{"title": t.title, "readOnlyHint": false, "destructiveHint": true, "idempotentHint": t.idempotent, "openWorldHint": true}
}

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func decode[T any](args json.RawMessage) (T, error) {
	var v T
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("arguments: %w", err)
	}
	return v, nil
}

func (s *Server) toolTable() []tool {
	return []tool{
		{
			name:        "propose_order",
			title:       "Propose an order",
			description: "Propose an order on a trading account. The engine checks it against the account's rules and, when allowed, signs and sends it to the venue; the decision says what happened. A limit order needs a price; a market order is priced at the engine's mark. Sizes and prices are decimal strings.",
			schema: schema(map[string]any{
				"tradingAccountId": prop("string", "The trading account (ta_…)."),
				"symbol":           prop("string", "The venue's symbol: BTC on Hyperliquid, BTCUSDT on Binance."),
				"side":             map[string]any{"type": "string", "enum": []string{"buy", "sell"}},
				"size":             prop("string", "Quantity as a decimal string."),
				"orderType":        map[string]any{"type": "string", "enum": []string{"limit", "market"}, "description": "limit (default) or market."},
				"price":            prop("string", "Limit price as a decimal string; not for a market order."),
				"reduceOnly":       prop("boolean", "The order may only reduce the position (an exit)."),
				"leverage":         prop("string", "Leverage as a decimal string, where the venue takes it per order."),
				"validForSeconds":  prop("integer", "How long the proposal stands, at most 86400; default 60. An order parked for a person past it expires."),
			}, "tradingAccountId", "symbol", "side", "size"),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[signer.ProposeRequest](args)
				if err != nil {
					return "", err
				}
				in.Kind, in.TargetClientOrderID, in.Channel = "order", nil, false
				d, err := s.backend.Submit(ctx, in)
				if err != nil {
					return "", err
				}
				return describeDecision(d)
			},
		},
		{
			name:        "cancel_order",
			title:       "Cancel an order",
			idempotent:  true,
			description: "Cancel a resting order this agent placed, by the clientOrderId its decision carries. No rule runs on a cancel and a stopped account still lets it through.",
			schema: schema(map[string]any{
				"tradingAccountId": prop("string", "The trading account (ta_…)."),
				"symbol":           prop("string", "The order's symbol."),
				"clientOrderId":    prop("string", "The resting order's client order id (msh-…)."),
			}, "tradingAccountId", "symbol", "clientOrderId"),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[struct {
					TradingAccountID string `json:"tradingAccountId"`
					Symbol           string `json:"symbol"`
					ClientOrderID    string `json:"clientOrderId"`
				}](args)
				if err != nil {
					return "", err
				}
				d, err := s.backend.Submit(ctx, signer.ProposeRequest{Kind: "cancel", TradingAccountID: in.TradingAccountID, Symbol: in.Symbol, TargetClientOrderID: &in.ClientOrderID})
				if err != nil {
					return "", err
				}
				return describeDecision(d)
			},
		},
		{
			name:        "get_decision",
			title:       "Get a decision",
			readOnly:    true,
			description: "Read one decision of this agent: its verdict, every rule's evaluation, the signing outcome, the venue's acknowledgement and, if it was parked, the approval's state.",
			schema:      schema(map[string]any{"decisionId": prop("string", "The decision (dec_…).")}, "decisionId"),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[struct {
					DecisionID string `json:"decisionId"`
				}](args)
				if err != nil {
					return "", err
				}
				d, err := s.backend.Decision(ctx, in.DecisionID)
				if err != nil {
					return "", err
				}
				return describeDecision(d)
			},
		},
		{
			name:        "wait_for_decision",
			title:       "Wait for a decision",
			readOnly:    true,
			description: "Follow a decision parked for a person until it is approved, rejected or expired, or until the timeout; returns the decision as it then stands.",
			schema: schema(map[string]any{
				"decisionId":     prop("string", "The decision (dec_…)."),
				"timeoutSeconds": prop("integer", "How long to wait, at most 600; default 60."),
			}, "decisionId"),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[struct {
					DecisionID     string `json:"decisionId"`
					TimeoutSeconds int    `json:"timeoutSeconds"`
				}](args)
				if err != nil {
					return "", err
				}
				timeout := 60 * time.Second
				if in.TimeoutSeconds > 0 {
					timeout = time.Duration(min(in.TimeoutSeconds, 600)) * time.Second
				}
				deadline := time.Now().Add(timeout)
				for {
					d, err := s.backend.Decision(ctx, in.DecisionID)
					if err != nil {
						return "", err
					}
					if Resolved(d) || time.Now().After(deadline) {
						return describeDecision(d)
					}
					select {
					case <-ctx.Done():
						return "", ctx.Err()
					case <-time.After(time.Second):
					}
				}
			},
		},
		{
			name:        "list_decisions",
			title:       "List decisions",
			readOnly:    true,
			description: "This agent's recent decisions, newest first.",
			schema: schema(map[string]any{
				"tradingAccountId": prop("string", "Only this trading account."),
				"verdict":          map[string]any{"type": "string", "enum": []string{"allow", "require_approval", "block"}},
				"limit":            prop("integer", "How many, default 20."),
			}),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[struct {
					TradingAccountID string `json:"tradingAccountId"`
					Verdict          string `json:"verdict"`
					Limit            int    `json:"limit"`
				}](args)
				if err != nil {
					return "", err
				}
				if in.Limit <= 0 {
					in.Limit = 20
				}
				page, err := s.backend.Decisions(ctx, signer.DecisionQuery{TradingAccountID: in.TradingAccountID, Verdict: in.Verdict, Limit: in.Limit})
				if err != nil {
					return "", err
				}
				return asJSON(page)
			},
		},
		{
			name:        "list_positions",
			title:       "List positions",
			readOnly:    true,
			description: "This agent's open positions, with the mark price and unrealised PnL as the engine last saw them.",
			schema:      schema(map[string]any{}),
			run: func(ctx context.Context, _ json.RawMessage) (string, error) {
				out, err := s.backend.Positions(ctx)
				if err != nil {
					return "", err
				}
				return asJSON(out)
			},
		},
		{
			name:        "get_pnl",
			title:       "Get PnL",
			readOnly:    true,
			description: "This agent's daily PnL (realised, unrealised, fees, funding), by day.",
			schema: schema(map[string]any{
				"from": prop("string", "First day, YYYY-MM-DD."),
				"to":   prop("string", "Last day, YYYY-MM-DD."),
			}),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[struct {
					From string `json:"from"`
					To   string `json:"to"`
				}](args)
				if err != nil {
					return "", err
				}
				out, err := s.backend.Pnl(ctx, in.From, in.To)
				if err != nil {
					return "", err
				}
				return asJSON(out)
			},
		},
		{
			name:        "list_approvals",
			title:       "List approvals",
			readOnly:    true,
			description: "This agent's proposals that wait for a person, or were decided by one.",
			schema:      schema(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"pending", "approved", "rejected", "expired"}}}),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[struct {
					Status string `json:"status"`
				}](args)
				if err != nil {
					return "", err
				}
				out, err := s.backend.Approvals(ctx, in.Status)
				if err != nil {
					return "", err
				}
				return asJSON(out)
			},
		},
		{
			name:        "get_trading_account",
			title:       "Get a trading account",
			readOnly:    true,
			description: "A trading account this agent may trade on: its venue, kind, status and setup, without any secret.",
			schema:      schema(map[string]any{"tradingAccountId": prop("string", "The trading account (ta_…).")}, "tradingAccountId"),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[struct {
					TradingAccountID string `json:"tradingAccountId"`
				}](args)
				if err != nil {
					return "", err
				}
				out, err := s.backend.TradingAccount(ctx, in.TradingAccountID)
				if err != nil {
					return "", err
				}
				return asJSON(out)
			},
		},
		{
			name:        "get_venue_rules",
			title:       "Get venue rules",
			readOnly:    true,
			description: "What the trading account's venue accepts per symbol: tick size, step size, minimums, as the engine last read them. Use it to size and price orders.",
			schema: schema(map[string]any{
				"tradingAccountId": prop("string", "The trading account (ta_…)."),
				"symbol":           prop("string", "One symbol; all when left out."),
			}, "tradingAccountId"),
			run: func(ctx context.Context, args json.RawMessage) (string, error) {
				in, err := decode[struct {
					TradingAccountID string `json:"tradingAccountId"`
					Symbol           string `json:"symbol"`
				}](args)
				if err != nil {
					return "", err
				}
				out, err := s.backend.VenueRules(ctx, in.TradingAccountID, in.Symbol)
				if err != nil {
					return "", err
				}
				return asJSON(out)
			},
		},
	}
}

// Resolved says whether a decision no longer waits for a person.
func Resolved(d api.Decision) bool {
	if d.Verdict != api.VerdictRequireApproval {
		return true
	}
	if d.Approval == nil {
		return false
	}
	switch d.Approval.Status {
	case api.ApprovalStatusApproved, api.ApprovalStatusRejected, api.ApprovalStatusExpired:
		return true
	}
	return false
}

// describeDecision is one line a model reads at a glance, then the decision
// as JSON for the details.
func describeDecision(d api.Decision) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Decision %s: %s", d.Id, d.Verdict)
	if d.Reason != nil && *d.Reason != "" {
		fmt.Fprintf(&b, " (%s)", *d.Reason)
	}
	switch {
	case d.Proposal.Kind == api.ProposalKindCancel && d.Proposal.TargetClientOrderId != nil:
		fmt.Fprintf(&b, ". Cancel of %s on %s.", *d.Proposal.TargetClientOrderId, d.Proposal.Venue)
	default:
		fmt.Fprintf(&b, ". %s %s %s on %s, notional %s USD.", d.Proposal.Side, d.Proposal.Size, d.Proposal.Symbol, d.Proposal.Venue, d.Proposal.NotionalUsd)
	}
	for _, e := range d.Evaluations {
		if !e.Passed && (e.Waived == nil || !*e.Waived) {
			fmt.Fprintf(&b, " Breached %s (%s): observed %s, limit %s.", e.RuleName, e.Metric, e.Observed, e.Threshold)
		}
	}
	if d.VenueAck != nil {
		fmt.Fprintf(&b, " Venue: %s, order %s.", d.VenueAck.Status, d.VenueAck.OrderId)
	}
	if d.Verdict == api.VerdictRequireApproval {
		if Resolved(d) {
			fmt.Fprintf(&b, " Approval: %s.", d.Approval.Status)
		} else {
			fmt.Fprint(&b, " Parked for a person's approval: call wait_for_decision with this id, or carry on.")
		}
	}
	if d.Signing != nil && d.Signing.Status != api.SigningStatusStatusSigned && d.Verdict == api.VerdictAllow {
		fmt.Fprintf(&b, " Signing: %s", d.Signing.Status)
		if d.Signing.Detail != nil && *d.Signing.Detail != "" {
			fmt.Fprintf(&b, " (%s)", *d.Signing.Detail)
		}
		fmt.Fprint(&b, ".")
	}
	js, err := asJSON(d)
	if err != nil {
		return "", err
	}
	return b.String() + "\n" + js, nil
}

func asJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ErrNoBackend is Serve's error when New was given none.
var ErrNoBackend = errors.New("mcp: no backend")
