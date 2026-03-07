// Package config — SynthesisConfig is the Synthesis Trade API configuration.
//
// This file adds a SynthesisConfig type that reuses all the common strategy,
// risk, scanner, store, logging, and dashboard config from the base Config,
// but replaces the auth block with Synthesis-specific fields:
//
//   auth.api_key    — static API key sent as X-API-KEY header (SYNTH_API_KEY)
//   auth.wallet_id  — Synthesis wallet ID for order placement (SYNTH_WALLET_ID)
//   auth.venue      — trading venue: "pol" (Polymarket) or "kal" (Kalshi)
//
// The existing Ed25519 auth fields (api_key_id, private_key_b64) are absent
// from synthesis_config.yaml and will be empty after loading — this is safe
// because SynthesisClient never calls NewAuth (the Polymarket Ed25519 auth).
//
// Load flow:
//
//	cfg, err := config.LoadSynthesis("configs/synthesis_config.yaml")
//	auth, err := exchange.NewSynthesisAuth(*cfg.ToBaseConfig())
//
// Environment overrides use the SYNTH_ prefix:
//
//	SYNTH_API_KEY    — overrides auth.api_key
//	SYNTH_WALLET_ID  — overrides auth.wallet_id
//	SYNTH_DRY_RUN    — overrides dry_run
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// SynthesisAuthConfig holds Synthesis Trade API credentials.
// These replace the Ed25519 fields used by the Polymarket Auth.
type SynthesisAuthConfig struct {
	// APIKey is the static API key sent as the X-API-KEY header.
	// Set via SYNTH_API_KEY environment variable.
	APIKey string `mapstructure:"api_key"`

	// WalletID is the Synthesis wallet ID used in order placement URLs:
	// POST /api/v1/wallet/{venue}/{wallet_id}/order
	// Set via SYNTH_WALLET_ID environment variable.
	WalletID string `mapstructure:"wallet_id"`

	// Venue is the trading venue code:
	//   "pol" → Polymarket
	//   "sol" → Kalshi
	// Defaults to "pol" if not set.
	Venue string `mapstructure:"venue"`
}

// SynthesisConfig is the full configuration for the Synthesis-backed bot.
//
// It mirrors the structure of Config but replaces the AuthConfig block with
// SynthesisAuthConfig. All other sections (strategy, risk, scanner, etc.) are
// identical and can be reused unchanged.
type SynthesisConfig struct {
	DryRun    bool                `mapstructure:"dry_run"`
	Auth      SynthesisAuthConfig `mapstructure:"auth"`
	API       APIConfig           `mapstructure:"api"`
	Strategy  StrategyConfig      `mapstructure:"strategy"`
	Risk      RiskConfig          `mapstructure:"risk"`
	Scanner   ScannerConfig       `mapstructure:"scanner"`
	Store     StoreConfig         `mapstructure:"store"`
	Logging   LoggingConfig       `mapstructure:"logging"`
	Dashboard DashboardConfig     `mapstructure:"dashboard"`
}

// ToBaseConfig converts a SynthesisConfig to the base Config type used by the
// engine, scanner, risk manager, and all downstream components.
//
// The auth fields are intentionally left empty in the resulting Config because
// Synthesis uses a different auth mechanism (SynthesisAuth, not the Ed25519 Auth).
// The engine is wired with SynthesisAuth directly; it never reads Config.Auth.
func (sc *SynthesisConfig) ToBaseConfig() *Config {
	return &Config{
		DryRun: sc.DryRun,
		Auth: AuthConfig{
			// Left empty — Synthesis auth is handled by SynthesisAuth, not
			// the Ed25519-based Auth. The engine is wired separately.
			APIKeyID:      "",
			PrivateKeyB64: "",
		},
		API:       sc.API,
		Strategy:  sc.Strategy,
		Risk:      sc.Risk,
		Scanner:   sc.Scanner,
		Store:     sc.Store,
		Logging:   sc.Logging,
		Dashboard: sc.Dashboard,
	}
}

// Validate checks that all required Synthesis-specific fields are present.
// It also runs the same strategy/risk validation as Config.Validate so that
// misconfigured bots are caught at startup.
func (sc *SynthesisConfig) Validate() error {
	if sc.Auth.APIKey == "" {
		return fmt.Errorf("auth.api_key is required (set SYNTH_API_KEY)")
	}
	if sc.Auth.WalletID == "" {
		return fmt.Errorf("auth.wallet_id is required (set SYNTH_WALLET_ID)")
	}
	if sc.Auth.Venue == "" {
		return fmt.Errorf("auth.venue is required (\"pol\" for Polymarket, \"sol\" for Kalshi)")
	}
	if sc.API.BaseURL == "" {
		return fmt.Errorf("api.base_url is required")
	}
	if sc.Strategy.Gamma <= 0 {
		return fmt.Errorf("strategy.gamma must be > 0")
	}
	if sc.Strategy.OrderSizeUSD <= 0 {
		return fmt.Errorf("strategy.order_size_usd must be > 0")
	}
	if sc.Risk.MaxPositionPerMarket <= 0 {
		return fmt.Errorf("risk.max_position_per_market must be > 0")
	}
	if sc.Risk.MaxGlobalExposure <= 0 {
		return fmt.Errorf("risk.max_global_exposure must be > 0")
	}
	if sc.Risk.MaxMarketsActive <= 0 {
		return fmt.Errorf("risk.max_markets_active must be > 0")
	}
	return nil
}

// LoadSynthesis reads config from a YAML file with SYNTH_ env var overrides.
//
// Sensitive auth fields are overridable without touching the config file:
//
//	SYNTH_API_KEY    — overrides auth.api_key
//	SYNTH_WALLET_ID  — overrides auth.wallet_id
//	SYNTH_DRY_RUN    — set to "true" to enable dry-run mode
func LoadSynthesis(path string) (*SynthesisConfig, error) {
	v := viper.New()
	v.SetConfigFile(path)

	// SYNTH_ prefix for env overrides (e.g. SYNTH_AUTH_API_KEY → auth.api_key)
	v.SetEnvPrefix("SYNTH")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read synthesis config: %w", err)
	}

	var cfg SynthesisConfig
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal synthesis config: %w", err)
	}

	// Apply defaults shared with the base config
	base := cfg.ToBaseConfig()
	base.SetDefaults()
	cfg.Strategy = base.Strategy

	// Manual env overrides for sensitive fields (flat SYNTH_* names)
	if key := os.Getenv("SYNTH_API_KEY"); key != "" {
		cfg.Auth.APIKey = key
	}
	if wid := os.Getenv("SYNTH_WALLET_ID"); wid != "" {
		cfg.Auth.WalletID = wid
	}
	if os.Getenv("SYNTH_DRY_RUN") == "true" || os.Getenv("SYNTH_DRY_RUN") == "1" {
		cfg.DryRun = true
	}

	// Default venue to Polymarket
	if cfg.Auth.Venue == "" {
		cfg.Auth.Venue = "pol"
	}

	// Default base URL
	if cfg.API.BaseURL == "" {
		cfg.API.BaseURL = "https://synthesis.trade/api/v1"
	}

	return &cfg, nil
}
