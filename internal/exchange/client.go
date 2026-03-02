// Package exchange implements the Polymarket US REST API client.
//
// The Client wraps a resty HTTP client targeting https://api.polymarket.us
// with Ed25519 auth headers, rate limiting, and automatic retry on 5xx errors.
//
// Key endpoints:
//   - GetMarkets:          GET  /v1/markets                    — market discovery
//   - GetOrderBook:        GET  /v1/markets/{slug}/book        — full L2 book
//   - GetBBO:              GET  /v1/markets/{slug}/bbo         — best bid/offer
//   - PlaceOrder:          POST /v1/orders                     — place single order
//   - CancelOrder:         DELETE /v1/order/{id}/cancel        — cancel by ID
//   - CancelMarketOrders:  POST /v1/orders/open/cancel         — cancel by slug list
//   - CancelAll:           POST /v1/orders/open/cancel         — cancel everything
//   - GetOpenOrders:       GET  /v1/orders/open                — open resting orders
//   - GetPositions:        GET  /v1/portfolio/positions        — current positions
//   - GetBalances:         GET  /v1/account/balances           — account balances
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

const baseURL = "https://api.polymarket.us"

// Client is the Polymarket US REST API client.
// It wraps a resty HTTP client with rate limiting, retry, and Ed25519 auth.
type Client struct {
	http   *resty.Client
	auth   *Auth
	rl     *RateLimiter
	dryRun bool
	logger *slog.Logger
}

// NewClient creates a REST client with rate limiting and retry.
func NewClient(cfg config.Config, auth *Auth, logger *slog.Logger) *Client {
	url := cfg.API.BaseURL
	if url == "" {
		url = baseURL
	}

	httpClient := resty.New().
		SetBaseURL(url).
		SetTimeout(10*time.Second).
		SetRetryCount(3).
		SetRetryWaitTime(500*time.Millisecond).
		SetRetryMaxWaitTime(5*time.Second).
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

// setAuthHeaders attaches Ed25519 auth headers to a resty request for the
// given HTTP method and path (must include leading slash, e.g. "/v1/orders").
func (c *Client) setAuthHeaders(req *resty.Request, method, path string) *resty.Request {
	headers := c.auth.SignRequest(method, path)
	return req.SetHeaders(headers)
}

// ————————————————————————————————————————————————————————————————————————
// Markets
// ————————————————————————————————————————————————————————————————————————

// GetMarkets fetches markets from GET /v1/markets with optional query filters.
func (c *Client) GetMarkets(ctx context.Context, params types.MarketQueryParams) ([]types.USMarket, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	req := c.http.R().SetContext(ctx)
	c.setAuthHeaders(req, "GET", "/v1/markets")

	// Build query params from the struct
	if params.Active != nil {
		req.SetQueryParam("active", boolStr(*params.Active))
	}
	if params.Closed != nil {
		req.SetQueryParam("closed", boolStr(*params.Closed))
	}
	if params.Archived != nil {
		req.SetQueryParam("archived", boolStr(*params.Archived))
	}
	if params.LiquidityMin > 0 {
		req.SetQueryParam("liquidityNumMin", floatStr(params.LiquidityMin))
	}
	if params.LiquidityMax > 0 {
		req.SetQueryParam("liquidityNumMax", floatStr(params.LiquidityMax))
	}
	if params.VolumeMin > 0 {
		req.SetQueryParam("volumeNumMin", floatStr(params.VolumeMin))
	}
	if params.VolumeMax > 0 {
		req.SetQueryParam("volumeNumMax", floatStr(params.VolumeMax))
	}
	if params.OrderBy != "" {
		req.SetQueryParam("orderBy", params.OrderBy)
	}
	if params.Limit > 0 {
		req.SetQueryParam("limit", intStr(params.Limit))
	}
	if params.Offset > 0 {
		req.SetQueryParam("offset", intStr(params.Offset))
	}

	var result types.USMarketsResponse
	resp, err := req.SetResult(&result).Get("/v1/markets")
	if err != nil {
		return nil, fmt.Errorf("get markets: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("get markets: status %d: %s", resp.StatusCode(), resp.String())
	}

	return result.Markets, nil
}

// GetOrderBook fetches the full order book for a market via GET /v1/markets/{slug}/book.
// This is the primary new method returning the US API response type.
func (c *Client) GetUSOrderBook(ctx context.Context, slug string) (*types.USBookResponse, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	path := "/v1/markets/" + slug + "/book"

	var result types.USBookResponse
	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx), "GET", path).
		SetResult(&result).
		Get(path)
	if err != nil {
		return nil, fmt.Errorf("get order book: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("get order book: status %d: %s", resp.StatusCode(), resp.String())
	}

	return &result, nil
}

// GetOrderBook fetches the full order book and returns it in the legacy BookResponse
// format so that existing callers (engine, market/book) continue to compile.
func (c *Client) GetOrderBook(ctx context.Context, slug string) (*types.BookResponse, error) {
	usResp, err := c.GetUSOrderBook(ctx, slug)
	if err != nil {
		return nil, err
	}
	return usBookToLegacy(usResp), nil
}

// usBookToLegacy converts a US API book response to the legacy BookResponse format.
func usBookToLegacy(us *types.USBookResponse) *types.BookResponse {
	if us == nil {
		return nil
	}
	bids := make([]types.PriceLevel, len(us.MarketData.Bids))
	for i, b := range us.MarketData.Bids {
		bids[i] = types.PriceLevel{Price: b.Px.Value, Size: b.Qty}
	}
	asks := make([]types.PriceLevel, len(us.MarketData.Offers))
	for i, a := range us.MarketData.Offers {
		asks[i] = types.PriceLevel{Price: a.Px.Value, Size: a.Qty}
	}
	return &types.BookResponse{
		Market:  us.MarketData.MarketSlug,
		AssetID: us.MarketData.MarketSlug,
		Bids:    bids,
		Asks:    asks,
	}
}

// GetBBO fetches the best bid/offer for a market via GET /v1/markets/{slug}/bbo.
func (c *Client) GetBBO(ctx context.Context, slug string) (*types.USBBOResponse, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	path := "/v1/markets/" + slug + "/bbo"

	var result types.USBBOResponse
	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx), "GET", path).
		SetResult(&result).
		Get(path)
	if err != nil {
		return nil, fmt.Errorf("get bbo: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("get bbo: status %d: %s", resp.StatusCode(), resp.String())
	}

	return &result, nil
}

// ————————————————————————————————————————————————————————————————————————
// Orders
// ————————————————————————————————————————————————————————————————————————

// PlaceOrder places a single order via POST /v1/orders.
func (c *Client) PlaceOrder(ctx context.Context, order types.USOrderRequest) (*types.USOrderResponse, error) {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would place order",
			"market", order.MarketSlug,
			"intent", order.Intent,
			"price", order.Price.Value,
			"qty", order.Quantity,
		)
		return &types.USOrderResponse{ID: "dry-run-" + order.MarketSlug}, nil
	}

	if err := c.rl.Order.Wait(ctx); err != nil {
		return nil, err
	}
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	body, err := json.Marshal(order)
	if err != nil {
		return nil, fmt.Errorf("marshal order: %w", err)
	}

	var result types.USOrderResponse
	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx), "POST", "/v1/orders").
		SetBody(json.RawMessage(body)).
		SetResult(&result).
		Post("/v1/orders")
	if err != nil {
		return nil, fmt.Errorf("place order: %w", err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusCreated {
		return nil, fmt.Errorf("place order: status %d: %s", resp.StatusCode(), resp.String())
	}

	c.logger.Info("order placed", "id", result.ID, "market", order.MarketSlug)
	return &result, nil
}

// CancelOrder cancels a single order via DELETE /v1/order/{orderID}/cancel.
// The marketSlug is sent in the request body as required by the API.
func (c *Client) CancelOrder(ctx context.Context, orderID, marketSlug string) error {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel order", "id", orderID, "market", marketSlug)
		return nil
	}

	if err := c.rl.Order.Wait(ctx); err != nil {
		return err
	}
	if err := c.rl.Global.Wait(ctx); err != nil {
		return err
	}

	path := "/v1/order/" + orderID + "/cancel"
	body, err := json.Marshal(map[string]string{"marketSlug": marketSlug})
	if err != nil {
		return fmt.Errorf("marshal cancel body: %w", err)
	}

	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx), "DELETE", path).
		SetBody(json.RawMessage(body)).
		Delete(path)
	if err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("cancel order: status %d: %s", resp.StatusCode(), resp.String())
	}

	c.logger.Info("order cancelled", "id", orderID)
	return nil
}

// CancelMarketOrders cancels all open orders for the specified market slugs
// via POST /v1/orders/open/cancel.
// This is the compatibility shim: it accepts a single conditionID/slug string
// (as used by the strategy layer) and wraps CancelMarketOrdersBySlugs.
func (c *Client) CancelMarketOrders(ctx context.Context, slug string) (*types.CancelResponse, error) {
	resp, err := c.CancelMarketOrdersBySlugs(ctx, []string{slug})
	if err != nil {
		return nil, err
	}
	return &types.CancelResponse{Canceled: resp.CanceledOrderIDs}, nil
}

// CancelMarketOrdersBySlugs cancels all open orders for the specified market slugs
// via POST /v1/orders/open/cancel.
func (c *Client) CancelMarketOrdersBySlugs(ctx context.Context, slugs []string) (*types.USCancelResponse, error) {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel market orders", "count", len(slugs))
		return &types.USCancelResponse{}, nil
	}

	if err := c.rl.Order.Wait(ctx); err != nil {
		return nil, err
	}
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	body, err := json.Marshal(map[string][]string{"slugs": slugs})
	if err != nil {
		return nil, fmt.Errorf("marshal cancel body: %w", err)
	}

	var result types.USCancelResponse
	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx), "POST", "/v1/orders/open/cancel").
		SetBody(json.RawMessage(body)).
		SetResult(&result).
		Post("/v1/orders/open/cancel")
	if err != nil {
		return nil, fmt.Errorf("cancel market orders: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("cancel market orders: status %d: %s", resp.StatusCode(), resp.String())
	}

	c.logger.Info("market orders cancelled", "count", len(result.CanceledOrderIDs))
	return &result, nil
}

// CancelAll cancels every open order by posting an empty slugs list to
// POST /v1/orders/open/cancel.
func (c *Client) CancelAll(ctx context.Context) (*types.USCancelResponse, error) {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel all orders")
		return &types.USCancelResponse{}, nil
	}

	if err := c.rl.Order.Wait(ctx); err != nil {
		return nil, err
	}
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	// Empty slugs array triggers cancel-all per the API spec
	body := []byte(`{"slugs":[]}`)

	var result types.USCancelResponse
	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx), "POST", "/v1/orders/open/cancel").
		SetBody(json.RawMessage(body)).
		SetResult(&result).
		Post("/v1/orders/open/cancel")
	if err != nil {
		return nil, fmt.Errorf("cancel all: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("cancel all: status %d: %s", resp.StatusCode(), resp.String())
	}

	c.logger.Warn("all orders cancelled", "count", len(result.CanceledOrderIDs))
	return &result, nil
}

// GetOpenOrders returns all open resting orders via GET /v1/orders/open.
// If slugs is non-empty, filters to those market slugs.
func (c *Client) GetOpenOrders(ctx context.Context, slugs []string) ([]types.USOpenOrder, error) {
	if err := c.rl.Book.Wait(ctx); err != nil {
		return nil, err
	}
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	req := c.setAuthHeaders(c.http.R().SetContext(ctx), "GET", "/v1/orders/open")
	for _, s := range slugs {
		req.QueryParam.Add("slugs", s)
	}

	var result types.USOpenOrdersResponse
	resp, err := req.SetResult(&result).Get("/v1/orders/open")
	if err != nil {
		return nil, fmt.Errorf("get open orders: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("get open orders: status %d: %s", resp.StatusCode(), resp.String())
	}

	return result.Orders, nil
}

// ————————————————————————————————————————————————————————————————————————
// Portfolio / Account
// ————————————————————————————————————————————————————————————————————————

// GetPositions returns current positions from GET /v1/portfolio/positions.
// The API returns an envelope with a "positions" map keyed by market slug.
func (c *Client) GetPositions(ctx context.Context) (map[string]types.USPosition, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	var result types.USPositionsResponse
	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx), "GET", "/v1/portfolio/positions").
		SetResult(&result).
		Get("/v1/portfolio/positions")
	if err != nil {
		return nil, fmt.Errorf("get positions: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("get positions: status %d: %s", resp.StatusCode(), resp.String())
	}

	if result.Positions == nil {
		return make(map[string]types.USPosition), nil
	}
	return result.Positions, nil
}

// GetBalances returns account balances from GET /v1/account/balances.
func (c *Client) GetBalances(ctx context.Context) ([]types.USBalance, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	var result types.USBalancesResponse
	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx), "GET", "/v1/account/balances").
		SetResult(&result).
		Get("/v1/account/balances")
	if err != nil {
		return nil, fmt.Errorf("get balances: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("get balances: status %d: %s", resp.StatusCode(), resp.String())
	}

	return result.Balances, nil
}

// DeriveAPIKey is a no-op compatibility stub. The Polymarket US API uses
// statically configured Ed25519 keys; there is no key derivation step.
// The engine calls this when HasL2Credentials() returns false, but since
// HasL2Credentials always returns true on the new Auth, this should never
// be called in practice.
func (c *Client) DeriveAPIKey(_ context.Context) (*Credentials, error) {
	c.logger.Info("DeriveAPIKey called (no-op on US API)")
	return &Credentials{}, nil
}

// ————————————————————————————————————————————————————————————————————————
// Compatibility shims
//
// These methods preserve the old interface used by internal/strategy/ so that
// the strategy layer can continue to compile while the exchange layer targets
// the new US API. They translate the old call signatures to the new ones.
// ————————————————————————————————————————————————————————————————————————

// CancelOrders cancels a list of orders by ID, returning a CancelResponse
// compatible with the old CLOB interface.
// Each order is cancelled individually via DELETE /v1/order/{id}/cancel.
// marketSlug is derived from the first entry's context (empty string if unknown).
func (c *Client) CancelOrders(ctx context.Context, orderIDs []string) (*types.CancelResponse, error) {
	if len(orderIDs) == 0 {
		return &types.CancelResponse{}, nil
	}
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel orders", "count", len(orderIDs))
		return &types.CancelResponse{Canceled: orderIDs}, nil
	}

	// The US API requires a marketSlug for individual order cancels.
	// When cancelling by a list of IDs without slug context, use the
	// cancel-all-by-slugs endpoint instead (empty list = cancel all).
	// Fall back to per-order cancel with empty marketSlug and let the server
	// return an error if required.
	canceled := make([]string, 0, len(orderIDs))
	for _, id := range orderIDs {
		if err := c.CancelOrder(ctx, id, ""); err != nil {
			c.logger.Warn("cancel order failed", "id", id, "error", err)
			continue
		}
		canceled = append(canceled, id)
	}
	return &types.CancelResponse{Canceled: canceled}, nil
}

// PostOrders places a batch of orders using the new US PlaceOrder API.
// The negRisk parameter is accepted for signature compatibility but ignored
// (the US API uses intent-based ordering instead).
func (c *Client) PostOrders(ctx context.Context, orders []types.UserOrder, negRisk bool) ([]types.OrderResponse, error) {
	if len(orders) == 0 {
		return nil, nil
	}
	if c.dryRun {
		c.logger.Info("DRY-RUN: would post orders", "count", len(orders))
		results := make([]types.OrderResponse, len(orders))
		for i := range orders {
			results[i] = types.OrderResponse{Success: true, OrderID: fmt.Sprintf("dry-run-%d", i), Status: "live"}
		}
		return results, nil
	}

	results := make([]types.OrderResponse, 0, len(orders))
	for _, order := range orders {
		usReq := userOrderToUSRequest(order)
		resp, err := c.PlaceOrder(ctx, usReq)
		if err != nil {
			results = append(results, types.OrderResponse{
				Success:  false,
				ErrorMsg: err.Error(),
			})
			continue
		}
		status := "live"
		if len(resp.Executions) > 0 {
			status = "matched"
		}
		results = append(results, types.OrderResponse{
			Success:    true,
			OrderID:    resp.ID,
			Status:     status,
			Executions: resp.Executions,
		})
	}
	return results, nil
}

// userOrderToUSRequest converts a legacy UserOrder to the new US API request format.
func userOrderToUSRequest(o types.UserOrder) types.USOrderRequest {
	intent := types.IntentBuyLong
	if o.Side == types.SELL {
		intent = types.IntentSellLong
	}
	return types.USOrderRequest{
		MarketSlug: o.TokenID, // TokenID as market identifier (caller should use slug)
		Intent:     intent,
		Type:       types.USOrderTypeLimit,
		Price:      types.USPrice{Value: fmt.Sprintf("%.6f", o.Price), Currency: "USD"},
		Quantity:   o.Size,
		TIF:        types.TIFGoodTillCancel,
		ManualOrderIndicator: types.ManualOrderAutomatic,
	}
}

// ————————————————————————————————————————————————————————————————————————
// Helpers
// ————————————————————————————————————————————————————————————————————————

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func floatStr(f float64) string {
	return fmt.Sprintf("%g", f)
}

func intStr(i int) string {
	return fmt.Sprintf("%d", i)
}
