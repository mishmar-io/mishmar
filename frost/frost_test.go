package frost

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// sign runs the whole protocol the way the engine and the agent's signer do:
// the engine commits, the agent commits and signs, the engine signs and
// aggregates.
func sign(t *testing.T, agent, engine *Share, message []byte) []byte {
	t.Helper()
	ep, err := engine.Party()
	if err != nil {
		t.Fatal(err)
	}
	ap, err := agent.Party()
	if err != nil {
		t.Fatal(err)
	}
	ec := ep.Commit()
	// The agent gets the engine's commitment and the message, answers with its own and its share.
	ac := ap.Commit()
	as, err := ap.Sign(message, ac, ec)
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
	return sig
}

func TestDealAndSign(t *testing.T) {
	agent, engine, err := Deal()
	if err != nil {
		t.Fatal(err)
	}
	if agent.ID() != AgentID || engine.ID() != EngineID {
		t.Fatalf("ids %d and %d", agent.ID(), engine.ID())
	}
	pub := engine.PublicKey()
	if len(pub) != ed25519.PublicKeySize || !bytes.Equal(pub, agent.PublicKey()) {
		t.Fatal("the two shares do not agree on the group's public key")
	}
	message := []byte("symbol=BTCUSDT&side=BUY&type=MARKET&quantity=0.001&timestamp=1761480000000")
	sig := sign(t, agent, engine, message)
	if !ed25519.Verify(pub, message, sig) {
		t.Fatal("the signature does not verify with crypto/ed25519")
	}
	if ed25519.Verify(pub, []byte("something else"), sig) {
		t.Fatal("the signature verifies another message")
	}
	// Every signature is fresh: new nonces, new R.
	if bytes.Equal(sig, sign(t, agent, engine, message)) {
		t.Fatal("two signatures of one message came out identical")
	}
}

func TestOneShareCannotSign(t *testing.T) {
	agent, engine, _ := Deal()
	message := []byte("m")
	ep, _ := engine.Party()
	ec := ep.Commit()
	if _, err := ep.Sign(message, ec); err == nil || !strings.Contains(err.Error(), "both parties") {
		t.Fatalf("signing with one commitment: %v", err)
	}
	ap, _ := agent.Party()
	ac := ap.Commit()
	es, err := ep.Sign(message, ec, ac)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Aggregate(message, []Commitment{ec, ac}, []SignatureShare{es}); err == nil {
		t.Fatal("aggregated with one share")
	}
	// Two shares from the same party are not a signature either.
	es2, _ := ep.Sign(message, ep.Commit(), ac)
	if _, err := engine.Aggregate(message, []Commitment{ec, ac}, []SignatureShare{es, es2}); err == nil {
		t.Fatal("aggregated two engine shares")
	}
}

func TestShareOverAnotherMessageIsRefused(t *testing.T) {
	agent, engine, _ := Deal()
	ep, _ := engine.Party()
	ap, _ := agent.Party()
	ec, ac := ep.Commit(), ap.Commit()
	as, _ := ap.Sign([]byte("what the agent saw"), ac, ec)
	message := []byte("what the engine sends")
	if err := engine.VerifyShare(message, as, ec, ac); err == nil {
		t.Fatal("a share over another message verified")
	}
	es, _ := ep.Sign(message, ec, ac)
	if _, err := engine.Aggregate(message, []Commitment{ec, ac}, []SignatureShare{as, es}); err == nil {
		t.Fatal("aggregated a share over another message")
	}
}

func TestCommitmentsAreSingleUse(t *testing.T) {
	agent, engine, _ := Deal()
	ep, _ := engine.Party()
	ap, _ := agent.Party()
	ec, ac := ep.Commit(), ap.Commit()
	if _, err := ap.Sign([]byte("one"), ac, ec); err != nil {
		t.Fatal(err)
	}
	if _, err := ap.Sign([]byte("two"), ac, ec); err == nil {
		t.Fatal("a commitment signed twice")
	}
}

func TestWireRoundTrips(t *testing.T) {
	agent, engine, _ := Deal()
	for _, s := range []*Share{agent, engine} {
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseShare(data)
		if err != nil {
			t.Fatal(err)
		}
		if back.ID() != s.ID() || !bytes.Equal(back.PublicKey(), s.PublicKey()) {
			t.Fatal("share changed over the round trip")
		}
	}
	agentData, _ := json.Marshal(agent)
	engineData, _ := json.Marshal(engine)
	agent2, _ := ParseShare(agentData)
	engine2, _ := ParseShare(engineData)
	// Commitments and shares cross the wire as bytes; the whole protocol runs on parsed values.
	ep, _ := engine2.Party()
	ap, _ := agent2.Party()
	ecWire := ep.Commit().Bytes()
	ec, err := ParseCommitment(ecWire)
	if err != nil {
		t.Fatal(err)
	}
	ac := ap.Commit()
	as, _ := ap.Sign([]byte("m"), ac, ec)
	acBack, _ := ParseCommitment(ac.Bytes())
	asBack, err := ParseSignatureShare(as.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if ec.Party() != EngineID || acBack.Party() != AgentID || asBack.Party() != AgentID {
		t.Fatal("parties lost on the wire")
	}
	es, _ := ep.Sign([]byte("m"), ec, acBack)
	sig, err := engine2.Aggregate([]byte("m"), []Commitment{ec, acBack}, []SignatureShare{asBack, es})
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(agent.PublicKey(), []byte("m"), sig) {
		t.Fatal("signature from parsed values does not verify")
	}
}

func TestParseShareRefusesTheWrongThing(t *testing.T) {
	agent, _, _ := Deal()
	data, _ := json.Marshal(agent)
	for name, mutate := range map[string]func(map[string]any){
		"another scheme":   func(m map[string]any) { m["scheme"] = "xor" },
		"party 3":          func(m map[string]any) { m["id"] = 3 },
		"the other party":  func(m map[string]any) { m["id"] = EngineID },
		"garbage keyShare": func(m map[string]any) { m["keyShare"] = "AAAA" },
		"garbage group":    func(m map[string]any) { m["group"] = "AAAA" },
	} {
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		mutate(m)
		mutated, _ := json.Marshal(m)
		if _, err := ParseShare(mutated); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := ParseShare([]byte("not json")); err == nil {
		t.Error("non-JSON was accepted")
	}
	if _, err := ParseCommitment([]byte{1, 2, 3}); err == nil {
		t.Error("a garbage commitment was accepted")
	}
	if _, err := ParseSignatureShare([]byte{1, 2, 3}); err == nil {
		t.Error("a garbage signature share was accepted")
	}
}

func TestPartyIsSafeForConcurrentUse(t *testing.T) {
	agent, engine, _ := Deal()
	ep, _ := engine.Party()
	ap, _ := agent.Party()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			message := []byte{byte(i)}
			ec, ac := ep.Commit(), ap.Commit()
			as, err := ap.Sign(message, ac, ec)
			if err != nil {
				t.Error(err)
				return
			}
			es, err := ep.Sign(message, ec, ac)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := engine.Aggregate(message, []Commitment{ec, ac}, []SignatureShare{as, es}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}

func BenchmarkSign(b *testing.B) {
	agent, engine, _ := Deal()
	ep, _ := engine.Party()
	ap, _ := agent.Party()
	message := []byte("symbol=BTCUSDT&side=BUY&type=MARKET&quantity=0.001&timestamp=1761480000000")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ec, ac := ep.Commit(), ap.Commit()
		as, _ := ap.Sign(message, ac, ec)
		es, _ := ep.Sign(message, ec, ac)
		if _, err := engine.Aggregate(message, []Commitment{ec, ac}, []SignatureShare{as, es}); err != nil {
			b.Fatal(err)
		}
	}
}
