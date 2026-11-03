package backtest

import "amazing/internal/clmath"

// DefaultFeeProtocol is Uniswap v3 slot0.feeProtocol = 0x44 (68).
// The low nibble is token0→token1, the high nibble is token1→token0.
// Each nibble 4 means the protocol takes 1/4 of swap fees and LPs keep 75%.
// Matches strategy_builder simulation_backend engine._SLOT0_FEE_PROTOCOL.
const DefaultFeeProtocol = 68

// TypicalBand is the ±15% range used to back out active tick liquidity from TVL.
// Same reference band as clmath.RefConcentration.
const TypicalBand = 0.15

// Swap is one Uniswap v3 Swap event in human token units.
// Amount0/Amount1 are signed: positive = tokens into the pool (the fee base).
type Swap struct {
	Timestamp int64
	Amount0   float64
	Amount1   float64
	Tick      int
	Liquidity float64
}

// LPMultiplier is the fraction of swap fees the LP keeps after the protocol cut.
// feeProtocol <= 0 means the protocol takes nothing.
func LPMultiplier(feeProtocol int) float64 {
	if feeProtocol <= 0 {
		return 1
	}
	return 1 - 1/float64(feeProtocol)
}

// EstimateFees returns (fees_token0, fees_token1) earned by a position over a
// time window. Port of strategy_builder engine._estimate_fees:
//
//	share = clip(positionL / swap.liquidity, 0, 1)
//	inbound = max(amount, 0)   // the token the swapper paid in
//	fees   = inbound * feeRate * share * protocolKeep
//
// Only swaps whose ending tick sits inside [tickLower, tickUpper] count —
// the same approximation the CSV fee estimator uses.
func EstimateFees(swaps []Swap, openS, closeS int64, tickLower, tickUpper int, positionL, feeRate float64, feeProtocol int) (fees0, fees1 float64) {
	if positionL <= 0 || feeRate <= 0 || len(swaps) == 0 {
		return 0, 0
	}
	lo, hi := tickLower, tickUpper
	if lo > hi {
		lo, hi = hi, lo
	}
	a, b := openS, closeS
	if a > b {
		a, b = b, a
	}
	mZfo := LPMultiplier(feeProtocol % 16)
	mOfz := LPMultiplier(feeProtocol >> 4)

	for _, s := range swaps {
		if s.Timestamp < a || s.Timestamp > b {
			continue
		}
		if s.Tick < lo || s.Tick > hi {
			continue
		}
		if s.Liquidity <= 0 {
			continue
		}
		share := positionL / s.Liquidity
		if share < 0 {
			share = 0
		} else if share > 1 {
			share = 1
		}
		mult := 1.0
		if s.Amount0 > 0 && s.Amount1 < 0 {
			mult = mZfo
		} else if s.Amount1 > 0 && s.Amount0 < 0 {
			mult = mOfz
		}
		in0, in1 := 0.0, 0.0
		if s.Amount0 > 0 {
			in0 = s.Amount0
		}
		if s.Amount1 > 0 {
			in1 = s.Amount1
		}
		fees0 += in0 * feeRate * share * mult
		fees1 += in1 * feeRate * share * mult
	}
	return fees0, fees1
}

// poolActiveL approximates liquidity sitting on the current tick as a typical
// ±15% position sized to the pool's TVL (in token1 units).
func poolActiveL(tvlToken1, spot float64) float64 {
	if tvlToken1 <= 0 || spot <= 0 {
		return 0
	}
	return clmath.LiquidityFor(tvlToken1, spot, spot*(1-TypicalBand), spot*(1+TypicalBand))
}

// daySwaps turns a day's USD volume into two signed Uniswap swaps (token1-in
// and token0-in) at `spot`, with pool liquidity Lpool. Amounts are human units.
func daySwaps(day int64, spot, volumeUSD, quoteUSD, lPool float64, tick int) []Swap {
	if spot <= 0 || volumeUSD <= 0 || quoteUSD <= 0 || lPool <= 0 {
		return nil
	}
	vol1 := (volumeUSD / quoteUSD) / 2
	return []Swap{
		{Timestamp: day, Amount0: -vol1 / spot, Amount1: vol1, Tick: tick, Liquidity: lPool},
		{Timestamp: day, Amount0: vol1 / spot, Amount1: -vol1, Tick: tick, Liquidity: lPool},
	}
}
