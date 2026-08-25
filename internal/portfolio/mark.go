package portfolio

import (
	"context"
	"log/slog"

	"amazing/internal/clmath"
	"amazing/internal/market"
	"amazing/internal/perps"
)

type Metrics struct {
	Mark      float64 `json:"mark"`
	Value     float64 `json:"value"`
	IL        float64 `json:"il"`
	PnL       float64 `json:"pnl"`
	Size      float64 `json:"size"`
	InRange   *bool   `json:"inRange,omitempty"`
	Liq       float64 `json:"liq,omitempty"`
	ReturnPct float64 `json:"returnPct"`
	Hodl      float64 `json:"hodl,omitempty"`
}

type MarkedPosition struct {
	market.Position
	Metrics
}

type Rollups struct {
	Total           float64 `json:"total"`
	CapitalDeployed float64 `json:"capitalDeployed"`
	LpPnl           float64 `json:"lpPnl"`
	PerpPnl         float64 `json:"perpPnl"`
	FeesCollected   float64 `json:"feesCollected"`
	FundingPaid     float64 `json:"fundingPaid"`
	ReturnPct       float64 `json:"returnPct"`
}

type Exposure struct {
	Asset     string  `json:"asset"`
	LpDelta   float64 `json:"lpDelta"`
	PerpDelta float64 `json:"perpDelta"`
	Net       float64 `json:"net"`
	Bias      string  `json:"bias"`
}

type Snapshot struct {
	Positions []MarkedPosition `json:"positions"`
	Rollups   Rollups          `json:"rollups"`
	Exposure  []Exposure       `json:"exposure"`
}

type Engine struct {
	log *slog.Logger
}

func NewEngine(log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{log: log}
}

func (e *Engine) Mark(pos market.Position, pools poolIndex, perpsIdx perpIndex) Metrics {
	eth, btc := market.MarksFromPerps(perpSlice(perpsIdx))
	if pos.Kind == "lp" {
		pool, ok := pools.get(pos.PoolID)
		if !ok {
			return Metrics{}
		}
		q := market.QuoteUSD(pool.Token1, eth, btc, e.log)
		L := clmath.LiquidityFor(pos.Capital/q, pos.Entry, pos.Lower, pos.Upper)
		start0, start1 := clmath.Amounts(pos.Entry, pos.Lower, pos.Upper, L)
		value := clmath.Value(pool.Price, pos.Lower, pos.Upper, L) * q
		hodl := (start0*pool.Price + start1) * q
		il := value - hodl
		pnl := (value - pos.Capital) + pos.Fees
		in := pool.Price >= pos.Lower && pool.Price <= pos.Upper
		ret := 0.0
		if pos.Capital != 0 {
			ret = pnl / pos.Capital * 100
		}
		return Metrics{Mark: pool.Price, Value: value, IL: il, PnL: pnl, Size: pos.Capital, InRange: &in, ReturnPct: ret, Hodl: hodl}
	}
	mkt, ok := perpsIdx.get(pos.PerpID)
	if !ok {
		return Metrics{}
	}
	size := perps.Size(pos.Collateral, pos.Leverage)
	dir := perps.Dir(pos.Kind)
	raw := perps.RawPnL(size, mkt.Price, pos.Entry, dir)
	pnl := raw + pos.Funding
	liq := perps.Liquidation(pos.Entry, pos.Leverage, dir)
	ret := 0.0
	if pos.Collateral != 0 {
		ret = pnl / pos.Collateral * 100
	}
	return Metrics{Mark: mkt.Price, Value: pos.Collateral + pnl, IL: raw, PnL: pnl, Size: size, Liq: liq, ReturnPct: ret}
}

func (e *Engine) Snapshot(ctx context.Context, positions []market.Position, src Prices) (Snapshot, error) {
	pools, err := src.Pools(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	perpList, err := src.Perps(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	po, pe := indexMarkets(pools, perpList)
	eth, btc := market.MarksFromPerps(perpList)

	marked := make([]MarkedPosition, 0, len(positions))
	var r Rollups
	expMap := make(map[string]*Exposure)
	expOrder := make([]string, 0)

	for _, p := range positions {
		m := e.Mark(p, po, pe)
		marked = append(marked, MarkedPosition{Position: p, Metrics: m})
		r.Total += m.PnL
		if p.Kind == "lp" {
			r.CapitalDeployed += p.Capital
			r.LpPnl += m.PnL
			r.FeesCollected += p.Fees
			pool, ok := po.get(p.PoolID)
			if ok {
				q := market.QuoteUSD(pool.Token1, eth, btc, e.log)
				d := clmath.Delta(p.Capital/q, pool.Price, p.Lower, p.Upper)
				usd := d * pool.Price * q
				ex := getExp(expMap, &expOrder, pool.Token0)
				ex.LpDelta += usd
			}
		} else {
			r.CapitalDeployed += p.Collateral
			r.PerpPnl += m.PnL
			r.FundingPaid += p.Funding
			mkt, ok := pe.get(p.PerpID)
			if ok {
				usd := p.Collateral * p.Leverage * perps.Dir(p.Kind)
				ex := getExp(expMap, &expOrder, mkt.Base)
				ex.PerpDelta += usd
			}
		}
	}
	if r.CapitalDeployed != 0 {
		r.ReturnPct = r.Total / r.CapitalDeployed * 100
	}
	exp := make([]Exposure, 0, len(expOrder))
	for _, a := range expOrder {
		v := expMap[a]
		v.Net = v.LpDelta + v.PerpDelta
		den := abs(v.LpDelta) + abs(v.PerpDelta)
		if den > 0 && abs(v.Net) < 0.15*den {
			v.Bias = "delta-neutral"
		} else if v.Net > 0 {
			v.Bias = "net-long"
		} else if v.Net < 0 {
			v.Bias = "net-short"
		} else {
			v.Bias = "delta-neutral"
		}
		v.Asset = a
		exp = append(exp, *v)
	}
	return Snapshot{Positions: marked, Rollups: r, Exposure: exp}, nil
}

func getExp(m map[string]*Exposure, order *[]string, asset string) *Exposure {
	if e, ok := m[asset]; ok {
		return e
	}
	e := &Exposure{Asset: asset}
	m[asset] = e
	*order = append(*order, asset)
	return e
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func perpSlice(idx perpIndex) []market.Perp {
	out := make([]market.Perp, 0, len(idx))
	for _, v := range idx {
		out = append(out, v)
	}
	return out
}
