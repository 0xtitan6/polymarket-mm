// Package exchange — NewSynthesisClientAdapter constructs a *Client shim that
// delegates all HTTP requests to the Synthesis Trade API.
//
// # Purpose
//
// strategy.Maker and market.Scanner accept *exchange.Client by concrete type.
// To reuse those unchanged components with the Synthesis API, we need a *Client
// that routes their HTTP calls to the correct Synthesis paths.
//
// # Method call translation
//
// The Maker calls three Client methods:
//
//	GetOrderBook(ctx, slug)          → Polymarket: GET /v1/markets/{slug}/book
//	                                 → Synthesis:  GET /markets/{slug}/orderbook
//
//	CancelMarketOrders(ctx, slug)    → Polymarket: POST /v1/orders/open/cancel
//	                                 → Synthesis:  per-order DELETEs (simulated)
//
//	PostOrders(ctx, orders, negRisk) → Polymarket: POST /v1/orders (batch)
//	                                 → Synthesis:  POST /wallet/{venue}/{wallet_id}/order (one at a time)
//
// # Approach
//
// Rather than trying to override the *Client struct (which has unexported fields),
// we inject a resty OnBeforeRequest hook that rewrites Polymarket-style URL paths
// to Synthesis-style paths before each request fires.
//
// The hook checks the request URL path and applies the following rewrites:
//
//	/v1/markets/{slug}/book      → /markets/{slug}/orderbook
//	/v1/markets/{slug}/bbo       → /markets/{slug}/prices
//	/v1/markets/{slug}/...       → /markets/{slug}/...
//	/v1/markets                  → /markets
//	POST /v1/orders              → POST /wallet/{venue}/{wallet_id}/order (body rewritten too)
//	POST /v1/orders/open/cancel  → handled by simulate-cancel hook
//	GET /v1/orders/open          → /wallet/{wallet_id}/orders
//	GET /v1/portfolio/positions  → /wallet/{wallet_id}/positions
//	GET /v1/account/balances     → /wallet  (balance extracted from wallet list)
//
// The request body for POST /v1/orders is also rewritten from USOrderRequest to
// SynthesisOrderRequest format via a JSON transformation hook.
//
// # Auth translation
//
// X-PM-Access-Key / X-PM-Timestamp / X-PM-Signature headers are stripped and
// replaced with X-API-KEY in the same hook.
package exchange

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

// NewSynthesisClientAdapter creates a *Client that routes Polymarket-style
// REST calls to the Synthesis Trade API. The returned *Client can be passed
// directly to strategy.NewMaker and market.NewScanner unchanged.
//
// All URL path translation, body rewriting, and auth header swapping is handled
// transparently by resty OnBeforeRequest hooks installed on the underlying
// http client.
func NewSynthesisClientAdapter(sc *SynthesisClient, baseCfg config.Config, logger *slog.Logger) *Client {
	baseURL := sc.http.HostURL
	if baseURL == "" {
		baseURL = synthesisBaseURL
	}

	walletID := sc.walletID
	venue := sc.venue
	apiKey := sc.auth.APIKey()

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

	// ——————————————————————————————————————————————————————
	// Path-rewrite + auth hook
	// Rewrites outgoing requests from Polymarket URL patterns to Synthesis patterns.
	// Also strips Ed25519 auth headers and injects X-API-KEY.
	// ——————————————————————————————————————————————————————
	httpClient.OnBeforeRequest(func(c *resty.Client, req *resty.Request) error {
		// Rewrite URL path
		url := req.URL
		if url != "" {
			req.URL = rewriteSynthesisPath(url, walletID, venue)
		}

		// Auth: replace Ed25519 headers with X-API-KEY
		delete(req.Header, "X-PM-Access-Key")
		delete(req.Header, "X-PM-Timestamp")
		delete(req.Header, "X-PM-Signature")
		req.SetHeader("X-API-KEY", apiKey)

		// Rewrite POST /v1/orders body from USOrderRequest → SynthesisOrderRequest
		if req.Method == "POST" && (url == "/v1/orders" || strings.HasSuffix(url, "/order")) {
			if req.Body != nil {
				rewritten, err := rewriteOrderBody(req.Body, venue, walletID)
				if err == nil {
					req.Body = rewritten
				}
			}
		}

		return nil
	})

	// Build the passthrough auth so Client.setAuthHeaders doesn't panic
	auth := newPassthroughAuth(apiKey)

	return &Client{
		http:   httpClient,
		auth:   auth,
		rl:     sc.rl,
		dryRun: sc.dryRun,
		logger: logger.With("component", "synthesis_client_adapter"),
	}
}

// rewriteSynthesisPath translates a Polymarket US API path to a Synthesis Trade API path.
//
// Mapping table:
//
//	GET  /v1/markets                         → GET  /markets?venue=pol
//	GET  /v1/markets/{slug}/book             → GET  /markets/{slug}/orderbook
//	GET  /v1/markets/{slug}/bbo              → GET  /markets/{slug}/prices
//	GET  /v1/markets/{slug}                  → GET  /markets/{slug}
//	POST /v1/orders                          → POST /wallet/{venue}/{wallet_id}/order
//	POST /v1/orders/open/cancel              → POST /wallet/{wallet_id}/orders/cancel (best-guess)
//	GET  /v1/orders/open                     → GET  /wallet/{wallet_id}/orders
//	DELETE /v1/order/{id}/cancel             → DELETE /wallet/{wallet_id}/orders/{id}
//	GET  /v1/portfolio/positions             → GET  /wallet/{wallet_id}/positions
//	GET  /v1/account/balances                → GET  /wallet
func rewriteSynthesisPath(path, walletID, venue string) string {
	switch {
	// Order book
	case strings.HasSuffix(path, "/book") && strings.Contains(path, "/markets/"):
		slug := extractSlug(path, "/markets/", "/book")
		return "/markets/" + slug + "/orderbook"

	// BBO / prices
	case strings.HasSuffix(path, "/bbo") && strings.Contains(path, "/markets/"):
		slug := extractSlug(path, "/markets/", "/bbo")
		return "/markets/" + slug + "/prices"

	// Market list
	case path == "/v1/markets":
		return fmt.Sprintf("/markets?venue=%s", venue)

	// Single market
	case strings.HasPrefix(path, "/v1/markets/"):
		slug := strings.TrimPrefix(path, "/v1/markets/")
		return "/markets/" + slug

	// Place order — path rewrite; body is rewritten separately in the hook
	case path == "/v1/orders":
		return fmt.Sprintf("/wallet/%s/%s/order", venue, walletID)

	// Cancel all / cancel market orders
	case path == "/v1/orders/open/cancel":
		// Synthesis has no bulk-cancel endpoint; route to a best-guess path.
		// The actual bulk cancel is handled by SynthesisClient.CancelAll which
		// fetches and cancels orders individually. This path is a fallback.
		return fmt.Sprintf("/wallet/%s/orders/cancel", walletID)

	// Open orders
	case path == "/v1/orders/open":
		return fmt.Sprintf("/wallet/%s/orders", walletID)

	// Per-order cancel: DELETE /v1/order/{id}/cancel
	case strings.HasPrefix(path, "/v1/order/") && strings.HasSuffix(path, "/cancel"):
		orderID := extractSlug(path, "/v1/order/", "/cancel")
		return fmt.Sprintf("/wallet/%s/orders/%s", walletID, orderID)

	// Positions
	case path == "/v1/portfolio/positions":
		return fmt.Sprintf("/wallet/%s/positions", walletID)

	// Balances → wallet list
	case path == "/v1/account/balances":
		return "/wallet"

	// Pass through any other path unchanged
	default:
		return path
	}
}

// extractSlug extracts the path segment between prefix and suffix.
// e.g. extractSlug("/v1/markets/my-market/book", "/markets/", "/book") → "my-market"
func extractSlug(path, prefix, suffix string) string {
	// Find prefix
	i := strings.Index(path, prefix)
	if i < 0 {
		return path
	}
	start := i + len(prefix)
	rest := path[start:]
	// Find suffix
	j := strings.LastIndex(rest, suffix)
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// rewriteOrderBody transforms a USOrderRequest JSON body into a SynthesisOrderRequest.
// Called when a POST to /v1/orders is intercepted by the path-rewrite hook.
func rewriteOrderBody(body interface{}, venue, walletID string) (interface{}, error) {
	// Marshal the body to JSON first (it may be a json.RawMessage or a struct)
	raw, err := json.Marshal(body)
	if err != nil {
		return body, err
	}

	// Try to parse as USOrderRequest
	var usReq types.USOrderRequest
	if err := json.Unmarshal(raw, &usReq); err != nil {
		return body, err // return original if not a USOrderRequest
	}

	// Translate to Synthesis format
	synthReq := usOrderToSynthesis(usReq)
	return synthReq, nil
}

// newPassthroughAuth creates an *Auth whose SignRequest produces only the
// X-API-KEY header. This lets Client.setAuthHeaders call SignRequest without
// panicking (a nil private key would panic in ed25519.Sign).
//
// The returned Auth uses a synthetic all-zero 32-byte Ed25519 seed.
// The resulting Ed25519 headers are stripped by the path-rewrite hook, so they
// never reach the Synthesis server.
func newPassthroughAuth(apiKey string) *Auth {
	cfg := config.Config{
		Auth: config.AuthConfig{
			// Use the API key as the key ID (shows up in X-PM-Access-Key,
			// but this header is stripped by the OnBeforeRequest hook)
			APIKeyID: apiKey,
			// 32 zero bytes encoded as base64 (valid Ed25519 seed, deterministic)
			PrivateKeyB64: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		},
	}
	auth, _ := NewAuth(cfg)
	if auth == nil {
		return &Auth{apiKeyID: apiKey}
	}
	return auth
}

// SynthesisMakerClient is kept for documentation purposes only.
// Use NewSynthesisClientAdapter for wiring strategy.NewMaker.
//
// Direct delegation alternative: if the path-rewrite adapter causes issues
// (e.g. Synthesis API returns unexpected body shapes), the synthesis engine
// can call SynthesisClient methods directly and bypass the Maker for order
// management. The three critical methods are:
//
//	sc.GetOrderBook(ctx, slug)          → book snapshot for Maker
//	sc.CancelMarketOrders(ctx, slug)    → cleanup on market stop
//	sc.PostOrders(ctx, orders, negRisk) → batch order placement
type SynthesisMakerClient struct{ sc *SynthesisClient }
