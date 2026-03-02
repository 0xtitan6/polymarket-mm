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

// PositionSaver is called after each fill to persist the position to disk.
// The engine provides this callback, backed by the store package.
type PositionSaver func(marketID string, pos Position) error

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

	// Persist position after each fill
	positionSaver PositionSaver

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
	positionSaver PositionSaver,
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
		positionSaver:   positionSaver,
		dashboardEvents: dashboardEvents,
		logger: logger.With(
			"component", "maker",
			"market", info.Slug,
		),
	}
}

// Run is the main loop for this market. Blocks until ctx is cancelled.
//
// tradeCh is accepted for interface compatibility but will never fire — the US
// API WS layer returns a nil channel from TradeEvents(). Fill detection is
// handled entirely via orderCh (EXECUTION_TYPE_FILL events).
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
			// tradeCh is nil on the US API path (TradeEvents returns nil),
			// so this case never fires in practice. Kept for compatibility
			// in case a legacy caller wires a non-nil channel.
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
	// 1. Check if book is stale — if so, try a REST refresh before giving up.
	// Sports markets on the US API often have no WS book updates for minutes
	// at a time, so we fall back to REST polling.
	if m.book.IsStale(m.cfg.StaleBookTimeout) {
		m.logger.Info("book is stale, fetching via REST")
		resp, err := m.client.GetOrderBook(ctx, m.marketInfo.Slug)
		if err != nil {
			m.logger.Warn("REST book fetch failed, cancelling orders", "error", err)
			m.cancelAllMyOrders(ctx)
			return
		}
		m.book.ApplyBookResponse(resp)
		// If still stale after refresh (empty book), bail
		if m.book.IsStale(m.cfg.StaleBookTimeout) {
			m.logger.Warn("book still stale after REST refresh, cancelling all orders")
			m.cancelAllMyOrders(ctx)
			return
		}
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
//
// Order construction notes (US API):
//   - TokenID is set to m.marketInfo.YesTokenID, which the scanner populates
//     with the market slug. The exchange shim (userOrderToUSRequest) uses this
//     field as MarketSlug when calling POST /v1/orders.
//   - Side BUY/SELL is translated by the shim to ORDER_INTENT_BUY_LONG /
//     ORDER_INTENT_SELL_LONG respectively.
func (m *Maker) computeQuotes(mid, remainingBudget float64) (*types.QuotePair, error) {
	q := m.inventory.NetDelta() // [-1, 1]
	gamma := m.cfg.Gamma
	k := m.cfg.K
	T := m.cfg.T
	minSpread := float64(m.cfg.DefaultSpreadBps) / 10000.0
	tickDec := m.marketInfo.TickSize.Decimals()
	tick := math.Pow(10, -float64(tickDec))

	// Dynamic sigma: scale volatility by probability distance from certainty.
	// Markets near 50/50 are inherently more volatile (max entropy), while
	// markets near 0 or 1 are more stable. Formula:
	//   sigma = baseSigma * (0.3 + 0.7 * 4*mid*(1-mid))
	// The 4*mid*(1-mid) term peaks at 1.0 when mid=0.5 and drops to 0 at extremes.
	// Floor of 0.3 prevents sigma from collapsing entirely at extreme prices.
	probFactor := 4.0 * mid * (1.0 - mid) // 0..1, peaks at mid=0.5
	sigma := m.cfg.Sigma * (0.3 + 0.7*probFactor)

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

	// Step 2b: Clamp optimal spread to a sane range.
	// The theoretical A-S spread can be absurdly wide (>100%) when γ is small
	// relative to k. For prediction markets we cap at 30% or the actual book
	// spread * 1.5, whichever is larger. This keeps quotes competitive.
	maxSpreadHard := 0.30 // 30% absolute cap
	bookSpread := m.marketInfo.Spread
	if bookSpread > 0 {
		// Match the book spread to sit at the BBO. Using 1.0x means we
		// quote exactly at the current best bid/ask, giving us the best
		// chance of getting filled. The minSpread floor still protects us
		// from quoting inside absurdly tight spreads.
		maxSpreadFromBook := math.Max(bookSpread, minSpread)
		maxSpreadHard = math.Max(maxSpreadFromBook, minSpread)
		// But never exceed 30% regardless
		if maxSpreadHard > 0.30 {
			maxSpreadHard = 0.30
		}
	}
	if optSpread > maxSpreadHard {
		optSpread = maxSpreadHard
	}

	// Step 3: Raw bid/ask
	bidRaw := reservationPrice - optSpread/2
	askRaw := reservationPrice + optSpread/2

	// Step 4: Enforce minimum spread
	if (askRaw - bidRaw) < minSpread {
		bidRaw = reservationPrice - minSpread/2
		askRaw = reservationPrice + minSpread/2
	}

	// Step 4b: Liquidity reward targeting.
	// If the market offers liquidity rewards (RewardsMaxSpread > 0), try to
	// tighten the spread to qualify. Only tighten — never widen beyond what
	// the model computed. This makes rewards the most reliable income source
	// for small accounts.
	rewardsMaxSpread := m.marketInfo.RewardsMaxSpread
	if rewardsMaxSpread > 0 && (askRaw-bidRaw) > rewardsMaxSpread {
		// Only tighten if the reward spread is wider than our minimum spread floor
		if rewardsMaxSpread >= minSpread {
			bidRaw = reservationPrice - rewardsMaxSpread/2
			askRaw = reservationPrice + rewardsMaxSpread/2
		}
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

	// Step 7b: If liquidity rewards are active, ensure order sizes meet the
	// rewards minimum. This is often the most reliable income source.
	if m.marketInfo.RewardsMinSize > 0 {
		bidSize = math.Max(bidSize, m.marketInfo.RewardsMinSize)
		askSize = math.Max(askSize, m.marketInfo.RewardsMinSize)
	}

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
			// TokenID carries the market slug (YesTokenID = Slug after scanner migration).
			// The exchange shim reads this as MarketSlug for POST /v1/orders.
			// Side BUY is translated to ORDER_INTENT_BUY_LONG by the shim.
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
			// TokenID carries the market slug (YesTokenID = Slug after scanner migration).
			// Side SELL is translated to ORDER_INTENT_SELL_LONG by the shim.
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
		"prob_factor", probFactor,
		"dynamic_sigma", sigma,
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
// size is within 10% of the desired size. Everything else is cancelled
// individually via CancelOrder (with the market slug), then new orders are
// placed via PostOrders.
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

		if isBuySide(order.Side) && desired.Bid != nil {
			if math.Abs(orderPrice-desired.Bid.Price) <= tick &&
				math.Abs(remainingSize-desired.Bid.Size)/desired.Bid.Size <= sizeTolerance {
				matchedBid = true
				continue
			}
		}
		if isSellSide(order.Side) && desired.Ask != nil {
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

	// Cancel stale orders via the bulk cancel endpoint (POST /v1/orders/open/cancel).
	// The individual DELETE /v1/order/{id}/cancel endpoint returns HTTP 501 on the
	// US API, so we always use the bulk method which cancels all open orders for
	// the given market slug. This also clears any orders we lost track of.
	if len(toCancel) > 0 {
		resp, err := m.client.CancelMarketOrders(ctx, m.marketInfo.Slug)
		if err != nil {
			m.logger.Warn("bulk cancel failed, evicting stale order IDs", "error", err, "count", len(toCancel))
			// Evict the stale IDs from tracking to prevent infinite retry loops.
			// If cancel truly failed, the next tick will re-detect them via
			// reconciliation and try again.
			for _, id := range toCancel {
				delete(m.activeOrders, id)
			}
		} else {
			// Remove all cancelled order IDs from tracking
			for _, id := range resp.Canceled {
				delete(m.activeOrders, id)
			}
			// Also evict the IDs we intended to cancel, in case the server
			// cancelled them but returned different IDs (e.g. exchange-assigned vs local)
			for _, id := range toCancel {
				delete(m.activeOrders, id)
			}
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
				// Compute total filled quantity from instant executions.
				var cumFilled float64
				for _, exec := range result.Executions {
					eQty, _ := strconv.ParseFloat(exec.Quantity, 64)
					cumFilled += eQty
				}
				m.activeOrders[result.OrderID] = types.OpenOrder{
					ID:           result.OrderID,
					Status:       result.Status,
					Market:       m.marketInfo.ConditionID,
					AssetID:      toPlace[i].TokenID,
					Side:         string(toPlace[i].Side),
					Price:        fmt.Sprintf("%.4f", toPlace[i].Price),
					OriginalSize: fmt.Sprintf("%.2f", toPlace[i].Size),
					SizeMatched:  fmt.Sprintf("%.6f", cumFilled),
				}
				// Process instant fills that happened at placement time.
				// These would otherwise be missed if the WS is disconnected.
				for _, exec := range result.Executions {
					m.processInstantFill(result.OrderID, toPlace[i], exec)
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
// On the US API path this method is never called because TradeEvents() returns
// a nil channel; fills arrive instead as EXECUTION_TYPE_FILL order events and
// are handled by handleFillFromOrder. This method is retained for backward
// compatibility in case a non-nil tradeCh is ever wired by a caller.
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

	// Persist position after every fill (crash-safe)
	if m.positionSaver != nil {
		if err := m.positionSaver(m.marketInfo.ConditionID, pos); err != nil {
			m.logger.Error("failed to save position after fill", "error", err)
		}
	}
}

// handleFillFromOrder processes a fill derived from a WSOrderEvent carrying
// execution type EXECUTION_TYPE_FILL. The US API private feed delivers fills
// as order events rather than as separate trade events.
//
// Fill size is computed as the delta between the order's new cumulative
// quantity and the previously tracked cumulative quantity. If the order was
// not yet tracked (first fill on a newly placed order), cumQty is used directly.
func (m *Maker) handleFillFromOrder(event types.WSOrderEvent) {
	price, _ := strconv.ParseFloat(event.Price, 64)
	cumQty, _ := strconv.ParseFloat(event.SizeMatched, 64)

	// Compute incremental fill size from the change in cumulative quantity.
	var fillSize float64
	if tracked, ok := m.activeOrders[event.ID]; ok {
		prevMatched, _ := strconv.ParseFloat(tracked.SizeMatched, 64)
		fillSize = cumQty - prevMatched
	} else {
		fillSize = cumQty
	}
	if fillSize <= 0 {
		// Fallback: treat cumQty as the fill size if the delta is non-positive.
		// This can happen on the first event for a partially-filled resting order.
		fillSize = cumQty
	}
	if fillSize <= 0 {
		return // Nothing to process
	}

	// Normalize the side string. The US API WS private feed may send
	// intent-format strings ("ORDER_INTENT_BUY_LONG") instead of "BUY".
	var side types.Side
	if isBuySide(event.Side) {
		side = types.BUY
	} else {
		side = types.SELL
	}

	fill := Fill{
		Timestamp: time.Now(),
		Side:      side,
		TokenID:   event.AssetID,
		Price:     price,
		Size:      fillSize,
		TradeID:   event.ID,
	}

	m.inventory.OnFill(fill)
	m.flowTracker.AddFill(fill)

	pos := m.inventory.Snapshot()

	// Check toxicity after fill
	toxicity := m.flowTracker.CalculateToxicity()
	if toxicity.IsAverse {
		m.logger.Warn("toxic flow detected",
			"side", event.Side,
			"toxicity_score", toxicity.ToxicityScore,
			"directional_imbalance", toxicity.DirectionalImbalance,
			"fill_velocity", toxicity.FillVelocity,
			"fill_count", m.flowTracker.GetFillCount(),
		)
	}

	m.logger.Info("fill",
		"side", event.Side,
		"price", price,
		"size", fillSize,
		"cum_qty", cumQty,
		"order_id", event.ID,
		"yes_qty", pos.YesQty,
		"no_qty", pos.NoQty,
		"realized_pnl", pos.RealizedPnL,
	)

	// Emit fill event to dashboard (synthesise a WSTradeEvent for the legacy helper)
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

	// Build a synthetic WSTradeEvent so NewFillEvent (which expects that type)
	// receives all fields it needs.
	syntheticTrade := types.WSTradeEvent{
		ID:      event.ID,
		AssetID: event.AssetID,
		Side:    event.Side,
		Price:   event.Price,
		Size:    fmt.Sprintf("%.6f", fillSize),
		Outcome: "Yes", // US API orders target YES long; adjust if needed
	}

	m.emitDashboardEvent(api.DashboardEvent{
		Type:      "fill",
		Timestamp: time.Now(),
		MarketID:  m.marketInfo.ConditionID,
		Data:      api.NewFillEvent(syntheticTrade, posSnapshot, m.marketInfo.Slug, price, fillSize),
	})

	// Persist position after every fill (crash-safe)
	if m.positionSaver != nil {
		if err := m.positionSaver(m.marketInfo.ConditionID, pos); err != nil {
			m.logger.Error("failed to save position after fill", "error", err)
		}
	}
}

// processInstantFill handles a fill that was returned synchronously in the
// PlaceOrder REST response. This is critical because:
//  1. Orders that cross the spread fill immediately at placement time.
//  2. The WS private feed may be disconnected when this happens.
//  3. Without processing these, the bot loses capital invisibly.
func (m *Maker) processInstantFill(orderID string, placed types.UserOrder, exec types.USExecution) {
	price, _ := strconv.ParseFloat(exec.Price.Value, 64)
	qty, _ := strconv.ParseFloat(exec.Quantity, 64)
	if qty <= 0 {
		return
	}

	side := placed.Side

	fill := Fill{
		Timestamp: time.Now(),
		Side:      side,
		TokenID:   placed.TokenID,
		Price:     price,
		Size:      qty,
		TradeID:   exec.ID,
	}

	m.inventory.OnFill(fill)
	m.flowTracker.AddFill(fill)

	pos := m.inventory.Snapshot()

	m.logger.Info("fill",
		"source", "instant",
		"side", side,
		"price", price,
		"size", qty,
		"order_id", orderID,
		"exec_id", exec.ID,
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

	syntheticTrade := types.WSTradeEvent{
		ID:      exec.ID,
		AssetID: placed.TokenID,
		Side:    string(side),
		Price:   exec.Price.Value,
		Size:    exec.Quantity,
		Outcome: "Yes",
	}

	m.emitDashboardEvent(api.DashboardEvent{
		Type:      "fill",
		Timestamp: time.Now(),
		MarketID:  m.marketInfo.ConditionID,
		Data:      api.NewFillEvent(syntheticTrade, posSnapshot, m.marketInfo.Slug, price, qty),
	})

	if m.positionSaver != nil {
		if err := m.positionSaver(m.marketInfo.ConditionID, pos); err != nil {
			m.logger.Error("failed to save position after instant fill", "error", err)
		}
	}
}

// handleOrderEvent processes order lifecycle events from the WS private feed.
//
// The US API private feed emits USWSPrivateEvent objects which the WS layer
// translates to WSOrderEvent with the execution type in the Type field:
//
//   - "EXECUTION_TYPE_NEW"      — order accepted and live on the book
//   - "EXECUTION_TYPE_FILL"     — order partially or fully filled
//   - "EXECUTION_TYPE_CANCELED" — order cancelled
//   - "EXECUTION_TYPE_EXPIRED"  — order expired (treated as cancelled)
//   - "EXECUTION_TYPE_REJECTED" — order rejected
//
// Legacy type strings ("PLACEMENT", "UPDATE", "CANCELLATION") are also handled
// for backward compatibility.
func (m *Maker) handleOrderEvent(event types.WSOrderEvent) {
	switch event.Type {
	case "EXECUTION_TYPE_CANCELED", "EXECUTION_TYPE_EXPIRED", "EXECUTION_TYPE_REJECTED",
		"CANCELLATION":
		delete(m.activeOrders, event.ID)

	case "EXECUTION_TYPE_FILL":
		// Process inventory / dashboard / persistence for this fill.
		m.handleFillFromOrder(event)
		// Update the tracked cumulative quantity so subsequent fill events
		// compute correct incremental sizes.
		if order, ok := m.activeOrders[event.ID]; ok {
			order.SizeMatched = event.SizeMatched
			m.activeOrders[event.ID] = order
		} else {
			// Order was not previously tracked (e.g., NEW event lost during WS disconnect).
			// Register it now so subsequent fill events can compute correct deltas.
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

	case "EXECUTION_TYPE_NEW", "PLACEMENT":
		// Register the order in our local tracking map if not already present.
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

	case "UPDATE":
		// Update the matched quantity for an existing order.
		if order, ok := m.activeOrders[event.ID]; ok {
			order.SizeMatched = event.SizeMatched
			m.activeOrders[event.ID] = order
		}
	}
}

// cancelAllMyOrders cancels all active orders for this market.
// CancelMarketOrders wraps CancelMarketOrdersBySlugs, and since ConditionID
// is set to the market slug in the new architecture, this call is correct.
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

// ————————————————————————————————————————————————————————————————————————
// Side helpers
// ————————————————————————————————————————————————————————————————————————

// isBuySide returns true for both the canonical "BUY" string and the US API
// intent string "ORDER_INTENT_BUY_LONG". This is needed because orders placed
// by the bot are tracked locally with "BUY"/"SELL", but orders arriving via
// the WS private feed may carry the intent string instead.
func isBuySide(s string) bool {
	return s == "BUY" || s == string(types.IntentBuyLong)
}

// isSellSide returns true for both "SELL" and "ORDER_INTENT_SELL_LONG".
func isSellSide(s string) bool {
	return s == "SELL" || s == string(types.IntentSellLong)
}

// ————————————————————————————————————————————————————————————————————————
// Math helpers
// ————————————————————————————————————————————————————————————————————————

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
