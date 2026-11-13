package signer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

// meEngine answers GET /agent/me with what me holds (raw JSON, so a test can
// leave the permission out as an older engine does, or 404 it), and the
// signer channel and key sync with nothing; it records every request.
type meEngine struct {
	mu   sync.Mutex
	me   string // the body of GET /agent/me; "" is 404
	seen []string
}

func (m *meEngine) set(me string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.me = me
}

func (m *meEngine) requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.seen...)
}

// count is how many requests were not reads of connected-account data, and
// how many were GET /agent/me.
func (m *meEngine) count() (others, me int) {
	for _, r := range m.requests() {
		method, path, _ := strings.Cut(r, " ")
		if !DataRead(method, path) {
			others++
		}
		if r == "GET /agent/me" {
			me++
		}
	}
	return others, me
}

func (m *meEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	m.mu.Lock()
	m.seen = append(m.seen, r.Method+" "+path)
	me := m.me
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case path == "/agent/me" && me != "":
		_, _ = w.Write([]byte(me))
	case path == "/agent/signer/poll":
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("[]"))
	case path == "/agent/venue-keys":
		_, _ = w.Write([]byte("[]"))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","message":"no"}`))
	}
}

const (
	readMe  = `{"agentId":"agt_1","agentName":"Reader","accountId":"acc_1","keyId":"key_1","permission":"read"}`
	tradeMe = `{"agentId":"agt_1","agentName":"Reader","accountId":"acc_1","keyId":"key_1","permission":"trade"}`
	// An engine from before AgentIdentity.permission.
	olderMe = `{"agentId":"agt_1","agentName":"Reader","accountId":"acc_1","keyId":"key_1"}`
)

// running starts Run for a signer on eng, with the key sync on and every
// interval short, and stops it when the test ends.
func running(t *testing.T, eng *meEngine) *Signer {
	t.Helper()
	srv := httptest.NewServer(eng)
	t.Cleanup(srv.Close)
	seed := testSeed(4)
	s, err := New(&Config{Engine: srv.URL + "/v1", KeyID: "key_1", Secret: agentkey.Secret("msk_abc", seed), AgentID: "agt_1", VenueKeys: map[string]VenueKey{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigPath = filepath.Join(t.TempDir(), "agent.json")
	s.PollWait, s.KeySyncInterval, s.PermissionInterval = time.Second, 10*time.Millisecond, 20*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	return s
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestARunningReadAgentCallsNothingButItsReads(t *testing.T) {
	eng := &meEngine{me: readMe}
	s := running(t, eng)
	if p, err := s.AwaitPermission(context.Background()); err != nil || p != api.AgentPermissionRead {
		t.Fatalf("permission %q %v", p, err)
	}
	time.Sleep(200 * time.Millisecond) // ten rounds of the permission check, twenty of the key sync
	if others, me := eng.count(); others != 0 || me < 5 {
		t.Fatalf("a read agent's signer sent %d calls outside its reads and asked GET /agent/me %d times: %v", others, me, eng.requests())
	}
	// Its client refuses the rest before anything is sent, a proposal too,
	// and still sends the reads.
	if _, err := s.Client.Positions(context.Background()); !errors.Is(err, ErrReadAgent) {
		t.Fatalf("positions: %v", err)
	}
	if _, err := s.Submit(context.Background(), ProposeRequest{ConnectedAccountID: "ta_1", Symbol: "BTC", Side: "buy", Size: "1", OrderType: "market"}); !errors.Is(err, ErrReadAgent) {
		t.Fatalf("a proposal: %v", err)
	}
	if _, err := s.Client.AccountBalances(context.Background(), "ta_1", time.Time{}); errors.Is(err, ErrReadAgent) {
		t.Fatalf("a read was refused: %v", err)
	}
	sent := false
	for _, r := range eng.requests() {
		sent = sent || r == "GET /agent/connected-accounts/ta_1/balances"
	}
	if others, _ := eng.count(); others != 0 || !sent {
		t.Fatalf("after the refusals and a read: %v", eng.requests())
	}

	// The local API, as an SDK's Signer reaches it, refuses the same.
	local := httptest.NewServer(s.LocalHandler(""))
	defer local.Close()
	for _, c := range []struct{ method, path string }{{http.MethodPost, "/propose"}, {http.MethodGet, "/positions"}, {http.MethodGet, "/decisions"}} {
		req, _ := http.NewRequest(c.method, local.URL+c.path, strings.NewReader(`{"connectedAccountId":"ta_1","symbol":"BTC","side":"buy","size":"1","orderType":"market"}`))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("local %s %s: %d", c.method, c.path, res.StatusCode)
		}
	}
	if others, _ := eng.count(); others != 0 {
		t.Fatalf("the local API sent %v", eng.requests())
	}
}

// An agent made a trade agent starts its signer channel, and one made a read
// agent again stops it, without a restart: GET /agent/me is asked again on a timer.
func TestRunFollowsAChangeOfPermission(t *testing.T) {
	eng := &meEngine{me: readMe}
	s := running(t, eng)
	eventually(t, "read", func() bool { p, _ := s.Permission(); return p == api.AgentPermissionRead })

	eng.set(tradeMe)
	eventually(t, "the channel starts", func() bool { others, _ := eng.count(); return others > 0 })
	if s.Client.ReadOnly() {
		t.Fatal("a trade agent's client is read-only")
	}
	polls := func() int {
		n := 0
		for _, r := range eng.requests() {
			if r == "POST /agent/signer/poll" {
				n++
			}
		}
		return n
	}
	eventually(t, "it polls", func() bool { return polls() > 2 })

	eng.set(readMe)
	eventually(t, "read again", func() bool { return s.Client.ReadOnly() })
	time.Sleep(50 * time.Millisecond) // a poll in flight ends
	before, _ := eng.count()
	time.Sleep(200 * time.Millisecond)
	if after, _ := eng.count(); after != before {
		t.Fatalf("still calling outside the reads after becoming a read agent: %d then %d: %v", before, after, eng.requests()[len(eng.requests())-5:])
	}
}

// An engine from before the field (or before GET /agent/me) says nothing
// about permission: its agents all trade, so the signer runs as it always
// has, and does not ask again.
func TestAnOlderEngineIsTakenAsTrade(t *testing.T) {
	for name, me := range map[string]string{"no permission field": olderMe, "no GET /agent/me": ""} {
		t.Run(name, func(t *testing.T) {
			eng := &meEngine{me: me}
			s := running(t, eng)
			if p, err := s.AwaitPermission(context.Background()); err != nil || p != api.AgentPermissionTrade {
				t.Fatalf("permission %q %v", p, err)
			}
			eventually(t, "it polls", func() bool {
				for _, r := range eng.requests() {
					if r == "POST /agent/signer/poll" {
						return true
					}
				}
				return false
			})
			time.Sleep(100 * time.Millisecond)
			if _, asked := eng.count(); asked != 1 || s.Client.ReadOnly() {
				t.Fatalf("GET /agent/me asked %d times, read-only %v", asked, s.Client.ReadOnly())
			}
		})
	}
}

// A key of scope read (contract 0.21.0, AgentIdentity.keyScope) starts no
// signer channel and no key sync, even on a trade agent: the engine refuses
// it the channel (403 key_scope), so it calls only its reads, as a read
// agent's signer does. A trade key on a trade agent runs the channel; a
// scope this signer does not know is taken as read.
func TestAReadKeyStartsNoSignerChannel(t *testing.T) {
	const (
		readKeyOnTradeAgent  = `{"agentId":"agt_1","agentName":"App","accountId":"acc_1","keyId":"key_1","permission":"trade","keyScope":"read"}`
		tradeKeyOnTradeAgent = `{"agentId":"agt_1","agentName":"App","accountId":"acc_1","keyId":"key_1","permission":"trade","keyScope":"trade","keyExpiresAt":"2027-05-12T10:00:00Z"}`
		tradeKeyOnReadAgent  = `{"agentId":"agt_1","agentName":"App","accountId":"acc_1","keyId":"key_1","permission":"read","keyScope":"trade"}`
		unknownScope         = `{"agentId":"agt_1","agentName":"App","accountId":"acc_1","keyId":"key_1","permission":"trade","keyScope":"admin"}`
	)
	for name, me := range map[string]string{"read key, trade agent": readKeyOnTradeAgent, "trade key, read agent": tradeKeyOnReadAgent, "unknown scope": unknownScope} {
		t.Run(name, func(t *testing.T) {
			eng := &meEngine{me: me}
			s := running(t, eng)
			if p, err := s.AwaitPermission(context.Background()); err != nil || p != api.AgentPermissionRead {
				t.Fatalf("permission %q %v", p, err)
			}
			time.Sleep(150 * time.Millisecond)
			if others, asked := eng.count(); others != 0 || asked < 2 || !s.Client.ReadOnly() {
				t.Fatalf("a read key's signer sent %d calls outside its reads (asked GET /agent/me %d times, read-only %v): %v", others, asked, s.Client.ReadOnly(), eng.requests())
			}
		})
	}
	// The same key made a trade key starts the channel without a restart.
	eng := &meEngine{me: readKeyOnTradeAgent}
	s := running(t, eng)
	eventually(t, "read", func() bool { p, _ := s.Permission(); return p == api.AgentPermissionRead })
	eng.set(tradeKeyOnTradeAgent)
	eventually(t, "the channel starts", func() bool { others, _ := eng.count(); return others > 0 })
	if s.Client.ReadOnly() {
		t.Fatal("a trade key's client is read-only")
	}
}

func TestAwaitPermissionSaysWhyItIsNotKnown(t *testing.T) {
	seed := testSeed(5)
	s, err := New(&Config{Engine: "http://127.0.0.1:1/v1", KeyID: "key_1", Secret: agentkey.Secret("msk_abc", seed), AgentID: "agt_1", VenueKeys: map[string]VenueKey{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	wait, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stop()
	if _, err := s.AwaitPermission(wait); err == nil || !strings.Contains(err.Error(), "GET /agent/me") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("unknown: %v", err)
	}
	if _, known := s.Permission(); known {
		t.Fatal("known without an answer")
	}
}

func TestDataRead(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/agent/me", true}, {"GET", "/agent/connected-accounts", true}, {"GET", "/agent/connected-accounts/ta_1", true},
		{"GET", "/agent/connected-accounts/ta_1/balances", true}, {"GET", "/agent/connected-accounts/ta_1/positions", true},
		{"GET", "/agent/connected-accounts/ta_1/transactions", true}, {"GET", "/agent/connected-accounts/ta_1/equity", true},
		{"GET", "/agent/connected-accounts/ta_1/venue-rules", false}, {"GET", "/agent/connected-accounts/", false},
		{"GET", "/agent/connected-accounts/ta_1/balances/x", false}, {"POST", "/agent/me", false}, {"GET", "/agent/venue-keys", false},
		{"GET", "/agent/positions", false}, {"POST", "/agent/signer/poll", false}, {"POST", "/proposals", false}, {"GET", "/agent/decisions", false},
	} {
		if got := DataRead(c.method, c.path); got != c.want {
			t.Errorf("DataRead(%s %s) = %v", c.method, c.path, got)
		}
	}
}
