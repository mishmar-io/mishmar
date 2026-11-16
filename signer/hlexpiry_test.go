package signer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

// An action the signer signs is good at the venue no later than the agent's
// own proposal lasts, plus the clock tolerance. The proposal's end is signed
// by the agent's key, so nothing in the request can move it.
func TestHyperliquidActionEndsNoLaterThanTheProposal(t *testing.T) {
	for _, kind := range []struct {
		name string
		mod  func(p *api.ProposalSubmit)
	}{{"order", nil}, {"reduce-only order", hlReduceOnly}, {"cancel", hlCancel}} {
		f := newHLFixture(t)
		if err := f.read(t); err != nil {
			t.Fatal(err)
		}
		p := f.proposal(t, kind.mod) // ends a minute from the fixture's clock
		at := func(d time.Duration) uint64 { return uint64(p.ExpiresAt.Add(d).UnixMilli()) }
		for _, tc := range []struct {
			name    string
			end     uint64
			refused bool
		}{
			{"built now for the proposal", 0, false},
			{"ten seconds from now", uint64(f.clock.Add(10 * time.Second).UnixMilli()), false},
			{"at the proposal's end", at(0), false},
			{"at the end plus the tolerance", at(hyperliquid.NonceWindow), false},
			{"a millisecond past the tolerance", at(hyperliquid.NonceWindow + time.Millisecond), true},
			{"an hour past the proposal's end", at(time.Hour), true},
			{"a year past the proposal's end", uint64(p.ExpiresAt.AddDate(1, 0, 0).UnixMilli()), true},
		} {
			f.end = tc.end
			r := f.check(f.request(t, p, 1, nil))
			switch {
			case tc.refused:
				wantHLRefusal(t, r, RefuseMessageTime, "past the proposal's end")
			case r != nil:
				t.Fatalf("%s, %s: refused: %s", kind.name, tc.name, r.Reason())
			}
		}
	}
}

// There is no signing without an end time: a body that carries none is
// refused, and so is a digest that was computed without the body's end time
// or with another one.
func TestHyperliquidActionIsSignedWithItsEndTime(t *testing.T) {
	f := newHLFixture(t)
	if err := f.read(t); err != nil {
		t.Fatal(err)
	}
	p := f.proposal(t, nil)
	good := f.request(t, p, 1, nil)
	if r := f.check(good); r != nil {
		t.Fatalf("the request as built: %s", r.Reason())
	}
	body, err := base64.StdEncoding.DecodeString(*good.VenueRequest.Body)
	if err != nil {
		t.Fatal(err)
	}
	m, action, err := hyperliquid.Decode(body)
	if err != nil {
		t.Fatal(err)
	}

	// The body without its end time, the digest being whatever it likes.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "expiresAfter")
	bare, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	h, err := hyperliquid.ActionHash(action, m.Nonce, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	without, err := hyperliquid.L1TypedData(hyperliquid.Testnet, h).Digest()
	if err != nil {
		t.Fatal(err)
	}
	for name, digest := range map[string][]byte{"the digest with the end time": mustB64(t, good.Message), "the digest of the action and nonce alone": without} {
		req := good
		req.VenueRequest = &api.VenueRequest{Method: good.VenueRequest.Method, Url: good.VenueRequest.Url, Body: ptr(base64.StdEncoding.EncodeToString(bare))}
		req.Message = base64.StdEncoding.EncodeToString(digest)
		r := f.check(req)
		if r == nil {
			t.Fatalf("a body with no end time was signed (%s)", name)
		}
		wantHLRefusal(t, r, RefuseMessage, "no expiresAfter")
	}

	// The body with its end time, the digest computed without it.
	req := good
	req.Message = base64.StdEncoding.EncodeToString(without)
	wantHLRefusal(t, f.check(req), RefuseMessage, "is not the digest of the action")

	// The body with its end time, the digest computed with a later one.
	later, err := hyperliquid.Digest(action, m.Nonce, m.ExpiresAfter+1, hyperliquid.Testnet)
	if err != nil {
		t.Fatal(err)
	}
	req.Message = base64.StdEncoding.EncodeToString(later)
	wantHLRefusal(t, f.check(req), RefuseMessage, "is not the digest of the action")
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A one-step message is built to end with the proposal, or a minute from
// now when the proposal lasts longer, and what the signer signs is the
// digest with that end time.
func TestOneStepHyperliquidMessageEndsWithTheProposal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lasts time.Duration
		ends  time.Duration // from the fixture's clock
	}{
		{"a proposal that lasts an hour", time.Hour, hyperliquid.NonceWindow},
		{"a proposal that lasts a minute", time.Minute, time.Minute},
		{"a proposal that lasts twenty seconds", 20 * time.Second, 20 * time.Second},
	} {
		for _, kind := range []string{"order", "cancel"} {
			f := newHLFixture(t)
			seen := filepath.Join(t.TempDir(), "request.json")
			f.s.Config.Hook = "cat > " + seen
			vk := f.s.Config.VenueKeys["vk_hl"]
			price := "3000"
			p := api.ProposalSubmit{
				KeyId: "key_a", ConnectedAccountId: "rt_hl", Kind: api.ProposalKindOrder, Symbol: "SOL", Side: api.SideBuy, Size: "1.5",
				Price: &price, OrderType: api.ProposalSubmitOrderTypeLimit, Nonce: "n1", ProposedAt: f.clock, ExpiresAt: f.clock.Add(tc.lasts),
			}
			if kind == "cancel" {
				hlCancel(&p)
			}
			// The key has no share here, so the build stops after the hook saw the request.
			if err := f.s.prepareHyperliquid(context.Background(), &p, "vk_hl", vk); err == nil {
				t.Fatal("prepared with no share")
			}
			raw, err := os.ReadFile(seen)
			if err != nil {
				t.Fatalf("%s, %s: the hook saw no request: %v", tc.name, kind, err)
			}
			var req api.SignRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			m, action, err := hyperliquid.Decode(mustB64(t, *req.VenueRequest.Body))
			if err != nil {
				t.Fatal(err)
			}
			if want := uint64(f.clock.Add(tc.ends).UnixMilli()); m.ExpiresAfter != want {
				t.Fatalf("%s, %s: the message ends at %d, want %d", tc.name, kind, m.ExpiresAfter, want)
			}
			if err := hyperliquid.CheckExpiry(m.ExpiresAfter, p.ExpiresAt); err != nil {
				t.Fatalf("%s, %s: the built message fails the check it will meet: %v", tc.name, kind, err)
			}
			want, err := hyperliquid.Digest(action, m.Nonce, m.ExpiresAfter, hyperliquid.Testnet)
			if err != nil {
				t.Fatal(err)
			}
			if got := mustB64(t, req.Message); string(got) != string(want) {
				t.Fatalf("%s, %s: the request's digest is not the digest with the message's end time", tc.name, kind)
			}
		}
	}
}
