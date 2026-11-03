// Command sim runs the regime-switching hedged LP simulator.
//
//	go run ./cmd/sim bars       --swaps data/swaps_<pool>.csv --out data/eth_5m.csv
//	go run ./cmd/sim run        --config configs/default.yaml --data data/eth_5m.csv --out results/
//	go run ./cmd/sim montecarlo --config configs/default.yaml --paths 5000 --seed 1 --out results/
//
// run writes result.json (metrics, attribution, break-even, switches and
// events per strategy), equity.csv (every strategy's equity curve) and
// ledger_<strategy>.csv (the per-bar books). montecarlo writes
// montecarlo.json (P&L distributions, mean attribution, detector accuracy and
// every path's P&L). Both print a summary table.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"amazing/internal/history"
	"amazing/internal/sim"
	"amazing/internal/sim/config"
	"amazing/internal/sim/core"
	"amazing/internal/sim/data"
)

const usage = `usage: sim <command> [flags]

commands:
  run         backtest every strategy over historical bars
  montecarlo  run every strategy over synthetic GBM paths
  bars        build OHLCV bars from a swap CSV

run "sim <command> -h" for a command's flags`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(ctx, os.Args[2:], os.Stdout)
	case "montecarlo":
		err = cmdMonteCarlo(ctx, os.Args[2:], os.Stdout)
	case "bars":
		err = cmdBars(os.Args[2:], os.Stdout)
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sim:", err)
		os.Exit(1)
	}
}

func loadConfig(path string) (config.Config, error) {
	if path == "" {
		cfg := config.Defaults()
		return cfg, cfg.Validate()
	}
	return config.Load(path)
}

func cmdRun(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/default.yaml", "YAML or JSON config")
	dataPath := fs.String("data", "", "OHLCV CSV (timestamp, open, high, low, close, volume[, liquidity, iv, regime])")
	outDir := fs.String("out", "results", "directory for result.json, equity.csv and ledgers")
	from := fs.String("from", "", "first day to use, YYYY-MM-DD (UTC)")
	to := fs.String("to", "", "last day to use, YYYY-MM-DD (UTC), inclusive")
	ledgers := fs.String("ledgers", "all", "ledger CSVs to write: all, none, or a comma-separated strategy list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataPath == "" {
		return errors.New("run: --data is required")
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	bars, err := data.LoadCSV(*dataPath, cfg.BarSize.D())
	if err != nil {
		return err
	}
	if bars, err = window(bars, *from, *to); err != nil {
		return err
	}
	t0 := time.Now()
	res, err := sim.Run(ctx, cfg, bars)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	times := make([]time.Time, len(bars))
	for i, b := range bars {
		times[i] = b.Time
	}
	if err := writeFile(filepath.Join(*outDir, "equity.csv"), func(w io.Writer) error { return sim.WriteEquityCSV(w, res, times) }); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, s := range strings.Split(*ledgers, ",") {
		want[strings.TrimSpace(s)] = true
	}
	for i := range res.Strategies {
		s := &res.Strategies[i]
		if want["all"] || want[s.Name] {
			if err := writeFile(filepath.Join(*outDir, "ledger_"+s.Name+".csv"), func(w io.Writer) error { return sim.WriteLedgerCSV(w, s.Ledger) }); err != nil {
				return err
			}
		}
		s.Ledger, s.Equity = nil, nil // in the CSVs; keep result.json readable
	}
	if err := writeJSON(filepath.Join(*outDir, "result.json"), res); err != nil {
		return err
	}
	fmt.Fprintf(out, "%d bars of %v, %s .. %s, realized vol %.1f%%  (%s)\n\n", res.Bars, cfg.BarSize.D(),
		res.From.Format("2006-01-02 15:04"), res.To.Format("2006-01-02 15:04"), res.RealizedVol*100, time.Since(t0).Round(time.Millisecond))
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "strategy\tnet P&L\treturn\tAPR\tmax DD\tSharpe\tworst 5% wk\tfees\tgamma\thedge\tperp fees\trecenters\tswitches\tfee APR need/got\t")
	for _, s := range res.Strategies {
		m, a := s.Metrics, s.Attribution
		be := "-"
		if s.BreakEven != nil {
			be = fmt.Sprintf("%.0f%% / %.0f%%", s.BreakEven.RequiredFeeAPR*100, s.BreakEven.RealizedFeeAPR*100)
		}
		name := s.Name
		if s.LooksAhead {
			name += " (look-ahead)"
		}
		fmt.Fprintf(tw, "%s\t%+.2f\t%+.2f%%\t%+.1f%%\t%.1f%%\t%.2f\t%+.2f\t%.2f\t%.2f\t%+.2f\t%.2f\t%d\t%d\t%s\t\n",
			name, m.NetPnL, m.TotalReturn*100, m.APR*100, m.MaxDrawdown*100, m.Sharpe, m.Worst5WeeklyPnL,
			a.PoolFees, -a.GammaLoss, a.HedgePnL, a.PerpFees, m.Recenters, m.Switches, be)
	}
	tw.Flush()
	fmt.Fprintf(out, "\nwrote %s\n", *outDir)
	return nil
}

func window(bars []core.Bar, from, to string) ([]core.Bar, error) {
	lo, hi := time.Time{}, time.Time{}
	var err error
	if from != "" {
		if lo, err = time.Parse("2006-01-02", from); err != nil {
			return nil, fmt.Errorf("--from: %w", err)
		}
	}
	if to != "" {
		if hi, err = time.Parse("2006-01-02", to); err != nil {
			return nil, fmt.Errorf("--to: %w", err)
		}
		hi = hi.Add(24 * time.Hour)
	}
	out := bars[:0:0]
	for _, b := range bars {
		if (lo.IsZero() || !b.Time.Before(lo)) && (hi.IsZero() || b.Time.Before(hi)) {
			out = append(out, b)
		}
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("no bars in %s .. %s", from, to)
	}
	return out, nil
}

func cmdMonteCarlo(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("montecarlo", flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/default.yaml", "YAML or JSON config")
	outDir := fs.String("out", "results", "directory for montecarlo.json")
	paths := fs.Int("paths", 0, "paths (0 = config)")
	seed := fs.Uint64("seed", 0, "seed (0 = config)")
	workers := fs.Int("workers", -1, "workers (-1 = config, 0 = NumCPU)")
	regimes := fs.Bool("regime-switching", false, "draw regime-switching GBM paths (scores the detector)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	mc := cfg.MonteCarlo
	if *paths > 0 {
		mc.Paths = *paths
	}
	if *seed > 0 {
		mc.Seed = *seed
	}
	if *workers >= 0 {
		mc.Workers = *workers
	}
	if *regimes {
		mc.RegimeSwitching = true
	}
	t0 := time.Now()
	res, err := sim.MonteCarlo(ctx, cfg, mc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(*outDir, "montecarlo.json"), res); err != nil {
		return err
	}
	kind := "GBM"
	if res.RegimeSwitching {
		kind = "regime-switching GBM"
	}
	fmt.Fprintf(out, "%d %s paths of %s (%d bars), seed %d, %d workers  (%s)\n\n", res.Paths, kind, res.Horizon, res.BarsPerPath,
		res.Seed, res.Workers, time.Since(t0).Round(time.Millisecond))
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "strategy\tmean\tstd\tp5\tmedian\tp95\tCVaR 5%\tmean max DD\tgamma\tperp fees\tfees\tdetector acc.\t")
	for _, s := range res.Strategies {
		acc := "-"
		if s.Detector != nil {
			acc = fmt.Sprintf("%.0f%%", s.Detector.Accuracy*100)
		}
		name := s.Name
		if s.LooksAhead {
			name += " (look-ahead)"
		}
		fmt.Fprintf(tw, "%s\t%+.2f\t%.2f\t%+.2f\t%+.2f\t%+.2f\t%+.2f\t%.1f%%\t%.2f\t%.2f\t%.2f\t%s\t\n", name,
			s.PnL.Mean, s.PnL.Std, s.PnL.P5, s.PnL.P50, s.PnL.P95, s.PnL.CVaR5, s.MaxDrawdown.Mean*100,
			-s.Attribution.GammaLoss, s.Attribution.PerpFees, s.Attribution.PoolFees, acc)
	}
	tw.Flush()
	fmt.Fprintf(out, "\nwrote %s\n", filepath.Join(*outDir, "montecarlo.json"))
	return nil
}

func cmdBars(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("bars", flag.ContinueOnError)
	swaps := fs.String("swaps", "", "swap CSV in the history store's format (e.g. data/swaps_<pool>.csv)")
	outPath := fs.String("out", "data/eth_5m.csv", "OHLCV CSV to write")
	bar := fs.Duration("bar", 5*time.Minute, "bar size")
	scale := fs.Float64("liquidity-scale", 1e12, "raw pool liquidity / this = human units (1e12 for ETH/USDC)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *swaps == "" {
		return errors.New("bars: --swaps is required")
	}
	rows, err := history.LoadSwaps(*swaps)
	if err != nil {
		return fmt.Errorf("bars: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("bars: %s has no swaps", *swaps)
	}
	bars := sim.BarsFromSwaps(rows, *bar, *scale)
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		return err
	}
	if err := writeFile(*outPath, func(w io.Writer) error { return data.WriteCSV(w, bars) }); err != nil {
		return err
	}
	fmt.Fprintf(out, "%d swaps -> %d bars of %v, %s .. %s -> %s\n", len(rows), len(bars), *bar,
		bars[0].Time.Format(time.RFC3339), bars[len(bars)-1].Time.Format(time.RFC3339), *outPath)
	return nil
}

func writeFile(path string, fill func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := fill(f); err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	return f.Close()
}

func writeJSON(path string, v any) error {
	return writeFile(path, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	})
}
