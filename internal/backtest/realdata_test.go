package backtest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"amazing/internal/history"
	"amazing/internal/simulate"
)

// realDataDir is backend/data, where the Base ETH/USDC swap CSV lives. These
// tests are the only ones that exercise the real CSV path end to end; they
// skip when the dump is absent so `go test ./...` still passes without it.
const realDataDir = "../../data"

// minRealRows guards against running against the old 16-hour sample.
const minRealBytes = 100 << 20

func realFeed(t *testing.T) *history.Store {
	t.Helper()
	path := filepath.Join(realDataDir, "swaps_"+history.BaseETHUSDC.FileKey()+".csv")
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < minRealBytes {
		t.Skipf("no full swap dump at %s (run the raw->human conversion first)", path)
	}
	// The window stays inside the CSV bounds, so MissingRanges finds no gaps
	// and this URL is never dialled.
	return history.New(realDataDir, map[int]string{8453: "http://127.0.0.1:1"}, nil)
}

// TestRealWindowRunsAndCosts records the cost of one on-chain backtest over the
// real dump. The load is a fixed cost per request: loadCSV parses every row
// before filterWindow narrows it, so a 7-day window costs about as much as 85.
func TestRealWindowRunsAndCosts(t *testing.T) {
	eng := New(nil)
	eng.SetFeed(realFeed(t))
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perpList, _ := m.Perps(context.Background())

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	t0 := time.Now()
	res, err := eng.Run(context.Background(), Request{
		From: "2026-01-19", To: "2026-04-14",
		Legs: []Leg{
			{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 50_000, Lower: 2600, Upper: 3400},
			{Kind: "short", PerpID: "ETH-USD", Capital: 10_000, Leverage: 3},
		},
	}, pools, perpList)
	el := time.Since(t0)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Logf("days=%d elapsed=%s heapLive=%d MiB sysFromOS=%d MiB",
		res.Days, el.Round(time.Millisecond), after.HeapAlloc>>20, after.Sys>>20)
	t.Logf("net=%.2f ret=%.2f%% fees=%.2f il=%.2f funding=%.2f perpMkt=%.2f",
		res.NetPnl, res.ReturnPct, res.FeesEarned, res.ImpermanentLoss, res.Funding, res.PerpMarketPnl)

	if res.Source != "onchain" {
		t.Errorf("source = %q, want onchain", res.Source)
	}
	if res.Days < 80 {
		t.Errorf("days = %d, want ~85 for this window", res.Days)
	}
	if res.FeesEarned <= 0 {
		t.Errorf("fees = %v, want positive from real swaps", res.FeesEarned)
	}
	for i, l := range res.Legs {
		if !Additive(l) {
			t.Errorf("leg %d not additive: %+v", i, l)
		}
	}
}

// TestRealWideRangeOutperformsNarrow cross-checks the engine against a known
// move: ETH falls 3331 -> 2326 over this window. A range that holds the whole
// fall stays in range and earns more fees at less impermanent loss than one
// that drops out the bottom a third of the way in.
func TestRealWideRangeOutperformsNarrow(t *testing.T) {
	eng := New(nil)
	eng.SetFeed(realFeed(t))
	m := simulate.NewMock(nil)
	pools, _ := m.Pools(context.Background())
	perpList, _ := m.Perps(context.Background())

	run := func(lo, hi float64) Result {
		t.Helper()
		res, err := eng.Run(context.Background(), Request{
			From: "2026-01-19", To: "2026-04-14",
			Legs: []Leg{{Kind: "lp", PoolID: "v3-eth-usdc-005", Capital: 50_000, Lower: lo, Upper: hi}},
		}, pools, perpList)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	narrow, wide := run(2600, 3400), run(1600, 3500)
	t.Logf("narrow 2600-3400: tir=%.3f fees=%.2f il=%.2f net=%.2f",
		narrow.Legs[0].TimeInRange, narrow.FeesEarned, narrow.ImpermanentLoss, narrow.NetPnl)
	t.Logf("wide   1600-3500: tir=%.3f fees=%.2f il=%.2f net=%.2f",
		wide.Legs[0].TimeInRange, wide.FeesEarned, wide.ImpermanentLoss, wide.NetPnl)

	if wide.Legs[0].TimeInRange <= narrow.Legs[0].TimeInRange {
		t.Errorf("wide time-in-range %v should exceed narrow %v", wide.Legs[0].TimeInRange, narrow.Legs[0].TimeInRange)
	}
	if wide.FeesEarned <= narrow.FeesEarned {
		t.Errorf("wide fees %v should exceed narrow %v over this fall", wide.FeesEarned, narrow.FeesEarned)
	}
	if wide.ImpermanentLoss <= narrow.ImpermanentLoss {
		t.Errorf("wide IL %v should be less negative than narrow %v", wide.ImpermanentLoss, narrow.ImpermanentLoss)
	}
}
