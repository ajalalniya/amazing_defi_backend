package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port           string
	Provider       string // mock | chain
	CORSOrigins    []string
	RPCURL         string
	EthereumRPCURL string
	BaseRPCURL     string
	ArbitrumRPCURL string
	BSCRPCURL      string
	SubgraphURL    string
	GraphAPIKey    string
	BaseSubgraph   string
	ArbSubgraph    string
	DatabaseURL    string
	DataDir        string
	ShutdownWait   time.Duration
}

func FromEnv() Config {
	c := Config{
		Port:           getenv("PORT", "8080"),
		Provider:       getenv("PROVIDER", "mock"),
		RPCURL:         os.Getenv("RPC_URL"),
		EthereumRPCURL: os.Getenv("ETHEREUM_RPC_URL"),
		BaseRPCURL:     firstNonEmpty(os.Getenv("BASE_RPC_URL"), os.Getenv("RPC_URL"), "https://mainnet.base.org"),
		ArbitrumRPCURL: getenv("ARBITRUM_RPC_URL", "https://arb1.arbitrum.io/rpc"),
		BSCRPCURL:      getenv("BSC_RPC_URL", "https://bsc-dataseed.binance.org"),
		SubgraphURL:    os.Getenv("SUBGRAPH_URL"),
		GraphAPIKey:    os.Getenv("GRAPH_API_KEY"),
		BaseSubgraph:   os.Getenv("BASE_SUBGRAPH_URL"),
		ArbSubgraph:    os.Getenv("ARBITRUM_SUBGRAPH_URL"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		DataDir:        getenv("DATA_DIR", "data"),
		ShutdownWait:   10 * time.Second,
	}
	origins := getenv("CORS_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,http://localhost:5174,http://127.0.0.1:5174,http://localhost:4173")
	for _, o := range strings.Split(origins, ",") {
		if s := strings.TrimSpace(o); s != "" {
			c.CORSOrigins = append(c.CORSOrigins, s)
		}
	}
	if n, err := strconv.Atoi(os.Getenv("SHUTDOWN_SECONDS")); err == nil && n > 0 {
		c.ShutdownWait = time.Duration(n) * time.Second
	}
	return c
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// RPCByChain maps chain ID → JSON-RPC URL for swap history fetches.
func (c Config) RPCByChain() map[int]string {
	out := map[int]string{}
	put := func(id int, url string) {
		if s := strings.TrimSpace(url); s != "" {
			out[id] = s
		}
	}
	put(1, c.EthereumRPCURL)
	put(56, c.BSCRPCURL)
	put(8453, c.BaseRPCURL)
	put(42161, c.ArbitrumRPCURL)
	return out
}

// SubgraphByChain maps chain ID → GraphQL endpoint when explicitly set.
func (c Config) SubgraphByChain() map[int]string {
	out := map[int]string{}
	put := func(id int, url string) {
		if s := strings.TrimSpace(url); s != "" {
			out[id] = s
		}
	}
	put(8453, c.BaseSubgraph)
	put(42161, c.ArbSubgraph)
	if c.SubgraphURL != "" && out[8453] == "" {
		out[8453] = c.SubgraphURL
	}
	return out
}
