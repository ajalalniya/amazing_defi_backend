// Package regime classifies the market into the five regimes the rule
// profiles are keyed by. Detectors are pluggable behind Detector; the
// RegimeSwitcher (package switcher) sits between a detector and the strategy
// and decides when a detected change is real.
//
// Detectors see bars strictly in order and only up to the bar they are given:
// none may look ahead except OracleDetector, which exists only as an upper
// bound and says so (LooksAhead).
package regime

import (
	"fmt"

	"amazing/internal/sim/core"
)

// Regime is core.Regime, re-exported so detector code reads naturally.
type Regime = core.Regime

// The five regimes.
const (
	Bearish     = core.Bearish
	TendBearish = core.TendBearish
	Range       = core.Range
	TendBullish = core.TendBullish
	Bullish     = core.Bullish
)

// Detector classifies the market one bar at a time.
type Detector interface {
	// Update is called once per bar, in order, and returns the regime the
	// data up to and including that bar indicates.
	Update(bar core.Bar) Regime
	// Reset clears all state so the detector can run a new series.
	Reset()
}

// Scorer is a detector with a continuous score behind its regimes (the
// momentum z-score). The switcher applies hysteresis to the score; detectors
// without one switch on their labels alone.
type Scorer interface {
	Score() float64
}

// LookAheader marks a detector that reads future data. Only the oracle does.
type LookAheader interface {
	LooksAhead() bool
}

// Thresholds map a score z onto regimes:
//
//	z > Bull                 BULLISH
//	TendBull < z ≤ Bull      TEND_BULLISH
//	TendBear ≤ z ≤ TendBull  RANGE
//	Bear ≤ z < TendBear      TEND_BEARISH
//	z < Bear                 BEARISH
type Thresholds struct {
	Bull     float64 `yaml:"bull" json:"bull"`
	TendBull float64 `yaml:"tend_bull" json:"tend_bull"`
	TendBear float64 `yaml:"tend_bear" json:"tend_bear"`
	Bear     float64 `yaml:"bear" json:"bear"`
}

// DefaultThresholds is ±0.5 for a tendency, ±1.5 for a trend.
func DefaultThresholds() Thresholds {
	return Thresholds{Bull: 1.5, TendBull: 0.5, TendBear: -0.5, Bear: -1.5}
}

// Validate checks the thresholds are strictly ordered.
func (t Thresholds) Validate() error {
	if !(t.Bear < t.TendBear && t.TendBear < t.TendBull && t.TendBull < t.Bull) {
		return fmt.Errorf("thresholds must satisfy bear < tend_bear < tend_bull < bull, got %v < %v < %v < %v", t.Bear, t.TendBear, t.TendBull, t.Bull)
	}
	return nil
}

// Classify maps a score to its regime.
func (t Thresholds) Classify(z float64) Regime {
	switch {
	case z > t.Bull:
		return Bullish
	case z > t.TendBull:
		return TendBullish
	case z >= t.TendBear:
		return Range
	case z >= t.Bear:
		return TendBearish
	default:
		return Bearish
	}
}

// Bounds is the score interval [lo, hi] that maps to r; the outer regimes are
// open-ended (±Inf is represented by ±1e300).
func (t Thresholds) Bounds(r Regime) (lo, hi float64) {
	const inf = 1e300
	switch r {
	case Bullish:
		return t.Bull, inf
	case TendBullish:
		return t.TendBull, t.Bull
	case Range:
		return t.TendBear, t.TendBull
	case TendBearish:
		return t.Bear, t.TendBear
	default:
		return -inf, t.Bear
	}
}
