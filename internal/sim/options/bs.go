// Package options is the optional true gamma hedge: long straddles or
// strangles priced with Black–Scholes, sized to offset a fraction of the
// LP's negative gamma, bought only when a range is likely to break.
//
// A perp has no gamma; options do. Buying them adds positive gamma that
// offsets part of the LP's, at the cost of the premium (theta). The overlay
// pays off when a range regime breaks out sharply and loses its premium when
// it does not.
package options

import (
	"fmt"
	"math"
	"time"

	"amazing/internal/sim/core"
)

// Config sizes and gates the overlay.
type Config struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// IV is the annualised implied volatility used when bars carry none.
	IV float64 `yaml:"iv" json:"iv"`
	// Tenor is the expiry bought; Roll is how long a position is held before
	// it is sold and replaced (Roll ≤ Tenor).
	Tenor core.Duration `yaml:"tenor" json:"tenor"`
	Roll  core.Duration `yaml:"roll" json:"roll"`
	// GammaFraction of |Γ_LP| the options offset (0..1).
	GammaFraction float64 `yaml:"gamma_fraction" json:"gamma_fraction"`
	// StrangleWidth puts the strikes at S·(1±w); 0 buys an at-the-money
	// straddle.
	StrangleWidth float64 `yaml:"strangle_width" json:"strangle_width"`
	// SpreadBps is paid on the premium of every buy and sale.
	SpreadBps float64 `yaml:"spread_bps" json:"spread_bps"`
	// TriggerProb is the breakout probability (the chance price ends the
	// tenor outside the LP range, under IV) needed to buy; RangeOnly also
	// requires the RANGE regime.
	TriggerProb float64 `yaml:"trigger_prob" json:"trigger_prob"`
	RangeOnly   bool    `yaml:"range_only" json:"range_only"`
}

// Validate checks an enabled overlay's settings.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch {
	case c.IV <= 0 || c.IV > 5:
		return fmt.Errorf("options.iv %v: want an annualised vol in (0, 5]", c.IV)
	case c.Tenor <= 0:
		return fmt.Errorf("options.tenor must be > 0")
	case c.Roll <= 0 || c.Roll > c.Tenor:
		return fmt.Errorf("options.roll %v: want 0 < roll <= tenor (%v)", c.Roll.D(), c.Tenor.D())
	case c.GammaFraction < 0 || c.GammaFraction > 1:
		return fmt.Errorf("options.gamma_fraction %v: want 0..1", c.GammaFraction)
	case c.StrangleWidth < 0 || c.StrangleWidth >= 1:
		return fmt.Errorf("options.strangle_width %v: want 0..1", c.StrangleWidth)
	case c.SpreadBps < 0:
		return fmt.Errorf("options.spread_bps %v: must be >= 0", c.SpreadBps)
	case c.TriggerProb < 0 || c.TriggerProb > 1:
		return fmt.Errorf("options.trigger_prob %v: want 0..1", c.TriggerProb)
	}
	return nil
}

func normCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }
func normPDF(x float64) float64 { return math.Exp(-x*x/2) / math.Sqrt(2*math.Pi) }

// BS is a Black–Scholes quote at zero rates: call and put values, and the
// gamma of one option at strike k.
func BS(s, k, t, iv float64) (call, put, gamma float64) {
	if s <= 0 || k <= 0 {
		return 0, 0, 0
	}
	if t <= 0 || iv <= 0 {
		return math.Max(s-k, 0), math.Max(k-s, 0), 0
	}
	v := iv * math.Sqrt(t)
	d1 := (math.Log(s/k) + v*v/2) / v
	d2 := d1 - v
	call = s*normCDF(d1) - k*normCDF(d2)
	put = call - s + k
	gamma = normPDF(d1) / (s * v)
	return call, put, gamma
}

// BreakoutProb is the chance, under a driftless lognormal at iv, that a price
// now at s ends t years later outside [pa, pb].
func BreakoutProb(s, pa, pb, t, iv float64) float64 {
	if s <= 0 || pa <= 0 || pb <= pa || t <= 0 || iv <= 0 {
		return 0
	}
	v := iv * math.Sqrt(t)
	below := normCDF((math.Log(pa/s) + v*v/2) / v)
	above := 1 - normCDF((math.Log(pb/s)+v*v/2)/v)
	return below + above
}

// Strangle is Qty long calls at KCall plus Qty long puts at KPut, expiring at
// Expiry (a straddle when the strikes match).
type Strangle struct {
	Qty    float64   `json:"qty"`
	KCall  float64   `json:"kCall"`
	KPut   float64   `json:"kPut"`
	Bought time.Time `json:"bought"`
	Expiry time.Time `json:"expiry"`
}

func (o Strangle) quote(now time.Time, s, iv float64) (value, gamma float64) {
	t := core.DtYears(o.Expiry.Sub(now))
	c, _, gc := BS(s, o.KCall, t, iv)
	_, p, gp := BS(s, o.KPut, t, iv)
	return o.Qty * (c + p), o.Qty * (gc + gp)
}

// Overlay holds at most one strangle and decides when to buy, roll and let
// go. Cash flows are reported to the caller; Mark is the open position's
// value.
type Overlay struct {
	cfg  Config
	pos  *Strangle
	open Strangle
	// Cumulative flows for the ledger.
	Premium  float64 `json:"premium"`  // paid for options bought
	Proceeds float64 `json:"proceeds"` // received on sales and expiries
	Costs    float64 `json:"costs"`    // spread paid
	Buys     int     `json:"buys"`
}

// NewOverlay builds an overlay; with cfg.Enabled false it never trades.
func NewOverlay(cfg Config) *Overlay { return &Overlay{cfg: cfg} }

// Step runs the overlay for one bar at price s and returns the cash it moved
// (negative when buying). lpGamma is the LP's gamma now; regime the active
// regime; [pa, pb] the LP range.
func (o *Overlay) Step(now time.Time, s, iv, lpGamma float64, r core.Regime, pa, pb float64) float64 {
	if !o.cfg.Enabled || s <= 0 {
		return 0
	}
	if iv <= 0 {
		iv = o.cfg.IV
	}
	spread := o.cfg.SpreadBps / 1e4
	cash := 0.0
	if o.pos != nil {
		switch {
		case !now.Before(o.pos.Expiry):
			payoff := o.pos.Qty * (math.Max(s-o.pos.KCall, 0) + math.Max(o.pos.KPut-s, 0))
			o.Proceeds += payoff
			cash += payoff
			o.pos = nil
		case now.Sub(o.pos.Bought) >= o.cfg.Roll.D():
			v, _ := o.pos.quote(now, s, iv)
			cost := v * spread
			o.Proceeds += v
			o.Costs += cost
			cash += v - cost
			o.pos = nil
		}
	}
	if o.pos != nil || lpGamma >= 0 || pb <= pa {
		return cash
	}
	if o.cfg.RangeOnly && r != core.Range {
		return cash
	}
	tenor := core.DtYears(o.cfg.Tenor.D())
	if BreakoutProb(s, pa, pb, tenor, iv) < o.cfg.TriggerProb {
		return cash
	}
	unit := Strangle{Qty: 1, KCall: s * (1 + o.cfg.StrangleWidth), KPut: s * (1 - o.cfg.StrangleWidth), Bought: now, Expiry: now.Add(o.cfg.Tenor.D())}
	price, g := unit.quote(now, s, iv)
	if g <= 0 || price <= 0 {
		return cash
	}
	unit.Qty = o.cfg.GammaFraction * -lpGamma / g
	if unit.Qty <= 0 {
		return cash
	}
	premium := unit.Qty * price
	cost := premium * spread
	o.Premium += premium
	o.Costs += cost
	o.Buys++
	o.open = unit
	o.pos = &o.open
	return cash - premium - cost
}

// Mark is the open position's Black–Scholes value (0 when flat).
func (o *Overlay) Mark(now time.Time, s, iv float64) float64 {
	if o.pos == nil {
		return 0
	}
	if iv <= 0 {
		iv = o.cfg.IV
	}
	v, _ := o.pos.quote(now, s, iv)
	return v
}

// Gamma is the open position's gamma (0 when flat).
func (o *Overlay) Gamma(now time.Time, s, iv float64) float64 {
	if o.pos == nil {
		return 0
	}
	if iv <= 0 {
		iv = o.cfg.IV
	}
	_, g := o.pos.quote(now, s, iv)
	return g
}
