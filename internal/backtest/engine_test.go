package backtest

import (
	"context"
	"math"
	"testing"

	"amazing/internal/simulate"
)

func TestAdditiveDecomposition(t *testing.T) {
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	eng := New(nil)
	res, err := eng.Run(context.Background(), Request{
		Days: 90, DriftPct: 12, VolPct: 65, Seed: 7,
		Legs: []Leg{
			{Kind: "lp", PoolID: "v4-eth-usdc-005", Capital: 50_000, Lower: 2850, Upper: 3550},
			{Kind: "short", PerpID: "ETH-USD", Capital: 10_000, Leverage: 3},
		},
	}, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	for i, leg := range res.Legs {
		if !Additive(leg) {
			t.Errorf("leg %d %s: net=%v carry=%v market=%v il=%v sum=%v",
				i, leg.Kind, leg.NetPnl, leg.Carry, leg.Market, leg.IL, leg.Carry+leg.Market+leg.IL)
		}
	}
}

func TestDeterminism(t *testing.T) {
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	eng := New(nil)
	req := Request{
		Days: 60, DriftPct: -10, VolPct: 40, Seed: 42,
		Legs: []Leg{
			{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 20_000, Lower: 2800, Upper: 3600},
			{Kind: "long", PerpID: "ETH-USD", Capital: 5_000, Leverage: 2},
		},
	}
	a, err := eng.Run(context.Background(), req, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	b, err := eng.Run(context.Background(), req, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	if a.NetPnl != b.NetPnl || a.Sharpe != b.Sharpe || len(a.Combined) != len(b.Combined) {
		t.Fatalf("non-deterministic: %#v vs %#v", a.NetPnl, b.NetPnl)
	}
	for i := range a.Combined {
		if a.Combined[i] != b.Combined[i] {
			t.Fatalf("combined[%d] %v != %v", i, a.Combined[i], b.Combined[i])
		}
	}
}

func TestShortLiquidatedInBull(t *testing.T) {
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	eng := New(nil)
	res, err := eng.Run(context.Background(), Request{
		Days: 90, DriftPct: 45, VolPct: 5, Seed: 1,
		Legs: []Leg{
			{Kind: "short", PerpID: "ETH-USD", Capital: 10_000, Leverage: 3},
		},
	}, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Legs[0].Liquidated {
		t.Fatalf("3× short in +45%% bull should liquidate; end=%v pnl=%v", res.Legs[0].End, res.Legs[0].NetPnl)
	}
}

func TestStablecoinNearZeroIL(t *testing.T) {
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	eng := New(nil)
	res, err := eng.Run(context.Background(), Request{
		Days: 90, DriftPct: 0, VolPct: 38, Seed: 3,
		Legs: []Leg{
			{Kind: "lp", PoolID: "v3-usdc-usdt-001", Capital: 50_000, Lower: 0.95, Upper: 1.05},
		},
	}, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	leg := res.Legs[0]
	if math.Abs(leg.IL)/leg.Capital > 0.005 {
		t.Fatalf("stablecoin IL = %v (%.3f%% of capital), want near zero", leg.IL, 100*leg.IL/leg.Capital)
	}
	if leg.TimeInRange < 0.9 {
		t.Fatalf("stablecoin time-in-range = %v, want high", leg.TimeInRange)
	}
}
