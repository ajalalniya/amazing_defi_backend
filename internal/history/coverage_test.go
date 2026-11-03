package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempCSV(t *testing.T, dir string, spec Spec, rows []string) string {
	t.Helper()
	path := filepath.Join(dir, "swaps_"+spec.FileKey()+".csv")
	body := csvHeader + "\n" + strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func row(ts int64) string {
	return fmt.Sprintf("0.001,-3.1,%d,1e18,4573075199760799433668080,-195208,3100.5", ts)
}

func TestCSVBounds(t *testing.T) {
	dir := t.TempDir()
	path := writeTempCSV(t, dir, BaseETHUSDC, []string{row(1000), row(2000), row(3000)})
	from, to, err := csvBounds(path)
	if err != nil {
		t.Fatal(err)
	}
	if from != 1000 || to != 3000 {
		t.Fatalf("bounds = %d..%d, want 1000..3000", from, to)
	}
}

// The tail read starts mid-row on any real file; the leading fragment must be
// discarded rather than parsed.
func TestCSVBoundsLongFileTailRead(t *testing.T) {
	dir := t.TempDir()
	rows := make([]string, 0, 20000)
	for i := 0; i < 20000; i++ {
		rows = append(rows, row(int64(1_700_000_000+i)))
	}
	path := writeTempCSV(t, dir, BaseETHUSDC, rows)
	fi, _ := os.Stat(path)
	if fi.Size() <= tailBytes {
		t.Fatalf("fixture %d bytes is not larger than the %d-byte tail read", fi.Size(), tailBytes)
	}
	from, to, err := csvBounds(path)
	if err != nil {
		t.Fatal(err)
	}
	if from != 1_700_000_000 || to != 1_700_019_999 {
		t.Fatalf("bounds = %d..%d", from, to)
	}
}

func TestCSVBoundsRejectsUnordered(t *testing.T) {
	dir := t.TempDir()
	path := writeTempCSV(t, dir, BaseETHUSDC, []string{row(3000), row(1000)})
	if _, _, err := csvBounds(path); err == nil {
		t.Fatal("expected an out-of-order error")
	}
}

func TestCoverageTrimsPartialDays(t *testing.T) {
	const day = int64(86400)
	b := Binding{ID: "v3-eth-usdc-005", Spec: BaseETHUSDC}
	// 2026-01-18 16:32:45 .. 2026-04-15 09:06:07 — both ends partial, so the
	// usable span is 2026-01-19 .. 2026-04-14.
	c, ok := coverageOf(b, 1768753965, 1776243967)
	if !ok {
		t.Fatal("expected coverage")
	}
	if c.From != "2026-01-18" || c.To != "2026-04-15" {
		t.Errorf("raw span %s..%s", c.From, c.To)
	}
	if c.FullFrom != "2026-01-19" || c.FullTo != "2026-04-14" {
		t.Errorf("full span %s..%s, want 2026-01-19..2026-04-14", c.FullFrom, c.FullTo)
	}
	if c.Days != 86 {
		t.Errorf("days = %d, want 86", c.Days)
	}
	if c.Pair != "ETH/USDC" || c.ChainID != 8453 {
		t.Errorf("pair/chain = %s/%d", c.Pair, c.ChainID)
	}

	// Exact day boundaries keep both days.
	c, ok = coverageOf(b, 20000*day, 20002*day+day-1)
	if !ok {
		t.Fatal("expected coverage")
	}
	if c.Days != 3 {
		t.Errorf("aligned span days = %d, want 3", c.Days)
	}

	// Less than one whole day is not usable.
	if _, ok := coverageOf(b, 20000*day+60, 20001*day-60); ok {
		t.Error("a sub-day span should report no coverage")
	}
}

func TestStoreCoverage(t *testing.T) {
	dir := t.TempDir()
	const day = int64(86400)
	writeTempCSV(t, dir, BaseETHUSDC, []string{row(20000 * day), row(20003*day + day - 1)})
	st := New(dir, nil, nil)

	cov := st.Coverage()
	if len(cov) != 1 {
		t.Fatalf("got %d coverage entries, want 1 (only the pool with a CSV)", len(cov))
	}
	if cov[0].PoolID != "v3-eth-usdc-005" {
		t.Errorf("poolId = %q", cov[0].PoolID)
	}
	if cov[0].Days != 4 {
		t.Errorf("days = %d, want 4", cov[0].Days)
	}
	if cov[0].Bytes == 0 {
		t.Error("bytes should be set")
	}
}

func TestStoreCoverageEmptyDir(t *testing.T) {
	if cov := New(t.TempDir(), nil, nil).Coverage(); len(cov) != 0 {
		t.Fatalf("got %d entries for an empty dir", len(cov))
	}
}

// One pool address carries several market ids; a leg naming the v4 alias must
// still match the coverage row, which is keyed on the v3 id.
func TestCoverageListsPoolAliases(t *testing.T) {
	const day = int64(86400)
	b := Binding{ID: "v3-eth-usdc-005", Spec: BaseETHUSDC}
	c, ok := coverageOf(b, 20000*day, 20002*day+day-1)
	if !ok {
		t.Fatal("expected coverage")
	}
	var sawV3, sawV4 bool
	for _, id := range c.PoolIDs {
		switch id {
		case "v3-eth-usdc-005":
			sawV3 = true
		case "v4-eth-usdc-005":
			sawV4 = true
		case "v3-eth-usdc-005-arb":
			t.Errorf("arbitrum id listed under the Base pool: %v", c.PoolIDs)
		}
	}
	if !sawV3 || !sawV4 {
		t.Errorf("poolIds = %v, want both the v3 and v4 Base ids", c.PoolIDs)
	}
}
