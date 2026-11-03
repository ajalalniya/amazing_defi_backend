package market

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"amazing/internal/history"
)

const chainCacheTTL = 2 * time.Second
const chainSeriesN = 150

// Chain is the on-chain market provider. Live spots, TVL, 24h volume and
// change come from Uniswap v3 contracts (and The Graph when configured).
type Chain struct {
	hist *history.Store
	log  *slog.Logger

	mu       sync.Mutex
	pools    []Pool
	perps    []Perp
	cachedAt time.Time
	series   map[string]*liveSeries
}

type liveSeries struct {
	t []int64
	p []float64
}

func NewChain(hist *history.Store, log *slog.Logger) *Chain {
	if log == nil {
		log = slog.Default()
	}
	return &Chain{hist: hist, log: log, series: map[string]*liveSeries{}}
}

func (c *Chain) refresh(ctx context.Context) error {
	c.mu.Lock()
	if c.pools != nil && time.Since(c.cachedAt) < chainCacheTTL {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if c.hist == nil {
		return fmt.Errorf("chain provider: no history store")
	}

	bindings := history.UniqueBindings()
	if len(bindings) == 0 {
		return fmt.Errorf("chain provider: no on-chain pool mappings")
	}

	type one struct {
		p   Pool
		err error
	}
	ch := make(chan one, len(bindings))
	for _, b := range bindings {
		b := b
		go func() {
			snap, err := c.hist.Snapshot(ctx, b.Spec)
			if err != nil {
				ch <- one{err: fmt.Errorf("%s: %w", b.ID, err)}
				return
			}
			ch <- one{p: poolFromBinding(b, snap)}
		}()
	}
	var pools []Pool
	var firstErr error
	for range bindings {
		r := <-ch
		if r.err != nil {
			if c.log != nil {
				c.log.Warn("live pool skipped", "err", r.err)
			}
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		pools = append(pools, r.p)
	}
	if len(pools) == 0 {
		if firstErr != nil {
			return firstErr
		}
		return fmt.Errorf("chain provider: no live pools")
	}
	sort.Slice(pools, func(i, j int) bool {
		if pools[i].Network != pools[j].Network {
			return pools[i].Network < pools[j].Network
		}
		return pools[i].ID < pools[j].ID
	})
	perps := ethPerpFromPools(pools)
	now := time.Now().UTC()

	c.mu.Lock()
	c.pools = pools
	c.perps = perps
	c.cachedAt = now
	for _, p := range pools {
		c.pushLocked(p.ID, now.Unix(), p.Price)
	}
	for _, p := range perps {
		c.pushLocked(p.ID, now.Unix(), p.Price)
	}
	c.mu.Unlock()
	return nil
}

func (c *Chain) pushLocked(id string, ts int64, price float64) {
	s := c.series[id]
	if s == nil {
		s = &liveSeries{}
		c.series[id] = s
	}
	n := len(s.p)
	if n > 0 && s.t[n-1] == ts {
		s.p[n-1] = price
		return
	}
	s.t = append(s.t, ts)
	s.p = append(s.p, price)
	if len(s.p) > chainSeriesN {
		s.t = s.t[1:]
		s.p = s.p[1:]
	}
}

func (c *Chain) Pools(ctx context.Context) ([]Pool, error) {
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Pool, len(c.pools))
	copy(out, c.pools)
	return out, nil
}

func (c *Chain) Perps(ctx context.Context) ([]Perp, error) {
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Perp, len(c.perps))
	copy(out, c.perps)
	return out, nil
}

func (c *Chain) poolLocked(id string) (Pool, bool) {
	for _, p := range c.pools {
		if p.ID == id {
			return p, true
		}
	}
	return Pool{}, false
}

func (c *Chain) Depth(ctx context.Context, poolID string, bins int, lo, hi float64) ([]DepthBin, error) {
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	p, ok := c.poolLocked(poolID)
	c.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	if lo <= 0 || hi <= lo {
		span := math.Exp(3 * (0.16 + p.Sigma*0.12))
		lo, hi = p.Price/span, p.Price*span
	}
	return liveDepthBins(p.Price, p.Sigma, lo, hi, bins), nil
}

func (c *Chain) Candles(ctx context.Context, id string, tf Timeframe) ([]Candle, error) {
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.series[id]
	price := 0.0
	if p, ok := c.poolLocked(id); ok {
		price = p.Price
	} else {
		for _, p := range c.perps {
			if p.ID == id {
				price = p.Price
				break
			}
		}
	}
	if price <= 0 {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	if s == nil || len(s.p) == 0 {
		return flatCandles(price, tf, now), nil
	}
	if len(s.p) < 8 {
		return flatCandles(price, tf, now), nil
	}
	return samplesToCandles(s.p, s.t, tf, now), nil
}

func (c *Chain) Subscribe(ctx context.Context) (<-chan PriceTick, error) {
	ch := make(chan PriceTick, 16)
	go func() {
		<-ctx.Done()
	}()
	return ch, nil
}

func poolFromBinding(b history.Binding, snap history.Snapshot) Pool {
	ver, t0, t1, fee := "v3", b.Spec.Token0Sym, b.Spec.Token1Sym, b.Spec.FeeRate*10_000
	parts := strings.Split(strings.ToLower(b.ID), "-")
	if len(parts) >= 1 && parts[0] != "" {
		ver = parts[0]
	}
	if t0 == "" && len(parts) >= 3 {
		t0 = tokenSymbol(parts[1])
		t1 = tokenSymbol(parts[2])
	}
	if t0 == "" {
		t0, t1 = "ETH", "USDC"
	}
	if len(parts) >= 4 {
		if n, err := strconv.Atoi(strings.TrimLeft(parts[3], "0")); err == nil && n > 0 {
			fee = float64(n)
		} else if parts[3] == "000" || parts[3] == "0" {
			fee = 0
		}
	}
	if fee <= 0 {
		fee = b.Spec.FeeRate * 10_000
	}
	if ver == "v4" {
		ver = "v3"
	}
	return Pool{
		ID:      b.ID,
		Version: ver,
		Token0:  t0,
		Token1:  t1,
		FeeBps:  fee,
		Price:   snap.Price,
		Change:  snap.Change24h,
		TVL:     snap.TVL,
		Volume:  snap.Volume24h,
		APR:     snap.APR,
		Sigma:   0.62,
		Network: history.NetworkLabel(b.Spec.ChainID),
		ChainID: b.Spec.ChainID,
	}
}

func tokenSymbol(s string) string {
	switch strings.ToLower(s) {
	case "wbtc":
		return "WBTC"
	case "wsteth":
		return "wstETH"
	default:
		return strings.ToUpper(s)
	}
}

func ethPerpFromPools(pools []Pool) []Perp {
	var eth float64
	for _, p := range pools {
		if strings.EqualFold(p.Token0, "ETH") && strings.EqualFold(p.Token1, "USDC") && p.Price > 0 {
			eth = p.Price
			break
		}
	}
	if eth == 0 {
		return nil
	}
	return []Perp{{
		ID: "ETH-USD", Base: "ETH", Price: eth, MaxLev: 50, Sigma: 0.62,
	}}
}

func liveDepthBins(spot, sigma, lo, hi float64, n int) []DepthBin {
	if n <= 0 {
		n = 48
	}
	if lo <= 0 || hi <= lo || spot <= 0 {
		return nil
	}
	bins := make([]DepthBin, n)
	max := 0.0
	for i := 0; i < n; i++ {
		p := lo * math.Pow(hi/lo, (float64(i)+0.5)/float64(n))
		z := math.Log(p/spot) / (0.16 + sigma*0.12)
		v := math.Exp(-0.5 * z * z)
		bins[i] = DepthBin{Price: p, Depth: v}
		if v > max {
			max = v
		}
	}
	if max > 0 {
		for i := range bins {
			bins[i].Depth /= max
		}
	}
	return bins
}

func flatCandles(price float64, tf Timeframe, now time.Time) []Candle {
	n := chainSeriesN
	out := make([]Candle, n)
	dt := time.Duration(tf.Days * 86400 / float64(n) * float64(time.Second))
	if dt < time.Second {
		dt = time.Second
	}
	for i := 0; i < n; i++ {
		t := now.Add(-dt * time.Duration(n-1-i)).Unix()
		out[i] = Candle{Time: t, Open: price, High: price, Low: price, Close: price}
	}
	return out
}

func samplesToCandles(prices []float64, times []int64, tf Timeframe, now time.Time) []Candle {
	_ = times
	out := flatCandles(prices[0], tf, now)
	n := len(out)
	src := prices
	if len(src) > n {
		src = src[len(src)-n:]
	}
	start := n - len(src)
	for i, p := range src {
		idx := start + i
		prev := p
		if idx > 0 {
			prev = out[idx-1].Close
		}
		hi, lo := prev, prev
		if p > hi {
			hi = p
		}
		if p < lo {
			lo = p
		}
		out[idx].Open = prev
		out[idx].High = hi
		out[idx].Low = lo
		out[idx].Close = p
	}
	return out
}
