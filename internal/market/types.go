package market

import (
	"context"
	"errors"
	"time"
)

var ErrNotImplemented = errors.New("not implemented")
var ErrNotFound = errors.New("not found")

// Pool is a concentrated-liquidity pool. JSON names are the API contract.
type Pool struct {
	ID      string  `json:"id"`
	Version string  `json:"version"`
	Token0  string  `json:"token0"`
	Token1  string  `json:"token1"`
	FeeBps  float64 `json:"feeBps"`
	Price   float64 `json:"price"`
	Change  float64 `json:"change24h"`
	TVL     float64 `json:"tvl"`
	Volume  float64 `json:"volume24h"`
	APR     float64 `json:"apr"`
	Sigma   float64 `json:"sigma"`
	Network string  `json:"network"`
	ChainID int     `json:"chainId,omitempty"`
	Hook    string  `json:"hook,omitempty"`
}

// Perp is a GMX-style perpetual market. Funding8h is a FRACTION per 8h
// (0.000071 == 0.0071%), never a percent.
type Perp struct {
	ID      string  `json:"id"`
	Base    string  `json:"base"`
	Price   float64 `json:"price"`
	Change  float64 `json:"change24h"`
	OI      float64 `json:"openInterest"`
	Funding float64 `json:"funding8h"`
	Borrow  float64 `json:"borrowHourly"`
	MaxLev  float64 `json:"maxLeverage"`
	Sigma   float64 `json:"sigma"`
}

// Position is an opened LP or perp leg. Server recomputes marks; never trust
// client-supplied P/L.
type Position struct {
	ID         string    `json:"id"`
	Owner      string    `json:"owner"`
	Platform   string    `json:"platform"`
	Kind       string    `json:"kind"`
	Market     string    `json:"market"`
	PoolID     string    `json:"poolId,omitempty"`
	PerpID     string    `json:"perpId,omitempty"`
	Capital    float64   `json:"capital,omitempty"`
	Lower      float64   `json:"lower,omitempty"`
	Upper      float64   `json:"upper,omitempty"`
	Fees       float64   `json:"fees,omitempty"`
	Collateral float64   `json:"collateral,omitempty"`
	Leverage   float64   `json:"leverage,omitempty"`
	Funding    float64   `json:"funding,omitempty"`
	Entry      float64   `json:"entry"`
	OpenedAt   time.Time `json:"openedAt"`
	TxHash     string    `json:"txHash,omitempty"`
}

type DepthBin struct {
	Price float64 `json:"price"`
	Depth float64 `json:"depth"`
}

type Candle struct {
	Time  int64   `json:"t"`
	Open  float64 `json:"o"`
	High  float64 `json:"h"`
	Low   float64 `json:"l"`
	Close float64 `json:"c"`
}

type PriceTick struct {
	ID        string  `json:"id"`
	Price     float64 `json:"price"`
	Change24h float64 `json:"change24h"`
}

type Timeframe struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Days  float64 `json:"days"`
}

// Provider is the market-data boundary. mock and chain both implement it.
type Provider interface {
	Pools(ctx context.Context) ([]Pool, error)
	Perps(ctx context.Context) ([]Perp, error)
	Depth(ctx context.Context, poolID string, bins int, lo, hi float64) ([]DepthBin, error)
	Candles(ctx context.Context, id string, tf Timeframe) ([]Candle, error)
	Subscribe(ctx context.Context) (<-chan PriceTick, error)
}

// Timeframes is the canonical set served by GET /api/v1/timeframes.
var Timeframes = []Timeframe{
	{ID: "1m", Label: "1m", Days: 1.0 / 1440},
	{ID: "5m", Label: "5m", Days: 5.0 / 1440},
	{ID: "15m", Label: "15m", Days: 15.0 / 1440},
	{ID: "1h", Label: "1H", Days: 1.0 / 24},
	{ID: "4h", Label: "4H", Days: 4.0 / 24},
	{ID: "1d", Label: "1D", Days: 1},
	{ID: "7d", Label: "7D", Days: 7},
	{ID: "30d", Label: "30D", Days: 30},
	{ID: "90d", Label: "90D", Days: 90},
	{ID: "1y", Label: "1Y", Days: 365},
}

func TimeframeByID(id string) (Timeframe, bool) {
	for _, tf := range Timeframes {
		if tf.ID == id {
			return tf, true
		}
	}
	return Timeframe{}, false
}

const MaxFunding8h = 0.01

func ValidFunding8h(v float64) bool {
	if v < 0 {
		v = -v
	}
	return v < MaxFunding8h
}
