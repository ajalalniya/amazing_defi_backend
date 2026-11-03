package market

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"amazing/internal/history"
)

func TestChainPoolsNeedRPC(t *testing.T) {
	h := history.New(t.TempDir(), map[int]string{}, slog.Default())
	c := NewChain(h, slog.Default())
	_, err := c.Pools(context.Background())
	if err == nil {
		t.Fatal("expected RPC error with no URLs configured")
	}
	if !strings.Contains(err.Error(), "BASE_RPC_URL") && !strings.Contains(err.Error(), "not set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPoolFromBinding(t *testing.T) {
	p := poolFromBinding(history.Binding{
		ID:   "v3-eth-usdc-005",
		Spec: history.BaseETHUSDC,
	}, history.Snapshot{Price: 3456.78, TVL: 1e6, Volume24h: 2e5, APR: 3.6, Change24h: 1.2})
	if p.Version != "v3" || p.Token0 != "ETH" || p.Token1 != "USDC" || p.FeeBps != 5 {
		t.Fatalf("got %+v", p)
	}
	if p.Price != 3456.78 || p.Network != "Base" || p.ChainID != 8453 || p.TVL != 1e6 {
		t.Fatalf("price/network %+v", p)
	}
}

func TestEthPerpFromPools(t *testing.T) {
	perps := ethPerpFromPools([]Pool{{Token0: "ETH", Token1: "USDC", Price: 3200}})
	if len(perps) != 1 || perps[0].ID != "ETH-USD" || perps[0].Price != 3200 {
		t.Fatalf("got %+v", perps)
	}
}

func TestLiveDepthBins(t *testing.T) {
	bins := liveDepthBins(3000, 0.62, 2000, 4000, 24)
	if len(bins) != 24 {
		t.Fatalf("len %d", len(bins))
	}
	var max float64
	for _, b := range bins {
		if b.Depth > max {
			max = b.Depth
		}
	}
	if max != 1 {
		t.Fatalf("max depth %v want 1", max)
	}
}
