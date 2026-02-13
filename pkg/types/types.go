// Package types defines shared data structures used across all packages.
//
// This package is the common vocabulary for the bot — order types, market
// metadata, order book snapshots, and WebSocket event payloads. It has no
// dependencies on internal packages, so it can be imported by any layer.
package types

import (
	"math/big"
	"time"
)

// ————————————————————————————————————————————————————————————————————————
// Core enums
// ————————————————————————————————————————————————————————————————————————

// Side represents the direction of an order: BUY or SELL.
type Side string

const (
	BUY  Side = "BUY"
	SELL Side = "SELL"
)

// OrderType enumerates the supported order lifecycles.
type OrderType string

const (
	OrderTypeGTC OrderType = "GTC" // Good-Til-Cancelled: stays on book until filled or cancelled
)

// SignatureType identifies the signing scheme for the CTF exchange contract.
type SignatureType int

const (
	SigEOA        SignatureType = 0 // externally-owned account (standard wallet)
	SigProxy      SignatureType = 1 // Polymarket proxy / Magic wallet
	SigGnosisSafe SignatureType = 2 // Gnosis Safe multisig
)

// TickSize represents the price granularity for a market. Polymarket supports
// four tick sizes; each market has a fixed tick size that determines the
// minimum price increment and USDC amount rounding precision.
type TickSize string

const (
	Tick01    TickSize = "0.1"    // 1 decimal  — coarse markets
	Tick001   TickSize = "0.01"   // 2 decimals — standard markets (most common)
	Tick0001  TickSize = "0.001"  // 3 decimals — fine-grained markets
	Tick00001 TickSize = "0.0001" // 4 decimals — ultra-precise markets
)

// TickDecimals returns the number of decimal places for a tick size.
func (t TickSize) Decimals() int {
	switch t {
	case Tick01:
		return 1
	case Tick001:
		return 2
	case Tick0001:
		return 3
	case Tick00001:
		return 4
	default:
		return 2
	}
}

// AmountDecimals returns the rounding precision for USDC amounts.
func (t TickSize) AmountDecimals() int {
	switch t {
	case Tick01:
		return 3
	case Tick001:
		return 4
	case Tick0001:
		return 5
	case Tick00001:
		return 6
	default:
		return 4
	}
}

// ————————————————————————————————————————————————————————————————————————
// Market metadata
// ————————————————————————————————————————————————————————————————————————

// MarketInfo is the internal representation of a Polymarket binary market.
// Populated from the Gamma API during scanning and passed to the strategy
// layer for quoting. A binary market has exactly two tokens (YES and NO)
// whose prices always sum to ~$1.
type MarketInfo struct {
	ID          string // Gamma market ID
	ConditionID string // CTF condition ID (used for cancels + user WS subscription)
	Slug        string // human-readable URL slug
	Question    string // the prediction question, e.g. "Will X happen by Y?"

	YesTokenID string // CLOB token ID for the YES outcome
	NoTokenID  string // CLOB token ID for the NO outcome

	TickSize     TickSize // price granularity (determines rounding)
	MinOrderSize float64  // minimum order size in tokens
	NegRisk      bool     // true if this is a neg-risk market (affects CTF exchange)

	Active          bool      // market is live
	Closed          bool      // market has been resolved
	AcceptingOrders bool      // CLOB is accepting new orders
	EndDate         time.Time // when the market is scheduled to resolve
	Liquidity       float64   // total USD liquidity on the book
	Volume24h       float64   // trailing 24-hour volume in USD

	BestBid        float64 // top-of-book bid price
	BestAsk        float64 // top-of-book ask price
	Spread         float64 // bestAsk - bestBid
	LastTradePrice float64 // most recent trade price

	RewardsMinSize   float64 // minimum size to qualify for liquidity rewards
	RewardsMaxSpread float64 // maximum spread to qualify for liquidity rewards
}

// MarketAllocation is emitted by the Scanner to tell the engine which markets
// to trade and how much capital to allocate. Score is the opportunity ranking
// used to prioritize when more markets pass filters than MaxMarketsActive.
type MarketAllocation struct {
	Market         MarketInfo
	MaxPositionUSD float64 // per-market position cap (from risk config)
	Score          float64 // composite opportunity score: spread × √volume × liquidity
}

// ————————————————————————————————————————————————————————————————————————
// Orders
// ————————————————————————————————————————————————————————————————————————

// UserOrder is the high-level order representation produced by the strategy.
// The exchange client converts it to a SignedOrder for the CLOB API.
type UserOrder struct {
	TokenID    string    // which token to trade (YES or NO asset ID)
	Price      float64   // limit price (0.0 to 1.0 for binary markets)
	Size       float64   // quantity in tokens
	Side       Side      // BUY or SELL
	OrderType  OrderType // GTC
	TickSize   TickSize  // market's price granularity (for amount rounding)
	Expiration int64     // unix timestamp, 0 = no expiry
	FeeRateBps int       // fee rate in basis points
}

// SignedOrder is the on-chain order format the CLOB API expects.
// MakerAmount and TakerAmount are in 6-decimal USDC units (1e6 = $1).
//
// For BUY:  maker gives MakerAmount USDC, receives TakerAmount tokens
// For SELL: maker gives MakerAmount tokens, receives TakerAmount USDC
type SignedOrder struct {
	Salt          string        `json:"salt"`
	Maker         string        `json:"maker"`       // funder/proxy wallet address
	Signer        string        `json:"signer"`      // EOA that signs the order
	Taker         string        `json:"taker"`       // zero address = open order
	TokenID       string        `json:"tokenId"`     // CTF token ID
	MakerAmount   *big.Int      `json:"makerAmount"` // what maker gives (scaled to 1e6)
	TakerAmount   *big.Int      `json:"takerAmount"` // what maker receives (scaled to 1e6)
	Side          Side          `json:"side"`
	Expiration    string        `json:"expiration"`    // unix timestamp as string
	Nonce         string        `json:"nonce"`         // replay protection
	FeeRateBps    string        `json:"feeRateBps"`    // fee in basis points as string
	SignatureType SignatureType `json:"signatureType"` // 0 = EOA
	Signature     string        `json:"signature"`     // EIP-712 signature hex
}

// OrderPayload is the REST API request body for POST /orders (batch).
type OrderPayload struct {
	Order     SignedOrder `json:"order"`
	Owner     string      `json:"owner"`              // API key of the order owner
	OrderType OrderType   `json:"orderType"`          // GTC
	PostOnly  bool        `json:"postOnly,omitempty"` // if true, rejects if it would cross
}

// OrderResponse is the REST API response for each order in a batch POST.
type OrderResponse struct {
	Success  bool   `json:"success"`
	ErrorMsg string `json:"errorMsg"`
	OrderID  string `json:"orderID"`
	Status   string `json:"status"` // e.g. "live", "matched"
}

// OpenOrder represents a live resting order on the CLOB.
type OpenOrder struct {
	ID           string `json:"id"`
	Status       string `json:"status"`        // "live", "matched", etc.
	Market       string `json:"market"`        // condition ID
	AssetID      string `json:"asset_id"`      // token ID
	Side         string `json:"side"`          // "BUY" or "SELL"
	OriginalSize string `json:"original_size"` // initial size
	SizeMatched  string `json:"size_matched"`  // how much has filled
	Price        string `json:"price"`         // limit price
}

// CancelResponse is returned by DELETE /orders, /cancel-all, /cancel-market-orders.
type CancelResponse struct {
	Canceled []string `json:"canceled"` // IDs of successfully cancelled orders
}

// QuotePair represents the desired bid and ask the strategy wants active
// for a single market. Nil Bid or Ask means the strategy wants that side
// pulled (no order). The engine compares this to current live orders and
// issues the minimal cancel+place to converge.
type QuotePair struct {
	MarketID    string
	YesTokenID  string
	NoTokenID   string
	Bid         *UserOrder // buy YES at this price/size, nil = no bid
	Ask         *UserOrder // sell YES at this price/size, nil = no ask
	GeneratedAt time.Time
}

// ————————————————————————————————————————————————————————————————————————
// Order book
// ————————————————————————————————————————————————————————————————————————

// PriceLevel is a single bid or ask level in the order book.
// Price and Size are strings because the CLOB API returns them as strings
// to preserve decimal precision.
type PriceLevel struct {
	Price string `json:"price"` // e.g. "0.55"
	Size  string `json:"size"`  // e.g. "100.5"
}

// OrderBookSnapshot is a point-in-time view of one token's order book.
// Maintained locally by market.Book and updated from REST + WebSocket sources.
type OrderBookSnapshot struct {
	AssetID   string       // token ID this book belongs to
	Bids      []PriceLevel // sorted descending by price (best bid first)
	Asks      []PriceLevel // sorted ascending by price (best ask first)
	Hash      string       // server-provided hash for staleness detection
	Timestamp time.Time    // when this snapshot was received
}

// BookResponse is the REST response from GET /book for a single token.
type BookResponse struct {
	Market       string       `json:"market"`
	AssetID      string       `json:"asset_id"`
	Bids         []PriceLevel `json:"bids"`
	Asks         []PriceLevel `json:"asks"`
	Hash         string       `json:"hash"`
	Timestamp    string       `json:"timestamp"`
	MinOrderSize string       `json:"min_order_size"`
	TickSize     string       `json:"tick_size"`
	NegRisk      bool         `json:"neg_risk"`
}

// ————————————————————————————————————————————————————————————————————————
// WebSocket events
// ————————————————————————————————————————————————————————————————————————
// These structs map 1:1 to the JSON messages sent over the Polymarket WebSocket.
// Market channel events: "book" (full snapshot), "price_change" (delta).
// User channel events: "trade" (fill), "order" (placement/cancel lifecycle).

// WSBookEvent is a full order book snapshot from the market WS channel.
// Replaces the entire local book for the given asset.
type WSBookEvent struct {
	EventType string       `json:"event_type"` // always "book"
	AssetID   string       `json:"asset_id"`
	Market    string       `json:"market"` // condition ID
	Timestamp string       `json:"timestamp"`
	Hash      string       `json:"hash"`  // book version hash
	Buys      []PriceLevel `json:"buys"`  // bid levels
	Sells     []PriceLevel `json:"sells"` // ask levels
}

// WSPriceChange is a single price level update within a price_change event.
type WSPriceChange struct {
	AssetID string `json:"asset_id"`
	Price   string `json:"price"`    // the price level that changed
	Size    string `json:"size"`     // new size at that level (0 = removed)
	Side    string `json:"side"`     // "BUY" or "SELL"
	Hash    string `json:"hash"`     // updated book hash
	BestBid string `json:"best_bid"` // new best bid after this change
	BestAsk string `json:"best_ask"` // new best ask after this change
}

// WSPriceChangeEvent is an incremental order book update from the market WS.
// Contains one or more level changes applied atomically.
type WSPriceChangeEvent struct {
	EventType    string          `json:"event_type"` // always "price_change"
	Market       string          `json:"market"`
	Timestamp    string          `json:"timestamp"`
	PriceChanges []WSPriceChange `json:"price_changes"`
}

// WSTradeEvent is a fill notification from the user WS channel.
// Received when one of our orders gets matched against a taker.
type WSTradeEvent struct {
	EventType string `json:"event_type"` // always "trade"
	ID        string `json:"id"`         // trade ID
	Market    string `json:"market"`     // condition ID
	AssetID   string `json:"asset_id"`   // token ID that was traded
	Side      string `json:"side"`       // our side: "BUY" or "SELL"
	Size      string `json:"size"`       // filled quantity
	Price     string `json:"price"`      // fill price
	Outcome   string `json:"outcome"`    // "Yes" or "No"
	Timestamp string `json:"timestamp"`
}

// WSOrderEvent is an order lifecycle notification from the user WS channel.
// Received on order placement, update, or cancellation.
type WSOrderEvent struct {
	EventType       string   `json:"event_type"` // always "order"
	ID              string   `json:"id"`         // order ID
	Market          string   `json:"market"`     // condition ID
	AssetID         string   `json:"asset_id"`   // token ID
	Side            string   `json:"side"`       // "BUY" or "SELL"
	Price           string   `json:"price"`
	OriginalSize    string   `json:"original_size"`
	SizeMatched     string   `json:"size_matched"` // cumulative filled
	Outcome         string   `json:"outcome"`      // "Yes" or "No"
	Owner           string   `json:"owner"`        // API key
	Timestamp       string   `json:"timestamp"`
	Type            string   `json:"type"`             // "PLACEMENT", "UPDATE", "CANCELLATION"
	AssociateTrades []string `json:"associate_trades"` // trade IDs from partial fills
}

// WSSubscribeMsg is the initial subscription message sent when connecting
// to a WebSocket channel. For user channels, Auth must be provided.
type WSSubscribeMsg struct {
	Auth     *WSAuth  `json:"auth,omitempty"`       // required for user channel
	Type     string   `json:"type"`                 // "market" or "user"
	Markets  []string `json:"markets,omitempty"`    // condition IDs (user channel)
	AssetIDs []string `json:"assets_ids,omitempty"` // token IDs (market channel)
}

// WSAuth contains the L2 API credentials for authenticating the user WS channel.
type WSAuth struct {
	ApiKey     string `json:"apiKey"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase"`
}

// WSUpdateMsg is sent to dynamically subscribe or unsubscribe from channels
// after the initial connection is established.
type WSUpdateMsg struct {
	AssetIDs  []string `json:"assets_ids,omitempty"` // token IDs (market channel)
	Markets   []string `json:"markets,omitempty"`    // condition IDs (user channel)
	Operation string   `json:"operation"`            // "subscribe" or "unsubscribe"
}
