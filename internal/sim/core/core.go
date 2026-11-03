// Package core holds the types every simulator package shares: a price Bar
// and the market Regime. It imports nothing from the simulator, so data
// sources (which label true regimes) and detectors (which read bars) can both
// depend on it without importing each other.
//
// Money is USD float64 throughout the simulator. On-chain integration would
// need big.Int / Q64.96 fixed-point maths; that is out of scope here.
package core

import (
	"fmt"
	"strings"
	"time"
)

// Regime is a market state the rule profiles are keyed by.
type Regime int8

// The five regimes, ordered from most bearish to most bullish so that
// thresholds on a score map onto them monotonically. NoRegime, the zero
// value, marks a bar with no known true regime (historical data), so a Bar
// built without one is never mistaken for a labelled bar.
const (
	NoRegime Regime = iota
	Bearish
	TendBearish
	Range
	TendBullish
	Bullish
)

// Regimes lists the five regimes in order, for iteration and reports.
var Regimes = [...]Regime{Bearish, TendBearish, Range, TendBullish, Bullish}

var regimeNames = [...]string{"BEARISH", "TEND_BEARISH", "RANGE", "TEND_BULLISH", "BULLISH"}

// String is the regime's config and report name, e.g. "TEND_BULLISH".
func (r Regime) String() string {
	if r < Bearish || r > Bullish {
		return "NONE"
	}
	return regimeNames[r.Index()]
}

// Index is the regime's position in Regimes (0 for BEARISH .. 4 for
// BULLISH), for arrays indexed by regime. It is -1 for NoRegime.
func (r Regime) Index() int { return int(r) - 1 }

// Valid reports whether r is one of the five regimes.
func (r Regime) Valid() bool { return r >= Bearish && r <= Bullish }

// MarshalText encodes a regime by name, so JSON and YAML read naturally.
func (r Regime) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText parses a regime name, case-insensitively.
func (r *Regime) UnmarshalText(b []byte) error {
	v, err := ParseRegime(string(b))
	if err != nil {
		return err
	}
	*r = v
	return nil
}

// ParseRegime parses a regime name such as "range" or "TEND_BEARISH".
func ParseRegime(s string) (Regime, error) {
	u := strings.ToUpper(strings.TrimSpace(s))
	if u == "NONE" || u == "" {
		return NoRegime, nil
	}
	for i, n := range regimeNames {
		if n == u {
			return Regime(i + 1), nil
		}
	}
	return NoRegime, fmt.Errorf("unknown regime %q (want one of %s)", s, strings.Join(regimeNames[:], ", "))
}

// Bar is one price bar. Volume is in USD (USDC). Liquidity is the pool's
// active liquidity in the same human units as a position's L (0 if unknown).
// IV is an annualised implied volatility for the options module (0 if none).
// TrueRegime is the regime a synthetic path was generated in, NoRegime for
// historical data.
type Bar struct {
	Time       time.Time `json:"time"`
	Open       float64   `json:"open"`
	High       float64   `json:"high"`
	Low        float64   `json:"low"`
	Close      float64   `json:"close"`
	Volume     float64   `json:"volume,omitempty"`
	Liquidity  float64   `json:"liquidity,omitempty"`
	IV         float64   `json:"iv,omitempty"`
	TrueRegime Regime    `json:"trueRegime"`
}

// Duration is a time.Duration that reads and writes as text ("5m", "12h",
// "3d"), so configs are the same in YAML and JSON. A "d" suffix means days.
type Duration time.Duration

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// MarshalText writes the duration in Go's notation.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// UnmarshalText parses Go durations plus whole or fractional days ("3d").
func (d *Duration) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%g", &days); err != nil {
			return fmt.Errorf("duration %q: %w", s, err)
		}
		*d = Duration(days * float64(24*time.Hour))
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// YearsPerHour converts hours to a year fraction (365-day year).
const YearsPerHour = 1.0 / (365 * 24)

// DtYears is d as a fraction of a 365-day year: the one place durations
// become the year fractions rates are quoted in.
func DtYears(d time.Duration) float64 { return d.Hours() * YearsPerHour }
