// Package market provides local order book management and market discovery.
//
// Book mirrors the CLOB order book for a single binary market (YES + NO tokens).
// It is updated from two sources:
//   - REST snapshots via ApplyBookResponse (initial load)
//   - WebSocket events via ApplyBookEvent (full snapshots) and ApplyPriceChange
//     (incremental updates)
//
// The Book is concurrency-safe (RWMutex protected) and provides derived
// values like MidPrice and BestBidAsk for the strategy layer.
package market

import (
	"strconv"
	"sync"
	"time"

	"polymarket-mm/pkg/types"
)

// Book maintains a local mirror of the order book for one market.
// It tracks both the YES and NO token books, though the strategy primarily
// uses the YES book for quoting (NO book is kept for completeness).
type Book struct {
	mu       sync.RWMutex
	marketID string
	yesToken string                // YES token asset ID
	noToken  string                // NO token asset ID
	yes      types.OrderBookSnapshot // YES token order book (bids desc, asks asc)
	no       types.OrderBookSnapshot // NO token order book
	lastHash map[string]string     // latest book hash per asset (for staleness)
	updated  time.Time             // last time any book data arrived
}

// NewBook creates a new local order book for a market.
func NewBook(marketID, yesToken, noToken string) *Book {
	return &Book{
		marketID: marketID,
		yesToken: yesToken,
		noToken:  noToken,
		lastHash: make(map[string]string),
	}
}

// ApplyBookEvent replaces the book for one token with a full snapshot.
func (b *Book) ApplyBookEvent(event types.WSBookEvent) {
	b.applySnapshot(event.AssetID, event.Buys, event.Sells, event.Hash)
}

// ApplyBookResponse applies a REST API book response.
func (b *Book) ApplyBookResponse(resp *types.BookResponse) {
	b.applySnapshot(resp.AssetID, resp.Bids, resp.Asks, resp.Hash)
}

func (b *Book) applySnapshot(assetID string, bids, asks []types.PriceLevel, hash string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	snap := types.OrderBookSnapshot{
		AssetID:   assetID,
		Bids:      bids,
		Asks:      asks,
		Hash:      hash,
		Timestamp: time.Now(),
	}

	if assetID == b.yesToken {
		b.yes = snap
	} else if assetID == b.noToken {
		b.no = snap
	}

	b.lastHash[assetID] = hash
	b.updated = time.Now()
}

// ApplyPriceChange applies an incremental price_change event.
func (b *Book) ApplyPriceChange(event types.WSPriceChangeEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, pc := range event.PriceChanges {
		b.lastHash[pc.AssetID] = pc.Hash
	}
	b.updated = time.Now()
}

// MidPrice returns the mid price for the YES token, computed as
// (bestBid + bestAsk) / 2. Returns false if the book is empty on either side.
// This value becomes the "s" (reference price) in the A-S formula.
func (b *Book) MidPrice() (float64, bool) {
	bid, ask, ok := b.BestBidAsk()
	if !ok {
		return 0, false
	}
	if bid == 0 && ask == 0 {
		return 0, false
	}
	return (bid + ask) / 2, true
}

// BestBidAsk returns the best bid and ask for the YES token.
func (b *Book) BestBidAsk() (bid, ask float64, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.yes.Bids) == 0 || len(b.yes.Asks) == 0 {
		return 0, 0, false
	}

	return parsePrice(b.yes.Bids[0].Price), parsePrice(b.yes.Asks[0].Price), true
}

// IsStale returns true if the book hasn't been updated within maxAge.
func (b *Book) IsStale(maxAge time.Duration) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.updated.IsZero() {
		return true
	}
	return time.Since(b.updated) > maxAge
}

// LastUpdated returns the timestamp of the last book update.
func (b *Book) LastUpdated() time.Time {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.updated
}

func parsePrice(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
