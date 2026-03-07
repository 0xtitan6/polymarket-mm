// Package exchange — SynthesisClient is a REST client for the Synthesis Trade API
// (https://synthesis.trade/api/v1).
//
// # Architecture
//
// SynthesisClient exposes the same method signatures as Client so that the
// engine and strategy layers can be wired with either implementation.
// The cmd/synthesis-bot package creates a SynthesisEngine that accepts
// *SynthesisClient instead of *Client; all other components (Maker, Risk, Scanner)
// work identically.
//
// # Key Differences from Polymarket Client
//
//  1. Auth: X-API-KEY header only — no Ed25519 timestamp signing.
//  2. Order placement: POST /api/v1/wallet/{venue}/{wallet_id}/order
//     with {token_id, side, amount, type, units, price} body (not USOrderRequest).
//  3. Order list: GET /api/v1/wallet/{wallet_id}/orders
//  4. Positions: GET /api/v1/wallet/{wallet_id}/positions
//  5. Markets: GET /api/v1/markets?venue=pol (to be confirmed)
//  6. Orderbook: GET /api/v1/markets/{market_id}/orderbook (to be confirmed)
//  7. BBO: GET /api/v1/markets/{market_id}/prices (to be confirmed)
//
// # Response Envelope
//
// All Synthesis API responses use the shape:
//   {"success": true, "response": <payload>}
// The client unpacks this envelope before parsing the inner payload.
//
// # Venue Codes
//
//   "pol" — Polymarket
//   "sol" — Kalshi (mapped via SOL chain)
//
// # Translation
//
// Synthesis API responses are translated into the existing USMarket,
// USBookResponse, USBBOResponse etc. types so the scanner, book, and strategy
// layers are completely unaware of the Synthesis-specific wire format.
//
// # Cancel Simulation
//
// The Synthesis API does not have a bulk cancel-all endpoint. CancelAll and
// CancelMarketOrders simulate bulk cancellation by fetching open orders and
// cancelling each one individually. This is slightly slower than Polymarket's
// single endpoint but functionally equivalent.
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-resty/resty/v2"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

const (
	synthesisBaseURL = "https://synthesis.trade/api/v1"
)

// SynthesisClient is the Synthesis Trade API REST client.
// It exposes the same methods as Client so it can be used as a drop-in
// in a synthesis-specific engine wiring.
//
// Market data (order book, BBO) is fetched from the Polymarket public CLOB API
// via the embedded PolymarketCLOB client. Order execution and account management
// are handled through the Synthesis Trade API.
type SynthesisClient struct {
	http     *resty.Client
	auth     *SynthesisAuth
	rl       *RateLimiter
	polyCLOB *PolymarketCLOB // public market data source (Polymarket CLOB)
	walletID string          // Synthesis wallet ID (from config.Auth.WalletID)
	venue    string          // "pol" or "sol" (from config.Auth.Venue)
	dryRun   bool
	logger   *slog.Logger
}

// NewSynthesisClient creates a SynthesisClient from a SynthesisConfig.
//
// The client reuses the same RateLimiter as the Polymarket client — the
// Synthesis API limits are not yet published; the Polymarket limits are used
// as a conservative default. Adjust via the token-bucket parameters if needed.
func NewSynthesisClient(cfg config.SynthesisConfig, auth *SynthesisAuth, logger *slog.Logger) *SynthesisClient {
	baseURL := cfg.API.BaseURL
	if baseURL == "" {
		baseURL = synthesisBaseURL
	}

	httpClient := resty.New().
		SetBaseURL(baseURL).
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

	// Create the Polymarket CLOB client for public market data
	polyCLOBURL := cfg.API.PolyCLOBBaseURL
	polyCLOB := NewPolymarketCLOB(polyCLOBURL, logger)

	return &SynthesisClient{
		http:     httpClient,
		auth:     auth,
		rl:       NewRateLimiter(),
		polyCLOB: polyCLOB,
		walletID: cfg.Auth.WalletID,
		venue:    cfg.Auth.Venue,
		dryRun:   cfg.DryRun,
		logger:   logger.With("component", "synthesis_client"),
	}
}

// setAuthHeaders attaches the X-API-KEY header to a resty request.
// The Synthesis API requires only this one header; no signing is needed.
func (c *SynthesisClient) setAuthHeaders(req *resty.Request) *resty.Request {
	return req.SetHeaders(c.auth.Headers())
}

// unwrapEnvelope parses the {"success": bool, "response": ...} envelope
// that all Synthesis API calls return. It returns the inner "response" payload
// as raw JSON, or an error if success is false or the envelope is malformed.
func unwrapEnvelope(body []byte) (json.RawMessage, error) {
	var env types.SynthesisEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("synthesis: failed to parse envelope: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("synthesis: API returned success=false: %s", string(body))
	}
	return env.Response, nil
}

// ————————————————————————————————————————————————————————————————————————
// Markets
// ————————————————————————————————————————————————————————————————————————

// GetMarkets fetches markets from GET /api/v1/markets?venue=<venue>.
// Results are translated from SynthesisMarket → USMarket so the scanner
// and engine work without modification.
func (c *SynthesisClient) GetMarkets(ctx context.Context, params types.MarketQueryParams) ([]types.USMarket, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	req := c.setAuthHeaders(c.http.R().SetContext(ctx))
	req.SetQueryParam("venue", c.venue)

	// Forward filters from the standard MarketQueryParams
	if params.Active != nil {
		req.SetQueryParam("active", boolStr(*params.Active))
	}
	if params.Closed != nil {
		req.SetQueryParam("closed", boolStr(*params.Closed))
	}
	if params.Archived != nil {
		req.SetQueryParam("archived", boolStr(*params.Archived))
	}
	if params.Limit > 0 {
		req.SetQueryParam("limit", intStr(params.Limit))
	}
	if params.Offset > 0 {
		req.SetQueryParam("offset", intStr(params.Offset))
	}

	resp, err := req.Get("/markets")
	if err != nil {
		return nil, fmt.Errorf("synthesis get markets: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("synthesis get markets: status %d: %s", resp.StatusCode(), resp.String())
	}

	// Try to unwrap the {"success": true, "response": ...} envelope first
	payload, envErr := unwrapEnvelope(resp.Body())

	if envErr == nil {
		// Envelope found — parse inner response
		// Inner response may be an array or an object with "markets" key
		var markets []types.SynthesisMarket
		if err := json.Unmarshal(payload, &markets); err == nil {
			return synthesisMarketsToUS(markets), nil
		}
		var envelope types.SynthesisMarketsResponse
		if err := json.Unmarshal(payload, &envelope); err == nil && len(envelope.Markets) > 0 {
			return synthesisMarketsToUS(envelope.Markets), nil
		}
		return nil, fmt.Errorf("synthesis get markets: parse inner response: %s", string(payload))
	}

	// No envelope — try legacy shapes for backwards compatibility
	raw := resp.Body()
	var envelope types.SynthesisMarketsResponse
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Markets) > 0 {
		return synthesisMarketsToUS(envelope.Markets), nil
	}
	var bare []types.SynthesisMarket
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, fmt.Errorf("synthesis get markets: parse response: %w", err)
	}
	return synthesisMarketsToUS(bare), nil
}

// GetUSOrderBook fetches the full order book from the Polymarket public CLOB API
// and translates the response into USBookResponse.
//
// This delegates to the PolymarketCLOB client which hits:
//   GET https://clob.polymarket.com/book?token_id=X
// No authentication is required.
func (c *SynthesisClient) GetUSOrderBook(ctx context.Context, marketID string) (*types.USBookResponse, error) {
	return c.polyCLOB.GetUSOrderBook(ctx, marketID)
}

// GetOrderBook fetches the full order book from the Polymarket CLOB and returns it
// in the legacy BookResponse format for callers that still use the old type.
func (c *SynthesisClient) GetOrderBook(ctx context.Context, marketID string) (*types.BookResponse, error) {
	return c.polyCLOB.GetBookResponse(ctx, marketID)
}

// GetBBO fetches the best bid/offer from the Polymarket public CLOB API.
//
// This uses the /price endpoint with BUY and SELL sides to derive the BBO.
// No authentication is required.
func (c *SynthesisClient) GetBBO(ctx context.Context, marketID string) (*types.USBBOResponse, error) {
	return c.polyCLOB.GetBBO(ctx, marketID)
}

// PolyCLOB returns the embedded Polymarket CLOB client for direct access
// by other components (e.g. the scanner, the WS feed).
func (c *SynthesisClient) PolyCLOB() *PolymarketCLOB {
	return c.polyCLOB
}

// ————————————————————————————————————————————————————————————————————————
// Orders
// ————————————————————————————————————————————————————————————————————————

// PlaceOrder places a single order via POST /api/v1/wallet/{venue}/{wallet_id}/order.
//
// The USOrderRequest is translated to the Synthesis wire format:
//   - MarketSlug → token_id
//   - IntentBuyLong / IntentBuyShort → side = "BUY"
//   - IntentSellLong / IntentSellShort → side = "SELL"
//   - Quantity → amount (denominated in USDC, matching the caller expectation)
//   - Price.Value → price
//   - ORDER_TYPE_LIMIT → type = "LIMIT" / ORDER_TYPE_MARKET → "MARKET"
//
// Response envelope: {"success": true, "response": {"order_id": "..."}}
func (c *SynthesisClient) PlaceOrder(ctx context.Context, order types.USOrderRequest) (*types.USOrderResponse, error) {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would place synthesis order",
			"market", order.MarketSlug,
			"intent", order.Intent,
			"price", order.Price.Value,
			"qty", order.Quantity,
		)
		return &types.USOrderResponse{ID: "dry-run-synth-" + order.MarketSlug}, nil
	}

	if err := c.rl.Order.Wait(ctx); err != nil {
		return nil, err
	}
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	synthReq := usOrderToSynthesis(order)
	path := fmt.Sprintf("/wallet/%s/%s/order", c.venue, c.walletID)

	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx)).
		SetBody(synthReq).
		Post(path)
	if err != nil {
		return nil, fmt.Errorf("synthesis place order: %w", err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusCreated {
		return nil, fmt.Errorf("synthesis place order: status %d: %s", resp.StatusCode(), resp.String())
	}

	// Unwrap {"success": true, "response": {"order_id": "..."}}
	payload, envErr := unwrapEnvelope(resp.Body())
	if envErr != nil {
		return nil, fmt.Errorf("synthesis place order: %w", envErr)
	}

	var result types.SynthesisOrderResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("synthesis place order: parse response: %w", err)
	}

	usResp := synthesisOrderResponseToUS(&result)
	c.logger.Info("synthesis order placed",
		"id", usResp.ID,
		"market", order.MarketSlug,
		"price", order.Price.Value,
	)
	return usResp, nil
}

// CancelOrder cancels a single order.
//
// The Synthesis API does not have a documented per-order cancel endpoint yet.
// We use DELETE /api/v1/wallet/{wallet_id}/orders/{order_id} as a reasonable
// default path — update when the confirmed spec is available.
func (c *SynthesisClient) CancelOrder(ctx context.Context, orderID, marketSlug string) error {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel synthesis order", "id", orderID, "market", marketSlug)
		return nil
	}

	if err := c.rl.Order.Wait(ctx); err != nil {
		return err
	}
	if err := c.rl.Global.Wait(ctx); err != nil {
		return err
	}

	// Best-guess cancel path — update when spec is confirmed
	path := fmt.Sprintf("/wallet/%s/orders/%s", c.walletID, orderID)

	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx)).
		Delete(path)
	if err != nil {
		return fmt.Errorf("synthesis cancel order: %w", err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("synthesis cancel order: status %d: %s", resp.StatusCode(), resp.String())
	}

	c.logger.Info("synthesis order cancelled", "id", orderID)
	return nil
}

// CancelMarketOrders cancels all open orders for the given market slug.
//
// Synthesis has no bulk-cancel endpoint, so this method:
//  1. Fetches all open orders for the wallet (GET /api/v1/wallet/{wallet_id}/orders)
//  2. Filters to the specified market slug (via token_id prefix match)
//  3. Issues individual CancelOrder calls for each matching order
//
// Returns a CancelResponse (legacy format) for the engine/strategy layer.
func (c *SynthesisClient) CancelMarketOrders(ctx context.Context, slug string) (*types.CancelResponse, error) {
	openOrders, err := c.getOpenOrdersRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("synthesis cancel market orders: list: %w", err)
	}

	var canceled []string
	for _, o := range openOrders {
		// Match by token_id containing the slug, or exact slug match
		if o.TokenID != slug && !slugMatch(o.TokenID, slug) {
			continue
		}
		if err := c.CancelOrder(ctx, o.OrderID, slug); err != nil {
			c.logger.Warn("synthesis cancel market order failed", "id", o.OrderID, "error", err)
			continue
		}
		canceled = append(canceled, o.OrderID)
	}

	c.logger.Info("synthesis market orders cancelled", "slug", slug, "count", len(canceled))
	return &types.CancelResponse{Canceled: canceled}, nil
}

// CancelAll cancels every open order for this wallet.
//
// Simulated by fetching all open orders and cancelling each one individually.
func (c *SynthesisClient) CancelAll(ctx context.Context) (*types.USCancelResponse, error) {
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel all synthesis orders")
		return &types.USCancelResponse{}, nil
	}

	openOrders, err := c.getOpenOrdersRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("synthesis cancel all: list: %w", err)
	}

	var canceled []string
	for _, o := range openOrders {
		if err := c.CancelOrder(ctx, o.OrderID, o.TokenID); err != nil {
			c.logger.Warn("synthesis cancel all: failed to cancel order", "id", o.OrderID, "error", err)
			continue
		}
		canceled = append(canceled, o.OrderID)
	}

	c.logger.Warn("synthesis all orders cancelled", "count", len(canceled))
	return &types.USCancelResponse{CanceledOrderIDs: canceled}, nil
}

// GetOpenOrders returns all open resting orders for this wallet,
// translated from the Synthesis format to USOpenOrder.
// If slugs is non-empty, filters to those market slugs.
func (c *SynthesisClient) GetOpenOrders(ctx context.Context, slugs []string) ([]types.USOpenOrder, error) {
	if err := c.rl.Book.Wait(ctx); err != nil {
		return nil, err
	}
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	raw, err := c.getOpenOrdersRaw(ctx)
	if err != nil {
		return nil, err
	}

	// Translate and optionally filter by slug
	slugSet := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		slugSet[s] = true
	}

	var result []types.USOpenOrder
	for _, o := range raw {
		if len(slugSet) > 0 && !slugSet[o.TokenID] && !slugMatchAny(o.TokenID, slugs) {
			continue
		}
		result = append(result, synthesisOrderToUS(o))
	}
	return result, nil
}

// getOpenOrdersRaw fetches raw Synthesis open orders without filtering or rate limiting.
// Called by CancelAll, CancelMarketOrders, GetOpenOrders.
//
// Response envelope: {"success": true, "response": [{...}, ...]}
func (c *SynthesisClient) getOpenOrdersRaw(ctx context.Context) ([]types.SynthesisOpenOrder, error) {
	path := fmt.Sprintf("/wallet/%s/orders", c.walletID)

	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx)).
		Get(path)
	if err != nil {
		return nil, fmt.Errorf("synthesis get orders: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("synthesis get orders: status %d: %s", resp.StatusCode(), resp.String())
	}

	// Unwrap {"success": true, "response": [...]}
	raw := resp.Body()
	if payload, envErr := unwrapEnvelope(raw); envErr == nil {
		// Inner response is an array of orders
		var orders []types.SynthesisOpenOrder
		if err := json.Unmarshal(payload, &orders); err != nil {
			return nil, fmt.Errorf("synthesis get orders: parse inner response: %w", err)
		}
		return orders, nil
	}

	// Fallback: try legacy shapes
	var envelope types.SynthesisEnvelope
	_ = json.Unmarshal(raw, &envelope)

	var bare []types.SynthesisOpenOrder
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, fmt.Errorf("synthesis get orders: parse: %w", err)
	}
	return bare, nil
}

// ————————————————————————————————————————————————————————————————————————
// Portfolio / Account
// ————————————————————————————————————————————————————————————————————————

// GetPositions returns current positions from GET /api/v1/wallet/{wallet_id}/positions.
// The Synthesis positions are translated to the map[string]USPosition format
// expected by the engine — keyed by token_id (acting as market slug).
//
// Response envelope: {"success": true, "response": [{...}, ...]}
func (c *SynthesisClient) GetPositions(ctx context.Context) (map[string]types.USPosition, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/wallet/%s/positions", c.walletID)

	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx)).
		Get(path)
	if err != nil {
		return nil, fmt.Errorf("synthesis get positions: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("synthesis get positions: status %d: %s", resp.StatusCode(), resp.String())
	}

	// Unwrap {"success": true, "response": [...]}
	raw := resp.Body()
	if payload, envErr := unwrapEnvelope(raw); envErr == nil {
		var positions []types.SynthesisPosition
		if err := json.Unmarshal(payload, &positions); err != nil {
			return nil, fmt.Errorf("synthesis get positions: parse inner response: %w", err)
		}
		return synthesisPositionsToUS(positions), nil
	}

	// Fallback: try bare array
	var bare []types.SynthesisPosition
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, fmt.Errorf("synthesis get positions: parse: %w", err)
	}
	return synthesisPositionsToUS(bare), nil
}

// GetBalances returns account balances from GET /api/v1/wallet.
// Synthesis wallets serve the same role as Polymarket balance accounts.
// The wallet balance is returned as a single USBalance entry keyed by USDC.
//
// Response envelope: {"success": true, "response": [{...}, ...]}
func (c *SynthesisClient) GetBalances(ctx context.Context) ([]types.USBalance, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return nil, err
	}

	resp, err := c.setAuthHeaders(c.http.R().SetContext(ctx)).
		Get("/wallet")
	if err != nil {
		return nil, fmt.Errorf("synthesis get balances: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("synthesis get balances: status %d: %s", resp.StatusCode(), resp.String())
	}

	// Unwrap {"success": true, "response": [...]}
	raw := resp.Body()
	if payload, envErr := unwrapEnvelope(raw); envErr == nil {
		var wallets []types.SynthesisWallet
		if err := json.Unmarshal(payload, &wallets); err != nil {
			return nil, fmt.Errorf("synthesis get balances: parse inner response: %w", err)
		}
		return synthesisWalletsToBalances(wallets, c.walletID), nil
	}

	// Fallback: try bare array
	var bare []types.SynthesisWallet
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, fmt.Errorf("synthesis get balances: parse: %w", err)
	}
	return synthesisWalletsToBalances(bare, c.walletID), nil
}

// DeriveAPIKey is a no-op compatibility stub. The Synthesis API uses a static
// X-API-KEY header; there is no derivation step.
// Returns an empty Credentials struct so the engine's DeriveAPIKey call path
// (which only fires when HasL2Credentials returns false) is safe.
func (c *SynthesisClient) DeriveAPIKey(_ context.Context) (*Credentials, error) {
	c.logger.Info("DeriveAPIKey called (no-op on Synthesis API)")
	return &Credentials{}, nil
}

// ————————————————————————————————————————————————————————————————————————
// Compatibility shims
// ————————————————————————————————————————————————————————————————————————

// CancelOrders cancels a list of orders by ID, returning a CancelResponse
// compatible with the strategy layer's old CLOB interface.
func (c *SynthesisClient) CancelOrders(ctx context.Context, orderIDs []string) (*types.CancelResponse, error) {
	if len(orderIDs) == 0 {
		return &types.CancelResponse{}, nil
	}
	if c.dryRun {
		c.logger.Info("DRY-RUN: would cancel synthesis orders", "count", len(orderIDs))
		return &types.CancelResponse{Canceled: orderIDs}, nil
	}

	canceled := make([]string, 0, len(orderIDs))
	for _, id := range orderIDs {
		if err := c.CancelOrder(ctx, id, ""); err != nil {
			c.logger.Warn("synthesis cancel order failed", "id", id, "error", err)
			continue
		}
		canceled = append(canceled, id)
	}
	return &types.CancelResponse{Canceled: canceled}, nil
}

// PostOrders places a batch of orders via the Synthesis PlaceOrder endpoint.
// The negRisk parameter is accepted for signature compatibility but ignored —
// Synthesis uses side-based ordering without neg-risk distinction.
func (c *SynthesisClient) PostOrders(ctx context.Context, orders []types.UserOrder, negRisk bool) ([]types.OrderResponse, error) {
	if len(orders) == 0 {
		return nil, nil
	}
	if c.dryRun {
		c.logger.Info("DRY-RUN: would post synthesis orders", "count", len(orders))
		results := make([]types.OrderResponse, len(orders))
		for i := range orders {
			results[i] = types.OrderResponse{
				Success: true,
				OrderID: fmt.Sprintf("dry-run-synth-%d", i),
				Status:  "live",
			}
		}
		return results, nil
	}

	results := make([]types.OrderResponse, 0, len(orders))
	for _, order := range orders {
		usReq := userOrderToUSRequest(order) // reuse Polymarket helper (same conversion)
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

// ————————————————————————————————————————————————————————————————————————
// Translation helpers
// ————————————————————————————————————————————————————————————————————————

// synthesisMarketsToUS converts a slice of SynthesisMarket to USMarket.
// The engine/scanner expects USMarket; this translation preserves all
// relevant fields and sets sensible defaults for missing ones.
func synthesisMarketsToUS(sm []types.SynthesisMarket) []types.USMarket {
	result := make([]types.USMarket, len(sm))
	for i, m := range sm {
		tickSz := 0.01
		if m.TickSize > 0 {
			tickSz = m.TickSize
		}
		result[i] = types.USMarket{
			ID:          m.ID,
			Slug:        m.Slug,
			Question:    m.Question,
			Description: m.Description,
			Category:    m.Category,
			Active:      m.Active,
			Closed:      m.Closed,
			EndDate:     m.EndDate,
			// MarketSides not populated — Synthesis doesn't use the same side model
			OrderPriceMinTickSize: tickSz,
			OrderMinSize:          m.MinOrderSize,
			BestBid:               m.BestBid,
			BestAsk:               m.BestAsk,
			Spread:                m.Spread,
			LastTradePrice:        m.LastTradePrice,
			LiquidityNum:          m.LiquidityNum,
			Volume24hr:            m.Volume24hr,
			AcceptingOrders:       m.Active && !m.Closed,
		}
	}
	return result
}

// synthesisBookToUS converts a SynthesisOrderBook to USBookResponse.
func synthesisBookToUS(b *types.SynthesisOrderBook) *types.USBookResponse {
	bids := make([]types.USBookLevel, len(b.Bids))
	for i, lv := range b.Bids {
		bids[i] = types.USBookLevel{
			Px:  types.USPrice{Value: lv.Price, Currency: "USD"},
			Qty: lv.Size,
		}
	}
	offers := make([]types.USBookLevel, len(b.Asks))
	for i, lv := range b.Asks {
		offers[i] = types.USBookLevel{
			Px:  types.USPrice{Value: lv.Price, Currency: "USD"},
			Qty: lv.Size,
		}
	}
	return &types.USBookResponse{
		MarketData: types.USMarketData{
			MarketSlug:   b.MarketID,
			Bids:         bids,
			Offers:       offers,
			TransactTime: b.Timestamp,
		},
	}
}

// synthesisPricesToBBO converts a SynthesisPricesResponse to USBBOResponse.
func synthesisPricesToBBO(p *types.SynthesisPricesResponse, marketID string) *types.USBBOResponse {
	slug := p.MarketID
	if slug == "" {
		slug = marketID
	}
	bestBid := types.USPrice{Value: p.BestBid, Currency: "USD"}
	bestAsk := types.USPrice{Value: p.BestAsk, Currency: "USD"}

	// Parse bid/ask depths as 0 — Synthesis prices endpoint may not include depth
	return &types.USBBOResponse{
		MarketData: types.USBBOMarketData{
			MarketSlug:  slug,
			BestBid:     bestBid,
			BestAsk:     bestAsk,
			LastTradePx: types.USPrice{Value: p.LastTradePrice, Currency: "USD"},
		},
	}
}

// usOrderToSynthesis translates a USOrderRequest to the Synthesis order body.
//
// Intent mapping:
//
//	IntentBuyLong / IntentBuyShort  → side = "BUY"
//	IntentSellLong / IntentSellShort → side = "SELL"
//
// Order type mapping:
//
//	ORDER_TYPE_LIMIT  → "LIMIT"
//	ORDER_TYPE_MARKET → "MARKET"
//
// Amount is sent as USDC-denominated (USD notional) by default.
// Price is passed verbatim from USOrderRequest.Price.Value.
func usOrderToSynthesis(o types.USOrderRequest) types.SynthesisOrderRequest {
	side := types.BUY
	switch o.Intent {
	case types.IntentSellLong, types.IntentSellShort:
		side = types.SELL
	}

	orderType := types.SynthesisOrderTypeLimit
	if o.Type == types.USOrderTypeMarket {
		orderType = types.SynthesisOrderTypeMarket
	}

	amount := fmt.Sprintf("%.6f", o.Quantity)

	req := types.SynthesisOrderRequest{
		TokenID: o.MarketSlug, // Synthesis uses token_id for the market identifier
		Side:    side,
		Amount:  amount,
		Type:    orderType,
		Units:   types.SynthesisUnitsUSDC,
	}
	if orderType == types.SynthesisOrderTypeLimit {
		req.Price = o.Price.Value
	}
	return req
}

// synthesisOrderResponseToUS translates a SynthesisOrderResponse to USOrderResponse.
func synthesisOrderResponseToUS(r *types.SynthesisOrderResponse) *types.USOrderResponse {
	return &types.USOrderResponse{
		ID: r.OrderID,
	}
}

// synthesisOrderToUS translates a SynthesisOpenOrder to USOpenOrder.
func synthesisOrderToUS(o types.SynthesisOpenOrder) types.USOpenOrder {
	intent := types.IntentBuyLong
	if o.Side == types.SELL {
		intent = types.IntentSellLong
	}
	orderType := types.USOrderTypeLimit
	if o.Type == types.SynthesisOrderTypeMarket {
		orderType = types.USOrderTypeMarket
	}
	state := types.OrderStateNew
	switch o.Status {
	case "live", "open":
		state = types.OrderStateNew
	case "partially_filled":
		state = types.OrderStatePartiallyFilled
	case "filled":
		state = types.OrderStateFilled
	case "cancelled", "canceled":
		state = types.OrderStateCanceled
	}
	return types.USOpenOrder{
		ID:             o.OrderID,
		MarketSlug:     o.TokenID,
		Intent:         intent,
		Type:           orderType,
		Price:          types.USPrice{Value: o.Price, Currency: "USD"},
		Quantity:       o.Amount,
		CumQuantity:    o.Filled,
		LeavesQuantity: subtractStrings(o.Amount, o.Filled),
		Status:         state,
		CreatedAt:      o.CreatedAt,
	}
}

// synthesisPositionsToUS converts Synthesis positions to the map[string]USPosition
// format expected by the engine.
func synthesisPositionsToUS(positions []types.SynthesisPosition) map[string]types.USPosition {
	result := make(map[string]types.USPosition, len(positions))
	for _, p := range positions {
		key := p.TokenID
		size, _ := strconv.ParseFloat(p.Size, 64)
		avgPrice, _ := strconv.ParseFloat(p.AvgPrice, 64)
		cost := size * avgPrice
		result[key] = types.USPosition{
			NetPosition: size,
			QtyBought:   size, // Synthesis may not split bought/sold separately
			Cost:        cost,
		}
	}
	return result
}

// synthesisWalletsToBalances converts Synthesis wallets to []USBalance.
// Finds the wallet matching walletID (or the first wallet if no match).
// Note: Synthesis wallet response does not include a direct "balance" field;
// actual balance data may need to come from positions or a separate endpoint.
// For now we return a placeholder balance entry.
func synthesisWalletsToBalances(wallets []types.SynthesisWallet, walletID string) []types.USBalance {
	var target *types.SynthesisWallet
	for i := range wallets {
		if wallets[i].WalletID == walletID {
			target = &wallets[i]
			break
		}
	}
	if target == nil && len(wallets) > 0 {
		target = &wallets[0]
	}
	if target == nil {
		return nil
	}

	// The Synthesis wallet API doesn't return a balance field directly.
	// Return a placeholder — the balance may need to come from an external source
	// or a future Synthesis endpoint.
	return []types.USBalance{{
		Currency:       "USDC",
		CurrentBalance: 0, // populated at runtime from positions or external source
		BuyingPower:    0,
	}}
}

// slugMatch checks whether the Synthesis token_id corresponds to the given slug.
// Synthesis may encode the slug inside a longer token_id string.
func slugMatch(tokenID, slug string) bool {
	if len(tokenID) < len(slug) {
		return false
	}
	return tokenID[:len(slug)] == slug
}

// slugMatchAny returns true if tokenID matches any of the given slugs.
func slugMatchAny(tokenID string, slugs []string) bool {
	for _, s := range slugs {
		if tokenID == s || slugMatch(tokenID, s) {
			return true
		}
	}
	return false
}

// subtractStrings subtracts decimal string b from decimal string a.
// Returns "0" on parse errors. Used for leavesQuantity = amount - filledAmt.
func subtractStrings(a, b string) string {
	av, errA := strconv.ParseFloat(a, 64)
	bv, errB := strconv.ParseFloat(b, 64)
	if errA != nil || errB != nil {
		return "0"
	}
	return fmt.Sprintf("%.6f", av-bv)
}
