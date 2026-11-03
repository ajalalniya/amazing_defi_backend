package engine

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"amazing/internal/sim/config"
	"amazing/internal/sim/core"
)

func mcConfig(t testing.TB, paths int) *config.Config {
	cfg := refConfig(t)
	cfg.MonteCarlo.Paths = paths
	cfg.MonteCarlo.Seed = 1
	cfg.MonteCarlo.Sigma = 0.6
	cfg.MonteCarlo.MuAPR = 0
	cfg.MonteCarlo.P0 = 3000
	cfg.MonteCarlo.Horizon = core.Duration(7 * 24 * time.Hour)
	return cfg
}

// Brief section 9: σ = 60%, 7 days of 5-minute bars, 5,000 paths, seed 1.
// Every hedge mode but continuous loses about the range's gamma decay on
// average (−$165 to −$185); stepping every $75 has a P&L std of about $52.
func TestMonteCarloReference(t *testing.T) {
	if testing.Short() {
		t.Skip("5,000 paths")
	}
	cfg := mcConfig(t, 5000)
	res, err := MonteCarlo(context.Background(), cfg, MCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Strategies {
		t.Logf("%-10s mean %8.2f  std %7.2f  p5 %8.2f  p95 %8.2f  gamma %7.2f  perp fees %6.2f",
			s.Name, s.PnL.Mean, s.PnL.Std, s.PnL.P5, s.PnL.P95, s.Attribution.GammaLoss, s.Attribution.PerpFees)
		switch s.Name {
		case config.LPOnly, config.StaticHedge, config.StepHedge:
			if s.PnL.Mean < -185*1.03 || s.PnL.Mean > -165*0.97 {
				t.Errorf("%s mean P&L %.2f, want -165..-185", s.Name, s.PnL.Mean)
			}
		}
		if s.Name == config.StepHedge && math.Abs(s.PnL.Std/52-1) > 0.15 {
			t.Errorf("step-$75 P&L std %.2f, want about 52", s.PnL.Std)
		}
	}
}

// The same seed gives identical results whatever the worker count: each
// path's randomness depends only on (seed, path index).
func TestMonteCarloDeterministicAcrossWorkers(t *testing.T) {
	cfg := mcConfig(t, 64)
	cfg.MonteCarlo.Horizon = core.Duration(24 * time.Hour)
	cfg.Strategies = nil // every strategy, including the regime switcher
	cfg.MonteCarlo.RegimeSwitching = true
	a, err := MonteCarlo(context.Background(), cfg, MCOptions{Workers: 1, KeepSamples: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := MonteCarlo(context.Background(), cfg, MCOptions{Workers: 8, KeepSamples: true})
	if err != nil {
		t.Fatal(err)
	}
	a.Workers, b.Workers = 0, 0
	if !reflect.DeepEqual(a, b) {
		for i := range a.Strategies {
			if !reflect.DeepEqual(a.Strategies[i], b.Strategies[i]) {
				t.Errorf("%s differs between 1 and 8 workers", a.Strategies[i].Name)
			}
		}
		t.Fatal("results depend on the worker count")
	}
}

func TestMonteCarloCancels(t *testing.T) {
	cfg := mcConfig(t, 100_000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MonteCarlo(ctx, cfg, MCOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func BenchmarkMonteCarlo(b *testing.B) {
	cfg := mcConfig(b, 200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MonteCarlo(context.Background(), cfg, MCOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}
