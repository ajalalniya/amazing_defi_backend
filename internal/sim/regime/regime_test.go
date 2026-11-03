package regime

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"amazing/internal/sim/core"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func walk(n int, drift, vol float64, seed uint64) []core.Bar {
	rng := rand.New(rand.NewPCG(seed, 0))
	bars := make([]core.Bar, n)
	p := 3000.0
	for i := range bars {
		if i > 0 {
			q := p * math.Exp(drift+vol*rng.NormFloat64())
			bars[i] = core.Bar{Time: t0.Add(time.Duration(i) * 5 * time.Minute), Open: p, High: math.Max(p, q) * 1.0005, Low: math.Min(p, q) * 0.9995, Close: q}
			p = q
			continue
		}
		bars[i] = core.Bar{Time: t0, Open: p, High: p, Low: p, Close: p}
	}
	return bars
}

func TestThresholdBoundaries(t *testing.T) {
	th := DefaultThresholds()
	for _, c := range []struct {
		z    float64
		want Regime
	}{{2, Bullish}, {1.5, TendBullish}, {0.51, TendBullish}, {0.5, Range}, {0, Range}, {-0.5, Range},
		{-0.51, TendBearish}, {-1.5, TendBearish}, {-1.51, Bearish}} {
		if got := th.Classify(c.z); got != c.want {
			t.Errorf("Classify(%v) = %s, want %s", c.z, got, c.want)
		}
		lo, hi := th.Bounds(c.want)
		if c.z < lo || c.z > hi {
			t.Errorf("z %v outside Bounds(%s) = [%v, %v]", c.z, c.want, lo, hi)
		}
	}
	if err := (Thresholds{Bull: 1, TendBull: 2, TendBear: -1, Bear: -2}).Validate(); err == nil {
		t.Error("misordered thresholds should not validate")
	}
}

// The O(1) ring-buffer z-score equals the direct computation at every bar.
func TestZScoreMatchesDirectComputation(t *testing.T) {
	const n = 36
	bars := walk(2000, 0.0001, 0.003, 1)
	d := NewZScore(n, DefaultThresholds())
	for i, b := range bars {
		d.Update(b)
		if i < n {
			if d.Score() != 0 {
				t.Fatalf("bar %d: z %v before warm-up", i, d.Score())
			}
			continue
		}
		sq := 0.0
		for j := i - n + 1; j <= i; j++ {
			r := math.Log(bars[j].Close / bars[j-1].Close)
			sq += r * r
		}
		want := math.Log(bars[i].Close/bars[i-n].Close) / math.Sqrt(sq)
		if math.Abs(d.Score()-want) > 1e-9 {
			t.Fatalf("bar %d: z %.12f, direct %.12f", i, d.Score(), want)
		}
	}
	d.Reset()
	if d.Update(bars[0]) != Range || d.Score() != 0 {
		t.Error("Reset should clear history")
	}
}

func share(d Detector, bars []core.Bar, want ...Regime) float64 {
	hit := 0
	for _, b := range bars {
		got := d.Update(b)
		for _, w := range want {
			if got == w {
				hit++
				break
			}
		}
	}
	return float64(hit) / float64(len(bars))
}

func TestDetectorsSeparateTrendFromChop(t *testing.T) {
	up := walk(3000, 0.002, 0.002, 2)
	down := walk(3000, -0.002, 0.002, 3)
	flat := walk(3000, 0, 0.002, 4)
	z := func() Detector { return NewZScore(288, DefaultThresholds()) }
	adx := func() Detector { return NewEMAADX(36, 144, 72, 20, 35) }
	for name, mk := range map[string]func() Detector{"zscore": z, "emaadx": adx} {
		if s := share(mk(), up[600:], Bullish, TendBullish); s < 0.8 {
			t.Errorf("%s: an uptrend reads bullish only %.0f%% of the time", name, s*100)
		}
		if s := share(mk(), down[600:], Bearish, TendBearish); s < 0.8 {
			t.Errorf("%s: a downtrend reads bearish only %.0f%% of the time", name, s*100)
		}
		if s := share(mk(), flat[600:], Bullish, Bearish); s > 0.15 {
			t.Errorf("%s: a driftless walk reads strongly trending %.0f%% of the time", name, s*100)
		}
	}
}

func TestOracle(t *testing.T) {
	bars := walk(10, 0, 0.01, 5)
	truth := []Regime{Range, Range, Bullish, Bullish, Bearish, Bearish, TendBearish, Range, Range, TendBullish}
	for i := range bars {
		bars[i].TrueRegime = truth[i]
	}
	o := NewOracle(OracleLabels(bars, 3, DefaultThresholds()))
	if !o.LooksAhead() {
		t.Fatal("oracle must report LooksAhead")
	}
	for i, b := range bars {
		if got := o.Update(b); got != truth[i] {
			t.Fatalf("bar %d: %s, want true label %s", i, got, truth[i])
		}
	}
	// Unlabelled: the label is the classified forward move.
	up := walk(400, 0.003, 0.001, 6)
	labels := OracleLabels(up, 50, DefaultThresholds())
	if labels[0] != Bullish {
		t.Errorf("a strong forward rise should label BULLISH, got %s", labels[0])
	}
}
