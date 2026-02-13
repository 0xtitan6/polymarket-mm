// Package exchange implements the Polymarket CLOB REST and WebSocket clients.
//
// The REST client (Client) talks to the Polymarket CLOB API for order management:
//   - GetOrderBook:       GET  /book               — fetch L2 book for a token
//   - PostOrders:         POST /orders              — batch-place up to 15 signed orders
//   - CancelOrders:       DELETE /orders            — cancel specific orders by ID
//   - CancelAll:          DELETE /cancel-all         — emergency cancel everything
//   - CancelMarketOrders: DELETE /cancel-market-orders — cancel one market's orders
//   - DeriveAPIKey:       GET  /auth/derive-api-key — bootstrap L2 creds from L1 wallet
//
// Every request is rate-limited via per-category TokenBuckets, automatically retried
// on 5xx errors, and authenticated with L2 HMAC headers (except book reads).
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-resty/resty/v2"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

// Client is the Polymarket CLOB REST API client.
// It wraps a resty HTTP client with rate limiting, retry, and auth.
type Client struct {
	http   *resty.Client  // HTTP client with retry + base URL
	auth   *Auth          // L1/L2 auth provider for request signing
	rl     *RateLimiter   // per-endpoint-category rate limiting
	dryRun bool           // when true, mutating methods return fake success without HTTP calls
	logger *slog.Logger
}

// NewClient creates a REST client with rate limiting and retry.
func NewClient(cfg config.Config, auth *Auth, logger *slog.Logger) *Client {
	httpClient := resty.New().
		SetBaseURL(cfg.API.CLOBBaseURL).
		SetTimeout(10 * time.Second).
		SetRetryCount(3).
		SetRetryWaitTime(500 * time.Millisecond).
		SetRetryMaxWaitTime(5 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			if err != nil {
				return true
			}
			return r.StatusCode() >= 500
		}).
		SetHeader("Content-Type", "application/json")

	return &Client{
		http:   httpClient,
		auth:   auth,
		rl:     NewRateLimiter(),
		dryRun: cfg.DryRun,
		logger: logger,
	}
}

// GetOrderBook fetches the order book for a single token.
func (c *Client) GetOrderBook(ctx context.Context, tokenID string) (*types.BookResponse, error) {
	if err := c.rl.Book.Wait(ctx); err != nil {
		return nil, err
	}

	var result types.BookResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetQueryParam("token_id", tokenID).
		SetResult(&result).
		Get("/book")
	if err != nil {
		return nil, fmt.Errorf("get book: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("get book: status %d: %s", resp.StatusCode(), resp.String())
	}
	return &result, nil
}

// buildOrderPayload converts a high-level UserOrder into the on-chain
// SignedOrder + metadata the REST API expects. It converts human-readable
// price/size to big.Int maker/taker amounts at the market's tick precision,
// sets the maker to the funder wallet (proxy), the signer to the EOA,
// and the taker to the zero address (open order, anyone can fill).
func (c *Client) buildOrderPayload(order types.UserOrder) types.OrderPayload {
	tickSize := order.TickSize
	if tickSize == "" {
		tickSize = types.Tick001
	}
	makerAmt, takerAmt := PriceToAmounts(order.Price, order.Size, order.Side, tickSize)

	return types.OrderPayload{
		Order: types.SignedOrder{
			Maker:         c.auth.FunderAddress().Hex(),
			Signer:        c.auth.Address().Hex(),
			Taker:         "0x0000000000000000000000000000000000000000",
			TokenID:       order.TokenID,
			MakerAmount:   makerAmt,
			TakerAmount:   takerAmt,
			Side:          order.Side,
			Expiration:    fmt.Sprintf("%d", order.Expiration),
			Nonce:         "0",
			FeeRateBps:    fmt.Sprintf("%d", order.FeeRateBps),
			SignatureType: c.auth.sigType,
		},
		Owner:     c.auth.creds.ApiKey,
		OrderType: order.OrderType,
	}
}

// PostOrders places up to 15 orders in a batch.
func (c *Client) PostOrders(ctx context.Context, orders []types.UserOrder, negRisk bool) ([]types.OrderResponse, error) {
	if len(orders) == 0 {
		return nil, nil
	}
	if len(orders) > 15 {
		return nil, fmt.Errorf("batch limit is 15 orders, got %d", len(orders))
	}
	if c.dryRun {
		c.logger.Info("DRY-RUN: would post orders", "count", len(orders))
		results := make([]types.OrderResponse, len(orders))
		for i := range orders {
			results[i] = types.OrderResponse{Success: true, OrderID: fmt.Sprintf("dry-run-%d", i), Status: "live"}
		}
		return results, nil
	}
	if err := c.rl.Order.Wait(ctx); err != nil {
		return nil, err
	}

	payloads := make([]types.OrderPayload, len(orders))
	for i, order := range orders {
		payloads[i] = c.buildOrderPayload(order)
	}

	body, err := json.Marshal(payloads)
	if err != nil {
		return nil, fmt.Errorf("marshal orders: %w", err)
	}
	headers, err := c.auth.L2Headers("POST", "/orders", string(body))
	if err != nil {
		return nil, fmt.Errorf("l2 headers: %w", err)
	}

	var results []types.OrderResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetHeaders(headers).
		SetBody(payloads).
		SetResult(&results).
		Post("/orders")
	if err != nil {
		return nil, fmt.Errorf("post orders: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("post orders: status %d: %s", resp.StatusCode(), resp.String())
	}

	return results, nil
}

// CancelOrders cancels multiple orders by ID.
func (c *Client) CancelOrders(ctx context.Context, orderIDs []string) (*types.CancelResponse, error) {
	if len(orderIDs) == 0 {
		return &types.CancelResponse{}, nil
	}
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel orders", "count", len(orderIDs))
		return &types.CancelResponse{Canceled: orderIDs}, nil
	}
	if err := c.rl.Cancel.Wait(ctx); err != nil {
		return nil, err
	}

	payload := struct {
		OrderIDs []string `json:"orderIDs"`
	}{OrderIDs: orderIDs}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal cancel request: %w", err)
	}
	headers, err := c.auth.L2Headers("DELETE", "/orders", string(body))
	if err != nil {
		return nil, fmt.Errorf("l2 headers: %w", err)
	}

	var result types.CancelResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetHeaders(headers).
		SetBody(json.RawMessage(body)).
		SetResult(&result).
		Delete("/orders")
	if err != nil {
		return nil, fmt.Errorf("cancel orders: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("cancel orders: status %d: %s", resp.StatusCode(), resp.String())
	}

	c.logger.Info("orders cancelled", "count", len(result.Canceled))
	return &result, nil
}

// CancelAll cancels every open order across all markets.
func (c *Client) CancelAll(ctx context.Context) (*types.CancelResponse, error) {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel all orders")
		return &types.CancelResponse{}, nil
	}
	if err := c.rl.Cancel.Wait(ctx); err != nil {
		return nil, err
	}

	headers, err := c.auth.L2Headers("DELETE", "/cancel-all", "")
	if err != nil {
		return nil, fmt.Errorf("l2 headers: %w", err)
	}

	var result types.CancelResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetHeaders(headers).
		SetResult(&result).
		Delete("/cancel-all")
	if err != nil {
		return nil, fmt.Errorf("cancel all: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("cancel all: status %d: %s", resp.StatusCode(), resp.String())
	}

	c.logger.Warn("all orders cancelled", "count", len(result.Canceled))
	return &result, nil
}

// CancelMarketOrders cancels all orders for a specific market.
func (c *Client) CancelMarketOrders(ctx context.Context, conditionID string) (*types.CancelResponse, error) {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel market orders", "market", conditionID)
		return &types.CancelResponse{}, nil
	}
	if err := c.rl.Cancel.Wait(ctx); err != nil {
		return nil, err
	}

	body := fmt.Sprintf(`{"market":"%s"}`, conditionID)
	headers, err := c.auth.L2Headers("DELETE", "/cancel-market-orders", body)
	if err != nil {
		return nil, fmt.Errorf("l2 headers: %w", err)
	}

	var result types.CancelResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetHeaders(headers).
		SetBody(json.RawMessage(body)).
		SetResult(&result).
		Delete("/cancel-market-orders")
	if err != nil {
		return nil, fmt.Errorf("cancel market orders: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("cancel market orders: status %d: %s", resp.StatusCode(), resp.String())
	}
	return &result, nil
}

// DeriveAPIKey derives L2 API credentials via L1 authentication.
func (c *Client) DeriveAPIKey(ctx context.Context) (*Credentials, error) {
	headers, err := c.auth.L1Headers(0)
	if err != nil {
		return nil, fmt.Errorf("l1 headers: %w", err)
	}

	var result Credentials
	resp, err := c.http.R().
		SetContext(ctx).
		SetHeaders(headers).
		SetResult(&result).
		Get("/auth/derive-api-key")
	if err != nil {
		return nil, fmt.Errorf("derive api key: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("derive api key: status %d: %s", resp.StatusCode(), resp.String())
	}

	c.auth.SetCredentials(result)
	c.logger.Info("API key derived", "api_key", result.ApiKey)
	return &result, nil
}
