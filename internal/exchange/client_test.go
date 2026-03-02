package exchange

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

// newTestClient creates a Client wired to a test HTTP server and a real Auth.
// The test server URL is set as the base URL.
func newTestClient(t *testing.T, serverURL string) (*Client, *Auth) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "test-key-id",
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
		API: config.APIConfig{
			BaseURL: serverURL,
		},
	}

	auth, err := NewAuth(cfg)
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	c := NewClient(cfg, auth, logger)
	return c, auth
}

// newDryRunClient creates a minimal dry-run client without a real auth.
func newDryRunClient(t *testing.T) *Client {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := config.Config{
		DryRun: true,
		Auth: config.AuthConfig{
			APIKeyID:      "dry-run-key",
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
		API: config.APIConfig{BaseURL: "http://localhost"},
	}
	auth, _ := NewAuth(cfg)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewClient(cfg, auth, logger)
}

// ————————————————————————————————————————————————————————————————————————
// Auth header presence tests (using a real test server)
// ————————————————————————————————————————————————————————————————————————

// TestClientSetsAuthHeaders verifies that every request carries the three
// required Ed25519 authentication headers.
func TestClientSetsAuthHeaders(t *testing.T) {
	t.Parallel()

	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(types.USMarketsResponse{Markets: []types.USMarket{}})
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	active := true
	_, err := c.GetMarkets(context.Background(), types.MarketQueryParams{Active: &active})
	if err != nil {
		t.Fatalf("GetMarkets: %v", err)
	}

	for _, h := range []string{"X-Pm-Access-Key", "X-Pm-Timestamp", "X-Pm-Signature"} {
		if capturedHeaders.Get(h) == "" {
			t.Errorf("missing auth header %q", h)
		}
	}
}

// ————————————————————————————————————————————————————————————————————————
// GetMarkets
// ————————————————————————————————————————————————————————————————————————

func TestGetMarketsSuccess(t *testing.T) {
	t.Parallel()

	markets := []types.USMarket{
		{ID: "m1", Slug: "will-btc-hit-100k", Active: true},
		{ID: "m2", Slug: "will-eth-flip-btc", Active: true},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(types.USMarketsResponse{Markets: markets})
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	got, err := c.GetMarkets(context.Background(), types.MarketQueryParams{})
	if err != nil {
		t.Fatalf("GetMarkets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d markets, want 2", len(got))
	}
	if got[0].Slug != "will-btc-hit-100k" {
		t.Errorf("markets[0].Slug = %q, want %q", got[0].Slug, "will-btc-hit-100k")
	}
}

func TestGetMarketsServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	// resty retries 3× on 5xx before surfacing the error
	_, err := c.GetMarkets(context.Background(), types.MarketQueryParams{})
	if err == nil {
		t.Fatal("expected error for 5xx response, got nil")
	}
}

// ————————————————————————————————————————————————————————————————————————
// GetOrderBook
// ————————————————————————————————————————————————————————————————————————

func TestGetOrderBookSuccess(t *testing.T) {
	t.Parallel()

	slug := "will-btc-hit-100k"
	usResp := types.USBookResponse{
		MarketData: types.USMarketData{
			MarketSlug: slug,
			Bids: []types.USBookLevel{
				{Px: types.USPrice{Value: "0.45", Currency: "USD"}, Qty: "500"},
			},
			Offers: []types.USBookLevel{
				{Px: types.USPrice{Value: "0.55", Currency: "USD"}, Qty: "300"},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != fmt.Sprintf("/v1/markets/%s/book", slug) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(usResp)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	// Test legacy GetOrderBook (returns *types.BookResponse)
	got, err := c.GetOrderBook(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetOrderBook: %v", err)
	}
	if got.Market != slug {
		t.Errorf("Market = %q, want %q", got.Market, slug)
	}
	if len(got.Bids) != 1 || got.Bids[0].Price != "0.45" {
		t.Errorf("unexpected bids: %+v", got.Bids)
	}

	// Also test GetUSOrderBook (returns *types.USBookResponse)
	gotUS, err := c.GetUSOrderBook(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetUSOrderBook: %v", err)
	}
	if gotUS.MarketData.MarketSlug != slug {
		t.Errorf("USBookResponse.MarketSlug = %q, want %q", gotUS.MarketData.MarketSlug, slug)
	}
}

// ————————————————————————————————————————————————————————————————————————
// GetBBO
// ————————————————————————————————————————————————————————————————————————

func TestGetBBOSuccess(t *testing.T) {
	t.Parallel()

	slug := "will-eth-flip-btc"
	want := types.USBBOResponse{
		MarketData: types.USBBOMarketData{
			MarketSlug: slug,
			BestBid:    types.USPrice{Value: "0.30", Currency: "USD"},
			BestAsk:    types.USPrice{Value: "0.35", Currency: "USD"},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	got, err := c.GetBBO(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetBBO: %v", err)
	}
	if got.MarketData.BestBid.Value != "0.30" || got.MarketData.BestAsk.Value != "0.35" {
		t.Errorf("BBO = {%v, %v}, want {0.30, 0.35}", got.MarketData.BestBid.Value, got.MarketData.BestAsk.Value)
	}
}

// ————————————————————————————————————————————————————————————————————————
// PlaceOrder (dry-run)
// ————————————————————————————————————————————————————————————————————————

func TestDryRunPlaceOrder(t *testing.T) {
	t.Parallel()
	c := newDryRunClient(t)

	resp, err := c.PlaceOrder(context.Background(), types.USOrderRequest{
		MarketSlug:           "btc-100k",
		Intent:               types.IntentBuyLong,
		Type:                 types.USOrderTypeLimit,
		Price:                types.USPrice{Value: "0.55", Currency: "USD"},
		Quantity:             100,
		TIF:                  types.TIFGoodTillCancel,
		ManualOrderIndicator: types.ManualOrderManual,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ID == "" {
		t.Error("expected non-empty order ID")
	}
}

// ————————————————————————————————————————————————————————————————————————
// PlaceOrder (real server)
// ————————————————————————————————————————————————————————————————————————

func TestPlaceOrderSuccess(t *testing.T) {
	t.Parallel()

	want := types.USOrderResponse{ID: "order-abc123"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/orders" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	got, err := c.PlaceOrder(context.Background(), types.USOrderRequest{
		MarketSlug:           "btc-100k",
		Intent:               types.IntentBuyLong,
		Type:                 types.USOrderTypeLimit,
		Price:                types.USPrice{Value: "0.55", Currency: "USD"},
		Quantity:             100,
		TIF:                  types.TIFGoodTillCancel,
		ManualOrderIndicator: types.ManualOrderManual,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if got.ID != "order-abc123" {
		t.Errorf("ID = %q, want %q", got.ID, "order-abc123")
	}
}

// ————————————————————————————————————————————————————————————————————————
// CancelOrder (dry-run and real server)
// ————————————————————————————————————————————————————————————————————————

func TestDryRunCancelOrder(t *testing.T) {
	t.Parallel()
	c := newDryRunClient(t)

	if err := c.CancelOrder(context.Background(), "order-1", "btc-100k"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
}

func TestCancelOrderSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	if err := c.CancelOrder(context.Background(), "order-xyz", "btc-100k"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
}

// ————————————————————————————————————————————————————————————————————————
// CancelAll
// ————————————————————————————————————————————————————————————————————————

func TestDryRunCancelAll(t *testing.T) {
	t.Parallel()
	c := newDryRunClient(t)

	resp, err := c.CancelAll(context.Background())
	if err != nil {
		t.Fatalf("CancelAll: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestCancelAllSuccess(t *testing.T) {
	t.Parallel()

	want := types.USCancelResponse{CanceledOrderIDs: []string{"o1", "o2", "o3"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify empty slugs body triggers cancel-all
		var body map[string][]string
		json.NewDecoder(r.Body).Decode(&body)
		if len(body["slugs"]) != 0 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "expected empty slugs for cancel-all, got %v", body["slugs"])
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	got, err := c.CancelAll(context.Background())
	if err != nil {
		t.Fatalf("CancelAll: %v", err)
	}
	if len(got.CanceledOrderIDs) != 3 {
		t.Errorf("CanceledOrderIDs len = %d, want 3", len(got.CanceledOrderIDs))
	}
}

// ————————————————————————————————————————————————————————————————————————
// CancelMarketOrders
// ————————————————————————————————————————————————————————————————————————

func TestDryRunCancelMarketOrders(t *testing.T) {
	t.Parallel()
	c := newDryRunClient(t)

	resp, err := c.CancelMarketOrdersBySlugs(context.Background(), []string{"btc-100k", "eth-flip"})
	if err != nil {
		t.Fatalf("CancelMarketOrdersBySlugs: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ————————————————————————————————————————————————————————————————————————
// GetOpenOrders
// ————————————————————————————————————————————————————————————————————————

func TestGetOpenOrdersSuccess(t *testing.T) {
	t.Parallel()

	want := types.USOpenOrdersResponse{
		Orders: []types.USOpenOrder{
			{ID: "open-1", MarketSlug: "btc-100k", Status: types.OrderStateNew},
			{ID: "open-2", MarketSlug: "btc-100k", Status: types.OrderStatePartiallyFilled},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	got, err := c.GetOpenOrders(context.Background(), []string{"btc-100k"})
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d orders, want 2", len(got))
	}
	if got[0].ID != "open-1" {
		t.Errorf("orders[0].ID = %q, want %q", got[0].ID, "open-1")
	}
}

// ————————————————————————————————————————————————————————————————————————
// GetPositions
// ————————————————————————————————————————————————————————————————————————

func TestGetPositionsSuccess(t *testing.T) {
	t.Parallel()

	want := types.USPositionsResponse{
		Positions: map[string]types.USPosition{
			"btc-100k": {NetPosition: 50.0, QtyBought: 50.0, CashValue: 27.5},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	got, err := c.GetPositions(context.Background())
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	pos, ok := got["btc-100k"]
	if !ok {
		t.Fatal("missing position for btc-100k")
	}
	if pos.NetPosition != 50.0 {
		t.Errorf("NetPosition = %v, want 50.0", pos.NetPosition)
	}
}

// ————————————————————————————————————————————————————————————————————————
// GetBalances
// ————————————————————————————————————————————————————————————————————————

func TestGetBalancesSuccess(t *testing.T) {
	t.Parallel()

	want := types.USBalancesResponse{
		Balances: []types.USBalance{
			{Currency: "USD", CurrentBalance: 1000.0, BuyingPower: 900.0},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	got, err := c.GetBalances(context.Background())
	if err != nil {
		t.Fatalf("GetBalances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d balances, want 1", len(got))
	}
	if got[0].Currency != "USD" {
		t.Errorf("Currency = %q, want %q", got[0].Currency, "USD")
	}
	if got[0].CurrentBalance != 1000.0 {
		t.Errorf("CurrentBalance = %v, want 1000.0", got[0].CurrentBalance)
	}
}

// ————————————————————————————————————————————————————————————————————————
// NewClient construction
// ————————————————————————————————————————————————————————————————————————

func TestNewClientDryRunFromConfig(t *testing.T) {
	t.Parallel()

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := config.Config{
		DryRun: true,
		Auth: config.AuthConfig{
			APIKeyID:      "key-id",
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
		API: config.APIConfig{BaseURL: "http://localhost"},
	}
	auth, _ := NewAuth(cfg)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	c := NewClient(cfg, auth, logger)

	if !c.dryRun {
		t.Error("client.dryRun should be true when config.DryRun is true")
	}
}

func TestNewClientUsesConfigBaseURL(t *testing.T) {
	t.Parallel()

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "key-id",
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
		API: config.APIConfig{BaseURL: "https://custom.example.com"},
	}
	auth, _ := NewAuth(cfg)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	c := NewClient(cfg, auth, logger)

	if c.http.HostURL != "https://custom.example.com" {
		t.Errorf("base URL = %q, want %q", c.http.HostURL, "https://custom.example.com")
	}
}

func TestNewClientDefaultsToProductionBaseURL(t *testing.T) {
	t.Parallel()

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "key-id",
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
		API: config.APIConfig{}, // no BaseURL set
	}
	auth, _ := NewAuth(cfg)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	c := NewClient(cfg, auth, logger)

	if c.http.HostURL != "https://api.polymarket.us" {
		t.Errorf("base URL = %q, want %q", c.http.HostURL, "https://api.polymarket.us")
	}
}
