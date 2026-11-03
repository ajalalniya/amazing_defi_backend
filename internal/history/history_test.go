package history

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRPCEnv(t *testing.T) {
	if RPCEnv(ChainBase) != "BASE_RPC_URL" || RPCEnv(ChainArbitrum) != "ARBITRUM_RPC_URL" ||
		RPCEnv(ChainEthereum) != "ETHEREUM_RPC_URL" || RPCEnv(ChainBSC) != "BSC_RPC_URL" {
		t.Fatalf("unexpected RPC env names")
	}
}

func TestRpcForMissingURL(t *testing.T) {
	s := New(t.TempDir(), map[int]string{ChainBase: "https://example.invalid"}, nil)
	if _, err := s.rpcFor(ChainArbitrum); err == nil || err.Error() == "" {
		t.Fatal("expected missing Arbitrum RPC error")
	}
	if _, err := s.rpcFor(ChainBase); err != nil {
		t.Fatal(err)
	}
}

func TestMissingRangesFullWhenEmpty(t *testing.T) {
	got := MissingRanges(nil, 100, 200)
	if len(got) != 1 || got[0][0] != 100 || got[0][1] != 200 {
		t.Fatalf("got %#v", got)
	}
}

func TestMissingRangesBeforeAndAfter(t *testing.T) {
	b := [2]int64{1_000, 2_000}
	got := MissingRanges(&b, 100, 3_000)
	if len(got) != 2 {
		t.Fatalf("gaps = %#v", got)
	}
	if got[0][0] != 100 || got[0][1] != 940 {
		t.Fatalf("before gap %#v", got[0])
	}
	if got[1][0] != 2_060 || got[1][1] != 3_000 {
		t.Fatalf("after gap %#v", got[1])
	}
}

func TestMissingRangesCovered(t *testing.T) {
	b := [2]int64{100, 300}
	if gaps := MissingRanges(&b, 150, 250); len(gaps) != 0 {
		t.Fatalf("covered window should have no gaps, got %#v", gaps)
	}
}

func TestDailyClosesCarryForward(t *testing.T) {
	rows := []Row{
		{Timestamp: 100, Price: 10},
		{Timestamp: 90_000, Price: 12},
	}
	from, to := int64(0), int64(2*86400-1)
	got := DailyCloses(rows, from, to)
	if len(got) != 2 {
		t.Fatalf("len %d", len(got))
	}
	if got[0] != 10 || got[1] != 12 {
		t.Fatalf("got %v", got)
	}
}

func TestEnsureFetchesOnlyMissing(t *testing.T) {
	dir := t.TempDir()
	var calls [][2]int64
	s := New(dir, nil, nil)
	s.fetchFn = func(_ context.Context, _ Spec, start, end int64) ([]Row, error) {
		calls = append(calls, [2]int64{start, end})
		out := []Row{}
		for ts := start; ts <= end; ts += 86400 {
			out = append(out, Row{
				Timestamp: ts, Price: 3000, Amount0: -1, Amount1: 3000,
				Liquidity: 1e15, Tick: 200000, SqrtPriceX96: "1",
			})
		}
		if len(out) == 0 {
			out = []Row{{Timestamp: start, Price: 3000, Liquidity: 1e15, Tick: 200000, SqrtPriceX96: "1"}}
		}
		return out, nil
	}

	from, to := int64(1_700_000_000), int64(1_700_000_000+3*86400)
	if _, err := s.Ensure(context.Background(), "v3-eth-usdc-005", from, to); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("first ensure calls %#v", calls)
	}

	calls = nil
	// extend 2 days past the existing end
	to2 := to + 2*86400
	if _, err := s.Ensure(context.Background(), "v3-eth-usdc-005", from, to2); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("second ensure should fetch one gap, got %#v", calls)
	}
	if calls[0][0] <= to {
		t.Fatalf("refetched already stored data: %#v existing_to=%d", calls[0], to)
	}

	calls = nil
	if _, err := s.Ensure(context.Background(), "v3-eth-usdc-005", from+86400, to); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("covered window should not fetch, got %#v", calls)
	}

	path := filepath.Join(dir, "swaps_"+BaseETHUSDC.FileKey()+".csv")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureReportsProgress(t *testing.T) {
	s := New(t.TempDir(), nil, nil)
	s.fetchFn = func(_ context.Context, _ Spec, start, end int64) ([]Row, error) {
		return []Row{{Timestamp: start, Price: 3000, Liquidity: 1e15, Tick: 200000, SqrtPriceX96: "1"}}, nil
	}
	var got []Progress
	ctx := WithProgress(context.Background(), func(p Progress) { got = append(got, p) })
	from, to := int64(1_700_000_000), int64(1_700_000_000+86400)
	if _, err := s.Ensure(ctx, "v3-eth-usdc-005", from, to); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected progress events")
	}
	if got[0].Percent != 3 || got[0].Message != "Reading swaps from disk" {
		t.Fatalf("first event %+v, want the disk read at 3%%", got[0])
	}
	sawDownload := false
	for _, p := range got {
		if p.Stage == "download" {
			sawDownload = true
		}
	}
	if !sawDownload {
		t.Fatalf("an empty CSV must report a download: %+v", got)
	}
	last := got[len(got)-1]
	if last.Percent < 90 {
		t.Fatalf("last percent %d want >= 90", last.Percent)
	}
}

func TestGapPercent(t *testing.T) {
	lo, hi := gapPercent(0, 1)
	if lo != 8 || hi != 88 {
		t.Fatalf("single gap %d %d", lo, hi)
	}
	lo, hi = gapPercent(0, 2)
	if lo != 8 || hi != 48 {
		t.Fatalf("first of two %d %d", lo, hi)
	}
	lo, hi = gapPercent(1, 2)
	if lo != 48 || hi != 88 {
		t.Fatalf("second of two %d %d", lo, hi)
	}
}

func TestBindings(t *testing.T) {
	got := Bindings()
	if len(got) < 1 {
		t.Fatal("expected at least one live pool binding")
	}
	var saw bool
	for _, b := range got {
		if b.ID == "v3-eth-usdc-005" && strings.EqualFold(b.Spec.Address, BaseETHUSDC.Address) {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("missing v3-eth-usdc-005: %+v", got)
	}
}

func TestParseSlot0Price(t *testing.T) {
	sqrt := new(big.Int).Lsh(big.NewInt(1), 96)
	word := make([]byte, 32)
	sqrt.FillBytes(word)
	p, err := parseSlot0Price(word, Spec{Decimals0: 18, Decimals1: 18})
	if err != nil {
		t.Fatal(err)
	}
	if p < 0.999 || p > 1.001 {
		t.Fatalf("price %v want 1", p)
	}
}

func TestSqrtPriceX96Positive(t *testing.T) {
	if SqrtPriceX96ToPrice(wordInt(make([]byte, 32)), 18, 6) != 0 {
		t.Fatal("zero sqrt should be 0")
	}
}

// A window the CSV already covers must run with no RPC configured: the data is
// on disk, so there is nothing to download.
func TestEnsureNeedsNoRPCWhenCSVCoversWindow(t *testing.T) {
	dir := t.TempDir()
	const day = int64(86400)
	from, to := 20000*day, 20002*day+day-1
	body := csvHeader + "\n"
	// Swaps run a day past the requested window, as a real dump does: the CSV
	// bounds have to straddle [from, to] for it to count as covered.
	for ts := from; ts <= to+day; ts += 3600 {
		body += fmt.Sprintf("0.001,-3.1,%d,1e18,4573075199760799433668080,-195208,3100.5\n", ts)
	}
	path := filepath.Join(dir, "swaps_"+BaseETHUSDC.FileKey()+".csv")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// No RPCs at all.
	st := New(dir, nil, nil)
	w, err := st.Ensure(context.Background(), "v3-eth-usdc-005", from, to)
	if err != nil {
		t.Fatalf("Ensure with full local coverage: %v", err)
	}
	if len(w.Swaps) == 0 {
		t.Fatal("no swaps returned")
	}
	if len(w.DailyPrices) != 3 {
		t.Errorf("daily prices = %d, want 3", len(w.DailyPrices))
	}
}

// A window reaching past the CSV still needs an RPC, and must say so.
func TestEnsureStillReportsMissingRPCForGaps(t *testing.T) {
	dir := t.TempDir()
	const day = int64(86400)
	body := csvHeader + "\n" +
		fmt.Sprintf("0.001,-3.1,%d,1e18,4573075199760799433668080,-195208,3100.5\n", 20000*day)
	path := filepath.Join(dir, "swaps_"+BaseETHUSDC.FileKey()+".csv")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st := New(dir, nil, nil)
	_, err := st.Ensure(context.Background(), "v3-eth-usdc-005", 20000*day, 20010*day)
	if err == nil {
		t.Fatal("expected an error for a window past the CSV with no RPC")
	}
	if !strings.Contains(err.Error(), "RPC_URL") {
		t.Errorf("error %q should name the missing RPC_URL env var", err)
	}
}

// A window the CSV covers must say so, and must never be labelled a download:
// the UI shows these messages, and "download" there sent users looking for a
// fetch that was not happening.
func TestEnsureCoveredWindowReportsNoDownload(t *testing.T) {
	dir := t.TempDir()
	const day = int64(86400)
	from, to := 20000*day, 20002*day+day-1
	body := csvHeader + "\n"
	for ts := from; ts <= to+day; ts += 3600 {
		body += fmt.Sprintf("0.001,-3.1,%d,1e18,4573075199760799433668080,-195208,3100.5\n", ts)
	}
	if err := os.WriteFile(filepath.Join(dir, "swaps_"+BaseETHUSDC.FileKey()+".csv"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []Progress
	ctx := WithProgress(context.Background(), func(p Progress) { got = append(got, p) })
	if _, err := New(dir, nil, nil).Ensure(ctx, "v3-eth-usdc-005", from, to); err != nil {
		t.Fatal(err)
	}
	sawNoDownload := false
	for _, p := range got {
		if p.Stage == "download" || strings.Contains(strings.ToLower(p.Message), "downloading") {
			t.Errorf("covered window reported a download: %+v", p)
		}
		if strings.Contains(p.Message, "no download needed") {
			sawNoDownload = true
		}
	}
	if !sawNoDownload {
		t.Errorf("expected a 'no download needed' message, got %+v", got)
	}
}
