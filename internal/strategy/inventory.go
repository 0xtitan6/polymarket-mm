package strategy

import (
	"math"
	"sync"
	"time"

	"polymarket-mm/pkg/types"
)

// Position represents current holdings in a single market.
// Serialized to JSON for persistence across bot restarts.
type Position struct {
	YesQty        float64   `json:"yes_qty"`
	NoQty         float64   `json:"no_qty"`
	AvgEntryYes   float64   `json:"avg_entry_yes"`
	AvgEntryNo    float64   `json:"avg_entry_no"`
	RealizedPnL   float64   `json:"realized_pnl"`
	UnrealizedPnL float64   `json:"unrealized_pnl"`
	LastUpdated   time.Time `json:"last_updated"`
}

// Fill records a single execution.
type Fill struct {
	Timestamp time.Time  `json:"timestamp"`
	Side      types.Side `json:"side"`
	TokenID   string     `json:"token_id"`
	Price     float64    `json:"price"`
	Size      float64    `json:"size"`
	TradeID   string     `json:"trade_id"`
}

// Inventory tracks the position for one market. Thread-safe via RWMutex.
// It handles fill processing, PnL tracking, and provides inventory skew (NetDelta)
// that drives the Avellaneda-Stoikov reservation price adjustment.
type Inventory struct {
	mu       sync.RWMutex
	marketID string
	yesToken string
	noToken  string
	pos      Position
}

// NewInventory creates inventory tracking for a market.
func NewInventory(marketID, yesToken, noToken string) *Inventory {
	return &Inventory{
		marketID: marketID,
		yesToken: yesToken,
		noToken:  noToken,
	}
}

// OnFill processes a fill event. Updates quantities and average entry prices.
// When a position is reduced, realized PnL is calculated.
func (inv *Inventory) OnFill(fill Fill) {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	if fill.TokenID == inv.yesToken {
		inv.applyYesFill(fill)
	} else {
		inv.applyNoFill(fill)
	}

	inv.pos.LastUpdated = time.Now()
}

func (inv *Inventory) applyYesFill(fill Fill) {
	if fill.Side == types.BUY {
		// Buying YES: increase position
		totalCost := inv.pos.AvgEntryYes*inv.pos.YesQty + fill.Price*fill.Size
		inv.pos.YesQty += fill.Size
		if inv.pos.YesQty > 0 {
			inv.pos.AvgEntryYes = totalCost / inv.pos.YesQty
		}
	} else {
		// Selling YES: reduce position, realize PnL
		if inv.pos.YesQty > 0 {
			sellQty := math.Min(fill.Size, inv.pos.YesQty)
			inv.pos.RealizedPnL += (fill.Price - inv.pos.AvgEntryYes) * sellQty
		}
		inv.pos.YesQty -= fill.Size
		if inv.pos.YesQty <= 0 {
			inv.pos.YesQty = 0
			inv.pos.AvgEntryYes = 0
		}
	}
}

func (inv *Inventory) applyNoFill(fill Fill) {
	if fill.Side == types.BUY {
		totalCost := inv.pos.AvgEntryNo*inv.pos.NoQty + fill.Price*fill.Size
		inv.pos.NoQty += fill.Size
		if inv.pos.NoQty > 0 {
			inv.pos.AvgEntryNo = totalCost / inv.pos.NoQty
		}
	} else {
		if inv.pos.NoQty > 0 {
			sellQty := math.Min(fill.Size, inv.pos.NoQty)
			inv.pos.RealizedPnL += (fill.Price - inv.pos.AvgEntryNo) * sellQty
		}
		inv.pos.NoQty -= fill.Size
		if inv.pos.NoQty <= 0 {
			inv.pos.NoQty = 0
			inv.pos.AvgEntryNo = 0
		}
	}
}

// Snapshot returns a copy of the current position.
func (inv *Inventory) Snapshot() Position {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	return inv.pos
}

// NetDelta returns inventory skew in [-1, 1].
// +1 = fully long YES, -1 = fully long NO, 0 = balanced.
// This is the "q" parameter in the Avellaneda-Stoikov model that skews quotes
// to reduce directional exposure.
func (inv *Inventory) NetDelta() float64 {
	inv.mu.RLock()
	defer inv.mu.RUnlock()

	total := inv.pos.YesQty + inv.pos.NoQty
	if total == 0 {
		return 0
	}
	return (inv.pos.YesQty - inv.pos.NoQty) / total
}

// TotalExposureUSD returns the dollar value of all holdings.
// In binary markets: YES is worth midPrice, NO is worth (1 - midPrice).
func (inv *Inventory) TotalExposureUSD(midPrice float64) float64 {
	inv.mu.RLock()
	defer inv.mu.RUnlock()

	return inv.pos.YesQty*midPrice + inv.pos.NoQty*(1-midPrice)
}

// UpdateMarkToMarket recalculates unrealized PnL.
func (inv *Inventory) UpdateMarkToMarket(midPrice float64) {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	yesUnreal := inv.pos.YesQty * (midPrice - inv.pos.AvgEntryYes)
	noUnreal := inv.pos.NoQty * ((1 - midPrice) - inv.pos.AvgEntryNo)
	inv.pos.UnrealizedPnL = yesUnreal + noUnreal
}

// SetPosition restores position from persistence (used on restart).
func (inv *Inventory) SetPosition(pos Position) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.pos = pos
}
