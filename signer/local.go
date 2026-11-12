package signer

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/proposal"
)

// The signer's local API: what an SDK, or `mishmar mcp`, on the same machine
// calls so that the one process holding the agent's key and shares does the
// proposing. The SDK then holds no secret at all: it starts `mishmar signer
// run --local …` as a child process and talks to it over a Unix socket (or,
// on Windows and in development, over localhost with a token). The API
// turns an intent into a proposal signed with the agent's key, sends it
// one-shot where the signer can build the venue's message and through the
// channel otherwise, and passes the agent's reads through.

// EnvLocalToken names the variable the CLI reads the local API's token from.
const EnvLocalToken = "MISHMAR_LOCAL_TOKEN"

// LocalError is the local API's answer to a request it cannot serve: an
// HTTP status with the code and message shape the engine's errors use.
type LocalError struct {
	Status  int
	Code    string
	Message string
}

func (e *LocalError) Error() string { return e.Code + ": " + e.Message }

func invalidRequest(format string, a ...any) error {
	return &LocalError{Status: http.StatusUnprocessableEntity, Code: "validation", Message: fmt.Sprintf(format, a...)}
}

// ProposeRequest is an intent the signer turns into a signed proposal: an
// order or a cancel, without the key, nonce, times and signature, which the
// signer supplies.
type ProposeRequest struct {
	ConnectedAccountID string `json:"connectedAccountId"`
	// Kind is order (the default) or cancel.
	Kind   string `json:"kind,omitempty"`
	Symbol string `json:"symbol"`
	// Side, Size, OrderType (limit, the default, or market), Price (limit
	// only), Leverage and ReduceOnly describe an order.
	Side       string  `json:"side,omitempty"`
	Size       string  `json:"size,omitempty"`
	OrderType  string  `json:"orderType,omitempty"`
	Price      *string `json:"price,omitempty"`
	Leverage   *string `json:"leverage,omitempty"`
	ReduceOnly *bool   `json:"reduceOnly,omitempty"`
	// TargetClientOrderID is the resting order a cancel pulls (msh-…).
	TargetClientOrderID *string `json:"targetClientOrderId,omitempty"`
	// ValidForSeconds is the agent's deadline for the proposal: 60 by
	// default, at most 86400.
	ValidForSeconds int `json:"validForSeconds,omitempty"`
	// Channel leaves the venue request to the engine and this signer's
	// poller even where one-shot is possible.
	Channel bool `json:"channel,omitempty"`
}

// Submit turns the intent into a proposal signed with the agent's key and
// sends it: one-shot where the signer can build the venue's message, through
// the channel otherwise or when asked. The decision comes back as the
// engine recorded it; a parked one carries its approval id.
func (s *Signer) Submit(ctx context.Context, r ProposeRequest) (api.Decision, error) {
	p, err := s.proposalOf(r)
	if err != nil {
		return api.Decision{}, err
	}
	if r.Channel {
		return s.Client.Propose(ctx, p)
	}
	return s.Propose(ctx, p)
}

// proposalOf is the proposal for an intent, checked before it is signed.
func (s *Signer) proposalOf(r ProposeRequest) (api.ProposalSubmit, error) {
	validFor := time.Minute
	if r.ValidForSeconds != 0 {
		validFor = time.Duration(r.ValidForSeconds) * time.Second
	}
	if validFor <= 0 || validFor > 24*time.Hour {
		return api.ProposalSubmit{}, invalidRequest("validForSeconds must be 1 to 86400")
	}
	connectedAccount, symbol := strings.TrimSpace(r.ConnectedAccountID), strings.TrimSpace(r.Symbol)
	if connectedAccount == "" {
		return api.ProposalSubmit{}, invalidRequest("connectedAccountId is required")
	}
	if symbol == "" {
		return api.ProposalSubmit{}, invalidRequest("symbol is required")
	}
	nonce, err := proposal.NewNonce()
	if err != nil {
		return api.ProposalSubmit{}, err
	}
	now := s.now().UTC()
	p := api.ProposalSubmit{
		KeyId: s.Config.KeyID, ConnectedAccountId: connectedAccount, Symbol: symbol,
		Nonce: nonce, ProposedAt: now, ExpiresAt: now.Add(validFor),
	}
	switch strings.ToLower(strings.TrimSpace(r.Kind)) {
	case "", "order":
		p.Kind = api.ProposalKindOrder
		side := strings.ToLower(strings.TrimSpace(r.Side))
		if side != "buy" && side != "sell" {
			return api.ProposalSubmit{}, invalidRequest("side must be buy or sell")
		}
		p.Side = api.Side(side)
		if strings.TrimSpace(r.Size) == "" {
			return api.ProposalSubmit{}, invalidRequest("size is required, a decimal string")
		}
		p.Size = strings.TrimSpace(r.Size)
		switch strings.ToLower(strings.TrimSpace(r.OrderType)) {
		case "", "limit":
			if r.Price == nil || strings.TrimSpace(*r.Price) == "" {
				return api.ProposalSubmit{}, invalidRequest("a limit order needs a price (or orderType market, priced at the engine's mark)")
			}
			p.OrderType = api.ProposalSubmitOrderTypeLimit
			price := strings.TrimSpace(*r.Price)
			p.Price = &price
		case "market":
			if r.Price != nil && strings.TrimSpace(*r.Price) != "" {
				return api.ProposalSubmit{}, invalidRequest("a market order takes no price: it is priced at the engine's mark")
			}
			p.OrderType = api.ProposalSubmitOrderTypeMarket
		default:
			return api.ProposalSubmit{}, invalidRequest("orderType must be limit or market")
		}
		reduceOnly := r.ReduceOnly != nil && *r.ReduceOnly
		p.ReduceOnly = &reduceOnly
		if r.Leverage != nil && strings.TrimSpace(*r.Leverage) != "" {
			leverage := strings.TrimSpace(*r.Leverage)
			p.Leverage = &leverage
		}
	case "cancel":
		p.Kind = api.ProposalKindCancel
		if r.TargetClientOrderID == nil || strings.TrimSpace(*r.TargetClientOrderID) == "" {
			return api.ProposalSubmit{}, invalidRequest("a cancel needs targetClientOrderId, the resting order's client order id (msh-…)")
		}
		target := strings.TrimSpace(*r.TargetClientOrderID)
		p.TargetClientOrderId = &target
	default:
		return api.ProposalSubmit{}, invalidRequest("kind must be order or cancel")
	}
	return p, nil
}

// LocalHandler serves the local API. With a token, every request must carry
// it as a bearer token.
func (s *Signer) LocalHandler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		keys := s.venueKeys()
		ids := make([]string, 0, len(keys))
		for id := range keys {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		perm, _ := s.Permission()
		writeLocal(w, http.StatusOK, map[string]any{
			"agentId": s.Config.AgentID, "keyId": s.Config.KeyID, "engine": s.Client.Engine,
			"venueKeys": ids, "engineInstance": s.Client.EngineInstance(), "permission": perm,
		})
	})
	mux.HandleFunc("POST /propose", func(w http.ResponseWriter, r *http.Request) {
		var in ProposeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeLocalError(w, invalidRequest("body: %v", err))
			return
		}
		d, err := s.Submit(r.Context(), in)
		if err != nil {
			writeLocalError(w, err)
			return
		}
		writeLocal(w, http.StatusOK, d)
	})
	mux.HandleFunc("GET /decisions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query := DecisionQuery{ConnectedAccountID: q.Get("connectedAccountId"), Verdict: q.Get("verdict"), Cursor: q.Get("cursor")}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				writeLocalError(w, invalidRequest("limit must be a positive integer"))
				return
			}
			query.Limit = n
		}
		var err error
		if query.From, err = parseLocalTime(q.Get("from")); err != nil {
			writeLocalError(w, invalidRequest("from: %v", err))
			return
		}
		if query.To, err = parseLocalTime(q.Get("to")); err != nil {
			writeLocalError(w, invalidRequest("to: %v", err))
			return
		}
		page, err := s.Client.Decisions(r.Context(), query)
		writeLocalResult(w, page, err)
	})
	mux.HandleFunc("GET /decisions/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, err := s.Client.Decision(r.Context(), r.PathValue("id"))
		writeLocalResult(w, d, err)
	})
	mux.HandleFunc("GET /approvals", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Client.Approvals(r.Context(), r.URL.Query().Get("status"))
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /positions", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Client.Positions(r.Context())
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /pnl", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Client.Pnl(r.Context(), r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /connected-accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Client.ConnectedAccount(r.Context(), r.PathValue("id"))
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /connected-accounts/{id}/venue-rules", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Client.VenueRules(r.Context(), r.PathValue("id"), r.URL.Query().Get("symbol"))
		writeLocalResult(w, out, err)
	})
	// The reads of connected-account data, as the Client makes them.
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Client.Me(r.Context())
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /connected-accounts", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Client.ConnectedAccounts(r.Context())
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /connected-accounts/{id}/balances", func(w http.ResponseWriter, r *http.Request) {
		at, err := parseLocalTime(r.URL.Query().Get("at"))
		if err != nil {
			writeLocalError(w, invalidRequest("at: %v", err))
			return
		}
		out, err := s.Client.AccountBalances(r.Context(), r.PathValue("id"), at)
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /connected-accounts/{id}/positions", func(w http.ResponseWriter, r *http.Request) {
		at, err := parseLocalTime(r.URL.Query().Get("at"))
		if err != nil {
			writeLocalError(w, invalidRequest("at: %v", err))
			return
		}
		out, err := s.Client.AccountPositions(r.Context(), r.PathValue("id"), at)
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /connected-accounts/{id}/transactions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query := TransactionQuery{Kind: q.Get("kind"), Cursor: q.Get("cursor")}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				writeLocalError(w, invalidRequest("limit must be a positive integer"))
				return
			}
			query.Limit = n
		}
		var err error
		if query.From, err = parseLocalTime(q.Get("from")); err != nil {
			writeLocalError(w, invalidRequest("from: %v", err))
			return
		}
		if query.To, err = parseLocalTime(q.Get("to")); err != nil {
			writeLocalError(w, invalidRequest("to: %v", err))
			return
		}
		out, err := s.Client.AccountTransactions(r.Context(), r.PathValue("id"), query)
		writeLocalResult(w, out, err)
	})
	mux.HandleFunc("GET /connected-accounts/{id}/equity", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query := EquityQuery{Resolution: q.Get("resolution")}
		var err error
		if query.From, err = parseLocalTime(q.Get("from")); err != nil {
			writeLocalError(w, invalidRequest("from: %v", err))
			return
		}
		if query.To, err = parseLocalTime(q.Get("to")); err != nil {
			writeLocalError(w, invalidRequest("to: %v", err))
			return
		}
		out, err := s.Client.AccountEquity(r.Context(), r.PathValue("id"), query)
		writeLocalResult(w, out, err)
	})
	if token == "" {
		return mux
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			writeLocalError(w, &LocalError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "the local API needs its token as a bearer token"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// ServeLocal serves the local API at addr until ctx ends: "unix:/path" (the
// socket is made mode 0600, so only this user reaches it) or "host:port" on
// a loopback address, which needs a token because any local process could
// reach it otherwise.
func (s *Signer) ServeLocal(ctx context.Context, addr, token string) error {
	var ln net.Listener
	switch {
	case strings.HasPrefix(addr, "unix:"):
		path := strings.TrimPrefix(addr, "unix:")
		// The kernel says only "invalid argument" for a path that does not fit.
		if limit := len(syscall.RawSockaddrUnix{}.Path) - 1; len(path) > limit {
			return fmt.Errorf("signer: local API: the socket path %s is %d bytes, and a Unix socket's path takes at most %d on this system: choose a shorter one (unix:/tmp/mishmar.sock)", path, len(path), limit)
		}
		_ = os.Remove(path)
		var err error
		if ln, err = net.Listen("unix", path); err != nil {
			return fmt.Errorf("signer: local API: %w", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			ln.Close()
			return err
		}
		defer os.Remove(path)
	default:
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("signer: local API address %q: %w", addr, err)
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("signer: the local API listens on a loopback address or a Unix socket only")
		}
		if token == "" {
			return errors.New("signer: the local API on a TCP port needs a token (" + EnvLocalToken + "): any local process could reach it otherwise")
		}
		if ln, err = net.Listen("tcp", addr); err != nil {
			return fmt.Errorf("signer: local API: %w", err)
		}
	}
	srv := &http.Server{Handler: s.LocalHandler(token), ReadHeaderTimeout: 10 * time.Second}
	s.Logger.Info("signer: local API listening", "on", ln.Addr().String(), "token", token != "")
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func parseLocalTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return time.Time{}, errors.New("not RFC 3339 or YYYY-MM-DD")
	}
	return t, nil
}

func writeLocal(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeLocalResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeLocalError(w, err)
		return
	}
	writeLocal(w, http.StatusOK, v)
}

// writeLocalError renders an error the way the engine does: the engine's
// own answer as it came, a refusal of this signer as 422 refused, a bad
// request as 422 validation, anything else as 502.
func writeLocalError(w http.ResponseWriter, err error) {
	var le *LocalError
	var ee *Error
	var ref *Refusal
	switch {
	case errors.Is(err, ErrReadAgent):
		writeLocal(w, http.StatusForbidden, api.Error{Code: "forbidden", Message: err.Error()})
	case errors.As(err, &le):
		writeLocal(w, le.Status, api.Error{Code: le.Code, Message: le.Message})
	case errors.As(err, &ee):
		writeLocal(w, ee.Status, api.Error{Code: ee.Code, Message: ee.Message})
	case errors.As(err, &ref):
		writeLocal(w, http.StatusUnprocessableEntity, api.Error{Code: "refused", Message: ref.Reason()})
	case errors.Is(err, context.DeadlineExceeded):
		writeLocal(w, http.StatusGatewayTimeout, api.Error{Code: "timeout", Message: err.Error()})
	default:
		writeLocal(w, http.StatusBadGateway, api.Error{Code: "unavailable", Message: err.Error()})
	}
}
