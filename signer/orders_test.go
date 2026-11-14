package signer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

// testClock is a clock the tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recordSigner is a signer of the fixture's key and venue key, its record of
// signed orders at path ("" keeps it in memory), on the clock. Each call is a
// new process's state over the same file.
func recordSigner(t *testing.T, f *fixture, path string, clock func() time.Time) *Signer {
	t.Helper()
	s, err := New(&Config{Engine: "http://127.0.0.1:1/v1", KeyID: "key_a", Secret: agentkey.Secret("msk_abcdef", testSeed(1)),
		VenueKeys: map[string]VenueKey{"vk_1": f.policy.VenueKeys["vk_1"]}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.OrdersPath, s.Now = path, clock
	return s
}

// liveProposal is the fixture's limit buy under the nonce, proposed a second
// before at and standing for life.
func (f *fixture) liveProposal(t *testing.T, nonce string, at time.Time, life time.Duration) api.ProposalSubmit {
	t.Helper()
	return f.proposal(t, f.key, func(p *api.ProposalSubmit) {
		p.Nonce, p.ProposedAt, p.ExpiresAt = nonce, at.Add(-time.Second).UTC(), at.Add(life).UTC()
	})
}

// orderAt is the engine's request id for the proposal's order, its message
// stamped at, with a fresh engine commitment.
func (f *fixture) orderAt(t *testing.T, p api.ProposalSubmit, id string, at time.Time) (api.SignRequest, engineRound, string) {
	t.Helper()
	msg := binanceOrder(p, "msh-ag1-abc123", map[string]string{"timestamp": ms(at)})
	round, commit := f.engineRound(t)
	r := orderRequest(p, msg)
	r.Id, r.SessionId, r.Engine = id, id, commit
	r.IssuedAt, r.ExpiresAt = at, at.Add(10*time.Second)
	return r, round, msg
}

// cancelAt is the engine's request id for a cancel of the fixture's order,
// stamped at.
func (f *fixture) cancelAt(t *testing.T, p api.ProposalSubmit, id string, at time.Time) (api.SignRequest, engineRound, string) {
	t.Helper()
	msg := "origClientOrderId=msh-ag1-abc123&recvWindow=5000&symbol=BTCUSDT&timestamp=" + ms(at)
	round, commit := f.engineRound(t)
	r := cancelRequest(p, msg)
	r.Id, r.SessionId, r.Engine = id, id, commit
	r.IssuedAt, r.ExpiresAt = at, at.Add(10*time.Second)
	return r, round, msg
}

func wantRefusal(t *testing.T, what string, a api.SignerAnswer, code string) {
	t.Helper()
	if a.Kind != api.SignerAnswerKindRefusal || a.Refusal == nil || !strings.HasPrefix(a.Refusal.Reason, code+": ") {
		t.Fatalf("%s: answered %+v, want a refusal %s", what, a, code)
	}
}

// readOrders is the record's file as written.
func readOrders(t *testing.T, path string) ordersFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f ordersFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return f
}

func nonces(f ordersFile) []string {
	var out []string
	for _, o := range f.Orders {
		out = append(out, o.Nonce)
	}
	return out
}

// The record lives beside the config, so every signer that runs on a config
// file (signer run, mcp, the background signer's apps) keeps one on disk.
func TestTheRecordLivesBesideTheConfig(t *testing.T) {
	for config, want := range map[string]string{
		filepath.Join("home", "apps", "claude-code", "agent.json"): filepath.Join("home", "apps", "claude-code", "agent.signed.json"),
		filepath.Join("bots", "basis"):                             filepath.Join("bots", "basis.signed.json"),
	} {
		if got := OrdersPathFor(config); got != want {
			t.Fatalf("OrdersPathFor(%s) = %s, want %s", config, got, want)
		}
	}
	f := newFixture(t)
	s := recordSigner(t, f, "", nil)
	s.ConfigPath = filepath.Join(t.TempDir(), "agent.json")
	if got := s.orders().path; got != OrdersPathFor(s.ConfigPath) {
		t.Fatalf("the record of a signer on %s is at %q", s.ConfigPath, got)
	}
	if got := recordSigner(t, f, "", nil).orders().path; got != "" {
		t.Fatalf("a signer with no config keeps its record at %q, want in memory", got)
	}
}

// An order signed for a proposal is in the record before the share leaves
// the signer. Any later order for that proposal is refused: asked again
// under a new request id, after the window the signer's memory used to keep
// it for (a Binance message's recvWindow plus the clock difference allowed),
// and after a restart, whose new process state reads the same file. After a
// restart the same request id is still a replay, as within a session.
// Another proposal is signed.
func TestAProposalIsSignedForOnceAcrossARestart(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.signed.json")
	clock := &testClock{t: now}
	s := recordSigner(t, f, path, clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)

	r1, round1, msg1 := f.orderAt(t, p, "sr_1", clock.now())
	a := s.Handle(context.Background(), r1)
	// Handle has returned the share and nothing has sent it yet: the record
	// already holds it.
	rec := readOrders(t, path)
	if len(rec.Orders) != 1 || rec.Orders[0].KeyID != "key_a" || rec.Orders[0].Nonce != "n1" || rec.Orders[0].RequestID != "sr_1" ||
		rec.Orders[0].ConnectedAccountID != "rt_1" || !rec.Orders[0].ExpiresAt.Equal(p.ExpiresAt) || rec.Version != ordersVersion {
		t.Fatalf("the record after the first order: %+v", rec)
	}
	f.finish(t, "the first order", a, round1, []byte(msg1))
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the record's mode: %v %v", info, err)
	}

	r2, _, _ := f.orderAt(t, p, "sr_2", clock.now().Add(time.Second))
	wantRefusal(t, "a second request for the proposal", s.Handle(context.Background(), r2), RefuseOrderSigned)

	clock.add(10*time.Second + BinanceClockTolerance + time.Millisecond)
	r3, _, _ := f.orderAt(t, p, "sr_3", clock.now())
	wantRefusal(t, "after the old memory's window", s.Handle(context.Background(), r3), RefuseOrderSigned)

	// A restart: a new process's state over the same directory.
	s2 := recordSigner(t, f, path, clock.now)
	again := r1
	again.Attempt = 2
	again.IssuedAt, again.ExpiresAt = clock.now(), clock.now().Add(10*time.Second)
	again.Message = b64(binanceOrder(p, "msh-ag1-abc123", map[string]string{"timestamp": ms(clock.now())}))
	a = s2.Handle(context.Background(), again)
	wantRefusal(t, "the first request handed out again after a restart", a, RefuseReplay)
	if !strings.Contains(a.Refusal.Reason, "sr_1 was answered already") {
		t.Fatalf("the replay's reason: %s", a.Refusal.Reason)
	}
	r4, _, _ := f.orderAt(t, p, "sr_4", clock.now())
	a = s2.Handle(context.Background(), r4)
	wantRefusal(t, "a new request for the proposal after a restart", a, RefuseOrderSigned)
	if !strings.Contains(a.Refusal.Reason, "proposal n1 of key key_a") || !strings.Contains(a.Refusal.Reason, "request sr_1") {
		t.Fatalf("the refusal names what was signed: %s", a.Refusal.Reason)
	}

	q := f.liveProposal(t, "n2", clock.now(), 10*time.Minute)
	r5, round5, msg5 := f.orderAt(t, q, "sr_5", clock.now())
	f.finish(t, "another proposal after the restart", s2.Handle(context.Background(), r5), round5, []byte(msg5))
	if got := nonces(readOrders(t, path)); len(got) != 2 {
		t.Fatalf("the record holds %v", got)
	}
}

// A signer with no file (a library user's, with no ConfigPath) keeps the
// same rule in memory, for its process's life.
func TestARecordInMemoryRefusesTheSecondOrderToo(t *testing.T) {
	f := newFixture(t)
	clock := &testClock{t: now}
	s := recordSigner(t, f, "", clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
	r1, round1, msg1 := f.orderAt(t, p, "sr_1", clock.now())
	f.finish(t, "the first order", s.Handle(context.Background(), r1), round1, []byte(msg1))
	clock.add(10*time.Second + BinanceClockTolerance + time.Millisecond)
	r2, _, _ := f.orderAt(t, p, "sr_2", clock.now())
	wantRefusal(t, "a second request", s.Handle(context.Background(), r2), RefuseOrderSigned)
}

// A cancel can be signed again: pulling an order twice does no harm, and
// nothing is recorded for it.
func TestACancelIsSignedTwice(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "agent.signed.json")
	clock := &testClock{t: now}
	s := recordSigner(t, f, path, clock.now)
	pull := f.proposal(t, f.key, func(p *api.ProposalSubmit) {
		p.Kind, p.TargetClientOrderId = api.ProposalKindCancel, ptr("msh-ag1-abc123")
		p.Side, p.Size, p.Price, p.OrderType, p.ReduceOnly = "", "", nil, "", nil
		p.ProposedAt, p.ExpiresAt = clock.now().Add(-time.Second).UTC(), clock.now().Add(10*time.Minute).UTC()
	})
	for i, id := range []string{"sr_c1", "sr_c2"} {
		clock.add(time.Second)
		r, round, msg := f.cancelAt(t, pull, id, clock.now())
		f.finish(t, "cancel "+id, s.Handle(context.Background(), r), round, []byte(msg))
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("after cancel %d the record exists: %v", i+1, err)
		}
	}
}

// An entry stays until its proposal's expiresAt plus the clock difference
// allowed, and goes with the next write after that.
func TestTheRecordForgetsProposalsThatEnded(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "agent.signed.json")
	clock := &testClock{t: now}
	s := recordSigner(t, f, path, clock.now)
	short := f.liveProposal(t, "n_short", clock.now(), time.Minute)
	r, round, msg := f.orderAt(t, short, "sr_1", clock.now())
	f.finish(t, "the short proposal", s.Handle(context.Background(), r), round, []byte(msg))

	// At the very end of its grace it is kept.
	clock.t = short.ExpiresAt.Add(orderRecordGrace)
	p2 := f.liveProposal(t, "n2", clock.now(), 10*time.Minute)
	r, round, msg = f.orderAt(t, p2, "sr_2", clock.now())
	f.finish(t, "the second proposal", s.Handle(context.Background(), r), round, []byte(msg))
	if got := nonces(readOrders(t, path)); len(got) != 2 || got[0] != "n_short" || got[1] != "n2" {
		t.Fatalf("at the end of the grace the record holds %v", got)
	}
	// A moment later it goes.
	clock.add(time.Millisecond)
	p3 := f.liveProposal(t, "n3", clock.now(), 10*time.Minute)
	r, round, msg = f.orderAt(t, p3, "sr_3", clock.now())
	f.finish(t, "the third proposal", s.Handle(context.Background(), r), round, []byte(msg))
	if got := nonces(readOrders(t, path)); len(got) != 2 || got[0] != "n2" || got[1] != "n3" {
		t.Fatalf("after the grace the record holds %v", got)
	}
}

// A record that cannot be read stops every order, says why, and is left as
// it is; it never starts empty in its place. Cancels still go.
func TestACorruptRecordStopsOrders(t *testing.T) {
	f := newFixture(t)
	for name, write := range map[string]func(path string) error{
		"not JSON":        func(p string) error { return os.WriteFile(p, []byte(`{"version":1,"orders":[{"keyId":`), 0o600) },
		"another version": func(p string) error { return os.WriteFile(p, []byte(`{"version":2,"orders":[]}`), 0o600) },
		"an entry without its nonce": func(p string) error {
			return os.WriteFile(p, []byte(`{"version":1,"orders":[{"keyId":"key_a","expiresAt":"2026-10-26T13:00:00Z"}]}`), 0o600)
		},
		"a proposal twice": func(p string) error {
			e := `{"keyId":"key_a","nonce":"n9","expiresAt":"2026-10-26T13:00:00Z"}`
			return os.WriteFile(p, []byte(`{"version":1,"orders":[`+e+`,`+e+`]}`), 0o600)
		},
		"a directory": func(p string) error { return os.Mkdir(p, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.signed.json")
			if err := write(path); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			clock := &testClock{t: now}
			s := recordSigner(t, f, path, clock.now)
			var logs bytes.Buffer
			s.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			s.logOrderRecord()
			if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "no order is signed") {
				t.Fatalf("the start's log: %s", logs.String())
			}
			p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
			r, _, _ := f.orderAt(t, p, "sr_1", clock.now())
			a := s.Handle(context.Background(), r)
			wantRefusal(t, "an order", a, RefuseOrderRecord)
			if !strings.Contains(a.Refusal.Reason, path) {
				t.Fatalf("the refusal does not name the file: %s", a.Refusal.Reason)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatalf("the record was rewritten: %s", after)
			}
			pull := f.proposal(t, f.key, func(p *api.ProposalSubmit) {
				p.Kind, p.TargetClientOrderId = api.ProposalKindCancel, ptr("msh-ag1-abc123")
				p.Side, p.Size, p.Price, p.OrderType, p.ReduceOnly = "", "", nil, "", nil
				p.Nonce, p.ProposedAt, p.ExpiresAt = "n_pull", clock.now().Add(-time.Second).UTC(), clock.now().Add(10*time.Minute).UTC()
			})
			c, round, msg := f.cancelAt(t, pull, "sr_c", clock.now())
			f.finish(t, "a cancel", s.Handle(context.Background(), c), round, []byte(msg))
		})
	}
}

// A record that cannot be written refuses the order: the share is not
// released unrecorded.
func TestAnUnwritableRecordReleasesNothing(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "gone", "agent.signed.json")
	clock := &testClock{t: now}
	s := recordSigner(t, f, path, clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
	r, _, _ := f.orderAt(t, p, "sr_1", clock.now())
	a := s.Handle(context.Background(), r)
	wantRefusal(t, "an order the record cannot take", a, RefuseOrderRecord)
	if a.Share != nil || a.Commitment != nil || !strings.Contains(a.Refusal.Reason, "not released") {
		t.Fatalf("the refusal: %+v", a)
	}
}

// The honest repeats: a request the signer refused (here, its hook) is not
// in the record, so the engine's next request for that proposal is signed;
// so is the next one after a request that expired while it was being signed,
// whose share was never released.
func TestHonestRetriesAfterNothingWasReleased(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "agent.signed.json")
	clock := &testClock{t: now}
	s := recordSigner(t, f, path, clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)

	s.Config.Hook = "echo not now; exit 1"
	r1, _, _ := f.orderAt(t, p, "sr_1", clock.now())
	wantRefusal(t, "the hook's refusal", s.Handle(context.Background(), r1), RefuseHook)
	s.Config.Hook = ""
	r2, round2, msg2 := f.orderAt(t, p, "sr_2", clock.now().Add(time.Second))
	f.finish(t, "the next request after a refusal", s.Handle(context.Background(), r2), round2, []byte(msg2))

	// Real time here: the hook takes longer than the request has.
	live := recordSigner(t, f, filepath.Join(t.TempDir(), "agent.signed.json"), nil)
	start := time.Now()
	q := f.liveProposal(t, "n2", start, 10*time.Minute)
	slow, _, _ := f.orderAt(t, q, "sr_slow", start)
	slow.ExpiresAt = start.Add(200 * time.Millisecond)
	live.Config.Hook = "sleep 0.4"
	a := live.Handle(context.Background(), slow)
	wantRefusal(t, "a request that expired while it was signed", a, RefuseExpired)
	if !strings.Contains(a.Refusal.Reason, "nothing was released") {
		t.Fatalf("the refusal: %s", a.Refusal.Reason)
	}
	live.Config.Hook = ""
	next, roundNext, msgNext := f.orderAt(t, q, "sr_next", time.Now())
	f.finish(t, "the engine's next request", live.Handle(context.Background(), next), roundNext, []byte(msgNext))
}

// Two signers on one record (two processes on one config, say), asked at
// once for the same proposal under different request ids: one share in all,
// every other request refused.
func TestTwoSignersOnOneRecordSignOnce(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "agent.signed.json")
	clock := &testClock{t: now}
	signers := []*Signer{recordSigner(t, f, path, clock.now), recordSigner(t, f, path, clock.now)}
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
	const n = 8
	reqs := make([]api.SignRequest, n)
	for i := range reqs {
		reqs[i], _, _ = f.orderAt(t, p, "sr_"+string(rune('a'+i)), clock.now())
	}
	answers := make([]api.SignerAnswer, n)
	var wg sync.WaitGroup
	for i := range reqs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			answers[i] = signers[i%2].Handle(context.Background(), reqs[i])
		}(i)
	}
	wg.Wait()
	shares := 0
	for i, a := range answers {
		switch {
		case a.Kind == api.SignerAnswerKindShare:
			shares++
		case a.Refusal == nil || !strings.HasPrefix(a.Refusal.Reason, RefuseOrderSigned+": "):
			t.Fatalf("request %d: %+v", i, a)
		}
	}
	if shares != 1 {
		t.Fatalf("%d shares for one proposal", shares)
	}
	if got := readOrders(t, path); len(got.Orders) != 1 {
		t.Fatalf("the record holds %+v", got)
	}
}
