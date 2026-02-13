package market

import (
	"math"
	"testing"
	"time"

	"polymarket-mm/internal/config"
)

func testScannerConfig() config.ScannerConfig {
	return config.ScannerConfig{
		MinLiquidity:   1000,
		MinVolume24h:   500,
		MinSpread:      0.01,
		MaxEndDateDays: 90,
		ExcludeSlugs:   []string{"excluded-slug"},
	}
}

func testRiskConfig() config.RiskConfig {
	return config.RiskConfig{
		MaxMarketsActive:     3,
		MaxPositionPerMarket: 100,
	}
}

func baseMarket() GammaMarket {
	endDate := time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	return GammaMarket{
		ID:              "m1",
		ConditionID:     "cond1",
		Slug:            "test-market",
		Active:          true,
		Closed:          false,
		AcceptingOrders: true,
		EnableOrderBook: true,
		EndDate:         endDate,
		Liquidity:       "5000",
		Volume24hr:      1000,
		Spread:          0.05,
		ClobTokenIds:    `["yes-token","no-token"]`,
	}
}

func newTestScanner() *Scanner {
	return &Scanner{
		cfg:     testScannerConfig(),
		riskCfg: testRiskConfig(),
	}
}

func TestFilterMarketsPassesValid(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	markets := []GammaMarket{baseMarket()}
	result := s.filterMarkets(markets)

	if len(result) != 1 {
		t.Fatalf("expected 1 market, got %d", len(result))
	}
}

func TestFilterMarketsRejectsInactive(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.Active = false
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for inactive, got %d", len(result))
	}
}

func TestFilterMarketsRejectsClosed(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.Closed = true
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for closed, got %d", len(result))
	}
}

func TestFilterMarketsRejectsNotAcceptingOrders(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.AcceptingOrders = false
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for not accepting orders, got %d", len(result))
	}
}

func TestFilterMarketsRejectsLowLiquidity(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.Liquidity = "100" // below 1000 threshold
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for low liquidity, got %d", len(result))
	}
}

func TestFilterMarketsRejectsLowVolume(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.Volume24hr = 100 // below 500 threshold
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for low volume, got %d", len(result))
	}
}

func TestFilterMarketsRejectsLowSpread(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.Spread = 0.005 // below 0.01 threshold
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for low spread, got %d", len(result))
	}
}

func TestFilterMarketsRejectsExcludedSlug(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.Slug = "excluded-slug"
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for excluded slug, got %d", len(result))
	}
}

func TestFilterMarketsRejectsExpiredEndDate(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.EndDate = time.Now().Add(-24 * time.Hour).Format(time.RFC3339) // past
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for expired end date, got %d", len(result))
	}
}

func TestFilterMarketsRejectsTooFarEndDate(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.EndDate = time.Now().Add(365 * 24 * time.Hour).Format(time.RFC3339) // >90 days
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for end date too far, got %d", len(result))
	}
}

func TestFilterMarketsRejectsNoTokenIDs(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.ClobTokenIds = ""
	result := s.filterMarkets([]GammaMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for missing token IDs, got %d", len(result))
	}
}

func TestRankMarketsScoring(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m1 := baseMarket()
	m1.ID = "high-score"
	m1.Spread = 0.10
	m1.Volume24hr = 10000
	m1.Liquidity = "50000"

	m2 := baseMarket()
	m2.ID = "low-score"
	m2.Spread = 0.02
	m2.Volume24hr = 100
	m2.Liquidity = "2000"

	ranked := s.rankMarkets([]GammaMarket{m2, m1})

	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked markets, got %d", len(ranked))
	}
	if ranked[0].Market.ID != "high-score" {
		t.Errorf("top market should be high-score, got %s", ranked[0].Market.ID)
	}
	if ranked[0].Score <= ranked[1].Score {
		t.Errorf("scores not sorted descending: %v <= %v", ranked[0].Score, ranked[1].Score)
	}
}

func TestRankMarketsLiquidityCap(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	// Two markets with same spread/volume but different liquidity above 10k
	m1 := baseMarket()
	m1.Liquidity = "20000"
	m1.Spread = 0.05
	m1.Volume24hr = 1000

	m2 := baseMarket()
	m2.Liquidity = "50000"
	m2.Spread = 0.05
	m2.Volume24hr = 1000

	ranked := s.rankMarkets([]GammaMarket{m1, m2})

	// Both above 10k → liquidityFactor capped at 1.0 → same score
	if math.Abs(ranked[0].Score-ranked[1].Score) > 1e-10 {
		t.Errorf("scores should be equal when both above liquidity cap: %v vs %v",
			ranked[0].Score, ranked[1].Score)
	}
}
