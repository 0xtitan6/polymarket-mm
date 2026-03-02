package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

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

	auth, _ := exchange.NewAuth(cfg)
	client := exchange.NewClient(cfg, auth, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Cancel all open orders
	resp, err := client.CancelAll(ctx)
	if err != nil {
		fmt.Println("Cancel all error:", err)
		os.Exit(1)
	}
	fmt.Printf("Cancelled %d orders: %v\n", len(resp.CanceledOrderIDs), resp.CanceledOrderIDs)

	// Check open orders
	orders, err := client.GetOpenOrders(ctx, nil)
	if err != nil {
		fmt.Println("Get open orders error:", err)
	} else {
		fmt.Printf("Open orders remaining: %d\n", len(orders))
	}

	// Check balances
	balances, err := client.GetBalances(ctx)
	if err != nil {
		fmt.Println("Get balances error:", err)
	} else {
		for _, b := range balances {
			fmt.Printf("Balance: %+v\n", b)
		}
	}
}
