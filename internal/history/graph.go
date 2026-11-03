package history

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Known Uniswap v3 subgraph IDs on The Graph Network (need GRAPH_API_KEY).
var defaultSubgraphID = map[int]string{
	ChainEthereum: "5zvR82QoaXYFyDEKLZ9t6v9adgnptxYpKpSbxtgVENFV",
	ChainArbitrum: "HyW7A86UEdYVt5b9Lrw8W2F98yKecerHKutZTRbSCX27",
}

// Graph queries Uniswap v3 subgraphs when a URL or API key is configured.
type Graph struct {
	APIKey string
	URLs   map[int]string
	log    *slog.Logger
	http   *http.Client
}

func NewGraph(apiKey string, urls map[int]string, log *slog.Logger) *Graph {
	if log == nil {
		log = slog.Default()
	}
	if urls == nil {
		urls = map[int]string{}
	}
	return &Graph{
		APIKey: strings.TrimSpace(apiKey),
		URLs:   urls,
		log:    log,
		http:   &http.Client{Timeout: 12 * time.Second},
	}
}

func (g *Graph) Enabled() bool {
	if g == nil {
		return false
	}
	if g.APIKey != "" {
		return true
	}
	for _, u := range g.URLs {
		if strings.TrimSpace(u) != "" {
			return true
		}
	}
	return false
}

func (g *Graph) endpoint(chainID int) string {
	if u := strings.TrimSpace(g.URLs[chainID]); u != "" {
		return u
	}
	if g.APIKey == "" {
		return ""
	}
	id := defaultSubgraphID[chainID]
	if id == "" {
		return ""
	}
	return "https://gateway.thegraph.com/api/" + g.APIKey + "/subgraphs/id/" + id
}

type graphStats struct {
	TVL       float64
	Volume24h float64
	Change24h float64
}

const poolStatsQuery = `query($id: ID!) {
  pool(id: $id) {
    totalValueLockedUSD
    poolDayData(first: 2, orderBy: date, orderDirection: desc) {
      volumeUSD
      open
      close
    }
  }
}`

func (g *Graph) PoolStats(ctx context.Context, spec Spec) (graphStats, error) {
	if g == nil || !g.Enabled() {
		return graphStats{}, fmt.Errorf("subgraph not configured")
	}
	url := g.endpoint(spec.ChainID)
	if url == "" {
		return graphStats{}, fmt.Errorf("no subgraph for chain %d", spec.ChainID)
	}
	id := strings.ToLower(spec.Address)
	body, _ := json.Marshal(map[string]any{
		"query":     poolStatsQuery,
		"variables": map[string]string{"id": id},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return graphStats{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := g.http.Do(req)
	if err != nil {
		return graphStats{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return graphStats{}, err
	}
	if res.StatusCode >= 300 {
		return graphStats{}, fmt.Errorf("subgraph status %d", res.StatusCode)
	}
	return parsePoolStats(raw)
}

type graphPoolResp struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Data struct {
		Pool *struct {
			TotalValueLockedUSD string `json:"totalValueLockedUSD"`
			PoolDayData         []struct {
				VolumeUSD string `json:"volumeUSD"`
				Open      string `json:"open"`
				Close     string `json:"close"`
			} `json:"poolDayData"`
		} `json:"pool"`
	} `json:"data"`
}

func parsePoolStats(raw []byte) (graphStats, error) {
	var wrap graphPoolResp
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return graphStats{}, err
	}
	if len(wrap.Errors) > 0 {
		return graphStats{}, fmt.Errorf("subgraph: %s", wrap.Errors[0].Message)
	}
	if wrap.Data.Pool == nil {
		return graphStats{}, fmt.Errorf("pool not in subgraph")
	}
	out := graphStats{TVL: parseFloat(wrap.Data.Pool.TotalValueLockedUSD)}
	days := wrap.Data.Pool.PoolDayData
	if len(days) > 0 {
		out.Volume24h = parseFloat(days[0].VolumeUSD)
		open := parseFloat(days[0].Open)
		close := parseFloat(days[0].Close)
		if open > 0 && close > 0 {
			out.Change24h = (close - open) / open * 100
		}
	}
	return out, nil
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, _ := strconv.ParseFloat(s, 64)
	return n
}
