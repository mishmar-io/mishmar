package hyperliquid

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The public info endpoint of each network. The signer reads the list of
// perp markets from here itself, so the address is part of the program: no
// setting, no environment variable and nothing the engine sends changes it.
const (
	mainnetInfoURL = "https://api.hyperliquid.xyz/info"
	testnetInfoURL = "https://api.hyperliquid-testnet.xyz/info"
)

// InfoURL is the network's public info endpoint.
func (n Network) InfoURL() string {
	if n == Mainnet {
		return mainnetInfoURL
	}
	return testnetInfoURL
}

// MarketsRequest is the body that asks the info endpoint for the list of
// perp markets.
const MarketsRequest = `{"type":"meta"}`

// Market is one perp market as Hyperliquid lists it: the coin's name, its
// rules (the asset index is its position in the list) and whether the venue
// marks it delisted.
type Market struct {
	Name     string
	Rules    Rules
	Delisted bool
}

// Markets is Hyperliquid's list of perp markets, in the venue's order. It
// holds the venue's own perps only: spot pairs and markets deployed by third
// parties are not in it, so they have no asset index here.
type Markets []Market

// ParseMarkets reads the info endpoint's answer to MarketsRequest. An answer
// with no markets, a market with no name or negative size decimals is
// refused whole: a list that cannot be trusted in one place is not used.
func ParseMarkets(body []byte) (Markets, error) {
	var meta struct {
		Universe []struct {
			Name       string `json:"name"`
			SzDecimals *int   `json:"szDecimals"`
			IsDelisted bool   `json:"isDelisted"`
		} `json:"universe"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("hyperliquid: the market list is not the JSON expected: %w", err)
	}
	if len(meta.Universe) == 0 {
		return nil, errors.New("hyperliquid: the market list is empty")
	}
	out := make(Markets, len(meta.Universe))
	for i, a := range meta.Universe {
		switch {
		case a.Name == "":
			return nil, fmt.Errorf("hyperliquid: market %d in the list has no name", i)
		case a.SzDecimals == nil || *a.SzDecimals < 0:
			return nil, fmt.Errorf("hyperliquid: %s in the market list has no valid szDecimals", a.Name)
		}
		out[i] = Market{Name: a.Name, Rules: Rules{AssetIndex: i, SizeDecimals: *a.SzDecimals}, Delisted: a.IsDelisted}
	}
	return out, nil
}

// Find returns the market a proposal's symbol names. The name must match
// exactly; a name that differs only in letter case is taken when exactly one
// market matches it that way (the venue lists kPEPE, a proposal may say
// KPEPE), and refused when two do.
func (m Markets) Find(symbol string) (Market, error) {
	var loose []Market
	for _, c := range m {
		if c.Name == symbol {
			return c, nil
		}
		if strings.EqualFold(c.Name, symbol) {
			loose = append(loose, c)
		}
	}
	switch len(loose) {
	case 1:
		return loose[0], nil
	case 0:
		return Market{}, fmt.Errorf("hyperliquid: %s is not in Hyperliquid's list of perp markets", symbol)
	default:
		return Market{}, fmt.Errorf("hyperliquid: %s matches %d markets in Hyperliquid's list that differ only in letter case (%s, %s); name one exactly", symbol, len(loose), loose[0].Name, loose[1].Name)
	}
}
