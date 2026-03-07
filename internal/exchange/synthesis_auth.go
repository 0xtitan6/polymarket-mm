// Package exchange — SynthesisAuth implements API-key authentication for the
// Synthesis Trade API (https://synthesis.trade/api/v1).
//
// The Synthesis API uses a single static header for authentication:
//
//	X-API-KEY: <your-api-key>
//
// This is intentionally simpler than the Polymarket Ed25519 scheme (which
// requires a timestamped signature per request). No signing is needed —
// the key is copied verbatim onto every request.
//
// Usage:
//
//	auth := exchange.NewSynthesisAuth(synthCfg)
//	req.SetHeader("X-API-KEY", auth.APIKey())
//
// The SynthesisAuth type also implements the same helper interface methods
// (HasL2Credentials, SetCredentials) used by the engine layer so that the
// engine can call them without knowing the concrete auth type.
package exchange

import (
	"polymarket-mm/internal/config"
)

// SynthesisAuth holds the Synthesis Trade API key and provides helper
// methods compatible with the existing engine/strategy interface.
//
// Thread-safety: SynthesisAuth is immutable after construction and is
// safe for concurrent use without locks.
type SynthesisAuth struct {
	apiKey string // raw API key sent as X-API-KEY header
}

// NewSynthesisAuth creates a SynthesisAuth from a loaded SynthesisConfig.
// It reads cfg.Auth.APIKey — the Synthesis-specific auth field.
// Returns an error if the API key is empty (would produce unauthenticated requests).
func NewSynthesisAuth(cfg config.SynthesisConfig) (*SynthesisAuth, error) {
	if cfg.Auth.APIKey == "" {
		return nil, ErrSynthesisAuthMissingKey
	}
	return &SynthesisAuth{apiKey: cfg.Auth.APIKey}, nil
}

// ErrSynthesisAuthMissingKey is returned when the API key is not configured.
var ErrSynthesisAuthMissingKey = errSynthesisAuthMissingKey{}

type errSynthesisAuthMissingKey struct{}

func (e errSynthesisAuthMissingKey) Error() string {
	return "synthesis auth: auth.api_key must not be empty (set SYNTH_API_KEY)"
}

// APIKey returns the configured API key string.
// Used by SynthesisClient to attach the X-API-KEY header.
func (a *SynthesisAuth) APIKey() string {
	return a.apiKey
}

// Headers returns the single authentication header map required by the
// Synthesis Trade API. The returned map can be passed directly to
// resty's SetHeaders method.
//
// Unlike the Polymarket Ed25519 auth, no per-request signing is needed —
// the same header is identical on every request.
func (a *SynthesisAuth) Headers() map[string]string {
	return map[string]string{
		"X-API-KEY": a.apiKey,
	}
}

// HasL2Credentials returns true — the API-key auth is always ready to use
// once constructed. This matches the interface expected by the engine layer
// (which checks HasL2Credentials before placing orders).
func (a *SynthesisAuth) HasL2Credentials() bool {
	return true
}

// SetCredentials is a no-op compatibility stub. The Synthesis API key is
// static and does not use derived L2 credentials. Implemented to satisfy
// the same engine interface as the Polymarket Ed25519 Auth.
func (a *SynthesisAuth) SetCredentials(_ Credentials) {}
