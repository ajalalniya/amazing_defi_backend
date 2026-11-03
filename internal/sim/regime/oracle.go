package regime

import (
	"math"

	"amazing/internal/sim/core"
)

// Oracle is a BENCHMARK-ONLY detector that knows the future. It replays a
// label per bar computed in advance: the true regime of a synthetic path, or
// for historical data the z-score of the move over the *next* N bars. Its
// results are an upper bound on what any real detector could achieve and must
// never be read as a tradable strategy; LooksAhead reports true so callers can
// flag it.
type Oracle struct {
	labels []Regime
	i      int
}

// NewOracle replays labels, one per bar. Bars past the end report RANGE.
func NewOracle(labels []Regime) *Oracle { return &Oracle{labels: labels} }

// OracleLabels derives the oracle's labels for bars: their TrueRegime when
// every bar has one (synthetic data), otherwise the forward z-score
// ln(P_{t+n}/P_t) / √(Σ r²) over the next n bars, classified by th. Bars too
// close to the end to see n bars ahead use what remains.
func OracleLabels(bars []core.Bar, n int, th Thresholds) []Regime {
	labels := make([]Regime, len(bars))
	known := len(bars) > 0
	for _, b := range bars {
		if !b.TrueRegime.Valid() {
			known = false
			break
		}
	}
	if known {
		for i, b := range bars {
			labels[i] = b.TrueRegime
		}
		return labels
	}
	n = max(n, 1)
	// Prefix sums of squared log returns make each forward window O(1).
	sq := make([]float64, len(bars))
	for i := 1; i < len(bars); i++ {
		r := 0.0
		if bars[i-1].Close > 0 && bars[i].Close > 0 {
			r = math.Log(bars[i].Close / bars[i-1].Close)
		}
		sq[i] = sq[i-1] + r*r
	}
	for i := range bars {
		j := min(i+n, len(bars)-1)
		if j == i || bars[i].Close <= 0 {
			labels[i] = Range
			continue
		}
		sigma := math.Sqrt(sq[j] - sq[i])
		z := 0.0
		if sigma > 0 {
			z = math.Log(bars[j].Close/bars[i].Close) / sigma
		}
		labels[i] = th.Classify(z)
	}
	return labels
}

// Update returns the next precomputed label.
func (d *Oracle) Update(core.Bar) Regime {
	if d.i >= len(d.labels) {
		return Range
	}
	r := d.labels[d.i]
	d.i++
	return r
}

// Reset rewinds to the first label.
func (d *Oracle) Reset() { d.i = 0 }

// LooksAhead is always true: the oracle reads the future.
func (d *Oracle) LooksAhead() bool { return true }
