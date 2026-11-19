package service

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signerapi"
	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

// terms is a terms record signed as the engine signs it: by key, over the
// domain line and the record.
func (h *harness) terms(t hyperliquid.Terms, key ed25519.PrivateKey) *signerapi.VenueTermsProof {
	h.t.Helper()
	record, err := t.Record()
	if err != nil {
		h.t.Fatal(err)
	}
	return &signerapi.VenueTermsProof{Record: b64(record), Signature: b64(ed25519.Sign(key, hyperliquid.TermsSigningBytes(record)))}
}

// orderTerms are the terms an engine states for harness.decisionOf's
// decision of a BTC order on the testnet key vk_hl.
func orderTerms(side string) hyperliquid.Terms {
	return hyperliquid.Terms{
		Kind: hyperliquid.TermsKind, DecisionID: "dec_1", VenueKeyID: "vk_hl", Venue: "hyperliquid", Network: "testnet",
		Purpose: hyperliquid.TermsOrder, AssetIndex: "0", SizeDecimals: "5", Side: side,
	}
}

// hlMarket builds a market order's message as the engine builds it, an
// immediate-or-cancel order at price, and the decision's proposal for it,
// priced at the mark.
func (h *harness) hlMarket(clientOrderID, side, price, mark string) (message []byte, digest []byte, proposal api.Proposal) {
	h.t.Helper()
	end := h.now.Add(time.Minute)
	expiresAfter := hyperliquid.ExpiresAfter(h.now, end)
	cloid := hyperliquid.CloidFromString(clientOrderID)
	action, err := json.Marshal(hyperliquid.OrderAction{
		Type:     "order",
		Orders:   []hyperliquid.OrderWire{{A: btc.AssetIndex, B: side == "buy", P: price, S: "0.0015", T: hyperliquid.OrderTypeWire{Limit: &hyperliquid.LimitWire{Tif: hyperliquid.Ioc}}, C: &cloid}},
		Grouping: "na",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	msg, err := json.Marshal(hyperliquid.Message{Action: action, Nonce: hyperliquid.Nonce(h.now), ExpiresAfter: expiresAfter, ClientOrderID: clientOrderID})
	if err != nil {
		h.t.Fatal(err)
	}
	return msg, h.hlDigest(msg, expiresAfter), api.Proposal{
		Kind: api.ProposalKindOrder, Venue: api.VenueHyperliquid, Symbol: "BTC", Side: api.Side(side), OrderType: api.ProposalOrderTypeMarket,
		Size: "0.0015", Price: &mark, ClientOrderId: clientOrderID, NotionalUsd: "97.5", ExpiresAt: end,
	}
}

// When a request carries the engine's venue terms, the service signs only an
// action that matches them, and only terms that are the engine's, for this
// decision, this key, this network and this kind of action. A request
// without terms is signed as before.
func TestHyperliquidActionIsCheckedAgainstTheEnginesTerms(t *testing.T) {
	h := newHarness(t)
	key := h.keygen("vk_hl", "ks_1")
	_, stranger, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := hyperliquid.ClientOrderID("agt_test")
	order := signerapi.FinishRequestPurposeOrder
	cancel := signerapi.FinishRequestPurposeCancel

	limitMsg, limitDigest, limit := h.hlOrder(id)
	// The same order under a decision whose limit price is further than 1 %
	// above the order's: on the proposal's passive side, and too far from it.
	far := limit
	farPrice := "70000"
	far.Price = &farPrice
	buyMsg, buyDigest, buy := h.hlMarket(id, "buy", "66000", "65000")
	sellMsg, sellDigest, sell := h.hlMarket(id, "sell", "64000", "65000")
	end := h.now.Add(time.Minute)
	cancelMsg, cancelDigest, cancelProposal := h.hlCancelEnding(id, hyperliquid.ExpiresAfter(h.now, end), end)

	with := func(change func(*hyperliquid.Terms)) func(hyperliquid.Terms) *signerapi.VenueTermsProof {
		return func(base hyperliquid.Terms) *signerapi.VenueTermsProof {
			change(&base)
			return h.terms(base, h.engine)
		}
	}
	honest := with(func(*hyperliquid.Terms) {})
	bound := func(b string) func(hyperliquid.Terms) *signerapi.VenueTermsProof {
		return with(func(t *hyperliquid.Terms) { t.PriceBound = b })
	}
	cancelTerms := orderTerms("")
	cancelTerms.Purpose = hyperliquid.TermsCancel

	type kind struct {
		msg, digest []byte
		proposal    api.Proposal
		purpose     signerapi.FinishRequestPurpose
		base        hyperliquid.Terms
	}
	aLimit := kind{limitMsg, limitDigest, limit, order, orderTerms("buy")}
	aFarLimit := kind{limitMsg, limitDigest, far, order, orderTerms("buy")}
	aBuy := kind{buyMsg, buyDigest, buy, order, orderTerms("buy")}
	aSell := kind{sellMsg, sellDigest, sell, order, orderTerms("sell")}
	aCancel := kind{cancelMsg, cancelDigest, cancelProposal, cancel, cancelTerms}

	cases := []struct {
		name  string
		k     kind
		terms func(hyperliquid.Terms) *signerapi.VenueTermsProof // nil: the request carries none
		want  string                                             // "" when the service signs
	}{
		{"a limit order, no terms", aLimit, nil, ""},
		{"a limit order, its terms", aLimit, honest, ""},
		{"signed by another key", aLimit, func(b hyperliquid.Terms) *signerapi.VenueTermsProof { return h.terms(b, stranger) }, "the terms' engine signature does not verify"},
		{"signed by the engine without the domain line", aLimit, func(b hyperliquid.Terms) *signerapi.VenueTermsProof {
			record, _ := b.Record()
			return &signerapi.VenueTermsProof{Record: b64(record), Signature: b64(ed25519.Sign(h.engine, record))}
		}, "the terms' engine signature does not verify"},
		{"the decision and its signature in the terms' place", aLimit, func(hyperliquid.Terms) *signerapi.VenueTermsProof {
			d := h.decision(limit)
			return &signerapi.VenueTermsProof{Record: d.Record, Signature: d.Signature}
		}, "the terms' engine signature does not verify"},
		{"a record of another kind", aLimit, with(func(t *hyperliquid.Terms) { t.Kind = "approval" }), "not venue terms"},
		{"another venue", aLimit, with(func(t *hyperliquid.Terms) { t.Venue = "binance" }), "not hyperliquid"},
		{"another decision", aLimit, with(func(t *hyperliquid.Terms) { t.DecisionID = "dec_2" }), "the terms are for another decision"},
		{"another key", aLimit, with(func(t *hyperliquid.Terms) { t.VenueKeyID = "vk_other" }), "the terms are for another key"},
		{"another network", aLimit, with(func(t *hyperliquid.Terms) { t.Network = "mainnet" }), "the terms are for mainnet"},
		{"terms for a cancel with an order", aLimit, func(hyperliquid.Terms) *signerapi.VenueTermsProof { return h.terms(cancelTerms, h.engine) }, "not an order"},
		{"another asset", aLimit, with(func(t *hyperliquid.Terms) { t.AssetIndex = "1" }), "asset 0 is not BTC's 1"},
		{"an asset index with a leading zero", aLimit, with(func(t *hyperliquid.Terms) { t.AssetIndex = "00" }), "not a whole number"},
		{"other size decimals", aLimit, with(func(t *hyperliquid.Terms) { t.SizeDecimals = "2" }), "not formatted to 2 decimals"},
		{"the other side", aLimit, with(func(t *hyperliquid.Terms) { t.Side = "sell" }), "the terms' side"},
		{"reduce-only, the decision is not", aLimit, with(func(t *hyperliquid.Terms) { t.ReduceOnly = true }), "the terms' reduceOnly"},
		{"a price bound on a limit order", aLimit, bound("66000"), "not a market order"},
		{"a limit price more than 1 % from the decision's, no terms", aFarLimit, nil, ""},
		{"a limit price more than 1 % from the decision's", aFarLimit, honest, "more than 1% from the proposed 70000"},

		{"a market buy at its bound", aBuy, bound("66000"), ""},
		{"a market buy under its bound", aBuy, bound("66500"), ""},
		{"a market buy past its bound", aBuy, bound("65999"), "buy price 66000 is above the terms' bound 65999"},
		{"a market buy, terms without a bound", aBuy, honest, "price bound"},
		{"a market buy, a bound that is not a number", aBuy, bound("high"), "price bound"},
		{"a market sell at its bound", aSell, bound("64000"), ""},
		{"a market sell above its bound", aSell, bound("63500"), ""},
		{"a market sell past its bound", aSell, bound("64001"), "sell price 64000 is below the terms' bound 64001"},

		{"a cancel, no terms", aCancel, nil, ""},
		{"a cancel, its terms", aCancel, honest, ""},
		{"a cancel on another asset", aCancel, with(func(t *hyperliquid.Terms) { t.AssetIndex = "1" }), "asset 0 is not the target's 1"},
		{"terms for an order with a cancel", aCancel, func(hyperliquid.Terms) *signerapi.VenueTermsProof { return h.terms(orderTerms("buy"), h.engine) }, "not a cancel"},
		{"a cancel's terms with a side", aCancel, with(func(t *hyperliquid.Terms) { t.Side = "buy" }), "the terms of a cancel carry"},
		{"a cancel's terms for another key", aCancel, with(func(t *hyperliquid.Terms) { t.VenueKeyID = "vk_other" }), "the terms are for another key"},
	}
	presig, slots := h.presign("vk_hl", "ps_1", key, len(cases))
	for i, c := range cases {
		m4, err := key.Complete(presig, i, c.k.digest)
		if err != nil {
			t.Fatal(err)
		}
		d := h.decision(c.k.proposal)
		if c.terms != nil {
			d.Terms = c.terms(c.k.base)
		}
		r := h.hlFinish("vk_hl", slots[i], m4, c.k.msg, d, c.k.purpose)
		if c.want != "" {
			wantRefused(t, c.name, r, c.want)
			continue
		}
		if r.JSON200 == nil {
			t.Fatalf("%s: refused: %s", c.name, r.Body)
		}
		sig := hyperliquid.Signature{R: r.JSON200.Signature.R, S: r.JSON200.Signature.S, V: uint8(r.JSON200.Signature.V)}
		if addr, err := hyperliquid.RecoverAddress(c.k.digest, sig); err != nil || addr != key.Address() {
			t.Fatalf("%s: the signature recovers %s, want %s (%v)", c.name, addr, key.Address(), err)
		}
	}
}

// A terms record with a field the service does not know is refused, not read
// for the fields it does know.
func TestTermsWithAnUnknownFieldAreRefused(t *testing.T) {
	h := newHarness(t)
	key := h.keygen("vk_hl", "ks_1")
	id := hyperliquid.ClientOrderID("agt_test")
	msg, digest, proposal := h.hlOrder(id)
	record, err := orderTerms("buy").Record()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(record, &fields); err != nil {
		t.Fatal(err)
	}
	fields["note"] = "x"
	record, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	presig, slots := h.presign("vk_hl", "ps_1", key, 1)
	m4, err := key.Complete(presig, 0, digest)
	if err != nil {
		t.Fatal(err)
	}
	d := h.decision(proposal)
	d.Terms = &signerapi.VenueTermsProof{Record: b64(record), Signature: b64(ed25519.Sign(h.engine, hyperliquid.TermsSigningBytes(record)))}
	wantRefused(t, "an unknown field", h.hlFinish("vk_hl", slots[0], m4, msg, d, signerapi.FinishRequestPurposeOrder), "the terms do not parse")
}
