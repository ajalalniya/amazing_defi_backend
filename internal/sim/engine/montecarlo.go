package engine

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"amazing/internal/sim/config"
	"amazing/internal/sim/core"
	"amazing/internal/sim/data"
	"amazing/internal/sim/metrics"
)

// MCOptions control a Monte Carlo run.
type MCOptions struct {
	// Workers overrides cfg.MonteCarlo.Workers (0 = use the config, whose 0
	// means runtime.NumCPU()).
	Workers int
	// KeepSamples returns every path's net P&L per strategy, for
	// distribution charts.
	KeepSamples bool
}

// MCStrategy is one strategy's results across all paths.
type MCStrategy struct {
	Name       string `json:"name"`
	LooksAhead bool   `json:"looksAhead,omitempty"`
	// PnL, Return and MaxDrawdown are distributions over paths.
	PnL         metrics.Summary `json:"pnl"`
	Return      metrics.Summary `json:"return"`
	MaxDrawdown metrics.Summary `json:"maxDrawdown"`
	// Attribution is the mean attribution per path.
	Attribution     metrics.Attribution `json:"attribution"`
	MeanRecenters   float64             `json:"meanRecenters"`
	MeanHedgeTrades float64             `json:"meanHedgeTrades"`
	MeanSwitches    float64             `json:"meanSwitches"`
	// Detector pools the confusion matrix over every path (regime-switching
	// paths only).
	Detector *metrics.DetectorScore `json:"detector,omitempty"`
	Samples  []float64              `json:"samples,omitempty"`
}

// MCResult is a Monte Carlo run.
type MCResult struct {
	Paths           int          `json:"paths"`
	BarsPerPath     int          `json:"barsPerPath"`
	Seed            uint64       `json:"seed"`
	Workers         int          `json:"workers"`
	RegimeSwitching bool         `json:"regimeSwitching"`
	Sigma           float64      `json:"sigma"`
	Horizon         string       `json:"horizon"`
	Strategies      []MCStrategy `json:"strategies"`
}

type pathOut struct {
	pnl, ret, dd float64
	att          metrics.Attribution
	rec, ht, sw  int
	det          *metrics.DetectorScore
	looksAhead   bool
}

// mcStart stamps synthetic paths; only spacing matters.
var mcStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// MonteCarlo runs every configured strategy over cfg.MonteCarlo.Paths
// synthetic paths in parallel. Path i is drawn from its own PCG source
// seeded with (Seed, i) and results are stored by path index, so the output
// is identical for any worker count and schedule. It stops early, returning
// ctx.Err(), when ctx is cancelled.
func MonteCarlo(ctx context.Context, cfg *config.Config, opt MCOptions) (*MCResult, error) {
	mc := cfg.MonteCarlo
	n := int(mc.Horizon.D() / cfg.BarSize.D())
	if n < 1 {
		return nil, fmt.Errorf("monte carlo: horizon %v is shorter than one bar", mc.Horizon.D())
	}
	var dyn map[core.Regime]data.Dynamics
	if mc.RegimeSwitching {
		byRegime, err := cfg.RegimeDynamicsByRegime()
		if err != nil {
			return nil, err
		}
		dyn = make(map[core.Regime]data.Dynamics, len(byRegime))
		for r, d := range byRegime {
			dyn[r] = data.Dynamics{MuAPR: d.MuAPR, Sigma: d.Sigma, MeanDwell: d.MeanDwell.D()}
		}
	}
	workers := opt.Workers
	if workers <= 0 {
		workers = mc.Workers
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	workers = min(workers, mc.Paths)
	names := cfg.StrategyList()
	out := make([][]pathOut, mc.Paths)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	fail := func(err error) {
		errOnce.Do(func() { firstErr = err; cancel() })
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				rng := data.PathRNG(mc.Seed, i)
				var bars []core.Bar
				if mc.RegimeSwitching {
					bars = data.RegimeGBM(rng, mc.P0, dyn, cfg.Detector.InitialRegime, cfg.BarSize.D(), n, mcStart)
				} else {
					bars = data.GBM(rng, mc.P0, mc.MuAPR, mc.Sigma, cfg.BarSize.D(), n, mcStart)
				}
				row := make([]pathOut, len(names))
				for k, name := range names {
					sr, err := RunStrategy(ctx, cfg, name, bars, Options{MaxEvents: 1})
					if err != nil {
						fail(fmt.Errorf("path %d, %s: %w", i, name, err))
						return
					}
					m := sr.Metrics
					row[k] = pathOut{pnl: m.NetPnL, ret: m.TotalReturn, dd: m.MaxDrawdown, att: sr.Attribution,
						rec: m.Recenters, ht: m.HedgeTrades, sw: m.Switches, det: m.Detector, looksAhead: sr.LooksAhead}
				}
				out[i] = row
			}
		}()
	}
feed:
	for i := 0; i < mc.Paths; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := &MCResult{Paths: mc.Paths, BarsPerPath: n + 1, Seed: mc.Seed, Workers: workers,
		RegimeSwitching: mc.RegimeSwitching, Sigma: mc.Sigma, Horizon: mc.Horizon.D().String()}
	pnl := make([]float64, mc.Paths)
	ret := make([]float64, mc.Paths)
	dd := make([]float64, mc.Paths)
	for k, name := range names {
		s := MCStrategy{Name: name}
		var att metrics.Attribution
		var rec, ht, sw float64
		for i := range out {
			o := out[i][k]
			pnl[i], ret[i], dd[i] = o.pnl, o.ret, o.dd
			att = addAtt(att, o.att)
			rec += float64(o.rec)
			ht += float64(o.ht)
			sw += float64(o.sw)
			s.LooksAhead = s.LooksAhead || o.looksAhead
			if o.det != nil {
				if s.Detector == nil {
					s.Detector = &metrics.DetectorScore{}
				}
				s.Detector.Merge(o.det)
			}
		}
		np := float64(mc.Paths)
		s.PnL, s.Return, s.MaxDrawdown = metrics.Summarize(pnl, 40), metrics.Summarize(ret, 0), metrics.Summarize(dd, 0)
		s.Attribution = scaleAtt(att, 1/np)
		s.MeanRecenters, s.MeanHedgeTrades, s.MeanSwitches = rec/np, ht/np, sw/np
		if s.Detector != nil {
			s.Detector.Finish()
		}
		if opt.KeepSamples {
			s.Samples = append([]float64(nil), pnl...)
		}
		res.Strategies = append(res.Strategies, s)
	}
	return res, nil
}

func addAtt(a, b metrics.Attribution) metrics.Attribution {
	a.PoolFees += b.PoolFees
	a.LPDeltaPnL += b.LPDeltaPnL
	a.GammaLoss += b.GammaLoss
	a.HedgePnL += b.HedgePnL
	a.PerpFees += b.PerpFees
	a.Funding += b.Funding
	a.RecenterCosts += b.RecenterCosts
	a.TransitionCosts += b.TransitionCosts
	a.OptionsPnL += b.OptionsPnL
	a.HoldingPnL += b.HoldingPnL
	a.NetPnL += b.NetPnL
	return a
}

func scaleAtt(a metrics.Attribution, k float64) metrics.Attribution {
	a.PoolFees *= k
	a.LPDeltaPnL *= k
	a.GammaLoss *= k
	a.HedgePnL *= k
	a.PerpFees *= k
	a.Funding *= k
	a.RecenterCosts *= k
	a.TransitionCosts *= k
	a.OptionsPnL *= k
	a.HoldingPnL *= k
	a.NetPnL *= k
	return a
}
