// Package main — cmd/sniper is the 5-minute crypto market sniper bot.
//
// It uses Coinbase spot prices to predict the direction of Polymarket's
// 5-minute Up/Down crypto markets and places aggressive bets via the
// Synthesis Trade API.
//
// # Architecture
//
//	CoinbaseFeed ──→ SniperStrategy ──→ SynthesisClient ──→ Polymarket
//	(500ms poll)     (checks every 1s)   (places orders)    (fills + resolution)
//	                       ↑
//	               MarketDiscovery
//	              (Gamma API: finds
//	               current 5M markets)
//
// # Configuration
//
//	SYNTH_API_KEY    — Synthesis API key
//	SYNTH_WALLET_ID  — Synthesis wallet ID
//	SYNTH_DRY_RUN    — "true" for dry-run mode (default: true)
//
//	-config path/to/sniper_config.yaml  (default: configs/sniper_config.yaml)
//	-monitor                            (monitor-only mode: watch prices, emit signals, never trade)
//
// # Monitor Mode
//
// With -monitor, the bot discovers markets and watches exchange prices but
// never places orders. Useful for validating the price signal before risking
// capital. Logs every signal it would have traded.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"polymarket-mm/internal/exchange"
	"polymarket-mm/internal/pricefeed"
	"polymarket-mm/internal/sniper"

	"gopkg.in/yaml.v3"
)

// SniperConfigFile is the YAML config shape.
type SniperConfigFile struct {
	DryRun bool `yaml:"dry_run"`

	Auth struct {
		APIKey   string `yaml:"api_key"`
		WalletID string `yaml:"wallet_id"`
		Venue    string `yaml:"venue"`
	} `yaml:"auth"`

	API struct {
		BaseURL string `yaml:"base_url"`
	} `yaml:"api"`

	PriceFeed struct {
		PollIntervalMs int      `yaml:"poll_interval_ms"`
		Assets         []string `yaml:"assets"`
	} `yaml:"price_feed"`

	Strategy struct {
		EarliestEntrySeconds int     `yaml:"earliest_entry_seconds"`
		LatestEntrySeconds   int     `yaml:"latest_entry_seconds"`
		MinEdgePct           float64 `yaml:"min_edge_pct"`
		StrongEdgePct        float64 `yaml:"strong_edge_pct"`
		OrderSizeUSD         float64 `yaml:"order_size_usd"`
		MaxPositionUSD       float64 `yaml:"max_position_usd"`
		MaxConcurrent        int     `yaml:"max_concurrent"`
		AggressiveBidOffset  float64 `yaml:"aggressive_bid_offset"`
	} `yaml:"strategy"`

	Logging struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"logging"`
}

func main() {
	configPath := flag.String("config", "configs/sniper_config.yaml", "path to sniper config")
	monitorOnly := flag.Bool("monitor", false, "monitor-only mode (watch prices, no trading)")
	flag.Parse()

	// ── Load config ──
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// ENV overrides
	if v := os.Getenv("SYNTH_API_KEY"); v != "" {
		cfg.Auth.APIKey = v
	}
	if v := os.Getenv("SYNTH_WALLET_ID"); v != "" {
		cfg.Auth.WalletID = v
	}
	if v := os.Getenv("SYNTH_DRY_RUN"); v == "true" {
		cfg.DryRun = true
	}

	if *monitorOnly {
		cfg.DryRun = true
	}

	// ── Logger ──
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)}
	if cfg.Logging.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)

	// ── Parse assets ──
	assets := parseAssets(cfg.PriceFeed.Assets)

	// ── Price feed ──
	feedInterval := 500 * time.Millisecond
	if cfg.PriceFeed.PollIntervalMs > 0 {
		feedInterval = time.Duration(cfg.PriceFeed.PollIntervalMs) * time.Millisecond
	}
	feed := pricefeed.NewCoinbaseFeed(feedInterval, logger)

	// ── Market discovery ──
	discovery := sniper.NewMarketDiscovery(logger, assets)

	// ── Strategy config ──
	stratCfg := sniper.DefaultSniperConfig()
	stratCfg.DryRun = cfg.DryRun
	stratCfg.Assets = assets
	if cfg.Strategy.EarliestEntrySeconds > 0 {
		stratCfg.EarliestEntrySeconds = cfg.Strategy.EarliestEntrySeconds
	}
	if cfg.Strategy.LatestEntrySeconds > 0 {
		stratCfg.LatestEntrySeconds = cfg.Strategy.LatestEntrySeconds
	}
	if cfg.Strategy.MinEdgePct > 0 {
		stratCfg.MinEdgePct = cfg.Strategy.MinEdgePct
	}
	if cfg.Strategy.StrongEdgePct > 0 {
		stratCfg.StrongEdgePct = cfg.Strategy.StrongEdgePct
	}
	if cfg.Strategy.OrderSizeUSD > 0 {
		stratCfg.OrderSizeUSD = cfg.Strategy.OrderSizeUSD
	}
	if cfg.Strategy.MaxPositionUSD > 0 {
		stratCfg.MaxPositionUSD = cfg.Strategy.MaxPositionUSD
	}
	if cfg.Strategy.MaxConcurrent > 0 {
		stratCfg.MaxConcurrent = cfg.Strategy.MaxConcurrent
	}
	if cfg.Strategy.AggressiveBidOffset > 0 {
		stratCfg.AggressiveBidOffset = cfg.Strategy.AggressiveBidOffset
	}

	// ── Order placement callback ──
	var placeOrder func(ctx context.Context, order sniper.OrderRequest) (*sniper.OrderResult, error)

	if *monitorOnly || cfg.Auth.APIKey == "" {
		// Monitor mode — just log
		placeOrder = func(ctx context.Context, order sniper.OrderRequest) (*sniper.OrderResult, error) {
			logger.Warn("MONITOR: would place order",
				"token_id", order.TokenID[:20]+"...",
				"direction", order.Direction,
				"bid", order.Price,
				"size_usd", order.SizeUSD,
			)
			return &sniper.OrderResult{OrderID: "MONITOR-" + order.EventSlug}, nil
		}
	} else {
		// Live trading via Synthesis
		placeOrder = buildSynthesisOrderPlacer(cfg, logger)
	}

	// ── Strategy ──
	strategy := sniper.NewSniperStrategy(stratCfg, feed, discovery, placeOrder, logger)

	// ── Context with signal handling ──
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// ── Start components ──
	mode := "DRY-RUN"
	if *monitorOnly {
		mode = "MONITOR-ONLY"
	} else if !cfg.DryRun {
		mode = "LIVE"
	}

	logger.Info("sniper bot starting",
		"mode", mode,
		"assets", fmt.Sprintf("%v", assets),
		"feed_interval", feedInterval.String(),
		"min_edge", fmt.Sprintf("%.4f%%", stratCfg.MinEdgePct*100),
		"strong_edge", fmt.Sprintf("%.4f%%", stratCfg.StrongEdgePct*100),
		"order_size", stratCfg.OrderSizeUSD,
		"entry_window", fmt.Sprintf("%ds-%ds", stratCfg.EarliestEntrySeconds, stratCfg.LatestEntrySeconds),
	)

	// Start price feed
	go func() {
		if err := feed.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("price feed error", "error", err)
		}
	}()

	// Wait for initial prices (up to 5 seconds)
	logger.Info("waiting for initial price data...")
	waitForPrices(ctx, feed, assets, 5*time.Second)

	// Print initial prices
	for _, a := range assets {
		if p, ok := feed.LatestPrice(a); ok {
			logger.Info("initial price", "asset", a, "price", fmt.Sprintf("$%.2f", p))
		}
	}

	// Start signal logger
	go func() {
		for sig := range strategy.Signals() {
			logger.Info("SIGNAL EMITTED",
				"asset", sig.Market.Asset,
				"direction", sig.Direction,
				"confidence", fmt.Sprintf("%.2f", sig.Confidence),
				"move", fmt.Sprintf("%.4f%%", sig.ExchangeMove*100),
				"open", fmt.Sprintf("$%.2f", sig.OpenPrice),
				"current", fmt.Sprintf("$%.2f", sig.CurrentPrice),
				"bid", fmt.Sprintf("$%.2f", sig.SuggestedBid),
				"token", sig.TokenID[:20]+"...",
			)
		}
	}()

	// Start strategy
	go func() {
		if err := strategy.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("strategy error", "error", err)
		}
	}()

	// Start periodic status printer
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ttc := sniper.TimeToClose()
				epoch := sniper.CurrentEpoch()
				bets := strategy.ActiveBetsSnapshot()

				logger.Info("STATUS",
					"epoch", epoch,
					"time_to_close", fmt.Sprintf("%.0fs", ttc.Seconds()),
					"active_bets", len(bets),
				)

				// Print prices
				for _, a := range assets {
					if p, ok := feed.LatestPrice(a); ok {
						change, hasChange := feed.PriceChange(a, time.Unix(epoch, 0))
						if hasChange {
							logger.Info("price",
								"asset", a,
								"price", fmt.Sprintf("$%.2f", p),
								"change", fmt.Sprintf("%.4f%%", change*100),
							)
						} else {
							logger.Info("price",
								"asset", a,
								"price", fmt.Sprintf("$%.2f", p),
							)
						}
					}
				}

				// Print active bets
				for _, b := range bets {
					logger.Info("active_bet",
						"slug", b.Market.EventSlug,
						"direction", b.Direction,
						"entry", b.EntryPrice,
						"resolves_in", fmt.Sprintf("%.0fs", b.ResolvesAt.Sub(time.Now()).Seconds()),
					)
				}
			}
		}
	}()

	logger.Info("sniper bot running. Press Ctrl+C to stop.")

	// Wait for shutdown
	sig := <-sigCh
	logger.Info("received shutdown signal", "signal", sig.String())
	cancel()

	// Give goroutines a moment to clean up
	time.Sleep(2 * time.Second)
	logger.Info("sniper bot stopped")
}

// buildSynthesisOrderPlacer creates the order placement function
// that calls the Synthesis Trade API.
func buildSynthesisOrderPlacer(cfg *SniperConfigFile, logger *slog.Logger) func(ctx context.Context, order sniper.OrderRequest) (*sniper.OrderResult, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	baseURL := cfg.API.BaseURL
	if baseURL == "" {
		baseURL = "https://synthesis.trade/api/v1"
	}
	apiKey := cfg.Auth.APIKey
	walletID := cfg.Auth.WalletID
	venue := cfg.Auth.Venue
	if venue == "" {
		venue = "pol"
	}

	return func(ctx context.Context, order sniper.OrderRequest) (*sniper.OrderResult, error) {
		v := order.Venue
		if v == "" {
			v = venue
		}

		// Build order body
		// amount is in USDC (the API interprets amount in the unit specified)
		// Minimum order size is $5 USDC
		amount := order.SizeUSD
		if amount < 5.0 {
			amount = 5.0 // Synthesis minimum
		}
		body := map[string]interface{}{
			"token_id": order.TokenID,
			"side":     "buy",
			"amount":   fmt.Sprintf("%.2f", amount),
			"type":     "limit",
			"units":    "USDC",
			"price":    fmt.Sprintf("%.2f", order.Price),
		}
		bodyBytes, _ := json.Marshal(body)

		url := fmt.Sprintf("%s/wallet/%s/%s/order", baseURL, v, walletID)
		req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(bodyBytes)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-KEY", apiKey)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("synthesis order request failed: %w", err)
		}
		defer resp.Body.Close()

		var result struct {
			Success  bool            `json:"success"`
			Response json.RawMessage `json:"response"`
			Error    string          `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}

		if !result.Success {
			return nil, fmt.Errorf("synthesis order failed: %s", result.Error)
		}

		// Try to extract order ID from response
		var orderResp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(result.Response, &orderResp)

		logger.Info("synthesis order response",
			"order_id", orderResp.ID,
			"status", resp.StatusCode,
			"raw", string(result.Response),
		)

		return &sniper.OrderResult{
			OrderID: orderResp.ID,
		}, nil
	}
}

func loadConfig(path string) (*SniperConfigFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg SniperConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parseAssets(names []string) []pricefeed.Asset {
	if len(names) == 0 {
		return pricefeed.AllAssets
	}
	var result []pricefeed.Asset
	for _, n := range names {
		switch strings.ToUpper(n) {
		case "BTC":
			result = append(result, pricefeed.BTC)
		case "ETH":
			result = append(result, pricefeed.ETH)
		case "SOL":
			result = append(result, pricefeed.SOL)
		case "XRP":
			result = append(result, pricefeed.XRP)
		}
	}
	if len(result) == 0 {
		return pricefeed.AllAssets
	}
	return result
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

func waitForPrices(ctx context.Context, feed *pricefeed.CoinbaseFeed, assets []pricefeed.Asset, timeout time.Duration) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case <-ticker.C:
			allReady := true
			for _, a := range assets {
				if _, ok := feed.LatestPrice(a); !ok {
					allReady = false
					break
				}
			}
			if allReady {
				return
			}
		}
	}
}

// Ensure we use the exchange package (needed for go build to include it)
var _ = exchange.NewSynthesisClientAdapter
var _ = strconv.Itoa
