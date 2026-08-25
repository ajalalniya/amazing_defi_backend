package market

import "context"

// Chain is the on-chain provider. Methods return ErrNotImplemented until
// subgraph/RPC/GMX wiring lands; the interface boundary is real from day one.
type Chain struct {
	RPC      string
	Subgraph string
}

func NewChain(rpc, subgraph string) *Chain {
	return &Chain{RPC: rpc, Subgraph: subgraph}
}

func (c *Chain) Pools(ctx context.Context) ([]Pool, error) {
	return nil, ErrNotImplemented
}

func (c *Chain) Perps(ctx context.Context) ([]Perp, error) {
	return nil, ErrNotImplemented
}

func (c *Chain) Depth(ctx context.Context, poolID string, bins int, lo, hi float64) ([]DepthBin, error) {
	return nil, ErrNotImplemented
}

func (c *Chain) Candles(ctx context.Context, id string, tf Timeframe) ([]Candle, error) {
	return nil, ErrNotImplemented
}

func (c *Chain) Subscribe(ctx context.Context) (<-chan PriceTick, error) {
	return nil, ErrNotImplemented
}

// Calldata is unsigned tx payload returned by quotes in chain mode.
type Calldata struct {
	To      string `json:"to"`
	Data    string `json:"data"`
	Value   string `json:"value"`
	ChainID int64  `json:"chainId"`
}
