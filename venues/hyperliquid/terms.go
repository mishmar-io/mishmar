package hyperliquid

import (
	"fmt"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/mishmar-io/mishmar/canon"
)

// TermsKind is the kind every venue terms record carries.
const TermsKind = "venue_terms"

// termsDomain goes in front of a terms record before it is signed. A
// decision and an approval are signed as their JSON alone, which starts with
// a brace, so a signature over terms can never verify as either, nor theirs
// as terms. The version is part of the line: a later shape of the record
// gets another line and cannot verify as this one.
const termsDomain = "mishmar venue terms v1\n"

// Purposes of a terms record: the action it is for.
const (
	TermsOrder  = "order"
	TermsCancel = "cancel"
)

// Terms is the engine's statement of the venue side of one decision: which
// trading key and network the action is for, which asset the proposal's coin
// is at the venue and how its sizes are written, and, for a market order,
// the price the order may not pass. The engine signs it beside the decision,
// and the signer service checks the action against it before it signs.
//
// Numbers travel as decimal strings, as everywhere in what Mishmar signs.
type Terms struct {
	Kind         string `json:"kind"`         // always TermsKind
	DecisionID   string `json:"decisionId"`   // the decision these terms belong to
	VenueKeyID   string `json:"venueKeyId"`   // the trading key that signs
	Venue        string `json:"venue"`        // always "hyperliquid"
	Network      string `json:"network"`      // mainnet or testnet
	Purpose      string `json:"purpose"`      // TermsOrder or TermsCancel
	AssetIndex   string `json:"assetIndex"`   // the coin's index in the venue's list
	SizeDecimals string `json:"sizeDecimals"` // decimals a size may have
	// An order's side and reduce-only, as the decision's proposal has them.
	// A cancel has neither.
	Side       string `json:"side,omitempty"`
	ReduceOnly bool   `json:"reduceOnly"`
	// A market order's bound: a buy is priced at most this, a sell at
	// least this. A limit order and a cancel have none.
	PriceBound string `json:"priceBound,omitempty"`
}

// Record is the canonical JSON of the terms, the record that travels.
func (t Terms) Record() ([]byte, error) { return canon.JSON(t) }

// TermsSigningBytes is what the engine's key signs for a terms record: the
// domain line, then the record as it travels.
func TermsSigningBytes(record []byte) []byte {
	return append([]byte(termsDomain), record...)
}

// DecodeTerms reads a terms record. It refuses a record of another kind or
// venue, an unknown field, a purpose that is neither an order nor a cancel,
// and an asset index or size decimals not written as a plain whole number.
func DecodeTerms(record []byte) (Terms, error) {
	var t Terms
	if err := strictJSON(record, &t); err != nil {
		return Terms{}, mismatch("the terms do not parse: %v", err)
	}
	switch {
	case t.Kind != TermsKind:
		return Terms{}, mismatch("the record is not venue terms")
	case t.Venue != "hyperliquid":
		return Terms{}, mismatch("the terms are for %q, not hyperliquid", t.Venue)
	case t.Purpose != TermsOrder && t.Purpose != TermsCancel:
		return Terms{}, mismatch("the terms are for a %q, neither an order nor a cancel", t.Purpose)
	}
	if _, err := t.Rules(); err != nil {
		return Terms{}, err
	}
	return t, nil
}

// Rules is the asset index and size decimals the terms name.
func (t Terms) Rules() (Rules, error) {
	index, err := wholeNumber(t.AssetIndex)
	if err != nil {
		return Rules{}, mismatch("the terms' asset index %q is not a whole number", t.AssetIndex)
	}
	decimals, err := wholeNumber(t.SizeDecimals)
	if err != nil {
		return Rules{}, mismatch("the terms' size decimals %q are not a whole number", t.SizeDecimals)
	}
	return Rules{AssetIndex: index, SizeDecimals: decimals}, nil
}

// wholeNumber reads a non-negative whole number in its one plain form: no
// sign, no leading zero, no spaces.
func wholeNumber(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || strconv.Itoa(n) != s {
		return 0, fmt.Errorf("%q is not a whole number", s)
	}
	return n, nil
}

// CheckOrder checks a decoded order against the terms and the intent of the
// decision's proposal: the terms are for an order, of the intent's side and
// reduce-only; the order passes CheckOrder with the terms' asset and size
// decimals; and a market order is priced within the terms' bound, a buy at
// most at it and a sell at least. Terms for a market order without a bound,
// or for a limit order with one, are refused.
func (t Terms) CheckOrder(o Order, in Intent, clientOrderID string) error {
	if t.Purpose != TermsOrder {
		return mismatch("the terms are for a %s, not an order", t.Purpose)
	}
	if t.Side != in.Side {
		return mismatch("the terms' side %q is not the proposed %s", t.Side, in.Side)
	}
	if t.ReduceOnly != in.ReduceOnly {
		return mismatch("the terms' reduceOnly is %t, the proposal says %t", t.ReduceOnly, in.ReduceOnly)
	}
	r, err := t.Rules()
	if err != nil {
		return err
	}
	if err := CheckOrder(o, in, r, clientOrderID); err != nil {
		return err
	}
	if in.OrderType != "market" {
		if t.PriceBound != "" {
			return mismatch("the terms bound the price of an order that is not a market order")
		}
		return nil
	}
	bound, err := decimal.NewFromString(t.PriceBound)
	if err != nil || !bound.IsPositive() {
		return mismatch("the terms' price bound %q is not a positive decimal", t.PriceBound)
	}
	// CheckOrder has shown the price is a positive decimal.
	price, _ := decimal.NewFromString(o.Price)
	switch {
	case o.Side == string(Buy) && price.GreaterThan(bound):
		return mismatch("buy price %s is above the terms' bound %s", o.Price, t.PriceBound)
	case o.Side == string(Sell) && price.LessThan(bound):
		return mismatch("sell price %s is below the terms' bound %s", o.Price, t.PriceBound)
	}
	return nil
}

// CheckCancel checks a decoded cancel against the terms: they are for a
// cancel, with no side, reduce-only or price bound, and the cancel pulls the
// target client order id on the terms' asset.
func (t Terms) CheckCancel(c Cancel, targetClientOrderID string) error {
	if t.Purpose != TermsCancel {
		return mismatch("the terms are for an %s, not a cancel", t.Purpose)
	}
	if t.Side != "" || t.ReduceOnly || t.PriceBound != "" {
		return mismatch("the terms of a cancel carry an order's side, reduce-only or price bound")
	}
	r, err := t.Rules()
	if err != nil {
		return err
	}
	return CheckCancel(c, targetClientOrderID, r.AssetIndex)
}
