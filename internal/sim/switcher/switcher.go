// Package switcher decides when a detected regime change is real and which
// rule profile is in force, and applies the safety floor that overrides any
// profile.
//
// Whipsaw controls, in the order they apply:
//
//   - Hysteresis: with a scoring detector, the score must leave the active
//     regime's band widened by Hysteresis before a different regime counts.
//   - Confirmation: the same new regime must be detected ConfirmBars bars in
//     a row.
//   - Dwell: at least MinDwell must have passed since the last switch.
package switcher

import (
	"fmt"
	"math"
	"time"

	"amazing/internal/sim/core"
	"amazing/internal/sim/profile"
	"amazing/internal/sim/regime"
)

// Config tunes the switcher and the safety floor.
type Config struct {
	ConfirmBars int           `yaml:"confirm_bars" json:"confirm_bars"`
	Hysteresis  float64       `yaml:"hysteresis" json:"hysteresis"`
	MinDwell    core.Duration `yaml:"min_dwell" json:"min_dwell"`
	// TransitionRerange is "immediate" (re-range as soon as a switch changes
	// k, skew or LP mode) or "deferred" (wait for the next natural re-centre).
	TransitionRerange string `yaml:"transition_rerange" json:"transition_rerange"`
	// MaxDD is the equity drawdown from peak (fraction) that trips the floor;
	// DDAction is "beta_one" (hedge fully) or "exit" (withdraw to USDC).
	MaxDD    float64 `yaml:"max_dd" json:"max_dd"`
	DDAction string  `yaml:"dd_action" json:"dd_action"`
	// MarginBuffer is how far above maintenance margin the perp's margin
	// ratio must stay before the floor tops up collateral or cuts the short.
	MarginBuffer float64 `yaml:"margin_buffer" json:"margin_buffer"`
	// FloorStepPct is the step the floor hedges with when it forces a step
	// hedge (price below the range, or drawdown).
	FloorStepPct float64 `yaml:"floor_step_pct" json:"floor_step_pct"`
}

// Defaults: 3-bar confirmation, 0.2 z hysteresis, 12h dwell, immediate
// re-range, 8% drawdown → β = 1, 5% margin buffer, 1.5% floor steps.
func Defaults() Config {
	return Config{
		ConfirmBars: 3, Hysteresis: 0.2, MinDwell: core.Duration(12 * time.Hour),
		TransitionRerange: "immediate", MaxDD: 0.08, DDAction: "beta_one",
		MarginBuffer: 0.05, FloorStepPct: 0.015,
	}
}

// Validate checks the switcher settings.
func (c Config) Validate() error {
	switch {
	case c.ConfirmBars < 1:
		return fmt.Errorf("switcher.confirm_bars %d: must be >= 1", c.ConfirmBars)
	case c.Hysteresis < 0:
		return fmt.Errorf("switcher.hysteresis %v: must be >= 0", c.Hysteresis)
	case c.MinDwell < 0:
		return fmt.Errorf("switcher.min_dwell %v: must be >= 0", c.MinDwell.D())
	case c.TransitionRerange != "immediate" && c.TransitionRerange != "deferred":
		return fmt.Errorf("switcher.transition_rerange %q: want immediate or deferred", c.TransitionRerange)
	case c.MaxDD <= 0 || c.MaxDD >= 1:
		return fmt.Errorf("switcher.max_dd %v: want a fraction in (0, 1)", c.MaxDD)
	case c.DDAction != "beta_one" && c.DDAction != "exit":
		return fmt.Errorf("switcher.dd_action %q: want beta_one or exit", c.DDAction)
	case c.MarginBuffer < 0:
		return fmt.Errorf("switcher.margin_buffer %v: must be >= 0", c.MarginBuffer)
	case c.FloorStepPct <= 0:
		return fmt.Errorf("switcher.floor_step_pct %v: must be > 0", c.FloorStepPct)
	}
	return nil
}

// SwitchEvent records one regime switch. Bar is the bar index the switch
// was decided on; it takes effect (and Time is) the next bar, when the
// engine executes it and fills in TransitionCost.
type SwitchEvent struct {
	Time           time.Time   `json:"time"`
	Bar            int         `json:"bar"`
	From           core.Regime `json:"from"`
	To             core.Regime `json:"to"`
	Z              float64     `json:"z"`
	HasZ           bool        `json:"hasZ"`
	TransitionCost float64     `json:"transitionCost"`
	Note           string      `json:"note,omitempty"`
}

// Switcher tracks the active regime. It holds no references to shared state,
// so each run (and each Monte Carlo path) owns its own.
type Switcher struct {
	cfg      Config
	th       regime.Thresholds
	profiles profile.Set
	active   core.Regime
	since    time.Time
	switched bool
	cand     core.Regime
	count    int
}

// New starts a switcher in the initial regime.
func New(cfg Config, th regime.Thresholds, profiles profile.Set, initial core.Regime) *Switcher {
	return &Switcher{cfg: cfg, th: th, profiles: profiles, active: initial, cand: core.NoRegime}
}

// Active is the regime in force.
func (s *Switcher) Active() core.Regime { return s.active }

// Profile is the active regime's rule profile.
func (s *Switcher) Profile() profile.RuleProfile { return s.profiles[s.active] }

// Update feeds one bar's detection. z is the detector's score when hasZ.
// It returns the active profile and, when this bar confirms a switch, the
// event (nil otherwise).
func (s *Switcher) Update(t time.Time, bar int, detected core.Regime, z float64, hasZ bool) (profile.RuleProfile, *SwitchEvent) {
	if hasZ {
		lo, hi := s.th.Bounds(s.active)
		if z >= lo-s.cfg.Hysteresis && z <= hi+s.cfg.Hysteresis {
			detected = s.active
		} else {
			detected = s.th.Classify(z)
		}
	}
	if detected == s.active || !detected.Valid() {
		s.cand, s.count = core.NoRegime, 0
		return s.Profile(), nil
	}
	if detected == s.cand {
		s.count++
	} else {
		s.cand, s.count = detected, 1
	}
	if s.count < s.cfg.ConfirmBars {
		return s.Profile(), nil
	}
	if s.switched && t.Sub(s.since) < s.cfg.MinDwell.D() {
		return s.Profile(), nil
	}
	ev := &SwitchEvent{Time: t, Bar: bar, From: s.active, To: detected, Z: z, HasZ: hasZ}
	s.active, s.since, s.switched = detected, t, true
	s.cand, s.count = core.NoRegime, 0
	return s.Profile(), ev
}

// Floor flags which safety-floor rules fired.
type Floor uint8

const (
	// BelowRange: price is under pa, the LP is all ETH; β ≥ 1 with a step
	// hedge until re-centred.
	BelowRange Floor = 1 << iota
	// Drawdown: equity is more than MaxDD below its peak.
	Drawdown
	// Exit: the drawdown action is to withdraw everything to USDC.
	Exit
	// Margin: the perp's margin ratio is inside the buffer above maintenance.
	Margin
)

// Has reports whether f includes g.
func (f Floor) Has(g Floor) bool { return f&g != 0 }

// FloorInput is the state the safety floor checks.
type FloorInput struct {
	Price, Pa         float64
	HoldsLP           bool
	DrawdownFrac      float64
	MarginRatio       float64
	MaintenanceMargin float64
}

// ApplyFloor returns p with the safety floor applied, and which rules fired.
// The floor overrides any regime: below the range the LP is all ETH and its
// delta at its largest, so β is raised to at least 1 and the hedge steps
// tightly; past the drawdown limit it hedges fully (or exits).
func (s *Switcher) ApplyFloor(p profile.RuleProfile, in FloorInput) (profile.RuleProfile, Floor) {
	var f Floor
	if in.HoldsLP && in.Price < in.Pa {
		f |= BelowRange
		p.Beta = math.Max(p.Beta, 1)
		p = s.forceStep(p)
	}
	if in.DrawdownFrac > s.cfg.MaxDD {
		f |= Drawdown
		if s.cfg.DDAction == "exit" {
			f |= Exit
		} else {
			p.Beta = math.Max(p.Beta, 1)
			p = s.forceStep(p)
		}
	}
	if in.MarginRatio < in.MaintenanceMargin+s.cfg.MarginBuffer {
		f |= Margin
	}
	return p, f
}

func (s *Switcher) forceStep(p profile.RuleProfile) profile.RuleProfile {
	if p.HedgeMode != profile.Continuous {
		if p.HedgeMode != profile.Step || p.StepAbs > 0 || p.StepPct > s.cfg.FloorStepPct {
			p.HedgeMode, p.StepPct, p.StepAbs = profile.Step, s.cfg.FloorStepPct, 0
		}
	}
	return p
}
