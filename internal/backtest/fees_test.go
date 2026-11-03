package backtest

import (
	"math"
	"testing"
)

func TestLPMultiplierProtocolNibble(t *testing.T) {
	// 0x44 → low nibble 4 and high nibble 4 → LP keeps 75%.
	if got := LPMultiplier(DefaultFeeProtocol % 16); math.Abs(got-0.75) > 1e-12 {
		t.Fatalf("token0-in keep = %v, want 0.75", got)
	}
	if got := LPMultiplier(DefaultFeeProtocol >> 4); math.Abs(got-0.75) > 1e-12 {
		t.Fatalf("token1-in keep = %v, want 0.75", got)
	}
	if LPMultiplier(0) != 1 {
		t.Fatalf("protocol 0 should keep 100%%")
	}
}

func TestEstimateFeesShareFormula(t *testing.T) {
	// One token0-in swap inside the range.
	// share = 100/1000 = 0.1; keep = 0.75; feeRate = 0.0005
	// fees0 = 100 * 0.0005 * 0.1 * 0.75 = 0.00375
	swaps := []Swap{{
		Timestamp: 50,
		Amount0:   100,
		Amount1:   -50,
		Tick:      100,
		Liquidity: 1000,
	}}
	f0, f1 := EstimateFees(swaps, 0, 100, 90, 110, 100, 0.0005, DefaultFeeProtocol)
	if math.Abs(f0-0.00375) > 1e-12 {
		t.Fatalf("fees0 = %v, want 0.00375", f0)
	}
	if f1 != 0 {
		t.Fatalf("fees1 = %v, want 0", f1)
	}
}

func TestEstimateFeesToken1In(t *testing.T) {
	swaps := []Swap{{
		Timestamp: 50,
		Amount0:   -2,
		Amount1:   200,
		Tick:      100,
		Liquidity: 500,
	}}
	// share = 50/500 = 0.1; fees1 = 200 * 0.0005 * 0.1 * 0.75 = 0.0075
	_, f1 := EstimateFees(swaps, 0, 100, 90, 110, 50, 0.0005, DefaultFeeProtocol)
	if math.Abs(f1-0.0075) > 1e-12 {
		t.Fatalf("fees1 = %v, want 0.0075", f1)
	}
}

func TestEstimateFeesIgnoresOutOfRangeTick(t *testing.T) {
	swaps := []Swap{{
		Timestamp: 50,
		Amount0:   100,
		Amount1:   -50,
		Tick:      200,
		Liquidity: 1000,
	}}
	f0, f1 := EstimateFees(swaps, 0, 100, 90, 110, 100, 0.0005, DefaultFeeProtocol)
	if f0 != 0 || f1 != 0 {
		t.Fatalf("out-of-range tick should earn 0, got %v %v", f0, f1)
	}
}

func TestEstimateFeesIgnoresOutsideWindow(t *testing.T) {
	swaps := []Swap{{
		Timestamp: 999,
		Amount0:   100,
		Amount1:   -50,
		Tick:      100,
		Liquidity: 1000,
	}}
	f0, f1 := EstimateFees(swaps, 0, 100, 90, 110, 100, 0.0005, DefaultFeeProtocol)
	if f0 != 0 || f1 != 0 {
		t.Fatalf("outside window should earn 0, got %v %v", f0, f1)
	}
}

func TestEstimateFeesClipsShareAtOne(t *testing.T) {
	swaps := []Swap{{
		Timestamp: 50,
		Amount0:   100,
		Amount1:   -50,
		Tick:      100,
		Liquidity: 10,
	}}
	// positionL 100 / pool 10 → clipped to 1
	// fees0 = 100 * 0.0005 * 1 * 0.75 = 0.0375
	f0, _ := EstimateFees(swaps, 0, 100, 90, 110, 100, 0.0005, DefaultFeeProtocol)
	if math.Abs(f0-0.0375) > 1e-12 {
		t.Fatalf("clipped fees0 = %v, want 0.0375", f0)
	}
}
