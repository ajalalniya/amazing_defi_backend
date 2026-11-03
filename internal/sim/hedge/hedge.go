// Package hedge holds the rules that size the perp short against the LP's
// delta. A perp has no gamma, so no rule removes the LP's gamma loss; the
// rules only change how it is paid:
//
//   - Static: a fixed short (β · the range's average delta). The loss
//     depends on where price ends; cheap in chop, worst on big moves.
//   - Step: re-set to β·Δ_LP each time price has moved a step since the last
//     adjustment. The loss depends on how much price wiggles; each leg costs
//     about ½|Γ|·Δp².
//   - Continuous: re-set every bar. Least residual risk, most trading fees.
package hedge

import (
	"math"

	"amazing/internal/sim/profile"
)

// State is what a rule remembers between bars.
type State struct {
	// Current is the short held now (ETH).
	Current float64
	// Anchor is the price at the last adjustment; 0 forces the next Target
	// to re-hedge (set after a mint, re-centre or regime transition).
	Anchor float64
	// Avg is the current range's average delta (V(pb)−V(pa))/(pb−pa).
	Avg float64
}

// Rule returns the desired short size (ETH) for this bar. Returning
// st.Current means "no trade".
type Rule interface {
	Target(p, lpDelta, beta float64, st *State) float64
}

// None holds no short.
type None struct{}

// Target is always 0.
func (None) Target(_, _, _ float64, _ *State) float64 { return 0 }

// Static holds β·h_avg.
type Static struct{}

// Target is β times the range's average delta, constant until the range
// (and so st.Avg) changes.
func (Static) Target(p, _, beta float64, st *State) float64 {
	if st.Anchor == 0 {
		st.Anchor = p
	}
	return beta * st.Avg
}

// Step re-hedges to β·Δ_LP when price has moved at least Pct (fraction of
// the anchor price) or, if Abs > 0, Abs USD since the last adjustment.
type Step struct {
	Pct float64
	Abs float64
}

// Target re-hedges on a step move (or when the anchor was cleared) and
// otherwise holds the current short.
func (s Step) Target(p, lpDelta, beta float64, st *State) float64 {
	if st.Anchor > 0 {
		move := math.Abs(p - st.Anchor)
		need := s.Abs
		if need <= 0 {
			need = s.Pct * st.Anchor
		}
		if move < need-1e-9 {
			return st.Current
		}
	}
	st.Anchor = p
	return beta * lpDelta
}

// Continuous re-hedges to β·Δ_LP every bar.
type Continuous struct{}

// Target is β·Δ_LP.
func (Continuous) Target(p, lpDelta, beta float64, st *State) float64 {
	st.Anchor = p
	return beta * lpDelta
}

// For builds the rule a profile asks for.
func For(p profile.RuleProfile) Rule {
	switch p.HedgeMode {
	case profile.Static:
		return Static{}
	case profile.Step:
		return Step{Pct: p.StepPct, Abs: p.StepAbs}
	case profile.Continuous:
		return Continuous{}
	default:
		return None{}
	}
}
