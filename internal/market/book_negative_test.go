package market

import (
	"testing"

	"polymarket-mm/pkg/types"
)

// ————————————————————————————————————————————————————————————————————————
// Book: negative tests for BestBidAskWithDepth
// ————————————————————————————————————————————————————————————————————————

// TestBestBidAskWithDepth_EmptyBook verifies empty book returns ok=false.
func TestBestBidAskWithDepth_EmptyBook(t *testing.T) {
	t.Parallel()
	b := NewBook("test", "yes-tok", "no-tok")

	_, _, _, _, ok := b.BestBidAskWithDepth()
	if ok {
		t.Error("empty book should return ok=false")
	}
}

// TestBestBidAskWithDepth_OneSideMissing verifies partial book returns ok=false.
func TestBestBidAskWithDepth_OneSideMissing(t *testing.T) {
	t.Parallel()
	b := NewBook("test", "yes-tok", "no-tok")

	// Only bids, no asks
	b.ApplyBookResponse(&types.BookResponse{
		AssetID: "yes-tok",
		Bids:    []types.PriceLevel{{Price: "0.50", Size: "100"}},
		Asks:    []types.PriceLevel{},
		Hash:    "h1",
	})

	_, _, _, _, ok := b.BestBidAskWithDepth()
	if ok {
		t.Error("book with only bids should return ok=false")
	}
}

// TestBestBidAskWithDepth_ValidBook verifies correct depth extraction.
func TestBestBidAskWithDepth_ValidBook(t *testing.T) {
	t.Parallel()
	b := NewBook("test", "yes-tok", "no-tok")

	b.ApplyBookResponse(&types.BookResponse{
		AssetID: "yes-tok",
		Bids:    []types.PriceLevel{{Price: "0.48", Size: "150"}},
		Asks:    []types.PriceLevel{{Price: "0.52", Size: "200"}},
		Hash:    "h1",
	})

	bid, ask, bidDepth, askDepth, ok := b.BestBidAskWithDepth()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if bid != 0.48 {
		t.Errorf("bid = %v, want 0.48", bid)
	}
	if ask != 0.52 {
		t.Errorf("ask = %v, want 0.52", ask)
	}
	if bidDepth != 150 {
		t.Errorf("bidDepth = %v, want 150", bidDepth)
	}
	if askDepth != 200 {
		t.Errorf("askDepth = %v, want 200", askDepth)
	}
}

// TestBestBidAskWithDepth_ZeroDepth verifies that zero-depth levels
// are still reported (e.g., if a level has price but 0 size).
func TestBestBidAskWithDepth_ZeroDepth(t *testing.T) {
	t.Parallel()
	b := NewBook("test", "yes-tok", "no-tok")

	b.ApplyBookResponse(&types.BookResponse{
		AssetID: "yes-tok",
		Bids:    []types.PriceLevel{{Price: "0.48", Size: "0"}},
		Asks:    []types.PriceLevel{{Price: "0.52", Size: "0"}},
		Hash:    "h1",
	})

	_, _, bidDepth, askDepth, ok := b.BestBidAskWithDepth()
	if !ok {
		t.Fatal("expected ok=true even with zero-size levels")
	}
	if bidDepth != 0 || askDepth != 0 {
		t.Errorf("expected zero depth, got bid=%v ask=%v", bidDepth, askDepth)
	}
}
