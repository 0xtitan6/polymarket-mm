package market

import (
	"testing"
	"time"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

func testScannerConfig() config.ScannerConfig {
	return config.ScannerConfig{
		MinLiquidity:    1000,
		MinVolume24h:    500,
		MinSpread:       0.01,
		MaxEndDateDays:  90,
		IncludeSlugs:    nil,
		IncludeKeywords: nil,
		ExcludeKeywords: nil,
		ExcludeSlugs:    []string{"excluded-slug"},
	}
}

func testRiskConfig() config.RiskConfig {
	return config.RiskConfig{
		MaxMarketsActive:     3,
		MaxPositionPerMarket: 100,
	}
}

func baseMarket() types.USMarket {
	endDate := time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	return types.USMarket{
		ID:       "m1",
		Slug:     "test-market",
		Question: "Will test pass?",
		Active:   true,
		Closed:   false,
		EndDate:  endDate,
	}
}

func newTestScanner() *Scanner {
	return &Scanner{
		cfg:         testScannerConfig(),
		riskCfg:     testRiskConfig(),
		activeSlugs: make(map[string]bool),
	}
}

func TestFilterMarketsPassesValid(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	markets := []types.USMarket{baseMarket()}
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
	result := s.filterMarkets([]types.USMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for inactive, got %d", len(result))
	}
}

func TestFilterMarketsRejectsClosed(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.Closed = true
	result := s.filterMarkets([]types.USMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for closed, got %d", len(result))
	}
}

func TestFilterMarketsRejectsExcludedSlug(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.Slug = "excluded-slug"
	result := s.filterMarkets([]types.USMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for excluded slug, got %d", len(result))
	}
}

func TestFilterMarketsRejectsExcludedKeyword(t *testing.T) {
	t.Parallel()
	s := newTestScanner()
	s.cfg.ExcludeKeywords = []string{"5m"}

	m := baseMarket()
	m.Slug = "btc-updown-5m-12345"
	m.Question = "BTC up or down in 5m?"
	result := s.filterMarkets([]types.USMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for excluded keyword, got %d", len(result))
	}
}

func TestFilterMarketsRejectsExpiredEndDate(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.EndDate = time.Now().Add(-24 * time.Hour).Format(time.RFC3339) // past
	result := s.filterMarkets([]types.USMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for expired end date, got %d", len(result))
	}
}

func TestFilterMarketsRejectsTooFarEndDate(t *testing.T) {
	t.Parallel()
	s := newTestScanner()

	m := baseMarket()
	m.EndDate = time.Now().Add(365 * 24 * time.Hour).Format(time.RFC3339) // >90 days
	result := s.filterMarkets([]types.USMarket{m})

	if len(result) != 0 {
		t.Errorf("expected 0 markets for end date too far, got %d", len(result))
	}
}

func TestFilterMarketsIncludesSlug(t *testing.T) {
	t.Parallel()
	s := newTestScanner()
	s.cfg.IncludeSlugs = []string{"btc-up-down-15m"}

	m := baseMarket()
	m.Slug = "btc-up-down-15m"
	other := baseMarket()
	other.Slug = "eth-up-down-15m"

	result := s.filterMarkets([]types.USMarket{m, other})
	if len(result) != 1 {
		t.Fatalf("expected 1 market, got %d", len(result))
	}
	if result[0].Slug != "btc-up-down-15m" {
		t.Fatalf("expected slug btc-up-down-15m, got %s", result[0].Slug)
	}
}

func TestFilterMarketsIncludesKeywordCaseInsensitive(t *testing.T) {
	t.Parallel()
	s := newTestScanner()
	s.cfg.IncludeKeywords = []string{"BitCoin"}

	m := baseMarket()
	m.Question = "Will Bitcoin close above $110k today?"
	m.Slug = "will-btc-close-above-110k"

	other := baseMarket()
	other.Question = "Will ETH close above $5k today?"
	other.Slug = "will-eth-close-above-5k"

	result := s.filterMarkets([]types.USMarket{m, other})
	if len(result) != 1 {
		t.Fatalf("expected 1 market, got %d", len(result))
	}
	if result[0].Slug != "will-btc-close-above-110k" {
		t.Fatalf("expected slug will-btc-close-above-110k, got %s", result[0].Slug)
	}
}

func TestFilterMarketsIncludeThenExclude(t *testing.T) {
	t.Parallel()
	s := newTestScanner()
	s.cfg.IncludeSlugs = []string{"btc-up-down-15m"}
	s.cfg.ExcludeSlugs = []string{"btc-up-down-15m"}

	m := baseMarket()
	m.Slug = "btc-up-down-15m"

	result := s.filterMarkets([]types.USMarket{m})
	if len(result) != 0 {
		t.Fatalf("expected 0 markets because exclude should win, got %d", len(result))
	}
}

func TestFilterMarketsIncludeThenExcludeKeyword(t *testing.T) {
	t.Parallel()
	s := newTestScanner()
	s.cfg.IncludeKeywords = []string{"bitcoin"}
	s.cfg.ExcludeKeywords = []string{"5m"}

	m := baseMarket()
	m.Slug = "bitcoin-up-or-down-5m"
	m.Question = "Bitcoin up or down in 5m?"

	result := s.filterMarkets([]types.USMarket{m})
	if len(result) != 0 {
		t.Fatalf("expected 0 markets because exclude keyword should win, got %d", len(result))
	}
}

func TestConvertToMarketInfo(t *testing.T) {
	t.Parallel()

	m := baseMarket()
	m.Slug = "bitcoin-100k"
	m.OrderPriceMinTickSize = 0.001

	mi := convertToMarketInfo(m, 0.45, 0.55, 0.10)

	if mi.Slug != "bitcoin-100k" {
		t.Errorf("Slug = %q, want %q", mi.Slug, "bitcoin-100k")
	}
	if mi.ConditionID != "bitcoin-100k" {
		t.Errorf("ConditionID = %q, want slug %q", mi.ConditionID, "bitcoin-100k")
	}
	if mi.YesTokenID != "bitcoin-100k" {
		t.Errorf("YesTokenID = %q, want slug", mi.YesTokenID)
	}
	if mi.TickSize != types.Tick0001 {
		t.Errorf("TickSize = %v, want Tick0001", mi.TickSize)
	}
	if mi.BestBid != 0.45 {
		t.Errorf("BestBid = %v, want 0.45", mi.BestBid)
	}
	if mi.BestAsk != 0.55 {
		t.Errorf("BestAsk = %v, want 0.55", mi.BestAsk)
	}
	if mi.Spread != 0.10 {
		t.Errorf("Spread = %v, want 0.10", mi.Spread)
	}
}

func TestParseFloat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  float64
	}{
		{"0.45", 0.45},
		{"0.001", 0.001},
		{"100.5", 100.5},
		{"", 0},
		{"not-a-number", 0},
	}
	for _, tt := range tests {
		got := parseFloat(tt.input)
		if got != tt.want {
			t.Errorf("parseFloat(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
