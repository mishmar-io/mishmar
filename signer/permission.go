package signer

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/api"
)

// An agent reads, or reads and trades (api.AgentPermission). A read agent
// has a Mishmar API key, no trading key and no use for a signer channel, and
// on hosted Mishmar every call it makes must be a read of connected-account
// data (DataRead): a collector answers those while the account's engine
// sleeps, and anything else would wake it, or keep it awake. So Run asks GET
// /agent/me (itself one of those reads) before anything else, and for a read
// agent polls nothing, syncs no keys and lets its Client send nothing but
// those reads. It asks again every PermissionInterval, so an agent made a
// trade agent starts its signer channel, and one made a read agent stops it,
// without a restart.

// DefaultPermissionInterval is how often a running signer asks GET /agent/me
// again whether its agent reads or trades.
const DefaultPermissionInterval = time.Minute

// ErrReadAgent is a Client's refusal, before anything is sent, of a call a
// read agent does not make: anything but the reads of connected-account data.
var ErrReadAgent = errors.New("signer: the agent is a read agent: it reads the connected accounts it was given and calls nothing else")

// DataRead says whether a request (the method and the path without /v1) is
// one of the reads of connected-account data, the calls a hosted account's
// collector answers while its engine sleeps (engine.AgentDataRead in the
// engine): GET /agent/me, /agent/connected-accounts,
// /agent/connected-accounts/{id} and its /balances, /positions,
// /transactions and /equity.
func DataRead(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/agent/me", "/agent/connected-accounts":
		return true
	}
	rest, ok := strings.CutPrefix(path, "/agent/connected-accounts/")
	if !ok || rest == "" {
		return false
	}
	id, sub, nested := strings.Cut(rest, "/")
	if id == "" {
		return false
	}
	if !nested {
		return true
	}
	switch sub {
	case "balances", "positions", "transactions", "equity":
		return true
	}
	return false
}

// permissionState is what Run has learnt of the agent's permission.
type permissionState struct {
	mu      sync.Mutex
	perm    api.AgentPermission // "" until GET /agent/me has answered
	known   chan struct{}       // closed once it has
	lastErr error               // the last failed ask, while nothing is known
}

func (s *Signer) permissionState() *permissionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perm == nil {
		s.perm = &permissionState{known: make(chan struct{})}
	}
	return s.perm
}

// Permission is whether the agent reads or trades as GET /agent/me last said
// (an engine that does not say is taken as trade), and whether Run has
// learnt it yet.
func (s *Signer) Permission() (api.AgentPermission, bool) {
	st := s.permissionState()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.perm, st.perm != ""
}

// AwaitPermission waits until Run has learnt whether the agent reads or
// trades, or until ctx ends; the error then says what the last ask got.
func (s *Signer) AwaitPermission(ctx context.Context) (api.AgentPermission, error) {
	st := s.permissionState()
	select {
	case <-st.known:
		p, _ := s.Permission()
		return p, nil
	case <-ctx.Done():
		st.mu.Lock()
		last := st.lastErr
		st.mu.Unlock()
		if last != nil {
			return "", errors.Join(errors.New("signer: GET /agent/me has not said whether the agent reads or trades"), last)
		}
		return "", errors.New("signer: GET /agent/me has not said yet whether the agent reads or trades")
	}
}

// setPermission records what the agent may do, and makes the Client send
// only reads of connected-account data for a read agent.
func (s *Signer) setPermission(p api.AgentPermission) {
	st := s.permissionState()
	st.mu.Lock()
	defer st.mu.Unlock()
	first := st.perm == ""
	st.perm, st.lastErr = p, nil
	s.Client.SetReadOnly(p == api.AgentPermissionRead)
	if first {
		close(st.known)
	}
}

// permissionOf is what an answer of GET /agent/me says the agent may do: an
// engine from before the field sends none, which is trade (its agents all
// were); a value this signer does not know is taken as read, which calls
// the least.
func permissionOf(me api.AgentIdentity) api.AgentPermission {
	switch me.Permission {
	case "", api.AgentPermissionTrade:
		return api.AgentPermissionTrade
	}
	return api.AgentPermissionRead
}

// learnPermission asks GET /agent/me until it answers, or ctx ends, and
// records the answer. follow says whether the engine reports the
// permission at all: one from before it (no /agent/me, or no field) never
// will, so there is nothing to ask again.
func (s *Signer) learnPermission(ctx context.Context) (perm api.AgentPermission, follow bool) {
	backoff := minBackoff
	for {
		me, err := s.Client.Me(ctx)
		var refused *Error
		switch {
		case err == nil:
			perm = permissionOf(me)
			s.setPermission(perm)
			return perm, me.Permission != ""
		case errors.As(err, &refused) && refused.Status == http.StatusNotFound:
			// An engine from before 0.19.0 has no GET /agent/me: trade, as before.
			s.setPermission(api.AgentPermissionTrade)
			return api.AgentPermissionTrade, false
		}
		if ctx.Err() != nil {
			return "", false
		}
		st := s.permissionState()
		st.mu.Lock()
		st.lastErr = err
		st.mu.Unlock()
		s.Logger.Warn("signer: asking the engine whether the agent reads or trades (GET /agent/me) failed", "err", err, "retryIn", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return "", false
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// recheckPermission asks GET /agent/me once more; a failed ask keeps what
// was known.
func (s *Signer) recheckPermission(ctx context.Context, cur api.AgentPermission) api.AgentPermission {
	me, err := s.Client.Me(ctx)
	if err != nil || me.Permission == "" {
		if err != nil && ctx.Err() == nil {
			s.Logger.Debug("signer: asking again whether the agent reads or trades failed; keeping what was known", "err", err, "permission", cur)
		}
		return cur
	}
	if p := permissionOf(me); p != cur {
		s.setPermission(p)
		return p
	}
	return cur
}

func (s *Signer) permissionInterval() time.Duration {
	if s.PermissionInterval > 0 {
		return s.PermissionInterval
	}
	return DefaultPermissionInterval
}

// Run learns whether the agent reads or trades, then runs the signer
// channel while it trades (runChannel: the poll loop and, with ConfigPath,
// the key sync) and nothing but a slow GET /agent/me while it reads, until
// ctx ends.
func (s *Signer) Run(ctx context.Context) error {
	perm, follow := s.learnPermission(ctx)
	if ctx.Err() != nil {
		return nil
	}
	interval := s.permissionInterval()
	for {
		if perm == api.AgentPermissionRead {
			s.Logger.Info("signer: a read agent: no signer channel and no key sync; it reads the connected accounts it was given", "agent", s.Config.AgentID, "askAgainEvery", interval)
			for perm == api.AgentPermissionRead {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(interval):
				}
				perm = s.recheckPermission(ctx, perm)
			}
			s.Logger.Info("signer: the agent may trade now; starting the signer channel", "agent", s.Config.AgentID)
		}
		channel, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = s.runChannel(channel)
		}()
		if !follow {
			// The engine does not say what an agent may do: trade, for good.
			<-done
			stop()
			return nil
		}
		for perm != api.AgentPermissionRead {
			select {
			case <-ctx.Done():
				stop()
				<-done
				return nil
			case <-time.After(interval):
			}
			perm = s.recheckPermission(ctx, perm)
		}
		s.Logger.Info("signer: the agent is a read agent now; stopping the signer channel and the key sync", "agent", s.Config.AgentID)
		stop()
		<-done
	}
}
