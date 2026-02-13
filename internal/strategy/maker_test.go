package strategy

import (
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"polymarket-mm/internal/config"
	"polymarket-mm/internal/market"
	"polymarket-mm/pkg/types"
)

func testStrategyConfig() config.StrategyConfig {
	return config.StrategyConfig{
		Gamma:            0.5,
		Sigma:            0.2,
		K:                10.0,
		T:                0.5,
		DefaultSpreadBps: 100, // 1% min spread
		OrderSizeUSD:     50,
		RefreshInterval:  5 * time.Second,
		StaleBookTimeout: 30 * time.Second,
		// Phase 1: Flow detection defaults
		FlowWindow:              60 * time.Second,
		FlowToxicityThreshold:   0.6,
		FlowCooldownPeriod:      120 * time.Second,
		FlowMaxSpreadMultiplier: 3.0,
	}
}

func testMarketInfo() types.MarketInfo {
	return types.MarketInfo{
		ConditionID:  "cond-1",
		YesTokenID:   "yes-token",
		NoTokenID:    "no-token",
		TickSize:     types.Tick001,
		MinOrderSize: 1.0,
	}
}

func setupMaker(cfg config.StrategyConfig, info types.MarketInfo) *Maker {
	b := market.NewBook(info.ConditionID, info.YesTokenID, info.NoTokenID)
	inv := NewInventory(info.ConditionID, info.YesTokenID, info.NoTokenID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	return &Maker{
		cfg:          cfg,
		marketInfo:   info,
		book:         b,
		inventory:    inv,
		flowTracker:  NewFlowTracker(cfg.FlowWindow, cfg.FlowToxicityThreshold, cfg.FlowCooldownPeriod, cfg.FlowMaxSpreadMultiplier),
		activeOrders: make(map[string]types.OpenOrder),
		logger:       logger,
	}
}

func TestComputeQuotesBalanced(t *testing.T) {
	t.Parallel()
	cfg := testStrategyConfig()
	info := testMarketInfo()
	m := setupMaker(cfg, info)

	// Seed book with a mid of 0.50
	m.book.ApplyBookResponse(&types.BookResponse{
		AssetID: info.YesTokenID,
		Bids:    []types.PriceLevel{{Price: "0.49", Size: "100"}},
		Asks:    []types.PriceLevel{{Price: "0.51", Size: "100"}},
		Hash:    "h1",
	})

	mid := 0.50
	budget := 1000.0
	quotes, err := m.computeQuotes(mid, budget)
	if err != nil {
		t.Fatalf("computeQuotes: %v", err)
	}

	if quotes.Bid == nil {
		t.Fatal("expected a bid")
	}
	if quotes.Ask == nil {
		t.Fatal("expected an ask")
	}

	// With q=0 (balanced), reservation price = mid
	// Bid should be below mid, ask above mid
	if quotes.Bid.Price >= mid {
		t.Errorf("bid price %v should be below mid %v", quotes.Bid.Price, mid)
	}
	if quotes.Ask.Price <= mid {
		t.Errorf("ask price %v should be above mid %v", quotes.Ask.Price, mid)
	}

	// Spread should be symmetric around mid when q=0
	bidDist := mid - quotes.Bid.Price
	askDist := quotes.Ask.Price - mid
	if math.Abs(bidDist-askDist) > 0.02 {
		t.Errorf("quotes not symmetric: bidDist=%v, askDist=%v", bidDist, askDist)
	}
}

func TestComputeQuotesLongSkew(t *testing.T) {
	t.Parallel()
	cfg := testStrategyConfig()
	info := testMarketInfo()
	m := setupMaker(cfg, info)

	// Make inventory long YES
	m.inventory.OnFill(Fill{Side: types.BUY, TokenID: info.YesTokenID, Price: 0.50, Size: 100})

	mid := 0.50
	budget := 1000.0
	quotes, err := m.computeQuotes(mid, budget)
	if err != nil {
		t.Fatalf("computeQuotes: %v", err)
	}

	if quotes.Bid == nil || quotes.Ask == nil {
		t.Fatal("expected both bid and ask")
	}

	// When long (q > 0), reservation price < mid → bid skewed lower
	// The ask should also be lower than balanced case
	midpoint := (quotes.Bid.Price + quotes.Ask.Price) / 2
	if midpoint >= mid {
		t.Errorf("midpoint of quotes %v should be below mid %v when long", midpoint, mid)
	}
}

func TestComputeQuotesShortSkew(t *testing.T) {
	t.Parallel()
	cfg := testStrategyConfig()
	info := testMarketInfo()
	m := setupMaker(cfg, info)

	// Make inventory long NO (= short YES in delta terms)
	m.inventory.OnFill(Fill{Side: types.BUY, TokenID: info.NoTokenID, Price: 0.50, Size: 100})

	mid := 0.50
	budget := 1000.0
	quotes, err := m.computeQuotes(mid, budget)
	if err != nil {
		t.Fatalf("computeQuotes: %v", err)
	}

	if quotes.Bid == nil || quotes.Ask == nil {
		t.Fatal("expected both bid and ask")
	}

	// When short (q < 0), reservation price > mid → bid/ask skewed up
	midpoint := (quotes.Bid.Price + quotes.Ask.Price) / 2
	if midpoint <= mid {
		t.Errorf("midpoint of quotes %v should be above mid %v when short YES", midpoint, mid)
	}
}

func TestComputeQuotesBudgetExhausted(t *testing.T) {
	t.Parallel()
	cfg := testStrategyConfig()
	info := testMarketInfo()
	m := setupMaker(cfg, info)

	mid := 0.50
	budget := 0.001 // too small for min order size
	quotes, err := m.computeQuotes(mid, budget)
	if err != nil {
		t.Fatalf("computeQuotes: %v", err)
	}

	// Both should be nil — can't meet min order size with tiny budget
	if quotes.Bid != nil {
		t.Errorf("expected nil bid with exhausted budget, got price=%v", quotes.Bid.Price)
	}
	if quotes.Ask != nil {
		t.Errorf("expected nil ask with exhausted budget, got price=%v", quotes.Ask.Price)
	}
}

func TestComputeQuotesCombinedNotionalWithinBudget(t *testing.T) {
	t.Parallel()
	cfg := testStrategyConfig()
	info := testMarketInfo()
	m := setupMaker(cfg, info)

	mid := 0.50
	budget := 25.0
	quotes, err := m.computeQuotes(mid, budget)
	if err != nil {
		t.Fatalf("computeQuotes: %v", err)
	}
	if quotes.Bid == nil || quotes.Ask == nil {
		t.Fatalf("expected both bid and ask for budget check")
	}

	totalNotional := quotes.Bid.Price*quotes.Bid.Size + quotes.Ask.Price*quotes.Ask.Size
	if totalNotional > budget+1e-9 {
		t.Fatalf("combined quoted notional exceeds budget: got %.6f > %.6f", totalNotional, budget)
	}
}

func TestComputeQuotesPricesClamped(t *testing.T) {
	t.Parallel()
	cfg := testStrategyConfig()
	info := testMarketInfo()
	m := setupMaker(cfg, info)

	mid := 0.50
	budget := 1000.0
	quotes, err := m.computeQuotes(mid, budget)
	if err != nil {
		t.Fatalf("computeQuotes: %v", err)
	}

	tick := 0.01 // Tick001

	if quotes.Bid != nil && (quotes.Bid.Price < tick || quotes.Bid.Price >= 1) {
		t.Errorf("bid price %v out of range [%v, 1)", quotes.Bid.Price, tick)
	}
	if quotes.Ask != nil && (quotes.Ask.Price <= 0 || quotes.Ask.Price > 1-tick) {
		t.Errorf("ask price %v out of range (0, %v]", quotes.Ask.Price, 1-tick)
	}
}

func TestComputeQuotesBidBelowAsk(t *testing.T) {
	t.Parallel()
	cfg := testStrategyConfig()
	info := testMarketInfo()
	m := setupMaker(cfg, info)

	mid := 0.50
	budget := 1000.0
	quotes, err := m.computeQuotes(mid, budget)
	if err != nil {
		t.Fatalf("computeQuotes: %v", err)
	}

	if quotes.Bid != nil && quotes.Ask != nil {
		if quotes.Bid.Price >= quotes.Ask.Price {
			t.Errorf("bid %v >= ask %v (crossed)", quotes.Bid.Price, quotes.Ask.Price)
		}
	}
}
