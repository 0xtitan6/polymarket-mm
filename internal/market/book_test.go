package market

import (
	"testing"
	"time"

	"polymarket-mm/pkg/types"
)

const (
	testYesToken = "yes-token-123"
	testNoToken  = "no-token-456"
	testMarket   = "market-abc"
)

func newTestBook() *Book {
	return NewBook(testMarket, testYesToken, testNoToken)
}

func TestApplyBookResponse(t *testing.T) {
	t.Parallel()
	b := newTestBook()

	b.ApplyBookResponse(&types.BookResponse{
		AssetID: testYesToken,
		Bids:    []types.PriceLevel{{Price: "0.55", Size: "100"}, {Price: "0.54", Size: "200"}},
		Asks:    []types.PriceLevel{{Price: "0.57", Size: "150"}},
		Hash:    "abc123",
	})

	bid, ask, ok := b.BestBidAsk()
	if !ok {
		t.Fatal("BestBidAsk returned ok=false after applying snapshot")
	}
	if bid != 0.55 {
		t.Errorf("bid = %v, want 0.55", bid)
	}
	if ask != 0.57 {
		t.Errorf("ask = %v, want 0.57", ask)
	}
}

func TestApplyWSBookEvent(t *testing.T) {
	t.Parallel()
	b := newTestBook()

	b.ApplyBookEvent(types.WSBookEvent{
		AssetID: testYesToken,
		Buys:    []types.PriceLevel{{Price: "0.60", Size: "50"}},
		Sells:   []types.PriceLevel{{Price: "0.62", Size: "75"}},
		Hash:    "ws-hash",
	})

	bid, ask, ok := b.BestBidAsk()
	if !ok {
		t.Fatal("BestBidAsk returned ok=false")
	}
	if bid != 0.60 {
		t.Errorf("bid = %v, want 0.60", bid)
	}
	if ask != 0.62 {
		t.Errorf("ask = %v, want 0.62", ask)
	}
}

func TestMidPrice(t *testing.T) {
	t.Parallel()
	b := newTestBook()

	// Empty book
	mid, ok := b.MidPrice()
	if ok {
		t.Error("MidPrice should return false for empty book")
	}
	if mid != 0 {
		t.Errorf("mid = %v, want 0 for empty book", mid)
	}

	// Populated book
	b.ApplyBookResponse(&types.BookResponse{
		AssetID: testYesToken,
		Bids:    []types.PriceLevel{{Price: "0.50", Size: "100"}},
		Asks:    []types.PriceLevel{{Price: "0.60", Size: "100"}},
		Hash:    "h1",
	})

	mid, ok = b.MidPrice()
	if !ok {
		t.Fatal("MidPrice returned false for populated book")
	}
	if mid != 0.55 {
		t.Errorf("mid = %v, want 0.55", mid)
	}
}

func TestBestBidAskEmpty(t *testing.T) {
	t.Parallel()
	b := newTestBook()

	_, _, ok := b.BestBidAsk()
	if ok {
		t.Error("BestBidAsk should return ok=false for empty book")
	}
}

func TestBestBidAskOneSided(t *testing.T) {
	t.Parallel()
	b := newTestBook()

	// Only bids, no asks
	b.ApplyBookResponse(&types.BookResponse{
		AssetID: testYesToken,
		Bids:    []types.PriceLevel{{Price: "0.50", Size: "100"}},
		Asks:    nil,
		Hash:    "h1",
	})

	_, _, ok := b.BestBidAsk()
	if ok {
		t.Error("BestBidAsk should return ok=false with only bids")
	}
}

func TestIsStale(t *testing.T) {
	t.Parallel()
	b := newTestBook()

	// Never updated → stale
	if !b.IsStale(time.Second) {
		t.Error("new book should be stale")
	}

	// Apply data → fresh
	b.ApplyBookResponse(&types.BookResponse{
		AssetID: testYesToken,
		Bids:    []types.PriceLevel{{Price: "0.50", Size: "100"}},
		Asks:    []types.PriceLevel{{Price: "0.60", Size: "100"}},
		Hash:    "h1",
	})

	if b.IsStale(time.Second) {
		t.Error("just-updated book should not be stale")
	}

	// Wait and check again
	time.Sleep(50 * time.Millisecond)
	if !b.IsStale(10 * time.Millisecond) {
		t.Error("book should be stale after maxAge")
	}
}
