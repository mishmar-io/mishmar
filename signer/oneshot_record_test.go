package signer

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/venues/binance"
)

// A one-shot order is in the record before its message exists, and its
// proposal is signed through the channel once, after that message has ended
// at the venue, under the same client order id (orders.go).

const oneShotCID = "msh-ag1-abc123"

// prepared records the fixture's proposal as Prepare does for a one-shot
// order whose message the venue takes for life from the clock's now.
func prepared(t *testing.T, s *Signer, p api.ProposalSubmit, clock *testClock, life time.Duration) time.Time {
	t.Helper()
	until := clock.now().Add(life)
	if r := s.recordOneShot(&p, ptr(oneShotCID), until, clock.now()); r != nil {
		t.Fatalf("recording the one-shot order: %s", r.Reason())
	}
	return until
}

// While the one-shot message may still be taken at the venue, and for the
// clock allowance after it, a channel request for its proposal is refused
// with the code that means "ask again later", in this process and after a
// restart. A moment later it is signed, once: the entry becomes a signed
// one, and a further request is refused as any second order is.
func TestAOneShotProposalWaitsOutItsMessageBeforeTheChannelSigns(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "agent.signed.json")
	clock := &testClock{t: now}
	s := recordSigner(t, f, path, clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
	until := prepared(t, s, p, clock, 10*time.Second)

	rec := readOrders(t, path)
	if len(rec.Orders) != 1 || rec.Orders[0].Nonce != "n1" || rec.Orders[0].KeyID != "key_a" || rec.Orders[0].RequestID != "" ||
		rec.Orders[0].ClientOrderID != oneShotCID || rec.Orders[0].OneShotUntil == nil || !rec.Orders[0].OneShotUntil.Equal(until) {
		t.Fatalf("the record after Prepare: %+v", rec)
	}

	r1, _, _ := f.orderAt(t, p, "sr_1", clock.now())
	a := s.Handle(context.Background(), r1)
	wantRefusal(t, "a channel request while the one-shot message is live", a, RefuseOneShotLive)
	if after := until.Add(oneShotGrace).UTC().Format(time.RFC3339); !strings.Contains(a.Refusal.Reason, "after "+after) {
		t.Fatalf("the refusal names when to ask again (%s): %s", after, a.Refusal.Reason)
	}

	// The record itself holds the wait, not only the check before signing:
	// a signature for the proposal is not recorded as released while the
	// one-shot message is live, and the entry stays Prepare's.
	if r := s.recordSigned(r1); r == nil || r.Code != RefuseOneShotLive {
		t.Fatalf("recording a channel signature while the one-shot message is live: %v, want %s", r, RefuseOneShotLive)
	}
	if got := readOrders(t, path).Orders[0]; got.RequestID != "" {
		t.Fatalf("the record took a channel signature during the wait: %+v", got)
	}

	// The message's end at the venue plus the clock allowance, to the moment: still refused, after a restart too.
	clock.add(10*time.Second + oneShotGrace)
	s = recordSigner(t, f, path, clock.now)
	r2, _, _ := f.orderAt(t, p, "sr_2", clock.now())
	wantRefusal(t, "at the last moment of the wait, after a restart", s.Handle(context.Background(), r2), RefuseOneShotLive)
	if got := readOrders(t, path).Orders[0]; got.RequestID != "" {
		t.Fatalf("a refused request changed the record: %+v", got)
	}

	clock.add(time.Millisecond)
	r3, round3, msg3 := f.orderAt(t, p, "sr_3", clock.now())
	f.finish(t, "the channel request after the wait", s.Handle(context.Background(), r3), round3, []byte(msg3))
	got := readOrders(t, path)
	if len(got.Orders) != 1 || got.Orders[0].RequestID != "sr_3" || got.Orders[0].OneShotUntil == nil || !got.Orders[0].OneShotUntil.Equal(until) {
		t.Fatalf("the record after the channel signed: %+v", got)
	}

	clock.add(time.Second)
	r4, _, _ := f.orderAt(t, p, "sr_4", clock.now())
	wantRefusal(t, "a second channel request", s.Handle(context.Background(), r4), RefuseOrderSigned)
}

// After the wait the channel signs only under the client order id the
// one-shot message carried: another id, or none, is refused and leaves the
// record as it was, and the right id is then still signed.
func TestTheChannelSignsAOneShotProposalUnderItsClientOrderIDOnly(t *testing.T) {
	f := newFixture(t)
	clock := &testClock{t: now}
	s := recordSigner(t, f, "", clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
	prepared(t, s, p, clock, 10*time.Second)
	clock.add(10*time.Second + oneShotGrace + time.Millisecond)

	other, _, _ := f.orderAt(t, p, "sr_1", clock.now())
	other.ClientOrderId = ptr("msh-ag1-zzz999")
	other.Message = b64(binanceOrder(p, "msh-ag1-zzz999", map[string]string{"timestamp": ms(clock.now())}))
	a := s.Handle(context.Background(), other)
	wantRefusal(t, "another client order id", a, RefuseClientOrderID)
	if !strings.Contains(a.Refusal.Reason, oneShotCID) || !strings.Contains(a.Refusal.Reason, "msh-ag1-zzz999") {
		t.Fatalf("the refusal names both ids: %s", a.Refusal.Reason)
	}

	same, round, msg := f.orderAt(t, p, "sr_2", clock.now())
	f.finish(t, "the one-shot message's own id", s.Handle(context.Background(), same), round, []byte(msg))
}

// A proposal gets one one-shot message: Prepare for a proposal that is in
// the record already, prepared or signed through the channel, is refused.
// Another proposal is recorded beside it, and a cancel is not recorded.
func TestASecondOneShotMessageForAProposalIsRefused(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "agent.signed.json")
	clock := &testClock{t: now}
	s := recordSigner(t, f, path, clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
	prepared(t, s, p, clock, 10*time.Second)
	clock.add(time.Hour)
	// Long after the first message ended, and under the same client order id: still no second message.
	for _, cid := range []string{oneShotCID, "msh-ag1-other1"} {
		if r := s.recordOneShot(&p, ptr(cid), clock.now().Add(10*time.Second), clock.now()); r == nil || r.Code != RefuseOrderSigned {
			t.Fatalf("a second Prepare for a prepared proposal, under %s: %v, want %s", cid, r, RefuseOrderSigned)
		}
	}

	q := f.liveProposal(t, "n2", clock.now(), 10*time.Minute)
	r1, round1, msg1 := f.orderAt(t, q, "sr_1", clock.now())
	f.finish(t, "an order through the channel", s.Handle(context.Background(), r1), round1, []byte(msg1))
	if r := s.recordOneShot(&q, ptr(oneShotCID), clock.now().Add(10*time.Second), clock.now()); r == nil || r.Code != RefuseOrderSigned {
		t.Fatalf("Prepare for a proposal the channel signed: %v, want %s", r, RefuseOrderSigned)
	}

	w := f.liveProposal(t, "n3", clock.now(), 10*time.Minute)
	prepared(t, s, w, clock, 10*time.Second)
	if got := nonces(readOrders(t, path)); len(got) != 2 {
		// n1 ended an hour ago and went with this write.
		t.Fatalf("the record holds %v, want n2 and n3", got)
	}

	fresh := filepath.Join(t.TempDir(), "agent.signed.json")
	s2 := recordSigner(t, f, fresh, clock.now)
	pull := f.liveProposal(t, "n4", clock.now(), 10*time.Minute)
	pull.Kind = api.ProposalKindCancel
	if r := s2.recordOneShot(&pull, nil, clock.now().Add(10*time.Second), clock.now()); r != nil {
		t.Fatalf("a one-shot cancel: %s", r.Reason())
	}
	if _, err := os.Stat(fresh); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a one-shot cancel wrote the record: %v", err)
	}
}

// No one-shot message is made for an order the record cannot take, or for
// one that names no client order id.
func TestNoOneShotMessageWithoutItsRecord(t *testing.T) {
	f := newFixture(t)
	clock := &testClock{t: now}
	path := filepath.Join(t.TempDir(), "agent.signed.json")
	if err := os.WriteFile(path, []byte("not the record"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := recordSigner(t, f, path, clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
	if r := s.recordOneShot(&p, ptr(oneShotCID), clock.now().Add(10*time.Second), clock.now()); r == nil || r.Code != RefuseOrderRecord {
		t.Fatalf("with a record that does not read: %v, want %s", r, RefuseOrderRecord)
	}
	s = recordSigner(t, f, "", clock.now)
	if r := s.recordOneShot(&p, nil, clock.now().Add(10*time.Second), clock.now()); r == nil || r.Code != RefuseMalformed {
		t.Fatalf("with no client order id: %v, want %s", r, RefuseMalformed)
	}
}

// A signer from before the field reads Prepare's entry as a signed order: it
// drops the field it does not know, and what it writes back refuses every
// later order for the proposal. A downgrade loses the wait, never the "once".
func TestAnEntryWithoutItsOneShotFieldIsASignedOrder(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "agent.signed.json")
	clock := &testClock{t: now}
	s := recordSigner(t, f, path, clock.now)
	p := f.liveProposal(t, "n1", clock.now(), 10*time.Minute)
	prepared(t, s, p, clock, 10*time.Second)

	rec := readOrders(t, path)
	rec.Orders[0].OneShotUntil = nil
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	clock.add(10*time.Second + oneShotGrace + time.Millisecond)
	r1, _, _ := f.orderAt(t, p, "sr_1", clock.now())
	wantRefusal(t, "after an older signer rewrote the entry", s.Handle(context.Background(), r1), RefuseOrderSigned)
}

// Prepare itself: a one-shot order sent through Propose is in the record
// with the client order id its message carries and the end of that
// message's window at the venue, and a second Propose of the same proposal
// sends nothing.
func TestPrepareRecordsTheOneShotOrder(t *testing.T) {
	s, eng := newOneShot(t, nil)
	s.OrdersPath = filepath.Join(t.TempDir(), "agent.signed.json")
	if _, err := s.Propose(context.Background(), limitProposal()); err != nil {
		t.Fatal(err)
	}
	if len(eng.sigs) != 1 {
		t.Fatalf("one-shot %d, want 1", len(eng.sigs))
	}
	o, err := binance.DecodeOrder(eng.sigs[0].message)
	if err != nil {
		t.Fatal(err)
	}
	rec := readOrders(t, s.OrdersPath)
	want := now.Add(binance.RecvWindow * time.Millisecond)
	if len(rec.Orders) != 1 || rec.Orders[0].KeyID != "key_a" || rec.Orders[0].Nonce != "n1" || rec.Orders[0].ClientOrderID != o.ClientOrderID ||
		rec.Orders[0].RequestID != "" || rec.Orders[0].OneShotUntil == nil || !rec.Orders[0].OneShotUntil.Equal(want) {
		t.Fatalf("the record after a one-shot order: %+v (message id %s, window ends %s)", rec, o.ClientOrderID, want)
	}

	var refused *Refusal
	if _, err := s.Propose(context.Background(), limitProposal()); !errors.As(err, &refused) || refused.Code != RefuseOrderSigned {
		t.Fatalf("the same proposal again: %v, want %s", err, RefuseOrderSigned)
	}
	if len(eng.sigs) != 1 || eng.plain != 0 {
		t.Fatalf("the same proposal again sent something: one-shot %d, plain %d", len(eng.sigs), eng.plain)
	}
}
