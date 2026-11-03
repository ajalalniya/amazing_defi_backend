package history

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"
)

const (
	selToken0    = "0x0dfe1681"
	selToken1    = "0xd21220a7"
	selBalanceOf = "0x70a08231"
	statsTTL     = 90 * time.Second
)

// Snapshot is live pool metrics: spot from slot0, TVL from token balances,
// 24h volume from swaps (or the Graph when configured).
type Snapshot struct {
	Price     float64
	Change24h float64
	TVL       float64
	Volume24h float64
	APR       float64
}

type snapCache struct {
	at   time.Time
	snap Snapshot
}

func (s *Store) statsLock() {
	s.mu.Lock()
	if s.snaps == nil {
		s.snaps = map[string]snapCache{}
	}
	s.mu.Unlock()
}

// Snapshot loads real metrics for one Uniswap v3 pool.
func (s *Store) Snapshot(ctx context.Context, spec Spec) (Snapshot, error) {
	price, err := s.Spot(ctx, spec)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Price: price}

	key := strings.ToLower(spec.Address) + ":" + fmt.Sprintf("%d", spec.ChainID)
	s.statsLock()
	s.mu.Lock()
	cached, ok := s.snaps[key]
	s.mu.Unlock()
	if ok && time.Since(cached.at) < statsTTL {
		out.Change24h = cached.snap.Change24h
		out.TVL = cached.snap.TVL
		out.Volume24h = cached.snap.Volume24h
		out.APR = feeAPR(out.Volume24h, out.TVL, spec.FeeRate)
		return out, nil
	}

	var wg sync.WaitGroup
	var tvl, vol, change float64
	wg.Add(3)
	go func() {
		defer wg.Done()
		if v, err := s.tvlUSD(ctx, spec, price); err == nil {
			tvl = v
		} else if s.log != nil {
			s.log.Warn("tvl", "pool", spec.Address, "err", err)
		}
	}()
	go func() {
		defer wg.Done()
		if v, err := s.change24h(ctx, spec, price); err == nil {
			change = v
		}
	}()
	go func() {
		defer wg.Done()
		if v, err := s.volume24h(ctx, spec); err == nil {
			vol = v
		} else if s.log != nil {
			s.log.Warn("volume24h", "pool", spec.Address, "err", err)
		}
	}()
	wg.Wait()

	if s.Graph != nil {
		if g, err := s.Graph.PoolStats(ctx, spec); err == nil {
			if g.TVL > 0 {
				tvl = g.TVL
			}
			if g.Volume24h > 0 {
				vol = g.Volume24h
			}
			if g.Change24h != 0 {
				change = g.Change24h
			}
		} else if s.log != nil {
			s.log.Warn("subgraph", "pool", spec.Address, "err", err)
		}
	}

	out.TVL = tvl
	out.Volume24h = vol
	out.Change24h = change
	out.APR = feeAPR(vol, tvl, spec.FeeRate)

	s.mu.Lock()
	s.snaps[key] = snapCache{at: time.Now(), snap: out}
	s.mu.Unlock()
	return out, nil
}

func feeAPR(volume, tvl, feeRate float64) float64 {
	if tvl <= 0 || feeRate <= 0 || volume <= 0 {
		return 0
	}
	return volume * feeRate / tvl * 365 * 100
}

func isStableSymbol(s string) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "USDC", "USDT", "DAI", "USD", "USDBC", "USDC.E":
		return true
	}
	return false
}

func tvlUSD(amount0, amount1, price float64, spec Spec) float64 {
	if isStableSymbol(spec.Token1Sym) {
		return amount0*price + amount1
	}
	if isStableSymbol(spec.Token0Sym) {
		if price > 0 {
			return amount0 + amount1/price
		}
		return amount0
	}
	return amount0*price + amount1
}

func volumeUSD(rows []Row, spec Spec) float64 {
	var v float64
	for _, r := range rows {
		switch {
		case isStableSymbol(spec.Token1Sym):
			v += math.Abs(r.Amount1)
		case isStableSymbol(spec.Token0Sym):
			v += math.Abs(r.Amount0)
		default:
			v += math.Abs(r.Amount0) * r.Price
		}
	}
	return v
}

func (s *Store) tvlUSD(ctx context.Context, spec Spec, price float64) (float64, error) {
	c, err := s.rpcFor(spec.ChainID)
	if err != nil {
		return 0, err
	}
	t0, t1, err := s.tokens(ctx, c, spec)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	a0, err := erc20Balance(ctx, c, t0, spec.Address, spec.Decimals0)
	if err != nil {
		return 0, err
	}
	a1, err := erc20Balance(ctx, c, t1, spec.Address, spec.Decimals1)
	if err != nil {
		return 0, err
	}
	return tvlUSD(a0, a1, price, spec), nil
}

func (s *Store) tokens(ctx context.Context, c *rpcClient, spec Spec) (t0, t1 string, err error) {
	t0, t1 = spec.Token0, spec.Token1
	if t0 != "" && t1 != "" {
		return t0, t1, nil
	}
	if t0 == "" {
		t0, err = callAddress(ctx, c, spec.Address, selToken0)
		if err != nil {
			return "", "", err
		}
	}
	if t1 == "" {
		t1, err = callAddress(ctx, c, spec.Address, selToken1)
		if err != nil {
			return "", "", err
		}
	}
	return t0, t1, nil
}

func (s *Store) change24h(ctx context.Context, spec Spec, spot float64) (float64, error) {
	c, err := s.rpcFor(spec.ChainID)
	if err != nil {
		return 0, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return 0, err
	}
	searchLo := spec.StartBlock
	if searchLo == 0 || searchLo > head {
		if head > 200_000 {
			searchLo = head - 200_000
		} else {
			searchLo = 1
		}
	}
	target := time.Now().Unix() - 86400
	block, err := blockAtOrAfter(ctx, c, target, searchLo, head, map[uint64]int64{})
	if err != nil || block == 0 {
		return 0, err
	}
	var hex string
	if err := c.callN(ctx, "eth_call", []any{
		map[string]any{"to": spec.Address, "data": slot0Selector},
		hexUint(block),
	}, &hex, 3, 400*time.Millisecond); err != nil {
		return 0, err
	}
	prev, err := slot0PriceFromHex(hex, spec)
	if err != nil || prev <= 0 {
		return 0, err
	}
	return (spot - prev) / prev * 100, nil
}

func (s *Store) volume24h(ctx context.Context, spec Spec) (float64, error) {
	c, err := s.rpcFor(spec.ChainID)
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rows, err := fetchRange(ctx, c, spec, now-86400, now, nil)
	if err != nil {
		return 0, err
	}
	return volumeUSD(rows, spec), nil
}

func callAddress(ctx context.Context, c *rpcClient, to, data string) (string, error) {
	var hexStr string
	if err := c.callN(ctx, "eth_call", []any{
		map[string]any{"to": to, "data": data},
		"latest",
	}, &hexStr, 3, 300*time.Millisecond); err != nil {
		return "", err
	}
	return addrFromWord(hexStr)
}

func erc20Balance(ctx context.Context, c *rpcClient, token, owner string, decimals int) (float64, error) {
	data := selBalanceOf + padAddress(owner)
	var hexStr string
	if err := c.callN(ctx, "eth_call", []any{
		map[string]any{"to": token, "data": data},
		"latest",
	}, &hexStr, 3, 300*time.Millisecond); err != nil {
		return 0, err
	}
	b, err := hexBytes(hexStr)
	if err != nil {
		return 0, err
	}
	return scaleAmount(new(big.Int).SetBytes(b), decimals), nil
}

func addrFromWord(hexStr string) (string, error) {
	b, err := hexBytes(hexStr)
	if err != nil {
		return "", err
	}
	if len(b) < 20 {
		return "", fmt.Errorf("address too short")
	}
	return "0x" + hex.EncodeToString(b[len(b)-20:]), nil
}

func padAddress(addr string) string {
	h := strings.TrimPrefix(strings.ToLower(addr), "0x")
	if len(h) > 64 {
		h = h[len(h)-64:]
	}
	return strings.Repeat("0", 64-len(h)) + h
}
