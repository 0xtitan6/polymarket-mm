package exchange

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

func newDryRunClient() *Client {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return &Client{
		dryRun: true,
		rl:     NewRateLimiter(),
		logger: logger,
	}
}

func TestDryRunPostOrders(t *testing.T) {
	t.Parallel()
	c := newDryRunClient()

	orders := []types.UserOrder{
		{TokenID: "tok1", Price: 0.50, Size: 10, Side: types.BUY, OrderType: types.OrderTypeGTC, TickSize: types.Tick001},
		{TokenID: "tok1", Price: 0.55, Size: 10, Side: types.SELL, OrderType: types.OrderTypeGTC, TickSize: types.Tick001},
	}

	results, err := c.PostOrders(context.Background(), orders, false)
	if err != nil {
		t.Fatalf("PostOrders: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if !r.Success {
			t.Errorf("result[%d].Success = false, want true", i)
		}
		if r.OrderID == "" {
			t.Errorf("result[%d].OrderID is empty", i)
		}
		if r.Status != "live" {
			t.Errorf("result[%d].Status = %q, want \"live\"", i, r.Status)
		}
	}
}

func TestDryRunPostOrdersEmpty(t *testing.T) {
	t.Parallel()
	c := newDryRunClient()

	results, err := c.PostOrders(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("PostOrders: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty orders, got %v", results)
	}
}

func TestDryRunCancelOrders(t *testing.T) {
	t.Parallel()
	c := newDryRunClient()

	resp, err := c.CancelOrders(context.Background(), []string{"order-1", "order-2"})
	if err != nil {
		t.Fatalf("CancelOrders: %v", err)
	}
	if len(resp.Canceled) != 2 {
		t.Errorf("expected 2 canceled, got %d", len(resp.Canceled))
	}
}

func TestDryRunCancelOrdersEmpty(t *testing.T) {
	t.Parallel()
	c := newDryRunClient()

	resp, err := c.CancelOrders(context.Background(), nil)
	if err != nil {
		t.Fatalf("CancelOrders: %v", err)
	}
	if len(resp.Canceled) != 0 {
		t.Errorf("expected 0 canceled, got %d", len(resp.Canceled))
	}
}

func TestDryRunCancelAll(t *testing.T) {
	t.Parallel()
	c := newDryRunClient()

	resp, err := c.CancelAll(context.Background())
	if err != nil {
		t.Fatalf("CancelAll: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestDryRunCancelMarketOrders(t *testing.T) {
	t.Parallel()
	c := newDryRunClient()

	resp, err := c.CancelMarketOrders(context.Background(), "condition-123")
	if err != nil {
		t.Fatalf("CancelMarketOrders: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestNewClientDryRunFromConfig(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	cfg := config.Config{DryRun: true, API: config.APIConfig{CLOBBaseURL: "http://localhost"}}
	auth := &Auth{}
	c := NewClient(cfg, auth, logger)

	if !c.dryRun {
		t.Error("client.dryRun should be true when config.DryRun is true")
	}
}
