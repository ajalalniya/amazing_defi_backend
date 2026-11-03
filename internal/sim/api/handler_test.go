package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"amazing/internal/sim/engine"
)

func serve(t *testing.T, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	(&Handler{}).Register(mux, "/sim")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestSimulateSyntheticAndBars(t *testing.T) {
	cfg := `{"strategies": ["lp_only", "step", "regime"], "monte_carlo": {"horizon": "2d"}}`
	rec, _ := serve(t, "/sim/simulate", `{"config": `+cfg+`, "synthetic": {"path": 3}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("synthetic: %d %s", rec.Code, rec.Body)
	}
	var res engine.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Strategies) != 3 || res.Bars != 2*288+1 || len(res.Strategies[0].Equity) != res.Bars {
		t.Fatalf("unexpected result: %d strategies, %d bars", len(res.Strategies), res.Bars)
	}

	bars := `[{"time":"2026-01-01T00:00:00Z","close":3000},{"time":"2026-01-01T00:05:00Z","close":3010},{"time":"2026-01-01T00:10:00Z","close":2990}]`
	rec, _ = serve(t, "/sim/simulate", `{"config": {"strategies": ["step"]}, "bars": `+bars+`, "ledger": true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ledger"`) {
		t.Fatalf("bars: %d %s", rec.Code, rec.Body)
	}
}

func TestMonteCarloEndpoint(t *testing.T) {
	rec, _ := serve(t, "/sim/montecarlo", `{"config": {"strategies": ["step"], "monte_carlo": {"paths": 20, "horizon": "1d"}}, "samples": true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var res engine.MCResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Paths != 20 || len(res.Strategies[0].Samples) != 20 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestBadRequests(t *testing.T) {
	for _, c := range []struct {
		path, body string
		code       int
		msg        string
	}{
		{"/sim/simulate", `{"config": {"capital": -5}, "synthetic": {}}`, 400, "capital"},
		{"/sim/simulate", `{"config": {}}`, 400, "bars"},
		{"/sim/simulate", `{"pool": {"poolId": "x", "from": "2026-01-01", "to": "2026-01-02"}}`, 501, "data source"},
		{"/sim/simulate", `{"nope": 1}`, 400, "nope"},
		{"/sim/montecarlo", `{"config": {"monte_carlo": {"paths": 50000}}}`, 413, "limit"},
	} {
		rec, out := serve(t, c.path, c.body)
		msg, _ := out["error"].(map[string]any)["message"].(string)
		if rec.Code != c.code || !strings.Contains(msg, c.msg) {
			t.Errorf("%s %s: %d %q, want %d mentioning %q", c.path, c.body, rec.Code, msg, c.code, c.msg)
		}
	}
}
