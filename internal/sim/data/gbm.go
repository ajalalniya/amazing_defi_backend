package data

import (
	"math"
	"math/rand/v2"
	"time"

	"amazing/internal/sim/core"
)

// PathRNG is the random source for Monte Carlo path i: a PCG seeded from
// (seed, i), so a path's prices depend only on its index, never on which
// worker ran it or when.
func PathRNG(seed uint64, i int) *rand.Rand {
	return rand.New(rand.NewPCG(seed, uint64(i)))
}

// GBM is a geometric Brownian motion path of n+1 bars of size d starting at
// p0: each log step is (μ − σ²/2)·dt + σ·√dt·Z, so muAPR is the price's
// expected (continuously compounded) return and muAPR = 0 makes the price a
// martingale. Bars are close-only (open = previous close; high/low the
// larger/smaller of the two).
func GBM(rng *rand.Rand, p0, muAPR, sigma float64, d time.Duration, n int, start time.Time) []core.Bar {
	bars := make([]core.Bar, n+1)
	dt := core.DtYears(d)
	drift := (muAPR - sigma*sigma/2) * dt
	vol := sigma * math.Sqrt(dt)
	p := p0
	bars[0] = core.Bar{Time: start, Open: p, High: p, Low: p, Close: p, TrueRegime: core.NoRegime}
	for i := 1; i <= n; i++ {
		q := p * math.Exp(drift+vol*rng.NormFloat64())
		bars[i] = core.Bar{Time: start.Add(time.Duration(i) * d), Open: p, High: math.Max(p, q), Low: math.Min(p, q), Close: q, TrueRegime: core.NoRegime}
		p = q
	}
	return bars
}

// Dynamics is one regime of a regime-switching GBM.
type Dynamics struct {
	MuAPR     float64
	Sigma     float64
	MeanDwell time.Duration
}

// RegimeGBM is a GBM whose drift and volatility follow a Markov chain over
// the five regimes: each bar the chain leaves its regime with probability
// dt/MeanDwell (an exponential dwell time) for one of the other four, chosen
// uniformly. Every bar is labelled with the regime it was drawn in, so a
// detector can be scored against the truth.
func RegimeGBM(rng *rand.Rand, p0 float64, dyn map[core.Regime]Dynamics, initial core.Regime, d time.Duration, n int, start time.Time) []core.Bar {
	bars := make([]core.Bar, n+1)
	dt := core.DtYears(d)
	r := initial
	p := p0
	bars[0] = core.Bar{Time: start, Open: p, High: p, Low: p, Close: p, TrueRegime: r}
	for i := 1; i <= n; i++ {
		if dw := dyn[r].MeanDwell; dw > 0 && rng.Float64() < float64(d)/float64(dw) {
			k := rng.IntN(len(core.Regimes) - 1)
			if core.Regimes[k] >= r {
				k++
			}
			r = core.Regimes[k]
		}
		x := dyn[r]
		q := p * math.Exp((x.MuAPR-x.Sigma*x.Sigma/2)*dt+x.Sigma*math.Sqrt(dt)*rng.NormFloat64())
		bars[i] = core.Bar{Time: start.Add(time.Duration(i) * d), Open: p, High: math.Max(p, q), Low: math.Min(p, q), Close: q, TrueRegime: r}
		p = q
	}
	return bars
}

// Ramp is a deterministic path that moves from a to b in steps of step USD
// (one bar each), for validation scenarios. Chained calls build round trips.
func Ramp(prices []float64, a, b, step float64) []float64 {
	if step <= 0 {
		return prices
	}
	if len(prices) == 0 || prices[len(prices)-1] != a {
		prices = append(prices, a)
	}
	dir := 1.0
	if b < a {
		dir = -1
	}
	for p := a + dir*step; dir*(b-p) > 1e-9; p += dir * step {
		prices = append(prices, p)
	}
	return append(prices, b)
}

// FromPrices wraps closes as bars of size d from start.
func FromPrices(prices []float64, d time.Duration, start time.Time) []core.Bar {
	bars := make([]core.Bar, len(prices))
	prev := 0.0
	for i, p := range prices {
		o := p
		if i > 0 {
			o = prev
		}
		bars[i] = core.Bar{Time: start.Add(time.Duration(i) * d), Open: o, High: math.Max(o, p), Low: math.Min(o, p), Close: p, TrueRegime: core.NoRegime}
		prev = p
	}
	return bars
}
