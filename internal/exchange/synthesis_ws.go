// Package exchange — SynthesisWSFeed is a polling-based market data feed for
// the Synthesis Trade bot.
//
// # Architecture: Dual-Source Data Feed
//
// Market data (order books) is polled from the Polymarket public CLOB API
// (https://clob.polymarket.com) which requires NO authentication. Order status
// (fills, cancels) is polled from the Synthesis Trade API using the API key.
//
// This dual-source approach is necessary because:
//   - Synthesis has no public market data endpoints
//   - Synthesis GET /orders returns only YOUR wallet's orders, not the full book
//   - Polymarket's CLOB API exposes the real order book for free
//
// Data flow:
//
//	BookEvents()  ← Polymarket CLOB: GET /book?token_id=X (public, no auth)
//	OrderEvents() ← Synthesis API:   GET /wallet/{id}/orders (private, X-API-KEY)
//
// # Upgrade path
//
// If Polymarket adds WebSocket rate limits or Synthesis adds market data
// endpoints, this file can be updated without changing the engine wiring.
// The interface (channel accessors + Run/Subscribe/Unsubscribe/Close) is
// identical to ws.go.
//
// # Interface compatibility
//
// SynthesisWSFeed exposes the same channel accessor methods as WSFeed:
//
//	BookEvents()         → <-chan types.WSBookEvent
//	OrderEvents()        → <-chan types.WSOrderEvent
//	PriceChangeEvents()  → <-chan types.WSPriceChangeEvent  (nil — always blocking)
//	TradeEvents()        → <-chan types.WSTradeEvent         (nil — always blocking)
//	USBookEvents()       → <-chan types.USWSBookEvent
//	USOrderEvents()      → <-chan types.USWSPrivateEvent
//
// The engine selects on BookEvents() and OrderEvents() — both are populated by
// the polling goroutines. The nil channels block silently, which is the expected
// behavior (no separate price-change or trade event stream).
package exchange

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"polymarket-mm/pkg/types"
)

const (
	// synthesisDefaultPollInterval is how often the book and order pollers fire.
	// Set conservatively to avoid hammering the Synthesis API with unauthenticated
	// load. Tune lower (e.g. 500ms) once rate limits are confirmed.
	synthesisDefaultPollInterval = 2 * time.Second

	// synthesisOrderPollInterval is how often open orders are polled.
	// Order status changes less frequently than book updates, so a slower
	// poll keeps the request load manageable.
	synthesisOrderPollInterval = 5 * time.Second
)

// SynthesisWSFeed is a polling-based replacement for the WebSocket-based WSFeed.
// It provides the same channel-oriented interface as WSFeed so the engine wiring
// is identical.
//
// Two goroutines are started by Run():
//  1. bookPoller   — polls Polymarket CLOB /book for subscribed token_ids
//  2. orderPoller  — polls Synthesis /wallet/{id}/orders for fill/status events
//
// Architecture: reads market data from Polymarket public CLOB API,
// while order status is polled from Synthesis (your own orders).
type SynthesisWSFeed struct {
	client    *SynthesisClient
	polyCLOB  *PolymarketCLOB // public market data source
	logger    *slog.Logger

	// Subscribed market slugs (guarded by subscribedMu)
	subscribedMu sync.RWMutex
	subscribed   map[string]bool

	// Poll intervals — configurable for testing
	bookPollInterval  time.Duration
	orderPollInterval time.Duration

	// Event channels — consumers read from these via accessor methods.
	// Same buffer sizes as WSFeed to avoid backpressure surprises.
	bookCh    chan types.USWSBookEvent    // polled book snapshots
	orderCh   chan types.USWSPrivateEvent // polled order status

	// Legacy channels (translated from the US API types above)
	legacyBookCh  chan types.WSBookEvent
	legacyOrderCh chan types.WSOrderEvent
}

// NewSynthesisMarketFeed creates a SynthesisWSFeed that polls market book data
// from the Polymarket public CLOB API.
// This is the synthesis equivalent of NewMarketFeed from ws.go.
func NewSynthesisMarketFeed(client *SynthesisClient, polyCLOB *PolymarketCLOB, logger *slog.Logger) *SynthesisWSFeed {
	return newSynthesisWSFeed(client, polyCLOB, logger)
}

// NewSynthesisUserFeed creates a SynthesisWSFeed that polls private order events
// from the Synthesis Trade API.
// This is the synthesis equivalent of NewUserFeed / NewPrivateFeed from ws.go.
// In the polling implementation both market and user data come from the same feed
// object — the single goroutine pair handles both.
func NewSynthesisUserFeed(client *SynthesisClient, polyCLOB *PolymarketCLOB, logger *slog.Logger) *SynthesisWSFeed {
	return newSynthesisWSFeed(client, polyCLOB, logger)
}

func newSynthesisWSFeed(client *SynthesisClient, polyCLOB *PolymarketCLOB, logger *slog.Logger) *SynthesisWSFeed {
	return &SynthesisWSFeed{
		client:            client,
		polyCLOB:          polyCLOB,
		logger:            logger.With("component", "synthesis_ws"),
		subscribed:        make(map[string]bool),
		bookPollInterval:  synthesisDefaultPollInterval,
		orderPollInterval: synthesisOrderPollInterval,
		bookCh:            make(chan types.USWSBookEvent, wsBookBufferSize),
		orderCh:           make(chan types.USWSPrivateEvent, wsPrivateBufferSize),
		legacyBookCh:      make(chan types.WSBookEvent, wsBookBufferSize),
		legacyOrderCh:     make(chan types.WSOrderEvent, wsPrivateBufferSize),
	}
}

// ————————————————————————————————————————————————————————————————————————
// Channel accessors — identical signatures to WSFeed
// ————————————————————————————————————————————————————————————————————————

// USBookEvents returns the US API-typed book event channel.
func (f *SynthesisWSFeed) USBookEvents() <-chan types.USWSBookEvent { return f.bookCh }

// USOrderEvents returns the US API-typed private event channel.
func (f *SynthesisWSFeed) USOrderEvents() <-chan types.USWSPrivateEvent { return f.orderCh }

// BookEvents returns the legacy-typed book event channel for the engine layer.
func (f *SynthesisWSFeed) BookEvents() <-chan types.WSBookEvent { return f.legacyBookCh }

// OrderEvents returns the legacy-typed order event channel for the engine layer.
func (f *SynthesisWSFeed) OrderEvents() <-chan types.WSOrderEvent { return f.legacyOrderCh }

// PriceChangeEvents returns nil — incremental price-change events are not
// emitted by the polling implementation. The engine's select will block on
// this channel forever, which is the correct behavior.
func (f *SynthesisWSFeed) PriceChangeEvents() <-chan types.WSPriceChangeEvent { return nil }

// TradeEvents returns nil — trade events are synthesized from order fill
// events on OrderEvents(). This matches the ws.go behavior.
func (f *SynthesisWSFeed) TradeEvents() <-chan types.WSTradeEvent { return nil }

// ————————————————————————————————————————————————————————————————————————
// Lifecycle: Run / Subscribe / Unsubscribe / Close
// ————————————————————————————————————————————————————————————————————————

// Run starts the polling goroutines and blocks until ctx is cancelled.
// Equivalent to WSFeed.Run in the engine wiring.
func (f *SynthesisWSFeed) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	// Book poller
	wg.Add(1)
	go func() {
		defer wg.Done()
		f.runBookPoller(ctx)
	}()

	// Order poller
	wg.Add(1)
	go func() {
		defer wg.Done()
		f.runOrderPoller(ctx)
	}()

	wg.Wait()
	return ctx.Err()
}

// Subscribe adds market slugs to the polling subscription set.
// The book poller will start fetching these slugs on the next tick.
// The ctx parameter is accepted for interface compatibility but is not stored.
func (f *SynthesisWSFeed) Subscribe(_ context.Context, slugs []string) error {
	f.subscribedMu.Lock()
	defer f.subscribedMu.Unlock()
	for _, s := range slugs {
		f.subscribed[s] = true
	}
	f.logger.Debug("synthesis feed: subscribed", "slugs", slugs)
	return nil
}

// Unsubscribe removes market slugs from the polling subscription set.
func (f *SynthesisWSFeed) Unsubscribe(_ context.Context, slugs []string) error {
	f.subscribedMu.Lock()
	defer f.subscribedMu.Unlock()
	for _, s := range slugs {
		delete(f.subscribed, s)
	}
	f.logger.Debug("synthesis feed: unsubscribed", "slugs", slugs)
	return nil
}

// Close is a no-op for the polling implementation — context cancellation
// is the shutdown mechanism. Implemented for interface compatibility with WSFeed.
func (f *SynthesisWSFeed) Close() error {
	return nil
}

// ————————————————————————————————————————————————————————————————————————
// Polling goroutines
// ————————————————————————————————————————————————————————————————————————

// runBookPoller polls order book snapshots for all subscribed slugs at a
// fixed interval. Each snapshot is translated and dispatched to both the
// US-typed and legacy channels.
func (f *SynthesisWSFeed) runBookPoller(ctx context.Context) {
	ticker := time.NewTicker(f.bookPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.pollBooks(ctx)
		}
	}
}

// runOrderPoller polls open order status at a fixed interval and emits
// order events when the status of a known order changes.
func (f *SynthesisWSFeed) runOrderPoller(ctx context.Context) {
	ticker := time.NewTicker(f.orderPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.pollOrders(ctx)
		}
	}
}

// pollBooks fetches the real order book from the Polymarket public CLOB API
// for each subscribed token_id and emits book events to the engine.
//
// Architecture: market data is read from Polymarket (public, no auth),
// while orders are placed/cancelled through Synthesis (private, API key).
func (f *SynthesisWSFeed) pollBooks(ctx context.Context) {
	f.subscribedMu.RLock()
	slugs := make([]string, 0, len(f.subscribed))
	for s := range f.subscribed {
		slugs = append(slugs, s)
	}
	f.subscribedMu.RUnlock()

	for _, slug := range slugs {
		// Use a short per-request deadline to avoid blocking the poll loop
		fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		book, err := f.polyCLOB.GetBook(fetchCtx, slug)
		cancel()

		if err != nil {
			f.logger.Debug("polymarket clob book poll failed", "token_id", slug, "error", err)
			continue
		}

		// Convert Polymarket CLOB book to USWSBookEvent
		evt := polyCLOBBookToWSEvent(book, slug)

		// Emit to US-typed channel
		select {
		case f.bookCh <- evt:
		default:
			f.logger.Warn("synthesis book channel full, dropping", "token_id", slug)
		}

		// Emit to legacy channel
		legacy := usWSBookToLegacy(evt)
		select {
		case f.legacyBookCh <- legacy:
		default:
			f.logger.Warn("synthesis legacy book channel full, dropping", "token_id", slug)
		}
	}
}

// pollOrders fetches open orders and emits a USWSPrivateEvent for each.
// This lets the Maker track fill status without a true WebSocket feed.
func (f *SynthesisWSFeed) pollOrders(ctx context.Context) {
	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	openOrders, err := f.client.GetOpenOrders(fetchCtx, nil)
	if err != nil {
		f.logger.Debug("synthesis order poll failed", "error", err)
		return
	}

	for _, o := range openOrders {
		evt := usOpenOrderToWSEvent(o)
		select {
		case f.orderCh <- evt:
		default:
			f.logger.Warn("synthesis order channel full, dropping", "id", o.ID)
		}

		legacy := usWSOrderToLegacy(evt)
		select {
		case f.legacyOrderCh <- legacy:
		default:
			f.logger.Warn("synthesis legacy order channel full, dropping", "id", o.ID)
		}
	}
}

// ————————————————————————————————————————————————————————————————————————
// Translation helpers for polling
// ————————————————————————————————————————————————————————————————————————


// ————————————————————————————————————————————————————————————————————————
// Sorting helpers for synthetic book levels
// ————————————————————————————————————————————————————————————————————————

// sortUSBookLevelsBids sorts bid levels descending by price (best bid first).
func sortUSBookLevelsBids(levels []types.USBookLevel) {
	for i := 1; i < len(levels); i++ {
		cur := levels[i]
		var curPx float64
		_, _ = fmt.Sscanf(cur.Px.Value, "%f", &curPx)
		j := i - 1
		for j >= 0 {
			var jPx float64
			_, _ = fmt.Sscanf(levels[j].Px.Value, "%f", &jPx)
			if jPx >= curPx {
				break
			}
			levels[j+1] = levels[j]
			j--
		}
		levels[j+1] = cur
	}
}

// sortUSBookLevelsAsks sorts ask levels ascending by price (best ask first).
func sortUSBookLevelsAsks(levels []types.USBookLevel) {
	for i := 1; i < len(levels); i++ {
		cur := levels[i]
		var curPx float64
		_, _ = fmt.Sscanf(cur.Px.Value, "%f", &curPx)
		j := i - 1
		for j >= 0 {
			var jPx float64
			_, _ = fmt.Sscanf(levels[j].Px.Value, "%f", &jPx)
			if jPx <= curPx {
				break
			}
			levels[j+1] = levels[j]
			j--
		}
		levels[j+1] = cur
	}
}
// usBookResponseToWSEvent converts a USBookResponse into a USWSBookEvent,
// using the same field layout that ws.go uses for real WS messages.
func usBookResponseToWSEvent(r *types.USBookResponse) types.USWSBookEvent {
	md := r.MarketData
	return types.USWSBookEvent{
		Payload: types.USWSMarketPayload{
			MarketSlug:   md.MarketSlug,
			Bids:         md.Bids,
			Offers:       md.Offers,
			State:        md.State,
			Stats:        md.Stats,
			TransactTime: md.TransactTime,
		},
	}
}

// usOpenOrderToWSEvent converts a USOpenOrder to a USWSPrivateEvent so the
// order poller can reuse the existing private-event dispatch path.
func usOpenOrderToWSEvent(o types.USOpenOrder) types.USWSPrivateEvent {
	// Map OrderState back to an ExecutionType for the private event
	execType := types.ExecutionTypeNew
	switch o.Status {
	case types.OrderStatePartiallyFilled, types.OrderStateFilled:
		execType = types.ExecutionTypeFill
	case types.OrderStateCanceled:
		execType = types.ExecutionTypeCanceled
	case types.OrderStateExpired:
		execType = types.ExecutionTypeExpired
	case types.OrderStateRejected:
		execType = types.ExecutionTypeRejected
	}

	return types.USWSPrivateEvent{
		Order: types.USWSOrderObject{
			ID:             o.ID,
			MarketSlug:     o.MarketSlug,
			Type:           o.Type,
			Price:          o.Price,
			Quantity:       o.Quantity,
			CumQuantity:    o.CumQuantity,
			LeavesQuantity: o.LeavesQuantity,
			Status:         o.Status,
		},
		Execution: types.USWSOrderExecution{
			Type:  execType,
			Price: o.Price,
		},
	}
}
