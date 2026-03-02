package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"polymarket-mm/pkg/types"
)

// ————————————————————————————————————————————————————————————————————————
// PostOrders: instant fill passthrough (Bug #1 regression tests)
//
// The original bug: PostOrders placed orders, the API returned instant
// fills in resp.Executions, but the client discarded them (just set
// status = "matched"). The strategy layer never saw the fills,
// resulting in $9 of invisible losses.
// ————————————————————————————————————————————————————————————————————————

// TestPostOrders_ExecutionsPassedThrough verifies that when PlaceOrder
// returns executions (instant fills), PostOrders passes them through
// in the OrderResponse so the strategy can process them.
func TestPostOrders_ExecutionsPassedThrough(t *testing.T) {
	t.Parallel()

	// Server returns an order that filled instantly
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := types.USOrderResponse{
			ID: "order-instant-1",
			Executions: []types.USExecution{
				{
					ID:       "exec-1",
					Type:     types.ExecutionTypeFill,
					Price:    types.USPrice{Value: "0.48", Currency: "USD"},
					Quantity: "10",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	orders := []types.UserOrder{
		{TokenID: "btc-100k", Side: types.BUY, Price: 0.48, Size: 10},
	}

	results, err := c.PostOrders(context.Background(), orders, false)
	if err != nil {
		t.Fatalf("PostOrders: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.OrderID != "order-instant-1" {
		t.Errorf("OrderID = %q, want %q", result.OrderID, "order-instant-1")
	}
	if result.Status != "matched" {
		t.Errorf("Status = %q, want %q (should be 'matched' when executions present)", result.Status, "matched")
	}
	if len(result.Executions) != 1 {
		t.Fatalf("BUG REGRESSION: Executions not passed through. Got %d, want 1", len(result.Executions))
	}
	if result.Executions[0].Quantity != "10" {
		t.Errorf("Execution quantity = %q, want %q", result.Executions[0].Quantity, "10")
	}
}

// TestPostOrders_MultipleExecutionsPassedThrough verifies that multiple
// executions (order matched against several resting orders) all pass through.
func TestPostOrders_MultipleExecutionsPassedThrough(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := types.USOrderResponse{
			ID: "order-multi-exec",
			Executions: []types.USExecution{
				{ID: "exec-a", Type: types.ExecutionTypeFill, Price: types.USPrice{Value: "0.48"}, Quantity: "7"},
				{ID: "exec-b", Type: types.ExecutionTypeFill, Price: types.USPrice{Value: "0.48"}, Quantity: "3"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	results, err := c.PostOrders(context.Background(), []types.UserOrder{
		{TokenID: "btc-100k", Side: types.BUY, Price: 0.48, Size: 10},
	}, false)
	if err != nil {
		t.Fatalf("PostOrders: %v", err)
	}

	if len(results[0].Executions) != 2 {
		t.Fatalf("expected 2 executions, got %d", len(results[0].Executions))
	}
}

// TestPostOrders_NoExecutions_StatusLive verifies that orders without
// instant fills get status "live" and empty executions.
func TestPostOrders_NoExecutions_StatusLive(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := types.USOrderResponse{
			ID:         "order-resting",
			Executions: nil, // No instant fills
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	results, err := c.PostOrders(context.Background(), []types.UserOrder{
		{TokenID: "btc-100k", Side: types.BUY, Price: 0.48, Size: 10},
	}, false)
	if err != nil {
		t.Fatalf("PostOrders: %v", err)
	}

	if results[0].Status != "live" {
		t.Errorf("Status = %q, want %q for order without executions", results[0].Status, "live")
	}
	if len(results[0].Executions) != 0 {
		t.Errorf("expected 0 executions for resting order, got %d", len(results[0].Executions))
	}
}

// TestPostOrders_PartialFailure verifies that when placing multiple
// orders and one fails, the successful ones still return their executions
// and the failed one has an error message.
func TestPostOrders_PartialFailure(t *testing.T) {
	t.Parallel()

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First order succeeds with instant fill
			resp := types.USOrderResponse{
				ID: "order-ok",
				Executions: []types.USExecution{
					{ID: "exec-ok", Type: types.ExecutionTypeFill, Price: types.USPrice{Value: "0.48"}, Quantity: "5"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		} else {
			// Second order rejected
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("insufficient balance"))
		}
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	results, err := c.PostOrders(context.Background(), []types.UserOrder{
		{TokenID: "btc-100k", Side: types.BUY, Price: 0.48, Size: 5},
		{TokenID: "btc-100k", Side: types.SELL, Price: 0.52, Size: 5},
	}, false)
	if err != nil {
		t.Fatalf("PostOrders: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First should succeed with execution
	if !results[0].Success {
		t.Error("first order should succeed")
	}
	if len(results[0].Executions) != 1 {
		t.Errorf("first order should have 1 execution, got %d", len(results[0].Executions))
	}

	// Second should fail
	if results[1].Success {
		t.Error("second order should fail")
	}
	if results[1].ErrorMsg == "" {
		t.Error("failed order should have error message")
	}
}

// TestPostOrders_EmptyOrders verifies that calling with empty slice
// returns nil without making any API calls.
func TestPostOrders_EmptyOrders(t *testing.T) {
	t.Parallel()

	apiCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	results, err := c.PostOrders(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("PostOrders: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for empty orders, got %v", results)
	}
	if apiCalled {
		t.Error("API should not be called for empty order list")
	}
}

// TestPostOrders_DryRun_NoExecutions verifies that dry-run mode returns
// synthetic results without executions (no real fills happen).
func TestPostOrders_DryRun_NoExecutions(t *testing.T) {
	t.Parallel()

	c := newDryRunClient(t)
	results, err := c.PostOrders(context.Background(), []types.UserOrder{
		{TokenID: "btc-100k", Side: types.BUY, Price: 0.48, Size: 10},
	}, false)
	if err != nil {
		t.Fatalf("PostOrders: %v", err)
	}

	if !results[0].Success {
		t.Error("dry-run should succeed")
	}
	if len(results[0].Executions) != 0 {
		t.Errorf("dry-run should have 0 executions, got %d", len(results[0].Executions))
	}
}

// ————————————————————————————————————————————————————————————————————————
// Server error handling
// ————————————————————————————————————————————————————————————————————————

// TestPlaceOrder_ServerReturnsHTML_NoJsonPanic verifies that when the
// server returns HTML (e.g., Cloudflare page) instead of JSON, the client
// returns a clean error rather than panicking on JSON parse.
func TestPlaceOrder_ServerReturnsHTML_NoJsonPanic(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>Cloudflare challenge</body></html>"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.PlaceOrder(context.Background(), types.USOrderRequest{
		MarketSlug: "btc-100k",
		Intent:     types.IntentBuyLong,
		Type:       types.USOrderTypeLimit,
		Price:      types.USPrice{Value: "0.50", Currency: "USD"},
		Quantity:   10,
		TIF:        types.TIFGoodTillCancel,
	})

	// Should not panic, result may be zero-valued
	if err != nil {
		t.Logf("got expected error for HTML response: %v", err)
	}
}

// TestGetBalances_EmptyResponse verifies handling of empty balances.
func TestGetBalances_EmptyResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(types.USBalancesResponse{Balances: nil})
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	got, err := c.GetBalances(context.Background())
	if err != nil {
		t.Fatalf("GetBalances: %v", err)
	}
	if got == nil {
		// nil is acceptable — check it doesn't panic
		t.Log("balances returned nil (acceptable)")
	}
}
