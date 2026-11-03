// Package metrics turns an equity curve and its bookkeeping into the
// numbers a strategy is judged by: returns, risk, the P&L attribution,
// Monte Carlo distributions and detector accuracy.
package metrics

import (
	"math"
	"slices"
	"time"

	"amazing/internal/sim/core"
)

// Attribution splits net P&L into what earned and what cost money. The
// parts sum exactly to NetPnL:
//
//	NetPnL = PoolFees + LPDeltaPnL − GammaLoss + HedgePnL − PerpFees + Funding
//	         − RecenterCosts − TransitionCosts + OptionsPnL + HoldingPnL
//
// GammaLoss is the LP's convexity paid bar by bar (V(p₁) − V(p₀) − Δ(p₀)·Δp,
// sign flipped): the impermanent loss a perp cannot hedge. LPDeltaPnL is the
// rest of the LP's price P&L, the part a hedge offsets. HoldingPnL is the
// price P&L of plain ETH held outside an LP (exit_to_eth, the baselines).
type Attribution struct {
	PoolFees        float64 `json:"poolFees"`
	LPDeltaPnL      float64 `json:"lpDeltaPnl"`
	GammaLoss       float64 `json:"gammaLoss"`
	HedgePnL        float64 `json:"hedgePnl"`
	PerpFees        float64 `json:"perpFees"`
	Funding         float64 `json:"funding"`
	RecenterCosts   float64 `json:"recenterCosts"`
	TransitionCosts float64 `json:"transitionCosts"`
	OptionsPnL      float64 `json:"optionsPnl"`
	HoldingPnL      float64 `json:"holdingPnl"`
	NetPnL          float64 `json:"netPnl"`
}

// Sum is the parts added up; it equals NetPnL when the books balance.
func (a Attribution) Sum() float64 {
	return a.PoolFees + a.LPDeltaPnL - a.GammaLoss + a.HedgePnL - a.PerpFees + a.Funding -
		a.RecenterCosts - a.TransitionCosts + a.OptionsPnL + a.HoldingPnL
}

// Metrics summarises one strategy run.
type Metrics struct {
	NetPnL      float64 `json:"netPnl"`
	TotalReturn float64 `json:"totalReturn"` // fraction of starting equity
	APR         float64 `json:"apr"`         // TotalReturn scaled to a year (simple)
	Volatility  float64 `json:"volatility"`  // annualised, of bar returns
	Sharpe      float64 `json:"sharpe"`      // annualised, zero risk-free rate
	MaxDrawdown float64 `json:"maxDrawdown"` // fraction, positive
	// Worst5WeeklyPnL is the 5th percentile of P&L over non-overlapping
	// weeks (the whole run when it is shorter than two weeks).
	Worst5WeeklyPnL float64 `json:"worst5WeeklyPnl"`
	Recenters       int     `json:"recenters"`
	HedgeTrades     int     `json:"hedgeTrades"`
	Switches        int     `json:"switches"`
	// TimeInRange is the share of bars the LP's range held the price;
	// TimeInRegime the share of bars each active regime was in force.
	TimeInRange  float64            `json:"timeInRange"`
	TimeInRegime map[string]float64 `json:"timeInRegime,omitempty"`
	// RealizedFeeAPR is fees earned per year over the average LP value.
	RealizedFeeAPR float64 `json:"realizedFeeApr"`
	// Detector scores the active regime against the truth (synthetic data).
	Detector *DetectorScore `json:"detector,omitempty"`
}

// Returns fills the return and risk fields from an equity curve sampled
// every bar of size d, starting from base.
func Returns(m *Metrics, equity []float64, base float64, d time.Duration) {
	if len(equity) == 0 || base <= 0 {
		return
	}
	end := equity[len(equity)-1]
	m.NetPnL = end - base
	m.TotalReturn = m.NetPnL / base
	years := core.DtYears(time.Duration(len(equity)-1) * d)
	if years > 0 {
		m.APR = m.TotalReturn / years
	}
	perYear := 1 / core.DtYears(d)
	var sum, sq float64
	n := 0
	peak, dd := equity[0], 0.0
	for i := 1; i < len(equity); i++ {
		if equity[i-1] > 0 {
			r := equity[i]/equity[i-1] - 1
			sum += r
			sq += r * r
			n++
		}
		peak = math.Max(peak, equity[i])
		if peak > 0 {
			dd = math.Max(dd, (peak-equity[i])/peak)
		}
	}
	m.MaxDrawdown = dd
	if n > 1 {
		mean := sum / float64(n)
		sd := math.Sqrt(math.Max(sq/float64(n)-mean*mean, 0))
		m.Volatility = sd * math.Sqrt(perYear)
		if sd > 0 {
			m.Sharpe = mean / sd * math.Sqrt(perYear)
		}
	}
	week := max(1, int(7*24*time.Hour/d))
	var weekly []float64
	for i := 0; i+week < len(equity); i += week {
		weekly = append(weekly, equity[i+week]-equity[i])
	}
	if len(weekly) < 2 {
		m.Worst5WeeklyPnL = m.NetPnL
	} else {
		slices.Sort(weekly)
		m.Worst5WeeklyPnL = Percentile(weekly, 0.05)
	}
}

// Percentile is the q-quantile (0..1) of sorted xs, linearly interpolated.
func Percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	i := int(pos)
	if i >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	f := pos - float64(i)
	return sorted[i]*(1-f) + sorted[i+1]*f
}

// Summary is a distribution's moments, tail and histogram.
type Summary struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean"`
	Std  float64 `json:"std"`
	Min  float64 `json:"min"`
	P5   float64 `json:"p5"`
	P25  float64 `json:"p25"`
	P50  float64 `json:"p50"`
	P75  float64 `json:"p75"`
	P95  float64 `json:"p95"`
	Max  float64 `json:"max"`
	// CVaR5 is the mean of the worst 5% of outcomes.
	CVaR5 float64 `json:"cvar5"`
	// Histogram is Bins counts over [Min, Max].
	Histogram []int `json:"histogram"`
}

// Summarize describes xs (left unsorted) with bins histogram buckets.
func Summarize(xs []float64, bins int) Summary {
	s := Summary{N: len(xs)}
	if len(xs) == 0 {
		return s
	}
	sorted := slices.Clone(xs)
	slices.Sort(sorted)
	var sum, sq float64
	for _, x := range sorted {
		sum += x
	}
	s.Mean = sum / float64(len(xs))
	for _, x := range sorted {
		sq += (x - s.Mean) * (x - s.Mean)
	}
	if len(xs) > 1 {
		s.Std = math.Sqrt(sq / float64(len(xs)-1))
	}
	s.Min, s.Max = sorted[0], sorted[len(sorted)-1]
	s.P5, s.P25, s.P50 = Percentile(sorted, 0.05), Percentile(sorted, 0.25), Percentile(sorted, 0.5)
	s.P75, s.P95 = Percentile(sorted, 0.75), Percentile(sorted, 0.95)
	tail := max(1, len(sorted)/20)
	t := 0.0
	for _, x := range sorted[:tail] {
		t += x
	}
	s.CVaR5 = t / float64(tail)
	if bins > 0 {
		s.Histogram = make([]int, bins)
		w := (s.Max - s.Min) / float64(bins)
		for _, x := range sorted {
			i := bins - 1
			if w > 0 {
				i = min(bins-1, int((x-s.Min)/w))
			}
			s.Histogram[i]++
		}
	}
	return s
}

// DetectorScore compares the active regime with the true one, bar by bar.
// Confusion[t][d] counts bars whose true regime is t and active regime d,
// both indexed in core.Regimes order (BEARISH .. BULLISH).
type DetectorScore struct {
	Accuracy  float64   `json:"accuracy"`
	Bars      int       `json:"bars"`
	Labels    []string  `json:"labels"`
	Confusion [5][5]int `json:"confusion"`
}

// Add counts one bar.
func (d *DetectorScore) Add(truth, active core.Regime) {
	if !truth.Valid() || !active.Valid() {
		return
	}
	d.Confusion[truth.Index()][active.Index()]++
	d.Bars++
}

// Merge adds another score's counts (Monte Carlo pools paths).
func (d *DetectorScore) Merge(o *DetectorScore) {
	if o == nil {
		return
	}
	for i := range d.Confusion {
		for j := range d.Confusion[i] {
			d.Confusion[i][j] += o.Confusion[i][j]
		}
	}
	d.Bars += o.Bars
}

// Finish fills Accuracy and Labels.
func (d *DetectorScore) Finish() {
	hit := 0
	for i := range d.Confusion {
		hit += d.Confusion[i][i]
	}
	if d.Bars > 0 {
		d.Accuracy = float64(hit) / float64(d.Bars)
	}
	d.Labels = make([]string, len(core.Regimes))
	for i, r := range core.Regimes {
		d.Labels[i] = r.String()
	}
}
