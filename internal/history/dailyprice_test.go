package history

import (
	"os"
	"testing"
	"time"
)

func TestDailyCloseAtReadsTheDayCloseFromDisk(t *testing.T) {
	const day = int64(86400)
	d0 := int64(20000) * day
	s := New(t.TempDir(), nil, nil)
	path := s.csvPath(BaseETHUSDC)
	rows := []Row{
		{Timestamp: d0 + 100, Price: 2000, SqrtPriceX96: "1"},
		{Timestamp: d0 + 50_000, Price: 2010, SqrtPriceX96: "1"}, // day 0 close
		{Timestamp: d0 + day + 10, Price: 2100, SqrtPriceX96: "1"},
		{Timestamp: d0 + 3*day + 10, Price: 1900, SqrtPriceX96: "1"}, // day 2 has no swaps
	}
	if err := writeCSV(path, rows); err != nil {
		t.Fatal(err)
	}
	at := func(i int64) (float64, bool) {
		p, ok, err := s.DailyCloseAt("v4-eth-usdc-005", time.Unix(d0+i*day+5, 0))
		if err != nil {
			t.Fatal(err)
		}
		return p, ok
	}
	for i, want := range []float64{2010, 2100, 2100, 1900} {
		if p, ok := at(int64(i)); !ok || p != want {
			t.Errorf("day %d: %v %v, want %v", i, p, ok, want)
		}
	}
	if _, ok := at(-1); ok {
		t.Error("a day before the CSV should not resolve")
	}
	if _, ok := at(4); ok {
		t.Error("a day after the CSV should not resolve")
	}
	if _, ok, _ := s.DailyCloseAt("no-such-pool", time.Unix(d0, 0)); ok {
		t.Error("an unmapped pool should not resolve")
	}

	// A CSV that grows is re-read rather than served stale.
	rows = append(rows, Row{Timestamp: d0 + 4*day + 10, Price: 1800, SqrtPriceX96: "1"})
	if err := writeCSV(path, rows); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(path, future, future)
	if p, ok := at(4); !ok || p != 1800 {
		t.Errorf("after append, day 4: %v %v, want 1800", p, ok)
	}
}
