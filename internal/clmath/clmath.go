// Package clmath is Uniswap v3/v4 concentrated-liquidity math.
// Pure and dependency-free so it can be tested without a server or network.
//
// All prices are token1-per-token0. p = spot, pa = lower, pb = upper.
package clmath

import "math"

// RefConcentration is the capital-efficiency of a typical (~±15%) LP.
// Quoted pool APRs already embed that band, so fee income scales with
// concentration relative to this reference — not the absolute factor.
const RefConcentration = 13.7

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Amounts returns the token amounts held by liquidity L over [pa, pb] at price p.
//
//	s  = sqrt(clamp(p, pa, pb)); sa = sqrt(pa); sb = sqrt(pb)
//	amount0 = L * (1/s - 1/sb)
//	amount1 = L * (s - sa)
func Amounts(p, pa, pb, L float64) (amount0, amount1 float64) {
	if pa <= 0 || pb <= pa || L == 0 {
		return 0, 0
	}
	s := math.Sqrt(clamp(p, pa, pb))
	sa := math.Sqrt(pa)
	sb := math.Sqrt(pb)
	if s == 0 || sb == 0 {
		return 0, 0
	}
	return L * (1/s - 1/sb), L * (s - sa)
}

// Value is the position value in token1 terms: amount0*p + amount1.
func Value(p, pa, pb, L float64) float64 {
	a0, a1 := Amounts(p, pa, pb, L)
	return a0*p + a1
}

// LiquidityFor returns L such that Value(p0, pa, pb, L) == capital
// (capital in token1 units).
func LiquidityFor(capital, p0, pa, pb float64) float64 {
	unit := Value(p0, pa, pb, 1)
	if unit <= 0 {
		return 0
	}
	return capital / unit
}

// Concentration is capital efficiency vs a full-range position, clamped to [1, 4000]:
//
//	1 / (1 - (pa/pb)^0.25)
func Concentration(p, pa, pb float64) float64 {
	_ = p
	if pa <= 0 || pb <= pa {
		return 1
	}
	c := 1 / (1 - math.Pow(pa/pb, 0.25))
	return clamp(c, 1, 4000)
}

// FeeMultiplier scales fee income with concentration relative to RefConcentration.
// Clamped to [0.05, 12].
func FeeMultiplier(p, pa, pb float64) float64 {
	return clamp(Concentration(p, pa, pb)/RefConcentration, 0.05, 12)
}

// TimeInRange is P(price stays in [pa, pb]) over `days`, lognormal:
//
//	sd = sigma*sqrt(days/365); z = ln(bound/p)/sd; Phi(zU)-Phi(zL), clamped [0.01, 1]
func TimeInRange(p, pa, pb, sigma, days float64) float64 {
	if p <= 0 || pa <= 0 || pb <= pa {
		return 0.01
	}
	sd := sigma * math.Sqrt(days/365)
	if sd == 0 {
		sd = 1e-6
	}
	zL := math.Log(pa/p) / sd
	zU := math.Log(pb/p) / sd
	return clamp(normCDF(zU)-normCDF(zL), 0.01, 1)
}

func normCDF(z float64) float64 {
	return 0.5 * (1 + erf(z/math.Sqrt2))
}

// erf is Abramowitz & Stegun 7.1.26, matching the prototype.
func erf(x float64) float64 {
	s := 1.0
	if x < 0 {
		s = -1
		x = -x
	} else if x == 0 {
		return 0
	}
	t := 1 / (1 + 0.3275911*x)
	y := 1 - (((((1.061405429*t-1.453152027)*t+1.421413741)*t-0.284496736)*t+0.254829592)*t)*math.Exp(-x*x)
	return s * y
}

// Delta is position delta in token0 units == amount0 at spot.
func Delta(capital, p, pa, pb float64) float64 {
	L := LiquidityFor(capital, p, pa, pb)
	a0, _ := Amounts(p, pa, pb, L)
	return a0
}

// WidthPct is (upper/lower - 1) * 100.
func WidthPct(lower, upper float64) float64 {
	if lower <= 0 {
		return 0
	}
	return (upper/lower - 1) * 100
}

// ILAtUpperEdgePct is IL as a percent of HODL if price walks to the upper bound.
func ILAtUpperEdgePct(p, pa, pb, L float64) float64 {
	a0, a1 := Amounts(p, pa, pb, L)
	atEdge := Value(pb, pa, pb, L)
	hodl := a0*pb + a1
	if hodl == 0 {
		return 0
	}
	return (atEdge - hodl) / hodl * 100
}
