package history

import (
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// SwapTopic is keccak256("Swap(address,address,int256,int256,uint160,uint128,int24)").
const SwapTopic = "0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67"

var two96 = new(big.Float).SetInt(new(big.Int).Lsh(big.NewInt(1), 96))

// Row is one Uniswap v3 Swap in human token units (amounts already decimal-scaled).
type Row struct {
	Amount0      float64
	Amount1      float64
	Timestamp    int64
	Liquidity    float64
	SqrtPriceX96 string
	Tick         int
	Price        float64
}

func decodeSwapData(data []byte) (amount0, amount1 *big.Int, sqrtX96, liq *big.Int, tick int64, err error) {
	if len(data) < 160 {
		return nil, nil, nil, nil, 0, fmt.Errorf("swap data too short: %d", len(data))
	}
	amount0 = wordInt(data[0:32])
	amount1 = wordInt(data[32:64])
	sqrtX96 = new(big.Int).SetBytes(data[64:96])
	liq = new(big.Int).SetBytes(data[96:128])
	tick = wordInt(data[128:160]).Int64()
	return amount0, amount1, sqrtX96, liq, tick, nil
}

func wordInt(b []byte) *big.Int {
	n := new(big.Int).SetBytes(b)
	if len(b) > 0 && b[0]&0x80 != 0 {
		mod := new(big.Int).Lsh(big.NewInt(1), 256)
		n.Sub(n, mod)
	}
	return n
}

func hexBytes(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	if len(s)%2 == 1 {
		s = "0" + s
	}
	return hex.DecodeString(s)
}

func parseHexUint(s string) (uint64, error) {
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	if s == "" {
		return 0, nil
	}
	var v uint64
	_, err := fmt.Sscanf(s, "%x", &v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func hexUint(n uint64) string {
	return fmt.Sprintf("0x%x", n)
}

func bigToFloat(n *big.Int) float64 {
	f, _ := new(big.Float).SetInt(n).Float64()
	return f
}

func scaleAmount(raw *big.Int, decimals int) float64 {
	if raw == nil {
		return 0
	}
	f := new(big.Float).SetInt(raw)
	den := new(big.Float).SetFloat64(math.Pow(10, float64(decimals)))
	out, _ := f.Quo(f, den).Float64()
	return out
}

// SqrtPriceX96ToPrice is human token1-per-token0.
func SqrtPriceX96ToPrice(sqrtX96 *big.Int, decimals0, decimals1 int) float64 {
	if sqrtX96 == nil || sqrtX96.Sign() <= 0 {
		return 0
	}
	sf := new(big.Float).SetInt(sqrtX96)
	ratio := new(big.Float).Quo(sf, two96)
	price := new(big.Float).Mul(ratio, ratio)
	scale := math.Pow(10, float64(decimals0-decimals1))
	price.Mul(price, big.NewFloat(scale))
	out, _ := price.Float64()
	return out
}
