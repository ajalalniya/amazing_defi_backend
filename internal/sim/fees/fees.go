// Package fees models the swap fees an LP position earns. Fees are paid out
// to the side (never compounded into the position) and only while the price
// is inside the range.
package fees

import (
	"fmt"
	"math"

	"amazing/internal/sim/core"
	"amazing/internal/sim/pool"
)

// Model is a pool fee income model.
type Model interface {
	// Income is the USD fee the position earns over a bar of dt years.
	Income(bar core.Bar, pos *pool.Position, dt float64) float64
}

// None earns nothing (the validation setup).
type None struct{}

// Income is 0.
func (None) Income(core.Bar, *pool.Position, float64) float64 { return 0 }

// APR earns V · BaseAPR · 1/(1−1/√k) · dt while in range, where BaseAPR is
// what a full-range position earns and the multiplier is the range's
// concentration.
type APR struct {
	BaseAPR float64
}

// Income is the concentrated APR on the position's value for the bar.
func (m APR) Income(bar core.Bar, pos *pool.Position, dt float64) float64 {
	if pos == nil || !pos.InRange(bar.Close) {
		return 0
	}
	return pos.Value(bar.Close) * m.BaseAPR * pool.ConcentrationMultiplier(pos.K()) * dt
}

// Volume earns the bar's volume · fee tier · the position's share of active
// liquidity, L_user / L_active (capped at 1). L_active is the bar's
// Liquidity when the data has it, else ActiveL.
type Volume struct {
	FeeTier float64
	ActiveL float64
}

// Income is the position's share of the bar's swap fees.
func (m Volume) Income(bar core.Bar, pos *pool.Position, _ float64) float64 {
	if pos == nil || !pos.InRange(bar.Close) || bar.Volume <= 0 {
		return 0
	}
	active := bar.Liquidity
	if active <= 0 {
		active = m.ActiveL
	}
	if active <= 0 {
		return 0
	}
	return bar.Volume * m.FeeTier * math.Min(1, pos.L/active)
}

// For builds the model named in config: "none", "apr" or "volume".
func For(kind string, baseAPR, feeTier, activeL float64) (Model, error) {
	switch kind {
	case "", "none":
		return None{}, nil
	case "apr":
		return APR{BaseAPR: baseAPR}, nil
	case "volume":
		return Volume{FeeTier: feeTier, ActiveL: activeL}, nil
	}
	return nil, fmt.Errorf("fee model %q: want none, apr or volume", kind)
}
