package history

import (
	"context"
	"fmt"
	"math/big"
	"time"
)

// slot0() selector — Uniswap v3 / v4-compatible pools.
const slot0Selector = "0x3850c7bd"

// Spot reads the pool's current token1-per-token0 price via slot0.
func (s *Store) Spot(ctx context.Context, spec Spec) (float64, error) {
	c, err := s.rpcFor(spec.ChainID)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var hex string
	if err := c.callN(ctx, "eth_call", []any{
		map[string]any{"to": spec.Address, "data": slot0Selector},
		"latest",
	}, &hex, 3, 400*time.Millisecond); err != nil {
		return 0, fmt.Errorf("slot0 %s: %w", spec.Address, err)
	}
	return slot0PriceFromHex(hex, spec)
}

func slot0PriceFromHex(hex string, spec Spec) (float64, error) {
	data, err := hexBytes(hex)
	if err != nil {
		return 0, err
	}
	return parseSlot0Price(data, spec)
}

func parseSlot0Price(data []byte, spec Spec) (float64, error) {
	if len(data) < 32 {
		return 0, fmt.Errorf("slot0 result too short: %d", len(data))
	}
	sqrt := new(big.Int).SetBytes(data[0:32])
	price := SqrtPriceX96ToPrice(sqrt, spec.Decimals0, spec.Decimals1)
	if spec.InvertPrice && price != 0 {
		price = 1 / price
	}
	if price <= 0 {
		return 0, fmt.Errorf("slot0 price is zero")
	}
	return price, nil
}
