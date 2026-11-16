package hyperliquid

import (
	"strings"
	"testing"
)

const testMeta = `{"universe":[
	{"name":"BTC","szDecimals":5,"maxLeverage":40},
	{"name":"ETH","szDecimals":4,"maxLeverage":25},
	{"name":"OLD","szDecimals":1,"maxLeverage":3,"isDelisted":true},
	{"name":"kPEPE","szDecimals":0,"maxLeverage":10}
],"marginTables":[]}`

func TestParseMarketsTakesTheIndexFromThePosition(t *testing.T) {
	m, err := ParseMarkets([]byte(testMeta))
	if err != nil {
		t.Fatal(err)
	}
	want := Markets{
		{Name: "BTC", Rules: Rules{AssetIndex: 0, SizeDecimals: 5}},
		{Name: "ETH", Rules: Rules{AssetIndex: 1, SizeDecimals: 4}},
		{Name: "OLD", Rules: Rules{AssetIndex: 2, SizeDecimals: 1}, Delisted: true},
		{Name: "kPEPE", Rules: Rules{AssetIndex: 3, SizeDecimals: 0}},
	}
	if len(m) != len(want) {
		t.Fatalf("got %d markets, want %d", len(m), len(want))
	}
	for i := range want {
		if m[i] != want[i] {
			t.Errorf("market %d: got %+v, want %+v", i, m[i], want[i])
		}
	}
}

func TestParseMarketsRefusesAListItCannotTrust(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":          `<html>rate limited</html>`,
		"no universe":       `{}`,
		"empty universe":    `{"universe":[]}`,
		"a list, not meta":  `[{"name":"BTC","szDecimals":5}]`,
		"no name":           `{"universe":[{"name":"BTC","szDecimals":5},{"szDecimals":4}]}`,
		"no szDecimals":     `{"universe":[{"name":"BTC"}]}`,
		"negative decimals": `{"universe":[{"name":"BTC","szDecimals":-1}]}`,
		"decimals as text":  `{"universe":[{"name":"BTC","szDecimals":"5"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if m, err := ParseMarkets([]byte(body)); err == nil {
				t.Fatalf("accepted: %+v", m)
			}
		})
	}
}

func TestFindNamesOneMarketOrNone(t *testing.T) {
	m, err := ParseMarkets([]byte(testMeta))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		symbol string
		index  int
	}{{"BTC", 0}, {"ETH", 1}, {"eth", 1}, {"kPEPE", 3}, {"KPEPE", 3}, {"OLD", 2}} {
		got, err := m.Find(tc.symbol)
		if err != nil || got.Rules.AssetIndex != tc.index {
			t.Errorf("Find(%q) = %+v, %v; want index %d", tc.symbol, got, err, tc.index)
		}
	}
	for _, symbol := range []string{"", "DOGE", "BTCUSDT", "BT", "@1", "PURR/USDC", "xyz:BTC", "4"} {
		if got, err := m.Find(symbol); err == nil {
			t.Errorf("Find(%q) = %+v, want an error", symbol, got)
		}
	}

	// Two names that differ only in case: the exact one is found, a third
	// spelling is refused.
	two := append(Markets{}, m...)
	two = append(two, Market{Name: "KPEPE", Rules: Rules{AssetIndex: 4}})
	if got, err := two.Find("kPEPE"); err != nil || got.Rules.AssetIndex != 3 {
		t.Errorf("exact kPEPE: %+v, %v", got, err)
	}
	if got, err := two.Find("KPEPE"); err != nil || got.Rules.AssetIndex != 4 {
		t.Errorf("exact KPEPE: %+v, %v", got, err)
	}
	if got, err := two.Find("kpepe"); err == nil || !strings.Contains(err.Error(), "differ only in letter case") {
		t.Errorf("kpepe: %+v, %v; want a refusal that names the case", got, err)
	}
}

func TestInfoURLIsTheNetworksOwn(t *testing.T) {
	if got := Mainnet.InfoURL(); got != "https://api.hyperliquid.xyz/info" {
		t.Errorf("mainnet: %s", got)
	}
	if got := Testnet.InfoURL(); got != "https://api.hyperliquid-testnet.xyz/info" {
		t.Errorf("testnet: %s", got)
	}
}
