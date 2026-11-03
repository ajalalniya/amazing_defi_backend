package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"amazing/internal/api"
	"amazing/internal/config"
	"amazing/internal/history"
	"amazing/internal/market"
	"amazing/internal/portfolio"
	"amazing/internal/simulate"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.FromEnv()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hist := history.New(cfg.DataDir, cfg.RPCByChain(), log)
	if g := history.NewGraph(cfg.GraphAPIKey, cfg.SubgraphByChain(), log); g.Enabled() {
		hist.Graph = g
		log.Info("subgraph", "enabled", true)
	}

	var provider market.Provider
	switch cfg.Provider {
	case "chain":
		provider = market.NewChain(hist, log)
		log.Info("provider", "mode", "chain")
	default:
		mock := simulate.NewMock(log)
		mock.Start(ctx)
		provider = mock
		log.Info("provider", "mode", "mock")
	}

	store := portfolio.NewMemory(portfolio.SeedPositions(time.Now()), cfg.Provider != "chain")
	srvAPI := api.New(log, provider, store, cfg.Provider == "chain").WithHistory(hist)
	log.Info("history", "dir", cfg.DataDir, "chains", configuredChains(cfg))
	go srvAPI.Hub().Start(ctx)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srvAPI.Handler(cfg.CORSOrigins),
		ReadHeaderTimeout: 8 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	srvAPI.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownWait)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	srvAPI.Hub().Drain()
}

func configuredChains(cfg config.Config) []string {
	var out []string
	for id := range cfg.RPCByChain() {
		out = append(out, history.ChainName(id))
	}
	return out
}
