package exchange

import (
	"crypto/ed25519"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"polymarket-mm/internal/config"
)

// generateTestConfig returns a config with a freshly generated Ed25519 key.
func generateTestConfig(t *testing.T) config.Config {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	// Encode the full 64-byte key in base64 (seed is first 32 bytes).
	privB64 := base64.StdEncoding.EncodeToString(priv)
	return config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "test-api-key-uuid",
			PrivateKeyB64: privB64,
		},
	}
}

// TestNewAuthSuccess verifies that NewAuth succeeds with a valid key.
func TestNewAuthSuccess(t *testing.T) {
	t.Parallel()
	cfg := generateTestConfig(t)

	a, err := NewAuth(cfg)
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	if a == nil {
		t.Fatal("NewAuth returned nil")
	}
	if a.APIKeyID() != "test-api-key-uuid" {
		t.Errorf("APIKeyID = %q, want %q", a.APIKeyID(), "test-api-key-uuid")
	}
}

// TestNewAuthSeedOnly verifies that NewAuth accepts a 32-byte seed (no public key appended).
func TestNewAuthSeedOnly(t *testing.T) {
	t.Parallel()
	// Generate a key and extract only the 32-byte seed.
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	seed := priv.Seed() // 32 bytes
	seedB64 := base64.StdEncoding.EncodeToString(seed)

	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "test-key-id",
			PrivateKeyB64: seedB64,
		},
	}

	a, err := NewAuth(cfg)
	if err != nil {
		t.Fatalf("NewAuth with seed: %v", err)
	}
	if a == nil {
		t.Fatal("NewAuth returned nil")
	}
}

// TestNewAuthTooShort verifies that NewAuth rejects keys shorter than 32 bytes.
func TestNewAuthTooShort(t *testing.T) {
	t.Parallel()
	short := base64.StdEncoding.EncodeToString([]byte("tooshort"))
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "test-key-id",
			PrivateKeyB64: short,
		},
	}

	_, err := NewAuth(cfg)
	if err == nil {
		t.Fatal("expected error for short key, got nil")
	}
}

// TestNewAuthEmptyKeyID verifies that NewAuth rejects an empty APIKeyID.
func TestNewAuthEmptyKeyID(t *testing.T) {
	t.Parallel()
	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "",
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
	}

	_, err := NewAuth(cfg)
	if err == nil {
		t.Fatal("expected error for empty APIKeyID, got nil")
	}
}

// TestNewAuthBadBase64 verifies that NewAuth returns an error for invalid base64.
func TestNewAuthBadBase64(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "test-key-id",
			PrivateKeyB64: "not!!valid==base64$$",
		},
	}

	_, err := NewAuth(cfg)
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
}

// TestSignRequestHeaderKeys verifies that SignRequest returns exactly the three
// required header keys.
func TestSignRequestHeaderKeys(t *testing.T) {
	t.Parallel()
	cfg := generateTestConfig(t)
	a, err := NewAuth(cfg)
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}

	headers := a.SignRequest("GET", "/v1/portfolio/positions")

	required := []string{"X-PM-Access-Key", "X-PM-Timestamp", "X-PM-Signature"}
	for _, k := range required {
		if v, ok := headers[k]; !ok || v == "" {
			t.Errorf("missing or empty header %q", k)
		}
	}
	if len(headers) != 3 {
		t.Errorf("expected exactly 3 headers, got %d: %v", len(headers), headers)
	}
}

// TestSignRequestAccessKeyMatchesConfig verifies that X-PM-Access-Key equals the
// configured API key ID.
func TestSignRequestAccessKeyMatchesConfig(t *testing.T) {
	t.Parallel()
	cfg := generateTestConfig(t)
	a, err := NewAuth(cfg)
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}

	headers := a.SignRequest("POST", "/v1/orders")
	if headers["X-PM-Access-Key"] != "test-api-key-uuid" {
		t.Errorf("X-PM-Access-Key = %q, want %q", headers["X-PM-Access-Key"], "test-api-key-uuid")
	}
}

// TestSignRequestTimestampIsMilliseconds verifies the timestamp is in ms (13 digits).
func TestSignRequestTimestampIsMilliseconds(t *testing.T) {
	t.Parallel()
	cfg := generateTestConfig(t)
	a, _ := NewAuth(cfg)

	headers := a.SignRequest("GET", "/v1/markets")
	ts := headers["X-PM-Timestamp"]

	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q is not an integer: %v", ts, err)
	}
	// A Unix millisecond timestamp for 2020-2050 is 13 digits.
	if len(ts) != 13 {
		t.Errorf("timestamp %q has %d digits, want 13 (milliseconds)", ts, len(ts))
	}
	// Sanity check: should be close to now.
	nowMs := time.Now().UnixMilli()
	delta := nowMs - tsInt
	if delta < 0 {
		delta = -delta
	}
	if delta > 5000 {
		t.Errorf("timestamp %d differs from now by %dms (>5s)", tsInt, delta)
	}
}

// TestSignRequestSignatureIsValidBase64 verifies the signature decodes as valid base64.
func TestSignRequestSignatureIsValidBase64(t *testing.T) {
	t.Parallel()
	cfg := generateTestConfig(t)
	a, _ := NewAuth(cfg)

	headers := a.SignRequest("DELETE", "/v1/order/abc123/cancel")
	sig := headers["X-PM-Signature"]

	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatalf("signature %q is not valid base64: %v", sig, err)
	}
	// Ed25519 signatures are always 64 bytes.
	if len(decoded) != ed25519.SignatureSize {
		t.Errorf("decoded signature length = %d, want %d", len(decoded), ed25519.SignatureSize)
	}
}

// TestSignRequestSignatureVerifies verifies the signature is cryptographically valid
// using the public key derived from the same seed.
func TestSignRequestSignatureVerifies(t *testing.T) {
	t.Parallel()

	// Generate a deterministic key pair.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "verify-test-key",
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
	}
	a, err := NewAuth(cfg)
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}

	method := "POST"
	path := "/v1/orders"
	headers := a.SignRequest(method, path)

	ts := headers["X-PM-Timestamp"]
	sigBytes, err := base64.StdEncoding.DecodeString(headers["X-PM-Signature"])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	message := []byte(ts + method + path)
	if !ed25519.Verify(pub, message, sigBytes) {
		t.Error("signature verification failed: Sign and Verify disagree")
	}
}

// TestSignRequestDifferentCallsDifferentTimestamps verifies that two calls produce
// different timestamps (and hence different signatures) when time advances.
// This is a best-effort check; it can only fail if both calls land in the same
// millisecond — extremely unlikely and tolerable.
func TestSignRequestDifferentCallsDifferentTimestamps(t *testing.T) {
	t.Parallel()
	cfg := generateTestConfig(t)
	a, _ := NewAuth(cfg)

	h1 := a.SignRequest("GET", "/v1/markets")
	time.Sleep(2 * time.Millisecond)
	h2 := a.SignRequest("GET", "/v1/markets")

	if h1["X-PM-Timestamp"] == h2["X-PM-Timestamp"] {
		// Not a hard failure — millisecond resolution means this can be a tie.
		t.Log("timestamps were equal (same millisecond); skipping signature diff check")
		return
	}
	if h1["X-PM-Signature"] == h2["X-PM-Signature"] {
		t.Error("different timestamps produced identical signatures (unexpected)")
	}
}

// TestSignRequestMessageFormat verifies the signed message is timestamp+method+path
// with no separators, matching the API specification.
func TestSignRequestMessageFormat(t *testing.T) {
	t.Parallel()

	pub, priv, _ := ed25519.GenerateKey(nil)
	cfg := config.Config{
		Auth: config.AuthConfig{
			APIKeyID:      "format-test",
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
	}
	a, _ := NewAuth(cfg)

	method := "GET"
	path := "/v1/portfolio/positions"
	headers := a.SignRequest(method, path)

	ts := headers["X-PM-Timestamp"]
	sigBytes, _ := base64.StdEncoding.DecodeString(headers["X-PM-Signature"])

	// The documented format is: timestamp_ms + HTTP_METHOD + path (no separators)
	// Example: "1705420800000GET/v1/portfolio/positions"
	message := ts + method + path
	if !strings.Contains(message, ts) || !strings.Contains(message, method) || !strings.Contains(message, path) {
		t.Errorf("message %q does not contain expected components", message)
	}

	if !ed25519.Verify(pub, []byte(message), sigBytes) {
		t.Error("signature does not verify against timestamp+method+path message")
	}
}
