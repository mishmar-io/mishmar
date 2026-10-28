// Package binance builds and decodes the Binance USDⓈ-M futures requests an
// agent's venue key signs: an order's or a cancel's parameter string, which
// is exactly what Binance verifies the signature over, timestamp and
// recvWindow included.
//
// The agent's signer builds them for one-shot signing (the venue message
// travels inside the proposal) and checks the ones the engine asks it to sign
// over the channel; the engine decodes a one-shot message with this same code
// before it spends its own share, so both sides agree on what an order is.
// Decimals are compared as exact rationals, never floats.
package binance

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mishmar-io/mishmar/proposal"
)

const (
	// OrderPath is where an order is placed (POST) and cancelled (DELETE).
	OrderPath = "/fapi/v1/order"
	// RecvWindow is the window, in milliseconds, a built request carries:
	// Binance refuses it once this long has passed since its timestamp.
	RecvWindow = 10000
	// RespType is the response an order asks for, so the ack carries the
	// order's status and what it filled.
	RespType = "RESULT"
)

// Rules are one symbol's trading rules as decimal strings, as the engine
// serves them (GET /agent/trading-accounts/{id}/venue-rules). An empty field
// is a rule the venue does not state.
type Rules struct {
	Tick, Step, MinQty, MinNotional string
}

// Intent is the order a proposal describes, in the proposal's own words.
type Intent struct {
	Symbol     string // as proposed; Binance's symbol is its upper case
	Side       string // buy or sell
	OrderType  string // market or limit
	Size       string
	Price      string // limit orders only
	ReduceOnly bool
}

// Order is a decoded order parameter string.
type Order struct {
	Symbol, Side, Type, Quantity, Price, TimeInForce, ClientOrderID, RespType string
	ReduceOnly                                                                bool
	Timestamp                                                                 time.Time
	RecvWindow                                                                int
}

// Cancel is a decoded cancel parameter string.
type Cancel struct {
	Symbol, ClientOrderID string
	Timestamp             time.Time
	RecvWindow            int
}

// Mismatch is why a message is not what it should be. Anything else this
// package returns is a problem with the caller's input.
type Mismatch struct{ Detail string }

func (m *Mismatch) Error() string { return "binance: " + m.Detail }

func mismatch(format string, a ...any) error { return &Mismatch{fmt.Sprintf(format, a...)} }

// IsMismatch reports whether err says the message is wrong (rather than the input).
func IsMismatch(err error) bool {
	var m *Mismatch
	return errors.As(err, &m)
}

var (
	orderParams  = set("symbol", "side", "type", "quantity", "price", "timeInForce", "reduceOnly", "newClientOrderId", "newOrderRespType", "timestamp", "recvWindow")
	cancelParams = set("symbol", "origClientOrderId", "timestamp", "recvWindow")
	// orderOnly are the parameters that make a request an order; a read the
	// signer co-signs to prove the key carries none of them.
	orderOnly = []string{"symbol", "side", "type", "quantity", "price", "newClientOrderId", "origClientOrderId"}
)

// BuildOrder is the order parameter string for an intent: the size floored
// to the step, a limit price rounded to the tick toward the passive side
// (down for a buy, up for a sell) with GTC, reduceOnly when the intent says
// so, the client order id, and the timestamp and recvWindow the signature
// covers. The keys are sorted, as net/url encodes them.
func BuildOrder(in Intent, r Rules, clientOrderID string, now time.Time) ([]byte, error) {
	if clientOrderID == "" || len(clientOrderID) > 36 {
		return nil, fmt.Errorf("binance: a client order id has 1 to 36 characters, not %q", clientOrderID)
	}
	size, ok := Decimal(in.Size)
	if !ok || size.Sign() <= 0 {
		return nil, fmt.Errorf("binance: size %q is not a positive decimal", in.Size)
	}
	step, err := optionalRule("step", r.Step)
	if err != nil {
		return nil, err
	}
	qty := floorTo(size, step)
	if qty.Sign() <= 0 {
		return nil, fmt.Errorf("binance: size %s is below the step of %s for %s", in.Size, r.Step, in.Symbol)
	}
	if minQty, err := optionalRule("minimum size", r.MinQty); err != nil {
		return nil, err
	} else if minQty != nil && qty.Cmp(minQty) < 0 {
		return nil, fmt.Errorf("binance: size %s is below the minimum of %s for %s", format(qty, r.Step), r.MinQty, in.Symbol)
	}
	p := url.Values{
		"symbol": {strings.ToUpper(in.Symbol)}, "quantity": {format(qty, r.Step)},
		"newClientOrderId": {clientOrderID}, "newOrderRespType": {RespType},
		"timestamp": {strconv.FormatInt(now.UnixMilli(), 10)}, "recvWindow": {strconv.Itoa(RecvWindow)},
	}
	switch in.Side {
	case "buy", "sell":
		p.Set("side", strings.ToUpper(in.Side))
	default:
		return nil, fmt.Errorf("binance: side %q is neither buy nor sell", in.Side)
	}
	switch in.OrderType {
	case "market":
		p.Set("type", "MARKET")
	case "limit":
		px, ok := Decimal(in.Price)
		if !ok || px.Sign() <= 0 {
			return nil, fmt.Errorf("binance: a limit order needs a positive decimal price, not %q", in.Price)
		}
		tick, err := optionalRule("tick", r.Tick)
		if err != nil {
			return nil, err
		}
		if in.Side == "buy" {
			px = floorTo(px, tick)
		} else {
			px = ceilTo(px, tick)
		}
		if px.Sign() <= 0 {
			return nil, fmt.Errorf("binance: price %s is below the tick of %s for %s", in.Price, r.Tick, in.Symbol)
		}
		p.Set("type", "LIMIT")
		p.Set("price", format(px, r.Tick))
		p.Set("timeInForce", "GTC")
	default:
		return nil, fmt.Errorf("binance: order type %q is neither market nor limit", in.OrderType)
	}
	if in.ReduceOnly {
		p.Set("reduceOnly", "true")
	}
	return []byte(p.Encode()), nil
}

// DecodeOrder reads an order parameter string. Any parameter outside the
// known set, or one given twice, is a Mismatch: the message is refused
// unseen rather than guessed at.
func DecodeOrder(message []byte) (Order, error) {
	p, err := params(message, orderParams)
	if err != nil {
		return Order{}, err
	}
	o := Order{
		Symbol: p["symbol"], Side: p["side"], Type: p["type"], Quantity: p["quantity"], Price: p["price"],
		TimeInForce: p["timeInForce"], ClientOrderID: p["newClientOrderId"], RespType: p["newOrderRespType"],
	}
	switch v, ok := p["reduceOnly"]; {
	case !ok || v == "false":
	case v == "true":
		o.ReduceOnly = true
	default:
		return Order{}, mismatch("reduceOnly %q is neither true nor false", v)
	}
	if o.Timestamp, o.RecvWindow, err = times(p); err != nil {
		return Order{}, err
	}
	return o, nil
}

// CheckOrder checks a decoded order against the intent it was built for and
// the client order id it must carry: the symbol is venueSymbol and starts
// with the proposed one (BTC → BTCUSDT); the side is the proposal's; the type
// is MARKET for a market proposal and LIMIT for a limit one; the quantity is
// a decimal with 0 < quantity ≤ the proposed size (it is floored to the step,
// never rounded up); a limit price is within 1 % of the proposed price and on
// its passive side (at most for a buy, at least for a sell), and a market
// order carries none; reduceOnly is the proposal's.
func CheckOrder(o Order, in Intent, venueSymbol, clientOrderID string) error {
	if o.ClientOrderID != clientOrderID {
		return mismatch("newClientOrderId %q is not the expected %q", o.ClientOrderID, clientOrderID)
	}
	if o.Symbol != venueSymbol || !strings.HasPrefix(o.Symbol, strings.ToUpper(in.Symbol)) {
		return mismatch("symbol %q is not the proposed %s", o.Symbol, strings.ToUpper(in.Symbol))
	}
	if want := strings.ToUpper(in.Side); o.Side != want {
		return mismatch("side %q is not the proposed %s", o.Side, want)
	}
	wantType := "LIMIT"
	if in.OrderType == "market" {
		wantType = "MARKET"
	}
	if o.Type != wantType {
		return mismatch("type %q is not %s", o.Type, wantType)
	}
	qty, ok := Decimal(o.Quantity)
	size, sizeOK := Decimal(in.Size)
	switch {
	case !ok:
		return mismatch("quantity %q is not a decimal", o.Quantity)
	case !sizeOK:
		return fmt.Errorf("binance: the proposal's size %q is not a decimal", in.Size)
	case qty.Sign() <= 0:
		return mismatch("quantity %s is not positive", o.Quantity)
	case qty.Cmp(size) > 0:
		return mismatch("quantity %s is more than the proposed %s", o.Quantity, in.Size)
	}
	if wantType == "MARKET" {
		if o.Price != "" {
			return mismatch("a market order carries no price")
		}
	} else {
		price, ok := Decimal(o.Price)
		want, wantOK := Decimal(in.Price)
		switch {
		case !ok:
			return mismatch("price %q is not a decimal", o.Price)
		case !wantOK || want.Sign() <= 0:
			return fmt.Errorf("binance: the proposal's price %q is not a positive decimal", in.Price)
		case in.Side == "buy" && price.Cmp(want) > 0:
			return mismatch("buy price %s is above the proposed %s", o.Price, in.Price)
		case in.Side == "sell" && price.Cmp(want) < 0:
			return mismatch("sell price %s is below the proposed %s", o.Price, in.Price)
		case !withinOnePercent(price, want):
			return mismatch("price %s is more than 1%% from the proposed %s", o.Price, in.Price)
		}
	}
	if o.ReduceOnly != in.ReduceOnly {
		return mismatch("reduceOnly is %t, the proposal says %t", o.ReduceOnly, in.ReduceOnly)
	}
	return nil
}

// CheckRules checks what the engine checks of a message it did not build: the
// quantity a multiple of the step and at least the minimum, a limit price a
// multiple of the tick with GTC, a market order without timeInForce, and the
// RESULT response type the engine reads the ack from.
func CheckRules(o Order, r Rules) error {
	qty, _ := Decimal(o.Quantity)
	if qty == nil {
		return mismatch("quantity %q is not a decimal", o.Quantity)
	}
	if err := multipleOf("quantity", qty, o.Quantity, r.Step); err != nil {
		return err
	}
	if minQty, ok := Decimal(r.MinQty); ok && r.MinQty != "" && qty.Cmp(minQty) < 0 {
		return mismatch("quantity %s is below the minimum of %s", o.Quantity, r.MinQty)
	}
	switch o.Type {
	case "LIMIT":
		px, ok := Decimal(o.Price)
		if !ok {
			return mismatch("price %q is not a decimal", o.Price)
		}
		if err := multipleOf("price", px, o.Price, r.Tick); err != nil {
			return err
		}
		if o.TimeInForce != "GTC" {
			return mismatch("timeInForce %q is not GTC", o.TimeInForce)
		}
	case "MARKET":
		if o.TimeInForce != "" {
			return mismatch("a market order carries no timeInForce")
		}
	default:
		return mismatch("type %q is neither LIMIT nor MARKET", o.Type)
	}
	if o.RespType != RespType {
		return mismatch("newOrderRespType %q is not %s", o.RespType, RespType)
	}
	return nil
}

// CheckFresh checks that a message's timestamp is not in the future and its
// window has not passed: what Binance itself refuses, refused earlier.
func CheckFresh(ts time.Time, recvWindow int, now time.Time) error {
	if recvWindow <= 0 || recvWindow > 60000 {
		return mismatch("recvWindow %d is not between 1 and 60000", recvWindow)
	}
	if ts.After(now.Add(time.Second)) {
		return mismatch("timestamp %d is in the future", ts.UnixMilli())
	}
	if !now.Before(ts.Add(time.Duration(recvWindow) * time.Millisecond)) {
		return mismatch("the message's window closed at %s", ts.Add(time.Duration(recvWindow)*time.Millisecond).UTC().Format(time.RFC3339Nano))
	}
	return nil
}

// BuildCancel is the cancel parameter string for an order placed with
// clientOrderID: DELETE /fapi/v1/order by origClientOrderId.
func BuildCancel(symbol, clientOrderID string, now time.Time) ([]byte, error) {
	if symbol == "" || clientOrderID == "" {
		return nil, errors.New("binance: a cancel names the symbol and the client order id")
	}
	p := url.Values{
		"symbol": {strings.ToUpper(symbol)}, "origClientOrderId": {clientOrderID},
		"timestamp": {strconv.FormatInt(now.UnixMilli(), 10)}, "recvWindow": {strconv.Itoa(RecvWindow)},
	}
	return []byte(p.Encode()), nil
}

// DecodeCancel reads a cancel parameter string; like DecodeOrder it refuses
// any parameter outside the set.
func DecodeCancel(message []byte) (Cancel, error) {
	p, err := params(message, cancelParams)
	if err != nil {
		return Cancel{}, err
	}
	c := Cancel{Symbol: p["symbol"], ClientOrderID: p["origClientOrderId"]}
	if c.Symbol == "" || c.ClientOrderID == "" {
		return Cancel{}, mismatch("a cancel names symbol and origClientOrderId")
	}
	if c.Timestamp, c.RecvWindow, err = times(p); err != nil {
		return Cancel{}, err
	}
	return c, nil
}

// CheckRead checks that a message is a read and not an order or a cancel: a
// parameter string without any of the parameters that make one.
func CheckRead(message []byte) error {
	p, err := params(message, nil)
	if err != nil {
		return err
	}
	for _, k := range orderOnly {
		if _, ok := p[k]; ok {
			return mismatch("a read carries no order, but the message has %s", k)
		}
	}
	return nil
}

// ClientOrderID is a fresh client order id for an agent, in the shape the
// engine assigns and checks: msh-<agent id without its prefix>-<6 random
// base32 characters>.
func ClientOrderID(agentID string) string {
	return clientOrderPrefix(agentID) + randomBase32(6)
}

// IsClientOrderIDOf reports whether id is in the shape ClientOrderID makes for agentID.
func IsClientOrderIDOf(id, agentID string) bool {
	rest, ok := strings.CutPrefix(id, clientOrderPrefix(agentID))
	if !ok || len(rest) != 6 || len(id) > 36 {
		return false
	}
	for _, r := range rest {
		if !strings.ContainsRune(base32, r) {
			return false
		}
	}
	return true
}

func clientOrderPrefix(agentID string) string {
	short := agentID
	if i := strings.IndexByte(short, '_'); i >= 0 {
		short = short[i+1:]
	}
	return "msh-" + short + "-"
}

const base32 = "abcdefghijklmnopqrstuvwxyz234567"

func randomBase32(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("binance: no randomness: " + err.Error())
	}
	for i := range b {
		b[i] = base32[int(b[i])%len(base32)]
	}
	return string(b)
}

// params decodes a parameter string as net/url does: a parameter given twice,
// or one outside allowed (when allowed is not nil), is a Mismatch.
func params(message []byte, allowed map[string]bool) (map[string]string, error) {
	if len(message) == 0 {
		return nil, mismatch("the message is empty")
	}
	values, err := url.ParseQuery(string(message))
	if err != nil {
		return nil, mismatch("the message is not a parameter string: %v", err)
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		if allowed != nil && !allowed[k] {
			return nil, mismatch("unexpected parameter %s", k)
		}
		if len(v) != 1 {
			return nil, mismatch("parameter %s is given %d times", k, len(v))
		}
		out[k] = v[0]
	}
	return out, nil
}

// times reads the timestamp and recvWindow every signed request carries.
func times(p map[string]string) (time.Time, int, error) {
	ms, err := strconv.ParseInt(p["timestamp"], 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, 0, mismatch("timestamp %q is not milliseconds since the epoch", p["timestamp"])
	}
	window := 5000 // Binance's default when the parameter is absent
	if v, ok := p["recvWindow"]; ok {
		if window, err = strconv.Atoi(v); err != nil {
			return time.Time{}, 0, mismatch("recvWindow %q is not an integer", v)
		}
	}
	return time.UnixMilli(ms).UTC(), window, nil
}

// Decimal reads a contract Decimal exactly, as a rational; no float.
func Decimal(s string) (*big.Rat, bool) {
	if !proposal.IsDecimal(s) {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}

// optionalRule parses a rule that may be absent (nil then).
func optionalRule(name, v string) (*big.Rat, error) {
	if v == "" {
		return nil, nil
	}
	d, ok := Decimal(v)
	if !ok || d.Sign() < 0 {
		return nil, fmt.Errorf("binance: the %s %q is not a decimal", name, v)
	}
	if d.Sign() == 0 {
		return nil, nil
	}
	return d, nil
}

// floorTo rounds x down to a multiple of step (x itself without a step).
func floorTo(x, step *big.Rat) *big.Rat {
	if step == nil {
		return x
	}
	q := new(big.Rat).Quo(x, step)
	n := new(big.Int).Quo(q.Num(), q.Denom()) // positive x: truncation is floor
	return new(big.Rat).Mul(new(big.Rat).SetInt(n), step)
}

// ceilTo rounds x up to a multiple of step.
func ceilTo(x, step *big.Rat) *big.Rat {
	if step == nil {
		return x
	}
	q := new(big.Rat).Quo(x, step)
	n, m := new(big.Int).QuoRem(q.Num(), q.Denom(), new(big.Int))
	if m.Sign() != 0 {
		n.Add(n, big.NewInt(1))
	}
	return new(big.Rat).Mul(new(big.Rat).SetInt(n), step)
}

// format renders x with as many decimals as the rule has (trailing zeros of
// the rule ignored: "0.10" → 1); x is a multiple of the rule, so it is exact.
func format(x *big.Rat, rule string) string {
	places := 0
	if i := strings.IndexByte(rule, '.'); i >= 0 {
		places = len(strings.TrimRight(rule[i+1:], "0"))
	}
	if rule == "" {
		// No rule: as few decimals as the value needs, up to 18.
		s := x.FloatString(18)
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		return s
	}
	return x.FloatString(places)
}

// multipleOf checks that x is a whole number of rule (no rule: anything goes).
func multipleOf(name string, x *big.Rat, raw, rule string) error {
	r, ok := Decimal(rule)
	if rule == "" || !ok || r.Sign() <= 0 {
		return nil
	}
	if !new(big.Rat).Quo(x, r).IsInt() {
		return mismatch("%s %s is not a multiple of %s", name, raw, rule)
	}
	return nil
}

// withinOnePercent reports |price − want| ≤ want / 100.
func withinOnePercent(price, want *big.Rat) bool {
	diff := new(big.Rat).Sub(price, want)
	diff.Abs(diff)
	diff.Mul(diff, big.NewRat(100, 1))
	return diff.Cmp(want) <= 0
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}
