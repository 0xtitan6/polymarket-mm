// Package exchange implements the Polymarket US REST and WebSocket clients.
package exchange

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"polymarket-mm/internal/config"
)

// Auth handles Ed25519 signing for the Polymarket US API.
//
// Headers required on every authenticated request:
//
//	X-PM-Access-Key  — API key UUID
//	X-PM-Timestamp   — Unix timestamp in milliseconds (string)
//	X-PM-Signature   — base64(ed25519.Sign(privateKey, message))
//
// Signed string format: timestamp_ms + HTTP_METHOD + path
// Example: "1705420800000GET/v1/portfolio/positions"
type Auth struct {
	privateKey ed25519.PrivateKey // Ed25519 private key (64 bytes: seed + public key)
	apiKeyID   string             // API key UUID sent as X-PM-Access-Key
}

// NewAuth creates an Auth instance from config.
// The private key is decoded from its base64 representation; only the first
// 32 bytes are used as the Ed25519 seed, matching the Python reference implementation.
func NewAuth(cfg config.Config) (*Auth, error) {
	raw, err := base64.StdEncoding.DecodeString(cfg.Auth.PrivateKeyB64)
	if err != nil {
		// Try URL-safe base64 as a fallback
		raw, err = base64.URLEncoding.DecodeString(cfg.Auth.PrivateKeyB64)
		if err != nil {
			return nil, fmt.Errorf("decode private key base64: %w", err)
		}
	}

	if len(raw) < 32 {
		return nil, fmt.Errorf("private key too short: need at least 32 bytes, got %d", len(raw))
	}

	// Only the first 32 bytes are the seed, as documented.
	seed := raw[:32]
	privateKey := ed25519.NewKeyFromSeed(seed)

	if cfg.Auth.APIKeyID == "" {
		return nil, fmt.Errorf("auth.api_key_id must not be empty")
	}

	return &Auth{
		privateKey: privateKey,
		apiKeyID:   cfg.Auth.APIKeyID,
	}, nil
}

// SignRequest produces the three authentication headers required by the
// Polymarket US API for the given HTTP method and path.
//
// The signed message is: timestamp_ms + method + path
// (e.g. "1705420800000POST/v1/orders")
func (a *Auth) SignRequest(method, path string) map[string]string {
	timestampMs := strconv.FormatInt(time.Now().UnixMilli(), 10)
	message := timestampMs + method + path
	sig := ed25519.Sign(a.privateKey, []byte(message))
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	return map[string]string{
		"X-PM-Access-Key": a.apiKeyID,
		"X-PM-Timestamp":  timestampMs,
		"X-PM-Signature":  sigB64,
	}
}

// APIKeyID returns the configured API key UUID.
func (a *Auth) APIKeyID() string {
	return a.apiKeyID
}

// HasL2Credentials returns true — the Ed25519 Auth is self-contained and
// always has credentials once constructed. This method exists for
// compatibility with the engine layer.
func (a *Auth) HasL2Credentials() bool {
	return true
}

// SetCredentials is a no-op compatibility stub. The Ed25519 Auth does not use
// derived L2 credentials; the private key is the single source of truth.
func (a *Auth) SetCredentials(_ Credentials) {}

// Credentials is a compatibility alias preserved for the engine layer.
// It is not used by the Ed25519 auth implementation.
type Credentials struct {
	ApiKey     string
	Secret     string
	Passphrase string
}
