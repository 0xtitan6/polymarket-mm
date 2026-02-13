// Package strategy implements the Avellaneda-Stoikov market-making algorithm
// for Polymarket binary prediction markets (prices in [0, 1]).
//
// The core idea: post a bid below and an ask above a "reservation price" that
// accounts for inventory risk. When the bot is long, it lowers quotes to
// attract sellers; when short, it raises quotes to attract buyers.
//
// Per-tick flow (every RefreshInterval):
//  1. Check book staleness and risk limits.
//  2. Compute reservation price:  r = mid - q * γ * σ² * T
//  3. Compute optimal spread:     δ = γ * σ² * T + (2/γ) * ln(1 + γ/k)
//  4. Derive bid = r - δ/2, ask = r + δ/2, clamped to [tick, 1-tick].
//  5. Reconcile: cancel stale orders, place new ones via batch API.
//
// The bot earns the spread when both sides fill. Inventory skew (q) ensures
// it doesn't accumulate unbounded directional risk.
package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"polymarket-mm/internal/api"
	"polymarket-mm/internal/config"
	"polymarket-mm/internal/exchange"
	"polymarket-mm/internal/market"
	"polymarket-mm/internal/risk"
	"polymarket-mm/pkg/types"
)

// Maker runs the Avellaneda-Stoikov strategy for a single market.
// It maintains a map of its own active orders and reconciles them each tick.
type Maker struct {
	cfg        config.StrategyConfig
	marketInfo types.MarketInfo
	book       *market.Book
	inventory  *Inventory
	client     *exchange.Client
	riskMgr    *risk.Manager

	// Flow detection (Phase 1)
	flowTracker *FlowTracker

	// Track our outstanding orders
	activeOrders map[string]types.OpenOrder // orderID -> order

	// Optional dashboard event channel
	dashboardEvents chan<- api.DashboardEvent

	logger *slog.Logger
}

// NewMaker creates a strategy instance for one market.
func NewMaker(
	cfg config.StrategyConfig,
	info types.MarketInfo,
	book *market.Book,
	inventory *Inventory,
	client *exchange.Client,
	riskMgr *risk.Manager,
	logger *slog.Logger,
	dashboardEvents chan<- api.DashboardEvent,
) *Maker {
	return &Maker{
		cfg:             cfg,
		marketInfo:      info,
		book:            book,
		inventory:       inventory,
		client:          client,
		riskMgr:         riskMgr,
		flowTracker:     NewFlowTracker(cfg.FlowWindow, cfg.FlowToxicityThreshold, cfg.FlowCooldownPeriod, cfg.FlowMaxSpreadMultiplier),
		activeOrders:    make(map[string]types.OpenOrder),
		dashboardEvents: dashboardEvents,
		logger: logger.With(
			"component", "maker",
			"market", info.Slug,
		),
	}
}

// Run is the main loop for this market. Blocks until ctx is cancelled.
func (m *Maker) Run(ctx context.Context, tradeCh <-chan types.WSTradeEvent, orderCh <-chan types.WSOrderEvent) {
	ticker := time.NewTicker(m.cfg.RefreshInterval)
	defer ticker.Stop()

	m.logger.Info("strategy started",
		"tick_size", m.marketInfo.TickSize,
		"order_size", m.cfg.OrderSizeUSD,
	)

	for {
		select {
		case <-ctx.Done():
			m.cancelAllMyOrders(context.Background())
			m.logger.Info("strategy stopped")
			return

		case trade := <-tradeCh:
			m.handleFill(trade)

		case order := <-orderCh:
			m.handleOrderEvent(order)

		case <-ticker.C:
			m.quoteUpdate(ctx)
		}
	}
}

// quoteUpdate is the core per-tick logic.
func (m *Maker) quoteUpdate(ctx context.Context) {
	// 1. Check if book is stale
	if m.book.IsStale(m.cfg.StaleBookTimeout) {
		m.logger.Warn("book is stale, cancelling all orders")
		m.cancelAllMyOrders(ctx)
		return
	}

	// 2. Check risk limits
	mid, ok := m.book.MidPrice()
	if !ok {
		m.logger.Debug("no mid price available")
		return
	}

	m.inventory.UpdateMarkToMarket(mid)

	// Report position to risk manager
	pos := m.inventory.Snapshot()
	exposureUSD := m.inventory.TotalExposureUSD(mid)
	m.riskMgr.Report(risk.PositionReport{
		MarketID:      m.marketInfo.ConditionID,
		YesQty:        pos.YesQty,
		NoQty:         pos.NoQty,
		MidPrice:      mid,
		ExposureUSD:   exposureUSD,
		UnrealizedPnL: pos.UnrealizedPnL,
		RealizedPnL:   pos.RealizedPnL,
		Timestamp:     time.Now(),
	})

	// Emit position event to dashboard
	posSnapshot := api.PositionSnapshot{
		YesQty:        pos.YesQty,
		NoQty:         pos.NoQty,
		AvgEntryYes:   pos.AvgEntryYes,
		AvgEntryNo:    pos.AvgEntryNo,
		RealizedPnL:   pos.RealizedPnL,
		UnrealizedPnL: pos.UnrealizedPnL,
		ExposureUSD:   exposureUSD,
		Skew:          m.inventory.NetDelta(),
		LastUpdated:   pos.LastUpdated,
	}
	m.emitDashboardEvent(api.DashboardEvent{
		Type:      "position",
		Timestamp: time.Now(),
		MarketID:  m.marketInfo.ConditionID,
		Data:      api.NewPositionEvent(posSnapshot, m.marketInfo.Slug, mid),
	})

	if m.riskMgr.IsKillSwitchActive() {
		m.logger.Warn("kill switch active, cancelling all orders")
		m.cancelAllMyOrders(ctx)
		return
	}

	remaining := m.riskMgr.RemainingBudget(m.marketInfo.ConditionID)
	if remaining <= 0 {
		m.logger.Info("risk budget exhausted")
		m.cancelAllMyOrders(ctx)
		return
	}

	// 3. Compute quotes using Avellaneda-Stoikov
	quotes, err := m.computeQuotes(mid, remaining)
	if err != nil {
		m.logger.Error("compute quotes failed", "error", err)
		return
	}

	// 4. Reconcile orders (cancel stale, place new)
	if err := m.reconcileOrders(ctx, quotes); err != nil {
		m.logger.Error("reconcile orders failed", "error", err)
	}
}

// computeQuotes implements the Avellaneda-Stoikov model for binary markets.
//
// Variables:
//
//	q     = inventory skew in [-1, 1] from NetDelta()
//	gamma = risk aversion (higher = tighter spread, less inventory risk)
//	sigma = estimated volatility
//	k     = order arrival intensity
//	T     = time horizon
//
// Formulas:
//
//	reservation_price = mid - q * gamma * sigma^2 * T
//	optimal_spread    = gamma * sigma^2 * T + (2/gamma) * ln(1 + gamma/k)
//	bid = reservation_price - optimal_spread/2
//	ask = reservation_price + optimal_spread/2
func (m *Maker) computeQuotes(mid, remainingBudget float64) (*types.QuotePair, error) {
	q := m.inventory.NetDelta() // [-1, 1]
	gamma := m.cfg.Gamma
	sigma := m.cfg.Sigma
	k := m.cfg.K
	T := m.cfg.T
	minSpread := float64(m.cfg.DefaultSpreadBps) / 10000.0
	tickDec := m.marketInfo.TickSize.Decimals()
	tick := math.Pow(10, -float64(tickDec))

	// Phase 1: Apply flow toxicity adjustment
	flowMultiplier := m.flowTracker.GetSpreadMultiplier()
	minSpread *= flowMultiplier

	// Step 1: Reservation price
	// r = mid - q * gamma * sigma^2 * T
	reservationPrice := mid - q*gamma*sigma*sigma*T

	// Step 2: Optimal spread (with toxicity adjustment)
	// delta = gamma * sigma^2 * T + (2/gamma) * ln(1 + gamma/k)
	optSpread := gamma*sigma*sigma*T + (2.0/gamma)*math.Log(1+gamma/k)
	optSpread *= flowMultiplier // Widen spread when flow is toxic

	// Step 3: Raw bid/ask
	bidRaw := reservationPrice - optSpread/2
	askRaw := reservationPrice + optSpread/2

	// Step 4: Enforce minimum spread
	if (askRaw - bidRaw) < minSpread {
		bidRaw = reservationPrice - minSpread/2
		askRaw = reservationPrice + minSpread/2
	}

	// Step 5: Clamp to valid price range [tick, 1-tick]
	bidRaw = clamp(bidRaw, tick, 1-tick)
	askRaw = clamp(askRaw, tick, 1-tick)

	// Ensure bid < ask after clamping
	if bidRaw >= askRaw {
		bidRaw = askRaw - tick
	}
	if bidRaw < tick {
		bidRaw = tick
	}

	// Step 6: Round to tick size
	bidPrice := roundDownToTick(bidRaw, tickDec)
	askPrice := roundUpToTick(askRaw, tickDec)

	// Ensure still valid after rounding
	if bidPrice >= askPrice {
		askPrice = bidPrice + tick
	}

	// Step 7: Compute size
	absQ := math.Abs(q)
	sizeFactor := 1.0 - 0.5*absQ // reduce size when heavily positioned
	baseSize := m.cfg.OrderSizeUSD / mid
	bidSize := math.Max(baseSize*sizeFactor, m.marketInfo.MinOrderSize)
	askSize := math.Max(baseSize*sizeFactor, m.marketInfo.MinOrderSize)

	// Limit by remaining risk budget
	// Keep combined quoted notional (bid + ask) within remaining headroom.
	maxBidSize := remainingBudget / bidPrice
	maxAskSize := remainingBudget / askPrice
	bidSize = math.Min(bidSize, maxBidSize)
	askSize = math.Min(askSize, maxAskSize)
	totalNotional := bidSize*bidPrice + askSize*askPrice
	if totalNotional > remainingBudget && totalNotional > 0 {
		scale := remainingBudget / totalNotional
		bidSize *= scale
		askSize *= scale
	}

	// Floor to min order size
	var bid, ask *types.UserOrder

	if bidSize >= m.marketInfo.MinOrderSize && bidPrice > 0 && bidPrice < 1 {
		bid = &types.UserOrder{
			TokenID:   m.marketInfo.YesTokenID,
			Price:     bidPrice,
			Size:      bidSize,
			Side:      types.BUY,
			OrderType: types.OrderTypeGTC,
			TickSize:  m.marketInfo.TickSize,
		}
	}

	if askSize >= m.marketInfo.MinOrderSize && askPrice > 0 && askPrice < 1 {
		ask = &types.UserOrder{
			TokenID:   m.marketInfo.YesTokenID,
			Price:     askPrice,
			Size:      askSize,
			Side:      types.SELL,
			OrderType: types.OrderTypeGTC,
			TickSize:  m.marketInfo.TickSize,
		}
	}

	// Get toxicity metrics for logging
	toxicity := m.flowTracker.CalculateToxicity()

	m.logger.Debug("quotes computed",
		"mid", mid,
		"q", q,
		"reservation", reservationPrice,
		"bid", bidPrice,
		"ask", askPrice,
		"bid_size", bidSize,
		"ask_size", askSize,
		"spread", askPrice-bidPrice,
		"toxicity_score", toxicity.ToxicityScore,
		"directional_imbalance", toxicity.DirectionalImbalance,
		"fill_velocity", toxicity.FillVelocity,
		"flow_spread_multiplier", flowMultiplier,
	)

	return &types.QuotePair{
		MarketID:    m.marketInfo.ConditionID,
		YesTokenID:  m.marketInfo.YesTokenID,
		NoTokenID:   m.marketInfo.NoTokenID,
		Bid:         bid,
		Ask:         ask,
		GeneratedAt: time.Now(),
	}, nil
}

// reconcileOrders diffs desired quotes against active orders.
// An existing order is kept if its price is within one tick and its remaining
// size is within 10% of the desired size. Everything else is cancelled.
// New orders are placed via the batch POST /orders endpoint.
func (m *Maker) reconcileOrders(ctx context.Context, desired *types.QuotePair) error {
	tick := math.Pow(10, -float64(m.marketInfo.TickSize.Decimals()))
	sizeTolerance := 0.10 // 10% size tolerance

	var toCancel []string
	var toPlace []types.UserOrder
	matchedBid := false
	matchedAsk := false

	// Check each active order against desired quotes
	for id, order := range m.activeOrders {
		orderPrice, _ := strconv.ParseFloat(order.Price, 64)
		orderSizeOrig, _ := strconv.ParseFloat(order.OriginalSize, 64)
		orderSizeMatched, _ := strconv.ParseFloat(order.SizeMatched, 64)
		remainingSize := orderSizeOrig - orderSizeMatched

		if order.Side == "BUY" && desired.Bid != nil {
			if math.Abs(orderPrice-desired.Bid.Price) <= tick &&
				math.Abs(remainingSize-desired.Bid.Size)/desired.Bid.Size <= sizeTolerance {
				matchedBid = true
				continue
			}
		}
		if order.Side == "SELL" && desired.Ask != nil {
			if math.Abs(orderPrice-desired.Ask.Price) <= tick &&
				math.Abs(remainingSize-desired.Ask.Size)/desired.Ask.Size <= sizeTolerance {
				matchedAsk = true
				continue
			}
		}

		// Order doesn't match any desired quote, cancel it
		toCancel = append(toCancel, id)
	}

	if !matchedBid && desired.Bid != nil {
		toPlace = append(toPlace, *desired.Bid)
	}
	if !matchedAsk && desired.Ask != nil {
		toPlace = append(toPlace, *desired.Ask)
	}

	// Cancel stale orders
	if len(toCancel) > 0 {
		resp, err := m.client.CancelOrders(ctx, toCancel)
		if err != nil {
			return fmt.Errorf("cancel orders: %w", err)
		}
		for _, id := range resp.Canceled {
			delete(m.activeOrders, id)
		}
	}

	// Place new orders
	if len(toPlace) > 0 {
		results, err := m.client.PostOrders(ctx, toPlace, m.marketInfo.NegRisk)
		if err != nil {
			return fmt.Errorf("place orders: %w", err)
		}
		for i, result := range results {
			if result.Success && result.OrderID != "" {
				m.activeOrders[result.OrderID] = types.OpenOrder{
					ID:           result.OrderID,
					Status:       result.Status,
					Market:       m.marketInfo.ConditionID,
					AssetID:      toPlace[i].TokenID,
					Side:         string(toPlace[i].Side),
					Price:        fmt.Sprintf("%.4f", toPlace[i].Price),
					OriginalSize: fmt.Sprintf("%.2f", toPlace[i].Size),
					SizeMatched:  "0",
				}
			} else if result.ErrorMsg != "" {
				m.logger.Error("order rejected",
					"error", result.ErrorMsg,
					"side", toPlace[i].Side,
					"price", toPlace[i].Price,
				)
			}
		}
	}

	return nil
}

// handleFill processes a trade event from the user WS channel.
func (m *Maker) handleFill(trade types.WSTradeEvent) {
	price, _ := strconv.ParseFloat(trade.Price, 64)
	size, _ := strconv.ParseFloat(trade.Size, 64)

	fill := Fill{
		Timestamp: time.Now(),
		Side:      types.Side(trade.Side),
		TokenID:   trade.AssetID,
		Price:     price,
		Size:      size,
		TradeID:   trade.ID,
	}

	m.inventory.OnFill(fill)
	m.flowTracker.AddFill(fill) // Track for toxicity detection

	pos := m.inventory.Snapshot()

	// Check toxicity after fill
	toxicity := m.flowTracker.CalculateToxicity()
	if toxicity.IsAverse {
		m.logger.Warn("toxic flow detected",
			"side", trade.Side,
			"toxicity_score", toxicity.ToxicityScore,
			"directional_imbalance", toxicity.DirectionalImbalance,
			"fill_velocity", toxicity.FillVelocity,
			"fill_count", m.flowTracker.GetFillCount(),
		)
	}

	m.logger.Info("fill",
		"side", trade.Side,
		"price", price,
		"size", size,
		"outcome", trade.Outcome,
		"yes_qty", pos.YesQty,
		"no_qty", pos.NoQty,
		"realized_pnl", pos.RealizedPnL,
	)

	// Emit fill event to dashboard
	mid, _ := m.book.MidPrice()
	unrealizedPnL := pos.YesQty*(mid-pos.AvgEntryYes) + pos.NoQty*((1-mid)-pos.AvgEntryNo)

	posSnapshot := api.PositionSnapshot{
		YesQty:        pos.YesQty,
		NoQty:         pos.NoQty,
		AvgEntryYes:   pos.AvgEntryYes,
		AvgEntryNo:    pos.AvgEntryNo,
		RealizedPnL:   pos.RealizedPnL,
		UnrealizedPnL: unrealizedPnL,
		LastUpdated:   pos.LastUpdated,
	}

	m.emitDashboardEvent(api.DashboardEvent{
		Type:      "fill",
		Timestamp: time.Now(),
		MarketID:  m.marketInfo.ConditionID,
		Data:      api.NewFillEvent(trade, posSnapshot, m.marketInfo.Slug, price, size),
	})
}

// handleOrderEvent processes order lifecycle events.
func (m *Maker) handleOrderEvent(event types.WSOrderEvent) {
	switch event.Type {
	case "CANCELLATION":
		delete(m.activeOrders, event.ID)
	case "UPDATE":
		if order, ok := m.activeOrders[event.ID]; ok {
			order.SizeMatched = event.SizeMatched
			m.activeOrders[event.ID] = order
		}
	case "PLACEMENT":
		if _, ok := m.activeOrders[event.ID]; !ok {
			m.activeOrders[event.ID] = types.OpenOrder{
				ID:           event.ID,
				Market:       event.Market,
				AssetID:      event.AssetID,
				Side:         event.Side,
				Price:        event.Price,
				OriginalSize: event.OriginalSize,
				SizeMatched:  event.SizeMatched,
			}
		}
	}
}

// cancelAllMyOrders cancels all active orders for this market.
func (m *Maker) cancelAllMyOrders(ctx context.Context) {
	if len(m.activeOrders) == 0 {
		return
	}

	resp, err := m.client.CancelMarketOrders(ctx, m.marketInfo.ConditionID)
	if err != nil {
		m.logger.Error("cancel all orders failed", "error", err)
		return
	}

	for _, id := range resp.Canceled {
		delete(m.activeOrders, id)
	}

	m.logger.Info("cancelled orders", "count", len(resp.Canceled))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func roundDownToTick(v float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Floor(v*pow) / pow
}

func roundUpToTick(v float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Ceil(v*pow) / pow
}

// emitDashboardEvent sends an event to the dashboard (non-blocking).
func (m *Maker) emitDashboardEvent(evt api.DashboardEvent) {
	if m.dashboardEvents == nil {
		return
	}

	select {
	case m.dashboardEvents <- evt:
	default:
		// Dashboard can't keep up, drop event
	}
}
