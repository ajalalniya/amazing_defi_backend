# Regime-switching hedged Uniswap v3 LP simulator

`internal/sim` backtests a concentrated-liquidity **ETH/USDC Uniswap v3 LP** hedged with a **GMX-style perp short**, whose rules switch automatically between five market regimes. It compares that strategy with fixed-rule baselines on historical bars and on Monte Carlo paths.

It is plain Go (standard library plus `gopkg.in/yaml.v3`), independent of the app's daily backtest in `internal/backtest`, and reachable three ways: the library (`sim.Run`, `sim.MonteCarlo`), the CLI (`cmd/sim`) and HTTP (`POST /api/v1/sim/simulate`, `/api/v1/sim/montecarlo`).

---

## 1. Concepts

### Position maths (`pool`)

Price `p` is USDC per ETH; a position has liquidity `L` over `[pa, pb]`.

| | ETH `x` | USDC `y` | value `V` |
|---|---|---|---|
| `pa ≤ p ≤ pb` | `L(1/√p − 1/√pb)` | `L(√p − √pa)` | `L(2√p − p/√pb − √pa)` |
| `p < pa` | `L(1/√pa − 1/√pb)` | 0 | `x·p` |
| `p > pb` | 0 | `L(√pb − √pa)` | `y` |

Minting `C` dollars at `p` solves `L = C / (2√p − p/√pb − √pa)`. A symmetric range is `p/k .. p·k`; **skew** shifts it in log space: `+1` is `p .. p·k²` (all ETH, keeps upside), `−1` is `p/k² .. p` (all USDC).

### Delta and gamma

- **Delta** `Δ = dV/dp = x`, the ETH held. A short of `h` ETH has delta `−h`; net delta is `x − h`.
- **Gamma** `Γ = −L / (2p^{3/2})` in range, always negative: the pool sells ETH as price rises and buys it as price falls. Against a static holding that is *sell low, buy high*, a loss in either direction of about `½|Γ|Δp²` per move `Δp` since the last hedge.
- Expected decay for a symmetric range is `σ² / (8(1 − 1/√k))` a year: about **97 %/yr (0.27 %/day) at σ = 60 %, k = 1.1**.
- A perp has **zero gamma**. It cancels delta, never gamma. Pool fees are what pays for carrying negative gamma, so **the strategy only works when fee income > gamma decay + hedging costs**. The break-even report checks exactly this.

### Hedging rules (`hedge`)

The short is resized to `β × Δ_LP` (`β` is the hedge ratio). The rules change *how* the gamma loss is paid, not whether:

| rule | target | loss depends on |
|---|---|---|
| none | 0 | direction (full price bet) |
| static | `β · h_avg`, `h_avg = (V(pb) − V(pa))/(pb − pa)` | where price ends |
| step | re-set to `β·Δ_LP` after a `step_pct` (or `step_abs`) move | how much price wiggles |
| continuous | `β·Δ_LP` every bar | wiggle (smallest) + most trading fees |

Bull trends cap a narrow LP's upside (above `pb` it is all USDC); bear trends make a static short too small just as the pool buys ETH (delta 1.67 → 3.42 ETH at `pa` in the reference range). The profiles below are built around those two problems.

### Regimes, profiles and switching

Five regimes: `RANGE`, `TEND_BULLISH`, `BULLISH`, `TEND_BEARISH`, `BEARISH`. Each maps to a **RuleProfile** (`profile`): LP mode, β, hedge rule, step, range width `k` and skew. Defaults:

| regime | lp_mode | β | hedge | step | k | skew |
|---|---|---|---|---|---|---|
| RANGE | active | 0.3 | static | – | 1.10 | 0 |
| TEND_BULLISH | active | 0.5 | step | 4 % | 1.15 | 0 |
| BULLISH | exit_to_eth | 0 | none | – | 1.15 | +1 |
| TEND_BEARISH | active | 1.0 | step | 2.5 % | 1.10 | 0 |
| BEARISH | active | 1.0 | step | 1.5 % | 1.15 | 0 |

`lp_mode` is `active`, `skewed_up` (active with skew +1), `exit_to_eth` or `exit_to_usdc`.

**Detection** (`regime`) is pluggable. The default momentum z-score is `z = ln(P_t/P_{t−N}) / √(Σ r²)` over a 3-day lookback, with thresholds ±0.5 (tendency) and ±1.5 (trend). The alternative is an EMA-crossover + ADX detector. The **oracle** reads the future (true labels on synthetic data, forward z-scores otherwise). It exists only as an upper bound and is flagged `looksAhead` everywhere.

**Switching** (`switcher`) stops whipsaw:
- **Confirmation:** the same new regime must be seen on `confirm_bars` bars in a row.
- **Hysteresis:** the score must leave the active regime's band widened by `hysteresis`.
- **Dwell:** at least `min_dwell` must pass between switches.

A confirmed switch executes on the **next** bar:
- the short moves to the new target in one trade;
- the range is rebuilt `immediate`ly, or at the next natural re-centre if `deferred`;
- the switch is logged with its z-score and transition cost.

The **safety floor** overrides any regime:
- **Below the range:** forces β ≥ 1 with a tight step hedge.
- **Drawdown over `max_dd`:** forces β = 1, or exits to USDC.
- **Margin near maintenance:** tops up collateral from collected fees, else cuts the short.

**Re-centring:** when price leaves the range (or comes within `edge_buffer` of an edge the range wasn't built to sit on), the LP withdraws, swaps to the new mix (pool fee + slippage), re-mints at the active profile's `k` and skew, and pays gas for 3 transactions.

### Costs and income

- **Pool fees** (`fees`) are paid out to the side, never compounded:
  - `apr` mode: `V · base_fee_apr · 1/(1−1/√k) · dt` while in range;
  - `volume` mode: `bar volume · fee_tier · L_user/L_active`, using the bars' liquidity or a constant.
- **Perp** (`perp`): fee per side plus price impact, and funding (positive `funding_apr` means longs pay the short). Liquidation happens at `maintenance_margin` of notional.
- **Re-centring:** swap fee + slippage on the USD swapped, plus gas per transaction.
- **Options** (`options`, optional): long straddles/strangles priced with Black–Scholes, sized to offset `gamma_fraction` of `|Γ_LP|`. They're bought only when the chance of leaving the range within the tenor reaches `trigger_prob`, and by default only in `RANGE`. Rolled every `roll`, paying `spread_bps`.

### Timing (no look-ahead)

Each bar is first marked to its close. A regime switch confirmed on bar `t` executes at bar `t+1`'s close. Price-triggered actions (re-centres, step re-hedges) trade at the close that triggered them, like a resting order. The detector only then sees bar `t`. `TestNoLookAhead` shuffles every bar after `T` and checks nothing up to `T` changes. As a control, it checks that the same test catches the oracle.

Capital is deployed at the first close without swap or gas costs; the opening hedge pays perp fees. Positions are **marked, not closed**, at the end.

---

## 2. Outputs

`engine.StrategyResult` per strategy:

- **`Metrics`:** net P&L, return, APR, annualised volatility, Sharpe, max drawdown, worst-5 % weekly P&L, re-centres, hedge trades, switches, time in range and time per regime, and realised fee APR. On synthetic data it also includes the detector's confusion matrix and accuracy.
- **`Attribution`:** parts that sum exactly to net P&L: `pool fees + LP delta P&L − gamma loss + hedge P&L − perp fees + funding − re-centre costs − transition costs + options P&L + holding P&L`. *Gamma loss* is the LP's convexity paid bar by bar, the part a perp cannot hedge.
- **`BreakEven`:** required fee APR `σ²/(8(1−1/√k)) + cost APR` against realised fee APR.
- **Logs:** `Switches` and `Events` (re-centres, floor, margin, liquidation).
- **`Ledger`:** a per-bar row with price, regimes, range, `x`, `y`, LP value, delta, short, β, the cumulative cost columns and equity.

`MonteCarlo` gives mean, std, percentiles, CVaR 5 % and a histogram of P&L per strategy, the mean attribution, the pooled detector score and optionally every path's P&L.

Charts are not drawn in Go. The CLI writes the series a chart needs:
- **`ledger_<strategy>.csv`:** price with range bands and regime shading, short versus LP delta.
- **`equity.csv`:** equity by strategy.
- **`result.json`:** attribution.
- **`montecarlo.json`:** distributions.

---

## 3. Running it

```sh
cd backend
go run ./cmd/sim bars --swaps data/swaps_<pool>.csv --out data/eth_5m.csv     # 5-min OHLCV from on-chain swaps
go run ./cmd/sim run --config configs/historical.yaml --data data/eth_5m.csv --out results/historical
go run ./cmd/sim montecarlo --config configs/default.yaml --paths 5000 --seed 1 --out results/montecarlo_gbm
go run ./cmd/sim montecarlo --config configs/default.yaml --regime-switching --out results/montecarlo_regimes
```

`configs/default.yaml` lists every key with its default (a test keeps it equal to `config.Defaults()`). A config file only needs the keys it changes; keys are snake_case in YAML and JSON alike, and unknown keys are errors.

HTTP (thin wrappers over the library; config is the same snake_case JSON):

```sh
curl -X POST localhost:8080/api/v1/sim/simulate -d '{"config":{"strategies":["step","regime"]},"pool":{"poolId":"v3-eth-usdc-005","from":"2026-02-01","to":"2026-03-01"}}'
curl -X POST localhost:8080/api/v1/sim/simulate -d '{"synthetic":{"path":0},"ledger":true}'
curl -X POST localhost:8080/api/v1/sim/montecarlo -d '{"config":{"monte_carlo":{"paths":2000}},"samples":true}'
```

Monte Carlo runs on `runtime.NumCPU()` workers. Path `i` draws from its own PCG source seeded with `(seed, i)` and results are stored by path index, so output is identical for any worker count (`TestMonteCarloDeterministicAcrossWorkers`). Long runs stop on `context` cancellation.

---

## 4. Extending

**A new detector:** implement `regime.Detector` (`Update(core.Bar) core.Regime`, `Reset()`).
- Optionally implement `regime.Scorer` (`Score() float64`) if a continuous score sits behind its labels; the switcher then applies hysteresis to it.
- Implement `regime.LookAheader` returning true if it reads the future.
- Add a `detector.kind` case in `config.Validate` and `engine.NewDetector`.

**A new hedge rule:** implement `hedge.Rule`:

```go
type Rule interface {
    // Target returns the desired short size (ETH) for this bar;
    // returning st.Current means "no trade".
    Target(p, lpDelta, beta float64, st *hedge.State) float64
}
```

Then add a `profile.HedgeMode` constant, accept it in `RuleProfile.Validate`, and map it in `hedge.For`. `State.Anchor = 0` means "re-hedge now" (set after mints, re-centres and transitions).

**A new profile or regime rule:** profiles are pure config. Change `profiles:` in YAML; give every field, since a profile in a file replaces the default for that regime. A new `lp_mode` needs a case in `engine.reposition` (how to move capital into it) and `profile.RuleProfile.HoldsLP`.

**A new fee model:** implement `fees.Model` (`Income(bar, *pool.Position, dt) float64`) and add it to `fees.For`.

---

## 5. Validation (`go test ./internal/sim/...`)

**Reference setup:** `C = 10,000`, `P0 = 3,000`, `k = 1.1`, 0.05 % perp fee, no pool fees, funding or gas, $1 price steps. It reproduces `L = 1961.6`, `pa = 2727.27`, `pb = 3300`, start delta 1.667, `h_avg = 1.626` and 3.415 ETH at `pa`. Net P&L against the brief's table (`testdata/validation.json`):

| strategy | →3300 | →3450 | →2727 | →2550 | chop ×5 |
|---|---|---|---|---|---|
| LP only | +244.04 (+244) | +244.04 (+244) | −688.16 (−688) | −1292.56 (−1293) | 0.00 (0) |
| static | −246.21 (−246) | −490.11 (−490) | −246.69 (−247) | −563.28 (−563) | −2.44 (−2) |
| step $75 | −68.03 (−68) | −68.03 (−68) | −65.36 (−65) | −74.56 (−75) | −339.43 (−339) |
| continuous | −5.96 (−6) | −5.96 (−6) | −5.87 (−6) | −5.87 (−6) | −20.08 (−20) |

The table's numbers come out only under these conventions: the opening hedge trade pays its fee, and positions are marked rather than closed at the end. The "−2" for static chop is that opening fee ($2.44), so values near zero are checked to within $1.

**Other checks:**
- **Monte Carlo** (σ 60 %, 7 days of 5-min bars, 5,000 paths, seed 1): LP-only −165.74, static −168.17, step-$75 −173.85 (std 51.42), continuous −238.84 (its extra $74 is fees).
- **Gamma loss:** with no fees, a step leg's loss equals the attribution's gamma loss exactly, and matches `½|Γ|Δp²` within 1 %.
- **Switcher:** no switch before `confirm_bars` or inside `min_dwell`; hysteresis holds RANGE through a z oscillating ±0.15 around 0.5; the floor forces β ≥ 1 below `pa`.
- **No look-ahead and determinism:** see above.

`go test -race` needs cgo and a C compiler. The determinism and concurrency tests are written for it, but it was not run on the Windows machine this was built on (no gcc).

---

## 6. Example report (Base ETH/USDC 0.05 %, 2026-01-18 → 2026-04-15)

**Historical:** 24,968 five-minute bars from the swap CSV, `fee_model: volume`, realised vol 74.5 % (`results/historical`). Baselines keep their first range (`recenter.baselines: false`), so once ETH fell out of it they earned almost nothing.

| strategy | net P&L | max DD | fees | gamma loss | hedge P&L | re-centres | switches |
|---|---|---|---|---|---|---|---|
| hold ETH | −3,018.70 | 32.2 % | | | | | |
| hold 50/50 | −1,509.35 | 16.2 % | | | | | |
| LP only | −2,808.24 | 30.9 % | 40 | −38 | | 0 | |
| static | −1,338.12 | 15.2 % | 40 | −38 | +1,473 | 0 | |
| step 2.5 % | −38.75 | 0.3 % | 40 | −38 | +2,775 | 0 | |
| continuous | −15.15 | 0.1 % | 40 | −38 | +2,810 | 0 | |
| **regime** | **−1,850.32** | 12.8 % | 2,057 | −1,963 | +942 | 10 | 92 |
| regime oracle (look-ahead) | +838.67 | 3.7 % | 2,245 | −2,154 | +2,530 | 7 | 114 |
| step + options | +87.18 | 0.6 % | 40 | −38 | +2,775 | 0 | |

The regime strategy's fees ($2,057) roughly paid its gamma loss ($1,963): realised fee APR 137 % against 143 % needed. It lost on **direction**. In a falling market the RANGE profile hedges only 30 % of delta, so −$2,042 of LP delta P&L was only partly offset (+$942). Being in ETH during short BULLISH spells cost another $535. The oracle shows what correct regimes would have been worth: +$839.

**Monte Carlo** (5,000 × 7-day paths, default `base_fee_apr` 8 % → about 172 % in a ±10 % range):

| strategy | GBM mean ± std | regime-switching GBM mean ± std |
|---|---|---|
| LP only | +126 ± 473 | +149 ± 449 |
| static | +124 ± 250 | +152 ± 227 |
| step 2.5 % | +118 ± 58 | +145 ± 72 |
| continuous | +53 ± 14 | +86 ± 55 |
| regime | +114 ± 308 | +134 ± 286 (detector accuracy 45 %) |
| regime oracle | +398 ± 366 | +180 ± 343 (accuracy 97 %) |

With fees above gamma decay, every LP strategy profits on average; step hedging keeps the mean while cutting the spread by ~88 % against LP-only. On regime-switching paths the 3-day z-score is right 45 % of the time against regimes that last 1–3 days, which is why the regime strategy trails a plain step hedge there. A faster detector is the obvious next lever.

### Known simplifications

- **Funding** is a constant rate.
- **Ticks:** ranges are not snapped to ticks.
- **Fee timing:** fees are judged at each bar's close.
- **Options IV:** options use a flat implied vol unless bars carry an `iv` column.
- **Precision:** money is `float64` USD. On-chain execution would need `big.Int` / Q64.96 maths.
