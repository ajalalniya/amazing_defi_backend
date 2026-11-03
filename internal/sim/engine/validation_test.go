package engine

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"sort"
	"testing"
	"time"

	"amazing/internal/sim/config"
	"amazing/internal/sim/data"
	"amazing/internal/sim/pool"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// refConfig is the brief's validation setup: C = 10,000 in a k = 1.1 range,
// 0.05% perp fee, no pool fees, funding, gas or slippage, ranges never
// re-centred, and the step baseline stepping every $75.
func refConfig(t testing.TB) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Capital, cfg.ReserveUSD = 10_000, 10_000
	cfg.Pool.FeeModel = "none"
	cfg.Perp.FeeRate, cfg.Perp.ImpactBps, cfg.Perp.FundingAPR = 0.0005, 0, 0
	cfg.Recenter.GasUSD, cfg.Recenter.SlippageBps, cfg.Recenter.Baselines = 0, 0, false
	cfg.Baseline.K, cfg.Baseline.Beta, cfg.Baseline.StepAbs = 1.1, 1, 75
	cfg.Strategies = []string{config.LPOnly, config.StaticHedge, config.StepHedge, config.Continuous}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

// near passes within 3% of want, or within $1 when want is near zero (the
// table rounds a $2.44 opening fee to "-2").
func near(got, want float64) bool {
	return math.Abs(got-want) <= math.Max(0.03*math.Abs(want), 1)
}

func TestReferencePosition(t *testing.T) {
	pa, pb, err := pool.RangeFor(3000, 1.1, 0)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := pool.Mint(10_000, 3000, pa, pb)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"L", pos.L, 1961.6},
		{"pa", pa, 2727.27},
		{"pb", pb, 3300},
		{"start delta", pos.Delta(3000), 1.667},
		{"h_avg", pos.AverageDelta(), 1.626},
		{"ETH at pa", pos.Delta(pa), 3.415},
		{"value", pos.Value(3000), 10_000},
	} {
		if math.Abs(c.got/c.want-1) > 0.001 {
			t.Errorf("%s = %.4f, want %.4f", c.name, c.got, c.want)
		}
	}
	// Γ = −L/(2p^{3/2}) matches the delta's slope.
	g := pos.Gamma(3000)
	slope := (pos.Delta(3000.01) - pos.Delta(2999.99)) / 0.02
	if math.Abs(g/slope-1) > 1e-6 || g >= 0 {
		t.Errorf("gamma %v, numeric slope %v", g, slope)
	}
}

func TestValidationTable(t *testing.T) {
	var ref struct {
		Paths    map[string][][2]float64       `json:"paths"`
		Expected map[string]map[string]float64 `json:"expected"`
	}
	b, err := os.ReadFile("testdata/validation.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	cfg := refConfig(t)
	names := make([]string, 0, len(ref.Paths))
	for n := range ref.Paths {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, pathName := range names {
		var prices []float64
		for _, leg := range ref.Paths[pathName] {
			prices = data.Ramp(prices, leg[0], leg[1], 1)
		}
		bars := data.FromPrices(prices, 5*time.Minute, t0)
		res, err := Run(context.Background(), cfg, bars, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, sr := range res.Strategies {
			want := ref.Expected[sr.Name][pathName]
			got := sr.Metrics.NetPnL
			if !near(got, want) {
				t.Errorf("%-10s %-18s net %8.2f, want %6.0f", sr.Name, pathName, got, want)
			} else {
				t.Logf("%-10s %-18s net %8.2f (table %6.0f)", sr.Name, pathName, got, want)
			}
			if a := sr.Attribution; math.Abs(a.Sum()-a.NetPnL) > 1e-6 {
				t.Errorf("%s %s: attribution sums to %.6f, net %.6f", sr.Name, pathName, a.Sum(), a.NetPnL)
			}
		}
	}
}

// For a small move Δp, one step-hedge leg loses about ½|Γ|Δp²: the LP's
// convexity, which the short (zero gamma) cannot offset.
func TestStepLegLossIsHalfGammaDpSquared(t *testing.T) {
	cfg := refConfig(t)
	cfg.Perp.FeeRate = 0
	cfg.Baseline.StepAbs = 10
	cfg.Strategies = []string{config.StepHedge}
	pos, _ := pool.Mint(10_000, 3000, 3000/1.1, 3300)
	for _, dp := range []float64{5, 10, -10} {
		bars := data.FromPrices([]float64{3000, 3000 + dp}, 5*time.Minute, t0)
		sr, err := RunStrategy(context.Background(), cfg, config.StepHedge, bars, Options{})
		if err != nil {
			t.Fatal(err)
		}
		want := -0.5 * math.Abs(pos.Gamma(3000)) * dp * dp
		if got := sr.Metrics.NetPnL; math.Abs(got/want-1) > 0.01 {
			t.Errorf("Δp=%v: leg P&L %.6f, ½|Γ|Δp² = %.6f", dp, got, want)
		}
		// With no fees the whole leg loss is the LP's convexity: the
		// attribution's gamma loss is that loss exactly.
		if g := sr.Attribution.GammaLoss; math.Abs(g+sr.Metrics.NetPnL) > 1e-9 {
			t.Errorf("Δp=%v: gamma loss %.9f, leg loss %.9f", dp, g, -sr.Metrics.NetPnL)
		}
	}
}
