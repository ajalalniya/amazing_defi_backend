// Package sim is the regime-switching hedged Uniswap v3 LP simulator's
// library API. It backtests a concentrated ETH/USDC LP position hedged with a
// GMX-style perp short whose rules switch between five market regimes, and
// compares it with fixed-rule baselines, over historical bars or Monte Carlo
// paths. See README.md in this directory for the concepts and how to extend
// it.
//
//	cfg, _ := config.Load("configs/default.yaml")
//	bars, _ := data.LoadCSV("data/eth_5m.csv", cfg.BarSize.D())
//	res, _ := sim.Run(ctx, cfg, bars)
//	mc, _ := sim.MonteCarlo(ctx, cfg, cfg.MonteCarlo)
//
// Every function is safe for concurrent use: state lives in each run.
package sim

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"amazing/internal/history"
	"amazing/internal/sim/config"
	"amazing/internal/sim/core"
	"amazing/internal/sim/data"
	"amazing/internal/sim/engine"
)

// Result is a historical run of every configured strategy.
type Result = engine.Result

// MCResult is a Monte Carlo run.
type MCResult = engine.MCResult

// Run backtests every strategy in cfg over prices, keeping per-bar ledgers
// and equity curves.
func Run(ctx context.Context, cfg config.Config, prices []core.Bar) (*Result, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("sim: %w", err)
	}
	return engine.Run(ctx, &cfg, prices, engine.Options{KeepLedger: true, KeepEquity: true})
}

// MonteCarlo runs every strategy in cfg over mcCfg.Paths synthetic paths.
// Results are deterministic for a given seed whatever the worker count.
func MonteCarlo(ctx context.Context, cfg config.Config, mcCfg config.MonteCarloConfig) (*MCResult, error) {
	cfg.MonteCarlo = mcCfg
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("sim: %w", err)
	}
	return engine.MonteCarlo(ctx, &cfg, engine.MCOptions{KeepSamples: true})
}

// BarsFromSwaps builds evenly spaced bars of size d from swap rows: price per
// swap, USD volume from the USDC leg (token1), and the pool's active
// liquidity divided by liquidityScale into the human units a position's L
// uses (1e12 for ETH(18)/USDC(6)).
func BarsFromSwaps(rows []history.Row, d time.Duration, liquidityScale float64) []core.Bar {
	trades := make([]data.Trade, 0, len(rows))
	for _, r := range rows {
		liq := 0.0
		if liquidityScale > 0 {
			liq = r.Liquidity / liquidityScale
		}
		trades = append(trades, data.Trade{Time: time.Unix(r.Timestamp, 0).UTC(), Price: r.Price, VolumeUSD: math.Abs(r.Amount1), Liquidity: liq})
	}
	return data.FromTrades(trades, d)
}

// WriteLedgerCSV writes a strategy's ledger, one row per bar.
func WriteLedgerCSV(w io.Writer, rows []engine.LedgerRow) error {
	cw := csv.NewWriter(w)
	head := []string{"time", "price", "regime", "detected", "true_regime", "pa", "pb", "x", "y", "lp_value",
		"held_eth", "held_usdc", "lp_delta", "short", "beta", "hedge_pnl", "fees_earned", "perp_fees",
		"funding", "swap_costs", "gas_costs", "options", "equity", "floor"}
	if err := cw.Write(head); err != nil {
		return err
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	reg := func(r core.Regime) string {
		if r.Valid() {
			return r.String()
		}
		return ""
	}
	rec := make([]string, len(head))
	for _, r := range rows {
		rec[0] = r.Time.UTC().Format(time.RFC3339)
		rec[1], rec[2], rec[3], rec[4] = f(r.Price), reg(r.Regime), reg(r.Detected), reg(r.TrueRegime)
		rec[5], rec[6], rec[7], rec[8], rec[9] = f(r.Pa), f(r.Pb), f(r.X), f(r.Y), f(r.LPValue)
		rec[10], rec[11], rec[12], rec[13], rec[14] = f(r.HeldETH), f(r.HeldUSDC), f(r.LPDelta), f(r.Short), f(r.Beta)
		rec[15], rec[16], rec[17], rec[18] = f(r.HedgePnL), f(r.FeesEarned), f(r.PerpFees), f(r.Funding)
		rec[19], rec[20], rec[21], rec[22] = f(r.SwapCosts), f(r.GasCosts), f(r.Options), f(r.Equity)
		rec[23] = strconv.Itoa(int(r.Floor))
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteEquityCSV writes every strategy's equity curve side by side, one row
// per bar, for an equity-by-strategy chart.
func WriteEquityCSV(w io.Writer, res *Result, times []time.Time) error {
	cw := csv.NewWriter(w)
	head := []string{"time"}
	for _, s := range res.Strategies {
		head = append(head, s.Name)
	}
	if err := cw.Write(head); err != nil {
		return err
	}
	rec := make([]string, len(head))
	for i, t := range times {
		rec[0] = t.UTC().Format(time.RFC3339)
		for k, s := range res.Strategies {
			rec[k+1] = ""
			if i < len(s.Equity) {
				rec[k+1] = strconv.FormatFloat(s.Equity[i], 'f', 2, 64)
			}
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
