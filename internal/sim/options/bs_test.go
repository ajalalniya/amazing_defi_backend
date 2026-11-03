package options

import (
	"math"
	"testing"
	"time"

	"amazing/internal/sim/core"
)

func TestBlackScholes(t *testing.T) {
	s, k, tt, iv := 3000.0, 3100.0, 7.0/365, 0.6
	c, p, g := BS(s, k, tt, iv)
	if math.Abs((c-p)-(s-k)) > 1e-9 {
		t.Errorf("put-call parity: c-p = %v, s-k = %v", c-p, s-k)
	}
	// Gamma matches the delta's numeric slope.
	h := 0.01
	cu, _, _ := BS(s+h, k, tt, iv)
	cd, _, _ := BS(s-h, k, tt, iv)
	if num := (cu - 2*c + cd) / (h * h); math.Abs(num/g-1) > 1e-3 {
		t.Errorf("gamma %v, numeric %v", g, num)
	}
	// ATM straddle ≈ 0.8·σ√T·S.
	ca, pa, _ := BS(s, s, tt, iv)
	if approx := 0.7979 * iv * math.Sqrt(tt) * s; math.Abs((ca+pa)/approx-1) > 0.01 {
		t.Errorf("ATM straddle %v, approx %v", ca+pa, approx)
	}
	if p := BreakoutProb(3000, 2727, 3300, tt, iv); p < 0.2 || p > 0.35 {
		t.Errorf("breakout probability of ±10%% over a week at 60%% vol: %v", p)
	}
}

func TestOverlayBuysRollsAndExpires(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := Config{Enabled: true, IV: 0.6, Tenor: core.Duration(7 * 24 * time.Hour), Roll: core.Duration(3 * 24 * time.Hour),
		GammaFraction: 0.5, TriggerProb: 0.2, RangeOnly: true}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	o := NewOverlay(cfg)
	lpGamma := -0.006
	if c := o.Step(t0, 3000, 0, lpGamma, core.Bullish, 2727, 3300); c != 0 {
		t.Fatal("range_only: no buy outside RANGE")
	}
	c := o.Step(t0, 3000, 0, lpGamma, core.Range, 2727, 3300)
	if c >= 0 || o.Buys != 1 {
		t.Fatalf("should buy in RANGE: cash %v buys %d", c, o.Buys)
	}
	if g := o.Gamma(t0, 3000, 0); math.Abs(g-0.5*-lpGamma) > 1e-9 {
		t.Errorf("bought gamma %v, want half the LP's %v", g, -lpGamma)
	}
	if c := o.Step(t0.Add(time.Hour), 3000, 0, lpGamma, core.Range, 2727, 3300); c != 0 {
		t.Error("no trade while holding inside the roll period")
	}
	c = o.Step(t0.Add(3*24*time.Hour), 3000, 0, lpGamma, core.Range, 2727, 3300)
	if o.Buys != 2 || o.Proceeds <= 0 {
		t.Errorf("should sell and rebuy at the roll: buys %d proceeds %v", o.Buys, o.Proceeds)
	}
	// The fresh 7-day straddle costs more than the 4-day one sold: rolling
	// pays the time value back.
	if c >= 0 {
		t.Errorf("rolling should cost net cash, got %v", c)
	}
}
