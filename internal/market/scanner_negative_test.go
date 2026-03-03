package market

import (
	"testing"
	"time"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

// ————————————————————————————————————————————————————————————————————————
// Scanner: negative tests for filtering, scoring, and hysteresis
// ————————————————————————————————————————————————————————————————————————

func testUSMarket(slug string, active bool, closed bool) types.USMarket {
	return types.USMarket{
		ID:       slug,
		Slug:     slug,
		Question: "Will " + slug + " happen?",
		Active:   active,
		Closed:   closed,
		EndDate:  time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}
}

func newNegativeScannerConfig() config.ScannerConfig {
	return config.ScannerConfig{
		PollInterval:   60 * time.Second,
		MinSpread:      0.005,
		MaxEndDateDays: 30,
	}
}

// TestFilterMarkets_InactiveExcluded verifies inactive markets are filtered.
func TestFilterMarkets_InactiveExcluded(t *testing.T) {
	t.Parallel()
	s := &Scanner{cfg: newNegativeScannerConfig(), activeSlugs: make(map[string]bool)}

	markets := []types.USMarket{
		testUSMarket("active-game", true, false),
		testUSMarket("inactive-game", false, false),
	}

	filtered := s.filterMarkets(markets)
	if len(filtered) != 1 {
		t.Errorf("expected 1 market, got %d", len(filtered))
	}
	if len(filtered) > 0 && filtered[0].Slug != "active-game" {
		t.Errorf("expected 'active-game', got %q", filtered[0].Slug)
	}
}

// TestFilterMarkets_ClosedExcluded verifies closed markets are filtered.
func TestFilterMarkets_ClosedExcluded(t *testing.T) {
	t.Parallel()
	s := &Scanner{cfg: newNegativeScannerConfig(), activeSlugs: make(map[string]bool)}

	markets := []types.USMarket{
		testUSMarket("open-game", true, false),
		testUSMarket("closed-game", true, true),
	}

	filtered := s.filterMarkets(markets)
	if len(filtered) != 1 {
		t.Errorf("expected 1 market, got %d", len(filtered))
	}
}

// TestFilterMarkets_ExcludeSlug verifies explicit slug exclusion.
func TestFilterMarkets_ExcludeSlug(t *testing.T) {
	t.Parallel()
	cfg := newNegativeScannerConfig()
	cfg.ExcludeSlugs = []string{"bad-market"}
	s := &Scanner{cfg: cfg, activeSlugs: make(map[string]bool)}

	markets := []types.USMarket{
		testUSMarket("good-market", true, false),
		testUSMarket("bad-market", true, false),
	}

	filtered := s.filterMarkets(markets)
	if len(filtered) != 1 {
		t.Errorf("expected 1 market after slug exclusion, got %d", len(filtered))
	}
}

// TestFilterMarkets_IncludeKeywordNarrows verifies keyword inclusion.
func TestFilterMarkets_IncludeKeywordNarrows(t *testing.T) {
	t.Parallel()
	cfg := newNegativeScannerConfig()
	cfg.IncludeKeywords = []string{"nba"}
	s := &Scanner{cfg: cfg, activeSlugs: make(map[string]bool)}

	markets := []types.USMarket{
		testUSMarket("aec-nba-game1", true, false),
		testUSMarket("aec-nhl-game1", true, false),
	}

	filtered := s.filterMarkets(markets)
	if len(filtered) != 1 {
		t.Errorf("expected 1 NBA market, got %d", len(filtered))
	}
}

// TestFilterMarkets_ExcludeKeyword verifies keyword exclusion.
func TestFilterMarkets_ExcludeKeyword(t *testing.T) {
	t.Parallel()
	cfg := newNegativeScannerConfig()
	cfg.ExcludeKeywords = []string{"politics"}
	s := &Scanner{cfg: cfg, activeSlugs: make(map[string]bool)}

	markets := []types.USMarket{
		testUSMarket("sports-game", true, false),
		testUSMarket("politics-election", true, false),
	}

	filtered := s.filterMarkets(markets)
	if len(filtered) != 1 {
		t.Errorf("expected 1 market after keyword exclusion, got %d", len(filtered))
	}
}

// TestFilterMarkets_ExpiredEndDate verifies markets past their end date
// are excluded.
func TestFilterMarkets_ExpiredEndDate(t *testing.T) {
	t.Parallel()
	s := &Scanner{cfg: newNegativeScannerConfig(), activeSlugs: make(map[string]bool)}

	expired := types.USMarket{
		ID:       "old-game",
		Slug:     "old-game",
		Question: "Old game?",
		Active:   true,
		Closed:   false,
		EndDate:  time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
	}

	filtered := s.filterMarkets([]types.USMarket{expired})
	if len(filtered) != 0 {
		t.Errorf("expired market should be filtered out, got %d", len(filtered))
	}
}

// TestFilterMarkets_TooFarEndDate verifies markets ending too far out
// are excluded.
func TestFilterMarkets_TooFarEndDate(t *testing.T) {
	t.Parallel()
	s := &Scanner{cfg: newNegativeScannerConfig(), activeSlugs: make(map[string]bool)}

	farOut := types.USMarket{
		ID:       "far-game",
		Slug:     "far-game",
		Question: "Far future game?",
		Active:   true,
		Closed:   false,
		EndDate:  time.Now().Add(365 * 24 * time.Hour).Format(time.RFC3339),
	}

	filtered := s.filterMarkets([]types.USMarket{farOut})
	if len(filtered) != 0 {
		t.Errorf("market too far out should be filtered out, got %d", len(filtered))
	}
}

// TestFilterMarkets_EmptyEndDate verifies markets with empty end date pass.
func TestFilterMarkets_EmptyEndDate(t *testing.T) {
	t.Parallel()
	s := &Scanner{cfg: newNegativeScannerConfig(), activeSlugs: make(map[string]bool)}

	m := types.USMarket{
		ID: "no-end", Slug: "no-end", Question: "?",
		Active: true, Closed: false, EndDate: "",
	}

	filtered := s.filterMarkets([]types.USMarket{m})
	if len(filtered) != 1 {
		t.Errorf("empty end date should pass filter, got %d", len(filtered))
	}
}

// TestFilterMarkets_CaseInsensitive verifies keyword matching is
// case-insensitive.
func TestFilterMarkets_CaseInsensitive(t *testing.T) {
	t.Parallel()
	cfg := newNegativeScannerConfig()
	cfg.IncludeKeywords = []string{"NBA"}
	s := &Scanner{cfg: cfg, activeSlugs: make(map[string]bool)}

	m := types.USMarket{
		ID: "nba-lower", Slug: "aec-nba-game", Question: "NBA game?",
		Active: true, Closed: false,
		EndDate: time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	filtered := s.filterMarkets([]types.USMarket{m})
	if len(filtered) != 1 {
		t.Errorf("case-insensitive keyword should match, got %d", len(filtered))
	}
}

// ————————————————————————————————————————————————————————————————————————
// Hysteresis tests
// ————————————————————————————————————————————————————————————————————————

// TestSetActiveSlugs_ReplacesPrevious verifies SetActiveSlugs replaces
// the old set completely.
func TestSetActiveSlugs_ReplacesPrevious(t *testing.T) {
	t.Parallel()
	s := &Scanner{activeSlugs: make(map[string]bool)}

	s.SetActiveSlugs([]string{"a", "b"})
	if !s.isActiveSlug("a") || !s.isActiveSlug("b") {
		t.Error("a and b should be active")
	}

	s.SetActiveSlugs([]string{"c"})
	if s.isActiveSlug("a") || s.isActiveSlug("b") {
		t.Error("a and b should no longer be active after replacement")
	}
	if !s.isActiveSlug("c") {
		t.Error("c should be active")
	}
}

// TestIsActiveSlug_NonexistentReturnsFalse verifies unknown slugs return false.
func TestIsActiveSlug_NonexistentReturnsFalse(t *testing.T) {
	t.Parallel()
	s := &Scanner{activeSlugs: make(map[string]bool)}
	if s.isActiveSlug("nonexistent") {
		t.Error("nonexistent slug should not be active")
	}
}
