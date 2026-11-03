package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amazing/internal/backtest"
	"amazing/internal/history"
)

// TestRealUIDefaultBacktest replays what the backtest page sends on first
// load, through the real HTTP handler and the real swap CSV: fetch coverage,
// take the last 30 whole days, and stream an on-chain backtest of the default
// legs. No RPC is configured, so it also proves the window is served from disk.
// Skips when the full dump is absent.
func TestRealUIDefaultBacktest(t *testing.T) {
	dir := "../../data"
	fi, err := os.Stat(filepath.Join(dir, "swaps_"+history.BaseETHUSDC.FileKey()+".csv"))
	if err != nil || fi.Size() < 100<<20 {
		t.Skip("no full swap dump in backend/data; run cmd/swapimport first")
	}
	h := testServer(t).WithHistory(history.New(dir, nil, nil)).Handler(nil)

	// 1. Coverage, as the page loads it.
	w := doJSON(t, h, "GET", "/api/v1/history/coverage", nil)
	if w.Code != 200 {
		t.Fatalf("coverage status %d: %s", w.Code, w.Body.String())
	}
	var cov struct {
		Coverage []history.Coverage `json:"coverage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cov); err != nil || len(cov.Coverage) == 0 {
		t.Fatalf("coverage: %v %s", err, w.Body.String())
	}
	c := cov.Coverage[0]
	end, _ := time.Parse("2006-01-02", c.FullTo)
	from := end.AddDate(0, 0, -29).Format("2006-01-02") // windowForHorizon(30)
	to := c.FullTo
	t.Logf("coverage %s..%s, UI default window %s..%s", c.FullFrom, c.FullTo, from, to)

	// 2. The default legs from stores/backtest.ts, streamed like the UI does.
	body := map[string]any{
		"source": "onchain", "from": from, "to": to,
		"days": 30, "driftPct": 45, "volPct": 65, "seed": 42,
		"legs": []map[string]any{
			{"kind": "lp", "poolId": "v4-eth-usdc-005", "capital": 50000, "lower": 2850, "upper": 3550},
			{"kind": "short", "perpId": "ETH-USD", "capital": 10000, "leverage": 3},
		},
	}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/v1/backtest?stream=1", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/x-ndjson")
	rec := httptest.NewRecorder()
	t0 := time.Now()
	h.ServeHTTP(rec, r)
	el := time.Since(t0)
	if rec.Code != 200 {
		t.Fatalf("backtest status %d: %s", rec.Code, rec.Body.String())
	}

	var res *backtest.Result
	var stages []string
	for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		var ev struct {
			Type    string            `json:"type"`
			Message string            `json:"message"`
			Result  *backtest.Result  `json:"result"`
			Error   map[string]string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad stream line %q: %v", line, err)
		}
		switch ev.Type {
		case "progress":
			stages = append(stages, ev.Message)
		case "error":
			t.Fatalf("stream error: %v", ev.Error)
		case "result":
			res = ev.Result
		}
	}
	if res == nil {
		t.Fatal("stream ended without a result")
	}
	for _, m := range stages {
		if strings.Contains(m, "Downloading") {
			t.Errorf("window inside coverage should not download, saw progress %q", m)
		}
	}

	t.Logf("elapsed %s, days=%d net=%.2f ret=%.2f%% dd=%.2f%% sharpe=%.2f",
		el.Round(time.Millisecond), res.Days, res.NetPnl, res.ReturnPct, res.MaxDrawdownPct, res.Sharpe)
	for _, l := range res.Legs {
		t.Logf("  %-18s carry=%10.2f market=%10.2f il=%10.2f net=%10.2f tir=%.2f liq=%v",
			l.Title, l.Carry, l.Market, l.IL, l.NetPnl, l.TimeInRange, l.Liquidated)
	}

	if res.Source != "onchain" || res.From != from || res.To != to {
		t.Errorf("result source/window = %s %s..%s", res.Source, res.From, res.To)
	}
	if res.Days != 29 || len(res.Combined) != 30 {
		t.Errorf("days=%d combined=%d, want 29 steps over 30 points", res.Days, len(res.Combined))
	}
	for i, l := range res.Legs {
		if !backtest.Additive(l) {
			t.Errorf("leg %d not additive: %+v", i, l)
		}
	}
}
