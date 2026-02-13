package api

import (
	"time"

	"polymarket-mm/pkg/types"
)

// DashboardEvent is the wrapper for all events sent to the dashboard
type DashboardEvent struct {
	Type      string      `json:"type"`      // "snapshot", "fill", "order", "position", "kill"
	Timestamp time.Time   `json:"timestamp"` // Event time
	MarketID  string      `json:"market_id"` // Condition ID (empty for global events)
	Data      interface{} `json:"data"`      // Event-specific payload
}

// FillEvent represents a trade fill notification
type FillEvent struct {
	OrderID    string  `json:"order_id"`
	Side       string  `json:"side"`        // "BUY" or "SELL"
	TokenType  string  `json:"token_type"`  // "YES" or "NO"
	Price      float64 `json:"price"`
	Size       float64 `json:"size"`
	MarketSlug string  `json:"market_slug"` // Human-readable market name
	// Position after fill
	YesQty         float64 `json:"yes_qty"`
	NoQty          float64 `json:"no_qty"`
	RealizedPnL    float64 `json:"realized_pnl"`
	UnrealizedPnL  float64 `json:"unrealized_pnl"`
}

// OrderEvent represents order placement/cancellation
type OrderEvent struct {
	OrderID   string  `json:"order_id"`
	Status    string  `json:"status"`     // "PLACED", "CANCELLED", "FILLED"
	Side      string  `json:"side"`       // "BUY" or "SELL"
	TokenType string  `json:"token_type"` // "YES" or "NO"
	Price     float64 `json:"price"`
	Size      float64 `json:"size"`
}

// PositionEvent is emitted when position changes
type PositionEvent struct {
	MarketSlug     string  `json:"market_slug"`
	YesQty         float64 `json:"yes_qty"`
	NoQty          float64 `json:"no_qty"`
	AvgEntryYes    float64 `json:"avg_entry_yes"`
	AvgEntryNo     float64 `json:"avg_entry_no"`
	RealizedPnL    float64 `json:"realized_pnl"`
	UnrealizedPnL  float64 `json:"unrealized_pnl"`
	ExposureUSD    float64 `json:"exposure_usd"`
	MidPrice       float64 `json:"mid_price"`
}

// KillEvent is emitted when kill switch activates
type KillEvent struct {
	Reason   string    `json:"reason"`
	Details  string    `json:"details"`
	Until    time.Time `json:"until"` // Cooldown expiry
	MarketID string    `json:"market_id,omitempty"`
}

// QuoteEvent represents current bid/ask quotes
type QuoteEvent struct {
	MarketSlug       string   `json:"market_slug"`
	BidPrice         float64  `json:"bid_price"`
	BidSize          float64  `json:"bid_size"`
	AskPrice         float64  `json:"ask_price"`
	AskSize          float64  `json:"ask_size"`
	ReservationPrice float64  `json:"reservation_price"`
	OptimalSpread    float64  `json:"optimal_spread"`
	MidPrice         float64  `json:"mid_price"`
}

// BookUpdateEvent represents order book changes
type BookUpdateEvent struct {
	MarketSlug string    `json:"market_slug"`
	BestBid    float64   `json:"best_bid"`
	BestAsk    float64   `json:"best_ask"`
	MidPrice   float64   `json:"mid_price"`
	Spread     float64   `json:"spread"`
	UpdateTime time.Time `json:"update_time"`
}

// NewFillEvent creates a fill event from trade data
func NewFillEvent(trade types.WSTradeEvent, pos PositionSnapshot, marketSlug string, price, size float64) FillEvent {
	return FillEvent{
		OrderID:        trade.ID,
		Side:           trade.Side,
		TokenType:      trade.Outcome, // "Yes" or "No"
		Price:          price,
		Size:           size,
		MarketSlug:     marketSlug,
		YesQty:         pos.YesQty,
		NoQty:          pos.NoQty,
		RealizedPnL:    pos.RealizedPnL,
		UnrealizedPnL:  pos.UnrealizedPnL,
	}
}

// NewOrderEvent creates an order event
func NewOrderEvent(orderID, status, side string, price, size float64) OrderEvent {
	return OrderEvent{
		OrderID:   orderID,
		Status:    status,
		Side:      side,
		TokenType: "YES", // TODO: determine from asset ID
		Price:     price,
		Size:      size,
	}
}

// NewPositionEvent creates a position event
func NewPositionEvent(pos PositionSnapshot, marketSlug string, midPrice float64) PositionEvent {
	return PositionEvent{
		MarketSlug:    marketSlug,
		YesQty:        pos.YesQty,
		NoQty:         pos.NoQty,
		AvgEntryYes:   pos.AvgEntryYes,
		AvgEntryNo:    pos.AvgEntryNo,
		RealizedPnL:   pos.RealizedPnL,
		UnrealizedPnL: pos.UnrealizedPnL,
		ExposureUSD:   pos.ExposureUSD,
		MidPrice:      midPrice,
	}
}

// NewKillEvent creates a kill switch event
func NewKillEvent(reason, details string, until time.Time, marketID string) KillEvent {
	return KillEvent{
		Reason:   reason,
		Details:  details,
		Until:    until,
		MarketID: marketID,
	}
}
