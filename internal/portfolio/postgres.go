package portfolio

import (
	"database/sql"
	"time"

	"context"

	"amazing/internal/market"
)

// Postgres is a PositionStore over database/sql. The caller injects an opened
// *sql.DB (pgx stdlib, lib/pq, …). Schema is created on first use.
type Postgres struct {
	db *sql.DB
}

func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db}
}

const schema = `
CREATE TABLE IF NOT EXISTS positions (
    id          TEXT PRIMARY KEY,
    owner       TEXT NOT NULL,
    platform    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    market      TEXT NOT NULL,
    pool_id     TEXT NOT NULL DEFAULT '',
    perp_id     TEXT NOT NULL DEFAULT '',
    capital     DOUBLE PRECISION NOT NULL DEFAULT 0,
    lower_px    DOUBLE PRECISION NOT NULL DEFAULT 0,
    upper_px    DOUBLE PRECISION NOT NULL DEFAULT 0,
    fees        DOUBLE PRECISION NOT NULL DEFAULT 0,
    collateral  DOUBLE PRECISION NOT NULL DEFAULT 0,
    leverage    DOUBLE PRECISION NOT NULL DEFAULT 0,
    funding     DOUBLE PRECISION NOT NULL DEFAULT 0,
    entry       DOUBLE PRECISION NOT NULL,
    opened_at   TIMESTAMPTZ NOT NULL,
    tx_hash     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS positions_owner_idx ON positions (owner);
`

func (p *Postgres) Ensure(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, schema)
	return err
}

func (p *Postgres) List(ctx context.Context, owner string) ([]market.Position, error) {
	q := `SELECT id, owner, platform, kind, market, pool_id, perp_id, capital, lower_px, upper_px, fees, collateral, leverage, funding, entry, opened_at, tx_hash FROM positions`
	args := []any{}
	if owner != "" {
		q += ` WHERE owner = $1`
		args = append(args, owner)
	}
	q += ` ORDER BY opened_at, id`
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []market.Position
	for rows.Next() {
		pos, err := scanPos(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pos)
	}
	return out, rows.Err()
}

func (p *Postgres) Get(ctx context.Context, id string) (market.Position, error) {
	row := p.db.QueryRowContext(ctx, `SELECT id, owner, platform, kind, market, pool_id, perp_id, capital, lower_px, upper_px, fees, collateral, leverage, funding, entry, opened_at, tx_hash FROM positions WHERE id = $1`, id)
	pos, err := scanPos(row)
	if err == sql.ErrNoRows {
		return market.Position{}, ErrNotFound
	}
	return pos, err
}

func (p *Postgres) Insert(ctx context.Context, pos market.Position) (market.Position, error) {
	if pos.OpenedAt.IsZero() {
		pos.OpenedAt = time.Now().UTC()
	}
	_, err := p.db.ExecContext(ctx, `INSERT INTO positions (id, owner, platform, kind, market, pool_id, perp_id, capital, lower_px, upper_px, fees, collateral, leverage, funding, entry, opened_at, tx_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		pos.ID, pos.Owner, pos.Platform, pos.Kind, pos.Market, pos.PoolID, pos.PerpID,
		pos.Capital, pos.Lower, pos.Upper, pos.Fees, pos.Collateral, pos.Leverage, pos.Funding,
		pos.Entry, pos.OpenedAt, pos.TxHash)
	return pos, err
}

func (p *Postgres) Delete(ctx context.Context, id, owner string) (market.Position, error) {
	pos, err := p.Get(ctx, id)
	if err != nil {
		return market.Position{}, err
	}
	if owner != "" && pos.Owner != owner {
		return market.Position{}, ErrForbidden
	}
	_, err = p.db.ExecContext(ctx, `DELETE FROM positions WHERE id = $1`, id)
	return pos, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPos(r rowScanner) (market.Position, error) {
	var p market.Position
	err := r.Scan(&p.ID, &p.Owner, &p.Platform, &p.Kind, &p.Market, &p.PoolID, &p.PerpID,
		&p.Capital, &p.Lower, &p.Upper, &p.Fees, &p.Collateral, &p.Leverage, &p.Funding,
		&p.Entry, &p.OpenedAt, &p.TxHash)
	return p, err
}
