package frost

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"testing"
)

// generate runs a whole two-party key generation and returns both shares.
func generate(t *testing.T) (agent, engine *Share) {
	t.Helper()
	ek, err := NewKeygen(EngineID)
	if err != nil {
		t.Fatal(err)
	}
	ak, err := NewKeygen(AgentID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ek.First()
	if err != nil {
		t.Fatal(err)
	}
	reply, err := ak.Reply(first)
	if err != nil {
		t.Fatal(err)
	}
	last, engine, err := ek.Last(reply)
	if err != nil {
		t.Fatal(err)
	}
	agent, err = ak.Finish(last)
	if err != nil {
		t.Fatal(err)
	}
	return agent, engine
}

func TestKeygenMakesAKeyBothHalvesSignWith(t *testing.T) {
	agent, engine := generate(t)
	if agent.ID() != AgentID || engine.ID() != EngineID {
		t.Fatalf("ids %d %d", agent.ID(), engine.ID())
	}
	if !bytes.Equal(agent.PublicKey(), engine.PublicKey()) {
		t.Fatal("the two halves disagree on the public key")
	}
	message := []byte("symbol=BTCUSDT&side=BUY&type=MARKET&quantity=0.001&timestamp=1")
	ap, err := agent.Party()
	if err != nil {
		t.Fatal(err)
	}
	ep, err := engine.Party()
	if err != nil {
		t.Fatal(err)
	}
	ac, ec := ap.Commit(), ep.Commit()
	as, err := ap.Sign(message, ec, ac)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.VerifyShare(message, as, ec, ac); err != nil {
		t.Fatal(err)
	}
	es, err := ep.Sign(message, ec, ac)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := engine.Aggregate(message, []Commitment{ec, ac}, []SignatureShare{as, es})
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(engine.PublicKey(), message, sig) {
		t.Fatal("the signature does not verify as plain Ed25519")
	}
	// Generated shares store and load like dealt ones.
	raw, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseShare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID() != AgentID || !bytes.Equal(back.PublicKey(), agent.PublicKey()) {
		t.Fatal("the stored share came back different")
	}
	// Two runs make two keys.
	again, _ := generate(t)
	if bytes.Equal(again.PublicKey(), agent.PublicKey()) {
		t.Fatal("two key generations made the same key")
	}
}

func TestKeygenRefusesTheWrongPartyAndOrder(t *testing.T) {
	ek, _ := NewKeygen(EngineID)
	ak, _ := NewKeygen(AgentID)
	if _, err := ak.First(); err == nil {
		t.Fatal("the agent opened")
	}
	if _, err := ek.Reply(nil); err == nil {
		t.Fatal("the engine replied")
	}
	if _, _, err := ek.Last(nil); err == nil {
		t.Fatal("Last ran before First")
	}
	if _, err := ak.Finish(nil); err == nil {
		t.Fatal("Finish ran before Reply")
	}
	if _, err := NewKeygen(3); err == nil {
		t.Fatal("party 3 was accepted")
	}
	first, err := ek.First()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ek.First(); err == nil {
		t.Fatal("First ran twice")
	}
	// The engine's own message fed back as a reply is refused.
	if _, _, err := ek.Last(first); err == nil {
		t.Fatal("the engine's own message was accepted as a reply")
	}
	// An agent that answered another engine's opening message: its reply
	// is well formed on its own (a party's share depends only on its own
	// polynomial), so the mismatch shows where it matters, at the agent's
	// Finish, where the engine's share is checked against the commitment
	// the agent actually saw.
	other, _ := NewKeygen(EngineID)
	otherFirst, _ := other.First()
	strangerReply, err := ak.Reply(otherFirst)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := ek.Last(strangerReply)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ak.Finish(last); err == nil {
		t.Fatal("the agent accepted a share from an engine whose commitment it never saw")
	}
}

func TestKeygenRefusesATamperedShare(t *testing.T) {
	ek, _ := NewKeygen(EngineID)
	ak, _ := NewKeygen(AgentID)
	first, _ := ek.First()
	reply, err := ak.Reply(first)
	if err != nil {
		t.Fatal(err)
	}
	var r keygenReply
	if err := json.Unmarshal(reply, &r); err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the middle of the agent's share for the engine.
	raw := []byte(r.Round2)
	raw[len(raw)/2] ^= 0x01
	r.Round2 = string(raw)
	tampered, _ := json.Marshal(r)
	if _, _, err := ek.Last(tampered); err == nil {
		t.Fatal("a tampered share was accepted")
	}
	// The engine's closing message, tampered, is refused by the agent.
	ek2, _ := NewKeygen(EngineID)
	ak2, _ := NewKeygen(AgentID)
	first2, _ := ek2.First()
	reply2, _ := ak2.Reply(first2)
	last, _, err := ek2.Last(reply2)
	if err != nil {
		t.Fatal(err)
	}
	last[len(last)/2] ^= 0x01
	if _, err := ak2.Finish(last); err == nil {
		t.Fatal("a tampered closing message was accepted")
	}
}
