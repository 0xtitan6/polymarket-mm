// Package types — Synthesis Trade API types.
//
// These structs map to the JSON responses from the Synthesis Trade API
// (https://synthesis.trade/api/v1). Where possible, Synthesis responses
// are translated into the existing US API types (USMarket, USBookResponse,
// etc.) so the upstream engine and strategy layers need no changes.
//
// Key differences from the Polymarket US API:
//   - Auth: X-API-KEY header only (no Ed25519/HMAC signing)
//   - Venue: orders are scoped to a venue ("pol" = Polymarket, "sol" = Kalshi)
//   - Wallet ID: each request targets a specific wallet by ID
//   - Order body: uses token_id/side/amount/type/units/price fields
//     (not the USOrderRequest intent-based format)
//   - All responses wrapped in {"success": bool, "response": ...} envelope
package types

import "encoding/json"

// ————————————————————————————————————————————————————————————————————————
// Generic API envelope
//
// All Synthesis API responses use the shape:
//   {"success": true, "response": <payload>}
// where <payload> is either an object or an array depending on the endpoint.
// ————————————————————————————————————————————————————————————————————————

// SynthesisEnvelope is the generic response wrapper for all Synthesis API calls.
type SynthesisEnvelope struct {
	Success  bool            `json:"success"`
	Response json.RawMessage `json:"response"`
}

// ————————————————————————————————————————————————————————————————————————
// Synthesis Wallet API
// ————————————————————————————————————————————————————————————————————————

// SynthesisChainAddresses holds per-chain wallet addresses.
// Example: {"POL": {"address": "0x..."}, "SOL": {"address": "..."}}
type SynthesisChainAddress struct {
	Address string `json:"address"`
}

// SynthesisWallet represents a Synthesis wallet from GET /api/v1/wallet.
//
// Response fields (from API docs):
//   - wallet_id: Unique wallet identifier
//   - name: Wallet display name
//   - chains: Chain addresses (POL, SOL)
//   - position: Wallet ordering position
//   - autoredeem: Auto-redeem resolved positions
//   - created_at: Wallet creation timestamp
type SynthesisWallet struct {
	WalletID   string                           `json:"wallet_id"`
	Name       string                           `json:"name"`
	Chains     map[string]SynthesisChainAddress `json:"chains"`     // keyed by "POL", "SOL"
	Position   int                              `json:"position"`   // wallet ordering position
	AutoRedeem bool                             `json:"autoredeem"` // auto-redeem resolved positions
	CreatedAt  string                           `json:"created_at"`
}

// SynthesisWalletCreateRequest is the body for POST /api/v1/wallet.
// Parameters: name (string, optional — wallet display name)
type SynthesisWalletCreateRequest struct {
	Name string `json:"name,omitempty"`
}

// SynthesisWalletUpdateRequest is the body for PUT /api/v1/wallet/{wallet_id}.
// Parameters:
//   - name: New wallet display name
//   - autoredeem: Auto-redeem resolved positions
type SynthesisWalletUpdateRequest struct {
	Name       string `json:"name,omitempty"`
	AutoRedeem *bool  `json:"autoredeem,omitempty"`
}

// SynthesisWalletCreateResponse is the response for POST /api/v1/wallet.
// Shape: {"wallet_id": "...", "name": "...", "autoredeem": true}
type SynthesisWalletCreateResponse struct {
	WalletID   string `json:"wallet_id"`
	Name       string `json:"name"`
	AutoRedeem bool   `json:"autoredeem"`
}

// SynthesisWalletDeleteResponse is the response for DELETE /api/v1/wallet/{wallet_id}.
// Shape: {"wallet_id": "...", "deleted": true}
type SynthesisWalletDeleteResponse struct {
	WalletID string `json:"wallet_id"`
	Deleted  bool   `json:"deleted"`
}

// ————————————————————————————————————————————————————————————————————————
// Synthesis Order API
// ————————————————————————————————————————————————————————————————————————

// SynthesisOrderType maps to the "type" field in a Synthesis order request.
type SynthesisOrderType string

const (
	SynthesisOrderTypeMarket SynthesisOrderType = "MARKET"
	SynthesisOrderTypeLimit  SynthesisOrderType = "LIMIT"
)

// SynthesisOrderSide is the direction of an order, matching the Side enum.
// We reuse the existing "BUY"/"SELL" strings directly.
type SynthesisOrderSide = Side // alias — same string values

// SynthesisUnits identifies what the "amount" field is denominated in.
type SynthesisUnits string

const (
	SynthesisUnitsUSDC   SynthesisUnits = "USDC"
	SynthesisUnitsShares SynthesisUnits = "SHARES"
)

// SynthesisOrderRequest is the body for POST /api/v1/wallet/{venue}/{wallet_id}/order.
//
// Parameters (from API docs):
//   - token_id:  string* — Polymarket condition token ID (pol) or Kalshi token mint address (sol)
//   - side:      string* — "BUY" or "SELL"
//   - amount:    string* — Amount in specified units
//   - type:      string* — "MARKET" or "LIMIT"
//   - units:     string* — "USDC" or "SHARES"
//   - price:     string  — Required for LIMIT orders (Polymarket only). Value between 0 and 1.
//
// Example (limit buy):
//
//	{
//	  "token_id": "some-market-token-id",
//	  "side":     "BUY",
//	  "amount":   "10",
//	  "type":     "LIMIT",
//	  "units":    "USDC",
//	  "price":    "0.45"
//	}
type SynthesisOrderRequest struct {
	// TokenID is the market/token identifier.
	// Polymarket venue: condition token ID
	// Kalshi venue: token mint address
	TokenID string `json:"token_id"`

	// Side is "BUY" or "SELL".
	Side SynthesisOrderSide `json:"side"`

	// Amount is the order size as a decimal string, denominated in Units.
	Amount string `json:"amount"`

	// Type is "MARKET" or "LIMIT".
	Type SynthesisOrderType `json:"type"`

	// Units specifies whether Amount is expressed in "USDC" or "SHARES".
	Units SynthesisUnits `json:"units"`

	// Price is the limit price as a decimal string (e.g. "0.45").
	// Required for LIMIT orders (Polymarket only). Value between 0 and 1.
	// Ignored for MARKET orders.
	Price string `json:"price,omitempty"`
}

// SynthesisOrderResponse is the "response" payload from
// POST /api/v1/wallet/{venue}/{wallet_id}/order.
//
// Envelope: {"success": true, "response": {"order_id": "..."}}
type SynthesisOrderResponse struct {
	// OrderID is the exchange-assigned order identifier.
	OrderID string `json:"order_id"`
}

// ————————————————————————————————————————————————————————————————————————
// Synthesis Order List
// ————————————————————————————————————————————————————————————————————————

// SynthesisOpenOrder represents a resting order from GET /api/v1/wallet/{wallet_id}/orders.
//
// Response fields (from API docs):
//   - order_id:   string — Order identifier
//   - venue:      string — "polymarket" or "kalshi"
//   - token_id:   string — Token identifier
//   - side:       string — "BUY" or "SELL"
//   - type:       string — "MARKET" or "LIMIT"
//   - status:     string — Order status
//   - amount:     string — Order amount
//   - filled:     string — Amount filled
//   - price:      string — Order price
//   - created_at: string — Order creation timestamp
type SynthesisOpenOrder struct {
	OrderID   string             `json:"order_id"`
	Venue     string             `json:"venue"`    // "polymarket" or "kalshi"
	TokenID   string             `json:"token_id"`
	Side      SynthesisOrderSide `json:"side"`
	Type      SynthesisOrderType `json:"type"`
	Status    string             `json:"status"`
	Amount    string             `json:"amount"`
	Filled    string             `json:"filled"` // amount filled
	Price     string             `json:"price"`
	CreatedAt string             `json:"created_at"`
}

// ————————————————————————————————————————————————————————————————————————
// Synthesis Position API
// ————————————————————————————————————————————————————————————————————————

// SynthesisPosition represents a position from GET /api/v1/wallet/{wallet_id}/positions.
//
// Response fields (from API docs):
//   - venue:         string — "polymarket" or "kalshi"
//   - token_id:      string — Token identifier
//   - size:          string — Position size in shares
//   - avg_price:     string — Average entry price
//   - current_price: string — Current market price
//   - pnl:           string — Profit and loss
type SynthesisPosition struct {
	Venue        string `json:"venue"`         // "polymarket" or "kalshi"
	TokenID      string `json:"token_id"`
	Size         string `json:"size"`          // position size in shares
	AvgPrice     string `json:"avg_price"`     // average entry price
	CurrentPrice string `json:"current_price"` // current market price
	PnL          string `json:"pnl"`           // profit and loss
}

// ————————————————————————————————————————————————————————————————————————
// Synthesis Market Data
//
// Note: the exact paths and shapes for market data endpoints are not yet
// confirmed by the user. The following types represent a reasonable default
// based on common prediction-market API patterns. Update when the actual
// spec is available.
// ————————————————————————————————————————————————————————————————————————

// SynthesisMarket represents a single market from GET /api/v1/markets.
// Fields are mapped to the existing USMarket type by SynthesisClient.GetMarkets.
type SynthesisMarket struct {
	// Core identity
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	TokenID string `json:"token_id,omitempty"` // Synthesis-internal token identifier

	// Human-readable metadata
	Question    string `json:"question"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`

	// Lifecycle
	Active  bool   `json:"active"`
	Closed  bool   `json:"closed"`
	EndDate string `json:"end_date,omitempty"`
	Venue   string `json:"venue"` // "polymarket" or "kalshi"

	// Pricing & liquidity (may be zero if not provided inline)
	BestBid        float64 `json:"best_bid,omitempty"`
	BestAsk        float64 `json:"best_ask,omitempty"`
	LastTradePrice float64 `json:"last_trade_price,omitempty"`
	Spread         float64 `json:"spread,omitempty"`
	LiquidityNum   float64 `json:"liquidity,omitempty"`
	Volume24hr     float64 `json:"volume_24h,omitempty"`

	// Order constraints
	MinOrderSize float64 `json:"min_order_size,omitempty"`
	TickSize     float64 `json:"tick_size,omitempty"`
}

// SynthesisMarketsResponse is the envelope for GET /api/v1/markets.
// The API may return a top-level array or a wrapped object.
type SynthesisMarketsResponse struct {
	Markets []SynthesisMarket `json:"markets"`
}

// ————————————————————————————————————————————————————————————————————————
// Synthesis Order Book
// ————————————————————————————————————————————————————————————————————————

// SynthesisBookLevel is a single price level in a Synthesis order book.
type SynthesisBookLevel struct {
	Price string `json:"price"` // decimal string, e.g. "0.55"
	Size  string `json:"size"`  // decimal string, e.g. "100.5"
}

// SynthesisOrderBook is returned by GET /api/v1/markets/{market_id}/orderbook.
type SynthesisOrderBook struct {
	MarketID  string               `json:"market_id"`
	Bids      []SynthesisBookLevel `json:"bids"` // sorted descending
	Asks      []SynthesisBookLevel `json:"asks"` // sorted ascending
	Timestamp string               `json:"timestamp,omitempty"`
}

// SynthesisOrderBookResponse is the outer envelope for the orderbook endpoint.
// Some API versions nest the book inside an "orderbook" key; others return it flat.
type SynthesisOrderBookResponse struct {
	OrderBook SynthesisOrderBook `json:"orderbook"`
}

// ————————————————————————————————————————————————————————————————————————
// Synthesis Prices (BBO)
// ————————————————————————————————————————————————————————————————————————

// SynthesisPricesResponse is returned by GET /api/v1/markets/{market_id}/prices.
// It provides a lightweight best-bid/offer snapshot without the full book.
type SynthesisPricesResponse struct {
	MarketID       string `json:"market_id"`
	BestBid        string `json:"best_bid"`
	BestAsk        string `json:"best_ask"`
	LastTradePrice string `json:"last_trade_price,omitempty"`
	MidPrice       string `json:"mid_price,omitempty"`
	Timestamp      string `json:"timestamp,omitempty"`
}

// ————————————————————————————————————————————————————————————————————————
// Cancel / Error helpers
// ————————————————————————————————————————————————————————————————————————

// SynthesisCancelOrderResponse is returned when cancelling an individual order.
// The Synthesis API does not have a bulk-cancel endpoint; the client simulates it.
type SynthesisCancelOrderResponse struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"` // e.g. "cancelled"
	Message string `json:"message,omitempty"`
}

// SynthesisErrorResponse is a generic API error envelope.
type SynthesisErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Code    int    `json:"code,omitempty"`
}

// ————————————————————————————————————————————————————————————————————————
// Polymarket Public CLOB API types
//
// The Polymarket CLOB API (https://clob.polymarket.com) provides public
// market data with NO authentication required. These types are used to
// read order books, midpoints, and prices from Polymarket while routing
// order execution through the Synthesis Trade API.
//
// Key endpoints:
//   GET /book?token_id=X          — full order book
//   GET /midpoint?token_id=X      — midpoint price
//   GET /price?token_id=X&side=S  — best price for side
//   POST /books                   — batch order books (up to 500 tokens)
// ————————————————————————————————————————————————————————————————————————

// PolyCLOBBookLevel is a single price level from the Polymarket CLOB /book endpoint.
type PolyCLOBBookLevel struct {
	Price string `json:"price"` // decimal string, e.g. "0.55"
	Size  string `json:"size"`  // decimal string, e.g. "100.5"
}

// PolyCLOBOrderBook is the response from GET /book?token_id=X.
//
// Example response:
//
//	{
//	  "market": "0x1234...",
//	  "asset_id": "token_id_value",
//	  "bids": [{"price": "0.55", "size": "100"}],
//	  "asks": [{"price": "0.60", "size": "50"}],
//	  "hash": "abc123",
//	  "timestamp": "1234567890"
//	}
type PolyCLOBOrderBook struct {
	Market       string              `json:"market"`
	AssetID      string              `json:"asset_id"`
	Bids         []PolyCLOBBookLevel `json:"bids"` // sorted descending by price
	Asks         []PolyCLOBBookLevel `json:"asks"` // sorted ascending by price
	Hash         string              `json:"hash"`
	Timestamp    string              `json:"timestamp"`
	MinTickSize  string              `json:"min_tick_size,omitempty"`
	NegRisk      bool                `json:"neg_risk,omitempty"`
	LastTradePrice string            `json:"last_trade_price,omitempty"`
}

// PolyCLOBMidpointResponse is the response from GET /midpoint?token_id=X.
//
// Example response:
//
//	{"mid": "0.575"}
type PolyCLOBMidpointResponse struct {
	Mid string `json:"mid"` // midpoint price as decimal string
}

// PolyCLOBPriceResponse is the response from GET /price?token_id=X&side=BUY.
//
// Example response:
//
//	{"price": "0.55"}
type PolyCLOBPriceResponse struct {
	Price string `json:"price"` // best price for the requested side
}
