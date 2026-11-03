package backtest

import (
	"log/slog"
	"math"
	"strings"

	"amazing/internal/market"
	"amazing/internal/perps"
)

// hedgeIntervals are the re-hedge intervals a leg may ask for, in hours; each
// divides a day, so every daily close is also a check.
var hedgeIntervals = map[int]bool{1: true, 2: true, 3: true, 4: true, 6: true, 8: true, 12: true, 24: true}

func hedgeInterval(l Leg) int {
	if hedgeIntervals[l.HedgeIntervalHours] {
		return l.HedgeIntervalHours
	}
	return HoursPerDay
}

// lpSide is the LP legs a managed perp follows: their summed delta (USD of
// token0 held) and value at any hourly step, from each leg's position and
// hourly pool price.
type lpSide struct {
	legs   []*LegResult
	hourly [][]float64
}

func (s lpSide) bookAt(r *LegResult, j int) lpBook {
	if j <= 0 {
		return r.books[0]
	}
	return r.books[(j-1)/HoursPerDay]
}

func (s lpSide) at(j int) (delta, value float64) {
	for i, r := range s.legs {
		p := s.hourly[i][j]
		b := s.bookAt(r, j)
		delta += b.amount0(p) * p * r.q
		value += b.value(p) * r.q
	}
	return delta, value
}

// applyManagedPerps re-simulates the perp legs whose size is driven by the LP
// legs on the same underlying, after every leg has run so the LP series
// exist. A perp is managed when it is a hedge short, or when one of those LP
// legs rebalanced: on each recentre the perp is closed and reopened with it.
// Any other perp keeps the static result simulateLeg produced.
func applyManagedPerps(legs []Leg, results []LegResult, legRel, legHourly [][]float64, days int, perpBy map[string]market.Perp, log *slog.Logger) {
	n := days + 1
	for i, l := range legs {
		kind := strings.ToLower(l.Kind)
		if kind == "lp" {
			continue
		}
		hedge := l.Hedge && kind == "short"
		m := perpBy[l.PerpID]
		value, resets := make([]float64, n), make([]bool, n)
		exposure := make([]float64, n)
		side := lpSide{}
		lpCapital := 0.0
		reset := false
		for k := range results {
			r := &results[k]
			if r.Kind != "lp" || r.asset != m.Base {
				continue
			}
			side.legs = append(side.legs, r)
			side.hourly = append(side.hourly, hourlyOrFlat(legHourly[k], r, days))
			lpCapital += r.Capital
			for d := 0; d < n && d < len(r.exposure); d++ {
				exposure[d] += r.exposure[d]
				value[d] += r.value[d]
				if r.resets[d] {
					resets[d], reset = true, true
				}
			}
		}
		if hedge && len(side.legs) == 0 {
			log.Warn("hedge leg has no LP leg on its underlying; running it as a static short", "perp", l.PerpID, "base", m.Base)
			continue
		}
		if !hedge && !reset {
			continue
		}
		// A hedge reopens at the LP's token0 exposure. A fixed-size perp keeps
		// the notional-to-LP-value ratio it opened with, so each reopen sizes
		// it to the LP's value at that close.
		target := exposure
		if !hedge {
			size := l.Capital * l.Leverage
			target = make([]float64, n)
			for d := range target {
				if lpCapital > 0 {
					target[d] = size * value[d] / lpCapital
				}
			}
			target[0] = size
		}
		relH := legHourly[i]
		if len(relH) != days*HoursPerDay+1 {
			rel := legRel[i]
			if rel == nil {
				rel = ones(n)
			}
			relH = flatHourly(rel)
		}
		results[i] = simulateManagedPerp(l, m, relH, target, resets, hedge, side, days)
	}
}

// hourlyOrFlat is an LP leg's hourly pool price, or its daily closes held
// flat through each day when no hourly path is available.
func hourlyOrFlat(h []float64, r *LegResult, days int) []float64 {
	if len(h) == days*HoursPerDay+1 && h[0] > 0 {
		return h
	}
	return flatHourly(r.price)
}

// simulateManagedPerp walks a perp hour by hour along relH. Collateral is
// fixed at l.Capital; P/L, funding and fees stay in the leg's equity, so its
// effective leverage floats. Liquidation is checked every hour.
//
//   - At a reset close (an LP leg recentred) the position is closed and
//     reopened at target[d], paying perps.OpenFee on both trades.
//   - A hedge is checked against the LP's delta every hedgeInterval hours and
//     traded to it, paying perps.OpenFee on the difference, unless it is
//     within HedgeBandPct of the LP's value. Between re-hedges its notional
//     drifts with price while the LP's delta moves the other way: that gap
//     is the LP's convexity (volatility cost) the hedge cannot remove.
//   - Otherwise the position is left alone, like any fixed-size perp.
func simulateManagedPerp(l Leg, m market.Perp, relH, target []float64, resets []bool, hedge bool, side lpSide, days int) LegResult {
	kind := strings.ToLower(l.Kind)
	label := "Long"
	if kind == "short" {
		label = "Short"
	}
	if hedge {
		label = "Hedge"
	}
	out := LegResult{
		Kind: kind, Title: label + " · " + m.ID, Capital: l.Capital, Hedged: hedge,
		Series: make([]float64, days+1), Notional: make([]float64, days+1),
	}
	dir := perps.Dir(kind)
	every := hedgeInterval(l)
	band := math.Max(0, l.HedgeBandPct) / 100
	notional := target[0]
	var pnl, funding, fees float64
	minMargin := math.Inf(1)
	out.Series[0] = l.Capital
	out.Notional[0] = notional
	for d := 1; d <= days; d++ {
		if out.Liquidated {
			out.Series[d] = 0
			continue
		}
		eq := l.Capital
		for h := 1; h <= HoursPerDay; h++ {
			j := (d-1)*HoursPerDay + h
			ret := 0.0
			if relH[j-1] > 0 {
				ret = relH[j]/relH[j-1] - 1
			}
			pnl += notional * ret * dir
			funding += perps.FundingPerDay(notional, m.Funding, dir) / HoursPerDay
			notional *= 1 + ret
			eq = perps.Equity(l.Capital, pnl, funding-fees)
			if notional > 0 {
				minMargin = math.Min(minMargin, eq/notional*100)
			}
			if perps.Liquidated(eq, l.Capital) {
				out.Liquidated = true
				break
			}
			last := d == days && h == HoursPerDay
			switch {
			case h == HoursPerDay && d < days && resets[d]:
				cost := perps.OpenFee(notional) + perps.OpenFee(target[d])
				fees += cost
				out.RebalanceCost += cost
				out.Rebalances++
				out.RebalanceDays = append(out.RebalanceDays, d)
				notional = target[d]
			case hedge && j%every == 0 && !last:
				delta, value := side.at(j)
				if gap := math.Abs(delta - notional); gap > band*value {
					fees += perps.OpenFee(gap)
					notional = delta
					out.HedgeTrades++
				}
			}
			eq = perps.Equity(l.Capital, pnl, funding-fees)
		}
		out.Notional[d] = notional
		if out.Liquidated {
			eq = 0
		}
		out.Series[d] = eq
	}
	out.funding, out.tradeFees = funding, fees
	out.Carry = funding - fees
	if out.Liquidated {
		out.Market = -l.Capital - out.Carry
	} else {
		out.Market = pnl
	}
	if !math.IsInf(minMargin, 1) {
		out.MinMarginPct = minMargin
	}
	out.End = out.Series[days]
	out.NetPnl = out.End - l.Capital
	if l.Capital != 0 {
		out.ReturnPct = out.NetPnl / l.Capital * 100
	}
	return out
}
