// Package exchange — PolymarketCLOB is a lightweight client for the Polymarket
// public CLOB API (https://clob.polymarket.com).
//
// # Purpose
//
// The Synthesis Trade API does not expose public market data endpoints (no
// order book, no midpoint, no prices). To market-make effectively through
// Synthesis, the bot needs real market data — specifically the public order
// book from Polymarket's CLOB.
//
// This client reads market data ONLY — it never places or cancels orders.
// Order execution goes through SynthesisClient exclusively.
//
// # Architecture
//
//	Read path:  PolymarketCLOB → https://clob.polymarket.com (NO auth)
//	Write path: SynthesisClient → https://synthesis.trade/api/v1 (X-API-KEY)
//
// # Endpoints (all public, no authentication required)
//
//	GET  /book?token_id=X           — full order book (bids + asks)
//	GET  /midpoint?token_id=X       — midpoint price
//	GET  /price?token_id=X&side=S   — best price for a side (BUY or SELL)
//	POST /books                     — batch order books (up to 500 token IDs)
//
// # Rate Limits
//
// The Polymarket CLOB public API is rate-limited. This client uses the same
// RateLimiter as the Synthesis client to avoid hitting limits. The default
// poll interval of 2 seconds is conservative enough for most use cases.
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

	"polymarket-mm/pkg/types"
)

const (
	// polymarketCLOBBaseURL is the public CLOB API base URL.
	// No authentication is required for any of these endpoints.
	polymarketCLOBBaseURL = "https://clob.polymarket.com"
)

// PolymarketCLOB is a read-only client for the Polymarket public CLOB API.
// It fetches order books, midpoints, and prices for token IDs that are
// being traded through Synthesis.
type PolymarketCLOB struct {
	http   *resty.Client
	rl     *RateLimiter
	logger *slog.Logger
}

// NewPolymarketCLOB creates a PolymarketCLOB client.
// The baseURL parameter allows overriding the default for testing;
// pass "" to use the production endpoint.
func NewPolymarketCLOB(baseURL string, logger *slog.Logger) *PolymarketCLOB {
	if baseURL == "" {
		baseURL = polymarketCLOBBaseURL
	}

	httpClient := resty.New().
		SetBaseURL(baseURL).
		SetTimeout(10 * time.Second).
		SetRetryCount(2).
		SetRetryWaitTime(500 * time.Millisecond).
		SetRetryMaxWaitTime(3 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			if err != nil {
				return true
			}
			return r.StatusCode() >= 500 || r.StatusCode() == http.StatusTooManyRequests
		}).
		SetHeader("Accept", "application/json")

	return &PolymarketCLOB{
		http:   httpClient,
		rl:     NewRateLimiter(),
		logger: logger.With("component", "polymarket_clob"),
	}
}

// ————————————————————————————————————————————————————————————————————————
// Order Book
// ————————————————————————————————————————————————————————————————————————

// GetBook fetches the full order book for a single token_id.
// Returns the raw Polymarket CLOB order book response.
//
// Endpoint: GET /book?token_id=X
// No authentication required.
func (c *PolymarketCLOB) GetBook(ctx context.Context, tokenID string) (*types.PolyCLOBOrderBook, error) {
	if err := c.rl.Book.Wait(ctx); err != nil {
		return nil, err
	}

	resp, err := c.http.R().SetContext(ctx).
		SetQueryParam("token_id", tokenID).
		Get("/book")
	if err != nil {
		return nil, fmt.Errorf("polymarket clob get book: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("polymarket clob get book: status %d: %s", resp.StatusCode(), resp.String())
	}

	var book types.PolyCLOBOrderBook
	if err := json.Unmarshal(resp.Body(), &book); err != nil {
		return nil, fmt.Errorf("polymarket clob get book: parse: %w", err)
	}

	return &book, nil
}

// GetUSOrderBook fetches the order book and translates it to USBookResponse
// for compatibility with the engine/book layer.
func (c *PolymarketCLOB) GetUSOrderBook(ctx context.Context, tokenID string) (*types.USBookResponse, error) {
	book, err := c.GetBook(ctx, tokenID)
	if err != nil {
		return nil, err
	}
	return polyCLOBBookToUS(book, tokenID), nil
}

// GetBookResponse fetches the order book and translates it to the legacy
// BookResponse format for callers that use the old type.
func (c *PolymarketCLOB) GetBookResponse(ctx context.Context, tokenID string) (*types.BookResponse, error) {
	book, err := c.GetBook(ctx, tokenID)
	if err != nil {
		return nil, err
	}
	return polyCLOBBookToLegacy(book, tokenID), nil
}

// ————————————————————————————————————————————————————————————————————————
// Midpoint
// ————————————————————————————————————————————————————————————————————————

// GetMidpoint fetches the midpoint price for a single token_id.
//
// Endpoint: GET /midpoint?token_id=X
// No authentication required.
func (c *PolymarketCLOB) GetMidpoint(ctx context.Context, tokenID string) (float64, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return 0, err
	}

	resp, err := c.http.R().SetContext(ctx).
		SetQueryParam("token_id", tokenID).
		Get("/midpoint")
	if err != nil {
		return 0, fmt.Errorf("polymarket clob get midpoint: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return 0, fmt.Errorf("polymarket clob get midpoint: status %d: %s", resp.StatusCode(), resp.String())
	}

	var result types.PolyCLOBMidpointResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return 0, fmt.Errorf("polymarket clob get midpoint: parse: %w", err)
	}

	mid, err := strconv.ParseFloat(result.Mid, 64)
	if err != nil {
		return 0, fmt.Errorf("polymarket clob get midpoint: parse mid value %q: %w", result.Mid, err)
	}

	return mid, nil
}

// ————————————————————————————————————————————————————————————————————————
// Best Price (BBO)
// ————————————————————————————————————————————————————————————————————————

// GetPrice fetches the best price for a given side (BUY or SELL).
//
// Endpoint: GET /price?token_id=X&side=BUY
// No authentication required.
func (c *PolymarketCLOB) GetPrice(ctx context.Context, tokenID string, side types.Side) (float64, error) {
	if err := c.rl.Global.Wait(ctx); err != nil {
		return 0, err
	}

	resp, err := c.http.R().SetContext(ctx).
		SetQueryParam("token_id", tokenID).
		SetQueryParam("side", string(side)).
		Get("/price")
	if err != nil {
		return 0, fmt.Errorf("polymarket clob get price: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return 0, fmt.Errorf("polymarket clob get price: status %d: %s", resp.StatusCode(), resp.String())
	}

	var result types.PolyCLOBPriceResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return 0, fmt.Errorf("polymarket clob get price: parse: %w", err)
	}

	price, err := strconv.ParseFloat(result.Price, 64)
	if err != nil {
		return 0, fmt.Errorf("polymarket clob get price: parse price value %q: %w", result.Price, err)
	}

	return price, nil
}

// GetBBO fetches best bid and best ask from the CLOB and returns a USBBOResponse.
// This uses two /price calls (one for BUY side = best bid, one for SELL side = best ask).
func (c *PolymarketCLOB) GetBBO(ctx context.Context, tokenID string) (*types.USBBOResponse, error) {
	bestBid, bidErr := c.GetPrice(ctx, tokenID, types.BUY)
	bestAsk, askErr := c.GetPrice(ctx, tokenID, types.SELL)

	// If both fail, return the first error
	if bidErr != nil && askErr != nil {
		return nil, fmt.Errorf("polymarket clob get bbo: bid: %w, ask: %v", bidErr, askErr)
	}

	bidStr := "0"
	if bidErr == nil {
		bidStr = fmt.Sprintf("%.4f", bestBid)
	}
	askStr := "0"
	if askErr == nil {
		askStr = fmt.Sprintf("%.4f", bestAsk)
	}

	return &types.USBBOResponse{
		MarketData: types.USBBOMarketData{
			MarketSlug: tokenID,
			BestBid:    types.USPrice{Value: bidStr, Currency: "USD"},
			BestAsk:    types.USPrice{Value: askStr, Currency: "USD"},
		},
	}, nil
}

// GetBBOFromBook fetches the full order book and derives BBO from the top levels.
// More efficient than two separate /price calls when you already need the book.
func (c *PolymarketCLOB) GetBBOFromBook(ctx context.Context, tokenID string) (bestBid, bestAsk float64, err error) {
	book, bookErr := c.GetBook(ctx, tokenID)
	if bookErr != nil {
		return 0, 0, bookErr
	}

	if len(book.Bids) > 0 {
		bestBid, _ = strconv.ParseFloat(book.Bids[0].Price, 64)
	}
	if len(book.Asks) > 0 {
		bestAsk, _ = strconv.ParseFloat(book.Asks[0].Price, 64)
	}

	return bestBid, bestAsk, nil
}

// ————————————————————————————————————————————————————————————————————————
// Translation helpers
// ————————————————————————————————————————————————————————————————————————

// polyCLOBBookToUS converts a Polymarket CLOB order book to USBookResponse
// for the engine's book layer.
func polyCLOBBookToUS(book *types.PolyCLOBOrderBook, tokenID string) *types.USBookResponse {
	bids := make([]types.USBookLevel, len(book.Bids))
	for i, lv := range book.Bids {
		bids[i] = types.USBookLevel{
			Px:  types.USPrice{Value: lv.Price, Currency: "USD"},
			Qty: lv.Size,
		}
	}
	offers := make([]types.USBookLevel, len(book.Asks))
	for i, lv := range book.Asks {
		offers[i] = types.USBookLevel{
			Px:  types.USPrice{Value: lv.Price, Currency: "USD"},
			Qty: lv.Size,
		}
	}

	slug := tokenID
	if book.AssetID != "" {
		slug = book.AssetID
	}

	return &types.USBookResponse{
		MarketData: types.USMarketData{
			MarketSlug:   slug,
			Bids:         bids,
			Offers:       offers,
			TransactTime: book.Timestamp,
		},
	}
}

// polyCLOBBookToLegacy converts a Polymarket CLOB order book to the legacy
// BookResponse format.
func polyCLOBBookToLegacy(book *types.PolyCLOBOrderBook, tokenID string) *types.BookResponse {
	bids := make([]types.PriceLevel, len(book.Bids))
	for i, lv := range book.Bids {
		bids[i] = types.PriceLevel{Price: lv.Price, Size: lv.Size}
	}
	asks := make([]types.PriceLevel, len(book.Asks))
	for i, lv := range book.Asks {
		asks[i] = types.PriceLevel{Price: lv.Price, Size: lv.Size}
	}

	assetID := tokenID
	if book.AssetID != "" {
		assetID = book.AssetID
	}

	return &types.BookResponse{
		Market:  book.Market,
		AssetID: assetID,
		Bids:    bids,
		Asks:    asks,
		Hash:    book.Hash,
	}
}

// polyCLOBBookToWSEvent converts a Polymarket CLOB order book to a USWSBookEvent
// for the polling feed channel.
func polyCLOBBookToWSEvent(book *types.PolyCLOBOrderBook, tokenID string) types.USWSBookEvent {
	bids := make([]types.USBookLevel, len(book.Bids))
	for i, lv := range book.Bids {
		bids[i] = types.USBookLevel{
			Px:  types.USPrice{Value: lv.Price, Currency: "USD"},
			Qty: lv.Size,
		}
	}
	offers := make([]types.USBookLevel, len(book.Asks))
	for i, lv := range book.Asks {
		offers[i] = types.USBookLevel{
			Px:  types.USPrice{Value: lv.Price, Currency: "USD"},
			Qty: lv.Size,
		}
	}

	slug := tokenID
	if book.AssetID != "" {
		slug = book.AssetID
	}

	return types.USWSBookEvent{
		Payload: types.USWSMarketPayload{
			MarketSlug:   slug,
			Bids:         bids,
			Offers:       offers,
			TransactTime: book.Timestamp,
		},
	}
}
