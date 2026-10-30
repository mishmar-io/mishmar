package hyperliquid

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Side of an order.
type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

// TIF is a limit order's time in force.
type TIF string

const (
	Gtc TIF = "Gtc" // good till cancelled
	Ioc TIF = "Ioc" // immediate or cancel: the unfilled part is cancelled
	Alo TIF = "Alo" // add liquidity only: cancelled instead of matching
)

// Tick and lot rules (docs: tick-and-lot-size). A price has at most
// MaxSignificantFigures significant figures and at most PerpMaxDecimals -
// szDecimals decimal places; integer prices are always allowed, whatever
// their number of digits. A size is rounded to szDecimals. Trailing zeros are
// stripped on the wire, since the signed bytes carry the string.
const (
	MaxSignificantFigures = 5
	PerpMaxDecimals       = 6
)

// FormatPrice rounds a perp price to the tick rules in the direction that
// cannot hurt the trader, buys down and sells up, so the order is never more
// aggressive than asked, and formats it for the wire. It refuses prices that
// are zero, negative or that round to zero.
func FormatPrice(d decimal.Decimal, szDecimals int, side Side) (string, error) {
	if side != Buy && side != Sell {
		return "", fmt.Errorf("hyperliquid: side %q is not buy or sell", side)
	}
	if !d.IsPositive() {
		return "", fmt.Errorf("hyperliquid: price %s must be positive", d.String())
	}
	maxDecimals := PerpMaxDecimals - szDecimals
	if maxDecimals < 0 {
		maxDecimals = 0
	}
	places := int32(maxDecimals)
	if !d.IsInteger() {
		// floor(log10 d) + 1 integer digits take part of the significance budget.
		magnitude := int32(d.NumDigits()) - 1 + d.Exponent()
		sigPlaces := MaxSignificantFigures - (magnitude + 1)
		if sigPlaces < 0 {
			sigPlaces = 0 // the integer part alone exceeds the budget; integers are always allowed
		}
		if sigPlaces < places {
			places = sigPlaces
		}
	}
	var r decimal.Decimal
	if side == Buy {
		r = d.RoundDown(places)
	} else {
		r = d.RoundUp(places)
	}
	if !r.IsPositive() {
		return "", fmt.Errorf("hyperliquid: price %s rounds to zero at %d decimals", d.String(), places)
	}
	return r.String(), nil
}

// FormatSize rounds a size down to szDecimals, never trading more than asked,
// and refuses sizes that are zero, negative or that round to zero.
func FormatSize(d decimal.Decimal, szDecimals int) (string, error) {
	if szDecimals < 0 {
		return "", fmt.Errorf("hyperliquid: szDecimals %d is negative", szDecimals)
	}
	if !d.IsPositive() {
		return "", fmt.Errorf("hyperliquid: size %s must be positive", d.String())
	}
	r := d.RoundDown(int32(szDecimals))
	if !r.IsPositive() {
		return "", fmt.Errorf("hyperliquid: size %s rounds to zero at %d decimals", d.String(), szDecimals)
	}
	return r.String(), nil
}
