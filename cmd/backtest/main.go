// Command backtest runs an on-chain backtest from the command line: give it a
// window, get the result. It uses the same engine and swap CSV as the API and
// UI, so it is a quick way to check a strategy before opening the page.
//
//	go run ./cmd/backtest -from 2026-02-01 -to 2026-03-01
//	go run ./cmd/backtest -from 2026-02-01 -to 2026-03-01 -range 15 -short 0
//	go run ./cmd/backtest -from 2026-02-01 -to 2026-03-01 -lower 1900 -upper 2600 -daily
//	go run ./cmd/backtest -from 2026-02-01 -to 2026-03-01 -json > result.json
//	go run ./cmd/backtest -coverage
//
// It works offline from backend/data. A window that reaches past the CSV is
// refused unless -rpc is given, in which case the missing days are downloaded.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"amazing/internal/backtest"
	"amazing/internal/history"
	"amazing/internal/simulate"
)

type opts struct {
	from, to, dataDir, pool, perp, rpc, legsFile string
	lp, lower, upper, rangePct                   float64
	short, long, leverage, hedgeBand             float64
	hedgeEvery                                   int
	daily, asJSON, coverage, hedge, rebalance    bool
	rebalanceUpOnly                              bool
}

func main() {
	var o opts
	flag.StringVar(&o.from, "from", "", "first day, YYYY-MM-DD (UTC)")
	flag.StringVar(&o.to, "to", "", "last day, YYYY-MM-DD (UTC), inclusive")
	flag.StringVar(&o.dataDir, "data", "data", "directory holding the swap CSVs")
	flag.StringVar(&o.pool, "pool", "v3-eth-usdc-005", "LP pool id")
	flag.StringVar(&o.perp, "perp", "ETH-USD", "perp market for -short/-long")
	flag.Float64Var(&o.lp, "lp", 50_000, "LP capital in USD (0 = no LP leg)")
	flag.Float64Var(&o.lower, "lower", 0, "LP lower price (with -upper; overrides -range)")
	flag.Float64Var(&o.upper, "upper", 0, "LP upper price")
	flag.Float64Var(&o.rangePct, "range", 10, "LP range as ±% around the price on -from")
	flag.Float64Var(&o.short, "short", 10_000, "short collateral in USD (0 = none)")
	flag.Float64Var(&o.long, "long", 0, "long collateral in USD (0 = none)")
	flag.Float64Var(&o.leverage, "leverage", 3, "perp leverage")
	flag.BoolVar(&o.rebalance, "rebalance", false, "recentre the LP (and reopen the perps) when a daily close leaves the range")
	flag.BoolVar(&o.rebalanceUpOnly, "rebalance-up-only", false, "with -rebalance: only recentre on exits above the range, never below")
	flag.BoolVar(&o.hedge, "hedge", false, "size the -short to the LP's ETH delta (ignores -leverage)")
	flag.IntVar(&o.hedgeEvery, "hedge-every", 24, "with -hedge: re-hedge check interval in hours (1, 2, 3, 4, 6, 8, 12, 24)")
	flag.Float64Var(&o.hedgeBand, "hedge-band", 0, "with -hedge: only re-hedge when off the delta by more than this % of LP value")
	flag.StringVar(&o.legsFile, "legs", "", "JSON file with a legs array (replaces the leg flags)")
	flag.StringVar(&o.rpc, "rpc", "", "Base RPC URL; allows downloading days missing from the CSV")
	flag.BoolVar(&o.daily, "daily", false, "print the day-by-day equity curve")
	flag.BoolVar(&o.asJSON, "json", false, "print the full result as JSON")
	flag.BoolVar(&o.coverage, "coverage", false, "print what data is on disk and exit")
	flag.Parse()

	if err := run(o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "backtest:", err)
		os.Exit(1)
	}
}

func run(o opts, out io.Writer) error {
	ctx := context.Background()
	rpcs := map[int]string{}
	if o.rpc != "" {
		rpcs[history.ChainBase] = o.rpc
	}
	store := history.New(o.dataDir, rpcs, nil)
	cov := store.Coverage()

	if o.coverage {
		if len(cov) == 0 {
			return fmt.Errorf("no swap CSVs in %s", o.dataDir)
		}
		for _, c := range cov {
			fmt.Fprintf(out, "%-18s %s  %s .. %s  (%d full days, %.0f MiB)  ids: %s\n",
				c.PoolID, c.Pair, c.FullFrom, c.FullTo, c.Days, float64(c.Bytes)/(1<<20), strings.Join(c.PoolIDs, ", "))
		}
		return nil
	}

	if o.from == "" || o.to == "" {
		return fmt.Errorf("-from and -to are required (see -coverage for available dates)")
	}
	if err := checkWindow(o, cov); err != nil {
		return err
	}

	t0 := time.Now() // includes loading the CSV, which dominates
	feed := &onceFeed{inner: store, cache: map[string]history.Window{}}
	legs, err := buildLegs(ctx, o, feed)
	if err != nil {
		return err
	}

	mock := simulate.NewMock(nil)
	pools, _ := mock.Pools(ctx)
	perps, _ := mock.Perps(ctx)
	eng := backtest.New(nil)
	eng.SetFeed(feed)

	res, err := eng.Run(ctx, backtest.Request{Source: "onchain", From: o.from, To: o.to, Legs: legs}, pools, perps)
	if err != nil {
		return err
	}
	if o.asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	printResult(out, o, legs, res, feed.prices(), time.Since(t0))
	return nil
}

// checkWindow refuses a window the CSV does not cover unless downloading is
// allowed, so a typo in a date fails fast instead of starting an RPC fetch.
func checkWindow(o opts, cov []history.Coverage) error {
	if _, err := time.Parse("2006-01-02", o.from); err != nil {
		return fmt.Errorf("-from %q: want YYYY-MM-DD", o.from)
	}
	if _, err := time.Parse("2006-01-02", o.to); err != nil {
		return fmt.Errorf("-to %q: want YYYY-MM-DD", o.to)
	}
	if o.to < o.from {
		return fmt.Errorf("-to %s is before -from %s", o.to, o.from)
	}
	if o.rpc != "" {
		return nil
	}
	for _, c := range cov {
		for _, id := range c.PoolIDs {
			if id == o.pool {
				if o.from < c.FullFrom || o.to > c.FullTo {
					return fmt.Errorf("%s..%s is outside the data on disk (%s..%s); pick dates inside it or pass -rpc to download the rest",
						o.from, o.to, c.FullFrom, c.FullTo)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("no swap CSV for pool %s in %s (run cmd/swapimport, or pass -rpc)", o.pool, o.dataDir)
}

func buildLegs(ctx context.Context, o opts, feed *onceFeed) ([]backtest.Leg, error) {
	if o.legsFile != "" {
		b, err := os.ReadFile(o.legsFile)
		if err != nil {
			return nil, err
		}
		var legs []backtest.Leg
		if err := json.Unmarshal(b, &legs); err != nil {
			return nil, fmt.Errorf("%s: %w", o.legsFile, err)
		}
		if len(legs) == 0 {
			return nil, fmt.Errorf("%s: no legs", o.legsFile)
		}
		return legs, nil
	}

	var legs []backtest.Leg
	if o.lp > 0 {
		lower, upper := o.lower, o.upper
		if lower <= 0 || upper <= 0 {
			// Centre the range on the price at the start of the window, so the
			// position begins in range whatever dates are chosen.
			p0, err := feed.startPrice(ctx, o.pool, o.from, o.to)
			if err != nil {
				return nil, err
			}
			lower, upper = p0*(1-o.rangePct/100), p0*(1+o.rangePct/100)
		}
		if upper <= lower {
			return nil, fmt.Errorf("LP range %.2f..%.2f: upper must exceed lower", lower, upper)
		}
		legs = append(legs, backtest.Leg{Kind: "lp", PoolID: o.pool, Capital: o.lp, Lower: lower, Upper: upper, Rebalance: o.rebalance, RebalanceSkipBelow: o.rebalanceUpOnly})
	}
	if o.short > 0 {
		legs = append(legs, backtest.Leg{Kind: "short", PerpID: o.perp, Capital: o.short, Leverage: o.leverage, Hedge: o.hedge, HedgeIntervalHours: o.hedgeEvery, HedgeBandPct: o.hedgeBand})
	}
	if o.long > 0 {
		legs = append(legs, backtest.Leg{Kind: "long", PerpID: o.perp, Capital: o.long, Leverage: o.leverage})
	}
	if len(legs) == 0 {
		return nil, fmt.Errorf("no legs: set -lp, -short or -long above 0")
	}
	return legs, nil
}

// onceFeed loads each window once. Choosing the LP range needs the start
// price before the run, and loading the CSV costs seconds, so the run reuses
// the window that lookup already loaded.
type onceFeed struct {
	inner *history.Store
	cache map[string]history.Window
	last  history.Window
}

func (f *onceFeed) Ensure(ctx context.Context, poolID string, fromTS, toTS int64) (history.Window, error) {
	key := fmt.Sprintf("%s|%d|%d", poolID, fromTS, toTS)
	if spec, ok := history.SpecFor(poolID); ok {
		key = fmt.Sprintf("%s|%d|%d", spec.FileKey(), fromTS, toTS)
	}
	if w, ok := f.cache[key]; ok {
		return w, nil
	}
	w, err := f.inner.Ensure(ctx, poolID, fromTS, toTS)
	if err != nil {
		return w, err
	}
	f.cache[key] = w
	f.last = w
	return w, nil
}

func (f *onceFeed) startPrice(ctx context.Context, pool, from, to string) (float64, error) {
	a, _ := time.Parse("2006-01-02", from)
	b, _ := time.Parse("2006-01-02", to)
	fromTS := a.UTC().Unix()
	toTS := b.UTC().Add(24*time.Hour - time.Second).Unix()
	w, err := f.Ensure(ctx, pool, fromTS, toTS)
	if err != nil {
		return 0, err
	}
	for _, p := range w.DailyPrices {
		if p > 0 {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no prices for %s in %s..%s", pool, from, to)
}

func (f *onceFeed) prices() []float64 { return f.last.DailyPrices }

func printResult(out io.Writer, o opts, legs []backtest.Leg, r backtest.Result, prices []float64, el time.Duration) {
	fmt.Fprintf(out, "On-chain backtest %s .. %s  (%d days, %s)\n", r.From, r.To, r.Days+1, el.Round(time.Millisecond))
	if n := len(prices); n > 0 {
		chg := 0.0
		if prices[0] > 0 {
			chg = (prices[n-1]/prices[0] - 1) * 100
		}
		fmt.Fprintf(out, "ETH price         %.2f -> %.2f  (%+.2f%%)\n", prices[0], prices[n-1], chg)
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "Legs")
	for _, l := range legs {
		switch l.Kind {
		case "lp":
			fmt.Fprintf(out, "  LP     %-16s $%-9.0f range %.2f .. %.2f", l.PoolID, l.Capital, l.Lower, l.Upper)
			if l.Rebalance {
				fmt.Fprint(out, ", recentred when a close leaves it")
			}
			fmt.Fprintln(out)
		default:
			if l.Hedge {
				fmt.Fprintf(out, "  %-6s %-16s $%-9.0f delta hedge, resized daily\n", strings.ToUpper(l.Kind[:1])+l.Kind[1:], l.PerpID, l.Capital)
				continue
			}
			fmt.Fprintf(out, "  %-6s %-16s $%-9.0f %.1fx\n", strings.ToUpper(l.Kind[:1])+l.Kind[1:], l.PerpID, l.Capital, l.Leverage)
		}
	}
	fmt.Fprintln(out)

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "Leg\tCapital\tFees/funding\tMarket\tIL\tNet P/L\tReturn\tIn range\t")
	for _, l := range r.Legs {
		tir := "-"
		if l.Kind == "lp" {
			tir = fmt.Sprintf("%.0f%%", l.TimeInRange*100)
		}
		title := strings.ReplaceAll(l.Title, " · ", " ")
		if l.Liquidated {
			title += " (LIQUIDATED)"
		}
		fmt.Fprintf(tw, "%s\t%.0f\t%+.2f\t%+.2f\t%+.2f\t%+.2f\t%+.2f%%\t%s\t\n",
			title, l.Capital, l.Carry, l.Market, l.IL, l.NetPnl, l.ReturnPct, tir)
	}
	tw.Flush()
	fmt.Fprintln(out)

	fmt.Fprintf(out, "Net P/L           %+.2f  (%+.2f%% on $%.0f)\n", r.NetPnl, r.ReturnPct, r.Capital)
	fmt.Fprintf(out, "Max drawdown      %.2f%%\n", r.MaxDrawdownPct)
	fmt.Fprintf(out, "Sharpe            %.2f\n", r.Sharpe)
	fmt.Fprintf(out, "LP fees           %+.2f\n", r.FeesEarned)
	fmt.Fprintf(out, "Impermanent loss  %+.2f\n", r.ImpermanentLoss)
	fmt.Fprintf(out, "Funding           %+.2f   (mock catalog rate, constant over the window)\n", r.Funding)
	fmt.Fprintf(out, "Perp market P/L   %+.2f\n", r.PerpMarketPnl)
	if r.Rebalances > 0 {
		fmt.Fprintf(out, "Rebalances        %d  (cost %.2f)\n", r.Rebalances, r.RebalanceCost)
		start, _ := time.Parse("2006-01-02", r.From)
		for _, l := range r.Legs {
			if l.Kind != "lp" {
				continue
			}
			for _, d := range l.RebalanceDays {
				fmt.Fprintf(out, "  %s  %s -> range %.2f .. %.2f\n", start.AddDate(0, 0, d).Format("2006-01-02"), l.Title, l.RangeLower[d], l.RangeUpper[d])
			}
		}
	}
	if r.HedgeEffectiveness != nil {
		fmt.Fprintf(out, "Hedge offsets     %.0f%% of IL\n", *r.HedgeEffectiveness*100)
	}
	if a := r.Attribution; a != nil {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Profit = fees - volatility cost - hedging costs")
		fmt.Fprintf(out, "  Fees                      %+10.2f\n", a.Fees)
		fmt.Fprintf(out, "  Volatility cost (gamma)   %+10.2f   LP convexity, measured hourly\n", -a.VolatilityCost)
		fmt.Fprintf(out, "  Hedge trading costs       %+10.2f\n", -a.HedgeCosts)
		fmt.Fprintf(out, "  LP rebalance swap fees    %+10.2f\n", -a.RebalanceCosts)
		fmt.Fprintf(out, "  Funding                   %+10.2f\n", a.Funding)
		fmt.Fprintf(out, "  Residual directional P/L  %+10.2f   delta left unhedged between re-hedges\n", a.Residual)
		fmt.Fprintf(out, "  = Net P/L                 %+10.2f\n", a.NetPnl)
		fmt.Fprintf(out, "Realized volatility %.1f%% a year", a.RealizedVolPct)
		if a.FeesToVolCost != nil {
			fmt.Fprintf(out, ", fees / volatility cost %.2fx", *a.FeesToVolCost)
		}
		fmt.Fprintln(out)
		if a.Hedged {
			fmt.Fprintf(out, "Hedge trades %d", a.HedgeTrades)
			if a.MinMarginPct != nil {
				fmt.Fprintf(out, ", lowest margin %.1f%% of notional", *a.MinMarginPct)
			}
			fmt.Fprintln(out)
		}
	}

	if !o.daily {
		return
	}
	fmt.Fprintln(out)
	start, _ := time.Parse("2006-01-02", r.From)
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	head := "Date\tETH\tEquity\t"
	for _, l := range r.Legs {
		head += strings.ReplaceAll(l.Title, " · ", " ") + "\t"
	}
	fmt.Fprintln(tw, head)
	for d := range r.Combined {
		px := 0.0
		if d < len(prices) {
			px = prices[d]
		}
		line := fmt.Sprintf("%s\t%.2f\t%.2f\t", start.AddDate(0, 0, d).Format("2006-01-02"), px, r.Combined[d])
		for _, l := range r.Legs {
			v := 0.0
			if d < len(l.Series) {
				v = l.Series[d]
			}
			line += fmt.Sprintf("%.2f\t", v)
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()
}
