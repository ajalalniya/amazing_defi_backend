package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port         string
	Provider     string // mock | chain
	CORSOrigins  []string
	RPCURL       string
	SubgraphURL  string
	DatabaseURL  string
	ShutdownWait time.Duration
}

func FromEnv() Config {
	c := Config{
		Port:         getenv("PORT", "8080"),
		Provider:     getenv("PROVIDER", "mock"),
		RPCURL:       os.Getenv("RPC_URL"),
		SubgraphURL:  os.Getenv("SUBGRAPH_URL"),
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		ShutdownWait: 10 * time.Second,
	}
	origins := getenv("CORS_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,http://localhost:4173")
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
