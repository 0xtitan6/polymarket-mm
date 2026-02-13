package risk

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"polymarket-mm/internal/config"
)

func testRiskConfig() config.RiskConfig {
	return config.RiskConfig{
		MaxPositionPerMarket: 100,
		MaxGlobalExposure:    500,
		MaxMarketsActive:     5,
		KillSwitchDropPct:    0.10, // 10%
		KillSwitchWindowSec:  60,
		MaxDailyLoss:         50,
		CooldownAfterKill:    5 * time.Minute,
	}
}

func newTestManager() *Manager {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewManager(testRiskConfig(), logger)
}

func TestProcessReportUnderLimits(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	rm.processReport(PositionReport{
		MarketID:      "m1",
		ExposureUSD:   50,
		RealizedPnL:   0,
		UnrealizedPnL: 0,
		MidPrice:      0.50,
		Timestamp:     time.Now(),
	})

	if rm.killSwitchActive {
		t.Error("kill switch should not fire for report under limits")
	}

	// No signal on channel
	select {
	case sig := <-rm.killCh:
		t.Errorf("unexpected kill signal: %+v", sig)
	default:
	}
}

func TestProcessReportPerMarketBreach(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	rm.processReport(PositionReport{
		MarketID:    "m1",
		ExposureUSD: 150, // exceeds 100 limit
		MidPrice:    0.50,
		Timestamp:   time.Now(),
	})

	if !rm.killSwitchActive {
		t.Error("kill switch should fire for per-market breach")
	}

	select {
	case sig := <-rm.killCh:
		if sig.MarketID != "m1" {
			t.Errorf("kill signal market = %q, want m1", sig.MarketID)
		}
	default:
		t.Error("expected kill signal on channel")
	}
}

func TestProcessReportGlobalBreach(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	// Submit multiple markets that together exceed global limit
	rm.processReport(PositionReport{MarketID: "m1", ExposureUSD: 90, MidPrice: 0.50, Timestamp: time.Now()})
	rm.processReport(PositionReport{MarketID: "m2", ExposureUSD: 90, MidPrice: 0.50, Timestamp: time.Now()})
	rm.processReport(PositionReport{MarketID: "m3", ExposureUSD: 90, MidPrice: 0.50, Timestamp: time.Now()})
	rm.processReport(PositionReport{MarketID: "m4", ExposureUSD: 90, MidPrice: 0.50, Timestamp: time.Now()})
	rm.processReport(PositionReport{MarketID: "m5", ExposureUSD: 90, MidPrice: 0.50, Timestamp: time.Now()})
	rm.processReport(PositionReport{MarketID: "m6", ExposureUSD: 90, MidPrice: 0.50, Timestamp: time.Now()})

	// Total = 540 > 500 global limit
	if !rm.killSwitchActive {
		t.Error("kill switch should fire for global exposure breach")
	}

	// Drain all kill signals
	drained := 0
	for {
		select {
		case <-rm.killCh:
			drained++
		default:
			goto done
		}
	}
done:
	if drained == 0 {
		t.Error("expected at least one kill signal")
	}
}

func TestProcessReportDailyLossBreach(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	rm.processReport(PositionReport{
		MarketID:      "m1",
		ExposureUSD:   10,
		RealizedPnL:   -30,
		UnrealizedPnL: -25,
		MidPrice:      0.50,
		Timestamp:     time.Now(),
	})

	// total PnL = -30 + -25 = -55 < -50 threshold
	if !rm.killSwitchActive {
		t.Error("kill switch should fire for daily loss breach")
	}
}

func TestCheckPriceMovementNormal(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	now := time.Now()

	// Set anchor
	rm.processReport(PositionReport{
		MarketID:  "m1",
		MidPrice:  0.50,
		Timestamp: now,
	})

	// Small price move within window
	rm.processReport(PositionReport{
		MarketID:  "m1",
		MidPrice:  0.52, // 4% move, below 10% threshold
		Timestamp: now.Add(10 * time.Second),
	})

	// Should not have fired kill for price movement
	// (it might fire for other reasons, but check killSwitchActive was not set by price check)
	select {
	case <-rm.killCh:
		t.Error("should not fire kill for 4% move")
	default:
	}
}

func TestCheckPriceMovementSpike(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	now := time.Now()

	// Set anchor
	rm.processReport(PositionReport{
		MarketID:  "m1",
		MidPrice:  0.50,
		Timestamp: now,
	})

	// Large price move within window
	rm.processReport(PositionReport{
		MarketID:  "m1",
		MidPrice:  0.35, // 30% drop, exceeds 10% threshold
		Timestamp: now.Add(10 * time.Second),
	})

	if !rm.killSwitchActive {
		t.Error("kill switch should fire for 30% price spike")
	}
}

func TestRemainingBudget(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	// No position → full budget
	remaining := rm.RemainingBudget("m1")
	if remaining != 100 { // min(per-market 100, global 500)
		t.Errorf("remaining = %v, want 100", remaining)
	}

	// After some exposure
	rm.processReport(PositionReport{
		MarketID:    "m1",
		ExposureUSD: 60,
		MidPrice:    0.50,
		Timestamp:   time.Now(),
	})

	remaining = rm.RemainingBudget("m1")
	if remaining != 40 { // 100 - 60 = 40 per-market; 500 - 60 = 440 global; min = 40
		t.Errorf("remaining = %v, want 40", remaining)
	}
}

func TestRemainingBudgetGlobalConstrained(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	// Fill up global exposure with other markets
	for i := 0; i < 5; i++ {
		rm.processReport(PositionReport{
			MarketID:    "other-" + string(rune('A'+i)),
			ExposureUSD: 95,
			MidPrice:    0.50,
			Timestamp:   time.Now(),
		})
	}
	// Drain kill signals from global breach
	for {
		select {
		case <-rm.killCh:
		default:
			goto done2
		}
	}
done2:

	// Total exposure = 475. Global remaining = 500 - 475 = 25.
	// Per-market m1 = 100 (no position). Min(100, 25) = 25.
	remaining := rm.RemainingBudget("m1")
	if remaining != 25 {
		t.Errorf("remaining = %v, want 25 (global constrained)", remaining)
	}
}

func TestIsKillSwitchCooldown(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	// Activate kill switch with short cooldown for testing
	rm.cfg.CooldownAfterKill = 100 * time.Millisecond
	rm.processReport(PositionReport{
		MarketID:    "m1",
		ExposureUSD: 200, // exceeds per-market limit
		MidPrice:    0.50,
		Timestamp:   time.Now(),
	})

	if !rm.IsKillSwitchActive() {
		t.Error("kill switch should be active immediately after breach")
	}

	// Wait for cooldown to expire
	time.Sleep(150 * time.Millisecond)

	if rm.IsKillSwitchActive() {
		t.Error("kill switch should expire after cooldown")
	}
}

func TestRemoveMarketRecomputesTotals(t *testing.T) {
	t.Parallel()
	rm := newTestManager()

	now := time.Now()
	rm.processReport(PositionReport{MarketID: "m1", ExposureUSD: 60, RealizedPnL: 5, MidPrice: 0.50, Timestamp: now})
	rm.processReport(PositionReport{MarketID: "m2", ExposureUSD: 70, RealizedPnL: 3, MidPrice: 0.50, Timestamp: now})

	if got := rm.totalExposure; got != 130 {
		t.Fatalf("totalExposure before remove = %v, want 130", got)
	}
	if got := rm.totalRealizedPnL; got != 8 {
		t.Fatalf("totalRealizedPnL before remove = %v, want 8", got)
	}

	rm.RemoveMarket("m2")

	if got := rm.totalExposure; got != 60 {
		t.Fatalf("totalExposure after remove = %v, want 60", got)
	}
	if got := rm.totalRealizedPnL; got != 5 {
		t.Fatalf("totalRealizedPnL after remove = %v, want 5", got)
	}
}
