// Package pool is the Uniswap v3 concentrated-liquidity position maths the
// simulator trades: value, delta (the ETH held), gamma, and minting a
// position from capital over a symmetric or skewed range.
//
// Prices are USDC per ETH; amounts are human units (ETH, USDC). This is
// continuous maths: ticks are not snapped and amounts are not integers, which
// is what a strategy simulator needs; on-chain code would use Q64.96.
package pool

import (
	"fmt"
	"math"
)

// Position is liquidity L over [Pa, Pb].
type Position struct {
	L  float64 `json:"l"`
	Pa float64 `json:"pa"`
	Pb float64 `json:"pb"`
}

// RangeFor returns the range of width factor k (> 1) around p, shifted by
// skew in log space: skew 0 is p/k..p·k, +1 is p..p·k² (all ETH, sitting on
// the lower edge), -1 is p/k²..p (all USDC, on the upper edge).
func RangeFor(p, k, skew float64) (pa, pb float64, err error) {
	if p <= 0 || math.IsNaN(p) || math.IsInf(p, 0) {
		return 0, 0, fmt.Errorf("range: price must be positive, got %v", p)
	}
	if !(k > 1) {
		return 0, 0, fmt.Errorf("range: width factor k must be > 1, got %v", k)
	}
	if skew < -1 || skew > 1 {
		return 0, 0, fmt.Errorf("range: skew must be in [-1, 1], got %v", skew)
	}
	// The fully skewed ranges are pinned to p exactly, so rounding cannot put
	// price a hair outside the edge it is meant to sit on.
	switch skew {
	case 1:
		return p, p * k * k, nil
	case -1:
		return p / (k * k), p, nil
	}
	c := p * math.Pow(k, skew)
	return c / k, c * k, nil
}

// Mint deploys capital (USD) at price p over [pa, pb]. Inside the range it
// solves L = C / (2√p − p/√pb − √pa); outside, the position is all one asset.
func Mint(capital, p, pa, pb float64) (Position, error) {
	if capital < 0 || p <= 0 || pa <= 0 || pb <= pa {
		return Position{}, fmt.Errorf("mint: need capital >= 0, p > 0 and 0 < pa < pb, got C=%v p=%v [%v, %v]", capital, p, pa, pb)
	}
	unit := Position{L: 1, Pa: pa, Pb: pb}.Value(p)
	if unit <= 0 {
		return Position{}, fmt.Errorf("mint: zero value per unit of liquidity at p=%v", p)
	}
	return Position{L: capital / unit, Pa: pa, Pb: pb}, nil
}

// Amounts is the ETH (x) and USDC (y) the position holds at p.
//
//	pa ≤ p ≤ pb: x = L(1/√p − 1/√pb), y = L(√p − √pa)
//	p < pa:      x = L(1/√pa − 1/√pb), y = 0
//	p > pb:      x = 0,                y = L(√pb − √pa)
func (s Position) Amounts(p float64) (x, y float64) {
	if s.L <= 0 || s.Pa <= 0 || s.Pb <= s.Pa || p <= 0 {
		return 0, 0
	}
	q := math.Sqrt(math.Min(math.Max(p, s.Pa), s.Pb))
	sa, sb := math.Sqrt(s.Pa), math.Sqrt(s.Pb)
	return s.L * (1/q - 1/sb), s.L * (q - sa)
}

// Value is the position's USD value at p: x·p + y, which in range equals
// L(2√p − p/√pb − √pa).
func (s Position) Value(p float64) float64 {
	x, y := s.Amounts(p)
	return x*p + y
}

// Delta is dV/dp, the ETH the position holds: the short a delta-neutral
// hedge needs.
func (s Position) Delta(p float64) float64 {
	x, _ := s.Amounts(p)
	return x
}

// Gamma is dDelta/dp: −L / (2·p^{3/2}) in range, 0 outside. Always ≤ 0.
func (s Position) Gamma(p float64) float64 {
	if !s.InRange(p) || s.L <= 0 {
		return 0
	}
	return -s.L / (2 * p * math.Sqrt(p))
}

// InRange reports pa ≤ p ≤ pb.
func (s Position) InRange(p float64) bool { return p >= s.Pa && p <= s.Pb }

// K is the range's width factor √(pb/pa); for a symmetric range p/k..p·k it
// is k.
func (s Position) K() float64 {
	if s.Pa <= 0 {
		return 0
	}
	return math.Sqrt(s.Pb / s.Pa)
}

// AverageDelta is the static hedge that best covers the whole range:
// (V(pb) − V(pa)) / (pb − pa), the slope of the value curve edge to edge.
func (s Position) AverageDelta() float64 {
	if s.Pb <= s.Pa {
		return 0
	}
	return (s.Value(s.Pb) - s.Value(s.Pa)) / (s.Pb - s.Pa)
}

// ConcentrationMultiplier is 1/(1 − 1/√k): how much more fee a dollar earns in
// this range than spread over the full range.
func ConcentrationMultiplier(k float64) float64 {
	if !(k > 1) {
		return 1
	}
	return 1 / (1 - 1/math.Sqrt(k))
}

// GammaDecayAPR is the expected yearly loss of a symmetric range of width k
// to volatility σ (annualised), as a fraction of its value: σ²/(8(1−1/√k)).
// It is the fee APR the range must earn just to break even before costs.
func GammaDecayAPR(sigma, k float64) float64 {
	if !(k > 1) {
		return 0
	}
	return sigma * sigma / (8 * (1 - 1/math.Sqrt(k)))
}
