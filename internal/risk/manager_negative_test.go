package risk

import (
	"fmt"
	"testing"
	"time"

	"log/slog"
	"os"

	"polymarket-mm/internal/config"
)

// ————————————————————————————————————————————————————————————————————————
// Risk Manager: negative tests for kill switch, budget, and edge cases
// ————————————————————————————————————————————————————————————————————————

func negTestRiskConfig() config.RiskConfig {
	return config.RiskConfig{
		MaxPositionPerMarket: 100.0,
		MaxGlobalExposure:    500.0,
		MaxMarketsActive:     5,
		KillSwitchDropPct:    0.30,
		KillSwitchWindowSec:  60,
		MaxDailyLoss:         50.0,
		CooldownAfterKill:    5 * time.Minute,
	}
}

func negTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestKillSwitch_PerMarketExposureBreach verifies that exceeding
// per-market exposure triggers a kill signal for that market.
func TestKillSwitch_PerMarketExposureBreach(t *testing.T) {
	t.Parallel()
	rm := NewManager(negTestRiskConfig(), negTestLogger())

	// Don't start the Run loop — just process directly
	report := PositionReport{
		MarketID:    "test-market",
		ExposureUSD: 150.0, // exceeds 100.0 limit
		MidPrice:    0.50,
		Timestamp:   time.Now(),
	}
	rm.processReport(report)

	select {
	case sig := <-rm.killCh:
		if sig.MarketID != "test-market" {
			t.Errorf("expected kill for 'test-market', got %q", sig.MarketID)
		}
	default:
		t.Error("expected kill signal for per-market exposure breach")
	}
}

// TestKillSwitch_GlobalExposureBreach verifies global exposure kill.
func TestKillSwitch_GlobalExposureBreach(t *testing.T) {
	t.Parallel()
	rm := NewManager(negTestRiskConfig(), negTestLogger())

	// Add several markets that together exceed global limit
	for i := 0; i < 6; i++ {
		rm.processReport(PositionReport{
			MarketID:    fmt.Sprintf("market-%d", i),
			ExposureUSD: 90.0, // 6 * 90 = 540 > 500
			MidPrice:    0.50,
			Timestamp:   time.Now(),
		})
	}

	// Should trigger global kill (empty MarketID)
	gotKill := false
	for {
		select {
		case sig := <-rm.killCh:
			if sig.MarketID == "" {
				gotKill = true
			}
		default:
			goto done
		}
	}
done:
	if !gotKill {
		t.Error("expected global kill signal for total exposure breach")
	}
}

// TestKillSwitch_DailyLossBreach verifies max daily loss kill.
func TestKillSwitch_DailyLossBreach(t *testing.T) {
	t.Parallel()
	rm := NewManager(negTestRiskConfig(), negTestLogger())

	rm.processReport(PositionReport{
		MarketID:      "loss-market",
		ExposureUSD:   10.0,
		MidPrice:      0.50,
		RealizedPnL:   -30.0,
		UnrealizedPnL: -25.0, // total = -55 < -50
		Timestamp:     time.Now(),
	})

	select {
	case sig := <-rm.killCh:
		if sig.MarketID != "" {
			t.Logf("got global kill (expected): reason=%s", sig.Reason)
		}
	default:
		t.Error("expected kill signal for daily loss breach")
	}
}

// TestKillSwitch_Cooldown verifies that the kill switch stays active
// during cooldown and auto-clears after.
func TestKillSwitch_Cooldown(t *testing.T) {
	t.Parallel()
	cfg := negTestRiskConfig()
	cfg.CooldownAfterKill = 100 * time.Millisecond // short for test
	rm := NewManager(cfg, negTestLogger())

	// Trigger kill
	rm.processReport(PositionReport{
		MarketID:    "cooldown-test",
		ExposureUSD: 200.0,
		MidPrice:    0.50,
		Timestamp:   time.Now(),
	})
	<-rm.killCh // drain

	if !rm.IsKillSwitchActive() {
		t.Error("kill switch should be active immediately after trigger")
	}

	// Wait for cooldown
	time.Sleep(150 * time.Millisecond)

	if rm.IsKillSwitchActive() {
		t.Error("kill switch should auto-clear after cooldown")
	}
}

// TestRemainingBudget_ExhaustedReturnsZero verifies that when exposure
// equals the limit, remaining budget is zero.
func TestRemainingBudget_ExhaustedReturnsZero(t *testing.T) {
	t.Parallel()
	rm := NewManager(negTestRiskConfig(), negTestLogger())

	rm.mu.Lock()
	rm.positions["full-market"] = PositionReport{
		MarketID:    "full-market",
		ExposureUSD: 100.0, // equals MaxPositionPerMarket
	}
	rm.totalExposure = 100.0
	rm.mu.Unlock()

	remaining := rm.RemainingBudget("full-market")
	if remaining != 0 {
		t.Errorf("expected 0 remaining budget, got %v", remaining)
	}
}

// TestRemainingBudget_GlobalLimitTakesMin verifies that global limit
// is respected even when per-market has headroom.
func TestRemainingBudget_GlobalLimitTakesMin(t *testing.T) {
	t.Parallel()
	rm := NewManager(negTestRiskConfig(), negTestLogger())

	rm.mu.Lock()
	rm.positions["mkt-a"] = PositionReport{
		MarketID: "mkt-a", ExposureUSD: 50.0,
	}
	rm.totalExposure = 490.0 // only $10 global headroom, but per-market has $50
	rm.mu.Unlock()

	remaining := rm.RemainingBudget("mkt-a")
	if remaining > 10.0 {
		t.Errorf("expected remaining <= 10.0 (global limit), got %v", remaining)
	}
}

// TestRapidPriceMovement_TriggersKill verifies that a 50% price swing
// within the window triggers the kill switch.
func TestRapidPriceMovement_TriggersKill(t *testing.T) {
	t.Parallel()
	cfg := negTestRiskConfig()
	cfg.KillSwitchDropPct = 0.20 // 20% threshold
	cfg.KillSwitchWindowSec = 10
	rm := NewManager(cfg, negTestLogger())

	now := time.Now()

	// Set anchor at price 0.50
	rm.processReport(PositionReport{
		MarketID:  "price-swing",
		MidPrice:  0.50,
		Timestamp: now,
	})
	// drain any non-kill signals
	select {
	case <-rm.killCh:
	default:
	}

	// Price jumps to 0.80 (60% move) within the window
	rm.processReport(PositionReport{
		MarketID:  "price-swing",
		MidPrice:  0.80,
		Timestamp: now.Add(5 * time.Second),
	})

	select {
	case sig := <-rm.killCh:
		if sig.MarketID != "price-swing" {
			t.Errorf("expected kill for 'price-swing', got %q", sig.MarketID)
		}
	default:
		t.Error("expected kill signal for rapid price movement")
	}
}

// TestRemoveMarket_CleansUpState verifies that removing a market
// clears its position and price anchor.
func TestRemoveMarket_CleansUpState(t *testing.T) {
	t.Parallel()
	rm := NewManager(negTestRiskConfig(), negTestLogger())

	rm.mu.Lock()
	rm.positions["dead-market"] = PositionReport{
		MarketID: "dead-market", ExposureUSD: 50.0,
	}
	rm.priceAnchors["dead-market"] = priceAnchor{price: 0.50, timestamp: time.Now()}
	rm.totalExposure = 50.0
	rm.mu.Unlock()

	rm.RemoveMarket("dead-market")

	rm.mu.RLock()
	defer rm.mu.RUnlock()
	if _, ok := rm.positions["dead-market"]; ok {
		t.Error("position should be cleaned up after RemoveMarket")
	}
	if _, ok := rm.priceAnchors["dead-market"]; ok {
		t.Error("price anchor should be cleaned up after RemoveMarket")
	}
	if rm.totalExposure != 0 {
		t.Errorf("total exposure should be recomputed to 0, got %v", rm.totalExposure)
	}
}
