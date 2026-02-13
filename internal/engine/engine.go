// Package engine is the central orchestrator of the market-making bot.
//
// It wires together all subsystems:
//
//  1. Scanner discovers wide-spread markets on Polymarket.
//  2. Engine starts/stops a strategy goroutine per market (reconcileMarkets).
//  3. Each market gets: a Book (order book mirror), an Inventory (position tracker),
//     and a Maker (the Avellaneda-Stoikov strategy that quotes bid/ask).
//  4. Two WebSocket feeds (market data + user fills) dispatch events to the correct market slot.
//  5. Risk manager monitors all markets and can trigger a kill switch.
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

	// slots maps conditionID → running market. Protected by slotsMu.
	slots   map[string]*marketSlot
	slotsMu sync.RWMutex

	// tokenMap maps tokenID → conditionID so WS market events (keyed by token)
	// can be routed to the correct market slot (keyed by condition).
	tokenMap   map[string]string
	tokenMapMu sync.RWMutex

	// dashboardEvents is an optional channel for sending events to the dashboard.
	// Nil if dashboard is disabled.
	dashboardEvents chan api.DashboardEvent

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates and wires all engine components.
// If L2 API credentials aren't configured, it derives them via L1 (EIP-712) auth.
func New(cfg config.Config, logger *slog.Logger) (*Engine, error) {
	auth, err := exchange.NewAuth(cfg)
	if err != nil {
		return nil, err
	}

	client := exchange.NewClient(cfg, auth, logger)

	// Derive API key if not provided
	if !auth.HasL2Credentials() {
		logger.Info("no L2 credentials, deriving API key via L1...")
		creds, err := client.DeriveAPIKey(context.Background())
		if err != nil {
			return nil, err
		}
		auth.SetCredentials(*creds)
	}

	mktFeed := exchange.NewMarketFeed(cfg.API.WSMarketURL, logger)
	usrFeed := exchange.NewUserFeed(cfg.API.WSUserURL, auth, logger)
	scanner := market.NewScanner(cfg, logger)
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
		tokenMap:        make(map[string]string),
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
			e.logger.Error("user feed error", "error", err)
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
func (e *Engine) reconcileMarkets(result market.ScanResult) {
	desired := make(map[string]types.MarketAllocation)
	for _, alloc := range result.Markets {
		desired[alloc.Market.ConditionID] = alloc
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
	if info.YesTokenID == "" || info.NoTokenID == "" {
		e.logger.Warn("skipping market with missing token IDs", "slug", info.Slug)
		return
	}

	book := market.NewBook(info.ConditionID, info.YesTokenID, info.NoTokenID)
	inv := strategy.NewInventory(info.ConditionID, info.YesTokenID, info.NoTokenID)

	// Restore position from persistence
	if pos, err := e.store.LoadPosition(info.ConditionID); err == nil && pos != nil {
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

	e.slots[info.ConditionID] = slot

	// Register token -> conditionID mapping
	e.tokenMapMu.Lock()
	e.tokenMap[info.YesTokenID] = info.ConditionID
	e.tokenMap[info.NoTokenID] = info.ConditionID
	e.tokenMapMu.Unlock()

	// Subscribe WebSocket feeds
	e.mktFeed.Subscribe(ctx, []string{info.YesTokenID, info.NoTokenID})
	e.usrFeed.Subscribe(ctx, []string{info.ConditionID})

	// Fetch initial book snapshots synchronously before starting strategy
	for _, tokenID := range []string{info.YesTokenID, info.NoTokenID} {
		resp, err := e.client.GetOrderBook(ctx, tokenID)
		if err != nil {
			e.logger.Error("failed to get initial book", "token", tokenID, "error", err)
			continue
		}
		book.ApplyBookResponse(resp)
	}

	// Start strategy goroutine
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		maker.Run(ctx, tradeCh, orderCh)
	}()

	e.logger.Info("market started",
		"slug", info.Slug,
		"condition_id", info.ConditionID,
		"spread", info.Spread,
		"score", alloc.Score,
	)
}

func (e *Engine) stopMarketLocked(conditionID string) {
	slot, ok := e.slots[conditionID]
	if !ok {
		return
	}

	// Cancel goroutine (maker.Run will cancel its own orders)
	slot.cancel()

	// Save position
	pos := slot.inventory.Snapshot()
	if err := e.store.SavePosition(conditionID, pos); err != nil {
		e.logger.Error("failed to save position on stop", "market", conditionID, "error", err)
	}

	// Unsubscribe WS
	e.mktFeed.Unsubscribe(e.ctx, []string{slot.info.YesTokenID, slot.info.NoTokenID})
	e.usrFeed.Unsubscribe(e.ctx, []string{conditionID})

	// Clean up risk state
	e.riskMgr.RemoveMarket(conditionID)

	// Clean up token map
	e.tokenMapMu.Lock()
	delete(e.tokenMap, slot.info.YesTokenID)
	delete(e.tokenMap, slot.info.NoTokenID)
	e.tokenMapMu.Unlock()

	delete(e.slots, conditionID)

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
func (e *Engine) dispatchMarketEvents() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case evt := <-e.mktFeed.BookEvents():
			e.routeBookEvent(evt)
		case evt := <-e.mktFeed.PriceChangeEvents():
			e.routePriceChange(evt)
		}
	}
}

func (e *Engine) routeBookEvent(evt types.WSBookEvent) {
	e.tokenMapMu.RLock()
	conditionID, ok := e.tokenMap[evt.AssetID]
	e.tokenMapMu.RUnlock()
	if !ok {
		return
	}

	e.slotsMu.RLock()
	slot, ok := e.slots[conditionID]
	e.slotsMu.RUnlock()
	if !ok {
		return
	}

	slot.book.ApplyBookEvent(evt)
}

func (e *Engine) routePriceChange(evt types.WSPriceChangeEvent) {
	if len(evt.PriceChanges) == 0 {
		return
	}

	e.tokenMapMu.RLock()
	conditionID, ok := e.tokenMap[evt.PriceChanges[0].AssetID]
	e.tokenMapMu.RUnlock()
	if !ok {
		return
	}

	e.slotsMu.RLock()
	slot, ok := e.slots[conditionID]
	e.slotsMu.RUnlock()
	if !ok {
		return
	}

	slot.book.ApplyPriceChange(evt)
}

// dispatchUserEvents routes WS user events to the correct slot's channels.
func (e *Engine) dispatchUserEvents() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case trade := <-e.usrFeed.TradeEvents():
			e.routeTrade(trade)
		case order := <-e.usrFeed.OrderEvents():
			e.routeOrder(order)
		}
	}
}

func (e *Engine) routeTrade(trade types.WSTradeEvent) {
	e.slotsMu.RLock()
	slot, ok := e.slots[trade.Market]
	e.slotsMu.RUnlock()
	if !ok {
		return
	}

	select {
	case slot.tradeCh <- trade:
	default:
		e.logger.Warn("trade channel full", "market", trade.Market)
	}
}

func (e *Engine) routeOrder(order types.WSOrderEvent) {
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
			ConditionID:      slot.info.ConditionID,
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
