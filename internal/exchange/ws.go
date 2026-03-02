// ws.go implements WebSocket feeds for real-time Polymarket US data.
//
// Two independent feeds run concurrently:
//
//   - Market feed (public):  wss://api.polymarket.us/v1/ws/markets
//     Subscribes by market slug. Receives full book snapshots and incremental
//     updates containing bids, offers, and stats.
//
//   - Private feed (authenticated): wss://api.polymarket.us/v1/ws/private
//     Subscribes globally. Receives order lifecycle events and fill notifications.
//
// Both feeds authenticate during the WebSocket HTTP upgrade handshake by
// sending the same Ed25519 headers (X-PM-Access-Key, X-PM-Timestamp,
// X-PM-Signature) as HTTP headers on the upgrade request.
//
// Both feeds auto-reconnect with exponential backoff (1s → 30s max) and
// re-subscribe to all tracked slugs on reconnection. A read deadline (90s)
// ensures silent server failures are detected within ~2 missed heartbeats.
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"polymarket-mm/pkg/types"
)

const (
	wsPingInterval     = 50 * time.Second // how often to send a ping to keep alive
	wsReadTimeout      = 90 * time.Second // ~2 missed heartbeats triggers reconnect
	wsMaxReconnectWait = 30 * time.Second // cap on exponential backoff
	wsWriteTimeout     = 10 * time.Second // deadline for outgoing messages
	wsBookBufferSize   = 256              // buffer depth for book update events
	wsPrivateBufferSize = 64             // buffer depth for private order/fill events
)

// WSFeed manages a single WebSocket connection (market or private channel).
// It handles connection lifecycle, subscription tracking, message routing,
// and automatic reconnection with exponential backoff.
type WSFeed struct {
	url         string
	conn        *websocket.Conn
	connMu      sync.Mutex  // protects conn reads/writes
	auth        *Auth       // used to sign the upgrade handshake
	feedType    string      // "market" or "private"

	// Slug subscriptions — tracked for automatic re-subscribe on reconnect.
	subscribedMu sync.RWMutex
	subscribed   map[string]bool // market slugs

	// Typed event channels — consumers read from these via accessor methods.
	bookCh    chan types.USWSBookEvent    // market feed: book snapshots / updates
	orderCh   chan types.USWSPrivateEvent // private feed: order / fill events

	// Legacy-typed channels for backward compatibility with the engine layer.
	// These mirror the new channels but use the old WSBookEvent / WSOrderEvent types,
	// translated from the US API response format.
	legacyBookCh  chan types.WSBookEvent  // translated from USWSBookEvent
	legacyOrderCh chan types.WSOrderEvent // translated from USWSPrivateEvent

	logger *slog.Logger
}

// NewMarketFeed creates a WebSocket feed for the market channel (public book updates).
// The auth parameter may be nil if the market feed doesn't require authentication,
// or omitted entirely for backward compatibility.
func NewMarketFeed(wsURL string, authAndLogger ...interface{}) *WSFeed {
	var auth *Auth
	var logger *slog.Logger

	for _, arg := range authAndLogger {
		switch v := arg.(type) {
		case *Auth:
			auth = v
		case *slog.Logger:
			logger = v
		}
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &WSFeed{
		url:           wsURL,
		auth:          auth,
		feedType:      "market",
		subscribed:    make(map[string]bool),
		bookCh:        make(chan types.USWSBookEvent, wsBookBufferSize),
		orderCh:       make(chan types.USWSPrivateEvent, wsPrivateBufferSize),
		legacyBookCh:  make(chan types.WSBookEvent, wsBookBufferSize),
		legacyOrderCh: make(chan types.WSOrderEvent, wsPrivateBufferSize),
		logger:        logger.With("component", "ws_market"),
	}
}

// NewPrivateFeed creates a WebSocket feed for the private channel (fills / order lifecycle).
func NewPrivateFeed(wsURL string, auth *Auth, logger *slog.Logger) *WSFeed {
	return &WSFeed{
		url:           wsURL,
		auth:          auth,
		feedType:      "private",
		subscribed:    make(map[string]bool),
		bookCh:        make(chan types.USWSBookEvent, wsBookBufferSize),
		orderCh:       make(chan types.USWSPrivateEvent, wsPrivateBufferSize),
		legacyBookCh:  make(chan types.WSBookEvent, wsBookBufferSize),
		legacyOrderCh: make(chan types.WSOrderEvent, wsPrivateBufferSize),
		logger:        logger.With("component", "ws_private"),
	}
}

// NewUserFeed is a compatibility alias for NewPrivateFeed.
// The engine uses exchange.NewUserFeed; this alias makes it compile unchanged.
func NewUserFeed(wsURL string, auth *Auth, logger *slog.Logger) *WSFeed {
	return NewPrivateFeed(wsURL, auth, logger)
}

// USBookEvents returns the US API-typed book update channel.
func (f *WSFeed) USBookEvents() <-chan types.USWSBookEvent { return f.bookCh }

// USOrderEvents returns the US API-typed private order/fill event channel.
func (f *WSFeed) USOrderEvents() <-chan types.USWSPrivateEvent { return f.orderCh }

// BookEvents returns the legacy-typed book snapshot channel for backward
// compatibility with the engine layer. Events are translated from USWSBookEvent.
func (f *WSFeed) BookEvents() <-chan types.WSBookEvent { return f.legacyBookCh }

// OrderEvents returns the legacy-typed order lifecycle channel for backward
// compatibility with the engine layer. Events are translated from USWSPrivateEvent.
func (f *WSFeed) OrderEvents() <-chan types.WSOrderEvent { return f.legacyOrderCh }

// PriceChangeEvents returns a compatibility channel for the legacy WSPriceChangeEvent type.
// The US API does not emit separate incremental price-change events; all book updates
// arrive as full snapshots on BookEvents(). This method returns a nil channel so
// existing engine code that selects on it compiles and runs safely (a nil channel
// blocks forever, which is the desired behavior — just no price-change events).
func (f *WSFeed) PriceChangeEvents() <-chan types.WSPriceChangeEvent { return nil }

// TradeEvents returns a compatibility channel for the legacy WSTradeEvent type.
// The US API private feed delivers fills within USWSPrivateEvent on OrderEvents().
// This method returns a nil channel for engine compatibility.
func (f *WSFeed) TradeEvents() <-chan types.WSTradeEvent { return nil }

// Run connects and maintains the WebSocket connection with auto-reconnect.
// Blocks until ctx is cancelled.
func (f *WSFeed) Run(ctx context.Context) error {
	backoff := time.Second

	for {
		err := f.connectAndRead(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		f.logger.Warn("websocket disconnected, reconnecting",
			"error", err,
			"backoff", backoff,
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		// Exponential backoff: 1s, 2s, 4s, …, 30s max
		backoff *= 2
		if backoff > wsMaxReconnectWait {
			backoff = wsMaxReconnectWait
		}
	}
}

// Subscribe adds market slugs to the subscription set and sends a subscribe
// message if the connection is live.
func (f *WSFeed) Subscribe(ctx context.Context, slugs []string) error {
	f.subscribedMu.Lock()
	for _, s := range slugs {
		f.subscribed[s] = true
	}
	f.subscribedMu.Unlock()

	return f.sendSubscribeMsg(slugs)
}

// Unsubscribe removes market slugs and sends an unsubscribe message.
func (f *WSFeed) Unsubscribe(ctx context.Context, slugs []string) error {
	f.subscribedMu.Lock()
	for _, s := range slugs {
		delete(f.subscribed, s)
	}
	f.subscribedMu.Unlock()

	msg := types.USWSDynamicSubscribe{
		Action: "unsubscribe",
		Params: types.USWSDynamicParams{
			MarketSlugs: slugs,
			SubscriptionTypes: []types.USWSSubscriptionType{
				types.SubscriptionTypeMarketData,
			},
		},
	}
	return f.writeJSON(msg)
}

// Close gracefully closes the connection.
func (f *WSFeed) Close() error {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	if f.conn != nil {
		return f.conn.Close()
	}
	return nil
}

// connectAndRead dials the server, sends the initial subscription, and reads
// messages until the connection drops or ctx is cancelled.
func (f *WSFeed) connectAndRead(ctx context.Context) error {
	// Build auth headers for the upgrade handshake.
	// The path used for signing is the WS path (after the host).
	wsPath := wsPathFrom(f.url)
	authHeaders := f.auth.SignRequest("GET", wsPath)
	upgradeHeaders := http.Header{}
	for k, v := range authHeaders {
		upgradeHeaders.Set(k, v)
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second, // don't hang forever on dial
	}
	conn, _, err := dialer.DialContext(ctx, f.url, upgradeHeaders)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	f.connMu.Lock()
	f.conn = conn
	f.connMu.Unlock()

	defer func() {
		f.connMu.Lock()
		conn.Close()
		f.conn = nil
		f.connMu.Unlock()
	}()

	// Send initial subscription message.
	if err := f.sendInitialSubscription(); err != nil {
		return fmt.Errorf("initial subscribe: %w", err)
	}

	f.logger.Info("websocket connected", "feed", f.feedType)

	// Read loop with deadline so we reconnect if the server goes silent.
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		f.dispatchMessage(msg)
	}
}

// sendInitialSubscription sends the subscription message appropriate for
// the feed type after a (re)connection is established.
func (f *WSFeed) sendInitialSubscription() error {
	f.subscribedMu.RLock()
	slugs := make([]string, 0, len(f.subscribed))
	for s := range f.subscribed {
		slugs = append(slugs, s)
	}
	f.subscribedMu.RUnlock()

	if f.feedType == "market" {
		return f.writeJSON(types.USWSSubscribeRequest{
			Request: types.USWSSubscribeBody{
				Type:        types.SubscriptionTypeMarketData,
				MarketSlugs: slugs,
			},
		})
	}

	// Private feed subscribes globally (no specific slugs required).
	return f.writeJSON(types.USWSSubscribeRequest{
		Request: types.USWSSubscribeBody{
			Type:        types.SubscriptionTypeOrder,
			MarketSlugs: []string{},
		},
	})
}

// sendSubscribeMsg sends a dynamic subscribe message for new slugs (market feed).
func (f *WSFeed) sendSubscribeMsg(slugs []string) error {
	if f.feedType == "private" {
		// Private feed doesn't filter by slug.
		return nil
	}

	msg := types.USWSDynamicSubscribe{
		Action: "subscribe",
		Params: types.USWSDynamicParams{
			MarketSlugs: slugs,
			SubscriptionTypes: []types.USWSSubscriptionType{
				types.SubscriptionTypeMarketData,
			},
		},
	}
	return f.writeJSON(msg)
}

// dispatchMessage routes an incoming raw message to the appropriate typed channels.
// It populates both the US-typed channels and the legacy-translated channels.
func (f *WSFeed) dispatchMessage(data []byte) {
	// First check for a heartbeat — cheapest case.
	var hb types.USWSHeartbeat
	if err := json.Unmarshal(data, &hb); err == nil && hb.Heartbeat != nil {
		f.logger.Debug("heartbeat received")
		return
	}

	if f.feedType == "market" {
		var evt types.USWSBookEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			f.logger.Debug("ignoring non-book market message", "data", truncate(string(data), 120))
			return
		}
		if evt.Payload.MarketSlug == "" {
			// Probably a control/info message; drop silently.
			return
		}
		// Send to US-typed channel.
		select {
		case f.bookCh <- evt:
		default:
			f.logger.Warn("book channel full, dropping event",
				"market", evt.Payload.MarketSlug)
		}
		// Send translated event to legacy channel.
		legacy := usWSBookToLegacy(evt)
		select {
		case f.legacyBookCh <- legacy:
		default:
			f.logger.Warn("legacy book channel full, dropping event",
				"market", evt.Payload.MarketSlug)
		}
		return
	}

	// Private feed
	var evt types.USWSPrivateEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		f.logger.Debug("ignoring non-order private message", "data", truncate(string(data), 120))
		return
	}
	if evt.Order.ID == "" {
		return
	}
	// Send to US-typed channel.
	select {
	case f.orderCh <- evt:
	default:
		f.logger.Warn("order channel full, dropping event", "id", evt.Order.ID)
	}
	// Send translated event to legacy channel.
	legacy := usWSOrderToLegacy(evt)
	select {
	case f.legacyOrderCh <- legacy:
	default:
		f.logger.Warn("legacy order channel full, dropping event", "id", evt.Order.ID)
	}
}

func (f *WSFeed) writeJSON(v interface{}) error {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	if f.conn == nil {
		return fmt.Errorf("websocket not connected")
	}
	f.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return f.conn.WriteJSON(v)
}

// wsPathFrom extracts the path component from a wss:// URL for signing purposes.
// e.g. "wss://api.polymarket.us/v1/ws/markets" → "/v1/ws/markets"
func wsPathFrom(rawURL string) string {
	// Find third slash (after scheme and host)
	// "wss://api.polymarket.us/v1/ws/markets"
	//           ^               ^
	//  skip "wss://"           start here
	const schemeLen = len("wss://")
	if len(rawURL) <= schemeLen {
		return "/"
	}
	rest := rawURL[schemeLen:]
	idx := 0
	for idx < len(rest) && rest[idx] != '/' {
		idx++
	}
	if idx >= len(rest) {
		return "/"
	}
	return rest[idx:]
}

// truncate limits a string to n bytes for logging purposes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// usWSBookToLegacy translates a US API book event to the legacy WSBookEvent format.
func usWSBookToLegacy(evt types.USWSBookEvent) types.WSBookEvent {
	p := evt.Payload
	buys := make([]types.PriceLevel, len(p.Bids))
	for i, b := range p.Bids {
		buys[i] = types.PriceLevel{Price: b.Px.Value, Size: b.Qty}
	}
	sells := make([]types.PriceLevel, len(p.Offers))
	for i, a := range p.Offers {
		sells[i] = types.PriceLevel{Price: a.Px.Value, Size: a.Qty}
	}
	return types.WSBookEvent{
		EventType: "book",
		AssetID:   p.MarketSlug,
		Market:    p.MarketSlug,
		Timestamp: p.TransactTime,
		Buys:      buys,
		Sells:     sells,
	}
}

// usWSOrderToLegacy translates a US API private event to the legacy WSOrderEvent format.
func usWSOrderToLegacy(evt types.USWSPrivateEvent) types.WSOrderEvent {
	ord := evt.Order
	return types.WSOrderEvent{
		EventType:    "order",
		ID:           ord.ID,
		Market:       ord.MarketSlug,
		AssetID:      ord.MarketSlug,
		Side:         ord.Side,
		Price:        ord.Price.Value,
		OriginalSize: ord.Quantity,
		SizeMatched:  ord.CumQuantity,
		Timestamp:    evt.Execution.TransactTime,
		Type:         string(evt.Execution.Type),
	}
}
