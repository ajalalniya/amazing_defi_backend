package clmath

import (
	"math"
	"testing"
)

func TestPriceTickRoundTrip(t *testing.T) {
	price := 3184.20
	spacing := 10
	tick := PriceToClosestTick(price, spacing, 0, 0)
	if tick%spacing != 0 {
		t.Fatalf("tick %d is not a multiple of spacing %d", tick, spacing)
	}
	back := TickToPrice(tick, 0, 0)
	rel := math.Abs(back-price) / price
	// One spacing is 10 ticks ≈ 0.10% in price; rounding error should be well inside that.
	if rel > 0.002 {
		t.Fatalf("round-trip price %v vs %v (rel %v)", back, price, rel)
	}
}

func TestPriceToClosestTickDecimalsETHUSDC(t *testing.T) {
	// WETH (18) / USDC (6) at 3184 USDC per ETH.
	tick := PriceToClosestTick(3184.20, 10, 18, 6)
	if tick%10 != 0 {
		t.Fatalf("tick %d not aligned to spacing 10", tick)
	}
	back := TickToPrice(tick, 18, 6)
	if math.Abs(back-3184.20)/3184.20 > 0.002 {
		t.Fatalf("ETH/USDC round-trip %v, want ~3184.20", back)
	}
}

func TestSnapRangeOrdersTicks(t *testing.T) {
	tl, tu, lo, hi := SnapRange(3025, 3343, 10, 0, 0)
	if tu <= tl {
		t.Fatalf("tickUpper %d should exceed tickLower %d", tu, tl)
	}
	if hi <= lo {
		t.Fatalf("upper price %v should exceed lower %v", hi, lo)
	}
}

func TestTickSpacingTiers(t *testing.T) {
	if TickSpacing(1) != 1 || TickSpacing(5) != 10 || TickSpacing(30) != 60 || TickSpacing(100) != 200 {
		t.Fatalf("unexpected spacing: 1→%d 5→%d 30→%d 100→%d", TickSpacing(1), TickSpacing(5), TickSpacing(30), TickSpacing(100))
	}
}

func TestDepositAmountsInteriorMatchesLiquidityFor(t *testing.T) {
	spot, value, lower, upper := 3184.20, 10_000.0, 3024.99, 3343.41
	a0, a1, L := DepositAmounts(spot, value, lower, upper)
	wantL := LiquidityFor(value, spot, lower, upper)
	if math.Abs(L-wantL) > 1e-6 {
		t.Fatalf("L = %v, LiquidityFor = %v", L, wantL)
	}
	got0, got1 := Amounts(spot, lower, upper, L)
	if math.Abs(a0-got0) > 1e-6 || math.Abs(a1-got1) > 1e-6 {
		t.Fatalf("amounts (%v, %v) vs Amounts (%v, %v)", a0, a1, got0, got1)
	}
	if math.Abs(a0*spot+a1-value) > 1e-6 {
		t.Fatalf("deployed value %v, want %v", a0*spot+a1, value)
	}
}

func TestDepositAmountsBelowRangeIsToken0Only(t *testing.T) {
	a0, a1, L := DepositAmounts(80, 1000, 90, 110)
	if a1 != 0 || a0 <= 0 || L <= 0 {
		t.Fatalf("below range: a0=%v a1=%v L=%v", a0, a1, L)
	}
	if math.Abs(a0*80-1000) > 1e-9 {
		t.Fatalf("below-range token0 value %v, want 1000", a0*80)
	}
}

func TestDepositAmountsAboveRangeIsToken1Only(t *testing.T) {
	a0, a1, L := DepositAmounts(120, 1000, 90, 110)
	if a0 != 0 || a1 != 1000 || L <= 0 {
		t.Fatalf("above range: a0=%v a1=%v L=%v", a0, a1, L)
	}
}
