package regime

import (
	"math"

	"amazing/internal/sim/core"
)

// EMAADX classifies with trend strength and direction: Wilder's ADX over
// AdxN bars says whether there is a trend at all, and a fast/slow EMA
// crossover says which way.
//
//	ADX < Trend            RANGE
//	ADX ≥ Strong           BULLISH / BEARISH  (fast EMA above / below slow)
//	otherwise              TEND_BULLISH / TEND_BEARISH
//
// It has no continuous score, so the switcher applies confirmation and
// dwell but no hysteresis to it. Until the ADX has warmed up (2·AdxN bars)
// it reports RANGE.
type EMAADX struct {
	fastN, slowN, adxN int
	trend, strong      float64

	fast, slow       float64
	prevH, prevL, pC float64
	trS, pdmS, ndmS  float64 // Wilder-smoothed TR, +DM, −DM
	dxSum, adx       float64
	bars             int
}

// NewEMAADX builds the detector; periods are in bars.
func NewEMAADX(fastN, slowN, adxN int, trend, strong float64) *EMAADX {
	d := &EMAADX{fastN: max(fastN, 1), slowN: max(slowN, 2), adxN: max(adxN, 2), trend: trend, strong: strong}
	return d
}

// Update adds a bar and returns the regime.
func (d *EMAADX) Update(bar core.Bar) Regime {
	h, l, c := bar.High, bar.Low, bar.Close
	if h <= 0 || l <= 0 {
		h, l = c, c
	}
	d.bars++
	if d.bars == 1 {
		d.fast, d.slow = c, c
		d.prevH, d.prevL, d.pC = h, l, c
		return Range
	}
	d.fast += (c - d.fast) * 2 / float64(d.fastN+1)
	d.slow += (c - d.slow) * 2 / float64(d.slowN+1)

	tr := math.Max(h-l, math.Max(math.Abs(h-d.pC), math.Abs(l-d.pC)))
	up, down := h-d.prevH, d.prevL-l
	pdm, ndm := 0.0, 0.0
	if up > down && up > 0 {
		pdm = up
	}
	if down > up && down > 0 {
		ndm = down
	}
	d.prevH, d.prevL, d.pC = h, l, c

	n := float64(d.adxN)
	k := d.bars - 1 // bars with a TR so far
	if k <= d.adxN {
		d.trS += tr
		d.pdmS += pdm
		d.ndmS += ndm
	} else {
		d.trS += tr - d.trS/n
		d.pdmS += pdm - d.pdmS/n
		d.ndmS += ndm - d.ndmS/n
	}
	if k < d.adxN {
		return Range
	}
	dx := 0.0
	if d.trS > 0 {
		pdi, ndi := 100*d.pdmS/d.trS, 100*d.ndmS/d.trS
		if pdi+ndi > 0 {
			dx = 100 * math.Abs(pdi-ndi) / (pdi + ndi)
		}
	}
	switch m := k - d.adxN; {
	case m < d.adxN:
		d.dxSum += dx
		if m == d.adxN-1 {
			d.adx = d.dxSum / n
		} else {
			return Range
		}
	default:
		d.adx += (dx - d.adx) / n
	}
	if d.adx < d.trend {
		return Range
	}
	up2 := d.fast >= d.slow
	switch {
	case d.adx >= d.strong && up2:
		return Bullish
	case d.adx >= d.strong:
		return Bearish
	case up2:
		return TendBullish
	default:
		return TendBearish
	}
}

// ADX is the current average directional index (0 until warmed up).
func (d *EMAADX) ADX() float64 { return d.adx }

// Reset clears all state.
func (d *EMAADX) Reset() {
	*d = EMAADX{fastN: d.fastN, slowN: d.slowN, adxN: d.adxN, trend: d.trend, strong: d.strong}
}
