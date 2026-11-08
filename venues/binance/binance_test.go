package binance

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

var btc = Rules{Tick: "0.10", Step: "0.001", MinQty: "0.001", MinNotional: "100"}

func limitBuy() Intent {
	return Intent{Symbol: "BTCUSDT", Side: "buy", OrderType: "limit", Size: "0.5009", Price: "60000.55"}
}

// TestBuildOrder pins the bytes: the keys sorted as net/url encodes them, the
// size floored to the step, the price to the tick on the passive side.
func TestBuildOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		in   Intent
		want string
	}{
		{"limit buy: size floored, price down to the tick", limitBuy(),
			"newClientOrderId=msh-a-abcdef&newOrderRespType=RESULT&price=60000.5&quantity=0.500&recvWindow=10000&side=BUY&symbol=BTCUSDT&timeInForce=GTC&timestamp=1793178000000&type=LIMIT"},
		{"limit sell: price up to the tick", Intent{Symbol: "btcusdt", Side: "sell", OrderType: "limit", Size: "1", Price: "60000.51"},
			"newClientOrderId=msh-a-abcdef&newOrderRespType=RESULT&price=60000.6&quantity=1.000&recvWindow=10000&side=SELL&symbol=BTCUSDT&timeInForce=GTC&timestamp=1793178000000&type=LIMIT"},
		{"market reduce-only: no price, no timeInForce", Intent{Symbol: "BTCUSDT", Side: "sell", OrderType: "market", Size: "0.002", ReduceOnly: true},
			"newClientOrderId=msh-a-abcdef&newOrderRespType=RESULT&quantity=0.002&recvWindow=10000&reduceOnly=true&side=SELL&symbol=BTCUSDT&timestamp=1793178000000&type=MARKET"},
	} {
		got, err := BuildOrder(c.in, btc, "msh-a-abcdef", now)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	for _, c := range []struct {
		name string
		in   Intent
		id   string
	}{
		{"a market order that is not an exit: the engine bounds it", Intent{Symbol: "BTCUSDT", Side: "buy", OrderType: "market", Size: "1"}, "msh-a-abcdef"},
		{"below the step", Intent{Symbol: "BTCUSDT", Side: "buy", OrderType: "market", Size: "0.0009"}, "msh-a-abcdef"},
		{"no side", Intent{Symbol: "BTCUSDT", OrderType: "market", Size: "1"}, "msh-a-abcdef"},
		{"limit without a price", Intent{Symbol: "BTCUSDT", Side: "buy", OrderType: "limit", Size: "1"}, "msh-a-abcdef"},
		{"a float size", Intent{Symbol: "BTCUSDT", Side: "buy", OrderType: "market", Size: "1e-3"}, "msh-a-abcdef"},
		{"no client order id", limitBuy(), ""},
		{"a client order id too long", limitBuy(), strings.Repeat("x", 37)},
	} {
		if _, err := BuildOrder(c.in, btc, c.id, now); err == nil || IsMismatch(err) {
			t.Errorf("%s: %v, want an input error", c.name, err)
		}
	}
}

// TestBuiltOrderPassesEveryCheck: what the signer builds, the signer's own
// check and the engine's rule check both accept.
func TestBuiltOrderPassesEveryCheck(t *testing.T) {
	for _, in := range []Intent{limitBuy(), {Symbol: "BTCUSDT", Side: "sell", OrderType: "market", Size: "0.25", ReduceOnly: true}} {
		msg, err := BuildOrder(in, btc, "msh-a-abcdef", now)
		if err != nil {
			t.Fatal(err)
		}
		o, err := DecodeOrder(msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckOrder(o, in, "BTCUSDT", "msh-a-abcdef"); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
		if err := CheckRules(o, btc); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
		if err := CheckFresh(o.Timestamp, o.RecvWindow, now.Add(9*time.Second)); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

func TestCheckOrderRefusesWhatTheProposalDidNotSay(t *testing.T) {
	base := "newClientOrderId=msh-a-abcdef&newOrderRespType=RESULT&price=60000.5&quantity=0.500&recvWindow=10000&side=BUY&symbol=BTCUSDT&timeInForce=GTC&timestamp=1793178000000&type=LIMIT"
	for _, c := range []struct {
		name, from, to string
	}{
		{"another client order id", "msh-a-abcdef", "msh-b-abcdef"},
		{"another symbol", "symbol=BTCUSDT", "symbol=ETHUSDT"},
		{"the other side", "side=BUY", "side=SELL"},
		{"a market type", "type=LIMIT", "type=MARKET"},
		{"more than proposed", "quantity=0.500", "quantity=0.501"},
		{"a zero quantity", "quantity=0.500", "quantity=0"},
		{"a buy above the proposed price", "price=60000.5", "price=60000.6"},
		{"more than 1 % away", "price=60000.5", "price=59000"},
		{"reduce-only the proposal did not ask for", "recvWindow=10000", "recvWindow=10000&reduceOnly=true"},
	} {
		o, err := DecodeOrder([]byte(strings.Replace(base, c.from, c.to, 1)))
		if err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		if err := CheckOrder(o, limitBuy(), "BTCUSDT", "msh-a-abcdef"); !IsMismatch(err) {
			t.Errorf("%s: %v, want a mismatch", c.name, err)
		}
	}
	for _, c := range []struct{ name, msg string }{
		{"an unknown parameter", base + "&stopPrice=1"},
		{"a parameter twice", base + "&side=SELL"},
		{"reduceOnly neither true nor false", base + "&reduceOnly=yes"},
		{"no timestamp", strings.Replace(base, "&timestamp=1793178000000", "", 1)},
		{"empty", ""},
	} {
		if _, err := DecodeOrder([]byte(c.msg)); !IsMismatch(err) {
			t.Errorf("%s: %v, want a mismatch", c.name, err)
		}
	}
}

// TestCheckOrderMarketProposal: a market proposal goes out either as a true
// MARKET order (an exit, or an older engine) or as the engine's LIMIT IOC at
// its bound, whose price the signer does not judge; anything else is refused.
func TestCheckOrderMarketProposal(t *testing.T) {
	in := Intent{Symbol: "BTC", Side: "buy", OrderType: "market", Size: "0.5"}
	const ioc = "newClientOrderId=msh-a-abcdef&newOrderRespType=RESULT&price=60600.5&quantity=0.500&recvWindow=10000&side=BUY&symbol=BTCUSDT&timeInForce=IOC&timestamp=1793178000000&type=LIMIT"
	const market = "newClientOrderId=msh-a-abcdef&newOrderRespType=RESULT&quantity=0.500&recvWindow=10000&side=BUY&symbol=BTCUSDT&timestamp=1793178000000&type=MARKET"
	for _, c := range []struct{ name, msg string }{
		{"a market order", market},
		{"an IOC limit at the engine's bound", ioc},
		{"an IOC limit at a bound far from anything", strings.Replace(ioc, "price=60600.5", "price=1", 1)},
	} {
		o, err := DecodeOrder([]byte(c.msg))
		if err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		if err := CheckOrder(o, in, "BTCUSDT", "msh-a-abcdef"); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if err := CheckRules(o, btc); err != nil {
			t.Errorf("%s: rules: %v", c.name, err)
		}
	}
	for _, c := range []struct{ name, msg string }{
		{"a resting limit", strings.Replace(ioc, "timeInForce=IOC", "timeInForce=GTC", 1)},
		{"a fill-or-kill limit", strings.Replace(ioc, "timeInForce=IOC", "timeInForce=FOK", 1)},
		{"a limit without a price", strings.Replace(ioc, "price=60600.5&", "", 1)},
		{"a limit at a zero bound", strings.Replace(ioc, "price=60600.5", "price=0", 1)},
		{"a market order with a price", strings.Replace(market, "quantity=0.500", "price=60600.5&quantity=0.500", 1)},
		{"a market order with a timeInForce", strings.Replace(market, "symbol=BTCUSDT", "symbol=BTCUSDT&timeInForce=IOC", 1)},
		{"a stop", strings.Replace(ioc, "type=LIMIT", "type=STOP", 1)},
	} {
		o, err := DecodeOrder([]byte(c.msg))
		if err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		if err := CheckOrder(o, in, "BTCUSDT", "msh-a-abcdef"); !IsMismatch(err) {
			t.Errorf("%s: %v, want a mismatch", c.name, err)
		}
	}
}

func TestCheckRules(t *testing.T) {
	base := "newClientOrderId=msh-a-abcdef&newOrderRespType=RESULT&price=60000.5&quantity=0.500&recvWindow=10000&side=BUY&symbol=BTCUSDT&timeInForce=GTC&timestamp=1793178000000&type=LIMIT"
	for _, c := range []struct{ name, from, to string }{
		{"off the step", "quantity=0.500", "quantity=0.5005"},
		{"off the tick", "price=60000.5", "price=60000.55"},
		{"below the minimum", "quantity=0.500", "quantity=0.000"},
		{"neither GTC nor IOC", "timeInForce=GTC", "timeInForce=FOK"},
		{"no RESULT", "newOrderRespType=RESULT&", ""},
	} {
		o, err := DecodeOrder([]byte(strings.Replace(base, c.from, c.to, 1)))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := CheckRules(o, btc); !IsMismatch(err) {
			t.Errorf("%s: %v, want a mismatch", c.name, err)
		}
	}
}

func TestCheckFresh(t *testing.T) {
	ts := now
	if err := CheckFresh(ts, RecvWindow, now.Add(10*time.Second)); !IsMismatch(err) {
		t.Errorf("a closed window: %v", err)
	}
	if err := CheckFresh(ts, RecvWindow, now.Add(-2*time.Second)); !IsMismatch(err) {
		t.Errorf("a future timestamp: %v", err)
	}
	if err := CheckFresh(ts, 0, now); !IsMismatch(err) {
		t.Errorf("no window: %v", err)
	}
}

func TestCancel(t *testing.T) {
	msg, err := BuildCancel("btcusdt", "msh-a-abcdef", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := "origClientOrderId=msh-a-abcdef&recvWindow=10000&symbol=BTCUSDT&timestamp=1793178000000"; string(msg) != want {
		t.Fatalf("got %s\nwant %s", msg, want)
	}
	c, err := DecodeCancel(msg)
	if err != nil || c.Symbol != "BTCUSDT" || c.ClientOrderID != "msh-a-abcdef" || !c.Timestamp.Equal(now) || c.RecvWindow != RecvWindow {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := DecodeCancel([]byte(string(msg) + "&orderId=1")); !IsMismatch(err) {
		t.Errorf("a cancel by venue order id: %v", err)
	}
	if err := CheckRead(msg); !IsMismatch(err) {
		t.Errorf("a cancel passed as a read: %v", err)
	}
	if err := CheckRead([]byte("recvWindow=10000&timestamp=1793178000000")); err != nil {
		t.Errorf("a read: %v", err)
	}
}

func TestClientOrderID(t *testing.T) {
	id := ClientOrderID("agt_gqmhszuwurpr")
	if !strings.HasPrefix(id, "msh-gqmhszuwurpr-") || len(id) != len("msh-gqmhszuwurpr-")+6 || !IsClientOrderIDOf(id, "agt_gqmhszuwurpr") {
		t.Fatalf("%q", id)
	}
	for _, other := range []string{"msh-other-abcdef", "msh-gqmhszuwurpr-ABCDEF", "msh-gqmhszuwurpr-abcde", "msh-gqmhszuwurpr-abcdef1"} {
		if IsClientOrderIDOf(other, "agt_gqmhszuwurpr") {
			t.Errorf("%q passed as the agent's", other)
		}
	}
}

func TestWithinOnePercent(t *testing.T) {
	cases := []struct {
		price, want string
		ok          bool
	}{
		{"60000", "60000", true}, {"59400", "60000", true}, {"60600", "60000", true},
		{"59399.999999", "60000", false}, {"60600.0001", "60000", false}, {"0.000999", "0.001", true}, {"0.00099", "0.001", true}, {"0.000989", "0.001", false},
	}
	for _, tc := range cases {
		p, _ := Decimal(tc.price)
		w, _ := Decimal(tc.want)
		if got := withinOnePercent(p, w); got != tc.ok {
			t.Errorf("%s within 1%% of %s: %v, want %v", tc.price, tc.want, got, tc.ok)
		}
	}
	if _, ok := Decimal("1e3"); ok {
		t.Fatal("an exponent was accepted")
	}
	if _, ok := Decimal(".5"); ok {
		t.Fatal("a bare fraction was accepted")
	}
}
