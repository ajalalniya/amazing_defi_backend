package simulate

import (
	"math"
	"time"

	"amazing/internal/market"
)

const (
	SeriesN      = 150
	TickSeconds  = 10
	tickVolScale = 10.0 / (365 * 24 * 3600) // sigma * sqrt(this) * N(0,1)
)

func TickVol() float64 {
	return math.Sqrt(tickVolScale)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Window is a 150-sample sliding price buffer that always ends at live spot.
type Window struct {
	Hist []float64
	SD   float64
	Step int
}

func seriesKey(id, tf string) string { return id + ":" + tf }

func newWindow(id string, price, sigma float64, tf market.Timeframe) *Window {
	dt := tf.Days / 365 / SeriesN
	sd := sigma * math.Sqrt(dt)
	drift := float64(int(HashStr(id)%9)-4) * 0.09
	rnd := Mulberry32(HashStr(id + tf.ID))
	rel := make([]float64, SeriesN)
	rel[0] = 1
	for i := 1; i < SeriesN; i++ {
		rel[i] = rel[i-1] * math.Exp((drift-sigma*sigma/2)*dt+sd*Gauss(rnd))
	}
	last := rel[SeriesN-1]
	hist := make([]float64, SeriesN)
	for i, v := range rel {
		hist[i] = v / last * price
	}
	step := clampInt(int(math.Round(TickSeconds/(tf.Days*86400/SeriesN))), 1, SeriesN)
	return &Window{Hist: hist, SD: sd, Step: step}
}

// Advance slides the window forward to the new spot: append `step` interpolated
// samples (last one exactly spot) and drop `step` off the front. Never rescale.
func (w *Window) Advance(spot float64, rnd *RNG) {
	if w == nil || len(w.Hist) == 0 {
		return
	}
	from := w.Hist[len(w.Hist)-1]
	for i := 1; i <= w.Step; i++ {
		lin := from + (spot-from)*float64(i)/float64(w.Step)
		if i == w.Step {
			w.Hist = append(w.Hist, spot)
		} else {
			w.Hist = append(w.Hist, lin*math.Exp(w.SD*Gauss(rnd)))
		}
	}
	w.Hist = w.Hist[w.Step:]
}

func candlesFromHist(hist []float64, tf market.Timeframe, now time.Time) []market.Candle {
	n := len(hist)
	if n == 0 {
		return nil
	}
	interval := time.Duration(tf.Days*86400/float64(n)) * time.Second
	out := make([]market.Candle, n)
	for i, c := range hist {
		o := c
		if i > 0 {
			o = hist[i-1]
		}
		hi, lo := o, o
		if c > hi {
			hi = c
		}
		if c < lo {
			lo = c
		}
		out[i] = market.Candle{
			Time:  now.Add(-time.Duration(n-1-i) * interval).UnixMilli(),
			Open:  o,
			High:  hi,
			Low:   lo,
			Close: c,
		}
	}
	return out
}

// LiquidityBins returns n log-spaced depth bins across [lo, hi], normalised
// so the max bin is 1. Seeded from the pool id so the histogram is stable.
func LiquidityBins(poolID string, spot, sigma, lo, hi float64, n int) []market.DepthBin {
	if n <= 0 {
		n = 48
	}
	if lo <= 0 || hi <= lo {
		span := math.Exp(3 * (0.16 + sigma*0.12))
		lo = spot / span
		hi = spot * span
	}
	rnd := Mulberry32(HashStr(poolID))
	bins := make([]market.DepthBin, n)
	max := 0.0
	for i := 0; i < n; i++ {
		p := lo * math.Pow(hi/lo, (float64(i)+0.5)/float64(n))
		z := math.Log(p/spot) / (0.16 + sigma*0.12)
		base := math.Exp(-0.5 * z * z)
		noise := 0.55 + rnd.Next()*0.9
		shelfW := 0.05
		if math.Abs(z) < 2.6 {
			shelfW = 0.18
		}
		shelf := shelfW * (0.6 + rnd.Next())
		v := base*noise + shelf
		bins[i] = market.DepthBin{Price: p, Depth: v}
		if v > max {
			max = v
		}
	}
	if max > 0 {
		for i := range bins {
			bins[i].Depth /= max
		}
	}
	return bins
}

func defaultDepthSpan(spot, sigma float64) (lo, hi float64) {
	span := math.Exp(3 * (0.16 + sigma*0.12))
	return spot / span, spot * span
}
