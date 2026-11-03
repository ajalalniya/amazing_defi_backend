// Package perp is a GMX-style perpetual short used as the LP's delta hedge:
// size in ETH, fees and price impact per trade, funding, margin and
// liquidation. All P/L settles into the position's collateral every mark, so
// Collateral is the perp account's equity.
package perp

import (
	"fmt"
	"math"
)

// Config prices a perp.
//
// FundingAPR is the annualised funding rate with the usual sign convention:
// positive means longs pay shorts, so the hedge (a short) receives
// size·price·FundingAPR·dt; negative means the short pays.
type Config struct {
	FeeRate           float64 `yaml:"fee_rate" json:"fee_rate"`                     // per side, fraction of notional (0.0005 = 0.05%)
	ImpactBps         float64 `yaml:"impact_bps" json:"impact_bps"`                 // price impact per trade, bps of notional
	FundingAPR        float64 `yaml:"funding_apr" json:"funding_apr"`               // see type doc for sign
	MaintenanceMargin float64 `yaml:"maintenance_margin" json:"maintenance_margin"` // liquidated when equity/notional falls to this
	LiquidationFee    float64 `yaml:"liquidation_fee" json:"liquidation_fee"`       // fraction of notional lost on liquidation
}

// Validate checks the perp parameters are usable.
func (c Config) Validate() error {
	switch {
	case c.FeeRate < 0 || c.FeeRate > 0.05:
		return fmt.Errorf("perp.fee_rate %v: want a fraction between 0 and 0.05", c.FeeRate)
	case c.ImpactBps < 0:
		return fmt.Errorf("perp.impact_bps %v: must be >= 0", c.ImpactBps)
	case c.MaintenanceMargin < 0 || c.MaintenanceMargin >= 1:
		return fmt.Errorf("perp.maintenance_margin %v: want a fraction in [0, 1)", c.MaintenanceMargin)
	case c.LiquidationFee < 0 || c.LiquidationFee >= 1:
		return fmt.Errorf("perp.liquidation_fee %v: want a fraction in [0, 1)", c.LiquidationFee)
	}
	return nil
}

// Position is a short of Short ETH with Collateral USD backing it.
type Position struct {
	Short      float64 `json:"short"`
	Collateral float64 `json:"collateral"`
	MarkPrice  float64 `json:"markPrice"`
	// Cumulative flows, for the ledger and attribution.
	PnL        float64 `json:"pnl"`
	Fees       float64 `json:"fees"`
	Funding    float64 `json:"funding"`
	Trades     int     `json:"trades"`
	Liquidated bool    `json:"liquidated"`
}

// New opens an empty short account with collateral posted, marked at price.
func New(collateral, price float64) Position {
	return Position{Collateral: collateral, MarkPrice: price}
}

// Mark settles the short's P/L from the last mark to price into collateral
// and returns it: −Short·(price − last).
func (p *Position) Mark(price float64) float64 {
	pnl := -p.Short * (price - p.MarkPrice)
	p.MarkPrice = price
	p.PnL += pnl
	p.Collateral += pnl
	return pnl
}

// AccrueFunding books funding over dt years at the current mark (see Config
// for the sign) and returns it.
func (p *Position) AccrueFunding(c Config, dt float64) float64 {
	f := p.Short * p.MarkPrice * c.FundingAPR * dt
	p.Funding += f
	p.Collateral += f
	return f
}

// Trade resizes the short to target ETH at the current mark, paying fee and
// impact on the size traded, and returns that cost. A negative target is
// treated as zero (the hedge never goes long).
func (p *Position) Trade(c Config, target float64) float64 {
	target = math.Max(0, target)
	d := math.Abs(target - p.Short)
	if d < 1e-12 {
		return 0
	}
	cost := d * p.MarkPrice * (c.FeeRate + c.ImpactBps/1e4)
	p.Short = target
	p.Fees += cost
	p.Collateral -= cost
	p.Trades++
	return cost
}

// Notional is the short's USD size at the mark.
func (p *Position) Notional() float64 { return p.Short * p.MarkPrice }

// MarginRatio is equity over notional (+Inf with no position).
func (p *Position) MarginRatio() float64 {
	n := p.Notional()
	if n <= 0 {
		return math.Inf(1)
	}
	return p.Collateral / n
}

// LiquidationPrice is the price at which equity/notional falls to the
// maintenance margin: Collateral − S·(P − m) = mm·S·P. 0 with no position.
func (p *Position) LiquidationPrice(c Config) float64 {
	if p.Short <= 0 {
		return 0
	}
	return (p.Collateral + p.Short*p.MarkPrice) / (p.Short * (1 + c.MaintenanceMargin))
}

// CheckLiquidation liquidates the short if its margin ratio has fallen to the
// maintenance margin: the position is closed and the liquidation fee taken
// from what collateral is left (never below zero). It reports whether it did.
func (p *Position) CheckLiquidation(c Config) bool {
	if p.Short <= 0 || p.MarginRatio() > c.MaintenanceMargin {
		return false
	}
	fee := math.Min(math.Max(p.Collateral, 0), c.LiquidationFee*p.Notional())
	p.Fees += fee
	p.Collateral -= fee
	p.Short = 0
	p.Liquidated = true
	return true
}
