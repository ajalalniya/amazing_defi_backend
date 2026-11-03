package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"log/slog"
	"os"
	"path/filepath"

	"amazing/internal/history"
	"amazing/internal/portfolio"
	"amazing/internal/simulate"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := simulate.NewMock(log)
	store := portfolio.NewMemory(portfolio.SeedPositions(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)), true)
	return New(log, mock, store, false)
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandlers(t *testing.T) {
	s := testServer(t)
	h := s.Handler(nil)

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
		code   string
	}{
		{"healthz", "GET", "/healthz", nil, 200, ""},
		{"status", "GET", "/api/v1/status", nil, 200, ""},
		{"readyz", "GET", "/readyz", nil, 200, ""},
		{"metrics", "GET", "/metrics", nil, 200, ""},
		{"pools", "GET", "/api/v1/pools", nil, 200, ""},
		{"pools v4", "GET", "/api/v1/pools?version=v4", nil, 200, ""},
		{"pools search", "GET", "/api/v1/pools?q=eth", nil, 200, ""},
		{"pool by id", "GET", "/api/v1/pools/v4-eth-usdc-005", nil, 200, ""},
		{"pool missing", "GET", "/api/v1/pools/nope", nil, 404, "not_found"},
		{"depth", "GET", "/api/v1/pools/v4-eth-usdc-005/depth?bins=48", nil, 200, ""},
		{"candles", "GET", "/api/v1/pools/v4-eth-usdc-005/candles?tf=15m", nil, 200, ""},
		{"bad tf", "GET", "/api/v1/pools/v4-eth-usdc-005/candles?tf=3y", nil, 400, "invalid_timeframe"},
		{"perps", "GET", "/api/v1/perps", nil, 200, ""},
		{"perp by id", "GET", "/api/v1/perps/ETH-USD", nil, 200, ""},
		{"perp candles", "GET", "/api/v1/perps/ETH-USD/candles?tf=4h", nil, 200, ""},
		{"timeframes", "GET", "/api/v1/timeframes", nil, 200, ""},
		{"portfolio", "GET", "/api/v1/portfolio?owner=demo", nil, 200, ""},
		{"lp quote ok", "POST", "/api/v1/quotes/lp", lpQuoteReq{PoolID: "v4-eth-usdc-005", Lower: 3000, Upper: 3400, CapitalUSD: 10000}, 200, ""},
		{"lp quote bad range", "POST", "/api/v1/quotes/lp", lpQuoteReq{PoolID: "v4-eth-usdc-005", Lower: 3400, Upper: 3000}, 400, "invalid_range"},
		{"lp quote zero lower", "POST", "/api/v1/quotes/lp", lpQuoteReq{PoolID: "v4-eth-usdc-005", Lower: 0, Upper: 3400}, 400, "invalid_range"},
		{"perp quote ok", "POST", "/api/v1/quotes/perp", perpQuoteReq{PerpID: "ETH-USD", Side: "long", Collateral: 5000, Leverage: 5}, 200, ""},
		{"perp quote over lev", "POST", "/api/v1/quotes/perp", perpQuoteReq{PerpID: "ETH-USD", Side: "long", Collateral: 5000, Leverage: 99}, 400, "invalid_leverage"},
		{"perp quote no coll", "POST", "/api/v1/quotes/perp", perpQuoteReq{PerpID: "ETH-USD", Side: "long", Collateral: 0, Leverage: 5}, 400, "invalid_capital"},
		{"open lp", "POST", "/api/v1/positions", map[string]any{"owner": "0xabc", "kind": "lp", "poolId": "v3-eth-usdc-005", "capital": 1000, "lower": 3000, "upper": 3400}, 201, ""},
		{"open lp bad range", "POST", "/api/v1/positions", map[string]any{"owner": "0xabc", "kind": "lp", "poolId": "v3-eth-usdc-005", "capital": 1000, "lower": 10, "upper": 1}, 400, "invalid_range"},
		{"backtest ok", "POST", "/api/v1/backtest", map[string]any{"days": 30, "driftPct": 10, "volPct": 40, "seed": 1, "legs": []map[string]any{{"kind": "lp", "poolId": "v4-eth-usdc-005", "capital": 10000, "lower": 2800, "upper": 3600}}}, 200, ""},
		{"backtest no legs", "POST", "/api/v1/backtest", map[string]any{"days": 30, "legs": []any{}}, 400, "invalid_legs"},
		{"backtest bad days", "POST", "/api/v1/backtest", map[string]any{"days": 0, "legs": []map[string]any{{"kind": "long", "perpId": "ETH-USD", "capital": 1, "leverage": 2}}}, 400, "invalid_days"},
		{"backtest window incomplete", "POST", "/api/v1/backtest", map[string]any{"from": "2024-01-01", "legs": []map[string]any{{"kind": "lp", "poolId": "v4-eth-usdc-005", "capital": 1000, "lower": 1, "upper": 2}}}, 400, "invalid_window"},
		{"strategies", "GET", "/api/v1/automation/strategies", nil, 200, ""},
		{"jobs", "GET", "/api/v1/automation/jobs?owner=0xabc", nil, 200, ""},
		{"create job", "POST", "/api/v1/automation/jobs", map[string]any{"owner": "0xabc"}, 501, "not_implemented"},
		{"grant", "GET", "/api/v1/automation/grant?owner=0xabc", nil, 200, ""},
		{"operator", "GET", "/api/v1/operator/status", nil, 200, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, h, tc.method, tc.path, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status %d want %d body=%s", w.Code, tc.want, w.Body.String())
			}
			if tc.code != "" {
				var e errBody
				if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
					t.Fatal(err)
				}
				if e.Error.Code != tc.code {
					t.Fatalf("code %q want %q", e.Error.Code, tc.code)
				}
			}
		})
	}
}

func TestBacktestStreamNDJSON(t *testing.T) {
	s := testServer(t)
	h := s.Handler(nil)
	body := map[string]any{
		"days": 30, "driftPct": 10, "volPct": 40, "seed": 1,
		"legs": []map[string]any{{"kind": "lp", "poolId": "v4-eth-usdc-005", "capital": 10000, "lower": 2800, "upper": 3600}},
	}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/v1/backtest?stream=1", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/x-ndjson")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Fatalf("content-type %q", ct)
	}
	var sawResult bool
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type   string          `json:"type"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if ev.Type == "result" {
			sawResult = true
			if len(ev.Result) == 0 {
				t.Fatal("empty result")
			}
		}
	}
	if !sawResult {
		t.Fatalf("no result event in %s", w.Body.String())
	}
}

func TestSeedPoolCountAndFunding(t *testing.T) {
	s := testServer(t)
	h := s.Handler(nil)
	w := doJSON(t, h, "GET", "/api/v1/pools", nil)
	var wrap struct {
		Pools []struct {
			ID     string  `json:"id"`
			FeeBps float64 `json:"feeBps"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrap); err != nil {
		t.Fatal(err)
	}
	if len(wrap.Pools) != 14 {
		t.Fatalf("got %d pools, want 14", len(wrap.Pools))
	}
	w = doJSON(t, h, "GET", "/api/v1/perps", nil)
	var pwrap struct {
		Perps []struct {
			ID      string  `json:"id"`
			Funding float64 `json:"funding8h"`
		} `json:"perps"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &pwrap)
	if len(pwrap.Perps) != 5 {
		t.Fatalf("got %d perps", len(pwrap.Perps))
	}
	for _, p := range pwrap.Perps {
		if p.Funding >= 0.01 || p.Funding <= -0.01 {
			t.Fatalf("%s funding8h %v looks like a percent, not a fraction", p.ID, p.Funding)
		}
	}
}

func TestClosePosition(t *testing.T) {
	s := testServer(t)
	h := s.Handler(nil)
	w := doJSON(t, h, "DELETE", "/api/v1/positions/p1?owner=demo", nil)
	if w.Code != 200 {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "realisedPnl") {
		t.Fatalf("expected realisedPnl, got %s", w.Body.String())
	}
}

func TestQuoteMatchesConcentrationGolden(t *testing.T) {
	s := testServer(t)
	h := s.Handler(nil)
	pool, _ := s.findPool(context.Background(), "v4-eth-usdc-005")
	lo, hi := pool.Price*0.95, pool.Price*1.05
	w := doJSON(t, h, "POST", "/api/v1/quotes/lp", lpQuoteReq{
		PoolID: pool.ID, Lower: lo, Upper: hi, CapitalUSD: 14600,
	})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var q LPQuote
	_ = json.Unmarshal(w.Body.Bytes(), &q)
	if q.CapitalEfficiency < 40 || q.CapitalEfficiency > 41 {
		t.Fatalf("capitalEfficiency %v want ~40.5", q.CapitalEfficiency)
	}
}

func TestStatusReportsMock(t *testing.T) {
	s := testServer(t)
	h := s.Handler(nil)
	w := doJSON(t, h, "GET", "/api/v1/status", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body struct {
		Status   string `json:"status"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Provider != "mock" || body.Status != "ok" {
		t.Fatalf("got %+v", body)
	}
}

func TestHistoryCoverage(t *testing.T) {
	h := testServer(t).Handler(nil)
	w := doJSON(t, h, "GET", "/api/v1/history/coverage", nil)
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	// No history store wired: the field must still be an array, never null,
	// so the frontend can iterate it without a guard.
	var got struct {
		Coverage []history.Coverage `json:"coverage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Coverage == nil {
		t.Fatal("coverage is null, want []")
	}
	if !strings.Contains(w.Body.String(), `"coverage":[]`) {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestHistoryCoverageWithStore(t *testing.T) {
	dir := t.TempDir()
	const day = int64(86400)
	body := "amount0,amount1,timestamp,liquidity,sqrtPriceX96,tick,price\n" +
		fmt.Sprintf("0.001,-3.1,%d,1e18,4573075199760799433668080,-195208,3100.5\n", 20000*day) +
		fmt.Sprintf("0.001,-3.1,%d,1e18,4573075199760799433668080,-195208,3100.5\n", 20002*day+day-1)
	path := filepath.Join(dir, "swaps_"+history.BaseETHUSDC.FileKey()+".csv")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	h := testServer(t).WithHistory(history.New(dir, nil, nil)).Handler(nil)
	w := doJSON(t, h, "GET", "/api/v1/history/coverage", nil)
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Coverage []history.Coverage `json:"coverage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Coverage) != 1 {
		t.Fatalf("got %d entries, want 1: %s", len(got.Coverage), w.Body.String())
	}
	c := got.Coverage[0]
	if c.Days != 3 || c.Pair != "ETH/USDC" || c.PoolID != "v3-eth-usdc-005" {
		t.Errorf("coverage = %+v", c)
	}
	if c.FullFrom == "" || c.FullTo == "" {
		t.Errorf("full span missing: %+v", c)
	}
}
