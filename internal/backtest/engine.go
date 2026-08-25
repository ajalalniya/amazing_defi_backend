package backtest

import (
	"context"
	"log/slog"
	"math"
	"strings"

	"amazing/internal/clmath"
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
}

type Leg struct {
	Kind     string  `json:"kind"`
	PoolID   string  `json:"poolId,omitempty"`
	PerpID   string  `json:"perpId,omitempty"`
	Capital  float64 `json:"capital"`
	Lower    float64 `json:"lower,omitempty"`
	Upper    float64 `json:"upper,omitempty"`
	Leverage float64 `json:"leverage,omitempty"`
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
}

type Engine struct {
	log *slog.Logger
}

func New(log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{log: log}
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

	results := make([]LegResult, 0, len(req.Legs))
	for i, l := range req.Legs {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		results = append(results, simulateLeg(l, pathOf[legAsset[i]], days, poolBy, perpBy, eth, btc, e.log))
	}

	n := days + 1
	combined := make([]float64, n)
	capital := 0.0
	for _, r := range results {
		capital += r.Capital
		for d := 0; d < n; d++ {
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

	var fees, il, fund, hedge float64
	var tirSum float64
	lpN := 0
	for _, r := range results {
		if r.Kind == "lp" {
			fees += r.Carry
			il += r.IL
			tirSum += r.TimeInRange
			lpN++
		} else {
			fund += r.Carry
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
	}, nil
}

func simulateLeg(l Leg, rel []float64, days int, poolBy map[string]market.Pool, perpBy map[string]market.Perp, eth, btc float64, log *slog.Logger) LegResult {
	kind := strings.ToLower(l.Kind)
	out := LegResult{Kind: kind, Capital: l.Capital, Series: make([]float64, days+1)}
	if rel == nil {
		rel = ones(days + 1)
	}

	if kind == "lp" {
		pool := poolBy[l.PoolID]
		out.Title = "LP · " + pool.Token0 + "/" + pool.Token1
		q := market.QuoteUSD(pool.Token1, eth, btc, log)
		p0 := pool.Price
		L := clmath.LiquidityFor(l.Capital/q, p0, l.Lower, l.Upper)
		start0, start1 := clmath.Amounts(p0, l.Lower, l.Upper, L)
		conc := clmath.FeeMultiplier(p0, l.Lower, l.Upper)
		fees := 0.0
		inRangeDays := 0
		for d := 0; d <= days; d++ {
			p := p0 * rel[d]
			inR := p >= l.Lower && p <= l.Upper
			if inR && d > 0 {
				fees += l.Capital * (pool.APR / 100) * conc / 365
				inRangeDays++
			}
			val := clmath.Value(p, l.Lower, l.Upper, L) * q
			out.Series[d] = val + fees
		}
		endP := p0 * rel[days]
		hodl := (start0*endP + start1) * q
		out.Carry = fees
		out.Market = hodl - l.Capital
		out.IL = clmath.Value(endP, l.Lower, l.Upper, L)*q - hodl
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
