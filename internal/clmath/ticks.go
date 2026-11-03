package clmath

import "math"

const tickBase = 1.0001

// TickSpacing is the Uniswap v3 spacing for a fee tier.
// FeeBps is the pool's fee in basis points (5 = 0.05%).
func TickSpacing(feeBps float64) int {
	switch {
	case feeBps <= 1:
		return 1
	case feeBps <= 5:
		return 10
	case feeBps <= 30:
		return 60
	default:
		return 200
	}
}

// NearestUsableTick rounds tick to a multiple of tickSpacing.
func NearestUsableTick(tick, tickSpacing int) int {
	if tickSpacing <= 0 {
		return tick
	}
	return int(math.Round(float64(tick)/float64(tickSpacing))) * tickSpacing
}

// PriceToClosestTick converts a human token1-per-token0 price to the nearest
// usable tick. decimals0/decimals1 scale the price the same way Uniswap does
// (raw = human * 10^(dec1-dec0)). Pass 0, 0 when prices are already human.
func PriceToClosestTick(priceHuman float64, tickSpacing, decimals0, decimals1 int) int {
	if priceHuman <= 0 {
		return 0
	}
	raw := priceHuman * math.Pow(10, float64(decimals1-decimals0))
	if raw <= 0 {
		return 0
	}
	floored := int(math.Floor(math.Log(raw) / math.Log(tickBase)))
	return NearestUsableTick(floored, tickSpacing)
}

// TickToPrice is the inverse of PriceToClosestTick (human token1-per-token0).
func TickToPrice(tick, decimals0, decimals1 int) float64 {
	raw := math.Pow(tickBase, float64(tick))
	return raw * math.Pow(10, float64(decimals0-decimals1))
}

// SnapRange maps [lower, upper] onto usable ticks and returns both the ticks
// and the corresponding human prices. tickUpper is always > tickLower.
func SnapRange(lower, upper float64, tickSpacing, decimals0, decimals1 int) (tickLower, tickUpper int, lowerP, upperP float64) {
	if tickSpacing <= 0 {
		tickSpacing = 1
	}
	tickLower = PriceToClosestTick(lower, tickSpacing, decimals0, decimals1)
	tickUpper = PriceToClosestTick(upper, tickSpacing, decimals0, decimals1)
	if tickUpper <= tickLower {
		tickUpper = tickLower + tickSpacing
	}
	return tickLower, tickUpper, TickToPrice(tickLower, decimals0, decimals1), TickToPrice(tickUpper, decimals0, decimals1)
}
