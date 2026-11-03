package data

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"amazing/internal/sim/core"
)

func TestReadCSVResamplesAndFillsGaps(t *testing.T) {
	in := `timestamp,open,high,low,close,volume
2026-01-01T00:00:00Z,100,101,99,100.5,10
2026-01-01T00:01:00Z,100.5,103,100,102,5
1767226020,102,102,98,99,7
2026-01-01 00:15:00,99,99,99,99.5,1
`
	bars, err := ReadCSV(strings.NewReader(in), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 4 {
		t.Fatalf("got %d bars, want 4 (00:00, 00:05, 00:10 filled, 00:15)", len(bars))
	}
	b := bars[0]
	if b.Open != 100 || b.High != 103 || b.Low != 99 || b.Close != 102 || b.Volume != 15 {
		t.Errorf("first bucket %+v", b)
	}
	if bars[1].Close != 99 || bars[1].Low != 98 {
		t.Errorf("unix-seconds row: %+v", bars[1])
	}
	if f := bars[2]; f.Close != 99 || f.Volume != 0 || f.Open != 99 {
		t.Errorf("gap should be a flat bar at the previous close: %+v", f)
	}
	if bars[3].Time != time.Date(2026, 1, 1, 0, 15, 0, 0, time.UTC) {
		t.Errorf("bars must be evenly spaced: %v", bars[3].Time)
	}
}

func TestReadCSVErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no close":     "timestamp,open\n2026-01-01T00:00:00Z,1\n",
		"out of order": "timestamp,close\n2026-01-02T00:00:00Z,1\n2026-01-01T00:00:00Z,1\n",
		"bad number":   "timestamp,close\n2026-01-01T00:00:00Z,abc\n",
		"bad time":     "timestamp,close\nyesterday,1\n",
		"empty":        "timestamp,close\n",
	} {
		if _, err := ReadCSV(strings.NewReader(in), time.Minute); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCSVRoundTrip(t *testing.T) {
	bars := GBM(PathRNG(1, 0), 3000, 0, 0.6, 5*time.Minute, 50, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	bars[3].TrueRegime = core.Bullish
	var buf bytes.Buffer
	if err := WriteCSV(&buf, bars); err != nil {
		t.Fatal(err)
	}
	back, err := ReadCSV(&buf, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(bars) || back[3].TrueRegime != core.Bullish || back[10].Close != bars[10].Close {
		t.Errorf("round trip lost data: %d bars, %+v", len(back), back[3])
	}
}

func TestPathsAreDeterministicPerIndex(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := GBM(PathRNG(1, 7), 3000, 0, 0.6, 5*time.Minute, 100, start)
	b := GBM(PathRNG(1, 7), 3000, 0, 0.6, 5*time.Minute, 100, start)
	c := GBM(PathRNG(1, 8), 3000, 0, 0.6, 5*time.Minute, 100, start)
	if !reflect.DeepEqual(a, b) || reflect.DeepEqual(a, c) {
		t.Error("a path must depend on (seed, index) only")
	}
	dyn := map[core.Regime]Dynamics{}
	for _, r := range core.Regimes {
		dyn[r] = Dynamics{Sigma: 0.5, MeanDwell: time.Hour}
	}
	rg := RegimeGBM(PathRNG(2, 0), 3000, dyn, core.Range, 5*time.Minute, 2000, start)
	seen := map[core.Regime]bool{}
	for _, b := range rg {
		seen[b.TrueRegime] = true
	}
	if len(seen) != 5 {
		t.Errorf("a 1h mean dwell over a week should visit all regimes, saw %v", seen)
	}
}

func TestRamp(t *testing.T) {
	got := Ramp(nil, 3000, 3003, 1)
	got = Ramp(got, 3003, 3001, 1)
	want := []float64{3000, 3001, 3002, 3003, 3002, 3001}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Ramp = %v, want %v", got, want)
	}
}
