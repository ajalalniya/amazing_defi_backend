package backtest

import (
	"context"
	"math"
	"testing"

	"amazing/internal/history"
	"amazing/internal/simulate"
)

func runHedged(t *testing.T, hours int, band float64, rebalance bool) Result {
	t.Helper()
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	res, err := New(nil).Run(context.Background(), Request{
		Days: 60, DriftPct: -20, VolPct: 70, Seed: 9,
		Legs: []Leg{
			{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 10_000, Lower: 2700, Upper: 3700, Rebalance: rebalance},
			{Kind: "short", PerpID: "ETH-USD", Capital: 5_000, Hedge: true, HedgeIntervalHours: hours, HedgeBandPct: band},
		},
	}, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAttributionSumsToNetPnl(t *testing.T) {
	for _, tc := range []struct {
		hours     int
		band      float64
		rebalance bool
	}{{24, 0, false}, {1, 0, false}, {4, 2, true}} {
		res := runHedged(t, tc.hours, tc.band, tc.rebalance)
		a := res.Attribution
		if a == nil {
			t.Fatal("no attribution for a run with an LP leg")
		}
		sum := a.Fees - a.VolatilityCost - a.HedgeCosts - a.RebalanceCosts + a.Funding + a.Residual
		if math.Abs(sum-res.NetPnl) > 1e-6 || math.Abs(a.NetPnl-res.NetPnl) > 1e-6 {
			t.Errorf("%+v: parts sum to %.6f, attribution net %.6f, result net %.6f", tc, sum, a.NetPnl, res.NetPnl)
		}
		if a.VolatilityCost <= 0 {
			t.Errorf("%+v: a concentrated LP must pay a volatility cost, got %v", tc, a.VolatilityCost)
		}
		if !a.Hedged || a.HedgeTrades == 0 || a.MinMarginPct == nil {
			t.Errorf("%+v: hedge stats missing: %+v", tc, a)
		}
		if a.RealizedVolPct < 30 || a.RealizedVolPct > 200 {
			t.Errorf("%+v: realized vol %.1f%% for a 70%% vol path", tc, a.RealizedVolPct)
		}
		for i, leg := range res.Legs {
			if !Additive(leg) {
				t.Errorf("%+v leg %d not additive", tc, i)
			}
		}
	}
}

// Re-hedging every hour leaves almost no directional P/L: what is left of the
// LP's price P/L is its volatility cost. Re-hedging daily leaves the delta
// drift of a whole day.
func TestHourlyHedgeLeavesOnlyVolatilityCost(t *testing.T) {
	daily, hourly := runHedged(t, 24, 0, false).Attribution, runHedged(t, 1, 0, false).Attribution
	if math.Abs(hourly.Residual) > 0.02*hourly.VolatilityCost {
		t.Errorf("hourly hedge residual %.2f vs volatility cost %.2f", hourly.Residual, hourly.VolatilityCost)
	}
	if math.Abs(hourly.Residual) >= math.Abs(daily.Residual) {
		t.Errorf("hourly residual %.2f should be below daily %.2f", hourly.Residual, daily.Residual)
	}
	// The LP and its volatility cost do not depend on how the hedge trades.
	if math.Abs(daily.VolatilityCost-hourly.VolatilityCost) > 1e-9 || daily.Fees != hourly.Fees {
		t.Errorf("LP side changed with hedge frequency: %v/%v fees %v/%v", daily.VolatilityCost, hourly.VolatilityCost, daily.Fees, hourly.Fees)
	}
	if hourly.HedgeTrades <= daily.HedgeTrades || hourly.HedgeCosts <= daily.HedgeCosts {
		t.Errorf("hourly should trade more: %d/%.2f vs daily %d/%.2f", hourly.HedgeTrades, hourly.HedgeCosts, daily.HedgeTrades, daily.HedgeCosts)
	}
}

func TestHedgeBandSkipsSmallRehedges(t *testing.T) {
	all, banded := runHedged(t, 1, 0, false).Attribution, runHedged(t, 1, 3, false).Attribution
	if banded.HedgeTrades >= all.HedgeTrades/2 {
		t.Errorf("a 3%% band should skip most hourly re-hedges: %d vs %d", banded.HedgeTrades, all.HedgeTrades)
	}
	if banded.HedgeCosts >= all.HedgeCosts {
		t.Errorf("fewer re-hedges should cost less: %.2f vs %.2f", banded.HedgeCosts, all.HedgeCosts)
	}
}

func TestBridgeHourlyKeepsDailyCloses(t *testing.T) {
	rel := []float64{1, 1.03, 0.97, 1.01}
	h := bridgeHourly(rel, 0.8, 3)
	if len(h) != 3*HoursPerDay+1 {
		t.Fatalf("len %d", len(h))
	}
	for d, want := range rel {
		if h[d*HoursPerDay] != want {
			t.Errorf("close %d: %v, want %v", d, h[d*HoursPerDay], want)
		}
	}
	moved := false
	for j := 1; j < HoursPerDay; j++ {
		if h[j] != rel[0] && h[j] != rel[1] {
			moved = true
		}
	}
	if !moved {
		t.Error("intraday points should move between closes")
	}
}

func TestHourlyClosesLineUpWithDailyCloses(t *testing.T) {
	const day = int64(86400)
	from := int64(20000) * day
	rows := []history.Row{
		{Timestamp: from + 100, Price: 100},
		{Timestamp: from + day - 10, Price: 101},        // day 0 close
		{Timestamp: from + day + 3600 + 5, Price: 103},  // hour 2 of day 1
		{Timestamp: from + 2*day - 1, Price: 102},       // day 1 close
		{Timestamp: from + 2*day + 7200 + 1, Price: 99}, // day 2
	}
	h := hourlyCloses(rows, from, 2)
	daily := history.DailyCloses(rows, from, from+3*day-1)
	for d := 0; d <= 2; d++ {
		if h[d*HoursPerDay] != daily[d] {
			t.Errorf("close %d: hourly %v, daily %v", d, h[d*HoursPerDay], daily[d])
		}
	}
	if h[1] != 101 || h[2] != 103 {
		t.Errorf("hours 1-2 of day 1: %v %v, want 101 103", h[1], h[2])
	}
}
