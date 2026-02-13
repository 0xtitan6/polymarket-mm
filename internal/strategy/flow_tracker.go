// Package strategy implements toxic flow detection for market making.
// FlowTracker monitors recent fills to detect adverse selection and inform spread adjustments.
package strategy

import (
	"math"
	"sync"
	"time"

	"polymarket-mm/pkg/types"
)

// ToxicityMetrics contains calculated adverse selection indicators.
type ToxicityMetrics struct {
	DirectionalImbalance float64 // [0, 1]: % of fills in dominant direction
	FillVelocity         float64 // Fills per minute
	ToxicityScore        float64 // [0, 1]: Composite toxicity score
	IsAverse             bool    // True if likely getting adversely selected
}

// FlowTracker tracks recent fills in a rolling time window to detect toxic flow patterns.
// Toxic flow = fills that consistently go in one direction, suggesting informed traders
// are picking off stale quotes right before price moves.
type FlowTracker struct {
	mu sync.RWMutex

	windowDuration time.Duration // How far back to look (e.g., 60s)
	fills          []Fill        // Rolling window of recent fills

	// Config
	toxicityThreshold  float64       // Score above this triggers spread widening
	cooldownPeriod     time.Duration // Stay wide after toxicity detected
	maxSpreadMultiple  float64       // Max spread multiplier (e.g., 3.0x)

	// State
	lastToxicTime time.Time // Last time toxicity was detected
}

// NewFlowTracker creates a flow tracker with the given configuration.
func NewFlowTracker(windowDuration time.Duration, toxicityThreshold float64, cooldownPeriod time.Duration, maxSpreadMultiple float64) *FlowTracker {
	return &FlowTracker{
		windowDuration:    windowDuration,
		fills:             make([]Fill, 0, 100),
		toxicityThreshold: toxicityThreshold,
		cooldownPeriod:    cooldownPeriod,
		maxSpreadMultiple: maxSpreadMultiple,
	}
}

// AddFill adds a new fill to the tracker and evicts stale entries outside the window.
func (ft *FlowTracker) AddFill(fill Fill) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	ft.fills = append(ft.fills, fill)
	ft.evictStaleLocked()
}

// evictStaleLocked removes fills older than the window duration.
// Must be called with lock held.
func (ft *FlowTracker) evictStaleLocked() {
	if len(ft.fills) == 0 {
		return
	}

	cutoff := time.Now().Add(-ft.windowDuration)
	validIdx := -1
	for i, fill := range ft.fills {
		if fill.Timestamp.After(cutoff) {
			validIdx = i
			break
		}
	}

	// If no valid fills found, clear all
	if validIdx == -1 {
		ft.fills = ft.fills[:0]
		return
	}

	// Keep only valid fills
	if validIdx > 0 {
		ft.fills = ft.fills[validIdx:]
	}
}

// CalculateToxicity computes adverse selection metrics from recent fills.
func (ft *FlowTracker) CalculateToxicity() ToxicityMetrics {
	ft.mu.Lock()
	ft.evictStaleLocked()
	ft.mu.Unlock()

	ft.mu.RLock()
	defer ft.mu.RUnlock()

	if len(ft.fills) == 0 {
		return ToxicityMetrics{}
	}

	// Count fills by side
	var buyCount, sellCount int
	for _, fill := range ft.fills {
		if fill.Side == types.BUY {
			buyCount++
		} else {
			sellCount++
		}
	}

	totalFills := len(ft.fills)

	// Directional imbalance: % of fills in the dominant direction
	dominant := math.Max(float64(buyCount), float64(sellCount))
	directionalImbalance := dominant / float64(totalFills)

	// Fill velocity: fills per minute
	if len(ft.fills) < 2 {
		return ToxicityMetrics{
			DirectionalImbalance: directionalImbalance,
			FillVelocity:         0,
			ToxicityScore:        directionalImbalance * 0.6, // Only directional component
			IsAverse:             directionalImbalance > ft.toxicityThreshold,
		}
	}

	windowDurationMinutes := ft.windowDuration.Minutes()
	fillVelocity := float64(totalFills) / windowDurationMinutes

	// Normalize velocity: >3 fills/min = very high (score 1.0)
	// This is aggressive for prediction markets
	velocityFactor := math.Min(fillVelocity/3.0, 1.0)

	// Composite toxicity score:
	// - 60% weight on directional imbalance (most important signal)
	// - 40% weight on fill velocity (burst of fills suggests sweep)
	toxicityScore := 0.6*directionalImbalance + 0.4*velocityFactor

	return ToxicityMetrics{
		DirectionalImbalance: directionalImbalance,
		FillVelocity:         fillVelocity,
		ToxicityScore:        toxicityScore,
		IsAverse:             toxicityScore > ft.toxicityThreshold,
	}
}

// GetSpreadMultiplier returns the spread multiplier to apply based on current toxicity.
// Returns 1.0 (no change) under normal conditions, up to maxSpreadMultiple when toxic.
func (ft *FlowTracker) GetSpreadMultiplier() float64 {
	metrics := ft.CalculateToxicity()

	// Update last toxic time if currently toxic
	if metrics.IsAverse {
		ft.mu.Lock()
		ft.lastToxicTime = time.Now()
		ft.mu.Unlock()
	}

	// Check if in cooldown period
	ft.mu.RLock()
	inCooldown := time.Since(ft.lastToxicTime) < ft.cooldownPeriod
	ft.mu.RUnlock()

	if !metrics.IsAverse && !inCooldown {
		return 1.0 // Normal spread
	}

	// During toxicity or cooldown: widen spread proportionally
	// Linear interpolation between 1.0x and maxSpreadMultiple based on toxicity score
	if metrics.ToxicityScore < ft.toxicityThreshold {
		// In cooldown but not currently toxic: gradually return to normal
		timeSinceToxic := time.Since(ft.lastToxicTime).Seconds()
		cooldownSeconds := ft.cooldownPeriod.Seconds()
		cooldownProgress := math.Min(timeSinceToxic/cooldownSeconds, 1.0)

		// Decay from max back to 1.0
		return 1.0 + (ft.maxSpreadMultiple-1.0)*(1.0-cooldownProgress)
	}

	// Currently toxic: scale multiplier by score
	// Score 0.6 (threshold) → 2.0x
	// Score 1.0 (max) → maxSpreadMultiple
	normalizedScore := (metrics.ToxicityScore - ft.toxicityThreshold) / (1.0 - ft.toxicityThreshold)
	return 1.0 + (ft.maxSpreadMultiple-1.0)*math.Min(normalizedScore*2.0, 1.0)
}

// IsFlowToxic returns true if current flow is showing adverse selection.
func (ft *FlowTracker) IsFlowToxic() bool {
	metrics := ft.CalculateToxicity()
	return metrics.IsAverse
}

// GetFillCount returns the number of fills in the current window.
func (ft *FlowTracker) GetFillCount() int {
	ft.mu.RLock()
	defer ft.mu.RUnlock()
	return len(ft.fills)
}
