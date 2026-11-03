package backtest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"amazing/internal/clmath"
	"amazing/internal/history"
	"amazing/internal/market"
)

func (e *Engine) runOnchain(ctx context.Context, req Request, pools []market.Pool, perpList []market.Perp) (Result, error) {
	if e.feed == nil {
		return Result{}, fmt.Errorf("on-chain backtest requires a swap feed")
	}
	fromTS, toTS, err := parseWindow(req.From, req.To)
	if err != nil {
		return Result{}, err
	}
	history.Report(ctx, history.Progress{Stage: "load", Percent: 1, Message: "Reading swaps from disk"})

	poolBy := map[string]market.Pool{}
	for _, p := range pools {
		poolBy[p.ID] = p
	}
	perpBy := map[string]market.Perp{}
	for _, p := range perpList {
		perpBy[p.ID] = p
	}
	eth, btc := market.MarksFromPerps(perpList)

	windows := map[string]history.Window{}
	needETH := false
	ethID := ""
	for _, l := range req.Legs {
		kind := strings.ToLower(l.Kind)
		if kind == "lp" {
			if _, err := e.windowFor(ctx, windows, l.PoolID, fromTS, toTS); err != nil {
				return Result{}, err
			}
			if poolBy[l.PoolID].Token0 == "ETH" {
				needETH = true
			}
		} else if perpBy[l.PerpID].Base == "ETH" {
			needETH = true
		}
	}
	var ethDaily []float64
	if needETH {
		ethID = "v3-eth-usdc-005"
		for _, l := range req.Legs {
			if strings.ToLower(l.Kind) == "lp" {
				if _, ok := history.SpecFor(l.PoolID); ok {
					ethID = l.PoolID
					break
				}
			}
		}
		w, err := e.windowFor(ctx, windows, ethID, fromTS, toTS)
		if err != nil {
			return Result{}, err
		}
		ethDaily = w.DailyPrices
	}

	n := 0
	for _, w := range windows {
		if len(w.DailyPrices) > n {
			n = len(w.DailyPrices)
		}
	}
	if n < 2 {
		n = 2
	}
	days := n - 1

	history.Report(ctx, history.Progress{Stage: "simulate", Percent: 92, Message: "Simulating strategy"})
	results := make([]LegResult, 0, len(req.Legs))
	legRel := make([][]float64, len(req.Legs))
	legHourly := make([][]float64, len(req.Legs))
	for i, l := range req.Legs {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		kind := strings.ToLower(l.Kind)
		if kind == "lp" {
			w := windows[l.PoolID]
			results = append(results, simulateLPOnchain(l, w, days, fromTS, toTS, poolBy, eth, btc, e.log))
			legHourly[i] = hourlyCloses(w.Swaps, fromTS, days)
			continue
		}
		rel := ones(days + 1)
		legHourly[i] = flatHourly(rel)
		if perpBy[l.PerpID].Base == "ETH" && len(ethDaily) > 0 {
			rel = absToRel(padDaily(ethDaily, days+1))
			if h := hourlyCloses(windows[ethID].Swaps, fromTS, days); h[0] > 0 {
				legHourly[i] = absToRel(h)
			} else {
				legHourly[i] = flatHourly(rel)
			}
		}
		legRel[i] = rel
		results = append(results, simulateLeg(l, rel, days, poolBy, perpBy, eth, btc, e.log))
	}
	applyManagedPerps(req.Legs, results, legRel, legHourly, days, perpBy, e.log)

	history.Report(ctx, history.Progress{Stage: "simulate", Percent: 100, Message: "Done"})
	out := assemble(results, days, "onchain", req.From, req.To)
	out.Attribution = attribute(results, legHourly, days)
	out.Price, out.PriceLabel = onchainPrice(req.Legs, windows, ethDaily, days, poolBy, perpBy)
	return out, nil
}

func (e *Engine) windowFor(ctx context.Context, cache map[string]history.Window, poolID string, fromTS, toTS int64) (history.Window, error) {
	if w, ok := cache[poolID]; ok {
		return w, nil
	}
	spec, ok := history.SpecFor(poolID)
	if ok {
		for _, w := range cache {
			if w.Spec.FileKey() == spec.FileKey() {
				cache[poolID] = w
				return w, nil
			}
		}
	}
	w, err := e.feed.Ensure(ctx, poolID, fromTS, toTS)
	if err != nil {
		return history.Window{}, err
	}
	cache[poolID] = w
	return w, nil
}

func parseWindow(from, to string) (int64, int64, error) {
	f, err := time.Parse("2006-01-02", from)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid from date")
	}
	t, err := time.Parse("2006-01-02", to)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid to date")
	}
	fromTS := f.UTC().Unix()
	toTS := t.UTC().Add(24*time.Hour - time.Second).Unix()
	if toTS <= fromTS {
		return 0, 0, fmt.Errorf("to must be after from")
	}
	if toTS-fromTS > 1825*86400 {
		return 0, 0, fmt.Errorf("window exceeds 1825 days")
	}
	return fromTS, toTS, nil
}

func simulateLPOnchain(l Leg, w history.Window, days int, fromTS, toTS int64, poolBy map[string]market.Pool, eth, btc float64, log *slog.Logger) LegResult {
	pool := poolBy[l.PoolID]
	out := newLPResult(l, pool.Token0, days)
	out.Title = "LP · " + pool.Token0 + "/" + pool.Token1
	q := market.QuoteUSD(pool.Token1, eth, btc, log)
	out.q = q
	daily := padDaily(w.DailyPrices, days+1)
	p0 := daily[0]
	if p0 <= 0 {
		p0 = pool.Price
		for i := range daily {
			if daily[i] <= 0 {
				daily[i] = p0
			}
		}
	}
	spec := w.Spec
	book := openLP(p0, l.Capital/q, l.Lower, l.Upper, spec.TickSpacing, spec.Decimals0, spec.Decimals1)
	start0, start1 := clmath.Amounts(p0, book.lower, book.upper, book.L)
	out.Lower, out.Upper = book.lower, book.upper
	swaps := make([]Swap, len(w.Swaps))
	for i, r := range w.Swaps {
		swaps[i] = Swap{
			Timestamp: r.Timestamp,
			Amount0:   r.Amount0,
			Amount1:   r.Amount1,
			Tick:      r.Tick,
			Liquidity: r.Liquidity,
		}
	}
	fees := 0.0
	inRangeDays := 0
	for d := 0; d <= days; d++ {
		p := daily[d]
		inR := p >= book.lower && p <= book.upper
		if d > 0 {
			if inR {
				inRangeDays++
			}
			dayStart := fromTS + int64(d)*86400
			dayEnd := dayStart + 86400 - 1
			if d == days {
				dayEnd = toTS
			}
			posL := book.L * spec.LiquidityScale
			f0, f1 := EstimateFees(swaps, dayStart, dayEnd, book.tickLower, book.tickUpper, posL, spec.FeeRate, DefaultFeeProtocol)
			fees += (f0*p + f1) * q
		}
		book = settleLPDay(&out, l, book, d, days, p, q, fees, spec.FeeRate, spec.TickSpacing, spec.Decimals0, spec.Decimals1)
	}
	endP := daily[days]
	hodl := (start0*endP + start1) * q
	out.Carry = fees
	out.Market = hodl - l.Capital
	out.IL = book.value(endP)*q - hodl
	if days > 0 {
		out.TimeInRange = float64(inRangeDays) / float64(days)
	}
	out.End = out.Series[len(out.Series)-1]
	out.NetPnl = out.End - l.Capital
	if l.Capital != 0 {
		out.ReturnPct = out.NetPnl / l.Capital * 100
	}
	return out
}

// onchainPrice is the replayed daily close of the first leg's underlying: the
// first LP leg's pool, else ETH for an ETH perp. Other perps have no on-chain
// price here, so nothing is returned rather than a flat line.
func onchainPrice(legs []Leg, windows map[string]history.Window, ethDaily []float64, days int, poolBy map[string]market.Pool, perpBy map[string]market.Perp) ([]float64, string) {
	for _, l := range legs {
		if strings.ToLower(l.Kind) != "lp" {
			continue
		}
		if w, ok := windows[l.PoolID]; ok && len(w.DailyPrices) > 0 {
			p := poolBy[l.PoolID]
			return padDaily(w.DailyPrices, days+1), p.Token0 + "/" + p.Token1
		}
	}
	for _, l := range legs {
		if strings.ToLower(l.Kind) != "lp" && perpBy[l.PerpID].Base == "ETH" && len(ethDaily) > 0 {
			return padDaily(ethDaily, days+1), "ETH"
		}
	}
	return nil, ""
}

func padDaily(daily []float64, n int) []float64 {
	out := make([]float64, n)
	if len(daily) == 0 {
		return out
	}
	last := daily[0]
	for i := 0; i < n; i++ {
		if i < len(daily) && daily[i] > 0 {
			last = daily[i]
		}
		out[i] = last
	}
	return out
}

func absToRel(abs []float64) []float64 {
	out := make([]float64, len(abs))
	base := abs[0]
	if base <= 0 {
		base = 1
	}
	for i, p := range abs {
		out[i] = p / base
	}
	return out
}
