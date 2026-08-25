package perps

import (
	"math"
	"testing"
)

func TestLiquidationLongShort(t *testing.T) {
	entry, lev := 100.0, 10.0
	long := Liquidation(entry, lev, 1)
	short := Liquidation(entry, lev, -1)
	if math.Abs(long-91) > 1e-9 {
		t.Fatalf("long liq %v want 91", long)
	}
	if math.Abs(short-109) > 1e-9 {
		t.Fatalf("short liq %v want 109", short)
	}
}

func TestRawPnLSigns(t *testing.T) {
	size := 1000.0
	if RawPnL(size, 110, 100, 1) <= 0 {
		t.Fatal("long should profit on rally")
	}
	if RawPnL(size, 110, 100, -1) >= 0 {
		t.Fatal("short should lose on rally")
	}
}
