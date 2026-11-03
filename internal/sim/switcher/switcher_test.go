package switcher

import (
	"testing"
	"time"

	"amazing/internal/sim/core"
	"amazing/internal/sim/profile"
	"amazing/internal/sim/regime"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newSwitcher(cfg Config) *Switcher {
	return New(cfg, regime.DefaultThresholds(), profile.Defaults(), core.Range)
}

// feed sends one z per 5-minute bar and returns the bars a switch fired on.
func feed(s *Switcher, start int, zs ...float64) []int {
	var fired []int
	th := regime.DefaultThresholds()
	for k, z := range zs {
		i := start + k
		if _, ev := s.Update(t0.Add(time.Duration(i)*5*time.Minute), i, th.Classify(z), z, true); ev != nil {
			fired = append(fired, i)
		}
	}
	return fired
}

func TestNoSwitchBeforeConfirmBars(t *testing.T) {
	cfg := Defaults()
	cfg.ConfirmBars, cfg.MinDwell, cfg.Hysteresis = 3, 0, 0
	s := newSwitcher(cfg)
	if got := feed(s, 0, 2, 2); len(got) != 0 || s.Active() != core.Range {
		t.Fatalf("switched after 2 of 3 bars: %v, active %s", got, s.Active())
	}
	if got := feed(s, 2, 2); len(got) != 1 || s.Active() != core.Bullish {
		t.Fatalf("third bar should switch: %v, active %s", got, s.Active())
	}
	// An interruption restarts the count.
	s = newSwitcher(cfg)
	if got := feed(s, 0, 2, 2, 0, 2, 2); len(got) != 0 {
		t.Fatalf("interrupted run switched: %v", got)
	}
}

func TestNoSwitchInsideMinDwell(t *testing.T) {
	cfg := Defaults()
	cfg.ConfirmBars, cfg.Hysteresis, cfg.MinDwell = 1, 0, core.Duration(12*time.Hour)
	s := newSwitcher(cfg)
	if got := feed(s, 0, 2); len(got) != 1 {
		t.Fatalf("first switch should not wait for dwell: %v", got)
	}
	// 12h = 144 bars. Bearish signals for 143 more bars do nothing.
	zs := make([]float64, 143)
	for i := range zs {
		zs[i] = -2
	}
	if got := feed(s, 1, zs...); len(got) != 0 || s.Active() != core.Bullish {
		t.Fatalf("switched inside dwell at %v", got)
	}
	if got := feed(s, 144, -2); len(got) != 1 || s.Active() != core.Bearish {
		t.Fatalf("should switch once 12h has passed: %v, active %s", got, s.Active())
	}
}

// A z-score oscillating around the RANGE/TEND_BULLISH threshold (0.5) flips
// the regime back and forth without hysteresis and never with it.
func TestHysteresisPreventsFlipFlop(t *testing.T) {
	osc := make([]float64, 200)
	for i := range osc {
		osc[i] = 0.5 + 0.15*float64(1-2*((i/4)%2)) // 0.65 x4, 0.35 x4, ...
	}
	cfg := Defaults()
	cfg.ConfirmBars, cfg.MinDwell = 3, 0

	cfg.Hysteresis = 0
	if n := len(feed(newSwitcher(cfg), 0, osc...)); n < 10 {
		t.Fatalf("without hysteresis the oscillation should flip often, got %d switches", n)
	}
	cfg.Hysteresis = 0.2
	if n := len(feed(newSwitcher(cfg), 0, osc...)); n != 0 {
		t.Fatalf("hysteresis 0.2 should hold RANGE through ±0.15 around 0.5, got %d switches", n)
	}
	// A real move past the widened band still switches.
	s := newSwitcher(cfg)
	if got := feed(s, 0, 0.8, 0.8, 0.8); len(got) != 1 || s.Active() != core.TendBullish {
		t.Fatalf("z=0.8 > 0.5+0.2 should switch: %v", got)
	}
}

func TestSafetyFloorForcesBetaOneBelowRange(t *testing.T) {
	s := newSwitcher(Defaults())
	rangeProf := profile.Defaults()[core.Range] // β 0.3, static
	in := FloorInput{Price: 2700, Pa: 2727, HoldsLP: true, MarginRatio: 1, MaintenanceMargin: 0.01}
	p, f := s.ApplyFloor(rangeProf, in)
	if !f.Has(BelowRange) || p.Beta < 1 || p.HedgeMode != profile.Step || p.StepPct != Defaults().FloorStepPct {
		t.Fatalf("below range: got %+v flags %b", p, f)
	}
	in.Price = 2800
	if p, f := s.ApplyFloor(rangeProf, in); f.Has(BelowRange) || p != rangeProf {
		t.Fatalf("in range the profile must pass through unchanged: %+v %b", p, f)
	}
	// β above 1 is kept, never lowered.
	over := rangeProf
	over.Beta = 1.3
	in.Price = 2700
	if p, _ := s.ApplyFloor(over, in); p.Beta != 1.3 {
		t.Fatalf("floor lowered beta to %v", p.Beta)
	}
}

func TestSafetyFloorDrawdownAndMargin(t *testing.T) {
	cfg := Defaults()
	s := newSwitcher(cfg)
	bull := profile.Defaults()[core.TendBullish] // β 0.5
	in := FloorInput{Price: 3000, Pa: 2700, HoldsLP: true, DrawdownFrac: 0.09, MarginRatio: 0.03, MaintenanceMargin: 0.01}
	p, f := s.ApplyFloor(bull, in)
	if !f.Has(Drawdown) || f.Has(Exit) || p.Beta != 1 {
		t.Fatalf("drawdown with beta_one: %+v %b", p, f)
	}
	if !f.Has(Margin) {
		t.Fatalf("margin ratio 3%% < 1%% + 5%% buffer should flag: %b", f)
	}
	cfg.DDAction = "exit"
	if _, f := newSwitcher(cfg).ApplyFloor(bull, in); !f.Has(Exit) {
		t.Fatalf("dd_action exit should flag Exit: %b", f)
	}
}
