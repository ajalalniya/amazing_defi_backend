package portfolio

import (
	"context"
	"errors"
	"time"

	"amazing/internal/market"
)

var (
	ErrNotFound = errors.New("position not found")
	ErrForbidden = errors.New("not owner")
)

// Store persists positions. In-memory for mock; Postgres behind the same interface.
type Store interface {
	List(ctx context.Context, owner string) ([]market.Position, error)
	Get(ctx context.Context, id string) (market.Position, error)
	Insert(ctx context.Context, p market.Position) (market.Position, error)
	Delete(ctx context.Context, id, owner string) (market.Position, error)
}

type Prices interface {
	Pools(ctx context.Context) ([]market.Pool, error)
	Perps(ctx context.Context) ([]market.Perp, error)
}

type poolIndex map[string]market.Pool
type perpIndex map[string]market.Perp

func indexMarkets(pools []market.Pool, perps []market.Perp) (poolIndex, perpIndex) {
	po := make(poolIndex, len(pools))
	for _, p := range pools {
		po[p.ID] = p
	}
	pe := make(perpIndex, len(perps))
	for _, p := range perps {
		pe[p.ID] = p
	}
	return po, pe
}

func (p poolIndex) get(id string) (market.Pool, bool) { v, ok := p[id]; return v, ok }
func (p perpIndex) get(id string) (market.Perp, bool) { v, ok := p[id]; return v, ok }

// DemoOwner is the wallet the prototype's six seeded positions belong to.
const DemoOwner = "demo"

func SeedPositions(now time.Time) []market.Position {
	opened := now.Add(-14 * 24 * time.Hour).UTC().Truncate(time.Hour)
	return []market.Position{
		{ID: "p1", Owner: DemoOwner, Platform: "v3", PoolID: "v3-eth-usdc-005", Market: "ETH/USDC", Kind: "lp", Capital: 42_000, Entry: 2980.00, Lower: 2750, Upper: 3300, Fees: 1_842, OpenedAt: opened},
		{ID: "p2", Owner: DemoOwner, Platform: "v4", PoolID: "v4-eth-usdc-dyn", Market: "ETH/USDC", Kind: "lp", Capital: 26_500, Entry: 3050.00, Lower: 2900, Upper: 3450, Fees: 1_106, OpenedAt: opened},
		{ID: "p3", Owner: DemoOwner, Platform: "v4", PoolID: "v4-arb-eth-030", Market: "ARB/ETH", Kind: "lp", Capital: 11_800, Entry: 0.000246, Lower: 0.000210, Upper: 0.000280, Fees: 742, OpenedAt: opened},
		{ID: "p4", Owner: DemoOwner, Platform: "gmx", PerpID: "ETH-USD", Market: "ETH-USD", Kind: "short", Collateral: 9_000, Leverage: 3, Entry: 3105.00, Funding: -184, OpenedAt: opened},
		{ID: "p5", Owner: DemoOwner, Platform: "gmx", PerpID: "BTC-USD", Market: "BTC-USD", Kind: "long", Collateral: 6_000, Leverage: 5, Entry: 61_400.00, Funding: -212, OpenedAt: opened},
		{ID: "p6", Owner: DemoOwner, Platform: "gmx", PerpID: "ARB-USD", Market: "ARB-USD", Kind: "short", Collateral: 4_000, Leverage: 4, Entry: 0.8120, Funding: 96, OpenedAt: opened},
	}
}
