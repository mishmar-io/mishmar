package ecdsa2p

import (
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func TestDealAgreesOnTheKeyAndTheAddress(t *testing.T) {
	agent, engine, err := Deal()
	if err != nil {
		t.Fatal(err)
	}
	if agent.ID() != AgentID || engine.ID() != EngineID {
		t.Fatalf("ids %d %d", agent.ID(), engine.ID())
	}
	if string(agent.PublicKey()) != string(engine.PublicKey()) || agent.Address() != engine.Address() {
		t.Fatal("the shares disagree on the group key")
	}
	if !regexp.MustCompile(`^0x[0-9a-f]{40}$`).MatchString(agent.Address()) {
		t.Fatalf("address %q", agent.Address())
	}
	// The group key is the key of the summed secret, which no party holds.
	var sum secp256k1.ModNScalar
	sum.Add2(&agent.secret.Key, &engine.secret.Key)
	whole := secp256k1.NewPrivateKey(&sum)
	if AddressOf(whole.PubKey()) != agent.Address() {
		t.Fatal("the group key is not the key of x1 + x2")
	}
	if _, err := agent.Sign([]byte("digest")); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Sign: %v", err)
	}
}

func TestShareRoundTripsAndRefusesTampering(t *testing.T) {
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
		if back.ID() != s.ID() || back.Address() != s.Address() {
			t.Fatal("share changed over the round trip")
		}
	}
	data, _ := json.Marshal(agent)
	other, _ := json.Marshal(engine)
	var m, o map[string]any
	_ = json.Unmarshal(data, &m)
	_ = json.Unmarshal(other, &o)
	for name, mutate := range map[string]func(){
		"another scheme":       func() { m["scheme"] = "frost-ed25519" },
		"party 3":              func() { m["id"] = 3 },
		"the other party's id": func() { m["id"] = EngineID },
		"another secret":       func() { m["secret"] = o["secret"] },
		"garbage secret":       func() { m["secret"] = "AAAA" },
	} {
		_ = json.Unmarshal(data, &m)
		mutate()
		mutated, _ := json.Marshal(m)
		if _, err := ParseShare(mutated); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
