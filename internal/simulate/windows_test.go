package simulate

import "testing"

func TestLiquidityBinsNormalised(t *testing.T) {
	bins := LiquidityBins("v4-eth-usdc-005", 3184.20, 0.62, 2000, 5000, 48)
	if len(bins) != 48 {
		t.Fatalf("len %d", len(bins))
	}
	max := 0.0
	for _, b := range bins {
		if b.Depth > max {
			max = b.Depth
		}
		if b.Price <= 0 {
			t.Fatalf("non-positive price %v", b.Price)
		}
	}
	if max != 1 {
		t.Fatalf("max depth %v want 1", max)
	}
	// log-spaced: first < last, mid near geometric mean
	if bins[0].Price >= bins[len(bins)-1].Price {
		t.Fatal("expected increasing log-spaced prices")
	}
}

func TestMulberryDeterminism(t *testing.T) {
	a, b := Mulberry32(7), Mulberry32(7)
	for i := 0; i < 20; i++ {
		if a.Next() != b.Next() {
			t.Fatal("rng not deterministic")
		}
	}
}
