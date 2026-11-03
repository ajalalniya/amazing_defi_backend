package backtest

import (
	"context"
	"testing"

	"amazing/internal/history"
	"amazing/internal/simulate"
)

type stubFeed struct {
	w history.Window
}

func (s stubFeed) Ensure(context.Context, string, int64, int64) (history.Window, error) {
	return s.w, nil
}

func TestOnchainBacktestUsesSwapShareFees(t *testing.T) {
	from := int64(1_700_000_000)
	to := from + 2*86400 - 1
	w := history.Window{
		Spec:        history.BaseETHUSDC,
		DailyPrices: []float64{3000, 3000, 3010},
		From:        from,
		To:          to,
		Swaps: []history.Row{
			{Timestamp: from + 100, Amount0: -1, Amount1: 3000, Tick: 80_000, Liquidity: 1e18, Price: 3000},
			{Timestamp: from + 86400 + 100, Amount0: 1, Amount1: -3010, Tick: 80_000, Liquidity: 1e18, Price: 3010},
		},
	}
	eng := New(nil)
	eng.SetFeed(stubFeed{w: w})
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	res, err := eng.Run(context.Background(), Request{
		From: "2023-11-14", To: "2023-11-16",
		Legs: []Leg{{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 10_000, Lower: 2700, Upper: 3300}},
	}, pools, perps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "onchain" {
		t.Fatalf("source %q", res.Source)
	}
	if !Additive(res.Legs[0]) {
		t.Fatalf("not additive: %+v", res.Legs[0])
	}
}

func TestOnchainRequiresFeed(t *testing.T) {
	eng := New(nil)
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perps, _ := m.Perps(context.Background())
	_, err := eng.Run(context.Background(), Request{
		From: "2024-01-01", To: "2024-01-10",
		Legs: []Leg{{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 1, Lower: 1, Upper: 2}},
	}, pools, perps)
	if err == nil {
		t.Fatal("expected error without feed")
	}
}
