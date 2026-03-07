// Package sniper — strategy.go implements the directional betting strategy
// for 5-minute crypto Up/Down markets.
//
// # Algorithm
//
// The strategy exploits the fact that Coinbase spot prices lead Chainlink
// Data Streams by 0.5-2 seconds. Near the end of a 5-minute window:
//
//  1. Read current Coinbase price and compare to price at window open
//  2. If abs(pctChange) > minEdgeThreshold, the direction is likely locked in:
//     Chainlink will catch up to where Coinbase already is
//  3. Buy the corresponding Up/Down token at an aggressive limit price
//  4. Wait for resolution (market closes, shares pay $1 if correct)
//
// # Risk Controls
//
//   - MinEdgePct: minimum price move to trigger a signal (default 0.03% = 3bps)
//   - MaxPositionUSD: max bet per 5M window
//   - MaxConcurrent: max simultaneous positions across all assets
//   - Don't bet in last 10 seconds (liquidity dries up, slippage too high)
//   - Don't bet in first 60 seconds (direction unclear)
package sniper

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"polymarket-mm/internal/pricefeed"
)

// SniperConfig holds strategy parameters.
type SniperConfig struct {
	// Timing
	EarliestEntrySeconds int     // earliest seconds into window to enter (default 180 = 3min)
	LatestEntrySeconds   int     // latest seconds into window to enter (default 280 = 4:40)
	PollInterval         time.Duration // how often to check for signals (default 1s)

	// Edge thresholds
	MinEdgePct    float64 // minimum abs price move to trigger (default 0.0003 = 0.03%)
	StrongEdgePct float64 // strong signal threshold (default 0.001 = 0.1%)

	// Sizing
	OrderSizeUSD     float64 // bet size in USD per signal (default 1.0)
	MaxPositionUSD   float64 // max total exposure across all active bets (default 5.0)
	MaxConcurrent    int     // max simultaneous bets (default 4)

	// Pricing — how aggressively to bid
	// The "fair" probability given our signal. We bid slightly below.
	AggressiveBidOffset float64 // how far below our fair value to bid (default 0.02 = 2c)

	// Assets to trade
	Assets []pricefeed.Asset

	// Dry run
	DryRun bool
}

// DefaultSniperConfig returns sane defaults for the strategy.
func DefaultSniperConfig() SniperConfig {
	return SniperConfig{
		EarliestEntrySeconds: 180, // 3 minutes in
		LatestEntrySeconds:   280, // 4:40 in (20s before close)
		PollInterval:         time.Second,
		MinEdgePct:           0.0003, // 0.03% — 3bps
		StrongEdgePct:        0.001,  // 0.1%  — 10bps
		OrderSizeUSD:         1.0,
		MaxPositionUSD:       5.0,
		MaxConcurrent:        4,
		AggressiveBidOffset:  0.02, // 2 cents below fair value
		Assets:               pricefeed.AllAssets,
		DryRun:               true,
	}
}

// SniperStrategy is the main strategy runner.
type SniperStrategy struct {
	cfg       SniperConfig
	feed      *pricefeed.CoinbaseFeed
	discovery *MarketDiscovery
	logger    *slog.Logger

	// Order placement callback — the caller wires this to SynthesisClient
	placeOrder func(ctx context.Context, order OrderRequest) (*OrderResult, error)

	// Track active bets to enforce MaxConcurrent
	activeMu   sync.RWMutex
	activeBets map[string]*ActiveBet // key: eventSlug

	// Track attempted slugs (success or fail) to prevent retries
	attemptedMu    sync.RWMutex
	attemptedSlugs map[string]time.Time // slug -> when attempted

	// Signals channel — external consumers can listen for signals
	signalCh chan Signal
}

// OrderRequest represents an order to place via Synthesis.
type OrderRequest struct {
	TokenID   string  // Polymarket CLOB token ID
	Side      string  // "BUY"
	Price     float64 // limit price (0-1)
	SizeUSD   float64 // dollar amount
	Venue     string  // "pol" or "sol"
	EventSlug string  // for tracking
	Direction string  // "Up" or "Down"
}

// OrderResult is returned after placing an order.
type OrderResult struct {
	OrderID   string
	Filled    bool
	FillPrice float64
	Error     error
}

// ActiveBet tracks a bet we've placed in a 5M window.
type ActiveBet struct {
	Market      *MarketWindow
	Direction   string
	EntryPrice  float64
	SizeUSD     float64
	OrderID     string
	PlacedAt    time.Time
	ResolvesAt  time.Time
}

// NewSniperStrategy creates the strategy.
func NewSniperStrategy(
	cfg SniperConfig,
	feed *pricefeed.CoinbaseFeed,
	discovery *MarketDiscovery,
	placeOrder func(ctx context.Context, order OrderRequest) (*OrderResult, error),
	logger *slog.Logger,
) *SniperStrategy {
	return &SniperStrategy{
		cfg:            cfg,
		feed:           feed,
		discovery:      discovery,
		logger:         logger.With("component", "sniper_strategy"),
		placeOrder:     placeOrder,
		activeBets:     make(map[string]*ActiveBet),
		attemptedSlugs: make(map[string]time.Time),
		signalCh:       make(chan Signal, 64),
	}
}

// Signals returns a channel that emits every signal generated (for logging/UI).
func (s *SniperStrategy) Signals() <-chan Signal {
	return s.signalCh
}

// ActiveBets returns current active bets.
func (s *SniperStrategy) ActiveBetsSnapshot() []*ActiveBet {
	s.activeMu.RLock()
	defer s.activeMu.RUnlock()
	result := make([]*ActiveBet, 0, len(s.activeBets))
	for _, b := range s.activeBets {
		cp := *b
		result = append(result, &cp)
	}
	return result
}

// Run is the main strategy loop. Blocks until ctx is cancelled.
func (s *SniperStrategy) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	s.logger.Info("sniper strategy started",
		"assets", fmt.Sprintf("%v", s.cfg.Assets),
		"min_edge", fmt.Sprintf("%.4f%%", s.cfg.MinEdgePct*100),
		"order_size", s.cfg.OrderSizeUSD,
		"dry_run", s.cfg.DryRun,
	)

	// Track which epoch we've already discovered markets for
	var lastDiscoveryEpoch int64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			now := time.Now()
			currentEpoch := CurrentEpoch()
			secondsIntoWindow := int(now.Unix() - currentEpoch)

			// Discover markets for this window (once per epoch)
			if currentEpoch != lastDiscoveryEpoch {
				s.logger.Info("new 5M window, discovering markets",
					"epoch", currentEpoch,
					"window_start", time.Unix(currentEpoch, 0).Format("15:04:05"),
					"window_end", time.Unix(currentEpoch+300, 0).Format("15:04:05"),
				)
				if _, err := s.discovery.DiscoverCurrent(ctx); err != nil {
					s.logger.Error("market discovery failed", "error", err)
				}
				lastDiscoveryEpoch = currentEpoch
			}

			// Clean up resolved bets
			s.cleanupResolvedBets(now)

			// Check if we're in the trading window
			if secondsIntoWindow < s.cfg.EarliestEntrySeconds {
				continue // too early
			}
			if secondsIntoWindow > s.cfg.LatestEntrySeconds {
				continue // too late
			}

			// Skip trading if price feed is stale (>10s without update)
			if s.feed.IsStale(10 * time.Second) {
				fails := s.feed.ConsecutiveFails()
				if fails%10 == 0 && fails > 0 {
					s.logger.Warn("SKIPPING trades — price feed is stale",
						"consecutive_fails", fails)
				}
				continue
			}

			// Check each asset for a signal
			for _, asset := range s.cfg.Assets {
				s.evaluateAsset(ctx, asset, currentEpoch, secondsIntoWindow)
			}
		}
	}
}

// evaluateAsset checks if there's a tradeable signal for a given asset.
func (s *SniperStrategy) evaluateAsset(ctx context.Context, asset pricefeed.Asset, epoch int64, secondsIn int) {
	// Already have a bet on this window?
	slug := fmt.Sprintf("%s-updown-5m-%d", toLower(string(asset)), epoch)
	s.activeMu.RLock()
	_, exists := s.activeBets[slug]
	s.activeMu.RUnlock()
	if exists {
		return
	}

	// Already attempted this slug (even if order failed)? Don't retry.
	s.attemptedMu.RLock()
	_, attempted := s.attemptedSlugs[slug]
	s.attemptedMu.RUnlock()
	if attempted {
		return
	}

	// Check concurrent limit
	s.activeMu.RLock()
	numActive := len(s.activeBets)
	s.activeMu.RUnlock()
	if numActive >= s.cfg.MaxConcurrent {
		return
	}

	// Get market data
	mw, ok := s.discovery.CurrentMarket(asset)
	if !ok {
		return
	}

	// Get exchange price at window open
	windowStart := time.Unix(epoch, 0)
	openPrice, ok := s.feed.PriceAtTime(asset, windowStart)
	if !ok {
		// Try to use the first price we have after window start
		openPrice, ok = s.feed.PriceAtTime(asset, windowStart.Add(2*time.Second))
		if !ok {
			return // no opening price data
		}
	}
	mw.OpenPrice = openPrice

	// Get current exchange price
	currentPrice, ok := s.feed.LatestPrice(asset)
	if !ok {
		return
	}

	// Calculate price change
	pctChange := (currentPrice - openPrice) / openPrice

	// Check if edge is large enough
	absChange := math.Abs(pctChange)
	if absChange < s.cfg.MinEdgePct {
		return // no edge
	}

	// Determine direction
	var direction string
	var tokenID string
	if pctChange > 0 {
		direction = "Up"
		tokenID = mw.UpTokenID
	} else {
		direction = "Down"
		tokenID = mw.DownTokenID
	}

	// Calculate confidence: linear scale from minEdge (0.5) to strongEdge (0.9)
	confidence := 0.5 + 0.4*math.Min(absChange/s.cfg.StrongEdgePct, 1.0)

	// Calculate suggested bid price
	// Our edge gives us estimated probability > 0.5 for the correct direction
	// We want to buy at a price that gives us positive expected value:
	//   EV = confidence * $1 - bidPrice > 0 → bidPrice < confidence
	// We bid at: confidence - offset (to leave a margin)
	suggestedBid := confidence - s.cfg.AggressiveBidOffset
	if suggestedBid < 0.50 {
		suggestedBid = 0.50 // floor: never pay more than fair for a coin flip
	}
	if suggestedBid > 0.60 {
		suggestedBid = 0.60 // HARD CAP: never pay more than 60c (lesson from past -$144 losses)
	}

	// Round to 2 decimals (Polymarket tick size)
	suggestedBid = math.Floor(suggestedBid*100) / 100

	signal := Signal{
		Market:       mw,
		Direction:    direction,
		Confidence:   confidence,
		ExchangeMove: pctChange,
		CurrentPrice: currentPrice,
		OpenPrice:    openPrice,
		TokenID:      tokenID,
		SuggestedBid: suggestedBid,
		GeneratedAt:  time.Now(),
	}

	// Emit signal
	select {
	case s.signalCh <- signal:
	default:
	}

	s.logger.Info("SIGNAL",
		"asset", asset,
		"direction", direction,
		"exchange_move", fmt.Sprintf("%.4f%%", pctChange*100),
		"confidence", fmt.Sprintf("%.2f", confidence),
		"open_price", fmt.Sprintf("$%.2f", openPrice),
		"current_price", fmt.Sprintf("$%.2f", currentPrice),
		"suggested_bid", fmt.Sprintf("$%.2f", suggestedBid),
		"seconds_in", secondsIn,
		"time_remaining", fmt.Sprintf("%ds", 300-secondsIn),
	)

	// Place order (or log in dry run)
	if s.cfg.DryRun {
		s.logger.Warn("DRY RUN — would place order",
			"token_id", tokenID[:20]+"...",
			"direction", direction,
			"bid", suggestedBid,
			"size_usd", s.cfg.OrderSizeUSD,
		)
		// Mark as attempted
		s.attemptedMu.Lock()
		s.attemptedSlugs[slug] = time.Now()
		s.attemptedMu.Unlock()
		// Still track as active bet in dry run
		s.activeMu.Lock()
		s.activeBets[slug] = &ActiveBet{
			Market:     mw,
			Direction:  direction,
			EntryPrice: suggestedBid,
			SizeUSD:    s.cfg.OrderSizeUSD,
			OrderID:    "DRY-RUN",
			PlacedAt:   time.Now(),
			ResolvesAt: mw.EndsAt,
		}
		s.activeMu.Unlock()
		return
	}

	// Mark as attempted BEFORE placing order — prevents retries on failure
	s.attemptedMu.Lock()
	s.attemptedSlugs[slug] = time.Now()
	s.attemptedMu.Unlock()

	// Live order
	order := OrderRequest{
		TokenID:   tokenID,
		Side:      "BUY",
		Price:     suggestedBid,
		SizeUSD:   s.cfg.OrderSizeUSD,
		Venue:     "pol",
		EventSlug: slug,
		Direction: direction,
	}

	result, err := s.placeOrder(ctx, order)
	if err != nil {
		s.logger.Error("failed to place sniper order (will NOT retry this window)",
			"error", err, "asset", asset, "slug", slug)
		return
	}

	s.activeMu.Lock()
	s.activeBets[slug] = &ActiveBet{
		Market:     mw,
		Direction:  direction,
		EntryPrice: suggestedBid,
		SizeUSD:    s.cfg.OrderSizeUSD,
		OrderID:    result.OrderID,
		PlacedAt:   time.Now(),
		ResolvesAt: mw.EndsAt,
	}
	s.activeMu.Unlock()

	s.logger.Info("sniper order placed",
		"order_id", result.OrderID,
		"asset", asset,
		"direction", direction,
		"bid", suggestedBid,
		"filled", result.Filled,
	)
}

// cleanupResolvedBets removes bets whose 5M window has ended.
func (s *SniperStrategy) cleanupResolvedBets(now time.Time) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	for slug, bet := range s.activeBets {
		// Give 30 seconds after window close for resolution
		if now.After(bet.ResolvesAt.Add(30 * time.Second)) {
			s.logger.Info("bet window closed, removing",
				"slug", slug,
				"direction", bet.Direction,
				"entry_price", bet.EntryPrice,
			)
			delete(s.activeBets, slug)
		}
	}

	// Also clean up old attempted slugs (older than 10 minutes = 2 windows)
	s.attemptedMu.Lock()
	defer s.attemptedMu.Unlock()
	for slug, t := range s.attemptedSlugs {
		if now.Sub(t) > 10*time.Minute {
			delete(s.attemptedSlugs, slug)
		}
	}
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		} else {
			b[i] = c
		}
	}
	return string(b)
}
