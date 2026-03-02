// Package engine is the central orchestrator of the market-making bot.
//
// It wires together all subsystems:
//
//  1. Scanner discovers wide-spread markets on Polymarket US.
//  2. Engine starts/stops a strategy goroutine per market (reconcileMarkets).
//  3. Each market gets: a Book (order book mirror), an Inventory (position tracker),
//     and a Maker (the Avellaneda-Stoikov strategy that quotes bid/ask).
//  4. Two WebSocket feeds (market data + private fills) dispatch events to the correct market slot.
//  5. Risk manager monitors all markets and can trigger a kill switch.
//
// Key difference from the old Polygon-based engine: markets are identified by
// slug (not conditionID/tokenID). The slug is used everywhere — WS subscriptions,
// slot map keys, order placement, and persistence.
//
// Lifecycle: New() → Start() → [runs until SIGINT] → Stop()
package engine

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"polymarket-mm/internal/api"
	"polymarket-mm/internal/config"
	"polymarket-mm/internal/exchange"
	"polymarket-mm/internal/market"
	"polymarket-mm/internal/risk"
	"polymarket-mm/internal/store"
	"polymarket-mm/internal/strategy"
	"polymarket-mm/pkg/types"
)

// marketSlot represents one actively-traded market.
// Each slot runs a dedicated goroutine (maker.Run) with its own book and inventory.
type marketSlot struct {
	info      types.MarketInfo
	book      *market.Book
	inventory *strategy.Inventory
	maker     *strategy.Maker
	cancel    context.CancelFunc
	tradeCh   chan types.WSTradeEvent
	orderCh   chan types.WSOrderEvent
}

// Engine orchestrates all components of the market-making system.
// It owns the lifecycle of all goroutines and manages market start/stop transitions.
type Engine struct {
	cfg     config.Config
	client  *exchange.Client
	auth    *exchange.Auth
	mktFeed *exchange.WSFeed
	usrFeed *exchange.WSFeed
	scanner *market.Scanner
	riskMgr *risk.Manager
	store   *store.Store
	logger  *slog.Logger

	// slots maps slug → running market. Protected by slotsMu.
	// In the US API, the market slug is the universal identifier (replaces conditionID).
	slots   map[string]*marketSlot
	slotsMu sync.RWMutex

	// slugMap maps slug → slug (identity) for WS event routing.
	// In the US API, WS events are keyed by market_slug, which is also
	// the slot key. This map is kept for structural parity with the old
	// token→condition routing, but is effectively an identity mapping.
	slugMap   map[string]string
	slugMapMu sync.RWMutex

	// dashboardEvents is an optional channel for sending events to the dashboard.
	// Nil if dashboard is disabled.
	dashboardEvents chan api.DashboardEvent

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates and wires all engine components.
// Ed25519 credentials must be configured (no key derivation step on the US API).
func New(cfg config.Config, logger *slog.Logger) (*Engine, error) {
	auth, err := exchange.NewAuth(cfg)
	if err != nil {
		return nil, err
	}

	client := exchange.NewClient(cfg, auth, logger)

	// The US API uses static Ed25519 keys — no derivation needed.
	// HasL2Credentials always returns true, but handle gracefully just in case.
	if !auth.HasL2Credentials() {
		logger.Warn("Ed25519 credentials not configured — bot will not be able to place orders")
	}

	// Market feed: authenticated via upgrade handshake headers
	mktFeed := exchange.NewMarketFeed(cfg.API.WSMarketURL, auth, logger)
	// Private feed: authenticated via upgrade handshake headers
	usrFeed := exchange.NewUserFeed(cfg.API.WSPrivateURL, auth, logger)
	// If WSPrivateURL is empty, fall back to WSUserURL (compatibility alias)
	if cfg.API.WSPrivateURL == "" && cfg.API.WSUserURL != "" {
		usrFeed = exchange.NewUserFeed(cfg.API.WSUserURL, auth, logger)
	}

	scanner := market.NewScanner(client, cfg, logger)
	riskMgr := risk.NewManager(cfg.Risk, logger)

	st, err := store.Open(cfg.Store.DataDir)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	var dashEvents chan api.DashboardEvent
	if cfg.Dashboard.Enabled {
		dashEvents = make(chan api.DashboardEvent, 100)
	}

	return &Engine{
		cfg:             cfg,
		client:          client,
		auth:            auth,
		mktFeed:         mktFeed,
		usrFeed:         usrFeed,
		scanner:         scanner,
		riskMgr:         riskMgr,
		store:           st,
		logger:          logger.With("component", "engine"),
		slots:           make(map[string]*marketSlot),
		slugMap:         make(map[string]string),
		dashboardEvents: dashEvents,
		ctx:             ctx,
		cancel:          cancel,
	}, nil
}

// Start launches all background goroutines: WS feeds, scanner, risk manager,
// event dispatchers, and the main market management loop.
func (e *Engine) Start() error {
	// Start WebSocket feeds
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := e.mktFeed.Run(e.ctx); err != nil && e.ctx.Err() == nil {
			e.logger.Error("market feed error", "error", err)
		}
	}()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := e.usrFeed.Run(e.ctx); err != nil && e.ctx.Err() == nil {
			e.logger.Error("private feed error", "error", err)
		}
	}()

	// Start scanner
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.scanner.Run(e.ctx)
	}()

	// Start risk manager
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.riskMgr.Run(e.ctx)
	}()

	// Start WS event dispatchers
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.dispatchMarketEvents()
	}()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.dispatchUserEvents()
	}()

	// Start main engine loop
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.manageMarkets()
	}()

	return nil
}

// Stop gracefully shuts down: cancels all contexts, sends a cancel-all to the exchange
// as a safety net, persists final positions, waits for goroutines, and closes resources.
func (e *Engine) Stop() {
	e.logger.Info("shutting down...")

	// Cancel all contexts (stops all goroutines)
	e.cancel()

	// Safety net: cancel all orders on the exchange
	cancelCtx, cancelCancel := context.WithTimeout(context.Background(), e.cfg.Strategy.StaleBookTimeout)
	defer cancelCancel()
	if _, err := e.client.CancelAll(cancelCtx); err != nil {
		e.logger.Error("failed to cancel all orders on shutdown", "error", err)
	}

	// Persist final positions
	e.slotsMu.RLock()
	for id, slot := range e.slots {
		pos := slot.inventory.Snapshot()
		if err := e.store.SavePosition(id, pos); err != nil {
			e.logger.Error("failed to save position", "market", id, "error", err)
		}
	}
	e.slotsMu.RUnlock()

	// Wait for all goroutines
	e.wg.Wait()

	// Close resources
	e.mktFeed.Close()
	e.usrFeed.Close()
	e.store.Close()

	e.logger.Info("shutdown complete")
}

// manageMarkets is the main engine loop. It reacts to two events:
// - Scanner results: start/stop markets to match the latest opportunity set.
// - Kill signals from the risk manager: immediately stop affected markets.
func (e *Engine) manageMarkets() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case result := <-e.scanner.Results():
			e.reconcileMarkets(result)
		case kill := <-e.riskMgr.KillCh():
			e.handleKillSignal(kill)
		}
	}
}

// reconcileMarkets diffs the desired market set (from scanner) against currently
// running markets. Stops markets no longer desired, starts newly discovered ones.
// Markets are keyed by slug (which equals ConditionID in the US API architecture).
func (e *Engine) reconcileMarkets(result market.ScanResult) {
	desired := make(map[string]types.MarketAllocation)
	for _, alloc := range result.Markets {
		desired[alloc.Market.Slug] = alloc
	}

	e.slotsMu.Lock()
	defer e.slotsMu.Unlock()

	// Stop markets no longer desired
	for id := range e.slots {
		if _, ok := desired[id]; !ok {
			e.stopMarketLocked(id)
		}
	}

	// Start new markets
	for id, alloc := range desired {
		if _, ok := e.slots[id]; !ok {
			e.startMarketLocked(alloc)
		}
	}
}

func (e *Engine) startMarketLocked(alloc types.MarketAllocation) {
	info := alloc.Market
	slug := info.Slug
	if slug == "" {
		e.logger.Warn("skipping market with missing slug")
		return
	}

	// Safety: cancel any pre-existing resting orders for this market.
	reconcileCtx, cancelReconcile := context.WithTimeout(e.ctx, 10*time.Second)
	_, err := e.client.CancelMarketOrders(reconcileCtx, slug)
	cancelReconcile()
	if err != nil {
		e.logger.Error("startup order reconciliation failed, skipping market",
			"slug", slug,
			"error", err,
		)
		return
	}

	// In the US API, the slug serves as the book identifier (replacing token IDs).
	// Both YES and NO are represented by the same single-instrument slug.
	book := market.NewBook(slug, slug, slug)
	inv := strategy.NewInventory(slug, slug, slug)

	// Restore position from persistence
	if pos, err := e.store.LoadPosition(slug); err == nil && pos != nil {
		inv.SetPosition(*pos)
	}

	tradeCh := make(chan types.WSTradeEvent, 64)
	orderCh := make(chan types.WSOrderEvent, 64)

	maker := strategy.NewMaker(
		e.cfg.Strategy,
		info,
		book,
		inv,
		e.client,
		e.riskMgr,
		e.logger,
		e.dashboardEvents,
		e.store.SavePosition,
	)

	ctx, cancel := context.WithCancel(e.ctx)

	slot := &marketSlot{
		info:      info,
		book:      book,
		inventory: inv,
		maker:     maker,
		cancel:    cancel,
		tradeCh:   tradeCh,
		orderCh:   orderCh,
	}

	e.slots[slug] = slot

	// Register slug in the routing map (identity mapping for US API)
	e.slugMapMu.Lock()
	e.slugMap[slug] = slug
	e.slugMapMu.Unlock()

	// Subscribe to market data WS for this slug
	e.mktFeed.Subscribe(ctx, []string{slug})
	// Private feed subscribes globally (no per-market subscription needed)
	e.usrFeed.Subscribe(ctx, []string{slug})

	// Fetch initial order book snapshot
	resp, err := e.client.GetOrderBook(ctx, slug)
	if err != nil {
		e.logger.Error("failed to get initial book", "slug", slug, "error", err)
	} else {
		book.ApplyBookResponse(resp)
	}

	// Start strategy goroutine
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		maker.Run(ctx, tradeCh, orderCh)
	}()

	e.logger.Info("market started",
		"slug", slug,
		"spread", info.Spread,
		"score", alloc.Score,
	)
}

func (e *Engine) stopMarketLocked(slug string) {
	slot, ok := e.slots[slug]
	if !ok {
		return
	}

	// Cancel goroutine (maker.Run will cancel its own orders)
	slot.cancel()

	// Save position
	pos := slot.inventory.Snapshot()
	if err := e.store.SavePosition(slug, pos); err != nil {
		e.logger.Error("failed to save position on stop", "market", slug, "error", err)
	}

	// Unsubscribe WS
	e.mktFeed.Unsubscribe(e.ctx, []string{slug})
	e.usrFeed.Unsubscribe(e.ctx, []string{slug})

	// Clean up risk state
	e.riskMgr.RemoveMarket(slug)

	// Clean up slug map
	e.slugMapMu.Lock()
	delete(e.slugMap, slug)
	e.slugMapMu.Unlock()

	delete(e.slots, slug)

	e.logger.Info("market stopped", "slug", slot.info.Slug)
}

func (e *Engine) handleKillSignal(kill risk.KillSignal) {
	e.logger.Error("KILL SIGNAL received",
		"market", kill.MarketID,
		"reason", kill.Reason,
	)

	// Emit kill event to dashboard
	e.emitDashboardEvent(api.DashboardEvent{
		Type:      "kill",
		Timestamp: time.Now(),
		MarketID:  kill.MarketID,
		Data: api.NewKillEvent(
			kill.Reason,
			kill.Reason,
			time.Now().Add(e.cfg.Risk.CooldownAfterKill),
			kill.MarketID,
		),
	})

	e.slotsMu.Lock()
	defer e.slotsMu.Unlock()

	if kill.MarketID == "" {
		// Kill all markets
		for id := range e.slots {
			e.stopMarketLocked(id)
		}
		// Also cancel-all as safety net
		cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, err := e.client.CancelAll(cancelCtx); err != nil {
			e.logger.Error("failed to cancel all orders", "error", err)
		}
		cancelCancel()
	} else {
		e.stopMarketLocked(kill.MarketID)
	}
}

// dispatchMarketEvents routes WS market events to the correct slot's Book.
// In the US API, book events are keyed by market_slug (which is our slot key).
// PriceChangeEvents returns a nil channel (US API sends full snapshots),
// so that select branch never fires.
func (e *Engine) dispatchMarketEvents() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case evt := <-e.mktFeed.BookEvents():
			e.routeBookEvent(evt)
		}
	}
}

func (e *Engine) routeBookEvent(evt types.WSBookEvent) {
	// In the US API, AssetID is the market slug (set by legacy translation).
	slug := evt.AssetID

	e.slotsMu.RLock()
	slot, ok := e.slots[slug]
	e.slotsMu.RUnlock()
	if !ok {
		return
	}

	slot.book.ApplyBookEvent(evt)
}

// dispatchUserEvents routes WS private events to the correct slot's channels.
// In the US API, order/fill events arrive on OrderEvents() keyed by market_slug.
// TradeEvents() returns a nil channel (fills come as EXECUTION_TYPE_FILL on the
// order channel), so that branch never fires.
func (e *Engine) dispatchUserEvents() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case order := <-e.usrFeed.OrderEvents():
			e.routeOrder(order)
		}
	}
}

func (e *Engine) routeOrder(order types.WSOrderEvent) {
	// In the US API, Market is the market_slug.
	e.slotsMu.RLock()
	slot, ok := e.slots[order.Market]
	e.slotsMu.RUnlock()
	if !ok {
		return
	}

	select {
	case slot.orderCh <- order:
	default:
		e.logger.Warn("order channel full", "market", order.Market)
	}
}

// DashboardEvents returns the dashboard event channel (may be nil).
func (e *Engine) DashboardEvents() <-chan api.DashboardEvent {
	return e.dashboardEvents
}

// GetMarketsSnapshot returns current state of all active markets for dashboard.
func (e *Engine) GetMarketsSnapshot() []api.MarketStatus {
	e.slotsMu.RLock()
	defer e.slotsMu.RUnlock()

	result := make([]api.MarketStatus, 0, len(e.slots))
	for _, slot := range e.slots {
		mid, midOk := slot.book.MidPrice()
		bid, ask, bookOk := slot.book.BestBidAsk()

		var spread, spreadBps float64
		if bookOk {
			spread = ask - bid
			if mid > 0 {
				spreadBps = (spread / mid) * 10000
			}
		}

		pos := slot.inventory.Snapshot()
		lastUpdated := slot.book.LastUpdated()
		isStale := slot.book.IsStale(e.cfg.Strategy.StaleBookTimeout)

		// Convert position to dashboard format
		var unrealizedPnL float64
		if midOk {
			unrealizedPnL = pos.YesQty*(mid-pos.AvgEntryYes) + pos.NoQty*((1-mid)-pos.AvgEntryNo)
		}

		posSnapshot := api.PositionSnapshot{
			YesQty:        pos.YesQty,
			NoQty:         pos.NoQty,
			AvgEntryYes:   pos.AvgEntryYes,
			AvgEntryNo:    pos.AvgEntryNo,
			RealizedPnL:   pos.RealizedPnL,
			UnrealizedPnL: unrealizedPnL,
			ExposureUSD:   slot.inventory.TotalExposureUSD(mid),
			Skew:          slot.inventory.NetDelta(),
			LastUpdated:   pos.LastUpdated,
		}

		status := api.MarketStatus{
			ConditionID:      slot.info.Slug, // slug is the primary ID on US API
			Slug:             slot.info.Slug,
			Question:         slot.info.Question,
			MidPrice:         mid,
			BestBid:          bid,
			BestAsk:          ask,
			Spread:           spread,
			SpreadBps:        spreadBps,
			LastUpdated:      lastUpdated,
			IsStale:          isStale,
			Position:         posSnapshot,
			ReservationPrice: 0, // Will be filled by maker
			OptimalSpread:    0, // Will be filled by maker
			TickSize:         parseTickSize(slot.info.TickSize),
			EndDate:          slot.info.EndDate,
			Liquidity:        slot.info.Liquidity,
			Volume24h:        slot.info.Volume24h,
		}

		result = append(result, status)
	}

	return result
}

// GetScanner returns the scanner for dashboard access.
func (e *Engine) GetScanner() *market.Scanner {
	return e.scanner
}

// GetRiskManager returns the risk manager for dashboard access.
func (e *Engine) GetRiskManager() *risk.Manager {
	return e.riskMgr
}

// emitDashboardEvent sends an event to the dashboard (non-blocking).
func (e *Engine) emitDashboardEvent(evt api.DashboardEvent) {
	if e.dashboardEvents == nil {
		return
	}

	select {
	case e.dashboardEvents <- evt:
	default:
		// Dashboard can't keep up, drop event
	}
}

// parseTickSize converts TickSize string to float64
func parseTickSize(ts types.TickSize) float64 {
	switch ts {
	case types.Tick01:
		return 0.1
	case types.Tick001:
		return 0.01
	case types.Tick0001:
		return 0.001
	case types.Tick00001:
		return 0.0001
	default:
		return 0.01 // default to 0.01
	}
}
