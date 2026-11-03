package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"amazing/internal/sim/core"
	"amazing/internal/sim/profile"
)

func TestDefaultYAMLMatchesDefaults(t *testing.T) {
	cfg, err := Load("../../../configs/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Defaults()) {
		t.Errorf("configs/default.yaml drifted from Defaults():\nfile     %+v\ndefaults %+v", cfg, Defaults())
	}
	set, err := cfg.ProfileSet()
	if err != nil {
		t.Fatal(err)
	}
	if p := set[core.TendBearish]; p.Beta != 1 || p.HedgeMode != profile.Step || p.StepPct != 0.025 {
		t.Errorf("TEND_BEARISH profile %+v", p)
	}
	if cfg.Switcher.MinDwell.D() != 12*time.Hour || cfg.Detector.Lookback.D() != 72*time.Hour {
		t.Errorf("durations: dwell %v lookback %v", cfg.Switcher.MinDwell.D(), cfg.Detector.Lookback.D())
	}
}

func TestParseOverridesAndJSON(t *testing.T) {
	cfg, err := Parse([]byte("capital: 25000\nswitcher:\n  confirm_bars: 5\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Capital != 25000 || cfg.Switcher.ConfirmBars != 5 || cfg.Switcher.Hysteresis != 0.2 {
		t.Errorf("partial YAML should override only what it names: %+v", cfg.Switcher)
	}
	cfg, err = Parse([]byte(`{"capital": 5000, "detector": {"lookback": "1d"}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Capital != 5000 || cfg.Detector.Lookback.D() != 24*time.Hour {
		t.Errorf("JSON: %v %v", cfg.Capital, cfg.Detector.Lookback.D())
	}
	if _, err := Parse(nil, true); err != nil {
		t.Errorf("an empty file is the defaults: %v", err)
	}
}

func TestValidationErrorsNameTheKey(t *testing.T) {
	for in, want := range map[string]string{
		"capital: -1":                          "capital",
		"bogus_key: 1":                         "bogus_key",
		"pool: {fee_model: magic}":             "pool.fee_model",
		"switcher: {transition_rerange: soon}": "switcher.transition_rerange",
		"profiles: {RANGE: {lp_mode: active, beta: 3, hedge_mode: static, k: 1.1}}":  "profiles.RANGE: beta",
		"profiles: {SIDEWAYS: {lp_mode: active, beta: 0, hedge_mode: none, k: 1.1}}": "SIDEWAYS",
		"profiles: {BEARISH: {lp_mode: active, beta: 1, hedge_mode: step, k: 1.1}}":  "step_pct",
		"detector: {lookback: 5m}": "detector.lookback",
		"detector: {thresholds: {bull: 0.1, tend_bull: 0.5, tend_bear: -0.5, bear: -1.5}}": "thresholds",
		"strategies: [yolo]":      "strategies",
		"monte_carlo: {paths: 0}": "monte_carlo.paths",
		"bar_size: soon":          "duration",
	} {
		_, err := Parse([]byte(in), true)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want it to mention %q", in, err, want)
		}
	}
}
