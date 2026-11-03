// Package api exposes the simulator over HTTP as a thin wrapper around the
// library API:
//
//	POST {prefix}/simulate    run every strategy over bars
//	POST {prefix}/montecarlo  run every strategy over synthetic paths
//
// Both take a JSON body whose "config" is a partial simulator config (JSON
// keys, applied over the defaults) and return the result JSON.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"amazing/internal/history"
	"amazing/internal/sim"
	"amazing/internal/sim/config"
	"amazing/internal/sim/core"
	"amazing/internal/sim/data"
	"amazing/internal/sim/engine"
)

// BarSource loads historical bars for a pool between two times.
type BarSource func(ctx context.Context, poolID string, from, to time.Time, barSize time.Duration) ([]core.Bar, error)

// Handler serves the simulator. Source may be nil (requests must then carry
// bars or ask for a synthetic path).
type Handler struct {
	Source   BarSource
	MaxBars  int // per simulate request (0 = 200,000)
	MaxPaths int // per montecarlo request (0 = 20,000)
}

// SimulateRequest is POST /simulate's body. Exactly one price source is
// used, in this order: Bars, Pool, Synthetic.
type SimulateRequest struct {
	Config json.RawMessage `json:"config,omitempty"`
	Bars   []core.Bar      `json:"bars,omitempty"`
	Pool   *struct {
		PoolID string `json:"poolId"`
		From   string `json:"from"` // YYYY-MM-DD
		To     string `json:"to"`   // YYYY-MM-DD, inclusive
	} `json:"pool,omitempty"`
	// Synthetic draws one path from the config's monte_carlo settings,
	// path index Path.
	Synthetic *struct {
		Path int `json:"path"`
	} `json:"synthetic,omitempty"`
	// Ledger includes per-bar ledgers (large); equity curves are always
	// included.
	Ledger bool `json:"ledger,omitempty"`
}

// MonteCarloRequest is POST /montecarlo's body.
type MonteCarloRequest struct {
	Config  json.RawMessage `json:"config,omitempty"`
	Samples bool            `json:"samples,omitempty"`
}

// Register mounts the handlers on mux under prefix (e.g. "/api/v1/sim").
func (h *Handler) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("POST "+prefix+"/simulate", h.simulate)
	mux.HandleFunc("POST "+prefix+"/montecarlo", h.monteCarlo)
}

func parseConfig(raw json.RawMessage) (config.Config, error) {
	if len(raw) == 0 || string(raw) == "null" {
		cfg := config.Defaults()
		return cfg, cfg.Validate()
	}
	return config.Parse(raw, false)
}

func (h *Handler) simulate(w http.ResponseWriter, r *http.Request) {
	var req SimulateRequest
	if err := decode(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := parseConfig(req.Config)
	if err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("config: %w", err))
		return
	}
	bars, status, err := h.bars(r.Context(), &cfg, req)
	if err != nil {
		fail(w, status, err)
		return
	}
	limit := h.MaxBars
	if limit <= 0 {
		limit = 200_000
	}
	if len(bars) > limit {
		fail(w, http.StatusRequestEntityTooLarge, fmt.Errorf("%d bars exceeds the limit of %d", len(bars), limit))
		return
	}
	res, err := engine.Run(r.Context(), &cfg, bars, engine.Options{KeepLedger: req.Ledger, KeepEquity: true})
	if err != nil {
		fail(w, statusFor(err), err)
		return
	}
	writeJSON(w, res)
}

func (h *Handler) bars(ctx context.Context, cfg *config.Config, req SimulateRequest) ([]core.Bar, int, error) {
	switch {
	case len(req.Bars) > 0:
		return data.Resample(req.Bars, cfg.BarSize.D()), 0, nil
	case req.Pool != nil:
		if h.Source == nil {
			return nil, http.StatusNotImplemented, errors.New("no historical data source configured; send bars instead")
		}
		from, err := time.Parse("2006-01-02", req.Pool.From)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("pool.from: %w", err)
		}
		to, err := time.Parse("2006-01-02", req.Pool.To)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("pool.to: %w", err)
		}
		bars, err := h.Source(ctx, req.Pool.PoolID, from, to.Add(24*time.Hour-time.Second), cfg.BarSize.D())
		if err != nil {
			return nil, http.StatusBadGateway, err
		}
		return bars, 0, nil
	case req.Synthetic != nil:
		mc := cfg.MonteCarlo
		n := int(mc.Horizon.D() / cfg.BarSize.D())
		rng := data.PathRNG(mc.Seed, req.Synthetic.Path)
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if !mc.RegimeSwitching {
			return data.GBM(rng, mc.P0, mc.MuAPR, mc.Sigma, cfg.BarSize.D(), n, start), 0, nil
		}
		byRegime, err := cfg.RegimeDynamicsByRegime()
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		dyn := map[core.Regime]data.Dynamics{}
		for k, d := range byRegime {
			dyn[k] = data.Dynamics{MuAPR: d.MuAPR, Sigma: d.Sigma, MeanDwell: d.MeanDwell.D()}
		}
		return data.RegimeGBM(rng, mc.P0, dyn, cfg.Detector.InitialRegime, cfg.BarSize.D(), n, start), 0, nil
	}
	return nil, http.StatusBadRequest, errors.New(`send "bars", "pool" or "synthetic"`)
}

func (h *Handler) monteCarlo(w http.ResponseWriter, r *http.Request) {
	var req MonteCarloRequest
	if err := decode(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := parseConfig(req.Config)
	if err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("config: %w", err))
		return
	}
	limit := h.MaxPaths
	if limit <= 0 {
		limit = 20_000
	}
	if cfg.MonteCarlo.Paths > limit {
		fail(w, http.StatusRequestEntityTooLarge, fmt.Errorf("%d paths exceeds the limit of %d", cfg.MonteCarlo.Paths, limit))
		return
	}
	res, err := engine.MonteCarlo(r.Context(), &cfg, engine.MCOptions{KeepSamples: req.Samples})
	if err != nil {
		fail(w, statusFor(err), err)
		return
	}
	writeJSON(w, res)
}

// SwapBars adapts a swap loader to a BarSource; liquidityScale converts raw
// pool liquidity to position units (see sim.BarsFromSwaps).
func SwapBars(load func(ctx context.Context, poolID string, fromTS, toTS int64) ([]history.Row, float64, error)) BarSource {
	return func(ctx context.Context, poolID string, from, to time.Time, d time.Duration) ([]core.Bar, error) {
		rows, scale, err := load(ctx, poolID, from.Unix(), to.Unix())
		if err != nil {
			return nil, err
		}
		return sim.BarsFromSwaps(rows, d, scale), nil
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("body: %w", err)
	}
	return nil
}

func statusFor(err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusServiceUnavailable
	}
	return http.StatusUnprocessableEntity
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": err.Error()}})
}
