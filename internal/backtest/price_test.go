package backtest

import (
	"context"
	"math"
	"testing"

	"amazing/internal/history"
	"amazing/internal/simulate"
)

// The price series is what the chart draws under the equity curve, so it must
// line up point for point with Combined.
func TestOnchainResultCarriesPriceAndRange(t *testing.T) {
	from := int64(1_700_000_000)
	w := history.Window{
		Spec:        history.BaseETHUSDC,
		DailyPrices: []float64{3000, 3050, 2990},
		From:        from,
		To:          from + 2*86400 - 1,
		Swaps: []history.Row{
			{Timestamp: from + 100, Amount0: -1, Amount1: 3000, Tick: 80_000, Liquidity: 1e18, Price: 3000},
		},
	}
	eng := New(nil)
	eng.SetFeed(stubFeed{w: w})
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	res, err := eng.Run(context.Background(), Request{
		From: "2023-11-14", To: "2023-11-16",
		Legs: []Leg{
			{Kind: "short", PerpID: "ETH-USD", Capital: 1_000, Leverage: 2},
			{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 10_000, Lower: 2700, Upper: 3300},
		},
	}, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Price) != len(res.Combined) {
		t.Fatalf("price has %d points, combined %d", len(res.Price), len(res.Combined))
	}
	for i, want := range w.DailyPrices {
		if res.Price[i] != want {
			t.Errorf("price[%d] = %v, want the replayed close %v", i, res.Price[i], want)
		}
	}
	// The LP leg wins over the perp listed before it.
	if res.PriceLabel != "ETH/USDC" {
		t.Errorf("priceLabel = %q", res.PriceLabel)
	}
	lp := res.Legs[1]
	if lp.Lower <= 0 || lp.Upper <= lp.Lower {
		t.Fatalf("LP range not reported: %v..%v", lp.Lower, lp.Upper)
	}
	// Snapped to ticks, so close to but not exactly the request.
	if math.Abs(lp.Lower-2700)/2700 > 0.01 || math.Abs(lp.Upper-3300)/3300 > 0.01 {
		t.Errorf("snapped range %v..%v drifted from 2700..3300", lp.Lower, lp.Upper)
	}
	if res.Legs[0].Lower != 0 || res.Legs[0].Upper != 0 {
		t.Errorf("perp leg should carry no range: %+v", res.Legs[0])
	}
}

func TestScenarioResultCarriesPricePath(t *testing.T) {
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	res, err := New(nil).Run(context.Background(), Request{
		Days: 30, DriftPct: 20, VolPct: 50, Seed: 9,
		Legs: []Leg{{Kind: "lp", PoolID: "v4-eth-usdc-005", Capital: 10_000, Lower: 2800, Upper: 3600}},
	}, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Price) != len(res.Combined) {
		t.Fatalf("price has %d points, combined %d", len(res.Price), len(res.Combined))
	}
	var start float64
	for _, p := range pools {
		if p.ID == "v4-eth-usdc-005" {
			start = p.Price
		}
	}
	if res.Price[0] != start {
		t.Errorf("price[0] = %v, want the pool's spot %v", res.Price[0], start)
	}
	if res.PriceLabel != "ETH/USDC" {
		t.Errorf("priceLabel = %q", res.PriceLabel)
	}
}
