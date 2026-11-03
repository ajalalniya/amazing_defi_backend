package history

import (
	"sort"
	"strings"
)

// Spec is an on-chain Uniswap v3 pool that Amazing pool IDs can bind to.
type Spec struct {
	Address        string
	ChainID        int
	Decimals0      int
	Decimals1      int
	TickSpacing    int
	FeeRate        float64
	LiquidityScale float64
	StartBlock     uint64
	InvertPrice    bool
	Token0         string // address
	Token1         string // address
	Token0Sym      string
	Token1Sym      string
}

const baseETHUSDC = "0xd0b53d9277642d899df5c87a3966a349a798f224"

// BaseETHUSDC is the Base 0.05% WETH/USDC pool from get_price_eth_usdc.py.
var BaseETHUSDC = Spec{
	Address:        baseETHUSDC,
	ChainID:        8453,
	Decimals0:      18,
	Decimals1:      6,
	TickSpacing:    10,
	FeeRate:        0.0005,
	LiquidityScale: 1e12,
	StartBlock:     40_982_309,
	Token0:         "0x4200000000000000000000000000000000000006",
	Token1:         "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
	Token0Sym:      "ETH",
	Token1Sym:      "USDC",
}

const arbETHUSDC = "0xc6962004f452be9203591991d15f6b388e09e8d0"

// ArbETHUSDC is the Arbitrum Uniswap v3 WETH/USDC 0.05% pool.
var ArbETHUSDC = Spec{
	Address:        arbETHUSDC,
	ChainID:        42161,
	Decimals0:      18,
	Decimals1:      6,
	TickSpacing:    10,
	FeeRate:        0.0005,
	LiquidityScale: 1e12,
	StartBlock:     150_000_000,
	Token0:         "0x82aF49447D8a07e3bd95BD0d56f35241523fBab1",
	Token1:         "0xaf88d065e77c8cC2239327C5EDb3A432268e5831",
	Token0Sym:      "ETH",
	Token1Sym:      "USDC",
}

var amazingToSpec = map[string]Spec{
	"v3-eth-usdc-005":     BaseETHUSDC,
	"v4-eth-usdc-005":     BaseETHUSDC,
	"v3-eth-usdc-005-arb": ArbETHUSDC,
}

// SpecFor returns the on-chain pool for an Amazing market id.
func SpecFor(poolID string) (Spec, bool) {
	s, ok := amazingToSpec[strings.ToLower(poolID)]
	return s, ok
}

// Binding maps an Amazing market id onto a Uniswap pool.
type Binding struct {
	ID   string
	Spec Spec
}

// Bindings is the live-market catalog: every Amazing id with an on-chain pool.
func Bindings() []Binding {
	ids := make([]string, 0, len(amazingToSpec))
	for id := range amazingToSpec {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Binding, 0, len(ids))
	for _, id := range ids {
		out = append(out, Binding{ID: id, Spec: amazingToSpec[id]})
	}
	return out
}

// UniqueBindings is one row per on-chain pool address (v3 ids win over v4 aliases).
func UniqueBindings() []Binding {
	seen := map[string]bool{}
	var out []Binding
	for _, b := range Bindings() {
		key := strings.ToLower(b.Spec.Address)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, b)
	}
	return out
}

func (s Spec) FileKey() string {
	return strings.TrimPrefix(strings.ToLower(s.Address), "0x")
}
