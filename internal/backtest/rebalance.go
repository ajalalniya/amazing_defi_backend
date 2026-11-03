package backtest

import (
	"math"

	"amazing/internal/clmath"
)

// lpBook is an LP leg's live position: its tick-snapped range and liquidity.
// It changes only when a rebalancing leg recentres.
type lpBook struct {
	tickLower, tickUpper int
	lower, upper, L      float64
}

// openLP deploys value (token1 units) at p over [lower, upper] snapped to the
// pool's ticks.
func openLP(p, value, lower, upper float64, spacing, dec0, dec1 int) lpBook {
	tl, tu, lo, hi := clmath.SnapRange(lower, upper, spacing, dec0, dec1)
	_, _, L := clmath.DepositAmounts(p, value, lo, hi)
	return lpBook{tickLower: tl, tickUpper: tu, lower: lo, upper: hi, L: L}
}

func (b lpBook) value(p float64) float64 {
	return clmath.Value(p, b.lower, b.upper, b.L)
}

func (b lpBook) amount0(p float64) float64 {
	a0, _ := clmath.Amounts(p, b.lower, b.upper, b.L)
	return a0
}

// recenterRange is the range with upper/lower == ratio and p at its
// arithmetic midpoint: lower = 2p/(1+ratio), upper = ratio*lower.
func recenterRange(p, ratio float64) (lower, upper float64) {
	lower = 2 * p / (1 + ratio)
	return lower, ratio * lower
}

// recenter closes b at p and reopens everything it was worth over a range
// with the same upper/lower ratio, centred on p. Out of range the old
// position is all one token, so the new mix is bought through the pool: the
// token0 swapped pays feeRate, returned as cost in token1 units and taken out
// of the redeposited value.
func (b lpBook) recenter(p, ratio, feeRate float64, spacing, dec0, dec1 int) (lpBook, float64) {
	value := b.value(p)
	lo, hi := recenterRange(p, ratio)
	nb := openLP(p, value, lo, hi, spacing, dec0, dec1)
	cost := math.Abs(nb.amount0(p)-b.amount0(p)) * p * feeRate
	return openLP(p, value-cost, lo, hi, spacing, dec0, dec1), cost
}

// rangeRatio is upper/lower of the leg's requested range, kept across
// rebalances so tick snapping never compounds into a drifting width.
func rangeRatio(l Leg) float64 {
	if l.Lower <= 0 || l.Upper <= l.Lower {
		return 1
	}
	return l.Upper / l.Lower
}

// newLPResult allocates an LP leg's per-day series.
func newLPResult(l Leg, asset string, days int) LegResult {
	n := days + 1
	out := LegResult{
		Kind: "lp", Capital: l.Capital, Series: make([]float64, n), Fees: make([]float64, n),
		asset: asset, exposure: make([]float64, n), value: make([]float64, n), resets: make([]bool, n),
		books: make([]lpBook, n), price: make([]float64, n),
	}
	if l.Rebalance {
		out.RangeLower, out.RangeUpper = make([]float64, n), make([]float64, n)
	}
	return out
}

// settleLPDay closes day d for an LP leg at price p: a rebalancing leg whose
// close is outside its range is recentred first (never on the last day, when
// there is nothing left to earn), then the day's position value, equity and
// token0 exposure are recorded. q converts token1 to USD.
func settleLPDay(out *LegResult, l Leg, book lpBook, d, days int, p, q, fees, feeRate float64, spacing, dec0, dec1 int) lpBook {
	exited := p > book.upper || (p < book.lower && !l.RebalanceSkipBelow)
	if l.Rebalance && d > 0 && d < days && exited {
		nb, cost := book.recenter(p, rangeRatio(l), feeRate, spacing, dec0, dec1)
		book = nb
		out.Rebalances++
		out.RebalanceCost += cost * q
		out.resets[d] = true
		out.RebalanceDays = append(out.RebalanceDays, d)
	}
	out.books[d] = book
	out.price[d] = p
	out.value[d] = book.value(p) * q
	out.Fees[d] = fees
	out.Series[d] = out.value[d] + fees
	out.exposure[d] = book.amount0(p) * p * q
	if l.Rebalance {
		out.RangeLower[d], out.RangeUpper[d] = book.lower, book.upper
	}
	return book
}
