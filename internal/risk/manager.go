// Package risk enforces portfolio-level risk limits across all active markets.
//
// The risk manager runs as a standalone goroutine that receives PositionReports
// from each market's strategy loop and checks them against configured limits:
//
//   - Per-market exposure:  caps USD exposure in any single market
//   - Global exposure:      caps total USD exposure across all markets
//   - Daily loss:           triggers kill switch if realized+unrealized PnL exceeds threshold
//   - Rapid price movement: triggers kill switch if mid-price moves more than
//     KillSwitchDropPct within KillSwitchWindowSec seconds
//
// When a limit is breached, the manager emits a KillSignal on KillCh(). The
// engine reads this signal and cancels all orders (globally or per-market).
// After a kill, the kill switch stays active for CooldownAfterKill duration,
// during which the strategy skips quoting.
package risk

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"polymarket-mm/internal/config"
)

// PositionReport is sent by each market's strategy goroutine every quote cycle.
// It contains the current inventory state and PnL for risk evaluation.
type PositionReport struct {
	MarketID      string
	YesQty        float64 // YES tokens held
	NoQty         float64 // NO tokens held
	MidPrice      float64 // current mid price (used for price-movement detection)
	ExposureUSD   float64 // total position value in USD
	UnrealizedPnL float64 // mark-to-market PnL
	RealizedPnL   float64 // locked-in PnL from closed trades
	Timestamp     time.Time
}

// KillSignal tells the engine to cancel all orders. If MarketID is empty,
// it means cancel across ALL markets (global kill).
type KillSignal struct {
	MarketID string // empty = kill ALL markets
	Reason   string
}

// priceAnchor stores a reference price at a point in time for detecting
// rapid price movements within a rolling window.
type priceAnchor struct {
	price     float64
	timestamp time.Time
}

// Manager enforces risk limits across all active markets. It aggregates
// position reports, checks limits, and emits kill signals when breached.
type Manager struct {
	cfg    config.RiskConfig
	logger *slog.Logger

	mu               sync.RWMutex
	positions        map[string]PositionReport // latest report per market
	totalExposure    float64                   // sum of all ExposureUSD
	totalRealizedPnL float64                   // sum of all RealizedPnL
	killSwitchActive bool                      // true while in cooldown
	killSwitchUntil  time.Time                 // when cooldown expires
	priceAnchors     map[string]priceAnchor    // reference prices for movement detection

	reportCh chan PositionReport // strategy goroutines write here
	killCh   chan KillSignal     // engine reads kill signals from here
}

// NewManager creates a risk manager.
func NewManager(cfg config.RiskConfig, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:          cfg,
		logger:       logger.With("component", "risk"),
		positions:    make(map[string]PositionReport),
		priceAnchors: make(map[string]priceAnchor),
		reportCh:     make(chan PositionReport, 100),
		killCh:       make(chan KillSignal, 10),
	}
}

// Run starts the risk monitoring loop.
func (rm *Manager) Run(ctx context.Context) {
	// Periodic check clears kill switch even when no reports arrive
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case report := <-rm.reportCh:
			rm.processReport(report)
		case <-ticker.C:
			rm.clearExpiredKillSwitch()
		}
	}
}

// Report submits a position report (non-blocking).
func (rm *Manager) Report(report PositionReport) {
	select {
	case rm.reportCh <- report:
	default:
		rm.logger.Warn("risk report channel full, dropping report",
			"market", report.MarketID)
	}
}

// KillCh returns the channel for reading kill signals.
func (rm *Manager) KillCh() <-chan KillSignal {
	return rm.killCh
}

// RemoveMarket cleans up state for a stopped market.
func (rm *Manager) RemoveMarket(marketID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	delete(rm.positions, marketID)
	delete(rm.priceAnchors, marketID)
}

// IsKillSwitchActive returns whether the kill switch is engaged.
func (rm *Manager) IsKillSwitchActive() bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if !rm.killSwitchActive {
		return false
	}
	if time.Now().After(rm.killSwitchUntil) {
		rm.killSwitchActive = false
		rm.logger.Info("kill switch cooldown expired")
		return false
	}
	return true
}

// RemainingBudget returns how much additional USD exposure is allowed for
// the given market. It takes the minimum of:
//   - per-market headroom: MaxPositionPerMarket − current market exposure
//   - global headroom:     MaxGlobalExposure − total exposure across all markets
//
// Returns 0 if either limit is already exceeded (the strategy will skip quoting).
func (rm *Manager) RemainingBudget(marketID string) float64 {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	var currentExposure float64
	if pos, ok := rm.positions[marketID]; ok {
		currentExposure = pos.ExposureUSD
	}

	perMarket := rm.cfg.MaxPositionPerMarket - currentExposure
	global := rm.cfg.MaxGlobalExposure - rm.totalExposure

	remaining := perMarket
	if global < remaining {
		remaining = global
	}
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetRiskSnapshot returns current aggregate risk metrics for dashboard.
func (rm *Manager) GetRiskSnapshot() RiskSnapshot {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	var totalUnrealizedPnL float64
	for _, pos := range rm.positions {
		totalUnrealizedPnL += pos.UnrealizedPnL
	}

	var exposurePct float64
	if rm.cfg.MaxGlobalExposure > 0 {
		exposurePct = (rm.totalExposure / rm.cfg.MaxGlobalExposure) * 100
	}

	var killReason string
	if rm.killSwitchActive {
		killReason = "cooldown"
	}

	return RiskSnapshot{
		GlobalExposure:       rm.totalExposure,
		MaxGlobalExposure:    rm.cfg.MaxGlobalExposure,
		ExposurePct:          exposurePct,
		KillSwitchActive:     rm.killSwitchActive,
		KillSwitchUntil:      rm.killSwitchUntil,
		KillSwitchReason:     killReason,
		TotalRealizedPnL:     rm.totalRealizedPnL,
		TotalUnrealizedPnL:   totalUnrealizedPnL,
		MaxPositionPerMarket: rm.cfg.MaxPositionPerMarket,
		MaxDailyLoss:         rm.cfg.MaxDailyLoss,
		MaxMarketsActive:     rm.cfg.MaxMarketsActive,
		CurrentMarketsActive: len(rm.positions),
	}
}

// RiskSnapshot represents aggregate risk metrics for dashboard
type RiskSnapshot struct {
	GlobalExposure       float64
	MaxGlobalExposure    float64
	ExposurePct          float64
	KillSwitchActive     bool
	KillSwitchUntil      time.Time
	KillSwitchReason     string
	TotalRealizedPnL     float64
	TotalUnrealizedPnL   float64
	MaxPositionPerMarket float64
	MaxDailyLoss         float64
	MaxMarketsActive     int
	CurrentMarketsActive int
}

func (rm *Manager) processReport(report PositionReport) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.positions[report.MarketID] = report

	// Recalculate totals
	rm.totalExposure = 0
	rm.totalRealizedPnL = 0
	totalUnrealizedPnL := 0.0
	for _, pos := range rm.positions {
		rm.totalExposure += pos.ExposureUSD
		rm.totalRealizedPnL += pos.RealizedPnL
		totalUnrealizedPnL += pos.UnrealizedPnL
	}

	// Check per-market limit
	if report.ExposureUSD > rm.cfg.MaxPositionPerMarket {
		rm.emitKill(report.MarketID, "per-market position limit breached")
	}

	// Check global limit
	if rm.totalExposure > rm.cfg.MaxGlobalExposure {
		rm.emitKill("", "global exposure limit breached")
	}

	// Check daily loss
	totalPnL := rm.totalRealizedPnL + totalUnrealizedPnL
	if totalPnL < -rm.cfg.MaxDailyLoss {
		rm.emitKill("", "max daily loss breached")
	}

	// Check rapid price movement (kill switch)
	rm.checkPriceMovement(report)

}

// checkPriceMovement detects rapid price swings using a rolling anchor.
// On each report, it compares mid-price to the anchor set at the start of
// the window. If the anchor is older than KillSwitchWindowSec, it resets.
// If price moved more than KillSwitchDropPct from anchor, kill switch fires.
func (rm *Manager) checkPriceMovement(report PositionReport) {
	window := time.Duration(rm.cfg.KillSwitchWindowSec) * time.Second

	anchor, ok := rm.priceAnchors[report.MarketID]
	if !ok || report.Timestamp.Sub(anchor.timestamp) > window {
		// No anchor or anchor expired — reset to current price
		rm.priceAnchors[report.MarketID] = priceAnchor{
			price:     report.MidPrice,
			timestamp: report.Timestamp,
		}
		return
	}

	if anchor.price == 0 {
		return
	}

	pctChange := (report.MidPrice - anchor.price) / anchor.price
	if pctChange < 0 {
		pctChange = -pctChange
	}

	if pctChange > rm.cfg.KillSwitchDropPct {
		rm.emitKill(report.MarketID, fmt.Sprintf(
			"rapid price movement: %.1f%% in %ds",
			pctChange*100, rm.cfg.KillSwitchWindowSec,
		))
	}
}

func (rm *Manager) clearExpiredKillSwitch() {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rm.killSwitchActive && time.Now().After(rm.killSwitchUntil) {
		rm.killSwitchActive = false
		rm.logger.Info("kill switch cooldown expired")
	}
}

// emitKill activates the kill switch, starts the cooldown timer, and sends
// a KillSignal to the engine. If the kill channel is full, it drains the
// stale signal first to ensure the latest kill reason is always delivered.
func (rm *Manager) emitKill(marketID, reason string) {
	rm.killSwitchActive = true
	rm.killSwitchUntil = time.Now().Add(rm.cfg.CooldownAfterKill)

	rm.logger.Error("KILL SWITCH",
		"market", marketID,
		"reason", reason,
		"cooldown_until", rm.killSwitchUntil,
	)

	// Drain stale signal if channel full, then send
	sig := KillSignal{MarketID: marketID, Reason: reason}
	select {
	case rm.killCh <- sig:
	default:
		select {
		case <-rm.killCh:
		default:
		}
		rm.killCh <- sig
	}
}

