// Synthesis Market Maker — an automated market-making bot for prediction markets
// via the Synthesis Trade API (https://synthesis.trade).
//
// # Architecture
//
// This entry point mirrors cmd/bot/main.go but wires the Synthesis-specific
// components instead of the Polymarket US API components:
//
//	main.go            — loads SynthesisConfig, starts SynthesisEngine
//	SynthesisEngine    — orchestrator wired with SynthesisClient + SynthesisWSFeed
//	SynthesisClient    — REST client for the Synthesis Trade API
//	SynthesisAuth      — X-API-KEY header auth (no Ed25519 signing)
//	SynthesisWSFeed    — polling-based market/order feed (no WS required)
//	strategy/maker.go  — Avellaneda-Stoikov quoting (unchanged)
//	market/scanner.go  — market discovery (uses SynthesisClient.GetMarkets/GetBBO)
//	risk/manager.go    — kill switch and position limits (unchanged)
//	store/store.go     — JSON position persistence (unchanged)
//
// # Why a separate engine?
//
// The existing engine.Engine uses *exchange.Client and *exchange.Auth by
// concrete type (not interface). Rather than modify those files, this package
// wires a SynthesisEngine that is structurally identical but uses
// *exchange.SynthesisClient and *exchange.SynthesisWSFeed.
//
// All market-making logic (strategy, risk, inventory, book) is shared.
// Only the exchange adapters differ.
//
// # Configuration
//
// Config is loaded from configs/synthesis_config.yaml by default.
// Override with SYNTH_CONFIG env var. Auth is set via:
//
//	SYNTH_API_KEY    — Synthesis API key
//	SYNTH_WALLET_ID  — Synthesis wallet ID
//	SYNTH_DRY_RUN=true — dry-run mode
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
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

func main() {
	// ——————————————————————————————————————————————————————————
	// Config
	// ——————————————————————————————————————————————————————————
	cfgPath := "configs/synthesis_config.yaml"
	if p := os.Getenv("SYNTH_CONFIG"); p != "" {
		cfgPath = p
	}

	synthCfg, err := config.LoadSynthesis(cfgPath)
	if err != nil {
		slog.Error("failed to load synthesis config", "error", err, "path", cfgPath)
		os.Exit(1)
	}
	if err := synthCfg.Validate(); err != nil {
		slog.Error("invalid synthesis config", "error", err)
		os.Exit(1)
	}

	// ——————————————————————————————————————————————————————————
	// Logger
	// ——————————————————————————————————————————————————————————
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLogLevel(synthCfg.Logging.Level)}
	if synthCfg.Logging.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)

	// ——————————————————————————————————————————————————————————
	// Auth + Client
	// ——————————————————————————————————————————————————————————
	auth, err := exchange.NewSynthesisAuth(*synthCfg)
	if err != nil {
		logger.Error("failed to create synthesis auth", "error", err)
		os.Exit(1)
	}

	client := exchange.NewSynthesisClient(*synthCfg, auth, logger)

	// ——————————————————————————————————————————————————————————
	// Engine
	// ——————————————————————————————————————————————————————————

	// Convert SynthesisConfig to base Config for components that need it
	baseCfg := synthCfg.ToBaseConfig()

	eng, err := newSynthesisEngine(*synthCfg, *baseCfg, client, logger)
	if err != nil {
		logger.Error("failed to create synthesis engine", "error", err)
		os.Exit(1)
	}

	// ——————————————————————————————————————————————————————————
	// Dashboard (optional)
	// ——————————————————————————————————————————————————————————
	var apiServer *api.Server
	if synthCfg.Dashboard.Enabled {
		apiServer = api.NewServer(synthCfg.Dashboard, eng, *baseCfg, logger)
		go func() {
			if err := apiServer.Start(); err != nil {
				logger.Error("synthesis dashboard server failed", "error", err)
			}
		}()
		logger.Info("synthesis dashboard started",
			"url", fmt.Sprintf("http://localhost:%d", synthCfg.Dashboard.Port))
	}

	if err := eng.Start(); err != nil {
		logger.Error("failed to start synthesis engine", "error", err)
		os.Exit(1)
	}

	if synthCfg.DryRun {
		logger.Warn("DRY-RUN MODE — no real orders will be placed on Synthesis")
	}

	logger.Info("synthesis market maker started",
		"venue", synthCfg.Auth.Venue,
		"wallet_id", synthCfg.Auth.WalletID,
		"markets_max", synthCfg.Risk.MaxMarketsActive,
		"order_size", synthCfg.Strategy.OrderSizeUSD,
		"max_exposure", synthCfg.Risk.MaxGlobalExposure,
		"dry_run", synthCfg.DryRun,
	)

	// ——————————————————————————————————————————————————————————
	// Shutdown on SIGINT / SIGTERM
	// ——————————————————————————————————————————————————————————
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	logger.Info("received shutdown signal", "signal", sig.String())

	if apiServer != nil {
		if err := apiServer.Stop(); err != nil {
			logger.Error("failed to stop synthesis dashboard", "error", err)
		}
	}

	eng.Stop()
}

// ————————————————————————————————————————————————————————————————————————
// SynthesisEngine
//
// A synthesis-specific orchestrator that is structurally identical to
// engine.Engine but uses *exchange.SynthesisClient and *exchange.SynthesisWSFeed
// instead of their Polymarket counterparts.
//
// All downstream components (Maker, Inventory, Book, RiskManager, Scanner, Store)
// are the same packages used by the Polymarket engine — only the exchange adapter
// wiring changes.
// ————————————————————————————————————————————————————————————————————————

// synthesisMarketSlot mirrors engine.marketSlot but is local to this package.
type synthesisMarketSlot struct {
	info      types.MarketInfo
	book      *market.Book
	inventory *strategy.Inventory
	maker     *strategy.Maker
	cancel    context.CancelFunc
	tradeCh   chan types.WSTradeEvent
	orderCh   chan types.WSOrderEvent
	bookCh    chan struct{}
}

// SynthesisEngine is the top-level orchestrator for the Synthesis-backed bot.
type SynthesisEngine struct {
	synthCfg config.SynthesisConfig
	baseCfg  config.Config
	client   *exchange.SynthesisClient
	mktFeed  *exchange.SynthesisWSFeed
	usrFeed  *exchange.SynthesisWSFeed
	scanner  *synthScanner   // synthesis-specific scanner wrapper
	riskMgr  *risk.Manager
	st       *store.Store
	logger   *slog.Logger

	slots   map[string]*synthesisMarketSlot
	slotsMu sync.RWMutex
	slugMap   map[string]string
	slugMapMu sync.RWMutex

	dashboardEvents chan api.DashboardEvent

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newSynthesisEngine wires all Synthesis-specific components.
func newSynthesisEngine(
	synthCfg config.SynthesisConfig,
	baseCfg config.Config,
	client *exchange.SynthesisClient,
	logger *slog.Logger,
) (*SynthesisEngine, error) {
	// Both feeds use the same polling client — the SynthesisWSFeed handles
	// both market data (via Polymarket CLOB) and order events (via Synthesis) internally.
	polyCLOB := client.PolyCLOB()
	mktFeed := exchange.NewSynthesisMarketFeed(client, polyCLOB, logger)
	usrFeed := exchange.NewSynthesisUserFeed(client, polyCLOB, logger)

	// The scanner needs a market.Scanner backed by exchange.Client, but
	// SynthesisClient has identical method signatures. We wrap it in a thin
	// adapter (synthScanner) that holds the synthesis client directly.
	scanner := newSynthScanner(client, baseCfg, logger)

	riskMgr := risk.NewManager(baseCfg.Risk, logger)

	st, err := store.Open(baseCfg.Store.DataDir)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	var dashEvents chan api.DashboardEvent
	if synthCfg.Dashboard.Enabled {
		dashEvents = make(chan api.DashboardEvent, 100)
	}

	return &SynthesisEngine{
		synthCfg:        synthCfg,
		baseCfg:         baseCfg,
		client:          client,
		mktFeed:         mktFeed,
		usrFeed:         usrFeed,
		scanner:         scanner,
		riskMgr:         riskMgr,
		st:              st,
		logger:          logger.With("component", "synthesis_engine"),
		slots:           make(map[string]*synthesisMarketSlot),
		slugMap:         make(map[string]string),
		dashboardEvents: dashEvents,
		ctx:             ctx,
		cancel:          cancel,
	}, nil
}

// Start launches all background goroutines. Mirrors engine.Engine.Start.
func (e *SynthesisEngine) Start() error {
	// Market (book poll) feed
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := e.mktFeed.Run(e.ctx); err != nil && e.ctx.Err() == nil {
			e.logger.Error("synthesis market feed error", "error", err)
		}
	}()

	// User (order poll) feed
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := e.usrFeed.Run(e.ctx); err != nil && e.ctx.Err() == nil {
			e.logger.Error("synthesis user feed error", "error", err)
		}
	}()

	// Scanner
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.scanner.run(e.ctx)
	}()

	// Risk manager
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.riskMgr.Run(e.ctx)
	}()

	// WS/poll event dispatchers
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

	// Main market management loop
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.manageMarkets()
	}()

	return nil
}

// Stop cancels everything, flushes positions, and waits for goroutines.
// Mirrors engine.Engine.Stop.
func (e *SynthesisEngine) Stop() {
	e.logger.Info("synthesis engine shutting down...")
	e.cancel()

	// Safety net: cancel all orders
	cancelCtx, cancelCancel := context.WithTimeout(context.Background(), e.baseCfg.Strategy.StaleBookTimeout)
	defer cancelCancel()
	if _, err := e.client.CancelAll(cancelCtx); err != nil {
		e.logger.Error("failed to cancel all synthesis orders on shutdown", "error", err)
	}

	// Persist final positions
	e.slotsMu.RLock()
	for id, slot := range e.slots {
		pos := slot.inventory.Snapshot()
		if err := e.st.SavePosition(id, pos); err != nil {
			e.logger.Error("failed to save position", "market", id, "error", err)
		}
	}
	e.slotsMu.RUnlock()

	e.wg.Wait()
	e.mktFeed.Close()
	e.usrFeed.Close()
	e.st.Close()

	e.logger.Info("synthesis engine shutdown complete")
}

// manageMarkets is the main engine loop — reacts to scanner results and kill signals.
func (e *SynthesisEngine) manageMarkets() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case result := <-e.scanner.results():
			e.reconcileMarkets(result)
		case kill := <-e.riskMgr.KillCh():
			e.handleKillSignal(kill)
		}
	}
}

// reconcileMarkets starts/stops market slots to match the scanner's desired set.
func (e *SynthesisEngine) reconcileMarkets(result market.ScanResult) {
	desired := make(map[string]types.MarketAllocation)
	for _, alloc := range result.Markets {
		desired[alloc.Market.Slug] = alloc
	}

	e.slotsMu.Lock()
	defer e.slotsMu.Unlock()

	for id := range e.slots {
		if _, ok := desired[id]; !ok {
			e.stopMarketLocked(id)
		}
	}
	for id, alloc := range desired {
		if _, ok := e.slots[id]; !ok {
			e.startMarketLocked(alloc)
		}
	}

	activeSlugs := make([]string, 0, len(e.slots))
	for slug := range e.slots {
		activeSlugs = append(activeSlugs, slug)
	}
	e.scanner.setActiveSlugs(activeSlugs)
}

func (e *SynthesisEngine) startMarketLocked(alloc types.MarketAllocation) {
	info := alloc.Market
	slug := info.Slug
	if slug == "" {
		e.logger.Warn("synthesis: skipping market with missing slug")
		return
	}

	// Cancel pre-existing orders for this market
	reconcileCtx, cancelReconcile := context.WithTimeout(e.ctx, 10*time.Second)
	_, err := e.client.CancelMarketOrders(reconcileCtx, slug)
	cancelReconcile()
	if err != nil {
		e.logger.Error("synthesis startup reconciliation failed, skipping market",
			"slug", slug, "error", err)
		return
	}

	book := market.NewBook(slug, slug, slug)
	inv := strategy.NewInventory(slug, slug, slug)

	if pos, err := e.st.LoadPosition(slug); err == nil && pos != nil {
		inv.SetPosition(*pos)
	}

	tradeCh := make(chan types.WSTradeEvent, 64)
	orderCh := make(chan types.WSOrderEvent, 64)

	// NewMaker expects *exchange.Client — we wrap SynthesisClient as a *Client
	// by using the synthesis-client adapter. The Maker calls only:
	//   GetOrderBook, CancelMarketOrders, PostOrders
	// which are all available on SynthesisClient. Because the Maker field is
	// typed as *exchange.Client, we use the clientAdapter helper below.
	clientAdapter := exchange.NewSynthesisClientAdapter(e.client, e.baseCfg, e.logger)

	maker := strategy.NewMaker(
		e.baseCfg.Strategy,
		info,
		book,
		inv,
		clientAdapter,
		e.riskMgr,
		e.logger,
		e.dashboardEvents,
		e.st.SavePosition,
	)

	ctx, cancel := context.WithCancel(e.ctx)
	slot := &synthesisMarketSlot{
		info:      info,
		book:      book,
		inventory: inv,
		maker:     maker,
		cancel:    cancel,
		tradeCh:   tradeCh,
		orderCh:   orderCh,
		bookCh:    make(chan struct{}, 16),
	}
	e.slots[slug] = slot

	e.slugMapMu.Lock()
	e.slugMap[slug] = slug
	e.slugMapMu.Unlock()

	e.mktFeed.Subscribe(ctx, []string{slug})
	e.usrFeed.Subscribe(ctx, []string{slug})

	// Build initial book snapshot from Polymarket public CLOB.
	initCtx, initCancel := context.WithTimeout(ctx, 5*time.Second)
	initBook, initErr := e.client.GetOrderBook(initCtx, slug)
	initCancel()
	if initErr != nil {
		e.logger.Warn("synthesis: failed to get initial book from Polymarket CLOB", "slug", slug, "error", initErr)
	} else if initBook != nil && (len(initBook.Bids) > 0 || len(initBook.Asks) > 0) {
		book.ApplyBookResponse(initBook)
	}

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		maker.Run(ctx, tradeCh, orderCh, slot.bookCh)
	}()

	e.logger.Info("synthesis market started",
		"slug", slug,
		"spread", info.Spread,
		"score", alloc.Score,
	)
}

func (e *SynthesisEngine) stopMarketLocked(slug string) {
	slot, ok := e.slots[slug]
	if !ok {
		return
	}
	slot.cancel()
	pos := slot.inventory.Snapshot()
	if err := e.st.SavePosition(slug, pos); err != nil {
		e.logger.Error("failed to save position on stop", "market", slug, "error", err)
	}
	e.mktFeed.Unsubscribe(e.ctx, []string{slug})
	e.usrFeed.Unsubscribe(e.ctx, []string{slug})
	e.riskMgr.RemoveMarket(slug)
	e.slugMapMu.Lock()
	delete(e.slugMap, slug)
	e.slugMapMu.Unlock()
	delete(e.slots, slug)
	e.logger.Info("synthesis market stopped", "slug", slug)
}

func (e *SynthesisEngine) handleKillSignal(kill risk.KillSignal) {
	e.logger.Error("KILL SIGNAL received",
		"market", kill.MarketID,
		"reason", kill.Reason,
	)

	e.emitDashboardEvent(api.DashboardEvent{
		Type:      "kill",
		Timestamp: time.Now(),
		MarketID:  kill.MarketID,
		Data: api.NewKillEvent(
			kill.Reason,
			kill.Reason,
			time.Now().Add(e.baseCfg.Risk.CooldownAfterKill),
			kill.MarketID,
		),
	})

	e.slotsMu.Lock()
	defer e.slotsMu.Unlock()

	if kill.MarketID == "" {
		for id := range e.slots {
			e.stopMarketLocked(id)
		}
		cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, err := e.client.CancelAll(cancelCtx); err != nil {
			e.logger.Error("synthesis: failed to cancel all orders", "error", err)
		}
		cancelCancel()
	} else {
		e.stopMarketLocked(kill.MarketID)
	}
}

// dispatchMarketEvents routes book-poll events to the correct slot's Book.
func (e *SynthesisEngine) dispatchMarketEvents() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case evt := <-e.mktFeed.BookEvents():
			e.routeBookEvent(evt)
		}
	}
}

func (e *SynthesisEngine) routeBookEvent(evt types.WSBookEvent) {
	slug := evt.AssetID
	e.slotsMu.RLock()
	slot, ok := e.slots[slug]
	e.slotsMu.RUnlock()
	if !ok {
		return
	}
	slot.book.ApplyBookEvent(evt)
	select {
	case slot.bookCh <- struct{}{}:
	default:
	}
}

// dispatchUserEvents routes order-poll events to the correct slot's channels.
func (e *SynthesisEngine) dispatchUserEvents() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case order := <-e.usrFeed.OrderEvents():
			e.routeOrder(order)
		}
	}
}

func (e *SynthesisEngine) routeOrder(order types.WSOrderEvent) {
	e.slotsMu.RLock()
	slot, ok := e.slots[order.Market]
	e.slotsMu.RUnlock()
	if !ok {
		return
	}
	select {
	case slot.orderCh <- order:
	default:
		e.logger.Warn("synthesis order channel full", "market", order.Market)
	}
}

func (e *SynthesisEngine) emitDashboardEvent(evt api.DashboardEvent) {
	if e.dashboardEvents == nil {
		return
	}
	select {
	case e.dashboardEvents <- evt:
	default:
	}
}

// ————————————————————————————————————————————————————————————————————————
// Dashboard interface — required by api.NewServer
// ————————————————————————————————————————————————————————————————————————

// DashboardEvents returns the dashboard event channel (may be nil).
func (e *SynthesisEngine) DashboardEvents() <-chan api.DashboardEvent {
	return e.dashboardEvents
}

// GetMarketsSnapshot returns current state of all active markets for the dashboard.
func (e *SynthesisEngine) GetMarketsSnapshot() []api.MarketStatus {
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
		isStale := slot.book.IsStale(e.baseCfg.Strategy.StaleBookTimeout)

		var unrealizedPnL float64
		if midOk {
			unrealizedPnL = pos.YesQty*(mid-pos.AvgEntryYes) +
				pos.NoQty*((1-mid)-pos.AvgEntryNo)
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

		result = append(result, api.MarketStatus{
			ConditionID: slot.info.Slug,
			Slug:        slot.info.Slug,
			Question:    slot.info.Question,
			MidPrice:    mid,
			BestBid:     bid,
			BestAsk:     ask,
			Spread:      spread,
			SpreadBps:   spreadBps,
			LastUpdated: lastUpdated,
			IsStale:     isStale,
			Position:    posSnapshot,
			EndDate:     slot.info.EndDate,
			Liquidity:   slot.info.Liquidity,
			Volume24h:   slot.info.Volume24h,
		})
	}
	return result
}

// GetScanner returns the scanner for dashboard access.
// Returns nil — SynthesisEngine uses a local scanner wrapper, not a market.Scanner.
// The dashboard gracefully handles a nil scanner.
func (e *SynthesisEngine) GetScanner() *market.Scanner {
	return nil
}

// GetRiskManager returns the risk manager for dashboard access.
func (e *SynthesisEngine) GetRiskManager() *risk.Manager {
	return e.riskMgr
}

// ————————————————————————————————————————————————————————————————————————
// synthScanner — wraps SynthesisClient to provide the scanner loop
//
// The existing market.Scanner is typed to *exchange.Client. Rather than
// change that, we replicate the scanner loop here using the Synthesis client
// directly. The logic is identical — poll GetMarkets, fetch BBO for each
// candidate, rank by spread/depth, emit ScanResults.
// ————————————————————————————————————————————————————————————————————————

type synthScanner struct {
	client    *exchange.SynthesisClient
	polyCLOB  *exchange.PolymarketCLOB
	cfg       config.Config
	logger    *slog.Logger
	resultCh  chan market.ScanResult
	activeMu  sync.RWMutex
	activeSlugs map[string]bool
}

func newSynthScanner(client *exchange.SynthesisClient, cfg config.Config, logger *slog.Logger) *synthScanner {
	return &synthScanner{
		client:      client,
		polyCLOB:    client.PolyCLOB(),
		cfg:         cfg,
		logger:      logger.With("component", "synthesis_scanner"),
		resultCh:    make(chan market.ScanResult, 1),
		activeSlugs: make(map[string]bool),
	}
}

func (s *synthScanner) results() <-chan market.ScanResult { return s.resultCh }

func (s *synthScanner) setActiveSlugs(slugs []string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.activeSlugs = make(map[string]bool, len(slugs))
	for _, sl := range slugs {
		s.activeSlugs[sl] = true
	}
}

// run is the scanner loop — polls markets, fetches BBO, emits ScanResults.
// Mirrors the core logic of market.Scanner.Run.
func (s *synthScanner) run(ctx context.Context) {
	interval := s.cfg.Scanner.PollInterval
	if interval == 0 {
		interval = 3 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Scan immediately on first tick
	s.scan(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

// scan performs one discovery cycle.
//
// Market selection: if Scanner.IncludeSlugs is configured, those token_ids are
// used directly. Otherwise, token_ids are derived from open orders and positions
// (i.e. markets where we already have activity).
//
// BBO is fetched from the Polymarket public CLOB API (no auth required).
// This gives us the real market-wide order book instead of just our own orders.
func (s *synthScanner) scan(ctx context.Context) {
	// ---- Step 1: determine the candidate token_ids ----
	sc := s.cfg.Scanner

	// Explicit whitelist takes priority.
	tokenIDs := sc.IncludeSlugs

	if len(tokenIDs) == 0 {
		// Derive from open orders
		ordersCtx, ordersCancel := context.WithTimeout(ctx, 5*time.Second)
		openOrders, ordErr := s.client.GetOpenOrders(ordersCtx, nil)
		ordersCancel()

		// Derive from positions
		posCtx, posCancel := context.WithTimeout(ctx, 5*time.Second)
		positions, posErr := s.client.GetPositions(posCtx)
		posCancel()

		if ordErr != nil && posErr != nil {
			s.logger.Error("synthesis scan: failed to fetch open orders and positions",
				"orders_err", ordErr, "positions_err", posErr)
			return
		}

		// Collect unique token_ids seen in orders
		seen := make(map[string]bool)
		if ordErr == nil {
			for _, o := range openOrders {
				if o.MarketSlug != "" {
					seen[o.MarketSlug] = true
				}
			}
		}
		// Collect token_ids from positions
		if posErr == nil {
			for tokenID := range positions {
				seen[tokenID] = true
			}
		}

		for id := range seen {
			tokenIDs = append(tokenIDs, id)
		}

		if len(tokenIDs) == 0 {
			s.logger.Info("synthesis scan: no token_ids found (no open orders or positions); waiting for IncludeSlugs config")
			// Emit an empty result so the engine can react (stop any stale slots)
			select {
			case s.resultCh <- market.ScanResult{Markets: nil, ScannedAt: time.Now()}:
			default:
			}
			return
		}
	}

	// ---- Step 2: fetch BBO from Polymarket public CLOB API ----
	type bboData struct {
		bestBid float64
		bestAsk float64
		hasBid  bool
		hasAsk  bool
	}
	bboByToken := make(map[string]*bboData)

	for _, tokenID := range tokenIDs {
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 5*time.Second)
		bid, ask, err := s.polyCLOB.GetBBOFromBook(fetchCtx, tokenID)
		fetchCancel()

		if err != nil {
			s.logger.Debug("synthesis scan: failed to fetch BBO from Polymarket CLOB",
				"token_id", tokenID, "error", err)
			continue
		}

		bboByToken[tokenID] = &bboData{
			bestBid: bid,
			bestAsk: ask,
			hasBid:  bid > 0,
			hasAsk:  ask > 0,
		}
	}

	// ---- Step 3: build allocations ----
	s.activeMu.RLock()
	activeSlugs := s.activeSlugs
	s.activeMu.RUnlock()

	var allocs []types.MarketAllocation
	for _, tokenID := range tokenIDs {
		// Apply keyword / exclude-slug filter (slug = tokenID in this context)
		if !s.passesTokenFilter(tokenID) {
			continue
		}

		// Get BBO from Polymarket CLOB data
		var bestBid, bestAsk, spread float64
		if bbo, ok := bboByToken[tokenID]; ok {
			bestBid = bbo.bestBid
			bestAsk = bbo.bestAsk
			if bbo.hasBid && bbo.hasAsk && bbo.bestAsk > bbo.bestBid {
				spread = bbo.bestAsk - bbo.bestBid
			}
		}

		if spread < sc.MinSpread {
			// Still include the market if it is explicitly listed in IncludeSlugs
			// (caller wants to make markets regardless of current spread).
			// For dynamically-discovered markets we skip thin spreads.
			if len(sc.IncludeSlugs) == 0 {
				continue
			}
		}

		score := spread // simple scoring
		if activeSlugs[tokenID] {
			score += sc.StickyBonus
		}

		info := types.MarketInfo{
			ID:              tokenID,
			Slug:            tokenID,
			Question:        tokenID,
			TickSize:        types.Tick001,
			Active:          true,
			AcceptingOrders: true,
			BestBid:         bestBid,
			BestAsk:         bestAsk,
			Spread:          spread,
		}

		allocs = append(allocs, types.MarketAllocation{
			Market:         info,
			MaxPositionUSD: s.cfg.Risk.MaxPositionPerMarket,
			Score:          score,
		})
	}

	// Sort descending by score and cap at MaxMarketsActive
	sortAllocsByScore(allocs)
	maxActive := s.cfg.Risk.MaxMarketsActive
	if maxActive > 0 && len(allocs) > maxActive {
		allocs = allocs[:maxActive]
	}

	select {
	case s.resultCh <- market.ScanResult{Markets: allocs, ScannedAt: time.Now()}:
	default:
		// Engine hasn't consumed last result — skip this cycle
	}
	s.logger.Info("synthesis scan complete",
		"candidates", len(tokenIDs),
		"selected", len(allocs),
	)
}

// passesTokenFilter applies scanner config keyword/slug filters to a token_id.
// In the Synthesis context, token_id acts as both the slug and the question text.
func (s *synthScanner) passesTokenFilter(tokenID string) bool {
	sc := s.cfg.Scanner
	// Include slug filter (whitelist) — when IncludeSlugs is set, scan() already
	// uses it as the authoritative list, so this is a belt-and-suspenders guard.
	if len(sc.IncludeSlugs) > 0 {
		matched := false
		for _, sl := range sc.IncludeSlugs {
			if tokenID == sl {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	// Exclude slug filter (blacklist)
	for _, sl := range sc.ExcludeSlugs {
		if tokenID == sl {
			return false
		}
	}
	// Keyword filters — match against the token_id string
	if len(sc.IncludeKeywords) > 0 {
		matched := false
		for _, kw := range sc.IncludeKeywords {
			if containsCI(tokenID, kw) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, kw := range sc.ExcludeKeywords {
		if containsCI(tokenID, kw) {
			return false
		}
	}
	return true
}

// ————————————————————————————————————————————————————————————————————————
// Helpers
// ————————————————————————————————————————————————————————————————————————

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

func parsePriceStr(s string, out *float64) {
	if s == "" {
		return
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err == nil {
		*out = v
	}
}

func containsCI(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	// simple byte-level scan (sufficient for slug/keyword matching)
	ls, lsub := toLower(s), toLower(sub)
	for i := 0; i <= len(ls)-len(lsub); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
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

func sortAllocsByScore(allocs []types.MarketAllocation) {
	// Simple insertion sort — market list is typically small (<200)
	for i := 1; i < len(allocs); i++ {
		cur := allocs[i]
		j := i - 1
		for j >= 0 && allocs[j].Score < cur.Score {
			allocs[j+1] = allocs[j]
			j--
		}
		allocs[j+1] = cur
	}
}
