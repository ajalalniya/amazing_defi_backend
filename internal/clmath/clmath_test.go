package clmath

import (
	"math"
	"testing"
)

func TestAmountsGolden(t *testing.T) {
	p, pa, pb, L := 100.0, 90.0, 110.0, 1.0
	a0, a1 := Amounts(p, pa, pb, L)
	s := math.Sqrt(p)
	sa := math.Sqrt(pa)
	sb := math.Sqrt(pb)
	want0 := L * (1/s - 1/sb)
	want1 := L * (s - sa)
	if math.Abs(a0-want0) > 1e-12 || math.Abs(a1-want1) > 1e-12 {
		t.Fatalf("Amounts = (%v, %v), want (%v, %v)", a0, a1, want0, want1)
	}
	v := Value(p, pa, pb, L)
	if math.Abs(v-(a0*p+a1)) > 1e-12 {
		t.Fatalf("Value = %v, want %v", v, a0*p+a1)
	}
}

func TestLiquidityForRoundTrip(t *testing.T) {
	capital, p0, pa, pb := 10_000.0, 3184.20, 3024.99, 3343.41
	L := LiquidityFor(capital, p0, pa, pb)
	got := Value(p0, pa, pb, L)
	if math.Abs(got-capital) > 1e-6 {
		t.Fatalf("Value(LiquidityFor) = %v, want %v", got, capital)
	}
}

func TestConcentrationFivePercentBand(t *testing.T) {
	p := 3184.20
	pa, pb := p*0.95, p*1.05
	c := Concentration(p, pa, pb)
	if math.Abs(c-40.5) > 0.15 {
		t.Fatalf("Concentration(±5%%) = %v, want ≈ 40.5", c)
	}
}

func TestTimeInRangeSymmetricAroundSpot(t *testing.T) {
	p := 100.0
	sigma, days := 0.62, 30.0
	tir := TimeInRange(p, p*0.95, p*1.05, sigma, days)
	// A band symmetric in log-space around spot should be near-symmetric in probability.
	// ±5% is nearly log-symmetric; check it's well above the floor and below 1.
	if tir <= 0.01 || tir >= 1 {
		t.Fatalf("TimeInRange = %v, want (0.01, 1)", tir)
	}
	wide := TimeInRange(p, p*0.5, p*2, sigma, days)
	if wide <= tir {
		t.Fatalf("wider range TimeInRange %v should exceed ±5%% %v", wide, tir)
	}
	// Log-symmetric band: P(down) ≈ P(up) so a one-sided shift reduces TIR.
	shifted := TimeInRange(p*1.08, p*0.95, p*1.05, sigma, days)
	if shifted >= tir {
		t.Fatalf("shifted TimeInRange %v should be below centered %v", shifted, tir)
	}
}

func TestILAtUpperEdgeFivePercent(t *testing.T) {
	p := 100.0
	pa, pb := p*0.95, p*1.05
	L := LiquidityFor(1, p, pa, pb)
	il := ILAtUpperEdgePct(p, pa, pb, L)
	// Analytically ≈ −1.2% for a ±5% band moved to its upper edge.
	if math.Abs(il-(-1.2)) > 0.15 {
		t.Fatalf("IL at upper edge = %v%%, want ≈ −1.2%%", il)
	}
}

func TestFeeMultiplierRelativeNotAbsolute(t *testing.T) {
	p := 3184.20
	pa, pb := p*0.95, p*1.05
	m := FeeMultiplier(p, pa, pb)
	// ~40.5 / 13.7 ≈ 2.96, not 40.5 (which would print 200%+ APRs).
	if m > 12 || m < 0.05 {
		t.Fatalf("FeeMultiplier out of clamp: %v", m)
	}
	if math.Abs(m-40.5/RefConcentration) > 0.02 {
		t.Fatalf("FeeMultiplier = %v, want ~%v", m, 40.5/RefConcentration)
	}
}

func TestDeltaEqualsAmount0AtSpot(t *testing.T) {
	capital, p, pa, pb := 10_000.0, 3184.20, 3000.0, 3400.0
	d := Delta(capital, p, pa, pb)
	L := LiquidityFor(capital, p, pa, pb)
	a0, _ := Amounts(p, pa, pb, L)
	if math.Abs(d-a0) > 1e-12 {
		t.Fatalf("Delta = %v, Amounts.a0 = %v", d, a0)
	}
}
