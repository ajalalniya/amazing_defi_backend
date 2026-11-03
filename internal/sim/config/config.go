// Package config is the simulator's configuration: one struct covering the
// pool, perp, re-centring costs, baselines, detector, switcher, regime
// profiles, options overlay and Monte Carlo, loaded from YAML or JSON on top
// of Defaults and checked by Validate.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"amazing/internal/sim/core"
	"amazing/internal/sim/options"
	"amazing/internal/sim/perp"
	"amazing/internal/sim/profile"
	"amazing/internal/sim/regime"
	"amazing/internal/sim/switcher"
)

// Strategy names, in report order.
const (
	HoldETH      = "hold_eth"
	Hold5050     = "hold_5050"
	LPOnly       = "lp_only"
	StaticHedge  = "static"
	StepHedge    = "step"
	Continuous   = "continuous"
	Regime       = "regime"
	RegimeOracle = "regime_oracle"
	StepOptions  = "step_options"
)

// AllStrategies is every strategy the engine knows, in report order.
var AllStrategies = []string{HoldETH, Hold5050, LPOnly, StaticHedge, StepHedge, Continuous, Regime, RegimeOracle, StepOptions}

// Config is the whole simulation configuration.
type Config struct {
	// Capital is the USD put into the LP (or the holding baselines).
	// ReserveUSD is set aside as perp collateral; returns are measured on
	// Capital + ReserveUSD.
	Capital    float64       `yaml:"capital" json:"capital"`
	ReserveUSD float64       `yaml:"reserve_usd" json:"reserve_usd"`
	BarSize    core.Duration `yaml:"bar_size" json:"bar_size"`
	// MaxBeta caps β in any profile; above 1 over-hedges.
	MaxBeta float64 `yaml:"max_beta" json:"max_beta"`

	Pool     PoolConfig                     `yaml:"pool" json:"pool"`
	Perp     perp.Config                    `yaml:"perp" json:"perp"`
	Recenter RecenterConfig                 `yaml:"recenter" json:"recenter"`
	Baseline BaselineConfig                 `yaml:"baseline" json:"baseline"`
	Detector DetectorConfig                 `yaml:"detector" json:"detector"`
	Switcher switcher.Config                `yaml:"switcher" json:"switcher"`
	Profiles map[string]profile.RuleProfile `yaml:"profiles" json:"profiles"`
	Options  options.Config                 `yaml:"options" json:"options"`
	// Strategies to run; empty runs AllStrategies.
	Strategies []string         `yaml:"strategies" json:"strategies"`
	MonteCarlo MonteCarloConfig `yaml:"monte_carlo" json:"monte_carlo"`
}

// PoolConfig is the pool's fee tier and the fee income model.
type PoolConfig struct {
	FeeTier float64 `yaml:"fee_tier" json:"fee_tier"` // 0.0005 = 0.05%
	// FeeModel is "none", "apr" (BaseFeeAPR, scaled by the range's
	// concentration) or "volume" (bar volume · fee tier · L share, using the
	// bars' liquidity or ActiveLiquidity).
	FeeModel        string  `yaml:"fee_model" json:"fee_model"`
	BaseFeeAPR      float64 `yaml:"base_fee_apr" json:"base_fee_apr"`
	ActiveLiquidity float64 `yaml:"active_liquidity" json:"active_liquidity"`
}

// RecenterConfig is when ranges are re-centred and what it costs.
type RecenterConfig struct {
	// Enabled re-centres the regime strategies when price leaves the range
	// (or comes within EdgeBuffer of an edge). Baselines re-centres the
	// fixed-range baselines too; off, they are set-and-forget ranges.
	Enabled   bool `yaml:"enabled" json:"enabled"`
	Baselines bool `yaml:"baselines" json:"baselines"`
	// EdgeBuffer re-centres when price is within this fraction of an edge
	// (0.01 = 1%). Edges a skewed range is built to sit on are exempt.
	EdgeBuffer  float64 `yaml:"edge_buffer" json:"edge_buffer"`
	SlippageBps float64 `yaml:"slippage_bps" json:"slippage_bps"`
	// GasUSD is paid per transaction: a re-centre is withdraw, swap and
	// mint (3); an exit is withdraw and swap (2).
	GasUSD float64 `yaml:"gas_usd" json:"gas_usd"`
}

// BaselineConfig shapes the fixed-rule baseline strategies.
type BaselineConfig struct {
	K    float64 `yaml:"k" json:"k"`
	Skew float64 `yaml:"skew" json:"skew"`
	Beta float64 `yaml:"beta" json:"beta"`
	// StepPct (or StepAbs in USD, if set) is the step baseline's trigger.
	StepPct float64 `yaml:"step_pct" json:"step_pct"`
	StepAbs float64 `yaml:"step_abs" json:"step_abs"`
}

// DetectorConfig picks and tunes the regime detector.
type DetectorConfig struct {
	// Kind is "zscore" (default) or "emaadx". The oracle is not selectable
	// here: it only runs as the regime_oracle benchmark strategy.
	Kind       string            `yaml:"kind" json:"kind"`
	Lookback   core.Duration     `yaml:"lookback" json:"lookback"`
	Thresholds regime.Thresholds `yaml:"thresholds" json:"thresholds"`
	EMAFast    core.Duration     `yaml:"ema_fast" json:"ema_fast"`
	EMASlow    core.Duration     `yaml:"ema_slow" json:"ema_slow"`
	ADXPeriod  core.Duration     `yaml:"adx_period" json:"adx_period"`
	ADXTrend   float64           `yaml:"adx_trend" json:"adx_trend"`
	ADXStrong  float64           `yaml:"adx_strong" json:"adx_strong"`
	// InitialRegime is in force until the first confirmed switch.
	InitialRegime core.Regime `yaml:"initial_regime" json:"initial_regime"`
}

// MonteCarloConfig generates synthetic paths.
type MonteCarloConfig struct {
	Paths   int           `yaml:"paths" json:"paths"`
	Seed    uint64        `yaml:"seed" json:"seed"`
	Workers int           `yaml:"workers" json:"workers"` // 0 = runtime.NumCPU()
	P0      float64       `yaml:"p0" json:"p0"`
	Horizon core.Duration `yaml:"horizon" json:"horizon"`
	// Sigma and MuAPR drive plain GBM. MuAPR is the price's expected
	// return; 0 makes the price a martingale.
	Sigma float64 `yaml:"sigma" json:"sigma"`
	MuAPR float64 `yaml:"mu_apr" json:"mu_apr"`
	// RegimeSwitching replaces plain GBM with a Markov chain over Regimes.
	RegimeSwitching bool                      `yaml:"regime_switching" json:"regime_switching"`
	Regimes         map[string]RegimeDynamics `yaml:"regimes" json:"regimes"`
}

// RegimeDynamics is one regime of the regime-switching GBM: its drift and
// volatility, and how long it lasts on average before the chain moves to
// another regime (chosen uniformly).
type RegimeDynamics struct {
	MuAPR     float64       `yaml:"mu_apr" json:"mu_apr"`
	Sigma     float64       `yaml:"sigma" json:"sigma"`
	MeanDwell core.Duration `yaml:"mean_dwell" json:"mean_dwell"`
}

// Defaults is the brief's configuration: $10k LP plus $5k collateral, 5-min
// bars, 0.05% pool, 0.05% perp fee, ±10% baseline range, z-score detector
// over 3 days, the default profiles and a 5,000-path Monte Carlo.
func Defaults() Config {
	d := func(x time.Duration) core.Duration { return core.Duration(x) }
	day := 24 * time.Hour
	profiles := map[string]profile.RuleProfile{}
	for r, p := range profile.Defaults() {
		profiles[r.String()] = p
	}
	return Config{
		Capital: 10_000, ReserveUSD: 5_000, BarSize: d(5 * time.Minute), MaxBeta: 1.5,
		Pool: PoolConfig{FeeTier: 0.0005, FeeModel: "apr", BaseFeeAPR: 0.08},
		Perp: perp.Config{FeeRate: 0.0005, MaintenanceMargin: 0.01, LiquidationFee: 0.005},
		Recenter: RecenterConfig{
			Enabled: true, EdgeBuffer: 0.005, SlippageBps: 5, GasUSD: 0.05,
		},
		Baseline: BaselineConfig{K: 1.1, Beta: 1, StepPct: 0.025},
		Detector: DetectorConfig{
			Kind: "zscore", Lookback: d(3 * day), Thresholds: regime.DefaultThresholds(),
			EMAFast: d(12 * time.Hour), EMASlow: d(3 * day), ADXPeriod: d(day), ADXTrend: 20, ADXStrong: 35,
			InitialRegime: core.Range,
		},
		Switcher: switcher.Defaults(),
		Profiles: profiles,
		Options: options.Config{
			IV: 0.6, Tenor: d(7 * day), Roll: d(3 * day), GammaFraction: 0.5,
			SpreadBps: 50, TriggerProb: 0.35, RangeOnly: true,
		},
		MonteCarlo: MonteCarloConfig{
			Paths: 5000, Seed: 1, P0: 3000, Horizon: d(7 * day), Sigma: 0.6,
			Regimes: map[string]RegimeDynamics{
				"RANGE":        {MuAPR: 0, Sigma: 0.45, MeanDwell: d(3 * day)},
				"TEND_BULLISH": {MuAPR: 1.0, Sigma: 0.55, MeanDwell: d(2 * day)},
				"BULLISH":      {MuAPR: 3.0, Sigma: 0.70, MeanDwell: d(day)},
				"TEND_BEARISH": {MuAPR: -1.0, Sigma: 0.60, MeanDwell: d(2 * day)},
				"BEARISH":      {MuAPR: -3.0, Sigma: 0.90, MeanDwell: d(day)},
			},
		},
	}
}

// Load reads a YAML (.yaml/.yml) or JSON file over Defaults and validates it.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	cfg, err := Parse(b, ext == ".yaml" || ext == ".yml")
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes YAML or JSON over Defaults (keys are snake_case in both),
// rejecting unknown keys, and
// validates the result. Profiles given in the file replace the default
// profile for that regime as a whole, so give every field.
func Parse(b []byte, isYAML bool) (Config, error) {
	cfg := Defaults()
	// JSON is YAML, so one decoder reads both and the keys are the same
	// snake_case names in either format. isYAML only words the error.
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		if isYAML {
			return Config{}, err
		}
		return Config{}, fmt.Errorf("json: %w", err)
	}
	if len(cfg.Strategies) == 0 {
		cfg.Strategies = nil // "strategies: []" means all, like an absent key
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ProfileSet converts Profiles to a set keyed by Regime.
func (c Config) ProfileSet() (profile.Set, error) {
	set := profile.Set{}
	for name, p := range c.Profiles {
		r, err := core.ParseRegime(name)
		if err != nil || !r.Valid() {
			return nil, fmt.Errorf("profiles: key %q is not a regime", name)
		}
		set[r] = p
	}
	return set, nil
}

// RegimeDynamicsByRegime converts MonteCarlo.Regimes to a regime-keyed map.
func (c Config) RegimeDynamicsByRegime() (map[core.Regime]RegimeDynamics, error) {
	out := map[core.Regime]RegimeDynamics{}
	for name, d := range c.MonteCarlo.Regimes {
		r, err := core.ParseRegime(name)
		if err != nil || !r.Valid() {
			return nil, fmt.Errorf("monte_carlo.regimes: key %q is not a regime", name)
		}
		out[r] = d
	}
	return out, nil
}

// BarsPer is how many bars d spans (at least 1).
func (c Config) BarsPer(d time.Duration) int {
	return max(1, int(d/c.BarSize.D()))
}

// StrategyList is Strategies, or AllStrategies when empty.
func (c Config) StrategyList() []string {
	if len(c.Strategies) == 0 {
		return AllStrategies
	}
	return c.Strategies
}

// Validate checks every section and returns the first problem found, naming
// the offending key.
func (c Config) Validate() error {
	switch {
	case !(c.Capital > 0):
		return fmt.Errorf("capital %v: must be > 0", c.Capital)
	case c.ReserveUSD < 0:
		return fmt.Errorf("reserve_usd %v: must be >= 0", c.ReserveUSD)
	case c.BarSize.D() <= 0:
		return fmt.Errorf("bar_size %v: must be > 0", c.BarSize.D())
	case c.MaxBeta < 1:
		return fmt.Errorf("max_beta %v: must be >= 1", c.MaxBeta)
	case c.Pool.FeeTier < 0 || c.Pool.FeeTier > 0.1:
		return fmt.Errorf("pool.fee_tier %v: want a fraction in [0, 0.1]", c.Pool.FeeTier)
	case c.Pool.BaseFeeAPR < 0:
		return fmt.Errorf("pool.base_fee_apr %v: must be >= 0", c.Pool.BaseFeeAPR)
	case c.Pool.ActiveLiquidity < 0:
		return fmt.Errorf("pool.active_liquidity %v: must be >= 0", c.Pool.ActiveLiquidity)
	}
	if !slices.Contains([]string{"none", "apr", "volume"}, c.Pool.FeeModel) {
		return fmt.Errorf("pool.fee_model %q: want none, apr or volume", c.Pool.FeeModel)
	}
	if err := c.Perp.Validate(); err != nil {
		return err
	}
	r := c.Recenter
	switch {
	case r.EdgeBuffer < 0 || r.EdgeBuffer >= 0.5:
		return fmt.Errorf("recenter.edge_buffer %v: want a fraction in [0, 0.5)", r.EdgeBuffer)
	case r.SlippageBps < 0:
		return fmt.Errorf("recenter.slippage_bps %v: must be >= 0", r.SlippageBps)
	case r.GasUSD < 0:
		return fmt.Errorf("recenter.gas_usd %v: must be >= 0", r.GasUSD)
	}
	b := c.Baseline
	switch {
	case !(b.K > 1):
		return fmt.Errorf("baseline.k %v: must be > 1", b.K)
	case b.Skew < -1 || b.Skew > 1:
		return fmt.Errorf("baseline.skew %v: want -1..1", b.Skew)
	case b.Beta < 0 || b.Beta > c.MaxBeta:
		return fmt.Errorf("baseline.beta %v: want 0..max_beta (%v)", b.Beta, c.MaxBeta)
	case b.StepPct <= 0 && b.StepAbs <= 0:
		return fmt.Errorf("baseline: step_pct or step_abs must be > 0")
	}
	dc := c.Detector
	switch dc.Kind {
	case "zscore", "emaadx":
	default:
		return fmt.Errorf("detector.kind %q: want zscore or emaadx", dc.Kind)
	}
	if err := dc.Thresholds.Validate(); err != nil {
		return fmt.Errorf("detector.thresholds: %w", err)
	}
	if c.BarsPer(dc.Lookback.D()) < 2 || dc.Lookback.D() < 2*c.BarSize.D() {
		return fmt.Errorf("detector.lookback %v: must span at least 2 bars of %v", dc.Lookback.D(), c.BarSize.D())
	}
	if dc.Kind == "emaadx" {
		switch {
		case dc.EMAFast.D() <= 0 || dc.EMASlow.D() <= dc.EMAFast.D():
			return fmt.Errorf("detector: need 0 < ema_fast < ema_slow, got %v and %v", dc.EMAFast.D(), dc.EMASlow.D())
		case dc.ADXPeriod.D() < 2*c.BarSize.D():
			return fmt.Errorf("detector.adx_period %v: must span at least 2 bars", dc.ADXPeriod.D())
		case !(dc.ADXTrend > 0 && dc.ADXStrong > dc.ADXTrend):
			return fmt.Errorf("detector: need 0 < adx_trend < adx_strong, got %v and %v", dc.ADXTrend, dc.ADXStrong)
		}
	}
	if !dc.InitialRegime.Valid() {
		return fmt.Errorf("detector.initial_regime %v: must be one of the five regimes", dc.InitialRegime)
	}
	if err := c.Switcher.Validate(); err != nil {
		return err
	}
	set, err := c.ProfileSet()
	if err != nil {
		return err
	}
	if err := set.Validate(c.MaxBeta); err != nil {
		return err
	}
	if err := c.Options.Validate(); err != nil {
		return err
	}
	for _, s := range c.Strategies {
		if !slices.Contains(AllStrategies, s) {
			return fmt.Errorf("strategies: unknown %q (want %s)", s, strings.Join(AllStrategies, ", "))
		}
	}
	m := c.MonteCarlo
	switch {
	case m.Paths < 1:
		return fmt.Errorf("monte_carlo.paths %d: must be >= 1", m.Paths)
	case m.Workers < 0:
		return fmt.Errorf("monte_carlo.workers %d: must be >= 0", m.Workers)
	case !(m.P0 > 0):
		return fmt.Errorf("monte_carlo.p0 %v: must be > 0", m.P0)
	case m.Horizon.D() < c.BarSize.D():
		return fmt.Errorf("monte_carlo.horizon %v: must be at least one bar", m.Horizon.D())
	case m.Sigma < 0:
		return fmt.Errorf("monte_carlo.sigma %v: must be >= 0", m.Sigma)
	}
	if m.RegimeSwitching {
		dyn, err := c.RegimeDynamicsByRegime()
		if err != nil {
			return err
		}
		for _, r := range core.Regimes {
			d, ok := dyn[r]
			if !ok {
				return fmt.Errorf("monte_carlo.regimes: no dynamics for %s", r)
			}
			if d.Sigma < 0 || d.MeanDwell.D() < c.BarSize.D() {
				return fmt.Errorf("monte_carlo.regimes.%s: need sigma >= 0 and mean_dwell >= one bar", r)
			}
		}
	}
	return nil
}
