package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/marketcollector"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/observability"
)

func main() {
	cfg := config.Load()
	logPath := cfg.MarketCollectorLogPath
	if logPath == "" && cfg.LogPath != "" {
		logPath = cfg.LogPath + ".collector"
	}
	log, logStore, err := observability.New(logPath, cfg.LogRetentionDays)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize market collector logging: %v\n", err)
		os.Exit(1)
	}
	defer logStore.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	collector := marketcollector.NewServer(cfg.MarketCollectorSocket, exchange.NewHealthRegistry(), log)
	if err := collector.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("market collector stopped", "error", err)
		os.Exit(1)
	}
}
