package history

import (
	"math"
	"testing"
)

func TestUniqueBindings(t *testing.T) {
	got := UniqueBindings()
	if len(got) != 2 {
		t.Fatalf("unique %d want 2 (Base + Arbitrum), got %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, b := range got {
		k := b.Spec.Address
		if seen[k] {
			t.Fatalf("duplicate address %s", k)
		}
		seen[k] = true
		if b.ID == "v4-eth-usdc-005" {
			t.Fatal("v4 alias should not appear in unique catalog")
		}
	}
}

func TestNetworkLabel(t *testing.T) {
	if NetworkLabel(ChainBase) != "Base" || NetworkLabel(ChainArbitrum) != "Arbitrum" {
		t.Fatal(NetworkLabel(ChainBase), NetworkLabel(ChainArbitrum))
	}
}

func TestTvlAndVolumeUSD(t *testing.T) {
	spec := BaseETHUSDC
	tvl := tvlUSD(10, 50_000, 3000, spec)
	if math.Abs(tvl-80_000) > 1e-6 {
		t.Fatalf("tvl %v want 80000", tvl)
	}
	vol := volumeUSD([]Row{{Amount1: -1200}, {Amount1: 800}}, spec)
	if vol != 2000 {
		t.Fatalf("vol %v", vol)
	}
	apr := feeAPR(10_000_000, 100_000_000, 0.0005)
	want := 10_000_000 * 0.0005 / 100_000_000 * 365 * 100
	if math.Abs(apr-want) > 1e-9 {
		t.Fatalf("apr %v want %v", apr, want)
	}
}

func TestParsePoolStats(t *testing.T) {
	raw := []byte(`{"data":{"pool":{"totalValueLockedUSD":"123456.7","poolDayData":[{"volumeUSD":"8900.5","open":"3000","close":"3060"}]}}}`)
	s, err := parsePoolStats(raw)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(s.TVL-123456.7) > 0.1 || math.Abs(s.Volume24h-8900.5) > 0.1 {
		t.Fatalf("%+v", s)
	}
	if math.Abs(s.Change24h-2) > 0.01 {
		t.Fatalf("change %v", s.Change24h)
	}
}

func TestPadAddress(t *testing.T) {
	got := padAddress("0xabc")
	if len(got) != 64 || got[len(got)-3:] != "abc" {
		t.Fatalf("%q", got)
	}
}
