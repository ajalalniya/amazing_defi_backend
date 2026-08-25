package portfolio

import (
	"context"
	"testing"
	"time"

	"amazing/internal/market"
	"amazing/internal/simulate"
)

type staticSrc struct {
	pools []market.Pool
	perps []market.Perp
}

func (s staticSrc) Pools(context.Context) ([]market.Pool, error) { return s.pools, nil }
func (s staticSrc) Perps(context.Context) ([]market.Perp, error) { return s.perps, nil }

func TestLPMarkInsideAboveBelow(t *testing.T) {
	pools := simulate.SeedPools()
	perps := simulate.SeedPerps()
	po, pe := indexMarkets(pools, perps)
	eng := NewEngine(nil)

	base := market.Position{
		ID: "x", Owner: "t", Platform: "v3", Kind: "lp", PoolID: "v3-eth-usdc-005",
		Market: "ETH/USDC", Capital: 10_000, Entry: 3184.20, Lower: 3000, Upper: 3400,
		OpenedAt: time.Now(),
	}
	inside := eng.Mark(base, po, pe)
	if inside.InRange == nil || !*inside.InRange {
		t.Fatalf("spot inside range should be in-range")
	}

	low := base
	low.Lower, low.Upper = 3500, 4000 // spot below range
	below := eng.Mark(low, po, pe)
	if below.InRange == nil || *below.InRange {
		t.Fatalf("spot below range should be out")
	}

	high := base
	high.Lower, high.Upper = 2000, 2500 // spot above range
	above := eng.Mark(high, po, pe)
	if above.InRange == nil || *above.InRange {
		t.Fatalf("spot above range should be out")
	}
}

func TestPerpPnLSigns(t *testing.T) {
	pools := simulate.SeedPools()
	perps := simulate.SeedPerps()
	po, pe := indexMarkets(pools, perps)
	eng := NewEngine(nil)
	eth := pe["ETH-USD"]

	long := market.Position{
		Kind: "long", Platform: "gmx", PerpID: "ETH-USD", Collateral: 1000, Leverage: 2,
		Entry: eth.Price * 0.9, Funding: 0,
	}
	short := long
	short.Kind = "short"
	short.Entry = eth.Price * 1.1

	lm := eng.Mark(long, po, pe)
	sm := eng.Mark(short, po, pe)
	if lm.PnL <= 0 {
		t.Fatalf("long below mark should profit, pnl=%v", lm.PnL)
	}
	if sm.PnL <= 0 {
		t.Fatalf("short above mark should profit, pnl=%v", sm.PnL)
	}
}

func TestRollupsSumOfParts(t *testing.T) {
	src := staticSrc{pools: simulate.SeedPools(), perps: simulate.SeedPerps()}
	eng := NewEngine(nil)
	pos := SeedPositions(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
	snap, err := eng.Snapshot(context.Background(), pos, src)
	if err != nil {
		t.Fatal(err)
	}
	var lp, perp, fees, fund, total float64
	var cap float64
	for _, p := range snap.Positions {
		total += p.PnL
		if p.Kind == "lp" {
			lp += p.PnL
			fees += p.Fees
			cap += p.Capital
		} else {
			perp += p.PnL
			fund += p.Funding
			cap += p.Collateral
		}
	}
	r := snap.Rollups
	if abs(r.Total-total) > 1e-6 || abs(r.LpPnl-lp) > 1e-6 || abs(r.PerpPnl-perp) > 1e-6 {
		t.Fatalf("rollups %+v vs parts total=%v lp=%v perp=%v", r, total, lp, perp)
	}
	if abs(r.FeesCollected-fees) > 1e-6 || abs(r.FundingPaid-fund) > 1e-6 {
		t.Fatalf("fees/funding mismatch")
	}
	if abs(r.CapitalDeployed-cap) > 1e-6 {
		t.Fatalf("capital %v vs %v", r.CapitalDeployed, cap)
	}
	if abs(r.LpPnl+r.PerpPnl-r.Total) > 1e-6 {
		t.Fatalf("lp+perp != total")
	}
}
