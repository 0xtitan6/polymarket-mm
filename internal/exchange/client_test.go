package exchange

import (
	"context"
	"log/slog"
	"os"
	"strings"
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

func TestBuildOrderPayloadSignsOrder(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := config.Config{
		Wallet: config.WalletConfig{
			PrivateKey:    "0x1111111111111111111111111111111111111111111111111111111111111111",
			ChainID:       137,
			SignatureType: 0,
		},
		API: config.APIConfig{
			CLOBBaseURL: "http://localhost",
			ApiKey:      "test-key",
			Secret:      "test-secret",
			Passphrase:  "test-pass",
		},
	}

	auth, err := NewAuth(cfg)
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}

	c := NewClient(cfg, auth, logger)
	payload, err := c.buildOrderPayload(types.UserOrder{
		TokenID:   "12345678901234567890",
		Price:     0.55,
		Size:      10,
		Side:      types.BUY,
		OrderType: types.OrderTypeGTC,
		TickSize:  types.Tick001,
	})
	if err != nil {
		t.Fatalf("buildOrderPayload: %v", err)
	}

	if payload.Order.Signature == "" || !strings.HasPrefix(payload.Order.Signature, "0x") {
		t.Fatalf("signature = %q, want non-empty 0x-prefixed signature", payload.Order.Signature)
	}
	if payload.Order.Salt == "" || payload.Order.Salt == "0" {
		t.Fatalf("salt = %q, want non-zero", payload.Order.Salt)
	}
	if payload.Order.Nonce != "0" {
		t.Fatalf("nonce = %q, want 0", payload.Order.Nonce)
	}
	if payload.Owner != "test-key" {
		t.Fatalf("owner = %q, want test-key", payload.Owner)
	}
}

func TestBuildOrderPayloadRejectsInvalidTokenID(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := config.Config{
		Wallet: config.WalletConfig{
			PrivateKey:    "0x1111111111111111111111111111111111111111111111111111111111111111",
			ChainID:       137,
			SignatureType: 0,
		},
		API: config.APIConfig{CLOBBaseURL: "http://localhost", ApiKey: "k", Secret: "s", Passphrase: "p"},
	}
	auth, err := NewAuth(cfg)
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	c := NewClient(cfg, auth, logger)

	_, err = c.buildOrderPayload(types.UserOrder{
		TokenID:   "not-a-number",
		Price:     0.50,
		Size:      1,
		Side:      types.BUY,
		OrderType: types.OrderTypeGTC,
		TickSize:  types.Tick001,
	})
	if err == nil {
		t.Fatal("expected error for invalid token ID")
	}
}
