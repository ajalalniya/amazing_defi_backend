package backtest

import (
	"context"
	"math"
	"testing"

	"amazing/internal/simulate"
)

func TestRecenterRangeKeepsRatioAroundPrice(t *testing.T) {
	lo, hi := recenterRange(2000, 2397.0/1771.0)
	if math.Abs((lo+hi)/2-2000) > 1e-9 {
		t.Errorf("midpoint %v, want 2000", (lo+hi)/2)
	}
	if math.Abs(hi/lo-2397.0/1771.0) > 1e-12 {
		t.Errorf("ratio %v, want %v", hi/lo, 2397.0/1771.0)
	}
}

func TestRebalanceRecentresLPAndReopensPerps(t *testing.T) {
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	eng := New(nil)
	// A ±4% range through a volatile 60 days leaves the range many times.
	run := func(rebalance bool) Result {
		res, err := eng.Run(context.Background(), Request{
			Days: 60, DriftPct: -25, VolPct: 90, Seed: 11,
			Legs: []Leg{
				{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 10_000, Lower: 3057, Upper: 3312, Rebalance: rebalance},
				{Kind: "short", PerpID: "ETH-USD", Capital: 5_000, Leverage: 1},
				{Kind: "short", PerpID: "ETH-USD", Capital: 5_000, Hedge: true},
			},
		}, pools, perps)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	static, rb := run(false), run(true)
	lp := rb.Legs[0]
	if lp.Rebalances == 0 || rb.Rebalances != lp.Rebalances {
		t.Fatalf("expected rebalances, lp=%d total=%d", lp.Rebalances, rb.Rebalances)
	}
	if static.Rebalances != 0 || static.Legs[0].RangeLower != nil {
		t.Errorf("a leg without rebalance must not recentre: %d", static.Rebalances)
	}
	ratio := 3312.0 / 3057.0
	for _, d := range lp.RebalanceDays {
		p, lo, hi := rb.Price[d], lp.RangeLower[d], lp.RangeUpper[d]
		// Tick snapping moves each edge by at most one tick spacing (~0.1%).
		if math.Abs((lo+hi)/2/p-1) > 0.002 {
			t.Errorf("day %d: range %.2f..%.2f not centred on %.2f", d, lo, hi, p)
		}
		if math.Abs(hi/lo/ratio-1) > 0.003 {
			t.Errorf("day %d: ratio %.5f, want %.5f", d, hi/lo, ratio)
		}
		prevLo, prevHi := lp.RangeLower[d-1], lp.RangeUpper[d-1]
		if d > 1 && p >= prevLo && p <= prevHi {
			t.Errorf("day %d: recentred while %.2f was inside %.2f..%.2f", d, p, prevLo, prevHi)
		}
	}
	for _, i := range []int{1, 2} {
		leg := rb.Legs[i]
		if leg.Rebalances != lp.Rebalances {
			t.Errorf("perp %d reopened %d times, LP recentred %d", i, leg.Rebalances, lp.Rebalances)
		}
		for j, d := range leg.RebalanceDays {
			if d != lp.RebalanceDays[j] {
				t.Errorf("perp %d reopened on day %d, LP on %d", i, d, lp.RebalanceDays[j])
			}
		}
		if leg.RebalanceCost <= 0 {
			t.Errorf("perp %d paid nothing to close and reopen", i)
		}
	}
	// The fixed short reopens at its starting notional-to-LP ratio (1:2).
	short := rb.Legs[1]
	for _, d := range short.RebalanceDays {
		want := 5_000 * lp.Series[d] / 10_000
		got := short.Notional[d]
		// Series includes accrued fees; the target uses the position alone.
		if got > want || got < want*0.9 {
			t.Errorf("day %d: short reopened at %.2f, want about %.2f", d, got, want)
		}
	}
	for i, leg := range rb.Legs {
		if !Additive(leg) {
			t.Errorf("leg %d not additive: net=%v carry=%v market=%v il=%v", i, leg.NetPnl, leg.Carry, leg.Market, leg.IL)
		}
	}
}

// Fees are collected to the side: a recentre redeposits the position's value
// less the swap cost, and the fee series is never fed back into liquidity.
func TestFeesAreNotCompounded(t *testing.T) {
	b := openLP(2000, 1000, 1900, 2100, 10, 0, 0)
	nb, cost := b.recenter(2200, 2100.0/1900.0, 0.0005, 10, 0, 0)
	if got, want := nb.value(2200), b.value(2200)-cost; math.Abs(got-want) > 1e-6*want {
		t.Errorf("recentred value %v, want old value less cost %v", got, want)
	}

	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	res, err := New(nil).Run(context.Background(), Request{
		Days: 45, DriftPct: 5, VolPct: 70, Seed: 4,
		Legs: []Leg{{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 10_000, Lower: 3057, Upper: 3312, Rebalance: true}},
	}, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	lp := res.Legs[0]
	if len(lp.Fees) != len(lp.Series) || lp.Fees[0] != 0 {
		t.Fatalf("fees series %v", lp.Fees)
	}
	for d := 1; d < len(lp.Fees); d++ {
		if lp.Fees[d] < lp.Fees[d-1] {
			t.Errorf("day %d: cumulative fees fell %v -> %v", d, lp.Fees[d-1], lp.Fees[d])
		}
		if math.Abs(lp.Series[d]-lp.Fees[d]-lp.value[d]) > 1e-9 {
			t.Errorf("day %d: series %v != position %v + fees %v", d, lp.Series[d], lp.value[d], lp.Fees[d])
		}
	}
	if last := lp.Fees[len(lp.Fees)-1]; math.Abs(last-lp.Carry) > 1e-9 || last <= 0 {
		t.Errorf("final fees %v, carry %v", last, lp.Carry)
	}
}

// RebalanceSkipBelow recentres only on upside exits: a close below the range
// leaves the position in place, all ETH, until price recovers.
func TestRebalanceSkipBelowOnlyRecentresUpside(t *testing.T) {
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	run := func(skip bool) Result {
		res, err := New(nil).Run(context.Background(), Request{
			Days: 60, DriftPct: -25, VolPct: 90, Seed: 11,
			Legs: []Leg{
				{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 10_000, Lower: 3057, Upper: 3312, Rebalance: true, RebalanceSkipBelow: skip},
				{Kind: "short", PerpID: "ETH-USD", Capital: 5_000, Hedge: true},
			},
		}, pools, perps)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	both, upOnly := run(false), run(true)
	lp := upOnly.Legs[0]
	below := 0
	for d := 1; d < len(upOnly.Price); d++ {
		p, lo, hi := upOnly.Price[d], lp.RangeLower[d-1], lp.RangeUpper[d-1]
		recentred := lp.resets[d]
		switch {
		case p < lo:
			below++
			if recentred {
				t.Errorf("day %d: recentred on a close %.2f below the range %.2f..%.2f", d, p, lo, hi)
			}
		case p > hi && d < len(upOnly.Price)-1 && !recentred:
			t.Errorf("day %d: close %.2f above the range %.2f..%.2f was not recentred", d, p, lo, hi)
		}
	}
	if below == 0 {
		t.Fatal("the path never closed below the range; the test needs one")
	}
	if lp.Rebalances >= both.Legs[0].Rebalances {
		t.Errorf("skipping downside exits should recentre less: %d vs %d", lp.Rebalances, both.Legs[0].Rebalances)
	}
	if h := upOnly.Legs[1]; h.Rebalances != lp.Rebalances || !Additive(h) || !Additive(lp) {
		t.Errorf("the hedge should reopen with the LP and stay additive: %d vs %d", h.Rebalances, lp.Rebalances)
	}
}
