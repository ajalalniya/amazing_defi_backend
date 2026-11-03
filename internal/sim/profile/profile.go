// Package profile defines the RuleProfile each regime maps to: what the LP
// does, how much of its delta is hedged and how, and the range it uses.
package profile

import (
	"fmt"

	"amazing/internal/sim/core"
)

// LPMode is what the LP capital does under a profile.
type LPMode string

const (
	// Active keeps a concentrated range of width K and Skew.
	Active LPMode = "active"
	// SkewedUp keeps a range fully above price (skew +1) so the position
	// stays ETH while price rises; it is Active with Skew forced to +1.
	SkewedUp LPMode = "skewed_up"
	// ExitToETH withdraws the LP and holds ETH (strong bull trend).
	ExitToETH LPMode = "exit_to_eth"
	// ExitToUSDC withdraws the LP and holds USDC (strong bear trend).
	ExitToUSDC LPMode = "exit_to_usdc"
)

// HedgeMode is how the perp short tracks β·Δ_LP.
type HedgeMode string

const (
	// NoHedge holds no short.
	NoHedge HedgeMode = "none"
	// Static holds β·h_avg, the range's average delta, until the range changes.
	Static HedgeMode = "static"
	// Step re-sets the short to β·Δ_LP when price has moved StepPct since
	// the last adjustment.
	Step HedgeMode = "step"
	// Continuous re-sets the short to β·Δ_LP every bar.
	Continuous HedgeMode = "continuous"
)

// RuleProfile is one regime's rules.
type RuleProfile struct {
	LPMode    LPMode    `yaml:"lp_mode" json:"lp_mode"`
	Beta      float64   `yaml:"beta" json:"beta"`
	HedgeMode HedgeMode `yaml:"hedge_mode" json:"hedge_mode"`
	// StepPct is the move (fraction, 0.025 = 2.5%) that triggers a step
	// re-hedge; StepAbs, if set, is an absolute USD move instead.
	StepPct float64 `yaml:"step_pct" json:"step_pct"`
	StepAbs float64 `yaml:"step_abs,omitempty" json:"step_abs,omitempty"`
	K       float64 `yaml:"k" json:"k"`
	Skew    float64 `yaml:"skew" json:"skew"`
}

// EffectiveSkew is Skew, or +1 for SkewedUp.
func (p RuleProfile) EffectiveSkew() float64 {
	if p.LPMode == SkewedUp {
		return 1
	}
	return p.Skew
}

// HoldsLP reports whether the profile keeps an LP position open.
func (p RuleProfile) HoldsLP() bool { return p.LPMode == Active || p.LPMode == SkewedUp }

// SameRange reports whether switching from p to q leaves the LP alone:
// same mode, width and skew.
func (p RuleProfile) SameRange(q RuleProfile) bool {
	return p.LPMode == q.LPMode && p.K == q.K && p.EffectiveSkew() == q.EffectiveSkew()
}

// Validate checks one profile; maxBeta caps β (β > 1 over-hedges, allowed
// only up to the configured cap).
func (p RuleProfile) Validate(maxBeta float64) error {
	switch p.LPMode {
	case Active, SkewedUp, ExitToETH, ExitToUSDC:
	default:
		return fmt.Errorf("lp_mode %q: want active, skewed_up, exit_to_eth or exit_to_usdc", p.LPMode)
	}
	switch p.HedgeMode {
	case NoHedge, Static, Step, Continuous:
	default:
		return fmt.Errorf("hedge_mode %q: want none, static, step or continuous", p.HedgeMode)
	}
	if p.Beta < 0 || p.Beta > maxBeta {
		return fmt.Errorf("beta %v: want 0..%v (raise max_beta to over-hedge)", p.Beta, maxBeta)
	}
	if p.HedgeMode == Step && p.StepPct <= 0 && p.StepAbs <= 0 {
		return fmt.Errorf("hedge_mode step needs step_pct > 0 (or step_abs > 0)")
	}
	if p.HoldsLP() && !(p.K > 1) {
		return fmt.Errorf("k %v: an LP range needs k > 1", p.K)
	}
	if p.Skew < -1 || p.Skew > 1 {
		return fmt.Errorf("skew %v: want -1..1", p.Skew)
	}
	return nil
}

// Set maps every regime to its profile.
type Set map[core.Regime]RuleProfile

// Validate checks all five regimes are present and valid.
func (s Set) Validate(maxBeta float64) error {
	for _, r := range core.Regimes {
		p, ok := s[r]
		if !ok {
			return fmt.Errorf("profiles: no profile for %s", r)
		}
		if err := p.Validate(maxBeta); err != nil {
			return fmt.Errorf("profiles.%s: %w", r, err)
		}
	}
	return nil
}

// Defaults is the brief's table: max fees in RANGE, keep upside when
// trending up, exit to ETH when strongly bullish, fully hedged with
// tightening steps on the way down.
func Defaults() Set {
	return Set{
		core.Range:       {LPMode: Active, Beta: 0.3, HedgeMode: Static, K: 1.10},
		core.TendBullish: {LPMode: Active, Beta: 0.5, HedgeMode: Step, StepPct: 0.04, K: 1.15},
		core.Bullish:     {LPMode: ExitToETH, Beta: 0, HedgeMode: NoHedge, K: 1.15, Skew: 1},
		core.TendBearish: {LPMode: Active, Beta: 1.0, HedgeMode: Step, StepPct: 0.025, K: 1.10},
		core.Bearish:     {LPMode: Active, Beta: 1.0, HedgeMode: Step, StepPct: 0.015, K: 1.15},
	}
}
