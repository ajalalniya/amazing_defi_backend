package regime

import (
	"math"

	"amazing/internal/sim/core"
)

// ZScore is the default momentum detector:
//
//	r   = ln(P_t / P_{t−N})                  the move over the last N bars
//	σ_N = √(Σ r_i²)  over those N bar returns  realized volatility, scaled to N
//	z   = r / σ_N
//
// and z is classified by Thresholds. Realized volatility is the root sum of
// squared log returns (not demeaned), the usual realized-variance estimator.
// Until N bars of history exist it reports RANGE with z = 0.
//
// Prices and returns live in ring buffers with running sums, so each Update
// is O(1); the sums are rebuilt exactly once per lap of the ring so rounding
// cannot drift over long series.
type ZScore struct {
	n      int
	th     Thresholds
	prices []float64 // last n+1 closes, ring
	rets   []float64 // last n log returns, ring
	head   int       // next write index into prices
	count  int       // closes seen (saturates at n+1)
	sumR2  float64
	laps   int
	z      float64
}

// NewZScore builds a detector with a lookback of n bars (n ≥ 2).
func NewZScore(n int, th Thresholds) *ZScore {
	if n < 2 {
		n = 2
	}
	return &ZScore{n: n, th: th, prices: make([]float64, n+1), rets: make([]float64, n)}
}

// Update adds a close and returns the regime its z-score indicates.
func (d *ZScore) Update(bar core.Bar) Regime {
	p := bar.Close
	if p <= 0 {
		return d.th.Classify(d.z)
	}
	if d.count > 0 {
		prev := d.prices[(d.head+len(d.prices)-1)%len(d.prices)]
		r := math.Log(p / prev)
		ri := d.laps % d.n
		d.sumR2 += r*r - d.rets[ri]*d.rets[ri]
		d.rets[ri] = r
		d.laps++
		if d.laps%d.n == 0 {
			d.sumR2 = 0
			for _, x := range d.rets {
				d.sumR2 += x * x
			}
		}
	}
	d.prices[d.head] = p
	d.head = (d.head + 1) % len(d.prices)
	if d.count < len(d.prices) {
		d.count++
	}
	if d.count < len(d.prices) {
		d.z = 0
		return Range
	}
	oldest := d.prices[d.head] // the slot about to be overwritten is n bars back
	sigma := math.Sqrt(math.Max(d.sumR2, 0))
	if sigma <= 0 {
		d.z = 0
	} else {
		d.z = math.Log(p/oldest) / sigma
	}
	return d.th.Classify(d.z)
}

// Score is the last z-score.
func (d *ZScore) Score() float64 { return d.z }

// Reset clears the history.
func (d *ZScore) Reset() {
	clear(d.prices)
	clear(d.rets)
	d.head, d.count, d.laps, d.sumR2, d.z = 0, 0, 0, 0, 0
}
