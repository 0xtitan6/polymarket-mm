package api

import (
	"time"

	"polymarket-mm/internal/config"
	"polymarket-mm/internal/market"
	"polymarket-mm/internal/risk"
)

// MarketSnapshotProvider provides snapshot access to market state
type MarketSnapshotProvider interface {
	GetMarketsSnapshot() []MarketStatus
	GetScanner() *market.Scanner
	GetRiskManager() *risk.Manager
}

// BuildSnapshot aggregates state from all components into a dashboard snapshot
func BuildSnapshot(
	provider MarketSnapshotProvider,
	cfg config.Config,
) DashboardSnapshot {
	// Get market snapshots
	markets := provider.GetMarketsSnapshot()

	// Get risk snapshot
	riskMgr := provider.GetRiskManager()
	riskSnap := riskMgr.GetRiskSnapshot()

	// Calculate aggregate P&L
	var totalRealized, totalUnrealized float64
	for _, m := range markets {
		totalRealized += m.Position.RealizedPnL
		totalUnrealized += m.Position.UnrealizedPnL
	}

	// Get scanner info
	_ = provider.GetScanner() // TODO: extract stats from scanner
	scannerInfo := ScannerInfo{
		LastScanTime:     time.Now(), // TODO: get from scanner
		MarketsScanned:   0,          // TODO: get from scanner
		MarketsFiltered:  0,          // TODO: get from scanner
		MarketsSelected:  len(markets),
	}

	return DashboardSnapshot{
		Timestamp:       time.Now(),
		Markets:         markets,
		TotalRealized:   totalRealized,
		TotalUnrealized: totalUnrealized,
		TotalPnL:        totalRealized + totalUnrealized,
		Risk:            convertRiskSnapshot(riskSnap),
		Config:          NewConfigSummary(cfg),
		Scanner:         scannerInfo,
	}
}

// convertRiskSnapshot converts internal risk snapshot to API format
func convertRiskSnapshot(snap risk.RiskSnapshot) RiskSnapshot {
	return RiskSnapshot{
		GlobalExposure:       snap.GlobalExposure,
		MaxGlobalExposure:    snap.MaxGlobalExposure,
		ExposurePct:          snap.ExposurePct,
		KillSwitchActive:     snap.KillSwitchActive,
		KillSwitchUntil:      snap.KillSwitchUntil,
		KillSwitchReason:     snap.KillSwitchReason,
		TotalRealizedPnL:     snap.TotalRealizedPnL,
		TotalUnrealizedPnL:   snap.TotalUnrealizedPnL,
		MaxPositionPerMarket: snap.MaxPositionPerMarket,
		MaxDailyLoss:         snap.MaxDailyLoss,
		MaxMarketsActive:     snap.MaxMarketsActive,
		CurrentMarketsActive: snap.CurrentMarketsActive,
	}
}
