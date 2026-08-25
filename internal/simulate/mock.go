package simulate

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"amazing/internal/market"
)

const DefaultTickInterval = 2500 * time.Millisecond

// Mock is the zero-dependency market provider. Deterministic, boots instantly.
type Mock struct {
	log  *slog.Logger
	mu   sync.RWMutex
	pools []market.Pool
	perps []market.Perp

	series map[string]*Window // id:tf → window
	tick   *RNG

	subsMu sync.Mutex
	subs   map[chan market.PriceTick]struct{}
}

func NewMock(log *slog.Logger) *Mock {
	if log == nil {
		log = slog.Default()
	}
	pools := SeedPools()
	perps := SeedPerps()
	for _, p := range perps {
		if !market.ValidFunding8h(p.Funding) {
			log.Error("rejecting seed perp with implausible funding8h", "id", p.ID, "funding8h", p.Funding)
		}
	}
	return &Mock{
		log:    log,
		pools:  pools,
		perps:  perps,
		series: make(map[string]*Window),
		tick:   Mulberry32(7),
		subs:   make(map[chan market.PriceTick]struct{}),
	}
}

// Start runs the live price loop until ctx is cancelled.
func (m *Mock) Start(ctx context.Context) {
	go m.loop(ctx)
}

func (m *Mock) loop(ctx context.Context) {
	t := time.NewTicker(DefaultTickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.subsMu.Lock()
			m.subs = make(map[chan market.PriceTick]struct{})
			m.subsMu.Unlock()
			return
		case <-t.C:
			m.Step()
		}
	}
}

// Step applies one 10-second-of-market-time tick. Exported for tests.
func (m *Mock) Step() {
	m.mu.Lock()
	defer m.mu.Unlock()
	vol := TickVol()

	for i := range m.perps {
		p := &m.perps[i]
		step := p.Price * p.Sigma * vol * Gauss(m.tick)
		p.Price = math.Max(p.Price*0.5, p.Price+step)
		p.Change += step / p.Price * 100
	}

	for i := range m.pools {
		p := &m.pools[i]
		step := p.Price * p.Sigma * vol * Gauss(m.tick)
		p.Price = math.Max(p.Price*0.5, p.Price+step)
		p.Change += step / p.Price * 100
		if link := m.perpByBaseLocked(p.Token0); link != nil && (p.Token1 == "USDC" || p.Token1 == "USDT") {
			p.Price = link.Price
			p.Change = link.Change
		}
		m.advanceLocked(p.ID, p.Price)
	}
	for i := range m.perps {
		m.advanceLocked(m.perps[i].ID, m.perps[i].Price)
	}

	ticks := make([]market.PriceTick, 0, len(m.pools)+len(m.perps))
	for _, p := range m.perps {
		ticks = append(ticks, market.PriceTick{ID: p.ID, Price: p.Price, Change24h: p.Change})
	}
	for _, p := range m.pools {
		ticks = append(ticks, market.PriceTick{ID: p.ID, Price: p.Price, Change24h: p.Change})
	}
	m.broadcast(ticks)
}

func (m *Mock) perpByBaseLocked(base string) *market.Perp {
	for i := range m.perps {
		if m.perps[i].Base == base {
			return &m.perps[i]
		}
	}
	return nil
}

func (m *Mock) advanceLocked(id string, spot float64) {
	prefix := id + ":"
	for k, w := range m.series {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			w.Advance(spot, m.tick)
		}
	}
}

func (m *Mock) broadcast(ticks []market.PriceTick) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	for ch := range m.subs {
		for _, t := range ticks {
			select {
			case ch <- t:
			default:
				// drop this tick for a slow subscriber; keep the channel
			}
		}
	}
}

func (m *Mock) Pools(ctx context.Context) ([]market.Pool, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]market.Pool, len(m.pools))
	copy(out, m.pools)
	return out, nil
}

func (m *Mock) Perps(ctx context.Context) ([]market.Perp, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]market.Perp, len(m.perps))
	copy(out, m.perps)
	return out, nil
}

func (m *Mock) Pool(id string) (market.Pool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.pools {
		if p.ID == id {
			return p, true
		}
	}
	return market.Pool{}, false
}

func (m *Mock) Perp(id string) (market.Perp, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.perps {
		if p.ID == id {
			return p, true
		}
	}
	return market.Perp{}, false
}

func (m *Mock) Depth(ctx context.Context, poolID string, bins int, lo, hi float64) ([]market.DepthBin, error) {
	_ = ctx
	p, ok := m.Pool(poolID)
	if !ok {
		return nil, market.ErrNotFound
	}
	if lo <= 0 || hi <= lo {
		lo, hi = defaultDepthSpan(p.Price, p.Sigma)
	}
	return LiquidityBins(p.ID, p.Price, p.Sigma, lo, hi, bins), nil
}

func (m *Mock) Candles(ctx context.Context, id string, tf market.Timeframe) ([]market.Candle, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	price, sigma, ok := m.priceSigmaLocked(id)
	if !ok {
		return nil, market.ErrNotFound
	}
	key := seriesKey(id, tf.ID)
	w := m.series[key]
	if w == nil {
		w = newWindow(id, price, sigma, tf)
		m.series[key] = w
	}
	hist := append([]float64(nil), w.Hist...)
	return candlesFromHist(hist, tf, time.Now()), nil
}

func (m *Mock) priceSigmaLocked(id string) (price, sigma float64, ok bool) {
	for _, p := range m.pools {
		if p.ID == id {
			return p.Price, p.Sigma, true
		}
	}
	for _, p := range m.perps {
		if p.ID == id {
			return p.Price, p.Sigma, true
		}
	}
	return 0, 0, false
}

func (m *Mock) Subscribe(ctx context.Context) (<-chan market.PriceTick, error) {
	ch := make(chan market.PriceTick, 256)
	m.subsMu.Lock()
	m.subs[ch] = struct{}{}
	m.subsMu.Unlock()
	go func() {
		<-ctx.Done()
		m.subsMu.Lock()
		delete(m.subs, ch)
		m.subsMu.Unlock()
	}()
	return ch, nil
}

// Marks returns current ETH and BTC USD prices.
func (m *Mock) Marks() (eth, btc float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return market.MarksFromPerps(m.perps)
}
