// Polymarket Market Maker — an automated market-making bot for Polymarket
// binary prediction markets using the Avellaneda-Stoikov algorithm.
//
// Architecture:
//
//	main.go            — entry point: loads config, starts engine, waits for SIGINT/SIGTERM
//	engine/engine.go   — orchestrator: wires scanner → strategy → exchange, manages market lifecycle
//	strategy/maker.go  — Avellaneda-Stoikov quoting: computes bid/ask from mid price + inventory skew
//	strategy/inventory — tracks YES/NO positions, avg entry prices, realized/unrealized PnL
//	market/scanner.go  — polls Gamma API for wide-spread markets, ranks by opportunity score
//	market/book.go     — local order book mirror fed by WebSocket snapshots + price changes
//	exchange/client.go — REST client for Polymarket CLOB API (place/cancel orders, fetch book)
//	exchange/auth.go   — L1 (EIP-712) and L2 (HMAC) authentication for the Polymarket API
//	exchange/ws.go     — WebSocket feeds (market data + user fills/orders) with auto-reconnect
//	risk/manager.go    — enforces per-market, global exposure, daily loss, and price-shock limits
//	store/store.go     — JSON file persistence for positions (survives restarts)
//
// How it makes money:
//
//	The bot captures the bid-ask spread on binary prediction markets.
//	It posts a buy (bid) below mid price and a sell (ask) above mid price.
//	When both sides fill, the bot earns the spread difference.
//	Avellaneda-Stoikov adjusts quotes based on inventory risk — if the bot
//	accumulates too much of one side, it skews prices to attract offsetting fills.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"polymarket-mm/internal/api"
	"polymarket-mm/internal/config"
	"polymarket-mm/internal/engine"
)

func main() {
	// Load config
	cfgPath := "configs/config.yaml"
	if p := os.Getenv("POLY_CONFIG"); p != "" {
		cfgPath = p
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("failed to load config", "error", err, "path", cfgPath)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid config", "error", err)
		os.Exit(1)
	}

	// Set up logger
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)}
	if cfg.Logging.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)

	// Create and start engine
	eng, err := engine.New(*cfg, logger)
	if err != nil {
		logger.Error("failed to create engine", "error", err)
		os.Exit(1)
	}

	// Start dashboard API server if enabled
	var apiServer *api.Server
	if cfg.Dashboard.Enabled {
		apiServer = api.NewServer(cfg.Dashboard, eng, *cfg, logger)
		go func() {
			if err := apiServer.Start(); err != nil {
				logger.Error("dashboard server failed", "error", err)
			}
		}()
		logger.Info("dashboard started", "url", fmt.Sprintf("http://localhost:%d", cfg.Dashboard.Port))
	}

	if err := eng.Start(); err != nil {
		logger.Error("failed to start engine", "error", err)
		os.Exit(1)
	}

	if cfg.DryRun {
		logger.Warn("DRY-RUN MODE — no real orders will be placed")
	}

	logger.Info("polymarket market maker started",
		"markets_max", cfg.Risk.MaxMarketsActive,
		"order_size", cfg.Strategy.OrderSizeUSD,
		"max_exposure", cfg.Risk.MaxGlobalExposure,
		"dry_run", cfg.DryRun,
	)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	logger.Info("received shutdown signal", "signal", sig.String())

	// Stop dashboard first
	if apiServer != nil {
		if err := apiServer.Stop(); err != nil {
			logger.Error("failed to stop dashboard", "error", err)
		}
	}

	eng.Stop()
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
