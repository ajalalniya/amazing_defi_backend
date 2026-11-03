// Package engine runs strategies bar by bar over a price series and keeps
// the books: a per-bar ledger, the P&L attribution, regime switches and the
// events (re-centres, safety floor, margin actions) that shaped the result.
//
// Timing, so nothing looks ahead:
//
//   - Each bar is first marked to its close: LP value, held ETH, the perp
//     short, funding and pool fees for the bar just ended.
//   - A regime switch the detector confirmed on bar t executes at bar t+1's
//     close (its transition trades and re-range).
//   - Price-triggered actions (re-centring when price leaves the range, step
//     re-hedges) trade at the close of the bar that triggered them, like a
//     resting order.
//   - Detector and switcher then see bar t; nothing they decide affects bar t.
//
// Starting a strategy deploys capital at the first close without swap or gas
// costs (the capital is assumed to arrive in the right mix); the opening
// hedge trade pays perp fees. At the end positions are marked, not closed.
package engine

import (
	"context"
	"fmt"
	"math"
	"time"

	"amazing/internal/sim/config"
	"amazing/internal/sim/core"
	"amazing/internal/sim/fees"
	"amazing/internal/sim/hedge"
	"amazing/internal/sim/metrics"
	"amazing/internal/sim/options"
	"amazing/internal/sim/perp"
	"amazing/internal/sim/pool"
	"amazing/internal/sim/profile"
	"amazing/internal/sim/regime"
	"amazing/internal/sim/switcher"
)

// LedgerRow is one bar of a strategy's books. Flow columns (HedgePnL through
// GasCosts) are cumulative from the start.
type LedgerRow struct {
	Time       time.Time   `json:"time"`
	Price      float64     `json:"price"`
	Regime     core.Regime `json:"regime"`     // active regime (regime strategies)
	Detected   core.Regime `json:"detected"`   // the detector's raw call this bar
	TrueRegime core.Regime `json:"trueRegime"` // synthetic data only
	Pa         float64     `json:"pa"`
	Pb         float64     `json:"pb"`
	X          float64     `json:"x"` // ETH in the LP
	Y          float64     `json:"y"` // USDC in the LP
	LPValue    float64     `json:"lpValue"`
	HeldETH    float64     `json:"heldEth"`  // ETH held outside an LP
	HeldUSDC   float64     `json:"heldUsdc"` // USDC held outside an LP
	LPDelta    float64     `json:"lpDelta"`  // ETH exposure the hedge targets
	Short      float64     `json:"short"`    // perp short, ETH
	Beta       float64     `json:"beta"`     // effective β after the safety floor
	HedgePnL   float64     `json:"hedgePnl"`
	FeesEarned float64     `json:"feesEarned"`
	PerpFees   float64     `json:"perpFees"`
	Funding    float64     `json:"funding"`
	SwapCosts  float64     `json:"swapCosts"`
	GasCosts   float64     `json:"gasCosts"`
	Options    float64     `json:"options"` // open options' value
	Equity     float64     `json:"equity"`
	Floor      uint8       `json:"floor,omitempty"` // switcher.Floor flags in force
}

// Event is something the engine did that a reader of the result should be
// able to find: a re-centre, a safety-floor action, a liquidation.
type Event struct {
	Time   time.Time `json:"time"`
	Bar    int       `json:"bar"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
	Cost   float64   `json:"cost,omitempty"`
}

// BreakEven compares the fee APR a strategy needed with what it earned.
// Required = σ²/(8(1−1/√k)) (the range's gamma decay at the realised
// volatility and its time-weighted width k) + its hedge and re-centre costs
// as an APR on capital.
type BreakEven struct {
	Sigma          float64 `json:"sigma"`
	K              float64 `json:"k"`
	GammaDecayAPR  float64 `json:"gammaDecayApr"`
	CostAPR        float64 `json:"costApr"`
	RequiredFeeAPR float64 `json:"requiredFeeApr"`
	RealizedFeeAPR float64 `json:"realizedFeeApr"`
	Covered        bool    `json:"covered"`
}

// StrategyResult is one strategy's run.
type StrategyResult struct {
	Name string `json:"name"`
	// LooksAhead marks the oracle benchmark: not a tradable result.
	LooksAhead  bool                   `json:"looksAhead,omitempty"`
	Metrics     metrics.Metrics        `json:"metrics"`
	Attribution metrics.Attribution    `json:"attribution"`
	BreakEven   *BreakEven             `json:"breakEven,omitempty"`
	Switches    []switcher.SwitchEvent `json:"switches,omitempty"`
	Events      []Event                `json:"events,omitempty"`
	// Equity is the equity curve, one point per bar (kept with or without
	// the ledger, for charts and metrics).
	Equity []float64   `json:"equity,omitempty"`
	Ledger []LedgerRow `json:"ledger,omitempty"`
}

// Options control what a run keeps.
type Options struct {
	// KeepLedger records a LedgerRow per bar.
	KeepLedger bool
	// KeepEquity returns the equity curve in StrategyResult.Equity.
	KeepEquity bool
	// MaxEvents caps Events (0 = 1000) so a pathological run stays small.
	MaxEvents int
}

// Result is every strategy run over one price series.
type Result struct {
	Bars        int              `json:"bars"`
	From        time.Time        `json:"from"`
	To          time.Time        `json:"to"`
	RealizedVol float64          `json:"realizedVol"`
	Strategies  []StrategyResult `json:"strategies"`
}

// Run runs every configured strategy over bars.
func Run(ctx context.Context, cfg *config.Config, bars []core.Bar, opt Options) (*Result, error) {
	if len(bars) < 2 {
		return nil, fmt.Errorf("run: need at least 2 bars, got %d", len(bars))
	}
	res := &Result{Bars: len(bars), From: bars[0].Time, To: bars[len(bars)-1].Time, RealizedVol: RealizedVol(bars)}
	for _, name := range cfg.StrategyList() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sr, err := RunStrategy(ctx, cfg, name, bars, opt)
		if err != nil {
			return nil, fmt.Errorf("strategy %s: %w", name, err)
		}
		res.Strategies = append(res.Strategies, sr)
	}
	return res, nil
}

// RealizedVol is the annualised volatility of bars' log returns (root mean
// square, not demeaned), using the bars' own spacing.
func RealizedVol(bars []core.Bar) float64 {
	if len(bars) < 2 {
		return 0
	}
	sq, n := 0.0, 0
	for i := 1; i < len(bars); i++ {
		if bars[i-1].Close > 0 && bars[i].Close > 0 {
			r := math.Log(bars[i].Close / bars[i-1].Close)
			sq += r * r
			n++
		}
	}
	years := core.DtYears(bars[len(bars)-1].Time.Sub(bars[0].Time))
	if n == 0 || years <= 0 {
		return 0
	}
	return math.Sqrt(sq / years)
}

// spec is how one named strategy behaves.
type spec struct {
	hold      string              // "eth" or "5050" for the holding baselines
	fixed     profile.RuleProfile // rules for the fixed baselines
	switching bool                // rules come from the regime switcher
	oracle    bool                // the switcher is fed the oracle
	gate      bool                // run a detector only to gate the options overlay
	options   bool
	recenter  bool
	floor     bool // apply the safety floor
}

func specFor(cfg *config.Config, name string) (spec, error) {
	b := cfg.Baseline
	lp := func(mode profile.HedgeMode, beta float64) profile.RuleProfile {
		return profile.RuleProfile{LPMode: profile.Active, Beta: beta, HedgeMode: mode, StepPct: b.StepPct, StepAbs: b.StepAbs, K: b.K, Skew: b.Skew}
	}
	base := spec{recenter: cfg.Recenter.Baselines}
	switch name {
	case config.HoldETH:
		return spec{hold: "eth"}, nil
	case config.Hold5050:
		return spec{hold: "5050"}, nil
	case config.LPOnly:
		base.fixed = lp(profile.NoHedge, 0)
	case config.StaticHedge:
		base.fixed = lp(profile.Static, b.Beta)
	case config.StepHedge:
		base.fixed = lp(profile.Step, b.Beta)
	case config.Continuous:
		base.fixed = lp(profile.Continuous, b.Beta)
	case config.StepOptions:
		base.fixed = lp(profile.Step, b.Beta)
		base.options, base.gate = true, true
	case config.Regime:
		return spec{switching: true, recenter: cfg.Recenter.Enabled, floor: true}, nil
	case config.RegimeOracle:
		return spec{switching: true, oracle: true, recenter: cfg.Recenter.Enabled, floor: true}, nil
	default:
		return spec{}, fmt.Errorf("unknown strategy %q", name)
	}
	return base, nil
}

// NewDetector builds the configured detector, or the oracle over bars.
func NewDetector(cfg *config.Config, bars []core.Bar, oracle bool) regime.Detector {
	dc := cfg.Detector
	if oracle {
		return regime.NewOracle(regime.OracleLabels(bars, cfg.BarsPer(dc.Lookback.D()), dc.Thresholds))
	}
	if dc.Kind == "emaadx" {
		return regime.NewEMAADX(cfg.BarsPer(dc.EMAFast.D()), cfg.BarsPer(dc.EMASlow.D()), cfg.BarsPer(dc.ADXPeriod.D()), dc.ADXTrend, dc.ADXStrong)
	}
	return regime.NewZScore(cfg.BarsPer(dc.Lookback.D()), dc.Thresholds)
}

// run is one strategy's mutable state. Nothing in it is shared, so runs are
// safe to execute concurrently.
type run struct {
	cfg  *config.Config
	sp   spec
	fee  fees.Model
	rate float64 // swap fee + slippage, fraction of USD swapped
	keep bool

	hasLP     bool
	pos       pool.Position
	deployed  profile.RuleProfile // how the capital is deployed now
	bufLo     bool                // the lower edge triggers a re-centre within the buffer
	bufHi     bool
	eth, usdc float64 // held outside an LP
	cash      float64 // fees collected, less gas, plus options flows
	perp      perp.Position
	hs        hedge.State
	rule      hedge.Rule
	ruleProf  profile.RuleProfile
	ov        *options.Overlay
	optCash   float64

	det     regime.Detector
	scorer  regime.Scorer
	sw      *switcher.Switcher
	pending *switcher.SwitchEvent
	exited  bool // drawdown exit: everything in USDC from here on
	floor   switcher.Floor

	att       metrics.Attribution
	swapCost  float64
	gasCost   float64
	peak      float64
	eqs       []float64
	inRange   int
	lpBars    int
	kSum      float64
	lpValSum  float64
	recenters int
	hedges    int
	regimeN   [5]int
	score     *metrics.DetectorScore

	res       StrategyResult
	maxEvents int
}

// RunStrategy runs one named strategy over bars.
func RunStrategy(ctx context.Context, cfg *config.Config, name string, bars []core.Bar, opt Options) (StrategyResult, error) {
	if len(bars) < 2 {
		return StrategyResult{}, fmt.Errorf("need at least 2 bars, got %d", len(bars))
	}
	sp, err := specFor(cfg, name)
	if err != nil {
		return StrategyResult{}, err
	}
	fm, err := fees.For(cfg.Pool.FeeModel, cfg.Pool.BaseFeeAPR, cfg.Pool.FeeTier, cfg.Pool.ActiveLiquidity)
	if err != nil {
		return StrategyResult{}, err
	}
	r := &run{
		cfg: cfg, sp: sp, fee: fm, rate: cfg.Pool.FeeTier + cfg.Recenter.SlippageBps/1e4, keep: opt.KeepLedger,
		res: StrategyResult{Name: name}, maxEvents: opt.MaxEvents, eqs: make([]float64, 0, len(bars)),
	}
	if r.maxEvents <= 0 {
		r.maxEvents = 1000
	}
	if r.keep {
		r.res.Ledger = make([]LedgerRow, 0, len(bars))
	}
	if sp.switching || sp.gate {
		set, err := cfg.ProfileSet()
		if err != nil {
			return StrategyResult{}, err
		}
		r.det = NewDetector(cfg, bars, sp.oracle)
		r.scorer, _ = r.det.(regime.Scorer)
		if la, ok := r.det.(regime.LookAheader); ok && la.LooksAhead() {
			r.res.LooksAhead = true
		}
		r.sw = switcher.New(cfg.Switcher, cfg.Detector.Thresholds, set, cfg.Detector.InitialRegime)
		if sp.switching && allLabelled(bars) {
			r.score = &metrics.DetectorScore{}
		}
	}
	if sp.options {
		oc := cfg.Options
		oc.Enabled = true
		r.ov = options.NewOverlay(oc)
	}
	if err := r.start(bars[0]); err != nil {
		return StrategyResult{}, err
	}
	r.record(bars[0], r.observe(0, bars[0]))
	for i := 1; i < len(bars); i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return StrategyResult{}, err
			}
		}
		if err := r.step(i, bars[i-1], bars[i]); err != nil {
			return StrategyResult{}, fmt.Errorf("bar %d (%s): %w", i, bars[i].Time.Format(time.RFC3339), err)
		}
	}
	r.finish(bars, opt.KeepEquity)
	return r.res, nil
}

func allLabelled(bars []core.Bar) bool {
	for _, b := range bars {
		if !b.TrueRegime.Valid() {
			return false
		}
	}
	return len(bars) > 0
}

// base is the starting equity returns are measured on.
func (r *run) base() float64 { return r.cfg.Capital + r.cfg.ReserveUSD }

// profile is the rule profile in force before the safety floor.
func (r *run) profile() profile.RuleProfile {
	if r.sp.switching {
		return r.sw.Profile()
	}
	return r.sp.fixed
}

func (r *run) start(b core.Bar) error {
	p := b.Close
	r.perp = perp.New(r.cfg.ReserveUSD, p)
	c := r.cfg.Capital
	switch r.sp.hold {
	case "eth":
		r.eth = c / p
	case "5050":
		r.eth, r.usdc = c/2/p, c/2
	default:
		prof := r.profile()
		switch prof.LPMode {
		case profile.ExitToETH:
			r.eth = c / p
		case profile.ExitToUSDC:
			r.usdc = c
		default:
			if err := r.mint(p, c, prof); err != nil {
				return err
			}
		}
		r.deployed = prof
		r.setRule(prof)
		r.hedge(p, prof, false)
	}
	r.peak = r.equity(p, b.Time)
	return nil
}

func (r *run) mint(p, value float64, prof profile.RuleProfile) error {
	pa, pb, err := pool.RangeFor(p, prof.K, prof.EffectiveSkew())
	if err != nil {
		return err
	}
	pos, err := pool.Mint(value, p, pa, pb)
	if err != nil {
		return err
	}
	eb := r.cfg.Recenter.EdgeBuffer
	r.pos, r.hasLP = pos, true
	r.bufLo = p/pa-1 > 2*eb
	r.bufHi = 1-p/pb > 2*eb
	r.hs.Avg = pos.AverageDelta()
	r.hs.Anchor = 0
	return nil
}

func (r *run) setRule(prof profile.RuleProfile) {
	r.rule = hedge.For(prof)
	r.ruleProf = prof
}

// exposure is the ETH the hedge targets: the LP's delta, or ETH held.
func (r *run) exposure(p float64) float64 {
	if r.hasLP {
		return r.pos.Delta(p)
	}
	return r.eth
}

// hedge trades the short to the rule's target and returns the fee. Fees of
// a transition are booked to TransitionCosts, others to PerpFees.
func (r *run) hedge(p float64, prof profile.RuleProfile, transition bool) float64 {
	beta := prof.Beta
	if r.exited {
		beta = 0
	}
	target := r.rule.Target(p, r.exposure(p), beta, &r.hs)
	cost := r.perp.Trade(r.cfg.Perp, target)
	r.hs.Current = r.perp.Short
	if cost > 0 {
		if transition {
			r.att.TransitionCosts += cost
		} else {
			r.att.PerpFees += cost
		}
		r.hedges++
	}
	return cost
}

// reposition moves the LP capital into what prof asks for at price p:
// withdraw (if in an LP), swap to the mix the target needs, then mint or
// hold. Swap fee and slippage come out of the value redeployed; gas is paid
// from cash at GasUSD per transaction (withdraw, swap, mint). It returns the
// total cost, which the caller books.
func (r *run) reposition(p float64, prof profile.RuleProfile) (float64, error) {
	var x0, y0 float64
	tx := 0
	if r.hasLP {
		x0, y0 = r.pos.Amounts(p)
		r.hasLP = false
		tx++
	} else {
		x0, y0 = r.eth, r.usdc
	}
	r.eth, r.usdc = 0, 0
	value := x0*p + y0
	var swapped float64
	switch prof.LPMode {
	case profile.ExitToETH:
		swapped = y0
		r.eth = x0 + (y0-swapped*r.rate)/p
	case profile.ExitToUSDC:
		swapped = x0 * p
		r.usdc = y0 + swapped*(1-r.rate)
	default:
		pa, pb, err := pool.RangeFor(p, prof.K, prof.EffectiveSkew())
		if err != nil {
			return 0, err
		}
		want, err := pool.Mint(value, p, pa, pb)
		if err != nil {
			return 0, err
		}
		x1, _ := want.Amounts(p)
		swapped = math.Abs(x1-x0) * p
		if err := r.mint(p, value-swapped*r.rate, prof); err != nil {
			return 0, err
		}
		tx++ // mint
	}
	swap := swapped * r.rate
	if swapped > 1e-9 {
		tx++
	}
	gas := float64(tx) * r.cfg.Recenter.GasUSD
	r.cash -= gas
	r.swapCost += swap
	r.gasCost += gas
	r.deployed = prof
	return swap + gas, nil
}

// needsRecenter: price left the range, or is inside the edge buffer of an
// edge the range was not built to sit on.
func (r *run) needsRecenter(p float64) bool {
	if !r.hasLP {
		return false
	}
	eb := r.cfg.Recenter.EdgeBuffer
	return p < r.pos.Pa || p > r.pos.Pb ||
		(r.bufLo && p < r.pos.Pa*(1+eb)) || (r.bufHi && p > r.pos.Pb*(1-eb))
}

func (r *run) event(i int, t time.Time, kind, detail string, cost float64) {
	if len(r.res.Events) < r.maxEvents {
		r.res.Events = append(r.res.Events, Event{Time: t, Bar: i, Kind: kind, Detail: detail, Cost: cost})
	}
}

func (r *run) equity(p float64, t time.Time) float64 {
	e := r.eth*p + r.usdc + r.cash + r.perp.Collateral
	if r.hasLP {
		e += r.pos.Value(p)
	}
	if r.ov != nil {
		e += r.ov.Mark(t, p, 0)
	}
	return e
}

func (r *run) step(i int, prev, b core.Bar) error {
	p0, p := prev.Close, b.Close
	dt := core.DtYears(b.Time.Sub(prev.Time))
	dp := p - p0

	// 1. Mark everything held over the bar to its close.
	if r.hasLP {
		d0 := r.pos.Delta(p0)
		dv := r.pos.Value(p) - r.pos.Value(p0)
		r.att.LPDeltaPnL += d0 * dp
		r.att.GammaLoss -= dv - d0*dp
		f := r.fee.Income(b, &r.pos, dt)
		r.cash += f
		r.att.PoolFees += f
	}
	r.att.HoldingPnL += r.eth * dp
	r.att.HedgePnL += r.perp.Mark(p)
	r.att.Funding += r.perp.AccrueFunding(r.cfg.Perp, dt)
	feesBefore := r.perp.Fees
	if r.perp.CheckLiquidation(r.cfg.Perp) {
		r.att.PerpFees += r.perp.Fees - feesBefore
		r.hs.Current = 0
		r.event(i, b.Time, "liquidation", "perp short liquidated at its maintenance margin", r.perp.Fees-feesBefore)
	}
	if r.sp.hold != "" {
		r.record(b, core.NoRegime)
		return nil
	}

	// 2. Execute the switch confirmed on the previous bar.
	if ev := r.pending; ev != nil {
		r.pending = nil
		if err := r.transition(b, ev); err != nil {
			return err
		}
	}

	// 3. Safety floor (regime strategies).
	prof := r.profile()
	if r.sp.floor && !r.exited {
		var err error
		if prof, err = r.applyFloor(i, b, prof); err != nil {
			return err
		}
	}
	if r.exited {
		prof.HedgeMode = profile.NoHedge
	}
	if prof.HedgeMode != r.ruleProf.HedgeMode || prof.StepPct != r.ruleProf.StepPct || prof.StepAbs != r.ruleProf.StepAbs {
		r.setRule(prof)
	}
	r.ruleProf.Beta = prof.Beta

	// 4. Re-centre when price has left the range (or reached the buffer),
	// with the active profile's width and skew.
	if r.sp.recenter && !r.exited && r.needsRecenter(p) {
		cost, err := r.reposition(p, prof)
		if err != nil {
			return err
		}
		r.att.RecenterCosts += cost
		r.recenters++
		r.hs.Anchor = 0
		detail := fmt.Sprintf("price %.2f reached the range edge; ", p)
		if r.hasLP {
			detail += fmt.Sprintf("new range %.2f..%.2f", r.pos.Pa, r.pos.Pb)
		} else {
			detail += "moved to " + string(prof.LPMode)
		}
		r.event(i, b.Time, "recenter", detail, cost)
	}

	// 5. Hedge to the rule, then keep the short's margin safe.
	r.hedge(p, prof, false)
	if r.floor.Has(switcher.Margin) && r.perp.Short > 0 {
		r.protectMargin(i, b)
	}

	// 6. Options overlay.
	if r.ov != nil {
		pa, pb, g := 0.0, 0.0, 0.0
		if r.hasLP {
			pa, pb, g = r.pos.Pa, r.pos.Pb, r.pos.Gamma(p)
		}
		c := r.ov.Step(b.Time, p, b.IV, g, r.sw.Active(), pa, pb)
		r.cash += c
		r.optCash += c
	}

	// 7. Detector and switcher see this bar; a switch executes next bar.
	r.record(b, r.observe(i, b))
	return nil
}

func (r *run) applyFloor(i int, b core.Bar, prof profile.RuleProfile) (profile.RuleProfile, error) {
	p := b.Close
	dd := 0.0
	if r.peak > 0 {
		dd = (r.peak - r.equity(p, b.Time)) / r.peak
	}
	in := switcher.FloorInput{Price: p, HoldsLP: r.hasLP, DrawdownFrac: dd,
		MarginRatio: r.perp.MarginRatio(), MaintenanceMargin: r.cfg.Perp.MaintenanceMargin}
	if r.hasLP {
		in.Pa = r.pos.Pa
	}
	prof, f := r.sw.ApplyFloor(prof, in)
	if f.Has(switcher.Exit) {
		cost, err := r.reposition(p, profile.RuleProfile{LPMode: profile.ExitToUSDC})
		if err != nil {
			return prof, err
		}
		r.att.RecenterCosts += cost
		r.exited = true
		r.event(i, b.Time, "floor_exit", fmt.Sprintf("drawdown %.1f%% above the %.1f%% limit: withdrew everything to USDC", dd*100, r.cfg.Switcher.MaxDD*100), cost)
	}
	if f&^switcher.Margin != r.floor&^switcher.Margin {
		r.event(i, b.Time, "floor", floorText(f), 0)
	}
	r.floor = f
	return prof, nil
}

// observe feeds the detector and switcher; a confirmed switch is queued for
// the next bar. It returns the raw detection.
func (r *run) observe(i int, b core.Bar) core.Regime {
	if r.det == nil {
		return core.NoRegime
	}
	detected := r.det.Update(b)
	z, hasZ := 0.0, false
	if r.scorer != nil {
		z, hasZ = r.scorer.Score(), true
	}
	if _, ev := r.sw.Update(b.Time, i, detected, z, hasZ); ev != nil && r.sp.switching {
		r.pending = ev
	}
	return detected
}

// transition carries out a confirmed switch at this bar's close: re-range
// now (or flag it for the next re-centre when deferred) and move the short
// to the new profile's target in one trade. For a static profile that target
// is β·h_avg; otherwise β·Δ_LP.
func (r *run) transition(b core.Bar, ev *switcher.SwitchEvent) error {
	p := b.Close
	prof := r.sw.Profile()
	cost := 0.0
	ev.Time = b.Time
	switch {
	case r.exited:
		ev.Note = "ignored: drawdown exit in force"
	default:
		if !r.sameDeployment(prof) {
			if r.cfg.Switcher.TransitionRerange == "immediate" || !r.hasLP || !prof.HoldsLP() {
				c, err := r.reposition(p, prof)
				if err != nil {
					return err
				}
				r.att.TransitionCosts += c
				cost += c
				ev.Note = "re-ranged"
			} else {
				ev.Note = "re-range deferred to the next re-centre"
			}
		}
		r.setRule(prof)
		r.hs.Anchor = 0
		cost += r.hedge(p, prof, true)
	}
	ev.TransitionCost = cost
	r.res.Switches = append(r.res.Switches, *ev)
	return nil
}

// sameDeployment reports whether the capital is already deployed the way
// prof wants: same LP mode, and for an LP the same width and skew.
func (r *run) sameDeployment(prof profile.RuleProfile) bool {
	d := r.deployed
	if prof.HoldsLP() != d.HoldsLP() {
		return false
	}
	if !prof.HoldsLP() {
		return prof.LPMode == d.LPMode
	}
	return d.K == prof.K && d.EffectiveSkew() == prof.EffectiveSkew()
}

// protectMargin keeps the short's margin ratio above maintenance plus the
// buffer: first by moving collected cash into collateral, then by cutting the
// short.
func (r *run) protectMargin(i int, b core.Bar) {
	ratio := r.cfg.Perp.MaintenanceMargin + r.cfg.Switcher.MarginBuffer
	need := ratio * r.perp.Notional()
	gap := need - r.perp.Collateral
	if gap <= 0 {
		return
	}
	if r.cash > 0 {
		move := math.Min(r.cash, gap*1.5)
		r.cash -= move
		r.perp.Collateral += move
		r.event(i, b.Time, "margin_topup", fmt.Sprintf("moved %.2f USD of collected cash into perp collateral", move), 0)
		if r.perp.Collateral >= need {
			return
		}
	}
	target := math.Max(0, r.perp.Collateral/(ratio*b.Close)) * 0.9
	cost := r.perp.Trade(r.cfg.Perp, target)
	r.hs.Current = r.perp.Short
	r.att.PerpFees += cost
	r.event(i, b.Time, "margin_cut", fmt.Sprintf("cut the short to %.4f ETH to stay above maintenance margin", target), cost)
}

func floorText(f switcher.Floor) string {
	if f&^switcher.Margin == 0 {
		return "safety floor released"
	}
	s := "safety floor:"
	if f.Has(switcher.BelowRange) {
		s += " price below range (beta >= 1, step hedge)"
	}
	if f.Has(switcher.Drawdown) {
		s += " drawdown limit"
	}
	return s
}

func (r *run) record(b core.Bar, detected core.Regime) {
	p := b.Close
	eq := r.equity(p, b.Time)
	r.peak = math.Max(r.peak, eq)
	r.eqs = append(r.eqs, eq)
	active := core.NoRegime
	if r.sp.switching {
		active = r.sw.Active()
		r.regimeN[active.Index()]++
		if r.score != nil {
			r.score.Add(b.TrueRegime, active)
		}
	}
	if r.hasLP {
		r.lpBars++
		r.kSum += r.pos.K()
		r.lpValSum += r.pos.Value(p)
		if r.pos.InRange(p) {
			r.inRange++
		}
	}
	if !r.keep {
		return
	}
	row := LedgerRow{
		Time: b.Time, Price: p, Regime: active, Detected: detected, TrueRegime: b.TrueRegime,
		HeldETH: r.eth, HeldUSDC: r.usdc, LPDelta: r.exposure(p), Short: r.perp.Short, Beta: r.ruleProf.Beta,
		HedgePnL: r.perp.PnL, FeesEarned: r.att.PoolFees, PerpFees: r.perp.Fees, Funding: r.perp.Funding,
		SwapCosts: r.swapCost, GasCosts: r.gasCost, Equity: eq, Floor: uint8(r.floor),
	}
	if r.exited || r.sp.hold != "" {
		row.Beta = 0
	}
	if r.hasLP {
		row.Pa, row.Pb = r.pos.Pa, r.pos.Pb
		row.X, row.Y = r.pos.Amounts(p)
		row.LPValue = r.pos.Value(p)
	}
	if r.ov != nil {
		row.Options = r.ov.Mark(b.Time, p, b.IV)
	}
	r.res.Ledger = append(r.res.Ledger, row)
}

func (r *run) finish(bars []core.Bar, keepEquity bool) {
	last := bars[len(bars)-1]
	n := len(bars)
	d := last.Time.Sub(bars[0].Time) / time.Duration(n-1)
	if d <= 0 {
		d = r.cfg.BarSize.D()
	}
	m := &r.res.Metrics
	metrics.Returns(m, r.eqs, r.base(), d)
	m.Recenters = r.recenters
	m.HedgeTrades = r.hedges
	m.Switches = len(r.res.Switches)
	if r.lpBars > 0 {
		m.TimeInRange = float64(r.inRange) / float64(r.lpBars)
	}
	years := core.DtYears(last.Time.Sub(bars[0].Time))
	avgLP := 0.0
	if r.lpBars > 0 {
		avgLP = r.lpValSum / float64(r.lpBars)
	}
	if avgLP > 0 && years > 0 {
		m.RealizedFeeAPR = r.att.PoolFees / avgLP / years
	}
	if r.sp.switching {
		m.TimeInRegime = map[string]float64{}
		for i, c := range r.regimeN {
			if c > 0 {
				m.TimeInRegime[core.Regimes[i].String()] = float64(c) / float64(n)
			}
		}
	}
	if r.score != nil {
		r.score.Finish()
		m.Detector = r.score
	}

	if r.ov != nil {
		r.att.OptionsPnL = r.optCash + r.ov.Mark(last.Time, last.Close, last.IV)
	}
	r.att.NetPnL = m.NetPnL
	r.res.Attribution = r.att

	if r.sp.hold == "" && r.lpBars > 0 && years > 0 {
		sigma := RealizedVol(bars)
		k := r.kSum / float64(r.lpBars)
		be := &BreakEven{Sigma: sigma, K: k, GammaDecayAPR: pool.GammaDecayAPR(sigma, k), RealizedFeeAPR: m.RealizedFeeAPR}
		be.CostAPR = (r.att.PerpFees + r.att.RecenterCosts + r.att.TransitionCosts) / r.cfg.Capital / years
		be.RequiredFeeAPR = be.GammaDecayAPR + be.CostAPR
		be.Covered = be.RealizedFeeAPR >= be.RequiredFeeAPR
		r.res.BreakEven = be
	}
	if keepEquity {
		r.res.Equity = r.eqs
	}
}
