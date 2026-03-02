package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"polymarket-mm/internal/config"
	"polymarket-mm/internal/exchange"
	"polymarket-mm/internal/market"
)

func main() {
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      os.Getenv("POLY_API_KEY_ID"),
			PrivateKeyB64: os.Getenv("POLY_PRIVATE_KEY_B64"),
		},
		API: config.APIConfig{
			BaseURL: "https://api.polymarket.us",
		},
		Scanner: config.ScannerConfig{
			PollInterval:   120 * time.Second,
			MinSpread:      0.02,
			MaxEndDateDays: 150,
			IncludeKeywords: []string{
				"tec-cbb-champ",
				"tec-nba-champ",
				"tec-nba-mvp",
				"tec-nhl-hart",
			},
		},
		Risk: config.RiskConfig{
			MaxPositionPerMarket: 5.0,
			MaxGlobalExposure:    10.0,
			MaxMarketsActive:     3,
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	auth, err := exchange.NewAuth(cfg)
	if err != nil {
		fmt.Println("Auth error:", err)
		os.Exit(1)
	}
	client := exchange.NewClient(cfg, auth, logger)
	scanner := market.NewScanner(client, cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Println("Starting scanner (single scan)...")
	start := time.Now()

	// Run scanner in background, read one result
	go scanner.Run(ctx)

	select {
	case result := <-scanner.Results():
		elapsed := time.Since(start)
		fmt.Printf("\nScan completed in %v\n", elapsed)
		fmt.Printf("Selected %d markets:\n\n", len(result.Markets))
		for i, alloc := range result.Markets {
			m := alloc.Market
			fmt.Printf("  %d. %s\n", i+1, m.Slug)
			fmt.Printf("     Question: %s\n", m.Question)
			fmt.Printf("     Bid: %.4f  Ask: %.4f  Spread: %.4f (%.1f%%)\n",
				m.BestBid, m.BestAsk, m.Spread, m.Spread*100)
			fmt.Printf("     Score: %.4f\n\n", alloc.Score)
		}
	case <-ctx.Done():
		fmt.Println("Timeout waiting for scan result!")
	}
}
