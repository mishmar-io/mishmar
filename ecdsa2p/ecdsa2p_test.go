package ecdsa2p

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// keygen runs the three messages by hand, as the engine relays them.
func keygen(t *testing.T) (engine, agent *Key) {
	t.Helper()
	e, err := NewKeygen(RoleEngine)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewKeygen(RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := e.First()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := a.Reply(m1)
	if err != nil {
		t.Fatal(err)
	}
	m3, engine, err := e.Last(m2)
	if err != nil {
		t.Fatal(err)
	}
	agent, err = a.Finish(m3)
	if err != nil {
		t.Fatal(err)
	}
	return engine, agent
}

// presign runs the three messages by hand for n nonces.
func presign(t *testing.T, engine, agent *Key, n int) (ep, ap *Presig) {
	t.Helper()
	ah, _ := NewSIDHalf()
	eh, _ := NewSIDHalf()
	sid, err := JointSID(ah, eh)
	if err != nil {
		t.Fatal(err)
	}
	e, err := engine.NewPresign(n, sid)
	if err != nil {
		t.Fatal(err)
	}
	a, err := agent.NewPresign(n, sid)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := e.First()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := a.Reply(m1)
	if err != nil {
		t.Fatal(err)
	}
	m3, ep, err := e.Last(m2)
	if err != nil {
		t.Fatal(err)
	}
	ap, err = a.Finish(m3)
	if err != nil {
		t.Fatal(err)
	}
	return ep, ap
}

func digest(s string) []byte {
	d := sha256.Sum256([]byte(s))
	return d[:]
}

func TestKeygenAndKeyFiles(t *testing.T) {
	engine, agent := keygen(t)
	if !bytes.Equal(engine.PublicKey(), agent.PublicKey()) || engine.Address() != agent.Address() {
		t.Fatal("the parties disagree on the key")
	}
	if engine.Role() != RoleEngine || agent.Role() != RoleAgent || !strings.HasPrefix(engine.Address(), "0x") || len(engine.Address()) != 42 {
		t.Fatalf("roles %s %s, address %s", engine.Role(), agent.Role(), engine.Address())
	}
	for _, k := range []*Key{engine, agent} {
		raw, err := json.Marshal(k)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"scheme":"`+Scheme+`"`) || !strings.Contains(string(raw), `"role":"`+string(k.Role())+`"`) {
			t.Fatalf("key file %s", raw)
		}
		back, err := ParseKey(raw)
		if err != nil {
			t.Fatal(err)
		}
		if back.Role() != k.Role() || back.Address() != k.Address() {
			t.Fatal("the key did not round-trip")
		}
		// A file whose public key does not match its blob is refused.
		var f keyFile
		_ = json.Unmarshal(raw, &f)
		f.PublicKey = f.PublicKey[:5] + "zz" + f.PublicKey[7:]
		bad, _ := json.Marshal(f)
		if _, err := ParseKey(bad); err == nil {
			t.Fatal("a key file with the wrong public key was accepted")
		}
	}
	if _, err := ParseKey([]byte(`{"scheme":"frost-ed25519","role":"agent","key":"AA==","publicKey":"AA=="}`)); err == nil {
		t.Fatal("another scheme's file was accepted")
	}
	// A step out of role is refused without touching the protocol.
	e, _ := NewKeygen(RoleEngine)
	defer e.Abandon()
	if _, err := e.Reply(nil); err == nil {
		t.Fatal("the engine replied")
	}
}

func TestPresignCompleteFinish(t *testing.T) {
	engine, agent := keygen(t)
	const n = 4
	ep, ap := presign(t, engine, agent, n)
	if ep.Len() != n || ap.Len() != n || ep.Unused() != n {
		t.Fatalf("slots %d %d", ep.Len(), ap.Len())
	}
	for i := 0; i < n; i++ {
		d := digest("order " + string(rune('a'+i)))
		m4, err := agent.Complete(ap, i, d)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := engine.Finish(ep, i, d, m4)
		if err != nil {
			t.Fatal(err)
		}
		if !engine.Verify(d, sig) || !agent.Verify(d, sig) {
			t.Fatalf("slot %d: the signature does not verify", i)
		}
		if sig.RecoveryID > 1 {
			t.Fatalf("recovery id %d", sig.RecoveryID)
		}
	}
	if ep.Unused() != 0 || ap.Unused() != 0 {
		t.Fatal("slots left after use")
	}
	// A used slot is refused on both sides, as is one out of range.
	if _, err := agent.Complete(ap, 0, digest("again")); err == nil {
		t.Fatal("the agent reused a slot")
	}
	if _, err := engine.Finish(ep, 0, digest("again"), nil); err == nil {
		t.Fatal("the engine reused a slot")
	}
	if _, err := agent.Complete(ap, n, digest("x")); err == nil {
		t.Fatal("a slot out of range was taken")
	}
	// Out of role.
	if _, err := engine.Complete(ep, 0, digest("x")); err == nil {
		t.Fatal("the engine completed")
	}
	if _, err := agent.Finish(ap, 0, digest("x"), nil); err == nil {
		t.Fatal("the agent finished")
	}
}

func TestFinishRefusesWhatIsNotForTheSlot(t *testing.T) {
	engine, agent := keygen(t)
	ep, ap := presign(t, engine, agent, 3)
	d := digest("the order")
	m4, err := agent.Complete(ap, 1, d)
	if err != nil {
		t.Fatal(err)
	}
	// The wrong slot, another digest, and a flipped byte: all refused, and
	// each attempt burns the slot it named.
	if sig, err := engine.Finish(ep, 2, d, m4); err == nil {
		t.Fatalf("finished as another slot: %+v", sig)
	}
	if sig, err := engine.Finish(ep, 0, digest("another order"), m4); err == nil {
		t.Fatalf("finished with another digest: %+v", sig)
	}
	bad := append([]byte(nil), m4...)
	bad[len(bad)/2] ^= 1
	if sig, err := engine.Finish(ep, 1, d, bad); err == nil {
		t.Fatalf("finished a corrupted message: %+v", sig)
	}
	if ep.Unused() != 0 {
		t.Fatalf("%d slots left; a failed finish must burn its slot", ep.Unused())
	}
	// The key is fine: a fresh batch signs.
	ep, ap = presign(t, engine, agent, 1)
	m4, _ = agent.Complete(ap, 0, d)
	if _, err := engine.Finish(ep, 0, d, m4); err != nil {
		t.Fatal(err)
	}
}

func TestSessionIDs(t *testing.T) {
	a, _ := NewSIDHalf()
	e, _ := NewSIDHalf()
	if len(a) != SIDHalfSize || bytes.Equal(a, e) {
		t.Fatal("halves")
	}
	s1, err := JointSID(a, e)
	if err != nil || len(s1) != 32 {
		t.Fatal(err)
	}
	if s2, _ := JointSID(e, a); bytes.Equal(s1, s2) {
		t.Fatal("the order of the halves does not matter")
	}
	if _, err := JointSID(a[:8], e); err == nil {
		t.Fatal("a short half was accepted")
	}
	engine, _ := keygen(t)
	if _, err := engine.NewPresign(0, s1); err == nil {
		t.Fatal("zero nonces")
	}
	if _, err := engine.NewPresign(1, a); err == nil {
		t.Fatal("a half as the session id")
	}
}

func TestSessionsFailCleanly(t *testing.T) {
	// A corrupted key generation message ends the run on both sides with
	// no key, and an abandoned run answers nothing more.
	e, _ := NewKeygen(RoleEngine)
	a, _ := NewKeygen(RoleAgent)
	m1, _ := e.First()
	m2, err := a.Reply(m1)
	if err != nil {
		t.Fatal(err)
	}
	m2[len(m2)/2] ^= 1
	if _, key, err := e.Last(m2); err == nil || key != nil {
		t.Fatal("the engine finished key generation from a corrupted message")
	}
	a.Abandon()
	if _, err := a.Finish(m1); err == nil {
		t.Fatal("an abandoned run finished")
	}
	// Presigning under different session ids fails.
	engine, agent := keygen(t)
	s1, _ := JointSID(make([]byte, 16), make([]byte, 16))
	s2, _ := JointSID(make([]byte, 16), bytes.Repeat([]byte{1}, 16))
	ep, _ := engine.NewPresign(2, s1)
	ap, _ := agent.NewPresign(2, s2)
	p1, _ := ep.First()
	p2, err := ap.Reply(p1)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := ep.Last(p2); err == nil {
			t.Error("the engine presigned under another session id")
		}
	}()
	select {
	case <-done:
	case <-time.After(SessionTimeout + 5*time.Second):
		t.Fatal("the failed run did not end")
	}
	ap.Abandon()
}
