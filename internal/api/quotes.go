package api

import (
	"strings"

	"amazing/internal/clmath"
	"amazing/internal/market"
	"amazing/internal/perps"
)

type lpQuoteReq struct {
	PoolID     string  `json:"poolId"`
	Lower      float64 `json:"lower"`
	Upper      float64 `json:"upper"`
	Amount0    float64 `json:"amount0"`
	Amount1    float64 `json:"amount1"`
	CapitalUSD float64 `json:"capitalUsd"`
}

type depositRatio struct {
	Token0Pct float64 `json:"token0Pct"`
	Token1Pct float64 `json:"token1Pct"`
}

type hedge struct {
	PerpID   string  `json:"perpId"`
	Side     string  `json:"side"`
	SizeUsd  float64 `json:"sizeUsd"`
	Leverage float64 `json:"leverage"`
}

type LPQuote struct {
	PoolID            string       `json:"poolId"`
	Price             float64      `json:"price"`
	Lower             float64      `json:"lower"`
	Upper             float64      `json:"upper"`
	WidthPct          float64      `json:"widthPct"`
	CapitalEfficiency float64      `json:"capitalEfficiency"`
	FeeAprInRange     float64      `json:"feeAprInRange"`
	TimeInRange       float64      `json:"timeInRange"`
	EstFees30d        float64      `json:"estFees30d"`
	ILAtUpperEdge     float64      `json:"ilAtUpperEdge"`
	DepositRatio      depositRatio `json:"depositRatio"`
	Amount0           float64      `json:"amount0"`
	Amount1           float64      `json:"amount1"`
	ValueUsd          float64      `json:"valueUsd"`
	DeltaToken0       float64      `json:"deltaToken0"`
	DeltaUsd          float64      `json:"deltaUsd"`
	SuggestedHedge    hedge        `json:"suggestedHedge"`
	SingleSided       bool         `json:"singleSided"`
	Warnings          []string     `json:"warnings"`
	Calldata          any          `json:"calldata,omitempty"`
}

type perpQuoteReq struct {
	PerpID     string  `json:"perpId"`
	Side       string  `json:"side"`
	Collateral float64 `json:"collateral"`
	Leverage   float64 `json:"leverage"`
}

type PerpQuote struct {
	PerpID         string   `json:"perpId"`
	Side           string   `json:"side"`
	Price          float64  `json:"price"`
	Collateral     float64  `json:"collateral"`
	Leverage       float64  `json:"leverage"`
	SizeUsd        float64  `json:"sizeUsd"`
	SizeBase       float64  `json:"sizeBase"`
	Entry          float64  `json:"entry"`
	Liq            float64  `json:"liq"`
	LiqDistancePct float64  `json:"liqDistancePct"`
	ImpactPct      float64  `json:"impactPct"`
	OpenFee        float64  `json:"openFee"`
	FundingPerDay  float64  `json:"fundingPerDay"`
	MaxLeverage    float64  `json:"maxLeverage"`
	Warnings       []string `json:"warnings"`
	Calldata       any      `json:"calldata,omitempty"`
}

func quoteLP(pool market.Pool, perpsList []market.Perp, req lpQuoteReq, logQuote func(token string) float64) LPQuote {
	p, lo, hi := pool.Price, req.Lower, req.Upper
	q := logQuote(pool.Token1)
	unit0, unit1 := clmath.Amounts(p, lo, hi, 1)
	w0, w1 := unit0*p, unit1
	wt := w0 + w1
	if wt == 0 {
		wt = 1
	}

	amt0, amt1 := req.Amount0, req.Amount1
	value := amt0*p*q + amt1*q
	if value <= 0 && req.CapitalUSD > 0 {
		value = req.CapitalUSD
		amt0 = (w0 / wt) * (value / q) / p
		if p == 0 {
			amt0 = 0
		}
		amt1 = (w1 / wt) * (value / q)
	}

	eff := clmath.Concentration(p, lo, hi)
	tir := clmath.TimeInRange(p, lo, hi, pool.Sigma, 30)
	apr := pool.APR * clmath.FeeMultiplier(p, lo, hi)
	L := clmath.LiquidityFor(value/q, p, lo, hi)
	delta := clmath.Delta(value/q, p, lo, hi)
	deltaUsd := delta * p * q
	outside := p < lo || p > hi

	warnings := []string{}
	if outside {
		tok := pool.Token1
		if p < lo {
			tok = pool.Token0
		}
		warnings = append(warnings, "Spot is outside the range — deposit is single-sided in "+tok+" and earns no fees until price returns.")
	}

	perpID := matchingPerp(pool.Token0, perpsList)
	return LPQuote{
		PoolID: pool.ID, Price: p, Lower: lo, Upper: hi,
		WidthPct: clmath.WidthPct(lo, hi), CapitalEfficiency: eff,
		FeeAprInRange: apr, TimeInRange: tir,
		EstFees30d:    value * apr / 100 * tir * 30 / 365,
		ILAtUpperEdge: clmath.ILAtUpperEdgePct(p, lo, hi, L),
		DepositRatio:  depositRatio{Token0Pct: w0 / wt * 100, Token1Pct: w1 / wt * 100},
		Amount0:       amt0, Amount1: amt1, ValueUsd: value,
		DeltaToken0: delta, DeltaUsd: deltaUsd,
		SuggestedHedge: hedge{PerpID: perpID, Side: "short", SizeUsd: deltaUsd, Leverage: 3},
		SingleSided:    outside, Warnings: warnings,
	}
}

func quotePerp(m market.Perp, req perpQuoteReq) PerpQuote {
	side := strings.ToLower(req.Side)
	if side != "short" {
		side = "long"
	}
	lev := req.Leverage
	if lev < 1.1 {
		lev = 1.1
	}
	if m.MaxLev > 0 && lev > m.MaxLev {
		lev = m.MaxLev
	}
	dir := perps.Dir(side)
	size := perps.Size(req.Collateral, lev)
	liq := perps.Liquidation(m.Price, lev, dir)
	var warnings []string
	if lev > 15 {
		warnings = append(warnings, "High leverage: a small adverse move liquidates this position.")
	}
	sizeBase := 0.0
	if m.Price != 0 {
		sizeBase = size / m.Price
	}
	liqDist := 0.0
	if m.Price != 0 {
		liqDist = (liq/m.Price - 1) * 100
	}
	return PerpQuote{
		PerpID: m.ID, Side: side, Price: m.Price, Collateral: req.Collateral, Leverage: lev,
		SizeUsd: size, SizeBase: sizeBase, Entry: m.Price, Liq: liq, LiqDistancePct: liqDist,
		ImpactPct: perps.ImpactPct(size, m.OI), OpenFee: perps.OpenFee(size),
		FundingPerDay: perps.FundingPerDay(size, m.Funding, dir), MaxLeverage: m.MaxLev,
		Warnings: warnings,
	}
}

func matchingPerp(base string, list []market.Perp) string {
	for _, p := range list {
		if p.Base == base {
			return p.ID
		}
	}
	if len(list) > 0 {
		return list[0].ID
	}
	return ""
}
