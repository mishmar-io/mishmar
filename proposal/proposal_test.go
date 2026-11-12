package proposal

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
)

var update = flag.Bool("update", false, "rewrite spec/vectors/proposal-signing.json from this test's cases")

var t0 = time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

func str(s string) *string { return &s }

// vectorSeed is the fixed key the vectors are signed with: 0x00..0x1f.
func vectorSeed() []byte {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	return seed
}

// cases are the vectors. The first three pin the same bytes as the engine's
// own TestSigningBytesIsStable, so the two can never drift apart unnoticed.
func cases() []vectorCase {
	limit := Submit{
		KeyID: "key_a", Kind: KindOrder, Nonce: "n1", OrderType: Limit, Price: str("98000.5"), ReduceOnly: Bool(false),
		ProposedAt: t0, ExpiresAt: t0.Add(10 * time.Minute), ConnectedAccountID: "rt_hl", Side: Buy, Size: "0.5", Symbol: "BTC",
	}
	market := limit
	market.Leverage, market.Price, market.OrderType = str("2"), nil, Market
	market.ProposedAt = t0.Add(250 * time.Millisecond).In(time.FixedZone("X", 3600))
	market.ReduceOnly = Bool(true)
	zeros := market
	zeros.Leverage, zeros.Price, zeros.Size, zeros.OrderType = str("2.0"), str("98000.2500000001"), "0.50", Limit
	escaped := limit
	escaped.Symbol, escaped.ConnectedAccountID = "A<B>&C", "rt_\"q\"\\"
	cancel := Submit{
		KeyID: "key_a", Kind: KindCancel, Nonce: "n2", ProposedAt: t0, ExpiresAt: t0.Add(time.Minute),
		ConnectedAccountID: "rt_bn", Symbol: "BTCUSDT", TargetClientOrderID: "msh-a1b2c3-k3j9xq",
	}
	oneShot := limit
	oneShot.ConnectedAccountID, oneShot.Symbol = "rt_bn", "BTCUSDT"
	oneShot.VenueMessage = &VenueMessage{
		VenueKeyID: "vk_1", Scheme: "frost-ed25519",
		// base64 of a Binance parameter string; the bytes are opaque to the proposal.
		Message:            "c3ltYm9sPUJUQ1VTRFQmc2lkZT1CVVkmdHlwZT1MSU1JVCZxdWFudGl0eT0wLjUmcHJpY2U9OTgwMDAuNSZ0aW1lc3RhbXA9MTc5MjMxNzYwMDAwMCZyZWN2V2luZG93PTEwMDAw",
		EngineCommitmentID: "cm_1", Commitment: "Y29tbWl0bWVudA==", Share: "c2hhcmU=",
	}
	return []vectorCase{
		{Name: "limit order, whole seconds", Submit: limit,
			SigningBytes: `{"connectedAccountId":"rt_hl","expiresAt":"2026-10-18T10:10:00Z","keyId":"key_a","kind":"order","nonce":"n1","orderType":"limit","price":"98000.5","proposedAt":"2026-10-18T10:00:00Z","reduceOnly":false,"side":"buy","size":"0.5","symbol":"BTC"}`},
		{Name: "market order with leverage, a fractional second, a non-UTC offset normalised to Z", Submit: market,
			SigningBytes: `{"connectedAccountId":"rt_hl","expiresAt":"2026-10-18T10:10:00Z","keyId":"key_a","kind":"order","leverage":"2","nonce":"n1","orderType":"market","proposedAt":"2026-10-18T10:00:00.25Z","reduceOnly":true,"side":"buy","size":"0.5","symbol":"BTC"}`},
		{Name: "decimals signed as written, trailing zeros and all", Submit: zeros,
			SigningBytes: `{"connectedAccountId":"rt_hl","expiresAt":"2026-10-18T10:10:00Z","keyId":"key_a","kind":"order","leverage":"2.0","nonce":"n1","orderType":"limit","price":"98000.2500000001","proposedAt":"2026-10-18T10:00:00.25Z","reduceOnly":true,"side":"buy","size":"0.50","symbol":"BTC"}`},
		{Name: "string escapes as Go's encoding/json writes them", Submit: escaped,
			SigningBytes: `{"connectedAccountId":"rt_\"q\"\\","expiresAt":"2026-10-18T10:10:00Z","keyId":"key_a","kind":"order","nonce":"n1","orderType":"limit","price":"98000.5","proposedAt":"2026-10-18T10:00:00Z","reduceOnly":false,"side":"buy","size":"0.5","symbol":"A\u003cB\u003e\u0026C"}`},
		{Name: "cancel: the target and no order fields", Submit: cancel,
			SigningBytes: `{"connectedAccountId":"rt_bn","expiresAt":"2026-10-18T10:01:00Z","keyId":"key_a","kind":"cancel","nonce":"n2","proposedAt":"2026-10-18T10:00:00Z","symbol":"BTCUSDT","targetClientOrderId":"msh-a1b2c3-k3j9xq"}`},
		{Name: "one-shot order: the signer's venue message nested, its keys sorted too", Submit: oneShot,
			SigningBytes: `{"connectedAccountId":"rt_bn","expiresAt":"2026-10-18T10:10:00Z","keyId":"key_a","kind":"order","nonce":"n1","orderType":"limit","price":"98000.5","proposedAt":"2026-10-18T10:00:00Z","reduceOnly":false,"side":"buy","size":"0.5","symbol":"BTCUSDT","venueMessage":{"commitment":"Y29tbWl0bWVudA==","engineCommitmentId":"cm_1","message":"c3ltYm9sPUJUQ1VTRFQmc2lkZT1CVVkmdHlwZT1MSU1JVCZxdWFudGl0eT0wLjUmcHJpY2U9OTgwMDAuNSZ0aW1lc3RhbXA9MTc5MjMxNzYwMDAwMCZyZWN2V2luZG93PTEwMDAw","scheme":"frost-ed25519","share":"c2hhcmU=","venueKeyId":"vk_1"}}`},
	}
}

type vectorCase struct {
	Name         string `json:"name"`
	Submit       Submit `json:"proposal"`
	SigningBytes string `json:"signingBytes"`
	Signature    string `json:"signature"`
}

type vectorFile struct {
	Description string       `json:"description"`
	Secret      string       `json:"secret"`
	PublicKey   string       `json:"publicKey"`
	Cases       []vectorCase `json:"cases"`
}

const vectorPath = "../spec/vectors/proposal-signing.json"

func TestSigningBytesMatchTheEngine(t *testing.T) {
	for _, c := range cases() {
		got, err := SigningBytes(c.Submit)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.SigningBytes {
			t.Errorf("%s:\n got %s\nwant %s", c.Name, got, c.SigningBytes)
		}
	}
	// The signature field never leaks into the bytes.
	s := cases()[0].Submit
	s.Signature = "must not appear"
	got, _ := SigningBytes(s)
	if bytes.Contains(got, []byte("must not appear")) {
		t.Fatal("signature in signing bytes")
	}
}

func TestSignAndVerify(t *testing.T) {
	key := agentkey.FromSeed(vectorSeed())
	s := cases()[0].Submit
	if err := s.Sign(key); err != nil {
		t.Fatal(err)
	}
	if !Verify(key.PublicKeyB64(), s) {
		t.Fatal("does not verify")
	}
	s.Size = "0.50"
	if Verify(key.PublicKeyB64(), s) {
		t.Fatal("verifies after a change to the size")
	}
}

func TestValidate(t *testing.T) {
	good := cases()[0].Submit
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Submit){
		"no key":            func(s *Submit) { s.KeyID = "" },
		"float size":        func(s *Submit) { s.Size = "1e3" },
		"signed size":       func(s *Submit) { s.Size = "+1" },
		"limit sans price":  func(s *Submit) { s.Price = nil },
		"market with price": func(s *Submit) { s.OrderType = Market },
		"expires first":     func(s *Submit) { s.ExpiresAt = s.ProposedAt },
		"odd side":          func(s *Submit) { s.Side = "long" },
		"bad leverage":      func(s *Submit) { s.Leverage = str("2x") },
		"no reduceOnly":     func(s *Submit) { s.ReduceOnly = nil },
		"order with target": func(s *Submit) { s.TargetClientOrderID = "msh-x" },
		"half a message":    func(s *Submit) { s.VenueMessage = &VenueMessage{VenueKeyID: "vk_1", Scheme: "frost-ed25519"} },
		"proxy message": func(s *Submit) {
			s.VenueMessage = &VenueMessage{VenueKeyID: "vk_1", Scheme: "proxy", Message: "bQ==", EngineCommitmentID: "cm_1", Share: "cw=="}
		},
	}
	for name, mutate := range bad {
		s := good
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	cancel := cases()[4].Submit
	if err := cancel.Validate(); err != nil {
		t.Fatal(err)
	}
	badCancel := map[string]func(*Submit){
		"no target":   func(s *Submit) { s.TargetClientOrderID = "" },
		"with a side": func(s *Submit) { s.Side = Buy },
		"with a size": func(s *Submit) { s.Size = "1" },
		"reduce-only": func(s *Submit) { s.ReduceOnly = Bool(false) },
		"no symbol":   func(s *Submit) { s.Symbol = "" },
	}
	for name, mutate := range badCancel {
		s := cancel
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("cancel %s accepted", name)
		}
	}
	// Sign defaults an order's reduceOnly to false and leaves a cancel's out.
	key := agentkey.FromSeed(vectorSeed())
	o := cases()[0].Submit
	o.ReduceOnly = nil
	if err := o.Sign(key); err != nil || o.ReduceOnly == nil || *o.ReduceOnly {
		t.Fatalf("order reduceOnly after Sign: %v %v", o.ReduceOnly, err)
	}
	if err := cancel.Sign(key); err != nil || cancel.ReduceOnly != nil {
		t.Fatalf("cancel after Sign: %v %v", cancel.ReduceOnly, err)
	}
	n, err := NewNonce()
	if err != nil || len(n) != 32 {
		t.Fatalf("nonce %q %v", n, err)
	}
}

// TestVectors keeps spec/vectors/proposal-signing.json equal to the cases
// above, signed with the fixed key. `go test ./proposal -update` rewrites it.
func TestVectors(t *testing.T) {
	key := agentkey.FromSeed(vectorSeed())
	file := vectorFile{
		Description: "Ed25519 over the canonical JSON of the proposal without `signature`. For each case: canonicalise `proposal` (times to UTC with Z, keys sorted, no whitespace, nulls dropped) and it must equal `signingBytes`; sign it with `secret` and it must equal `signature`; `signature` must verify under `publicKey`. Ed25519 is deterministic, so the signatures are exact.",
		Secret:      agentkey.Secret("msk_vector", vectorSeed()),
		PublicKey:   key.PublicKeyB64(),
	}
	for _, c := range cases() {
		s := c.Submit
		if err := s.Sign(key); err != nil {
			t.Fatal(err)
		}
		c.Submit = s
		c.Signature = s.Signature
		c.Submit.Signature = ""
		file.Cases = append(file.Cases, c)
	}
	want, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(vectorPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vectorPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("spec/vectors/proposal-signing.json is out of date: run `go test ./proposal -update`")
	}
	// And the file is self-consistent as another language would read it.
	var back vectorFile
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	k, err := agentkey.Parse(back.Secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range back.Cases {
		b, _ := SigningBytes(c.Submit)
		if string(b) != c.SigningBytes || k.Sign(b) != c.Signature || !agentkey.Verify(back.PublicKey, b, c.Signature) {
			t.Errorf("%s: the file does not round-trip", c.Name)
		}
	}
}
