// Package pricefeed provides real-time cryptocurrency price feeds from
// centralized exchanges. The primary source is Coinbase's public REST API
// which requires no authentication and has ~100-150ms latency.
//
// Exchange prices LEAD Chainlink Data Streams by 0.5-2 seconds because
// Chainlink aggregates FROM exchanges. This time advantage is the basis
// of the 5M market sniper strategy.
package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Asset represents a supported cryptocurrency.
type Asset string

const (
	BTC Asset = "BTC"
	ETH Asset = "ETH"
	SOL Asset = "SOL"
	XRP Asset = "XRP"
)

// AllAssets lists every crypto we track for 5M markets.
var AllAssets = []Asset{BTC, ETH, SOL, XRP}

// PriceTick holds a single price observation.
type PriceTick struct {
	Asset     Asset
	Price     float64
	Timestamp time.Time
	Source    string
	Latency  time.Duration // round-trip HTTP time
}

// coinbaseSpotResp is the Coinbase /v2/prices/{pair}/spot JSON shape.
type coinbaseSpotResp struct {
	Data struct {
		Amount   string `json:"amount"`
		Base     string `json:"base"`
		Currency string `json:"currency"`
	} `json:"data"`
}

// CoinbaseFeed polls the Coinbase public REST API for spot prices.
// It maintains a latest-price cache that callers can read lock-free
// via LatestPrice(asset).
type CoinbaseFeed struct {
	httpClient *http.Client
	interval   time.Duration
	logger     *slog.Logger

	mu     sync.RWMutex
	latest map[Asset]*PriceTick // most recent price per asset

	// Price history for calculating momentum / direction
	histMu  sync.RWMutex
	history map[Asset][]PriceTick // rolling window per asset

	// Subscriber channel — sniper listens for every tick
	subMu   sync.RWMutex
	subCh   chan PriceTick

	// Staleness tracking — detect and recover from feed stalls
	failMu          sync.Mutex
	consecutiveFails int
	lastSuccessAt   time.Time
}

// NewCoinbaseFeed creates a new feed that polls every `interval`.
// Recommended interval: 500ms for sniper, 2s for monitoring.
func NewCoinbaseFeed(interval time.Duration, logger *slog.Logger) *CoinbaseFeed {
	return &CoinbaseFeed{
		httpClient:    &http.Client{Timeout: 3 * time.Second},
		interval:      interval,
		logger:        logger.With("component", "coinbase_feed"),
		latest:        make(map[Asset]*PriceTick),
		history:       make(map[Asset][]PriceTick),
		subCh:         make(chan PriceTick, 256),
		lastSuccessAt: time.Now(),
	}
}

// Subscribe returns a channel that receives every price tick.
func (f *CoinbaseFeed) Subscribe() <-chan PriceTick {
	return f.subCh
}

// Run starts polling all assets. Blocks until ctx is cancelled.
func (f *CoinbaseFeed) Run(ctx context.Context) error {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	// Immediate first poll
	f.pollAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			f.pollAll(ctx)
		}
	}
}

// pollAll fetches spot prices for all assets concurrently.
// Tracks consecutive failures and logs warnings when the feed goes stale.
func (f *CoinbaseFeed) pollAll(ctx context.Context) {
	var wg sync.WaitGroup
	var anySuccess int32 // atomic-ish, only used for logging
	for _, asset := range AllAssets {
		wg.Add(1)
		go func(a Asset) {
			defer wg.Done()
			if f.fetchSpot(ctx, a) {
				atomicStoreInt32(&anySuccess, 1)
			}
		}(asset)
	}
	wg.Wait()

	f.failMu.Lock()
	defer f.failMu.Unlock()

	if atomicLoadInt32(&anySuccess) > 0 {
		if f.consecutiveFails > 3 {
			f.logger.Info("coinbase feed RECOVERED after stall",
				"missed_polls", f.consecutiveFails)
		}
		f.consecutiveFails = 0
		f.lastSuccessAt = time.Now()
	} else {
		f.consecutiveFails++
		staleFor := time.Since(f.lastSuccessAt)
		if f.consecutiveFails == 5 {
			f.logger.Warn("coinbase feed STALE — 5 consecutive failed polls",
				"stale_for", staleFor.Round(time.Second).String())
		} else if f.consecutiveFails%20 == 0 {
			f.logger.Error("coinbase feed STILL STALE",
				"consecutive_fails", f.consecutiveFails,
				"stale_for", staleFor.Round(time.Second).String())
		}
		// Back off slightly if we keep failing — wait an extra 500ms
		if f.consecutiveFails > 10 {
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// Simple int32 helpers (avoid importing sync/atomic for this)
var atomicVal int32
func atomicStoreInt32(addr *int32, val int32) { *addr = val }
func atomicLoadInt32(addr *int32) int32       { return *addr }

// fetchSpot fetches a single asset spot price from Coinbase.
// Returns true if the price was successfully fetched.
func (f *CoinbaseFeed) fetchSpot(ctx context.Context, asset Asset) bool {
	url := fmt.Sprintf("https://api.coinbase.com/v2/prices/%s-USD/spot", string(asset))

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		f.logger.Debug("coinbase fetch failed", "asset", asset, "error", err)
		return false
	}
	defer resp.Body.Close()
	latency := time.Since(start)

	if resp.StatusCode != 200 {
		f.logger.Debug("coinbase non-200", "asset", asset, "status", resp.StatusCode)
		return false
	}

	var body coinbaseSpotResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		f.logger.Debug("coinbase decode failed", "asset", asset, "error", err)
		return false
	}

	price, err := strconv.ParseFloat(body.Data.Amount, 64)
	if err != nil {
		return false
	}

	tick := PriceTick{
		Asset:     asset,
		Price:     price,
		Timestamp: time.Now(),
		Source:    "coinbase",
		Latency:  latency,
	}

	// Update latest
	f.mu.Lock()
	f.latest[asset] = &tick
	f.mu.Unlock()

	// Append to history (keep last 600 ticks = ~5 min at 500ms)
	f.histMu.Lock()
	hist := f.history[asset]
	hist = append(hist, tick)
	if len(hist) > 600 {
		hist = hist[len(hist)-600:]
	}
	f.history[asset] = hist
	f.histMu.Unlock()

	// Notify subscribers
	select {
	case f.subCh <- tick:
	default:
		// subscriber not keeping up — drop tick
	}

	return true
}

// LatestPrice returns the most recent price for an asset.
// Returns 0, false if no price has been fetched yet.
func (f *CoinbaseFeed) LatestPrice(asset Asset) (float64, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	tick, ok := f.latest[asset]
	if !ok || tick == nil {
		return 0, false
	}
	return tick.Price, true
}

// LatestTick returns the full PriceTick for an asset.
func (f *CoinbaseFeed) LatestTick(asset Asset) (*PriceTick, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	tick, ok := f.latest[asset]
	if !ok || tick == nil {
		return nil, false
	}
	cp := *tick
	return &cp, true
}

// PriceAtTime returns the price closest to the given timestamp for an asset.
// Useful for determining the "opening price" at the start of a 5M window.
func (f *CoinbaseFeed) PriceAtTime(asset Asset, target time.Time) (float64, bool) {
	f.histMu.RLock()
	defer f.histMu.RUnlock()

	hist := f.history[asset]
	if len(hist) == 0 {
		return 0, false
	}

	// Find the tick closest to target time
	bestIdx := 0
	bestDiff := absDuration(hist[0].Timestamp.Sub(target))
	for i := 1; i < len(hist); i++ {
		diff := absDuration(hist[i].Timestamp.Sub(target))
		if diff < bestDiff {
			bestDiff = diff
			bestIdx = i
		}
	}

	// Only accept if within 5 seconds of target
	if bestDiff > 5*time.Second {
		return 0, false
	}
	return hist[bestIdx].Price, true
}

// PriceChange returns the percentage change from the price at `since` to now.
// Positive means price went up. Returns 0, false if data unavailable.
func (f *CoinbaseFeed) PriceChange(asset Asset, since time.Time) (float64, bool) {
	openPrice, ok := f.PriceAtTime(asset, since)
	if !ok || openPrice == 0 {
		return 0, false
	}
	currentPrice, ok := f.LatestPrice(asset)
	if !ok {
		return 0, false
	}
	return (currentPrice - openPrice) / openPrice, true
}


// IsStale returns true if the feed hasn't received any successful price
// update in the last `threshold` duration. The strategy should avoid
// trading on stale data.
func (f *CoinbaseFeed) IsStale(threshold time.Duration) bool {
	f.failMu.Lock()
	defer f.failMu.Unlock()
	return time.Since(f.lastSuccessAt) > threshold
}

// ConsecutiveFails returns the number of consecutive failed poll cycles.
func (f *CoinbaseFeed) ConsecutiveFails() int {
	f.failMu.Lock()
	defer f.failMu.Unlock()
	return f.consecutiveFails
}
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
