package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/ecdsa2p"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/signerapi"
	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

// The service holds at most 8192 unused presigned slots and FROST
// commitments for one key, counted together with the nonces its open
// presigning sessions are making. These tests fill a key up to that ceiling
// and check what is refused, what is stored, and what frees a place again.

// clock is the service's clock in these tests. The harness's own clock is a
// fixed time the tests cannot move, so a test that needs time to pass puts
// this one in its place before its first request and moves it.
type clock struct{ at atomic.Pointer[time.Time] }

func (c *clock) now() time.Time  { return *c.at.Load() }
func (c *clock) set(t time.Time) { c.at.Store(&t) }

func (h *harness) movableClock() *clock {
	c := &clock{}
	c.set(h.now)
	h.svc.opts.Now = c.now
	return c
}

// frostKey imports a FROST key for keyID and returns the agent's share.
func (h *harness) frostKey(keyID string) *frost.Share {
	h.t.Helper()
	agentShare, engineShare, err := frost.Deal()
	if err != nil {
		h.t.Fatal(err)
	}
	engineJSON, _ := json.Marshal(engineShare)
	imp, err := h.client.ImportKeyWithResponse(context.Background(), keyID, signerapi.KeyImport{Scheme: signerapi.SchemeFrostEd25519, Share: string(engineJSON)})
	if err != nil || imp.JSON201 == nil {
		h.t.Fatalf("import %s: %v %s", keyID, err, imp.Body)
	}
	return agentShare
}

// issue asks for n commitments for the key.
func (h *harness) issue(keyID string, n int) *signerapi.IssueCommitmentsResponse {
	h.t.Helper()
	r, err := h.client.IssueCommitmentsWithResponse(context.Background(), keyID, signerapi.IssueCommitmentsJSONRequestBody{Count: n})
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

// issued asks for n commitments and fails the test unless all n come back.
func (h *harness) issued(keyID string, n int) []signerapi.Commitment {
	h.t.Helper()
	r := h.issue(keyID, n)
	if r.JSON200 == nil || len(*r.JSON200) != n {
		h.t.Fatalf("%d commitments for %s: %d %s", n, keyID, r.StatusCode(), r.Body)
	}
	return *r.JSON200
}

// unusedNonces is what GetKey says the key holds unused.
func (h *harness) unusedNonces(keyID string) int {
	h.t.Helper()
	k, err := h.client.GetKeyWithResponse(context.Background(), keyID)
	if err != nil || k.JSON200 == nil || k.JSON200.UnusedNonces == nil {
		h.t.Fatalf("get key %s: %v %s", keyID, err, k.Body)
	}
	return *k.JSON200.UnusedNonces
}

// standIns puts n unused slots for the key straight into the service, good
// for NonceTTL from the service's clock. They carry no presig and are never
// finished; they only take places, as real presigning would, without its cost.
func (h *harness) standIns(keyID string, n int) {
	h.svc.mu.Lock()
	defer h.svc.mu.Unlock()
	expires := h.svc.opts.Now().Add(NonceTTL)
	for i := range n {
		h.svc.slots[fmt.Sprintf("ps_standin_%s_%d", keyID, i)] = &slot{keyID: keyID, expires: expires}
	}
}

// wantCeiling fails the test unless the answer is a 409 conflict that names
// the key, what it holds and the ceiling.
func wantCeiling(t *testing.T, name string, status int, body *signerapi.Error, raw []byte, keyID string, held int) {
	t.Helper()
	if status != http.StatusConflict || body == nil || body.Code != signerapi.ErrorCodeConflict {
		t.Fatalf("%s: answered %d %s, want 409 conflict", name, status, raw)
	}
	for _, want := range []string{"key " + keyID + " ", "holds " + strconv.Itoa(held) + " unused", "the 8192 the service keeps"} {
		if !strings.Contains(body.Message, want) {
			t.Fatalf("%s: the message %q does not say %q", name, body.Message, want)
		}
	}
}

// presignRun is one presigning session, the agent's side and the service's.
type presignRun struct {
	h       *harness
	keyID   string
	session string
	agent   *ecdsa2p.Presign
	first   *signerapi.PresignStepResponse
}

// presignStep1 sends step 1 for n nonces. The answer is in run.first; the
// caller checks it.
func (h *harness) presignStep1(keyID, session string, key *ecdsa2p.Key, n int) *presignRun {
	h.t.Helper()
	ah, _ := ecdsa2p.NewSIDHalf()
	eh, _ := ecdsa2p.NewSIDHalf()
	sid, _ := ecdsa2p.JointSID(ah, eh)
	agent, err := key.NewPresign(n, sid)
	if err != nil {
		h.t.Fatal(err)
	}
	r1, err := h.client.PresignStepWithResponse(context.Background(), keyID, signerapi.PresignStep{Step: signerapi.PresignStepStepN1, SessionId: session, Count: &n, Sid: ptr(b64(sid))})
	if err != nil {
		h.t.Fatal(err)
	}
	return &presignRun{h: h, keyID: keyID, session: session, agent: agent, first: r1}
}

// accepted fails the test unless step 1 was taken.
func (p *presignRun) accepted(name string) *presignRun {
	p.h.t.Helper()
	if p.first.JSON200 == nil {
		p.h.t.Fatalf("%s: step 1 answered %d %s", name, p.first.StatusCode(), p.first.Body)
	}
	return p
}

// reply is the agent's answer to step 1.
func (p *presignRun) reply() []byte {
	p.h.t.Helper()
	m2, err := p.agent.Reply(decode(p.h.t, p.first.JSON200.Message))
	if err != nil {
		p.h.t.Fatal(err)
	}
	return m2
}

// step2 sends step 2 with the agent's message.
func (p *presignRun) step2(m2 []byte) *signerapi.PresignStepResponse {
	p.h.t.Helper()
	r2, err := p.h.client.PresignStepWithResponse(context.Background(), p.keyID, signerapi.PresignStep{Step: signerapi.PresignStepStepN2, SessionId: p.session, AgentMessage: ptr(b64(m2))})
	if err != nil {
		p.h.t.Fatal(err)
	}
	return r2
}

// finished checks a step 2 answer and returns the agent's presig and the slots.
func (p *presignRun) finished(name string, r2 *signerapi.PresignStepResponse, n int) (*ecdsa2p.Presig, []signerapi.Slot) {
	p.h.t.Helper()
	if r2.JSON200 == nil || r2.JSON200.Slots == nil || len(*r2.JSON200.Slots) != n {
		p.h.t.Fatalf("%s: step 2 answered %d %s, want %d slots", name, r2.StatusCode(), r2.Body, n)
	}
	presig, err := p.agent.Finish(decode(p.h.t, r2.JSON200.Message))
	if err != nil {
		p.h.t.Fatal(err)
	}
	return presig, *r2.JSON200.Slots
}

// wantStep1Ceiling fails the test unless step 1 was refused at the ceiling
// and left no session behind.
func (p *presignRun) wantStep1Ceiling(name string, held int) {
	p.h.t.Helper()
	wantCeiling(p.h.t, name, p.first.StatusCode(), p.first.JSONDefault, p.first.Body, p.keyID, held)
	p.h.svc.mu.Lock()
	_, kept := p.h.svc.presigns[p.session]
	p.h.svc.mu.Unlock()
	if kept {
		p.h.t.Fatalf("%s: the refused step 1 left session %s open", name, p.session)
	}
}

func TestMaxUnusedIs8192(t *testing.T) {
	if MaxUnused != 8192 {
		t.Fatalf("MaxUnused is %d, want 8192", MaxUnused)
	}
}

// Eight issues of 1024 fill a FROST key; one more commitment is refused and
// nothing is stored for it.
func TestUnusedCommitmentsStopAtTheCeiling(t *testing.T) {
	h := newHarness(t)
	h.frostKey("vk_bn")
	for range 8 {
		h.issued("vk_bn", 1024)
	}
	if n := h.unusedNonces("vk_bn"); n != 8192 {
		t.Fatalf("unused nonces %d, want 8192", n)
	}
	r := h.issue("vk_bn", 1)
	wantCeiling(t, "one more commitment", r.StatusCode(), r.JSONDefault, r.Body, "vk_bn", 8192)
	if n := h.unusedNonces("vk_bn"); n != 8192 {
		t.Fatalf("after the refusal: unused nonces %d, want 8192", n)
	}
}

// At 8182 held, 11 more would pass the ceiling and 10 reach it.
func TestUnusedCommitmentsAtTheEdge(t *testing.T) {
	h := newHarness(t)
	h.frostKey("vk_bn")
	for range 7 {
		h.issued("vk_bn", 1024)
	}
	h.issued("vk_bn", 1014)
	if n := h.unusedNonces("vk_bn"); n != 8182 {
		t.Fatalf("unused nonces %d, want 8182", n)
	}
	r := h.issue("vk_bn", 11)
	wantCeiling(t, "11 more at 8182", r.StatusCode(), r.JSONDefault, r.Body, "vk_bn", 8182)
	if n := h.unusedNonces("vk_bn"); n != 8182 {
		t.Fatalf("after the refusal: unused nonces %d, want 8182", n)
	}
	h.issued("vk_bn", 10)
	if n := h.unusedNonces("vk_bn"); n != 8192 {
		t.Fatalf("unused nonces %d, want 8192", n)
	}
}

// A commitment used for a signature frees its place.
func TestUnusedCommitmentsFreeAPlaceWhenOneSigns(t *testing.T) {
	h := newHarness(t)
	agentShare := h.frostKey("vk_bn")
	var first signerapi.Commitment
	for i := range 8 {
		cs := h.issued("vk_bn", 1024)
		if i == 0 {
			first = cs[0]
		}
	}
	if r := h.issue("vk_bn", 1); r.StatusCode() != http.StatusConflict {
		t.Fatalf("at 8192 one more answered %d %s", r.StatusCode(), r.Body)
	}

	// A read for verify, signed with one commitment, as TestFrost does.
	read := []byte("recvWindow=10000&timestamp=" + strconv.FormatInt(h.now.UnixMilli(), 10))
	party, _ := agentShare.Party()
	ac := party.Commit()
	ec, err := frost.ParseCommitment(decode(t, first.Commitment))
	if err != nil {
		t.Fatal(err)
	}
	as, err := party.Sign(read, ec, ac)
	if err != nil {
		t.Fatal(err)
	}
	verify := signerapi.FrostSignRequestPurposeVerify
	sr, err := h.client.FrostSignWithResponse(context.Background(), "vk_bn", signerapi.FrostSignRequest{
		CommitmentId: first.Id, AgentCommitment: b64(ac.Bytes()), AgentShare: b64(as.Bytes()), Purpose: &verify,
		VenueAction: signerapi.VenueAction{Venue: signerapi.VenueActionVenueBinance, Action: b64(read)},
	})
	if err != nil || sr.JSON200 == nil {
		t.Fatalf("frost sign: %v %s", err, sr.Body)
	}
	if n := h.unusedNonces("vk_bn"); n != 8191 {
		t.Fatalf("after a signature: unused nonces %d, want 8191", n)
	}
	h.issued("vk_bn", 1)
	r := h.issue("vk_bn", 1)
	wantCeiling(t, "the next one after the freed place is taken", r.StatusCode(), r.JSONDefault, r.Body, "vk_bn", 8192)
}

// Commitments past NonceTTL free their places.
func TestUnusedCommitmentsFreeTheirPlacesWhenTheyExpire(t *testing.T) {
	h := newHarness(t)
	c := h.movableClock()
	h.frostKey("vk_bn")
	for range 8 {
		h.issued("vk_bn", 1024)
	}
	if r := h.issue("vk_bn", 1); r.StatusCode() != http.StatusConflict {
		t.Fatalf("at 8192 one more answered %d %s", r.StatusCode(), r.Body)
	}
	c.set(h.now.Add(24*time.Hour + time.Second))
	h.issued("vk_bn", 1024)
	if n := h.unusedNonces("vk_bn"); n != 1024 {
		t.Fatalf("after expiry: unused nonces %d, want 1024", n)
	}
}

// With 8188 slots held, step 1 for 5 is refused and leaves no session; step
// 1 for 4 is taken.
func TestPresignStepOneStopsAtTheCeiling(t *testing.T) {
	h := newHarness(t)
	key := h.keygen("vk_hl", "ks_1")
	h.standIns("vk_hl", 8188)
	h.presignStep1("vk_hl", "ps_5", key, 5).wantStep1Ceiling("step 1 for 5 at 8188", 8188)
	h.presignStep1("vk_hl", "ps_4", key, 4).accepted("step 1 for 4 at 8188")
}

// ecdsaAtTheCeiling brings an ECDSA key to 8188 stand-in slots and an open
// session for 4, checks that the open session's nonces are counted, finishes
// it, and checks the key is then full with its 4 real slots.
func ecdsaAtTheCeiling(t *testing.T) (*harness, *ecdsa2p.Key, *ecdsa2p.Presig, []signerapi.Slot) {
	t.Helper()
	h := newHarness(t)
	key := h.keygen("vk_hl", "ks_1")
	h.standIns("vk_hl", 8188)
	open := h.presignStep1("vk_hl", "ps_1", key, 4).accepted("step 1 for 4 at 8188")
	h.presignStep1("vk_hl", "ps_2", key, 1).wantStep1Ceiling("step 1 for 1 while a session for 4 is open", 8192)
	presig, slots := open.finished("the open session's step 2", open.step2(open.reply()), 4)
	if n := h.unusedNonces("vk_hl"); n != 8192 {
		t.Fatalf("unused nonces %d, want 8192", n)
	}
	h.presignStep1("vk_hl", "ps_3", key, 1).wantStep1Ceiling("step 1 for 1 at 8192", 8192)
	return h, key, presig, slots
}

// An open session's nonces are counted from step 1 on.
func TestPresignOpenSessionIsCounted(t *testing.T) {
	ecdsaAtTheCeiling(t)
}

// A slot used to sign frees its place.
func TestPresignSlotUsedFreesAPlace(t *testing.T) {
	h, key, presig, slots := ecdsaAtTheCeiling(t)
	msg, digest, proposal := h.hlOrder(hyperliquid.ClientOrderID("agt_test"))
	m4, err := key.Complete(presig, 0, digest)
	if err != nil {
		t.Fatal(err)
	}
	if r := h.hlFinish("vk_hl", slots[0], m4, msg, h.decision(proposal), signerapi.FinishRequestPurposeOrder); r.JSON200 == nil {
		t.Fatalf("finish: %s", r.Body)
	}
	if n := h.unusedNonces("vk_hl"); n != 8191 {
		t.Fatalf("after a signature: unused nonces %d, want 8191", n)
	}
	h.presignStep1("vk_hl", "ps_4", key, 1).accepted("step 1 for 1 after a slot was used")
}

// A session that never takes its step 2 is dropped after SessionTTL (by the
// sweep at the start of the next presign step) and its places are free.
func TestPresignUnfinishedSessionGivesItsPlacesBack(t *testing.T) {
	h := newHarness(t)
	c := h.movableClock()
	key := h.keygen("vk_hl", "ks_1")
	h.standIns("vk_hl", 8188)
	h.presignStep1("vk_hl", "ps_1", key, 4).accepted("step 1 for 4 at 8188")
	h.presignStep1("vk_hl", "ps_2", key, 4).wantStep1Ceiling("a second session for 4 while the first is open", 8192)
	c.set(h.now.Add(time.Minute + time.Second))
	h.presignStep1("vk_hl", "ps_3", key, 4).accepted("a new session for 4 after the first one's time")
}

// One key at its ceiling does not hold back another key of the same service.
func TestUnusedCeilingIsPerKey(t *testing.T) {
	t.Run("FROST", func(t *testing.T) {
		h := newHarness(t)
		h.frostKey("vk_bn_a")
		h.frostKey("vk_bn_b")
		for range 8 {
			h.issued("vk_bn_a", 1024)
		}
		r := h.issue("vk_bn_a", 1)
		wantCeiling(t, "the full key", r.StatusCode(), r.JSONDefault, r.Body, "vk_bn_a", 8192)
		h.issued("vk_bn_b", 1024)
		if n := h.unusedNonces("vk_bn_b"); n != 1024 {
			t.Fatalf("the other key: unused nonces %d, want 1024", n)
		}
	})
	t.Run("ECDSA", func(t *testing.T) {
		h := newHarness(t)
		keyA := h.keygen("vk_hl_a", "ks_a")
		keyB := h.keygen("vk_hl_b", "ks_b")
		h.standIns("vk_hl_a", 8192)
		h.presignStep1("vk_hl_a", "ps_a", keyA, 1).wantStep1Ceiling("the full key", 8192)
		h.presignStep1("vk_hl_b", "ps_b", keyB, 1).accepted("the other key")
	})
}

// Unchanged behaviour: a session whose step 2 is done takes no second one.
func TestPresignSecondStepTwoIsNotFound(t *testing.T) {
	h := newHarness(t)
	key := h.keygen("vk_hl", "ks_1")
	run := h.presignStep1("vk_hl", "ps_1", key, 1).accepted("step 1")
	m2 := run.reply()
	run.finished("step 2", run.step2(m2), 1)
	again := run.step2(m2)
	if again.StatusCode() != http.StatusNotFound || again.JSONDefault == nil || again.JSONDefault.Code != signerapi.ErrorCodeNotFound {
		t.Fatalf("a second step 2 answered %d %s, want 404 not_found", again.StatusCode(), again.Body)
	}
	if n := h.unusedNonces("vk_hl"); n != 1 {
		t.Fatalf("unused nonces %d, want 1", n)
	}
}

// A step 2 that names another key's session finds none and leaves that
// session as it was: its own key's step 2 still finishes it, and its nonces
// stay counted meanwhile.
func TestPresignStepTwoUnderAnotherKeyLeavesTheSessionAlone(t *testing.T) {
	h := newHarness(t)
	keyA := h.keygen("vk_hl_a", "ks_a")
	h.keygen("vk_hl_b", "ks_b")
	run := h.presignStep1("vk_hl_a", "ps_1", keyA, 2).accepted("step 1")
	m2 := run.reply()

	other := &presignRun{h: h, keyID: "vk_hl_b", session: "ps_1"}
	r := other.step2(m2)
	if r.StatusCode() != http.StatusNotFound || r.JSONDefault == nil || r.JSONDefault.Code != signerapi.ErrorCodeNotFound {
		t.Fatalf("a step 2 under another key answered %d %s, want 404 not_found", r.StatusCode(), r.Body)
	}
	if p, ok := h.openSession("ps_1"); !ok || p.finishing || p.keyID != "vk_hl_a" || p.count != 2 {
		t.Fatalf("the other key's step 2 changed the session: %v %+v", ok, p)
	}

	run.finished("the session's own step 2", run.step2(m2), 2)
	if n := h.unusedNonces("vk_hl_a"); n != 2 {
		t.Fatalf("the session's key: unused nonces %d, want 2", n)
	}
	if n := h.unusedNonces("vk_hl_b"); n != 0 {
		t.Fatalf("the other key: unused nonces %d, want 0", n)
	}
}

// openSession is the service's record of an open session, read under its lock.
func (h *harness) openSession(id string) (*presignSession, bool) {
	h.svc.mu.Lock()
	defer h.svc.mu.Unlock()
	p, ok := h.svc.presigns[id]
	return p, ok
}

// A session whose step 2 is under way takes no other step 2, and the sweep
// does not drop it however old it is. The step 2 under way is stood in for
// by marking the session finishing, as step 2 does; once the mark is taken
// away the session's own run still finishes, so neither the refused step 2
// nor the sweep abandoned it.
func TestPresignSessionInStepTwoTakesNoOtherAndIsNotSwept(t *testing.T) {
	h := newHarness(t)
	c := h.movableClock()
	key := h.keygen("vk_hl", "ks_1")
	run := h.presignStep1("vk_hl", "ps_1", key, 2).accepted("step 1")
	m2 := run.reply()
	ses, ok := h.openSession("ps_1")
	if !ok {
		t.Fatal("step 1 left no session")
	}
	h.svc.mu.Lock()
	ses.finishing = true
	h.svc.mu.Unlock()

	again := run.step2(m2)
	if again.StatusCode() != http.StatusNotFound || again.JSONDefault == nil || again.JSONDefault.Code != signerapi.ErrorCodeNotFound {
		t.Fatalf("a step 2 for a session in its step 2 answered %d %s, want 404 not_found", again.StatusCode(), again.Body)
	}
	if p, ok := h.openSession("ps_1"); !ok || p != ses || !p.finishing {
		t.Fatalf("the refused step 2 changed the session in its step 2: %v %+v", ok, p)
	}

	// Past SessionTTL, a presign step sweeps; the session in its step 2 stays,
	// and its 2 nonces are still counted.
	c.set(h.now.Add(time.Minute + time.Second))
	h.standIns("vk_hl", 8190)
	h.presignStep1("vk_hl", "ps_2", key, 1).wantStep1Ceiling("step 1 for 1 past the session's time", 8192)
	if _, ok := h.openSession("ps_1"); !ok {
		t.Fatal("the sweep dropped a session in its step 2")
	}

	// Its own step 2 goes on: back to its time, mark taken away, step 2 again.
	c.set(h.now)
	h.svc.mu.Lock()
	ses.finishing = false
	h.svc.mu.Unlock()
	run.finished("the session's own step 2", run.step2(m2), 2)
}

// A key forgotten while a session's step 2 is under way lets go of the
// session, and leaves its run to that step 2: the run still finishes.
func TestPresignSessionInStepTwoIsNotAbandonedWhenTheKeyIsForgotten(t *testing.T) {
	h := newHarness(t)
	key := h.keygen("vk_hl", "ks_1")
	run := h.presignStep1("vk_hl", "ps_1", key, 1).accepted("step 1")
	m2 := run.reply()
	ses, _ := h.openSession("ps_1")
	h.svc.mu.Lock()
	ses.finishing = true
	h.svc.mu.Unlock()
	if r, _ := h.client.ForgetKeyWithResponse(context.Background(), "vk_hl"); r.StatusCode() != http.StatusNoContent {
		t.Fatalf("forget: %d", r.StatusCode())
	}
	if _, ok := h.openSession("ps_1"); ok {
		t.Fatal("the forgotten key's session is still open")
	}
	if _, _, err := ses.run.Last(m2); err != nil {
		t.Fatalf("the session's run was abandoned under its step 2: %v", err)
	}
}

// A step 2 that fails closes its session and frees its places: one with a
// message that is not base64, and one with a message the protocol refuses.
func TestPresignFailedStepTwoGivesItsPlacesBack(t *testing.T) {
	h := newHarness(t)
	key := h.keygen("vk_hl", "ks_1")
	h.standIns("vk_hl", 8188)
	for _, c := range []struct {
		name    string
		session string
		message func(*signerapi.PresignStep)
		code    signerapi.ErrorCode
	}{
		{"a message that is not base64", "ps_1", func(p *signerapi.PresignStep) { p.AgentMessage = ptr("!!!") }, signerapi.ErrorCodeValidation},
		{"a message the protocol refuses", "ps_2", func(p *signerapi.PresignStep) { p.AgentMessage = ptr(b64([]byte("not the agent's reply"))) }, signerapi.ErrorCodeRefused},
	} {
		h.presignStep1("vk_hl", c.session, key, 4).accepted(c.name + ": step 1 for 4 at 8188")
		body := signerapi.PresignStep{Step: signerapi.PresignStepStepN2, SessionId: c.session}
		c.message(&body)
		done := make(chan *signerapi.PresignStepResponse, 1)
		go func() {
			r2, _ := h.client.PresignStepWithResponse(context.Background(), "vk_hl", body)
			done <- r2
		}()
		var r2 *signerapi.PresignStepResponse
		select {
		case r2 = <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("%s: step 2 did not answer in 60 s", c.name)
		}
		if r2 == nil || r2.StatusCode() != http.StatusUnprocessableEntity || r2.JSONDefault == nil || r2.JSONDefault.Code != c.code {
			t.Fatalf("%s: step 2 answered %v, want 422 %s", c.name, r2, c.code)
		}
		if _, ok := h.openSession(c.session); ok {
			t.Fatalf("%s: the failed step 2 left its session open", c.name)
		}
		h.presignStep1("vk_hl", c.session+"_next", key, 4).accepted(c.name + ": step 1 for 4 after the failed step 2")
		// The next session takes the places back for the next case.
		h.svc.mu.Lock()
		h.svc.presigns[c.session+"_next"].run.Abandon()
		delete(h.svc.presigns, c.session+"_next")
		h.svc.mu.Unlock()
	}
}

// While step 2 computes, the session's nonces stay counted until its slots
// are stored: at the ceiling, no place is free at any moment before, during
// or after the step 2. The test asks the service for room for 1 over and
// over, under its lock, while the step 2 runs.
func TestPresignSessionInStepTwoKeepsItsPlaces(t *testing.T) {
	h := newHarness(t)
	key := h.keygen("vk_hl", "ks_1")
	h.standIns("vk_hl", 8188)
	run := h.presignStep1("vk_hl", "ps_1", key, 4).accepted("step 1 for 4 at 8188")
	m2 := run.reply()

	type answer struct {
		r   *signerapi.PresignStepResponse
		err error
	}
	done := make(chan answer, 1)
	go func() {
		r2, err := h.client.PresignStepWithResponse(context.Background(), "vk_hl", signerapi.PresignStep{Step: signerapi.PresignStepStepN2, SessionId: "ps_1", AgentMessage: ptr(b64(m2))})
		done <- answer{r2, err}
	}()
	deadline := time.After(60 * time.Second)
	probes, free := 0, 0
	var got answer
	for waiting := true; waiting; {
		select {
		case got = <-done:
			waiting = false
		case <-deadline:
			t.Fatalf("step 2 did not answer in 60 s (%d probes)", probes)
		default:
		}
		h.svc.mu.Lock()
		if h.svc.room("vk_hl", 1, h.svc.opts.Now()) == nil {
			free++
		}
		h.svc.mu.Unlock()
		probes++
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if free > 0 {
		t.Fatalf("%d of %d asks for room for 1 were given room while the session's step 2 ran", free, probes)
	}
	run.finished("step 2", got.r, 4)
	if n := h.unusedNonces("vk_hl"); n != 8192 {
		t.Fatalf("unused nonces %d, want 8192", n)
	}
}
