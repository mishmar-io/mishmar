// Package hyperliquid builds, decodes and checks the Hyperliquid exchange
// actions an agent's API wallet signs: one limit order, or the cancel of an
// order by its client order id, with the nonce that goes into the signature.
//
// The agent's signer builds the message for one-shot signing (it travels
// inside the proposal with the signer's half of the signature) and checks
// the actions the engine asks it to sign over the channel; the engine decodes
// a one-shot message with this same code before it finishes the signature,
// so both sides agree on what an order is, and both derive the EIP-712
// digest from the action themselves (sign.go). Decimals are compared as
// exact decimals, never floats.
package hyperliquid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/mishmar-io/mishmar/venues/clientorder"
)

// NonceWindow is how far a message's nonce (the signer's clock, in
// milliseconds) may be from the engine's clock: the venue itself refuses
// nonces further from its own, and a message older than this is not sent.
const NonceWindow = time.Minute

// Rules are one coin's rules as the engine serves them
// (GET /agent/trading-accounts/{id}/venue-rules): the asset index an order
// names instead of the symbol, and how many decimals a size may have.
// Hyperliquid states no tick: a price has at most five significant figures
// and at most 6 - SizeDecimals decimals, and any integer is valid.
type Rules struct {
	AssetIndex   int
	SizeDecimals int
}

// Intent is the order a proposal describes, in the proposal's own words.
type Intent struct {
	Symbol     string // the coin, as proposed: BTC
	Side       string // buy or sell
	OrderType  string // market or limit
	Size       string
	Price      string // limit orders only
	ReduceOnly bool
}

// Message is a one-shot venue message (ProposalVenueMessage.message) or the
// body of a channel request: the action and the nonce the signature covers,
// as the exchange endpoint takes them, and the Mishmar client order id the
// order's cloid is derived from, which the venue never sees.
type Message struct {
	Action        json.RawMessage `json:"action"`
	Nonce         uint64          `json:"nonce"`
	ClientOrderID string          `json:"clientOrderId"`
}

// Order is a decoded order action: its one order, in the wire's own words.
type Order struct {
	Asset      int
	Side       string // buy or sell
	Price      string
	Size       string
	ReduceOnly bool
	TIF        string // Gtc, Ioc or Alo
	Cloid      string // 0x and 32 hex characters
}

// Cancel is a decoded cancel action: the one order it pulls, by cloid.
type Cancel struct {
	Asset int
	Cloid string
}

// Mismatch is why a message is not what it should be. Anything else this
// package returns is a problem with the caller's input.
type Mismatch struct{ Detail string }

func (m *Mismatch) Error() string { return "hyperliquid: " + m.Detail }

func mismatch(format string, a ...any) error { return &Mismatch{fmt.Sprintf(format, a...)} }

// IsMismatch reports whether err says the message is wrong (rather than the input).
func IsMismatch(err error) bool {
	var m *Mismatch
	return errors.As(err, &m)
}

// BuildOrder is the message for a limit intent: one order on the rules'
// asset, the size floored to the size decimals, the price rounded to the
// tick rules toward the passive side (down for a buy, up for a sell), good
// till cancelled, reduce-only when the intent says so, with the cloid
// derived from the client order id, and the nonce. A market intent is
// refused: the venue has no market orders, and the engine prices one at its
// mark and asks the signer over the channel.
func BuildOrder(in Intent, r Rules, clientOrderID string, nonce uint64) ([]byte, error) {
	if in.OrderType != "limit" {
		return nil, fmt.Errorf("hyperliquid: a %s order is not built by the signer; the engine prices it at the mark", in.OrderType)
	}
	if clientOrderID == "" {
		return nil, errors.New("hyperliquid: a client order id is required")
	}
	if r.AssetIndex < 0 {
		return nil, fmt.Errorf("hyperliquid: asset index %d is negative", r.AssetIndex)
	}
	side, err := side(in.Side)
	if err != nil {
		return nil, err
	}
	size, err := decimal.NewFromString(in.Size)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: size %q is not a decimal", in.Size)
	}
	price, err := decimal.NewFromString(in.Price)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: price %q is not a decimal", in.Price)
	}
	sz, err := FormatSize(size, r.SizeDecimals)
	if err != nil {
		return nil, err
	}
	px, err := FormatPrice(price, r.SizeDecimals, side)
	if err != nil {
		return nil, err
	}
	cloid := CloidFromString(clientOrderID)
	action := OrderAction{
		Type:     "order",
		Orders:   []OrderWire{{A: r.AssetIndex, B: side == Buy, P: px, S: sz, R: in.ReduceOnly, T: OrderTypeWire{Limit: &LimitWire{Tif: Gtc}}, C: &cloid}},
		Grouping: "na",
	}
	return encodeMessage(action, nonce, clientOrderID)
}

// BuildCancel is the message that pulls the order the agent placed under the
// client order id, on the asset: a cancel by cloid.
func BuildCancel(assetIndex int, clientOrderID string, nonce uint64) ([]byte, error) {
	if clientOrderID == "" {
		return nil, errors.New("hyperliquid: a client order id is required")
	}
	if assetIndex < 0 {
		return nil, fmt.Errorf("hyperliquid: asset index %d is negative", assetIndex)
	}
	action := CancelByCloidAction{Type: "cancelByCloid", Cancels: []CancelByCloidWire{{Asset: assetIndex, Cloid: CloidFromString(clientOrderID)}}}
	return encodeMessage(action, nonce, clientOrderID)
}

func encodeMessage(action any, nonce uint64, clientOrderID string) ([]byte, error) {
	if nonce == 0 {
		return nil, errors.New("hyperliquid: a nonce is required")
	}
	raw, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Message{Action: raw, Nonce: nonce, ClientOrderID: clientOrderID})
}

// Nonce is the nonce a message built now carries: the clock in milliseconds.
func Nonce(now time.Time) uint64 { return uint64(now.UnixMilli()) }

// Decode reads a message: the action, the nonce and the client order id,
// nothing else. The action is one order (DecodeOrder) or one cancel
// (DecodeCancel); its type says which. Anything unknown, missing or given
// twice is a Mismatch: the message is refused unseen rather than guessed at.
func Decode(message []byte) (Message, Action, error) {
	var m Message
	if err := strictJSON(message, &m); err != nil {
		return Message{}, nil, mismatch("message: %v", err)
	}
	switch {
	case m.Nonce == 0:
		return Message{}, nil, mismatch("message carries no nonce")
	case m.ClientOrderID == "":
		return Message{}, nil, mismatch("message carries no clientOrderId")
	case len(m.Action) == 0:
		return Message{}, nil, mismatch("message carries no action")
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(m.Action, &head); err != nil {
		return Message{}, nil, mismatch("action: %v", err)
	}
	switch head.Type {
	case "order":
		var a OrderAction
		if err := strictJSON(m.Action, &a); err != nil {
			return Message{}, nil, mismatch("order action: %v", err)
		}
		return m, a, nil
	case "cancelByCloid":
		var a CancelByCloidAction
		if err := strictJSON(m.Action, &a); err != nil {
			return Message{}, nil, mismatch("cancel action: %v", err)
		}
		return m, a, nil
	}
	return Message{}, nil, mismatch("action type %q is neither order nor cancelByCloid", head.Type)
}

// DecodeOrder reads the one order of an order action: one order, grouping
// "na", no builder fee, a plain limit order type.
func DecodeOrder(a Action) (Order, error) {
	oa, ok := a.(OrderAction)
	if !ok {
		return Order{}, mismatch("the action is a %s, not an order", a.ActionType())
	}
	switch {
	case oa.Type != "order":
		return Order{}, mismatch("action type %q is not order", oa.Type)
	case len(oa.Orders) != 1:
		return Order{}, mismatch("the action carries %d orders, not one", len(oa.Orders))
	case oa.Grouping != "na":
		return Order{}, mismatch("grouping %q is not na", oa.Grouping)
	case oa.Builder != nil:
		return Order{}, mismatch("the action carries a builder fee")
	}
	w := oa.Orders[0]
	switch {
	case w.T.Limit == nil || w.T.Trigger != nil:
		return Order{}, mismatch("the order is not a plain limit order")
	case w.A < 0:
		return Order{}, mismatch("asset index %d is negative", w.A)
	case w.C == nil:
		return Order{}, mismatch("the order carries no cloid")
	}
	if _, err := ParseCloid(*w.C); err != nil {
		return Order{}, mismatch("%v", err)
	}
	o := Order{Asset: w.A, Side: string(Sell), Price: w.P, Size: w.S, ReduceOnly: w.R, TIF: string(w.T.Limit.Tif), Cloid: *w.C}
	if w.B {
		o.Side = string(Buy)
	}
	return o, nil
}

// DecodeCancel reads the one cancel of a cancel-by-cloid action.
func DecodeCancel(a Action) (Cancel, error) {
	ca, ok := a.(CancelByCloidAction)
	if !ok {
		return Cancel{}, mismatch("the action is a %s, not a cancel", a.ActionType())
	}
	switch {
	case ca.Type != "cancelByCloid":
		return Cancel{}, mismatch("action type %q is not cancelByCloid", ca.Type)
	case len(ca.Cancels) != 1:
		return Cancel{}, mismatch("the action carries %d cancels, not one", len(ca.Cancels))
	case ca.Cancels[0].Asset < 0:
		return Cancel{}, mismatch("asset index %d is negative", ca.Cancels[0].Asset)
	}
	if _, err := ParseCloid(ca.Cancels[0].Cloid); err != nil {
		return Cancel{}, mismatch("%v", err)
	}
	return Cancel{Asset: ca.Cancels[0].Asset, Cloid: ca.Cancels[0].Cloid}, nil
}

// CheckOrder checks a decoded order against the intent it was built for,
// the coin's rules and the client order id it must carry: the asset is the
// rules'; the side is the proposal's; the cloid derives from the client
// order id; the size is formatted to the size decimals, positive and at
// most the proposed size (floored, never rounded up); a limit order is Gtc
// at a price on the tick rules, within 1 % of the proposed price and on its
// passive side (at most for a buy, at least for a sell); a market order is
// Ioc, its price the engine's bound at the mark, which the signer does not
// know and does not check; reduce-only is the proposal's.
func CheckOrder(o Order, in Intent, r Rules, clientOrderID string) error {
	if o.Cloid != CloidFromString(clientOrderID) {
		return mismatch("cloid %s is not the one of client order id %q", o.Cloid, clientOrderID)
	}
	if o.Asset != r.AssetIndex {
		return mismatch("asset %d is not %s's %d", o.Asset, in.Symbol, r.AssetIndex)
	}
	s, err := side(in.Side)
	if err != nil {
		return err
	}
	if o.Side != string(s) {
		return mismatch("side %s is not the proposed %s", o.Side, in.Side)
	}
	size, err := decimal.NewFromString(o.Size)
	want, wantErr := decimal.NewFromString(in.Size)
	switch {
	case err != nil:
		return mismatch("size %q is not a decimal", o.Size)
	case wantErr != nil:
		return fmt.Errorf("hyperliquid: the proposal's size %q is not a decimal", in.Size)
	case !size.IsPositive():
		return mismatch("size %s is not positive", o.Size)
	case size.GreaterThan(want):
		return mismatch("size %s is more than the proposed %s", o.Size, in.Size)
	}
	if f, err := FormatSize(size, r.SizeDecimals); err != nil || f != o.Size {
		return mismatch("size %s is not formatted to %d decimals", o.Size, r.SizeDecimals)
	}
	price, err := decimal.NewFromString(o.Price)
	if err != nil || !price.IsPositive() {
		return mismatch("price %q is not a positive decimal", o.Price)
	}
	if f, err := FormatPrice(price, r.SizeDecimals, s); err != nil || f != o.Price {
		return mismatch("price %s is not on the tick rules for %d size decimals", o.Price, r.SizeDecimals)
	}
	switch in.OrderType {
	case "market":
		if o.TIF != string(Ioc) {
			return mismatch("a market order is sent Ioc, not %s", o.TIF)
		}
	case "limit":
		if o.TIF != string(Gtc) {
			return mismatch("a limit order is sent Gtc, not %s", o.TIF)
		}
		wantPrice, err := decimal.NewFromString(in.Price)
		switch {
		case err != nil || !wantPrice.IsPositive():
			return fmt.Errorf("hyperliquid: the proposal's price %q is not a positive decimal", in.Price)
		case s == Buy && price.GreaterThan(wantPrice):
			return mismatch("buy price %s is above the proposed %s", o.Price, in.Price)
		case s == Sell && price.LessThan(wantPrice):
			return mismatch("sell price %s is below the proposed %s", o.Price, in.Price)
		case !withinOnePercent(price, wantPrice):
			return mismatch("price %s is more than 1%% from the proposed %s", o.Price, in.Price)
		}
	default:
		return fmt.Errorf("hyperliquid: order type %q is neither market nor limit", in.OrderType)
	}
	if o.ReduceOnly != in.ReduceOnly {
		return mismatch("reduceOnly is %t, the proposal says %t", o.ReduceOnly, in.ReduceOnly)
	}
	return nil
}

// CheckCancel checks a decoded cancel: it pulls the target client order id
// on the asset.
func CheckCancel(c Cancel, targetClientOrderID string, assetIndex int) error {
	if c.Cloid != CloidFromString(targetClientOrderID) {
		return mismatch("cloid %s is not the one of the target %q", c.Cloid, targetClientOrderID)
	}
	if c.Asset != assetIndex {
		return mismatch("asset %d is not the target's %d", c.Asset, assetIndex)
	}
	return nil
}

// CheckFresh refuses a nonce further than NonceWindow from now.
func CheckFresh(nonce uint64, now time.Time) error {
	at := time.UnixMilli(int64(nonce))
	if d := now.Sub(at); d > NonceWindow || d < -NonceWindow {
		return mismatch("nonce %d is %s from now", nonce, d.Round(time.Second))
	}
	return nil
}

// ClientOrderID is a fresh client order id for the agent, in Mishmar's shape.
func ClientOrderID(agentID string) string { return clientorder.New(agentID) }

// IsClientOrderIDOf reports whether id is one of the agent's.
func IsClientOrderIDOf(id, agentID string) bool { return clientorder.IsOf(id, agentID) }

// CloidFromString derives the cloid an order carries from a Mishmar client
// order id: the first 16 bytes of SHA-256 of the id, as 0x and 32 hex
// characters. Anyone holding the decision can recompute it.
func CloidFromString(clientOrderID string) string {
	sum := sha256.Sum256([]byte(clientOrderID))
	return "0x" + hex.EncodeToString(sum[:16])
}

// ParseCloid checks a cloid's shape: 0x and 32 lower-case hex characters.
func ParseCloid(s string) ([16]byte, error) {
	var c [16]byte
	if len(s) != 34 || s[:2] != "0x" || strings.ToLower(s) != s {
		return c, fmt.Errorf("hyperliquid: cloid %q must be 0x followed by 32 lower-case hex characters", s)
	}
	raw, err := hex.DecodeString(s[2:])
	if err != nil {
		return c, fmt.Errorf("hyperliquid: cloid %q: %w", s, err)
	}
	copy(c[:], raw)
	return c, nil
}

func side(s string) (Side, error) {
	switch s {
	case "buy":
		return Buy, nil
	case "sell":
		return Sell, nil
	}
	return "", fmt.Errorf("hyperliquid: side %q is not buy or sell", s)
}

func withinOnePercent(price, want decimal.Decimal) bool {
	diff := price.Sub(want).Abs()
	return diff.Mul(decimal.NewFromInt(100)).LessThanOrEqual(want)
}

// strictJSON decodes one JSON value into v, refusing unknown fields and
// anything after the value.
func strictJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}
