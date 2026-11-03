package backtest

import "math"

// Attribution splits a run's P/L along
//
//	profit = fees - volatility cost - hedging costs (+ funding + residual)
//
// The parts sum exactly to NetPnl:
//
//   - VolatilityCost is the LP legs' convexity, measured every hour: over
//     each hour, what the position lost against holding its start-of-hour
//     delta, V(p1) - V(p0) - delta(p0)*(p1-p0). A concentrated LP is short
//     gamma, so this is never a gain; it is the gamma / LVR loss a delta hedge
//     cannot remove, only measure.
//   - HedgeCosts are perp trading fees (re-hedges and rebalance reopens);
//     RebalanceCosts are the LP's pool swap fees when it recentres.
//   - Residual is the directional P/L left over: the LP's delta P/L plus the
//     perps' market P/L. Unhedged it is the whole price bet; hedged it is the
//     delta drift between re-hedges, which shrinks as they get more frequent.
type Attribution struct {
	Fees           float64 `json:"fees"`
	VolatilityCost float64 `json:"volatilityCost"`
	HedgeCosts     float64 `json:"hedgeCosts"`
	RebalanceCosts float64 `json:"rebalanceCosts"`
	Funding        float64 `json:"funding"`
	Residual       float64 `json:"residual"`
	NetPnl         float64 `json:"netPnl"`
	// RealizedVolPct is the first LP pool's annualised volatility from its
	// hourly closes. FeesToVolCost > 1 means fees out-earned the volatility
	// cost: the condition the strategy needs.
	RealizedVolPct float64  `json:"realizedVolPct"`
	FeesToVolCost  *float64 `json:"feesToVolCost"`
	// Hedged is true when a perp is sized to the LP's delta. HedgeTrades and
	// MinMarginPct sum and floor those legs (see LegResult).
	Hedged       bool     `json:"hedged"`
	HedgeTrades  int      `json:"hedgeTrades"`
	MinMarginPct *float64 `json:"minMarginPct"`
}

// attribute builds the Attribution for a run with at least one LP leg, from
// the LP positions held each day and their hourly pool prices.
func attribute(results []LegResult, legHourly [][]float64, days int) *Attribution {
	a := &Attribution{}
	lpPrice, perpMarket := 0.0, 0.0
	hasLP := false
	for i := range results {
		r := &results[i]
		if r.Kind != "lp" {
			a.Funding += r.funding
			a.HedgeCosts += r.tradeFees
			perpMarket += r.Market
			if r.Hedged {
				a.Hedged = true
				a.HedgeTrades += r.HedgeTrades
				if r.MinMarginPct != 0 && (a.MinMarginPct == nil || r.MinMarginPct < *a.MinMarginPct) {
					v := r.MinMarginPct
					a.MinMarginPct = &v
				}
			}
			continue
		}
		h := hourlyOrFlat(legHourly[i], r, days)
		if !hasLP {
			a.RealizedVolPct = realizedVol(h)
		}
		hasLP = true
		a.Fees += r.Carry
		a.RebalanceCosts += r.RebalanceCost
		lpPrice += r.Market + r.IL + r.RebalanceCost
		for j := 1; j < len(h); j++ {
			b := r.books[(j-1)/HoursPerDay]
			p0, p1 := h[j-1], h[j]
			convexity := b.value(p1) - b.value(p0) - b.amount0(p0)*(p1-p0)
			a.VolatilityCost -= convexity * r.q
		}
	}
	if !hasLP {
		return nil
	}
	a.NetPnl = a.Fees - a.RebalanceCosts + lpPrice + a.Funding - a.HedgeCosts + perpMarket
	a.Residual = lpPrice + perpMarket + a.VolatilityCost
	if a.VolatilityCost > 0 {
		v := a.Fees / a.VolatilityCost
		a.FeesToVolCost = &v
	}
	return a
}

// realizedVol is the annualised volatility of hourly log returns, in percent.
func realizedVol(h []float64) float64 {
	rets := make([]float64, 0, len(h))
	for j := 1; j < len(h); j++ {
		if h[j-1] > 0 && h[j] > 0 {
			rets = append(rets, math.Log(h[j]/h[j-1]))
		}
	}
	if len(rets) < 2 {
		return 0
	}
	_, sd := meanSD(rets)
	return sd * math.Sqrt(HoursPerDay*365) * 100
}
