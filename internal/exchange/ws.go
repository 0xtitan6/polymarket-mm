// ws.go implements WebSocket feeds for real-time Polymarket data.
//
// Two independent feeds run concurrently:
//
//   - Market feed (public): subscribes by asset ID (token ID), receives
//     "book" snapshots and "price_change" deltas for the order book.
//
//   - User feed (authenticated): subscribes by condition ID, receives
//     "trade" fills and "order" lifecycle events (placement, cancellation).
//
// Both feeds auto-reconnect with exponential backoff (1s → 30s max) and
// re-subscribe to all tracked IDs on reconnection. A read deadline (90s)
// ensures silent server failures are detected within ~2 missed pings.
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"polymarket-mm/pkg/types"
)

const (
	pingInterval     = 50 * time.Second  // how often we send PING to keep alive
	readTimeout      = 90 * time.Second  // ~2 missed pings triggers reconnect
	maxReconnectWait = 30 * time.Second  // cap on exponential backoff
	writeTimeout     = 10 * time.Second  // deadline for outgoing messages
	readBufferSize   = 256               // buffer for book/price events
	tradeBufferSize  = 64                // buffer for trade/order events
)

// WSFeed manages a single WebSocket connection (market or user channel).
// It handles connection lifecycle, subscription tracking, message routing,
// and automatic reconnection with exponential backoff.
type WSFeed struct {
	url         string
	conn        *websocket.Conn
	connMu      sync.Mutex   // protects conn reads/writes
	auth        *Auth        // nil for market channel, set for user channel
	channelType string       // "market" or "user"

	// Track subscriptions for automatic re-subscribe on reconnect
	subscribedMu sync.RWMutex
	subscribed   map[string]bool // asset IDs (market) or condition IDs (user)

	// Typed event channels — consumers read from these via accessor methods
	bookCh        chan types.WSBookEvent        // full book snapshots
	priceChangeCh chan types.WSPriceChangeEvent // incremental book updates
	tradeCh       chan types.WSTradeEvent       // fill notifications
	orderCh       chan types.WSOrderEvent       // order lifecycle events

	logger *slog.Logger
}

// NewMarketFeed creates a WebSocket feed for the market channel (public).
func NewMarketFeed(wsURL string, logger *slog.Logger) *WSFeed {
	return &WSFeed{
		url:           wsURL,
		channelType:   "market",
		subscribed:    make(map[string]bool),
		bookCh:        make(chan types.WSBookEvent, readBufferSize),
		priceChangeCh: make(chan types.WSPriceChangeEvent, readBufferSize),
		tradeCh:       make(chan types.WSTradeEvent, tradeBufferSize),
		orderCh:       make(chan types.WSOrderEvent, tradeBufferSize),
		logger:        logger.With("component", "ws_market"),
	}
}

// NewUserFeed creates a WebSocket feed for the user channel (authenticated).
func NewUserFeed(wsURL string, auth *Auth, logger *slog.Logger) *WSFeed {
	return &WSFeed{
		url:           wsURL,
		auth:          auth,
		channelType:   "user",
		subscribed:    make(map[string]bool),
		bookCh:        make(chan types.WSBookEvent, readBufferSize),
		priceChangeCh: make(chan types.WSPriceChangeEvent, readBufferSize),
		tradeCh:       make(chan types.WSTradeEvent, tradeBufferSize),
		orderCh:       make(chan types.WSOrderEvent, tradeBufferSize),
		logger:        logger.With("component", "ws_user"),
	}
}

// BookEvents returns a read-only channel of book snapshot events.
func (f *WSFeed) BookEvents() <-chan types.WSBookEvent { return f.bookCh }

// PriceChangeEvents returns a read-only channel of price change events.
func (f *WSFeed) PriceChangeEvents() <-chan types.WSPriceChangeEvent { return f.priceChangeCh }

// TradeEvents returns a read-only channel of trade events (user channel).
func (f *WSFeed) TradeEvents() <-chan types.WSTradeEvent { return f.tradeCh }

// OrderEvents returns a read-only channel of order events (user channel).
func (f *WSFeed) OrderEvents() <-chan types.WSOrderEvent { return f.orderCh }

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

		// Exponential backoff: 1s, 2s, 4s, 8s, ..., 30s max
		backoff *= 2
		if backoff > maxReconnectWait {
			backoff = maxReconnectWait
		}
	}
}

// Subscribe adds asset IDs (market channel) or condition IDs (user channel).
func (f *WSFeed) Subscribe(ctx context.Context, ids []string) error {
	f.subscribedMu.Lock()
	for _, id := range ids {
		f.subscribed[id] = true
	}
	f.subscribedMu.Unlock()

	msg := types.WSUpdateMsg{
		Operation: "subscribe",
	}
	if f.channelType == "market" {
		msg.AssetIDs = ids
	} else {
		msg.Markets = ids
	}

	return f.writeJSON(msg)
}

// Unsubscribe removes IDs from the subscription.
func (f *WSFeed) Unsubscribe(ctx context.Context, ids []string) error {
	f.subscribedMu.Lock()
	for _, id := range ids {
		delete(f.subscribed, id)
	}
	f.subscribedMu.Unlock()

	msg := types.WSUpdateMsg{
		Operation: "unsubscribe",
	}
	if f.channelType == "market" {
		msg.AssetIDs = ids
	} else {
		msg.Markets = ids
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

func (f *WSFeed) connectAndRead(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, f.url, nil)
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

	// Send initial subscription
	if err := f.sendInitialSubscription(); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	f.logger.Info("websocket connected", "channel", f.channelType)

	// Start ping goroutine
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go f.pingLoop(pingCtx)

	// Read loop with deadline so we reconnect if server goes silent
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		conn.SetReadDeadline(time.Now().Add(readTimeout))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		f.dispatchMessage(msg)
	}
}

func (f *WSFeed) sendInitialSubscription() error {
	f.subscribedMu.RLock()
	ids := make([]string, 0, len(f.subscribed))
	for id := range f.subscribed {
		ids = append(ids, id)
	}
	f.subscribedMu.RUnlock()

	if f.channelType == "market" {
		msg := types.WSSubscribeMsg{
			Type:     "market",
			AssetIDs: ids,
		}
		return f.writeJSON(msg)
	}

	// User channel requires auth
	msg := types.WSSubscribeMsg{
		Type:    "user",
		Auth:    f.auth.WSAuthPayload(),
		Markets: ids,
	}
	return f.writeJSON(msg)
}

func (f *WSFeed) dispatchMessage(data []byte) {
	// Peek at event_type to route
	var envelope struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		f.logger.Debug("ignoring non-json ws message", "data", string(data))
		return
	}

	switch envelope.EventType {
	case "book":
		var evt types.WSBookEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			f.logger.Error("unmarshal book event", "error", err)
			return
		}
		select {
		case f.bookCh <- evt:
		default:
			f.logger.Warn("book channel full, dropping event", "asset", evt.AssetID)
		}

	case "price_change":
		var evt types.WSPriceChangeEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			f.logger.Error("unmarshal price_change event", "error", err)
			return
		}
		select {
		case f.priceChangeCh <- evt:
		default:
			f.logger.Warn("price_change channel full, dropping event")
		}

	case "trade":
		var evt types.WSTradeEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			f.logger.Error("unmarshal trade event", "error", err)
			return
		}
		select {
		case f.tradeCh <- evt:
		default:
			f.logger.Warn("trade channel full, dropping event", "id", evt.ID)
		}

	case "order":
		var evt types.WSOrderEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			f.logger.Error("unmarshal order event", "error", err)
			return
		}
		select {
		case f.orderCh <- evt:
		default:
			f.logger.Warn("order channel full, dropping event", "id", evt.ID)
		}

	case "last_trade_price", "tick_size_change", "best_bid_ask", "new_market", "market_resolved":
		// Informational events we don't need to process
		f.logger.Debug("ignoring event", "type", envelope.EventType)

	default:
		f.logger.Debug("unknown ws event type", "type", envelope.EventType)
	}
}

func (f *WSFeed) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := f.writeMessage(websocket.TextMessage, []byte("PING")); err != nil {
				f.logger.Warn("ping failed", "error", err)
				return
			}
		}
	}
}

func (f *WSFeed) writeJSON(v interface{}) error {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	if f.conn == nil {
		return fmt.Errorf("websocket not connected")
	}
	f.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return f.conn.WriteJSON(v)
}

func (f *WSFeed) writeMessage(msgType int, data []byte) error {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	if f.conn == nil {
		return fmt.Errorf("websocket not connected")
	}
	f.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return f.conn.WriteMessage(msgType, data)
}
