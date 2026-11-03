package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"amazing/internal/automation"
	"amazing/internal/backtest"
	"amazing/internal/history"
	"amazing/internal/market"
	"amazing/internal/policy"
	"amazing/internal/portfolio"
	"amazing/internal/signer"
	simapi "amazing/internal/sim/api"
)

type Server struct {
	log      *slog.Logger
	provider market.Provider
	store    portfolio.Store
	engine   *portfolio.Engine
	bt       *backtest.Engine
	hist     *history.Store
	hub      *Hub
	jobs     automation.Store
	guard    *policy.Guard
	signer   signer.Signer
	limiter  *ipLimiter
	ready    atomic.Bool
	metrics  metrics
	chain    bool
}

type metrics struct {
	httpTotal     atomic.Int64
	wsClients     atomic.Int64
	backtests     atomic.Int64
	policyRejects atomic.Int64
}

func New(log *slog.Logger, provider market.Provider, store portfolio.Store, chain bool) *Server {
	s := &Server{
		log:      log,
		provider: provider,
		store:    store,
		engine:   portfolio.NewEngine(log),
		bt:       backtest.New(log),
		jobs:     automation.NewMemory(),
		guard:    policy.NewGuard(policy.Limits{}),
		signer:   signer.Disabled{},
		limiter:  newIPLimiter(time.Minute, 8),
		chain:    chain,
	}
	s.hub = NewHub(log, s)
	s.ready.Store(true)
	return s
}

func (s *Server) WithHistory(h *history.Store) *Server {
	if h != nil {
		s.hist = h
		s.bt.SetFeed(h)
	}
	return s
}

func (s *Server) Handler(origins []string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /metrics", s.metricsHandler)
	mux.HandleFunc("GET /api/v1/status", s.status)

	mux.HandleFunc("GET /api/v1/pools", s.listPools)
	mux.HandleFunc("GET /api/v1/pools/{id}", s.getPool)
	mux.HandleFunc("GET /api/v1/pools/{id}/depth", s.getDepth)
	mux.HandleFunc("GET /api/v1/pools/{id}/candles", s.getCandles)
	mux.HandleFunc("GET /api/v1/perps", s.listPerps)
	mux.HandleFunc("GET /api/v1/perps/{id}", s.getPerp)
	mux.HandleFunc("GET /api/v1/perps/{id}/candles", s.getPerpCandles)
	mux.HandleFunc("POST /api/v1/quotes/lp", s.quoteLP)
	mux.HandleFunc("POST /api/v1/quotes/perp", s.quotePerp)
	mux.HandleFunc("GET /api/v1/portfolio", s.getPortfolio)
	mux.HandleFunc("POST /api/v1/positions", s.openPosition)
	mux.HandleFunc("DELETE /api/v1/positions/{id}", s.closePosition)
	mux.HandleFunc("POST /api/v1/backtest", s.runBacktest)
	mux.HandleFunc("GET /api/v1/history/coverage", s.historyCoverage)
	mux.HandleFunc("GET /api/v1/history/price", s.historyPrice)
	s.simHandler().Register(mux, "/api/v1/sim")
	mux.HandleFunc("GET /api/v1/timeframes", s.timeframes)
	mux.HandleFunc("GET /api/v1/ws", s.ws)

	mux.HandleFunc("GET /api/v1/automation/strategies", s.autoStrategies)
	mux.HandleFunc("GET /api/v1/automation/jobs", s.autoJobs)
	mux.HandleFunc("POST /api/v1/automation/jobs", s.autoCreateJob)
	mux.HandleFunc("PATCH /api/v1/automation/jobs/{id}", s.autoPatchJob)
	mux.HandleFunc("DELETE /api/v1/automation/jobs/{id}", s.autoDeleteJob)
	mux.HandleFunc("GET /api/v1/automation/jobs/{id}/runs", s.autoRuns)
	mux.HandleFunc("GET /api/v1/automation/grant", s.autoGrant)
	mux.HandleFunc("GET /api/v1/operator/status", s.operatorStatus)

	h := cors(origins)(withRequestID(mux, s.log))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.metrics.httpTotal.Add(1)
		h.ServeHTTP(w, r)
	})
}

func (s *Server) Hub() *Hub { return s.hub }

func (s *Server) providerMode() string {
	if s.chain {
		return "chain"
	}
	return "mock"
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok", "provider": s.providerMode()})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok", "provider": s.providerMode()})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeError(w, 503, "not_ready", "shutting down")
		return
	}
	if _, err := s.provider.Pools(r.Context()); err != nil && !errors.Is(err, market.ErrNotImplemented) {
		writeError(w, 503, "not_ready", err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	kill, _ := s.guard.Tripped()
	ks := 0
	if kill {
		ks = 1
	}
	_, _ = w.Write([]byte(
		"amazing_http_requests_total " + strconv.FormatInt(s.metrics.httpTotal.Load(), 10) + "\n" +
			"amazing_ws_clients " + strconv.FormatInt(s.metrics.wsClients.Load(), 10) + "\n" +
			"amazing_backtests_total " + strconv.FormatInt(s.metrics.backtests.Load(), 10) + "\n" +
			"amazing_policy_rejections_total " + strconv.FormatInt(s.metrics.policyRejects.Load(), 10) + "\n" +
			"amazing_kill_switch " + strconv.Itoa(ks) + "\n",
	))
}

func (s *Server) listPools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.provider.Pools(r.Context())
	if err != nil {
		s.providerErr(w, err)
		return
	}
	ver := r.URL.Query().Get("version")
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := make([]market.Pool, 0, len(pools))
	for _, p := range pools {
		if ver != "" && ver != "all" && p.Version != ver {
			continue
		}
		if q != "" {
			blob := strings.ToLower(p.Token0 + "/" + p.Token1 + " " + p.Token0 + " " + p.Token1 + " " + p.ID + " " + p.Network)
			if !strings.Contains(blob, q) {
				continue
			}
		}
		out = append(out, p)
	}
	writeJSON(w, 200, map[string]any{"pools": out})
}

func (s *Server) getPool(w http.ResponseWriter, r *http.Request) {
	p, err := s.findPool(r.Context(), r.PathValue("id"))
	if err != nil {
		s.providerErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) getDepth(w http.ResponseWriter, r *http.Request) {
	bins, _ := strconv.Atoi(r.URL.Query().Get("bins"))
	if bins <= 0 {
		bins = 48
	}
	if bins > 256 {
		bins = 256
	}
	lo, _ := strconv.ParseFloat(r.URL.Query().Get("lo"), 64)
	hi, _ := strconv.ParseFloat(r.URL.Query().Get("hi"), 64)
	d, err := s.provider.Depth(r.Context(), r.PathValue("id"), bins, lo, hi)
	if err != nil {
		s.providerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"bins": d})
}

func (s *Server) getCandles(w http.ResponseWriter, r *http.Request) {
	s.candles(w, r, r.PathValue("id"))
}

func (s *Server) getPerpCandles(w http.ResponseWriter, r *http.Request) {
	s.candles(w, r, r.PathValue("id"))
}

func (s *Server) candles(w http.ResponseWriter, r *http.Request, id string) {
	tfID := r.URL.Query().Get("tf")
	if tfID == "" {
		tfID = "15m"
	}
	tf, ok := market.TimeframeByID(tfID)
	if !ok {
		writeError(w, 400, "invalid_timeframe", "unknown timeframe "+tfID)
		return
	}
	cs, err := s.provider.Candles(r.Context(), id, tf)
	if err != nil {
		s.providerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"timeframe": tf, "candles": cs})
}

func (s *Server) listPerps(w http.ResponseWriter, r *http.Request) {
	list, err := s.provider.Perps(r.Context())
	if err != nil {
		s.providerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"perps": list})
}

func (s *Server) getPerp(w http.ResponseWriter, r *http.Request) {
	p, err := s.findPerp(r.Context(), r.PathValue("id"))
	if err != nil {
		s.providerErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) quoteLP(w http.ResponseWriter, r *http.Request) {
	var req lpQuoteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_json", err.Error())
		return
	}
	if req.Lower <= 0 || req.Upper <= req.Lower {
		writeError(w, 400, "invalid_range", "lower must be > 0 and upper > lower")
		return
	}
	pool, err := s.findPool(r.Context(), req.PoolID)
	if err != nil {
		s.providerErr(w, err)
		return
	}
	perps, err := s.provider.Perps(r.Context())
	if err != nil {
		s.providerErr(w, err)
		return
	}
	eth, btc := market.MarksFromPerps(perps)
	q := quoteLP(pool, perps, req, func(tok string) float64 {
		return market.QuoteUSD(tok, eth, btc, s.log)
	})
	writeJSON(w, 200, q)
}

func (s *Server) quotePerp(w http.ResponseWriter, r *http.Request) {
	var req perpQuoteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_json", err.Error())
		return
	}
	if req.Collateral <= 0 {
		writeError(w, 400, "invalid_capital", "collateral must be > 0")
		return
	}
	m, err := s.findPerp(r.Context(), req.PerpID)
	if err != nil {
		s.providerErr(w, err)
		return
	}
	if req.Leverage > m.MaxLev {
		writeError(w, 400, "invalid_leverage", "leverage exceeds market max")
		return
	}
	writeJSON(w, 200, quotePerp(m, req))
}

func (s *Server) getPortfolio(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("owner")
	snap, err := s.snapshot(r.Context(), owner)
	if err != nil {
		writeError(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, snap)
}

func (s *Server) snapshot(ctx context.Context, owner string) (portfolio.Snapshot, error) {
	pos, err := s.store.List(ctx, owner)
	if err != nil {
		return portfolio.Snapshot{}, err
	}
	return s.engine.Snapshot(ctx, pos, s.provider)
}

func (s *Server) openPosition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Owner      string  `json:"owner"`
		Platform   string  `json:"platform"`
		Kind       string  `json:"kind"`
		PoolID     string  `json:"poolId"`
		PerpID     string  `json:"perpId"`
		Capital    float64 `json:"capital"`
		Lower      float64 `json:"lower"`
		Upper      float64 `json:"upper"`
		Collateral float64 `json:"collateral"`
		Leverage   float64 `json:"leverage"`
		TxHash     string  `json:"txHash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_json", err.Error())
		return
	}
	kind := strings.ToLower(req.Kind)
	if req.Owner == "" {
		writeError(w, 400, "invalid_owner", "owner required")
		return
	}
	pos := market.Position{Owner: req.Owner, Platform: req.Platform, Kind: kind, TxHash: req.TxHash, OpenedAt: time.Now().UTC()}

	switch kind {
	case "lp":
		if req.Capital <= 0 {
			writeError(w, 400, "invalid_capital", "capital must be > 0")
			return
		}
		if req.Lower <= 0 || req.Upper <= req.Lower {
			writeError(w, 400, "invalid_range", "lower must be > 0 and upper > lower")
			return
		}
		pool, err := s.findPool(r.Context(), req.PoolID)
		if err != nil {
			s.providerErr(w, err)
			return
		}
		if req.Platform == "" {
			pos.Platform = pool.Version
		}
		pos.PoolID = pool.ID
		pos.Market = pool.Token0 + "/" + pool.Token1
		pos.Capital = req.Capital
		pos.Lower = req.Lower
		pos.Upper = req.Upper
		pos.Entry = pool.Price
		pos.Fees = 0
	case "long", "short":
		if req.Collateral <= 0 {
			writeError(w, 400, "invalid_capital", "collateral must be > 0")
			return
		}
		m, err := s.findPerp(r.Context(), req.PerpID)
		if err != nil {
			s.providerErr(w, err)
			return
		}
		if req.Leverage > m.MaxLev || req.Leverage < 1 {
			writeError(w, 400, "invalid_leverage", "leverage out of range")
			return
		}
		pos.Platform = "gmx"
		pos.PerpID = m.ID
		pos.Market = m.ID
		pos.Collateral = req.Collateral
		pos.Leverage = req.Leverage
		pos.Entry = m.Price
		pos.Funding = 0
	default:
		writeError(w, 400, "invalid_kind", "kind must be lp, long, or short")
		return
	}

	if s.chain && req.TxHash == "" {
		writeError(w, 400, "tx_required", "txHash required in chain mode")
		return
	}

	saved, err := s.store.Insert(r.Context(), pos)
	if err != nil {
		writeError(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 201, saved)
}

func (s *Server) closePosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	owner := r.URL.Query().Get("owner")
	pos, err := s.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, portfolio.ErrNotFound) {
			writeError(w, 404, "not_found", "position not found")
			return
		}
		writeError(w, 500, "internal", err.Error())
		return
	}
	snap, err := s.engine.Snapshot(r.Context(), []market.Position{pos}, s.provider)
	if err != nil {
		writeError(w, 500, "internal", err.Error())
		return
	}
	realised := 0.0
	if len(snap.Positions) > 0 {
		realised = snap.Positions[0].PnL
	}
	if _, err := s.store.Delete(r.Context(), id, owner); err != nil {
		if errors.Is(err, portfolio.ErrNotFound) {
			writeError(w, 404, "not_found", "position not found")
			return
		}
		if errors.Is(err, portfolio.ErrForbidden) {
			writeError(w, 403, "forbidden", "not owner")
			return
		}
		writeError(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "realisedPnl": realised})
}

func (s *Server) runBacktest(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientIP(r)) {
		writeError(w, 429, "rate_limited", "too many backtests")
		return
	}
	var req backtest.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_json", err.Error())
		return
	}
	if req.From != "" || req.To != "" || strings.EqualFold(req.Source, "onchain") {
		if req.From == "" || req.To == "" {
			writeError(w, 400, "invalid_window", "from and to dates are both required")
			return
		}
	} else if req.Days < 1 || req.Days > 1825 {
		writeError(w, 400, "invalid_days", "days must be 1..1825")
		return
	}
	if len(req.Legs) == 0 {
		writeError(w, 400, "invalid_legs", "at least one leg required")
		return
	}
	if req.From == "" && len(req.Legs)*req.Days > 20_000 {
		writeError(w, 400, "too_expensive", "days*legs exceeds cap")
		return
	}
	for _, l := range req.Legs {
		k := strings.ToLower(l.Kind)
		if k != "lp" && k != "long" && k != "short" {
			writeError(w, 400, "invalid_kind", "kind must be lp, long, or short")
			return
		}
		if l.Capital <= 0 {
			writeError(w, 400, "invalid_capital", "capital must be > 0")
			return
		}
		if k == "lp" && (l.Lower <= 0 || l.Upper <= l.Lower) {
			writeError(w, 400, "invalid_range", "lower must be > 0 and upper > lower")
			return
		}
	}
	pools, err := s.provider.Pools(r.Context())
	if err != nil {
		s.providerErr(w, err)
		return
	}
	perps, err := s.provider.Perps(r.Context())
	if err != nil {
		s.providerErr(w, err)
		return
	}
	deadline := 8 * time.Second
	if req.From != "" || strings.EqualFold(req.Source, "onchain") {
		deadline = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()
	var sink *ndjsonSink
	if backtestWantsStream(r) {
		sink = &ndjsonSink{w: w}
		ctx = history.WithProgress(ctx, sink.progress)
	}
	s.metrics.backtests.Add(1)
	res, err := s.bt.Run(ctx, req, pools, perps)
	if err != nil {
		status, code, msg := classifyBacktestErr(err)
		if sink != nil && sink.fail(code, msg) {
			return
		}
		writeError(w, status, code, msg)
		return
	}
	if sink != nil {
		sink.result(res)
		return
	}
	writeJSON(w, 200, res)
}

// historyCoverage reports the swap data already on disk, so the UI can offer
// dates that are actually backed by a CSV instead of defaulting to a window
// that would trigger a long RPC download.
func (s *Server) historyCoverage(w http.ResponseWriter, r *http.Request) {
	cov := []history.Coverage{}
	if s.hist != nil {
		cov = s.hist.Coverage()
	}
	writeJSON(w, 200, map[string]any{"coverage": cov})
}

// simHandler serves the regime-switching LP simulator (internal/sim). With a
// history store, POST /api/v1/sim/simulate can replay a pool's swaps.
func (s *Server) simHandler() *simapi.Handler {
	h := &simapi.Handler{}
	if s.hist != nil {
		h.Source = simapi.SwapBars(func(ctx context.Context, poolID string, fromTS, toTS int64) ([]history.Row, float64, error) {
			w, err := s.hist.Ensure(ctx, poolID, fromTS, toTS)
			if err != nil {
				return nil, 0, err
			}
			return w.Swaps, w.Spec.LiquidityScale, nil
		})
	}
	return h
}

// historyPrice is a pool's daily close on a date, from the swaps on disk: the
// price an on-chain backtest starting that day opens its LP at, so the UI can
// express a range as ± percent of it. Dates off the CSV are 404, never fetched.
func (s *Server) historyPrice(w http.ResponseWriter, r *http.Request) {
	poolID, date := r.URL.Query().Get("poolId"), r.URL.Query().Get("date")
	day, err := history.ParseDay(date)
	if err != nil || poolID == "" {
		writeError(w, 400, "invalid_request", "poolId and date=YYYY-MM-DD are required")
		return
	}
	if s.hist == nil {
		writeError(w, 404, "not_found", "no swap history configured")
		return
	}
	price, ok, err := s.hist.DailyCloseAt(poolID, day)
	if err != nil {
		writeError(w, 500, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, 404, "not_found", fmt.Sprintf("no swaps on disk for %s on %s", poolID, date))
		return
	}
	writeJSON(w, 200, map[string]any{"poolId": poolID, "date": date, "price": price})
}

func classifyBacktestErr(err error) (status int, code, msg string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return 504, "timeout", "backtest exceeded deadline"
	}
	msg = err.Error()
	switch {
	case strings.Contains(msg, "invalid from") || strings.Contains(msg, "invalid to") || strings.Contains(msg, "to must be after"):
		return 400, "invalid_window", msg
	case strings.Contains(msg, "no on-chain pool mapping"):
		return 422, "unsupported_pool", msg
	case strings.Contains(msg, "RPC_URL") || strings.Contains(msg, "swap feed"):
		return 503, "history_unavailable", msg
	case strings.Contains(msg, "rpc fetch"):
		return 502, "rpc_error", msg
	case strings.Contains(msg, "no swap data"):
		return 422, "no_market_data", msg
	default:
		return 500, "internal", msg
	}
}

func (s *Server) timeframes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"timeframes": market.Timeframes})
}

func (s *Server) autoStrategies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"strategies": automation.Catalog()})
}

func (s *Server) autoJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.jobs.List(r.Context(), r.URL.Query().Get("owner"))
	if err != nil {
		writeError(w, 500, "internal", err.Error())
		return
	}
	if jobs == nil {
		jobs = []automation.Job{}
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}

func (s *Server) autoCreateJob(w http.ResponseWriter, r *http.Request) {
	writeError(w, 501, "not_implemented", "automation jobs require a verified on-chain grant (phase 2)")
}

func (s *Server) autoPatchJob(w http.ResponseWriter, r *http.Request) {
	writeError(w, 501, "not_implemented", "automation jobs require a verified on-chain grant (phase 2)")
}

func (s *Server) autoDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.jobs.Delete(r.Context(), id); err != nil {
		writeError(w, 500, "internal", err.Error())
		return
	}
	w.WriteHeader(204)
}

func (s *Server) autoRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.jobs.Runs(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "internal", err.Error())
		return
	}
	if runs == nil {
		runs = []automation.Run{}
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

func (s *Server) autoGrant(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("owner")
	writeJSON(w, 200, automation.Grant{
		Owner:    owner,
		Scope:    []string{"rebalance", "auto-hedge", "harvest", "stop-loss"},
		CapUsd:   0,
		Expiry:   time.Time{},
		Verified: false,
		Revoke:   automation.Calldata{To: "", Data: "0x", Value: "0", ChainID: 42161},
	})
}

func (s *Server) operatorStatus(w http.ResponseWriter, r *http.Request) {
	addr, _ := s.signer.Address(r.Context())
	kill, reason := s.guard.Tripped()
	writeJSON(w, 200, automation.OperatorStatus{
		Address:    string(addr),
		GasBalance: "0",
		QueueDepth: 0,
		KillSwitch: kill,
		KillReason: reason,
	})
}

func (s *Server) findPool(ctx context.Context, id string) (market.Pool, error) {
	pools, err := s.provider.Pools(ctx)
	if err != nil {
		return market.Pool{}, err
	}
	for _, p := range pools {
		if p.ID == id {
			return p, nil
		}
	}
	return market.Pool{}, market.ErrNotFound
}

func (s *Server) findPerp(ctx context.Context, id string) (market.Perp, error) {
	list, err := s.provider.Perps(ctx)
	if err != nil {
		return market.Perp{}, err
	}
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	return market.Perp{}, market.ErrNotFound
}

func (s *Server) providerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, market.ErrNotFound):
		writeError(w, 404, "not_found", err.Error())
	case errors.Is(err, market.ErrNotImplemented):
		writeError(w, 501, "not_implemented", err.Error())
	default:
		writeError(w, 500, "internal", err.Error())
	}
}

func (s *Server) SetReady(v bool) { s.ready.Store(v) }
