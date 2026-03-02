// Package types — US API types for the Polymarket US REST and WebSocket API.
// These map directly to the JSON schemas documented at https://docs.polymarket.us/
package types

import (
	"encoding/json"
)

// ————————————————————————————————————————————————————————————————————————
// US API Enums
// ————————————————————————————————————————————————————————————————————————

// OrderIntent identifies the direction and position type of an order.
type OrderIntent string

const (
	IntentBuyLong   OrderIntent = "ORDER_INTENT_BUY_LONG"
	IntentSellLong  OrderIntent = "ORDER_INTENT_SELL_LONG"
	IntentBuyShort  OrderIntent = "ORDER_INTENT_BUY_SHORT"
	IntentSellShort OrderIntent = "ORDER_INTENT_SELL_SHORT"
)

// USOrderType identifies the execution type of an order.
type USOrderType string

const (
	USOrderTypeLimit  USOrderType = "ORDER_TYPE_LIMIT"
	USOrderTypeMarket USOrderType = "ORDER_TYPE_MARKET"
)

// TimeInForce identifies the order lifetime policy.
type TimeInForce string

const (
	TIFGoodTillCancel TimeInForce = "TIME_IN_FORCE_GOOD_TILL_CANCEL"
	TIFGoodTillDate   TimeInForce = "TIME_IN_FORCE_GOOD_TILL_DATE"
	TIFImmediateOrCancel TimeInForce = "TIME_IN_FORCE_IMMEDIATE_OR_CANCEL"
	TIFFillOrKill     TimeInForce = "TIME_IN_FORCE_FILL_OR_KILL"
)

// ManualIndicator identifies whether an order was placed manually or by automation.
type ManualIndicator string

const (
	ManualOrderManual    ManualIndicator = "MANUAL_ORDER_INDICATOR_MANUAL"
	ManualOrderAutomatic ManualIndicator = "MANUAL_ORDER_INDICATOR_AUTOMATIC"
)

// MarketState identifies the current state of a market.
type MarketState string

const (
	MarketStateOpen     MarketState = "MARKET_STATE_OPEN"
	MarketStateClosed   MarketState = "MARKET_STATE_CLOSED"
	MarketStateSettled  MarketState = "MARKET_STATE_SETTLED"
)

// OrderState identifies the current state of an order in the exchange.
type OrderState string

const (
	OrderStatePendingNew      OrderState = "ORDER_STATE_PENDING_NEW"
	OrderStateNew             OrderState = "ORDER_STATE_NEW"
	OrderStatePartiallyFilled OrderState = "ORDER_STATE_PARTIALLY_FILLED"
	OrderStateFilled          OrderState = "ORDER_STATE_FILLED"
	OrderStateCanceled        OrderState = "ORDER_STATE_CANCELED"
	OrderStateRejected        OrderState = "ORDER_STATE_REJECTED"
	OrderStateExpired         OrderState = "ORDER_STATE_EXPIRED"
)

// ExecutionType identifies the type of execution event in the private WS feed.
type ExecutionType string

const (
	ExecutionTypeFill     ExecutionType = "EXECUTION_TYPE_FILL"
	ExecutionTypeCanceled ExecutionType = "EXECUTION_TYPE_CANCELED"
	ExecutionTypeNew      ExecutionType = "EXECUTION_TYPE_NEW"
	ExecutionTypeExpired  ExecutionType = "EXECUTION_TYPE_EXPIRED"
	ExecutionTypeRejected ExecutionType = "EXECUTION_TYPE_REJECTED"
)

// ————————————————————————————————————————————————————————————————————————
// Price helper
// ————————————————————————————————————————————————————————————————————————

// USPrice wraps a decimal price value with its currency denomination.
// Used throughout the US API wherever a price is expressed.
type USPrice struct {
	Value    string `json:"value"`    // decimal string, e.g. "0.55"
	Currency string `json:"currency"` // e.g. "USD"
}

// StringArray is a custom type for JSON fields that are returned as
// JSON-encoded strings (e.g. `"[\"Yes\",\"No\"]"`) rather than native arrays.
type StringArray []string

// UnmarshalJSON handles both native JSON arrays and JSON-encoded string arrays.
func (s *StringArray) UnmarshalJSON(data []byte) error {
	// Try native array first
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = arr
		return nil
	}
	// Try JSON-encoded string: "[\"Yes\",\"No\"]"
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	return json.Unmarshal([]byte(str), (*[]string)(s))
}

// ————————————————————————————————————————————————————————————————————————
// Markets API
// ————————————————————————————————————————————————————————————————————————

// USMarketSide represents a single side (instrument) of a market.
// Each market has two sides: long (Yes) and short (No).
type USMarketSide struct {
	ID             string `json:"id"`
	MarketSideType string `json:"marketSideType"` // e.g. "MARKET_SIDE_TYPE_INSTRUMENT"
	Identifier     string `json:"identifier"`     // typically same as market slug
	Description    string `json:"description"`    // e.g. "Chargers", "Yes"
	MarketID       int    `json:"marketId"`
	Long           bool   `json:"long"` // true = Yes/Long side
	ParticipantID  string `json:"participantId,omitempty"`
}

// USMarket is a single market returned by GET /v1/markets.
// Only the most relevant fields for a market-making bot are modelled;
// additional fields are silently ignored during JSON decoding.
type USMarket struct {
	ID              string      `json:"id"`
	Slug            string      `json:"slug"`
	Question        string      `json:"question"`
	Description     string      `json:"description"`
	Category        string      `json:"category"`
	Active          bool        `json:"active"`
	Closed          bool        `json:"closed"`
	Archived        bool        `json:"archived"`
	StartDate       string      `json:"startDate"`
	EndDate         string      `json:"endDate"`
	CreatedAt       string      `json:"createdAt"`
	UpdatedAt       string      `json:"updatedAt"`
	MarketType      string      `json:"marketType"`       // e.g. "moneyline", "binary"
	Hidden          bool        `json:"hidden"`

	// Market sides (instruments)
	MarketSides []USMarketSide `json:"marketSides"`

	// Order constraints
	OrderPriceMinTickSize float64 `json:"orderPriceMinTickSize"`
	OrderMinSize          float64 `json:"orderMinSize"`

	// Outcomes — the API returns these as JSON-encoded strings
	Outcomes      StringArray `json:"outcomes"`
	OutcomePrices StringArray `json:"outcomePrices"`

	// Status
	EP3Status string `json:"ep3Status"` // e.g. "ACTIVE", "EXPIRED"

	// Volume / liquidity / pricing
	LiquidityNum   float64 `json:"liquidityNum"`
	VolumeNum      float64 `json:"volumeNum"`
	Volume24hr     float64 `json:"volume24hr"`
	Spread         float64 `json:"spread"`
	BestBid        float64 `json:"bestBid"`
	BestAsk        float64 `json:"bestAsk"`
	LastTradePrice float64 `json:"lastTradePrice"`

	// Game/event metadata
	GameStartTime    string `json:"gameStartTime"`
	GameID           string `json:"gameId"`
	SportsMarketType string `json:"sportsMarketType"`
	AcceptingOrders  bool   `json:"acceptingOrders"`
}

// USMarketsResponse is the envelope returned by GET /v1/markets.
type USMarketsResponse struct {
	Markets []USMarket `json:"markets"`
}

// MarketQueryParams are query parameters accepted by GET /v1/markets.
type MarketQueryParams struct {
	Active       *bool   `json:"active,omitempty"`
	Closed       *bool   `json:"closed,omitempty"`
	Archived     *bool   `json:"archived,omitempty"`
	LiquidityMin float64 `json:"liquidityNumMin,omitempty"`
	LiquidityMax float64 `json:"liquidityNumMax,omitempty"`
	VolumeMin    float64 `json:"volumeNumMin,omitempty"`
	VolumeMax    float64 `json:"volumeNumMax,omitempty"`
	OrderBy      string  `json:"orderBy,omitempty"`
	Limit        int     `json:"limit,omitempty"`
	Offset       int     `json:"offset,omitempty"`
}

// ————————————————————————————————————————————————————————————————————————
// Order Book API
// ————————————————————————————————————————————————————————————————————————

// USBookLevel is a single price level in the order book (bid or offer).
type USBookLevel struct {
	Px  USPrice `json:"px"`  // price as value+currency object
	Qty string  `json:"qty"` // quantity as decimal string
}

// USBookStats contains OHLC and other market statistics.
type USBookStats struct {
	OpenPx       USPrice `json:"openPx"`
	HighPx       USPrice `json:"highPx"`
	LowPx        USPrice `json:"lowPx"`
	LastTradePx  USPrice `json:"lastTradePx"`
	SettlementPx USPrice `json:"settlementPx"`
	SharesTraded string  `json:"sharesTraded"`
	OpenInterest string  `json:"openInterest"`
}

// USMarketData is the inner object inside USBookResponse.
type USMarketData struct {
	MarketSlug    string        `json:"marketSlug"`
	Bids          []USBookLevel `json:"bids"`
	Offers        []USBookLevel `json:"offers"`
	State         MarketState   `json:"state"`
	Stats         USBookStats   `json:"stats"`
	TransactTime  string        `json:"transactTime"`
}

// USBookResponse is returned by GET /v1/markets/{slug}/book.
type USBookResponse struct {
	MarketData USMarketData `json:"marketData"`
}

// USBBOMarketData is the inner object inside USBBOResponse.
type USBBOMarketData struct {
	MarketSlug      string  `json:"marketSlug"`
	CurrentPx       USPrice `json:"currentPx"`
	LastTradePx     USPrice `json:"lastTradePx"`
	SettlementPx    USPrice `json:"settlementPx"`
	BestBid         USPrice `json:"bestBid"`
	BestAsk         USPrice `json:"bestAsk"`
	BidDepth        int     `json:"bidDepth"`
	AskDepth        int     `json:"askDepth"`
	SharesTraded    string  `json:"sharesTraded"`
	OpenInterest    string  `json:"openInterest"`
}

// USBBOResponse is returned by GET /v1/markets/{slug}/bbo.
// It is a lightweight snapshot containing only best bid/offer and summary stats.
type USBBOResponse struct {
	MarketData USBBOMarketData `json:"marketData"`
}

// ————————————————————————————————————————————————————————————————————————
// Orders API
// ————————————————————————————————————————————————————————————————————————

// USOrderRequest is the body for POST /v1/orders.
type USOrderRequest struct {
	MarketSlug              string          `json:"marketSlug"`
	Intent                  OrderIntent     `json:"intent"`
	Type                    USOrderType     `json:"type"`
	Price                   USPrice         `json:"price"`
	Quantity                float64         `json:"quantity"`
	TIF                     TimeInForce     `json:"tif,omitempty"`
	GoodTillTime            string          `json:"goodTillTime,omitempty"`
	ParticipateDontInitiate bool            `json:"participateDontInitiate,omitempty"`
	ManualOrderIndicator    ManualIndicator `json:"manualOrderIndicator"`
	SynchronousExecution    bool            `json:"synchronousExecution,omitempty"`
}

// USExecution is a single fill event embedded in an order response.
type USExecution struct {
	ID            string        `json:"id"`
	Type          ExecutionType `json:"type"`
	Price         USPrice       `json:"price"`
	Quantity      string        `json:"quantity"`
	TransactTime  string        `json:"transactTime"`
}

// USOrderResponse is returned by POST /v1/orders.
type USOrderResponse struct {
	ID         string        `json:"id"`
	Executions []USExecution `json:"executions"`
}

// USOpenOrder is a single resting order from GET /v1/orders/open.
type USOpenOrder struct {
	ID             string      `json:"id"`
	MarketSlug     string      `json:"marketSlug"`
	Intent         OrderIntent `json:"intent"`
	Type           USOrderType `json:"type"`
	Price          USPrice     `json:"price"`
	Quantity       string      `json:"quantity"`
	CumQuantity    string      `json:"cumQuantity"`
	LeavesQuantity string      `json:"leavesQuantity"`
	AveragePrice   string      `json:"averagePrice"`
	Status         OrderState  `json:"status"`
	TIF            TimeInForce `json:"tif"`
	CreatedAt      string      `json:"createdAt"`
	UpdatedAt      string      `json:"updatedAt"`
}

// USOpenOrdersResponse is the envelope for GET /v1/orders/open.
type USOpenOrdersResponse struct {
	Orders []USOpenOrder `json:"orders"`
}

// USCancelResponse is returned by POST /v1/orders/open/cancel.
type USCancelResponse struct {
	CanceledOrderIDs []string `json:"canceledOrderIds"`
}

// ————————————————————————————————————————————————————————————————————————
// Portfolio API
// ————————————————————————————————————————————————————————————————————————

// USPositionsResponse is the envelope returned by GET /v1/portfolio/positions.
type USPositionsResponse struct {
	Positions          map[string]USPosition `json:"positions"`
	NextCursor         string                `json:"nextCursor"`
	EOF                bool                  `json:"eof"`
	AvailablePositions []interface{}         `json:"availablePositions"`
}

// USPosition is a single market position from GET /v1/portfolio/positions.
// The API returns these inside a USPositionsResponse envelope.
type USPosition struct {
	NetPosition  float64 `json:"netPosition"`
	QtyBought    float64 `json:"qtyBought"`
	QtySold      float64 `json:"qtySold"`
	Cost         float64 `json:"cost"`
	Realized     float64 `json:"realized"`
	CashValue    float64 `json:"cashValue"`
	QtyAvailable float64 `json:"qtyAvailable"`
}

// USBalancesResponse is the envelope returned by GET /v1/account/balances.
type USBalancesResponse struct {
	Balances []USBalance `json:"balances"`
}

// USBalance is a single currency balance from GET /v1/account/balances.
type USBalance struct {
	Currency       string  `json:"currency"`
	CurrentBalance float64 `json:"currentBalance"`
	BuyingPower    float64 `json:"buyingPower"`
	AssetNotional  float64 `json:"assetNotional"`
	AssetAvailable float64 `json:"assetAvailable"`
	PendingCredit  float64 `json:"pendingCredit"`
	OpenOrders     float64 `json:"openOrders"`
	UnsettledFunds float64 `json:"unsettledFunds"`
}

// ————————————————————————————————————————————————————————————————————————
// WebSocket message types
// ————————————————————————————————————————————————————————————————————————

// USWSSubscriptionType is the subscription type string sent to the WS server.
type USWSSubscriptionType string

const (
	SubscriptionTypeMarketData USWSSubscriptionType = "SUBSCRIPTION_TYPE_MARKET_DATA"
	SubscriptionTypeOrder      USWSSubscriptionType = "SUBSCRIPTION_TYPE_ORDER"
)

// USWSSubscribeRequest is the subscription message sent on connect.
// Format: {"request": {"type": "...", "market_slugs": [...]}}
type USWSSubscribeRequest struct {
	Request USWSSubscribeBody `json:"request"`
}

// USWSSubscribeBody is the inner body of USWSSubscribeRequest.
type USWSSubscribeBody struct {
	Type        USWSSubscriptionType `json:"type"`
	MarketSlugs []string             `json:"market_slugs"`
}

// USWSDynamicSubscribe is sent to dynamically subscribe/unsubscribe after connect.
type USWSDynamicSubscribe struct {
	Action string               `json:"action"` // "subscribe" or "unsubscribe"
	Params USWSDynamicParams    `json:"params"`
}

// USWSDynamicParams holds subscription parameters for dynamic subscribe messages.
type USWSDynamicParams struct {
	MarketSlugs       []string               `json:"market_slugs"`
	SubscriptionTypes []USWSSubscriptionType `json:"subscription_types"`
}

// USWSHeartbeat is the heartbeat message sent by the server.
type USWSHeartbeat struct {
	Heartbeat interface{} `json:"heartbeat"`
}

// USWSMarketPayload is the market data payload in a WS book update message.
type USWSMarketPayload struct {
	MarketSlug   string        `json:"market_slug"`
	Bids         []USBookLevel `json:"bids"`
	Offers       []USBookLevel `json:"offers"`
	State        MarketState   `json:"state"`
	Stats        USBookStats   `json:"stats"`
	TransactTime string        `json:"transact_time"`
}

// USWSBookEvent is a full or incremental book update from the market WS feed.
type USWSBookEvent struct {
	Payload USWSMarketPayload `json:"payload"`
}

// USWSOrderExecution is a fill or lifecycle event within a private WS message.
type USWSOrderExecution struct {
	Type          ExecutionType `json:"type"`
	Price         USPrice       `json:"price"`
	Quantity      string        `json:"quantity"`
	TransactTime  string        `json:"transact_time"`
}

// USWSOrderObject is the order details embedded in a private WS message.
type USWSOrderObject struct {
	ID             string      `json:"id"`
	MarketSlug     string      `json:"market_slug"`
	Side           string      `json:"side"`
	Type           USOrderType `json:"type"`
	Price          USPrice     `json:"price"`
	Quantity       string      `json:"quantity"`
	CumQuantity    string      `json:"cum_quantity"`
	LeavesQuantity string      `json:"leaves_quantity"`
	Status         OrderState  `json:"status"`
}

// USWSPrivateEvent is a private order/fill event from the private WS feed.
type USWSPrivateEvent struct {
	Order     USWSOrderObject    `json:"order"`
	Execution USWSOrderExecution `json:"execution"`
}
