// Package perps is perpetual-futures math: size, liquidation, funding accrual.
package perps

import "strings"

// Dir is +1 for long, −1 for short.
func Dir(kind string) float64 {
	switch strings.ToLower(kind) {
	case "short":
		return -1
	default:
		return 1
	}
}

// Size is notional in USD: collateral * leverage.
func Size(collateral, leverage float64) float64 {
	return collateral * leverage
}

// RawPnL is directional P/L before funding: size * (mark/entry - 1) * dir.
func RawPnL(size, mark, entry, dir float64) float64 {
	if entry == 0 {
		return 0
	}
	return size * (mark/entry - 1) * dir
}

// Liquidation is the price at which 90% of collateral is lost:
//
//	entry * (1 - dir*(0.9/leverage))
func Liquidation(entry, leverage, dir float64) float64 {
	if leverage == 0 {
		return 0
	}
	return entry * (1 - dir*(0.9/leverage))
}

// FundingPerDay is −size * funding8h * 3 * dir (three 8h windows per day).
// Positive means the position receives funding.
func FundingPerDay(size, funding8h, dir float64) float64 {
	return -size * funding8h * 3 * dir
}

// Equity is collateral + raw P/L + cumulative funding.
func Equity(collateral, raw, funding float64) float64 {
	return collateral + raw + funding
}

// Liquidated reports whether equity has fallen to ≤ 10% of collateral.
func Liquidated(equity, collateral float64) bool {
	return equity <= 0.1*collateral
}

// OpenFee is 6 bps of notional.
func OpenFee(size float64) float64 {
	return size * 0.0006
}

// ImpactPct is a toy price-impact model: min(0.9, size/oi * 45).
func ImpactPct(size, openInterest float64) float64 {
	if openInterest <= 0 {
		return 0
	}
	v := size / openInterest * 45
	if v > 0.9 {
		return 0.9
	}
	return v
}
