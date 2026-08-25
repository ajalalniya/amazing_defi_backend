package simulate

import "amazing/internal/market"

// SeedPools is the 14 prototype pools (7 v3, 7 v4). Literal values must match
// index.html so the frontend renders identical numbers.
func SeedPools() []market.Pool {
	return []market.Pool{
		{ID: "v3-eth-usdc-005", Version: "v3", Token0: "ETH", Token1: "USDC", FeeBps: 5, Price: 3184.20, Change: 2.41, TVL: 184_300_000, Volume: 96_400_000, APR: 19.1, Sigma: 0.62},
		{ID: "v3-eth-usdc-030", Version: "v3", Token0: "ETH", Token1: "USDC", FeeBps: 30, Price: 3184.20, Change: 2.41, TVL: 41_800_000, Volume: 12_100_000, APR: 10.4, Sigma: 0.62},
		{ID: "v3-wbtc-usdc-005", Version: "v3", Token0: "WBTC", Token1: "USDC", FeeBps: 5, Price: 64820.00, Change: 1.12, TVL: 88_600_000, Volume: 33_900_000, APR: 13.7, Sigma: 0.48},
		{ID: "v3-wbtc-eth-005", Version: "v3", Token0: "WBTC", Token1: "ETH", FeeBps: 5, Price: 20.36, Change: -1.24, TVL: 32_100_000, Volume: 7_400_000, APR: 8.6, Sigma: 0.31},
		{ID: "v3-arb-usdc-030", Version: "v3", Token0: "ARB", Token1: "USDC", FeeBps: 30, Price: 0.7412, Change: -3.87, TVL: 19_400_000, Volume: 8_800_000, APR: 24.8, Sigma: 0.94},
		{ID: "v3-link-eth-030", Version: "v3", Token0: "LINK", Token1: "ETH", FeeBps: 30, Price: 0.004512, Change: 0.64, TVL: 9_200_000, Volume: 2_300_000, APR: 16.2, Sigma: 0.71},
		{ID: "v3-usdc-usdt-001", Version: "v3", Token0: "USDC", Token1: "USDT", FeeBps: 1, Price: 0.9998, Change: 0.01, TVL: 57_800_000, Volume: 41_600_000, APR: 6.9, Sigma: 0.02},
		{ID: "v4-eth-usdc-005", Version: "v4", Token0: "ETH", Token1: "USDC", FeeBps: 5, Price: 3184.20, Change: 2.41, TVL: 122_700_000, Volume: 88_200_000, APR: 22.6, Sigma: 0.62, Hook: "Dynamic fee"},
		{ID: "v4-eth-usdc-dyn", Version: "v4", Token0: "ETH", Token1: "USDC", FeeBps: 2, Price: 3184.20, Change: 2.41, TVL: 64_500_000, Volume: 71_300_000, APR: 26.3, Sigma: 0.62, Hook: "Auto-rebalance"},
		{ID: "v4-wsteth-eth-001", Version: "v4", Token0: "wstETH", Token1: "ETH", FeeBps: 1, Price: 1.1746, Change: 0.08, TVL: 45_900_000, Volume: 16_800_000, APR: 9.4, Sigma: 0.09, Hook: "Oracle range"},
		{ID: "v4-arb-eth-030", Version: "v4", Token0: "ARB", Token1: "ETH", FeeBps: 30, Price: 0.000233, Change: -4.11, TVL: 11_600_000, Volume: 5_100_000, APR: 29.7, Sigma: 0.98, Hook: "Limit order"},
		{ID: "v4-gmx-usdc-030", Version: "v4", Token0: "GMX", Token1: "USDC", FeeBps: 30, Price: 28.74, Change: 4.92, TVL: 7_300_000, Volume: 3_600_000, APR: 31.4, Sigma: 0.86, Hook: "Dynamic fee"},
		{ID: "v4-pendle-eth-030", Version: "v4", Token0: "PENDLE", Token1: "ETH", FeeBps: 30, Price: 0.001284, Change: 6.31, TVL: 5_100_000, Volume: 2_900_000, APR: 38.2, Sigma: 1.12, Hook: "Dynamic fee"},
		{ID: "v4-rdnt-usdc-100", Version: "v4", Token0: "RDNT", Token1: "USDC", FeeBps: 100, Price: 0.0518, Change: -7.42, TVL: 2_400_000, Volume: 1_800_000, APR: 44.9, Sigma: 1.34, Hook: "MEV guard"},
	}
}

// SeedPerps is the 5 prototype perpetual markets. funding8h is a fraction.
func SeedPerps() []market.Perp {
	return []market.Perp{
		{ID: "ETH-USD", Base: "ETH", Price: 3184.20, Change: 2.41, OI: 412_000_000, Funding: 0.000071, Borrow: 0.000090, MaxLev: 50, Sigma: 0.62},
		{ID: "BTC-USD", Base: "WBTC", Price: 64820.00, Change: 1.12, OI: 688_000_000, Funding: 0.000043, Borrow: 0.000060, MaxLev: 50, Sigma: 0.48},
		{ID: "ARB-USD", Base: "ARB", Price: 0.7412, Change: -3.87, OI: 46_000_000, Funding: -0.000112, Borrow: 0.000140, MaxLev: 30, Sigma: 0.94},
		{ID: "LINK-USD", Base: "LINK", Price: 14.36, Change: 0.64, OI: 28_000_000, Funding: 0.000058, Borrow: 0.000110, MaxLev: 30, Sigma: 0.71},
		{ID: "GMX-USD", Base: "GMX", Price: 28.74, Change: 4.92, OI: 12_400_000, Funding: 0.000096, Borrow: 0.000160, MaxLev: 20, Sigma: 0.86},
	}
}
