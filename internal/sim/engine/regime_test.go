package engine

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"amazing/internal/sim/config"
	"amazing/internal/sim/core"
	"amazing/internal/sim/data"
)

// regimePath is 30 days of 5-minute regime-switching GBM with fees on.
func regimePath(t testing.TB, seed uint64) (*config.Config, []core.Bar) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Pool.FeeModel, cfg.Pool.BaseFeeAPR = "apr", 0.15
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	dyn := map[core.Regime]data.Dynamics{}
	byRegime, err := cfg.RegimeDynamicsByRegime()
	if err != nil {
		t.Fatal(err)
	}
	for r, d := range byRegime {
		dyn[r] = data.Dynamics{MuAPR: d.MuAPR, Sigma: d.Sigma, MeanDwell: d.MeanDwell.D()}
	}
	bars := data.RegimeGBM(data.PathRNG(seed, 0), 3000, dyn, core.Range, 5*time.Minute, 30*288, t0)
	return &cfg, bars
}

func TestEveryStrategyBalancesOnARegimePath(t *testing.T) {
	cfg, bars := regimePath(t, 7)
	res, err := Run(context.Background(), cfg, bars, Options{KeepLedger: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Strategies) != len(config.AllStrategies) {
		t.Fatalf("ran %d strategies, want %d", len(res.Strategies), len(config.AllStrategies))
	}
	for _, sr := range res.Strategies {
		a := sr.Attribution
		if math.Abs(a.Sum()-a.NetPnL) > 1e-6*math.Max(1, math.Abs(a.NetPnL)) {
			t.Errorf("%s: attribution sums to %.6f, net %.6f (%+v)", sr.Name, a.Sum(), a.NetPnL, a)
		}
		last := sr.Ledger[len(sr.Ledger)-1]
		if math.Abs(last.Equity-cfg.Capital-cfg.ReserveUSD-sr.Metrics.NetPnL) > 1e-6 {
			t.Errorf("%s: ledger equity %.2f disagrees with net P&L %.2f", sr.Name, last.Equity, sr.Metrics.NetPnL)
		}
		t.Logf("%-13s net %9.2f  max dd %5.1f%%  recenters %3d  hedge trades %4d  switches %3d",
			sr.Name, sr.Metrics.NetPnL, sr.Metrics.MaxDrawdown*100, sr.Metrics.Recenters, sr.Metrics.HedgeTrades, sr.Metrics.Switches)
	}
	if opt := res.Strategies[8]; opt.Name != config.StepOptions || opt.Attribution.OptionsPnL == 0 {
		t.Errorf("the options overlay never traded: %+v", opt.Attribution)
	}
	reg := res.Strategies[6]
	if reg.Name != config.Regime || reg.Metrics.Switches == 0 {
		t.Fatalf("regime strategy never switched: %+v", reg.Metrics)
	}
	total := 0.0
	for _, f := range reg.Metrics.TimeInRegime {
		total += f
	}
	if math.Abs(total-1) > 1e-9 {
		t.Errorf("time in regime sums to %v", total)
	}
	if d := reg.Metrics.Detector; d == nil || d.Bars != len(bars) {
		t.Errorf("detector score missing or incomplete: %+v", d)
	}
	for _, ev := range reg.Switches {
		if ev.Time.Before(bars[ev.Bar].Time) || ev.Time.Equal(bars[ev.Bar].Time) {
			t.Errorf("switch decided on bar %d executed at %s, not the next bar", ev.Bar, ev.Time)
		}
	}
	if !res.Strategies[7].LooksAhead {
		t.Error("the oracle benchmark must be flagged LooksAhead")
	}
}

// Shuffling every bar after T must not change any decision up to T.
func TestNoLookAhead(t *testing.T) {
	cfg, bars := regimePath(t, 11)
	const cut = 20 * 288
	shuffled := append([]core.Bar(nil), bars...)
	future := shuffled[cut+1:]
	rng := rand.New(rand.NewPCG(99, 1))
	closes := make([]float64, len(future))
	for i, b := range future {
		closes[i] = b.Close
	}
	rng.Shuffle(len(closes), func(i, j int) { closes[i], closes[j] = closes[j], closes[i] })
	for i := range future {
		c := closes[i]
		future[i].Open, future[i].High, future[i].Low, future[i].Close = c, c, c, c
	}

	same := func(name string, x, y []core.Bar) bool {
		a, err := RunStrategy(context.Background(), cfg, name, x, Options{KeepLedger: true})
		if err != nil {
			t.Fatal(err)
		}
		b, err := RunStrategy(context.Background(), cfg, name, y, Options{KeepLedger: true})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i <= cut; i++ {
			if a.Ledger[i] != b.Ledger[i] {
				return false
			}
		}
		return true
	}
	for _, name := range []string{config.Regime, config.StepHedge, config.StepOptions} {
		if !same(name, bars, shuffled) {
			t.Errorf("%s: shuffling bars after %d changed earlier decisions", name, cut)
		}
	}
	// The check has teeth: the oracle reads the future, and fails it. (On
	// unlabelled bars it labels each bar by the move that follows it.)
	unlabel := func(in []core.Bar) []core.Bar {
		out := append([]core.Bar(nil), in...)
		for i := range out {
			out[i].TrueRegime = core.NoRegime
		}
		return out
	}
	if same(config.RegimeOracle, unlabel(bars), unlabel(shuffled)) {
		t.Error("the oracle should change past decisions when the future changes")
	}
}

func BenchmarkRun(b *testing.B) {
	cfg, bars := regimePath(b, 3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Run(context.Background(), cfg, bars, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
