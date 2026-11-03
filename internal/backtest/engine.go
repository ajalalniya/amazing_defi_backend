package backtest

import (
	"context"
	"log/slog"
	"math"
	"strings"

	"amazing/internal/clmath"
	"amazing/internal/history"
	"amazing/internal/market"
	"amazing/internal/perps"
	"amazing/internal/simulate"
)

type Request struct {
	Legs     []Leg   `json:"legs"`
	Days     int     `json:"days"`
	DriftPct float64 `json:"driftPct"`
	VolPct   float64 `json:"volPct"`
	Seed     int64   `json:"seed"`
	From     string  `json:"from,omitempty"`
	To       string  `json:"to,omitempty"`
	Source   string  `json:"source,omitempty"`
}

type Leg struct {
	Kind     string  `json:"kind"`
	PoolID   string  `json:"poolId,omitempty"`
	PerpID   string  `json:"perpId,omitempty"`
	Capital  float64 `json:"capital"`
	Lower    float64 `json:"lower,omitempty"`
	Upper    float64 `json:"upper,omitempty"`
	Leverage float64 `json:"leverage,omitempty"`
	// Hedge makes a short leg delta-neutral against the LP legs on the same
	// underlying: Capital is fixed collateral and Leverage is ignored, because
	// the notional is resized to the LP's delta (see simulateManagedPerp).
	Hedge bool `json:"hedge,omitempty"`
	// HedgeIntervalHours is how often a hedge is checked against the LP's
	// delta: 1, 4, 8 or 24 (the default, once per daily close).
	// HedgeBandPct skips a re-hedge unless the hedge is off from the delta by
	// more than this percent of the LP's value; 0 re-hedges at every check.
	HedgeIntervalHours int     `json:"hedgeIntervalHours,omitempty"`
	HedgeBandPct       float64 `json:"hedgeBandPct,omitempty"`
	// Rebalance makes an LP leg recentre whenever a daily close leaves its
	// range: it closes and reopens with the same upper/lower ratio, the close
	// at the middle. Perp legs on the same underlying close and reopen with it
	// (see applyManagedPerps).
	Rebalance bool `json:"rebalance,omitempty"`
	// RebalanceSkipBelow limits Rebalance to exits above the range: a close
	// below it leaves the LP where it is (all token0) until price comes back
	// into range or breaks out above.
	RebalanceSkipBelow bool `json:"rebalanceSkipBelow,omitempty"`
}

type LegResult struct {
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Capital     float64   `json:"capital"`
	Carry       float64   `json:"carry"`
	Market      float64   `json:"market"`
	IL          float64   `json:"il"`
	NetPnl      float64   `json:"netPnl"`
	ReturnPct   float64   `json:"returnPct"`
	Series      []float64 `json:"series"`
	Liquidated  bool      `json:"liquidated"`
	TimeInRange float64   `json:"timeInRange,omitempty"`
	End         float64   `json:"end"`
	// Lower/Upper are an LP leg's range after snapping to the pool's ticks, in
	// the same units as Result.Price, so a chart can shade when it was in range.
	Lower float64 `json:"lower,omitempty"`
	Upper float64 `json:"upper,omitempty"`
	// Hedged marks a short resized daily to the LP's exposure; Notional is
	// its USD size held through each day, one point per entry in Series.
	Hedged   bool      `json:"hedged,omitempty"`
	Notional []float64 `json:"notional,omitempty"`
	// Rebalances counts the closes the leg was closed and reopened on, listed
	// in RebalanceDays as indexes into Series. RebalanceCost is what that cost
	// in USD: pool swap fees for an LP, perp close+open fees for a perp.
	Rebalances    int     `json:"rebalances,omitempty"`
	RebalanceDays []int   `json:"rebalanceDays,omitempty"`
	RebalanceCost float64 `json:"rebalanceCost,omitempty"`
	// RangeLower/RangeUpper are a rebalancing LP's range held after each
	// daily close, so entry d is the range in force over day d+1.
	RangeLower []float64 `json:"rangeLower,omitempty"`
	RangeUpper []float64 `json:"rangeUpper,omitempty"`
	// Fees is an LP leg's cumulative fees in USD at each close. Fees are
	// collected to the side, never added back to the position's liquidity, so
	// Series[d] - Fees[d] is the position alone.
	Fees []float64 `json:"fees,omitempty"`
	// HedgeTrades counts a hedge's re-hedges (trims to the LP's delta, not
	// counting rebalance close/reopens). MinMarginPct is a managed perp's
	// lowest equity as a percent of its notional at any hourly step: how close
	// the short came to liquidation.
	HedgeTrades  int     `json:"hedgeTrades,omitempty"`
	MinMarginPct float64 `json:"minMarginPct,omitempty"`

	// books is an LP leg's position held after each daily close (entry d is
	// in force over day d+1) and q converts its token1 to USD; together with
	// an hourly price they give its delta and convexity between closes.
	// funding and tradeFees split a perp's Carry.
	books     []lpBook
	price     []float64
	q         float64
	funding   float64
	tradeFees float64

	// asset and exposure are an LP leg's token0 and the USD value of the
	// token0 it holds at each daily close: what a delta hedge has to short.
	// value is the position's USD value at each close, fees excluded, and
	// resets marks the closes it was recentred on.
	asset    string
	exposure []float64
	value    []float64
	resets   []bool
}

type Result struct {
	Days               int         `json:"days"`
	Capital            float64     `json:"capital"`
	Combined           []float64   `json:"combined"`
	Legs               []LegResult `json:"legs"`
	NetPnl             float64     `json:"netPnl"`
	ReturnPct          float64     `json:"returnPct"`
	MaxDrawdownPct     float64     `json:"maxDrawdownPct"`
	Sharpe             float64     `json:"sharpe"`
	FeesEarned         float64     `json:"feesEarned"`
	ImpermanentLoss    float64     `json:"impermanentLoss"`
	Funding            float64     `json:"funding"`
	PerpMarketPnl      float64     `json:"perpMarketPnl"`
	AvgTimeInRange     *float64    `json:"avgTimeInRange"`
	HedgeEffectiveness *float64    `json:"hedgeEffectiveness"`
	// Rebalances is how many times the LP legs recentred; RebalanceCost sums
	// every leg's cost of doing so.
	Rebalances    int     `json:"rebalances"`
	RebalanceCost float64 `json:"rebalanceCost"`
	// Attribution splits the result along profit = fees - volatility cost -
	// hedging costs, for any run with an LP leg.
	Attribution *Attribution `json:"attribution,omitempty"`
	Source      string       `json:"source"`
	From        string       `json:"from,omitempty"`
	To          string       `json:"to,omitempty"`
	// Price is the daily price of the strategy's underlying, one point per
	// entry in Combined: the first LP leg's pool, else the first perp. It is
	// the replayed on-chain close in on-chain mode, the simulated path in
	// scenario mode. PriceLabel names it, e.g. "ETH/USDC".
	Price      []float64 `json:"price,omitempty"`
	PriceLabel string    `json:"priceLabel,omitempty"`
}

type Engine struct {
	log  *slog.Logger
	feed OnchainFeed
}

type OnchainFeed interface {
	Ensure(ctx context.Context, poolID string, fromTS, toTS int64) (history.Window, error)
}

func New(log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{log: log}
}

func (e *Engine) SetFeed(f OnchainFeed) {
	e.feed = f
}

func assetMultiplier(asset string) float64 {
	switch asset {
	case "USDC", "USDT":
		return 0.02
	case "ARB", "GMX", "RDNT":
		return 1.35
	default:
		return 1
	}
}

func (e *Engine) Run(ctx context.Context, req Request, pools []market.Pool, perpList []market.Perp) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.EqualFold(req.Source, "onchain") || (req.From != "" && req.To != "") {
		return e.runOnchain(ctx, req, pools, perpList)
	}
	return e.runScenario(ctx, req, pools, perpList)
}

func (e *Engine) runScenario(ctx context.Context, req Request, pools []market.Pool, perpList []market.Perp) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	days := req.Days
	if days < 1 {
		days = 1
	}
	poolBy := map[string]market.Pool{}
	for _, p := range pools {
		poolBy[p.ID] = p
	}
	perpBy := map[string]market.Perp{}
	for _, p := range perpList {
		perpBy[p.ID] = p
	}
	eth, btc := market.MarksFromPerps(perpList)

	legAsset := make([]string, len(req.Legs))
	assets := make([]string, 0)
	seen := map[string]bool{}
	for i, l := range req.Legs {
		a := legUnderlying(l, poolBy, perpBy)
		legAsset[i] = a
		if a != "" && !seen[a] {
			seen[a] = true
			assets = append(assets, a)
		}
	}

	drift := req.DriftPct / 100
	vol := req.VolPct / 100

	factorRnd := simulate.Mulberry32(simulate.ToUint32(float64(int32(req.Seed))*7919 + float64(days)))
	factor := make([]float64, days+1)
	for d := 1; d <= days; d++ {
		factor[d] = simulate.Gauss(factorRnd)
	}

	pathOf := make(map[string][]float64, len(assets))
	for ai, a := range assets {
		arnd := simulate.Mulberry32(simulate.ToUint32(float64(int32(req.Seed))*31 + float64(ai)*977 + 5))
		sig := vol * assetMultiplier(a)
		mu := math.Log(1+drift) / float64(days)
		dt := 1.0 / 365
		p := make([]float64, days+1)
		p[0] = 1
		for d := 1; d <= days; d++ {
			shock := 0.78*factor[d] + 0.62*simulate.Gauss(arnd)
			p[d] = p[d-1] * math.Exp(mu-sig*sig*dt/2+sig*math.Sqrt(dt)*shock)
		}
		pathOf[a] = p
	}

	// Hourly steps between the simulated closes, for intraday re-hedging.
	hourlyOf := make(map[string][]float64, len(assets))
	for ai, a := range assets {
		hourlyOf[a] = bridgeHourly(pathOf[a], vol*assetMultiplier(a), req.Seed+int64(ai)*7)
	}

	results := make([]LegResult, 0, len(req.Legs))
	legRel := make([][]float64, len(req.Legs))
	legHourly := make([][]float64, len(req.Legs))
	for i, l := range req.Legs {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		legRel[i] = pathOf[legAsset[i]]
		results = append(results, simulateLeg(l, legRel[i], days, poolBy, perpBy, eth, btc, e.log))
		if h := hourlyOf[legAsset[i]]; h != nil {
			legHourly[i] = h
			if strings.ToLower(l.Kind) == "lp" {
				legHourly[i] = scale(h, poolBy[l.PoolID].Price)
			}
		}
	}
	applyManagedPerps(req.Legs, results, legRel, legHourly, days, perpBy, e.log)

	out := assemble(results, days, "scenario", "", "")
	out.Attribution = attribute(results, legHourly, days)
	if len(req.Legs) > 0 {
		l := req.Legs[0]
		base, label := 0.0, ""
		if strings.ToLower(l.Kind) == "lp" {
			p := poolBy[l.PoolID]
			base, label = p.Price, p.Token0+"/"+p.Token1
		} else {
			m := perpBy[l.PerpID]
			base, label = m.Price, m.ID
		}
		if rel := pathOf[legAsset[0]]; rel != nil && base > 0 {
			out.Price = make([]float64, len(rel))
			for i, r := range rel {
				out.Price[i] = base * r
			}
			out.PriceLabel = label
		}
	}
	return out, nil
}

func simulateLeg(l Leg, rel []float64, days int, poolBy map[string]market.Pool, perpBy map[string]market.Perp, eth, btc float64, log *slog.Logger) LegResult {
	kind := strings.ToLower(l.Kind)
	out := LegResult{Kind: kind, Capital: l.Capital, Series: make([]float64, days+1)}
	if rel == nil {
		rel = ones(days + 1)
	}

	if kind == "lp" {
		pool := poolBy[l.PoolID]
		out = newLPResult(l, pool.Token0, days)
		out.Title = "LP · " + pool.Token0 + "/" + pool.Token1
		q := market.QuoteUSD(pool.Token1, eth, btc, log)
		out.q = q
		p0 := pool.Price
		spacing := clmath.TickSpacing(pool.FeeBps)
		book := openLP(p0, l.Capital/q, l.Lower, l.Upper, spacing, 0, 0)
		start0, start1 := clmath.Amounts(p0, book.lower, book.upper, book.L)
		out.Lower, out.Upper = book.lower, book.upper
		feeRate := pool.FeeBps / 10_000
		lPool := poolActiveL(pool.TVL/q, p0)
		fees := 0.0
		inRangeDays := 0
		for d := 0; d <= days; d++ {
			p := p0 * rel[d]
			inR := p >= book.lower && p <= book.upper
			if d > 0 {
				if inR {
					inRangeDays++
				}
				tick := clmath.PriceToClosestTick(p, spacing, 0, 0)
				swaps := daySwaps(int64(d), p, pool.Volume, q, lPool, tick)
				f0, f1 := EstimateFees(swaps, int64(d), int64(d), book.tickLower, book.tickUpper, book.L, feeRate, DefaultFeeProtocol)
				fees += (f0*p + f1) * q
			}
			book = settleLPDay(&out, l, book, d, days, p, q, fees, feeRate, spacing, 0, 0)
		}
		endP := p0 * rel[days]
		hodl := (start0*endP + start1) * q
		out.Carry = fees
		out.Market = hodl - l.Capital
		// With rebalancing this also holds the loss realised at each recentre
		// and the swap fees it paid: the position simply ends worth less.
		out.IL = book.value(endP)*q - hodl
		if days > 0 {
			out.TimeInRange = float64(inRangeDays) / float64(days)
		}
	} else {
		m := perpBy[l.PerpID]
		label := "Long"
		if kind == "short" {
			label = "Short"
		}
		out.Title = label + " · " + m.ID
		dir := perps.Dir(kind)
		size := l.Capital * l.Leverage
		funding := 0.0
		dead := false
		frozen := 0.0
		for d := 0; d <= days; d++ {
			px := rel[d]
			if d > 0 && !dead {
				funding += -size * m.Funding * 3 * dir
			}
			raw := size * (px - 1) * dir
			eq := l.Capital + raw + funding
			if !dead && eq <= l.Capital*0.1 {
				dead = true
				out.Liquidated = true
				frozen = 0
			}
			if dead {
				out.Series[d] = frozen
			} else {
				out.Series[d] = eq
			}
		}
		out.Carry = funding
		out.funding = funding
		if out.Liquidated {
			out.Market = -l.Capital - funding
		} else {
			out.Market = size * (rel[days] - 1) * dir
		}
	}

	out.End = out.Series[len(out.Series)-1]
	out.NetPnl = out.End - l.Capital
	if l.Capital != 0 {
		out.ReturnPct = out.NetPnl / l.Capital * 100
	}
	return out
}

func legUnderlying(l Leg, poolBy map[string]market.Pool, perpBy map[string]market.Perp) string {
	if strings.ToLower(l.Kind) == "lp" {
		return poolBy[l.PoolID].Token0
	}
	return perpBy[l.PerpID].Base
}

func assemble(results []LegResult, days int, source, from, to string) Result {
	n := days + 1
	combined := make([]float64, n)
	capital := 0.0
	for _, r := range results {
		capital += r.Capital
		for d := 0; d < n && d < len(r.Series); d++ {
			combined[d] += r.Series[d]
		}
	}

	peak := combined[0]
	dd := 0.0
	rets := make([]float64, 0, n)
	for d := 0; d < n; d++ {
		if combined[d] > peak {
			peak = combined[d]
		}
		if peak > 0 {
			draw := (combined[d] - peak) / peak
			if draw < dd {
				dd = draw
			}
		}
		if d > 0 && combined[d-1] > 0 {
			rets = append(rets, combined[d]/combined[d-1]-1)
		}
	}
	mean, sd := meanSD(rets)
	sharpe := 0.0
	if sd > 0 {
		sharpe = mean / sd * math.Sqrt(365)
	}

	var fees, il, fund, hedge, rebalanceCost float64
	rebalances := 0
	var tirSum float64
	lpN := 0
	for _, r := range results {
		rebalanceCost += r.RebalanceCost
		if r.Kind == "lp" {
			fees += r.Carry
			il += r.IL
			rebalances += r.Rebalances
			tirSum += r.TimeInRange
			lpN++
		} else {
			fund += r.funding
			hedge += r.Market
		}
	}
	var avgTIR *float64
	if lpN > 0 {
		v := tirSum / float64(lpN)
		avgTIR = &v
	}
	var hedgeEff *float64
	if il != 0 {
		v := hedge / -il
		hedgeEff = &v
	}

	pnl := combined[n-1] - capital
	ret := 0.0
	if capital != 0 {
		ret = pnl / capital * 100
	}
	return Result{
		Days: days, Capital: capital, Combined: combined, Legs: results,
		NetPnl: pnl, ReturnPct: ret, MaxDrawdownPct: dd * 100, Sharpe: sharpe,
		FeesEarned: fees, ImpermanentLoss: il, Funding: fund, PerpMarketPnl: hedge,
		AvgTimeInRange: avgTIR, HedgeEffectiveness: hedgeEff,
		Rebalances: rebalances, RebalanceCost: rebalanceCost,
		Source: source, From: from, To: to,
	}
}

func scale(xs []float64, k float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = x * k
	}
	return out
}

func ones(n int) []float64 {
	s := make([]float64, n)
	for i := range s {
		s[i] = 1
	}
	return s
}

func meanSD(xs []float64) (mean, sd float64) {
	if len(xs) == 0 {
		return 0, 1e-9
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	var ss float64
	for _, x := range xs {
		d := x - mean
		ss += d * d
	}
	sd = math.Sqrt(ss / float64(len(xs)))
	if sd == 0 {
		sd = 1e-9
	}
	return mean, sd
}

// Additive reports whether net == carry + market + il within relative 1e-6.
func Additive(r LegResult) bool {
	sum := r.Carry + r.Market + r.IL
	den := math.Abs(r.NetPnl)
	if den < 1e-9 {
		return math.Abs(sum-r.NetPnl) < 1e-6
	}
	return math.Abs(sum-r.NetPnl)/den < 1e-6
}
