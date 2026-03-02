package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"polymarket-mm/internal/config"
	"polymarket-mm/internal/exchange"
	"polymarket-mm/pkg/types"
)

func main() {
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      os.Getenv("POLY_API_KEY_ID"),
			PrivateKeyB64: os.Getenv("POLY_PRIVATE_KEY_B64"),
		},
		API: config.APIConfig{BaseURL: "https://api.polymarket.us"},
	}
	auth, _ := exchange.NewAuth(cfg)
	client := exchange.NewClient(cfg, auth, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// These are the tradeable tec- markets
	active := true
	closed := false
	var all []types.USMarket
	for offset := 0; ; offset += 100 {
		page, err := client.GetMarkets(ctx, types.MarketQueryParams{Active: &active, Closed: &closed, Limit: 100, Offset: offset})
		if err != nil { break }
		all = append(all, page...)
		if len(page) < 100 { break }
	}

	fmt.Printf("Total tradeable (tec-) markets: %d\n\n", len(all))

	// Show market types and end dates
	types2 := make(map[string]int)
	for _, m := range all {
		t := m.SportsMarketType
		if t == "" { t = m.MarketType }
		types2[t]++
	}
	fmt.Println("Market types:")
	for t, c := range types2 {
		fmt.Printf("  %s: %d\n", t, c)
	}

	// Check for any game-specific markets (moneyline, spread, totals)
	fmt.Println("\nGame-specific market types:")
	for _, m := range all {
		t := m.SportsMarketType
		if t == "moneyline" || t == "spread" || t == "totals" || strings.Contains(t, "prop") {
			fmt.Printf("  %s: %s (%s) end=%s\n", t, m.Slug, m.Question, m.EndDate[:10])
		}
	}

	// Show end dates distribution 
	endDates := make(map[string]int)
	for _, m := range all {
		if len(m.EndDate) >= 10 {
			endDates[m.EndDate[:10]]++
		}
	}
	fmt.Println("\nEnd date distribution:")
	for d, c := range endDates {
		fmt.Printf("  %s: %d markets\n", d, c)
	}
	
	// Check if any contain "03-01" in slug
	fmt.Println("\nMarkets with March in slug:")
	for _, m := range all {
		if strings.Contains(m.Slug, "03-0") {
			fmt.Printf("  %s | %s | end=%s\n", m.Slug, m.Question, m.EndDate)
		}
	}
	
	// Print sample of all slugs
	fmt.Println("\nFirst 20 slugs:")
	for i, m := range all {
		if i >= 20 { break }
		fmt.Printf("  %s | %s\n", m.Slug, m.Question)
	}
}
