package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"

	"polymarket-mm/internal/config"
	"polymarket-mm/internal/exchange"
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
	}

	auth, err := exchange.NewAuth(cfg)
	if err != nil {
		log.Fatal("auth: ", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client := exchange.NewClient(cfg, auth, logger)
	ctx := context.Background()

	// Check open orders
	fmt.Println("=== OPEN ORDERS ===")
	orders, err := client.GetOpenOrders(ctx, nil)
	if err != nil {
		fmt.Printf("Error getting orders: %v\n", err)
	} else {
		data, _ := json.MarshalIndent(orders, "", "  ")
		fmt.Println(string(data))
		fmt.Printf("Total open orders: %d\n", len(orders))
	}

	// Check balance
	fmt.Println("\n=== BALANCES ===")
	bals, err := client.GetBalances(ctx)
	if err != nil {
		fmt.Printf("Error getting balances: %v\n", err)
	} else {
		data, _ := json.MarshalIndent(bals, "", "  ")
		fmt.Println(string(data))
	}

	// Check positions
	fmt.Println("\n=== POSITIONS ===")
	positions, err := client.GetPositions(ctx)
	if err != nil {
		fmt.Printf("Error getting positions: %v\n", err)
	} else {
		data, _ := json.MarshalIndent(positions, "", "  ")
		fmt.Println(string(data))
	}

	// Check books for active markets
	slugs := []string{
		"aec-cbb-mst-ind-2026-03-01",
		"aec-cbb-depaul-marq-2026-03-01",
		"aec-nba-no-lac-2026-03-01",
		"aec-nba-okc-dal-2026-03-01",
		"aec-nhl-fla-nyi-2026-03-01",
	}
	for _, slug := range slugs {
		fmt.Printf("\n=== BOOK: %s ===\n", slug)
		book, err := client.GetUSOrderBook(ctx, slug)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		data, _ := json.MarshalIndent(book, "", "  ")
		fmt.Println(string(data))
	}
}
