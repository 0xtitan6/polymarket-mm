// Package sniper implements the 5-minute crypto market discovery and
// directional betting strategy.
//
// # How 5M Markets Work
//
// Polymarket runs recurring 5-minute markets for BTC, ETH, SOL, and XRP.
// Each market is a binary outcome: "Up" or "Down". The market resolves
// based on whether the Chainlink Data Stream price at the end of the
// 5-minute window is >= the price at the beginning.
//
// Event slugs follow the pattern: {asset}-updown-5m-{epoch}
// where epoch is the Unix timestamp of the window start, aligned to 300s.
//
// Token ordering: clobTokenIds[0] = Up, clobTokenIds[1] = Down
//
// # The Edge
//
// Exchange prices (Coinbase) LEAD Chainlink by 0.5-2 seconds because
// Chainlink aggregates FROM exchanges. By reading the Coinbase price
// near the end of the 5-minute window, we can predict which direction
// the Chainlink price will settle with >50% accuracy when there's a
// clear directional move.
//
// # Strategy
//
// 1. Track exchange prices from the start of each 5M window
// 2. At T-30s before close, compute price change since open
// 3. If abs(change) > threshold, the direction is highly likely:
//    - price up → buy "Up" token
//    - price down → buy "Down" token
// 4. Place aggressive LIMIT order at the ask (or slightly above mid)
// 5. If filled, wait for resolution and collect payout
package sniper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"polymarket-mm/internal/pricefeed"
)

// MarketWindow represents a single 5-minute crypto market.
type MarketWindow struct {
	Asset       pricefeed.Asset
	Epoch       int64  // Unix timestamp of window start (multiple of 300)
	EventSlug   string // e.g. "btc-updown-5m-1772844900"
	UpTokenID   string // Polymarket CLOB token for "Up" outcome
	DownTokenID string // Polymarket CLOB token for "Down" outcome

	// Prices at discovery time
	UpPrice   float64
	DownPrice float64

	// Window timing
	StartsAt time.Time
	EndsAt   time.Time

	// Exchange price at window open (Coinbase)
	OpenPrice float64
}

// Signal represents a directional trading signal.
type Signal struct {
	Market       *MarketWindow
	Direction    string  // "Up" or "Down"
	Confidence   float64 // 0-1, based on price move magnitude
	ExchangeMove float64 // percentage price change since open
	CurrentPrice float64 // current exchange price
	OpenPrice    float64 // exchange price at window open
	TokenID      string  // which token to buy
	SuggestedBid float64 // suggested limit price
	GeneratedAt  time.Time
}

// MarketDiscovery finds active 5M crypto markets on Polymarket
// via the Gamma API.
type MarketDiscovery struct {
	httpClient  *http.Client
	gammaBase   string
	logger      *slog.Logger
	assets      []pricefeed.Asset

	mu      sync.RWMutex
	current map[pricefeed.Asset]*MarketWindow // latest known window per asset
}

// gammaEventResp is the Gamma API event response shape.
type gammaEventResp struct {
	Title   string            `json:"title"`
	Slug    string            `json:"slug"`
	Markets []gammaMarketResp `json:"markets"`
}

type gammaMarketResp struct {
	Question      string `json:"question"`
	ConditionID   string `json:"conditionId"`
	Outcomes      string `json:"outcomes"`      // JSON-encoded: ["Up","Down"]
	OutcomePrices string `json:"outcomePrices"` // JSON-encoded: ["0.505","0.495"]
	ClobTokenIds  string `json:"clobTokenIds"`  // JSON-encoded array of token ID strings
	Active        bool   `json:"active"`
	Closed        bool   `json:"closed"`
}

// NewMarketDiscovery creates a new discovery service.
func NewMarketDiscovery(logger *slog.Logger, assets []pricefeed.Asset) *MarketDiscovery {
	if len(assets) == 0 {
		assets = pricefeed.AllAssets
	}
	return &MarketDiscovery{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		gammaBase:  "https://gamma-api.polymarket.com",
		logger:     logger.With("component", "market_discovery"),
		assets:     assets,
		current:    make(map[pricefeed.Asset]*MarketWindow),
	}
}

// DiscoverCurrent finds the currently active 5M market for each asset.
// Returns markets that are currently open (between start and end time).
func (d *MarketDiscovery) DiscoverCurrent(ctx context.Context) ([]*MarketWindow, error) {
	now := time.Now()
	epoch := now.Unix() - (now.Unix() % 300) // current 5-min boundary

	var results []*MarketWindow
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, asset := range d.assets {
		wg.Add(1)
		go func(a pricefeed.Asset) {
			defer wg.Done()
			mw, err := d.fetchMarket(ctx, a, epoch)
			if err != nil {
				d.logger.Debug("failed to fetch 5M market",
					"asset", a, "epoch", epoch, "error", err)
				return
			}
			mu.Lock()
			results = append(results, mw)
			mu.Unlock()
		}(asset)
	}
	wg.Wait()

	// Update cache
	d.mu.Lock()
	for _, mw := range results {
		d.current[mw.Asset] = mw
	}
	d.mu.Unlock()

	return results, nil
}

// DiscoverNext finds the NEXT 5M market window (one that hasn't started yet or
// just started). Useful for pre-positioning.
func (d *MarketDiscovery) DiscoverNext(ctx context.Context) ([]*MarketWindow, error) {
	now := time.Now()
	nextEpoch := now.Unix() - (now.Unix() % 300) + 300 // next boundary

	var results []*MarketWindow
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, asset := range d.assets {
		wg.Add(1)
		go func(a pricefeed.Asset) {
			defer wg.Done()
			mw, err := d.fetchMarket(ctx, a, nextEpoch)
			if err != nil {
				d.logger.Debug("failed to fetch next 5M market",
					"asset", a, "epoch", nextEpoch, "error", err)
				return
			}
			mu.Lock()
			results = append(results, mw)
			mu.Unlock()
		}(asset)
	}
	wg.Wait()

	return results, nil
}

// CurrentMarket returns the cached current market for an asset.
func (d *MarketDiscovery) CurrentMarket(asset pricefeed.Asset) (*MarketWindow, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	mw, ok := d.current[asset]
	return mw, ok
}

// fetchMarket fetches a specific 5M market from the Gamma API.
func (d *MarketDiscovery) fetchMarket(ctx context.Context, asset pricefeed.Asset, epoch int64) (*MarketWindow, error) {
	slug := fmt.Sprintf("%s-updown-5m-%d", strings.ToLower(string(asset)), epoch)
	url := fmt.Sprintf("%s/events?slug=%s", d.gammaBase, slug)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gamma API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gamma API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var events []gammaEventResp
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}

	if len(events) == 0 {
		return nil, fmt.Errorf("no event found for slug %s", slug)
	}

	event := events[0]
	if len(event.Markets) == 0 {
		return nil, fmt.Errorf("event %s has no markets", slug)
	}

	m := event.Markets[0]

	// Parse JSON-encoded arrays
	var tokenIDs []string
	if err := json.Unmarshal([]byte(m.ClobTokenIds), &tokenIDs); err != nil {
		return nil, fmt.Errorf("parse clobTokenIds: %w", err)
	}
	if len(tokenIDs) < 2 {
		return nil, fmt.Errorf("expected 2 token IDs, got %d", len(tokenIDs))
	}

	var outcomes []string
	if err := json.Unmarshal([]byte(m.Outcomes), &outcomes); err != nil {
		return nil, fmt.Errorf("parse outcomes: %w", err)
	}

	var prices []string
	if err := json.Unmarshal([]byte(m.OutcomePrices), &prices); err != nil {
		return nil, fmt.Errorf("parse outcomePrices: %w", err)
	}

	// Determine which index is Up and which is Down
	upIdx, downIdx := 0, 1
	for i, o := range outcomes {
		switch o {
		case "Up":
			upIdx = i
		case "Down":
			downIdx = i
		}
	}

	var upPrice, downPrice float64
	if upIdx < len(prices) {
		fmt.Sscanf(prices[upIdx], "%f", &upPrice)
	}
	if downIdx < len(prices) {
		fmt.Sscanf(prices[downIdx], "%f", &downPrice)
	}

	startTime := time.Unix(epoch, 0)
	endTime := startTime.Add(5 * time.Minute)

	return &MarketWindow{
		Asset:       asset,
		Epoch:       epoch,
		EventSlug:   slug,
		UpTokenID:   tokenIDs[upIdx],
		DownTokenID: tokenIDs[downIdx],
		UpPrice:     upPrice,
		DownPrice:   downPrice,
		StartsAt:    startTime,
		EndsAt:      endTime,
	}, nil
}

// TimeToClose returns how many seconds until the current 5M window ends.
func TimeToClose() time.Duration {
	now := time.Now()
	epoch := now.Unix() - (now.Unix() % 300)
	endTime := time.Unix(epoch+300, 0)
	return endTime.Sub(now)
}

// CurrentEpoch returns the current 5-minute boundary epoch.
func CurrentEpoch() int64 {
	now := time.Now().Unix()
	return now - (now % 300)
}

// NextEpoch returns the next 5-minute boundary epoch.
func NextEpoch() int64 {
	return CurrentEpoch() + 300
}
