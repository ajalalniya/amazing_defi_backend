package pool

import (
	"math"
	"testing"
)

func TestSkewedRanges(t *testing.T) {
	for _, c := range []struct {
		skew, pa, pb float64
	}{{0, 3000 / 1.1, 3300}, {1, 3000, 3000 * 1.21}, {-1, 3000 / 1.21, 3000}} {
		pa, pb, err := RangeFor(3000, 1.1, c.skew)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(pa-c.pa) > 1e-9 || math.Abs(pb-c.pb) > 1e-9 {
			t.Errorf("skew %v: [%v, %v], want [%v, %v]", c.skew, pa, pb, c.pa, c.pb)
		}
	}
	// Skew +1 sits on the lower edge: all ETH, so it keeps the upside.
	pa, pb, _ := RangeFor(3000, 1.15, 1)
	pos, _ := Mint(10_000, 3000, pa, pb)
	if x, y := pos.Amounts(3000); math.Abs(x*3000-10_000) > 1e-6 || y > 1e-9 {
		t.Errorf("skew +1 should start all ETH, got x=%v y=%v", x, y)
	}
	for _, bad := range [][3]float64{{0, 1.1, 0}, {3000, 1, 0}, {3000, 1.1, 1.5}} {
		if _, _, err := RangeFor(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("RangeFor%v should fail", bad)
		}
	}
}

func TestValueIsContinuousAcrossEdges(t *testing.T) {
	pos, _ := Mint(10_000, 3000, 3000/1.1, 3300)
	for _, edge := range []float64{pos.Pa, pos.Pb} {
		lo, hi := pos.Value(edge*(1-1e-12)), pos.Value(edge*(1+1e-12))
		if math.Abs(lo-hi) > 1e-6 {
			t.Errorf("value jumps at %v: %v vs %v", edge, lo, hi)
		}
	}
	// Outside the range: all one asset, no gamma.
	if pos.Gamma(2000) != 0 || pos.Gamma(4000) != 0 || pos.Delta(4000) != 0 {
		t.Error("outside the range gamma is 0, and above it delta is 0")
	}
	if _, err := Mint(10_000, 3000, 3300, 3000); err == nil {
		t.Error("an inverted range should not mint")
	}
}

func TestDecayRate(t *testing.T) {
	// Brief 1.3: σ = 60%, k = 1.1 decays about 97% a year, 0.27% a day.
	r := GammaDecayAPR(0.6, 1.1)
	if math.Abs(r-0.97) > 0.01 || math.Abs(r/365-0.0027) > 0.0001 {
		t.Errorf("decay %v/yr, %v/day", r, r/365)
	}
	if m := ConcentrationMultiplier(1.1); math.Abs(m-21.49) > 0.01 {
		t.Errorf("concentration multiplier %v", m)
	}
}
