package exchange

import (
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

// Credentials holds the L2 API key triplet returned by /auth/derive-api-key.
// These are used for HMAC-signed trading requests (L2 auth).
type Credentials struct {
	ApiKey     string `json:"apiKey"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase"`
}

// Auth handles two layers of Polymarket authentication:
//
//   - L1 (EIP-712): Used only once to derive L2 API keys. Signs a typed-data
//     "ClobAuth" message with the wallet's private key, proving ownership.
//
//   - L2 (HMAC-SHA256): Used for all trading operations. Signs
//     "timestamp + method + path [+ body]" with the derived API secret.
//
// The funderAddress may differ from address when using a proxy/multisig wallet.
type Auth struct {
	privateKey    *ecdsa.PrivateKey   // EOA private key for L1 signing
	address       common.Address      // EOA address derived from privateKey
	funderAddress common.Address      // proxy/funder wallet (== address if no proxy)
	chainID       *big.Int            // Polygon chain ID (137 mainnet, 80002 amoy)
	sigType       types.SignatureType // 0 = EOA
	creds         Credentials         // L2 API credentials (derived or configured)
}

// NewAuth creates an Auth instance from config.
func NewAuth(cfg config.Config) (*Auth, error) {
	// Strip 0x prefix if present
	keyHex := cfg.Wallet.PrivateKey
	if len(keyHex) >= 2 && keyHex[:2] == "0x" {
		keyHex = keyHex[2:]
	}

	privateKey, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	address := crypto.PubkeyToAddress(privateKey.PublicKey)

	var funder common.Address
	if cfg.Wallet.FunderAddress != "" {
		funder = common.HexToAddress(cfg.Wallet.FunderAddress)
	} else {
		funder = address
	}

	return &Auth{
		privateKey:    privateKey,
		address:       address,
		funderAddress: funder,
		chainID:       big.NewInt(int64(cfg.Wallet.ChainID)),
		sigType:       types.SignatureType(cfg.Wallet.SignatureType),
		creds: Credentials{
			ApiKey:     cfg.API.ApiKey,
			Secret:     cfg.API.Secret,
			Passphrase: cfg.API.Passphrase,
		},
	}, nil
}

// Address returns the signer's Ethereum address.
func (a *Auth) Address() common.Address {
	return a.address
}

// ChainID returns the configured chain ID.
func (a *Auth) ChainID() *big.Int {
	return a.chainID
}

// FunderAddress returns the funder/proxy wallet address.
func (a *Auth) FunderAddress() common.Address {
	return a.funderAddress
}

// HasL2Credentials returns whether L2 API credentials are configured.
func (a *Auth) HasL2Credentials() bool {
	return a.creds.ApiKey != "" && a.creds.Secret != "" && a.creds.Passphrase != ""
}

// SetCredentials sets the L2 API credentials (after deriving them via L1).
func (a *Auth) SetCredentials(creds Credentials) {
	a.creds = creds
}

// L1Headers generates headers for L1-authenticated endpoints (key management).
func (a *Auth) L1Headers(nonce int) (map[string]string, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	sig, err := a.signClobAuth(timestamp, nonce)
	if err != nil {
		return nil, fmt.Errorf("sign clob auth: %w", err)
	}

	return map[string]string{
		"POLY_ADDRESS":   a.address.Hex(),
		"POLY_SIGNATURE": sig,
		"POLY_TIMESTAMP": timestamp,
		"POLY_NONCE":     strconv.Itoa(nonce),
	}, nil
}

// L2Headers generates headers for L2-authenticated trading endpoints.
func (a *Auth) L2Headers(method, path, body string) (map[string]string, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	sig, err := a.buildHMAC(timestamp, method, path, body)
	if err != nil {
		return nil, fmt.Errorf("build hmac: %w", err)
	}

	return map[string]string{
		"POLY_ADDRESS":    a.address.Hex(),
		"POLY_SIGNATURE":  sig,
		"POLY_TIMESTAMP":  timestamp,
		"POLY_API_KEY":    a.creds.ApiKey,
		"POLY_PASSPHRASE": a.creds.Passphrase,
	}, nil
}

// WSAuthPayload returns credentials for the user WebSocket channel.
func (a *Auth) WSAuthPayload() *types.WSAuth {
	return &types.WSAuth{
		ApiKey:     a.creds.ApiKey,
		Secret:     a.creds.Secret,
		Passphrase: a.creds.Passphrase,
	}
}

// signClobAuth produces an EIP-712 signature for L1 authentication.
func (a *Auth) signClobAuth(timestamp string, nonce int) (string, error) {
	sig, err := a.SignTypedData(
		&apitypes.TypedDataDomain{
			Name:    "ClobAuthDomain",
			Version: "1",
			ChainId: (*ethmath.HexOrDecimal256)(new(big.Int).Set(a.chainID)),
		},
		apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
			},
			"ClobAuth": {
				{Name: "address", Type: "address"},
				{Name: "timestamp", Type: "string"},
				{Name: "nonce", Type: "uint256"},
				{Name: "message", Type: "string"},
			},
		},
		apitypes.TypedDataMessage{
			"address":   a.address.Hex(),
			"timestamp": timestamp,
			"nonce":     fmt.Sprintf("%d", nonce),
			"message":   "This message attests that I control the given wallet",
		},
		"ClobAuth",
	)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	return "0x" + common.Bytes2Hex(sig), nil
}

// SignTypedData signs EIP-712 typed data and adjusts V to 27/28.
func (a *Auth) SignTypedData(
	domain *apitypes.TypedDataDomain,
	typesDef apitypes.Types,
	message apitypes.TypedDataMessage,
	primaryType string,
) ([]byte, error) {
	typedData := apitypes.TypedData{
		Types:       typesDef,
		PrimaryType: primaryType,
		Domain:      *domain,
		Message:     message,
	}

	hash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return nil, fmt.Errorf("typed data hash: %w", err)
	}

	sig, err := crypto.Sign(hash, a.privateKey)
	if err != nil {
		return nil, fmt.Errorf("sign typed data: %w", err)
	}

	if sig[64] < 27 {
		sig[64] += 27
	}
	return sig, nil
}

// buildHMAC computes the HMAC-SHA256 signature for L2 auth.
// message = timestamp + method + requestPath [+ body]
func (a *Auth) buildHMAC(timestamp, method, path, body string) (string, error) {
	decoders := []*base64.Encoding{
		base64.URLEncoding,
		base64.RawURLEncoding,
		base64.StdEncoding,
		base64.RawStdEncoding,
	}

	var secretBytes []byte
	var err error
	for _, dec := range decoders {
		secretBytes, err = dec.DecodeString(a.creds.Secret)
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("decode secret: %w", err)
	}

	message := timestamp + method + path
	if body != "" {
		message += body
	}

	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(message))
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	return sig, nil
}

// PriceToAmounts converts a human-readable price and size to
// makerAmount and takerAmount as big.Int values scaled to 6 decimals (USDC).
//
// For BUY: you pay makerAmount USDC, you receive takerAmount tokens
// For SELL: you give makerAmount tokens, you receive takerAmount USDC
func PriceToAmounts(price, size float64, side types.Side, tickSize types.TickSize) (makerAmt, takerAmt *big.Int) {
	amtDecimals := tickSize.AmountDecimals()
	scale := new(big.Float).SetFloat64(1e6) // USDC 6 decimals

	sizeRounded := roundDown(size, 2)

	switch side {
	case types.BUY:
		// makerAmount = USDC cost = size * price
		cost := roundDown(sizeRounded*price, amtDecimals)
		makerF := new(big.Float).Mul(new(big.Float).SetFloat64(cost), scale)
		makerAmt, _ = makerF.Int(nil)
		// takerAmount = tokens received = size
		takerF := new(big.Float).Mul(new(big.Float).SetFloat64(sizeRounded), scale)
		takerAmt, _ = takerF.Int(nil)
	case types.SELL:
		// makerAmount = tokens given = size
		makerF := new(big.Float).Mul(new(big.Float).SetFloat64(sizeRounded), scale)
		makerAmt, _ = makerF.Int(nil)
		// takerAmount = USDC received = size * price
		revenue := roundDown(sizeRounded*price, amtDecimals)
		takerF := new(big.Float).Mul(new(big.Float).SetFloat64(revenue), scale)
		takerAmt, _ = takerF.Int(nil)
	}

	return makerAmt, takerAmt
}

// roundDown truncates a float to the given number of decimal places.
func roundDown(val float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return float64(int64(val*pow)) / pow
}
