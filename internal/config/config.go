// Package config defines all configuration for the market-making bot.
// Config is loaded from a YAML file (default: configs/config.yaml) with
// sensitive fields overridable via POLY_* environment variables.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the top-level configuration. Maps directly to the YAML file structure.
type Config struct {
	DryRun    bool            `mapstructure:"dry_run"`
	Wallet    WalletConfig    `mapstructure:"wallet"`
	API       APIConfig       `mapstructure:"api"`
	Strategy  StrategyConfig  `mapstructure:"strategy"`
	Risk      RiskConfig      `mapstructure:"risk"`
	Scanner   ScannerConfig   `mapstructure:"scanner"`
	Store     StoreConfig     `mapstructure:"store"`
	Logging   LoggingConfig   `mapstructure:"logging"`
	Dashboard DashboardConfig `mapstructure:"dashboard"`
}

// WalletConfig holds the Ethereum wallet used for signing orders.
// PrivateKey signs L1 (EIP-712) auth and derives L2 API keys.
// FunderAddress is the on-chain address that funds orders (may differ from signer if using a proxy).
type WalletConfig struct {
	PrivateKey    string `mapstructure:"private_key"`
	SignatureType int    `mapstructure:"signature_type"`
	FunderAddress string `mapstructure:"funder_address"`
	ChainID       int    `mapstructure:"chain_id"`
}

// APIConfig holds Polymarket API endpoints and optional pre-derived L2 credentials.
// If ApiKey/Secret/Passphrase are empty, the bot derives them via L1 auth on startup.
type APIConfig struct {
	CLOBBaseURL  string `mapstructure:"clob_base_url"`
	GammaBaseURL string `mapstructure:"gamma_base_url"`
	WSMarketURL  string `mapstructure:"ws_market_url"`
	WSUserURL    string `mapstructure:"ws_user_url"`
	ApiKey       string `mapstructure:"api_key"`
	Secret       string `mapstructure:"secret"`
	Passphrase   string `mapstructure:"passphrase"`
}

// StrategyConfig tunes the Avellaneda-Stoikov market-making algorithm.
//
//   - Gamma: risk aversion parameter. Higher = tighter spread, less inventory risk.
//   - Sigma: estimated price volatility (annualized std dev).
//   - K:     order arrival rate. Higher K = more aggressive quotes.
//   - T:     time horizon in years (e.g. 1.0 = 1 year).
//   - DefaultSpreadBps: minimum spread floor in basis points.
//   - OrderSizeUSD: target notional size per order.
//   - RefreshInterval: how often to recompute and reconcile quotes.
//   - StaleBookTimeout: cancel all orders if no book update within this window.
//
// Flow Detection (Phase 1):
//   - FlowWindow: rolling time window for tracking fills (e.g., 60s).
//   - FlowToxicityThreshold: toxicity score above this triggers spread widening (e.g., 0.6).
//   - FlowCooldownPeriod: stay wide for this duration after toxicity detected (e.g., 120s).
//   - FlowMaxSpreadMultiplier: maximum spread widening factor (e.g., 3.0x).
type StrategyConfig struct {
	Gamma            float64       `mapstructure:"gamma"`
	Sigma            float64       `mapstructure:"sigma"`
	K                float64       `mapstructure:"k"`
	T                float64       `mapstructure:"t"`
	DefaultSpreadBps int           `mapstructure:"default_spread_bps"`
	OrderSizeUSD     float64       `mapstructure:"order_size_usd"`
	RefreshInterval  time.Duration `mapstructure:"refresh_interval"`
	StaleBookTimeout time.Duration `mapstructure:"stale_book_timeout"`

	// Phase 1: Toxic flow detection
	FlowWindow              time.Duration `mapstructure:"flow_window"`
	FlowToxicityThreshold   float64       `mapstructure:"flow_toxicity_threshold"`
	FlowCooldownPeriod      time.Duration `mapstructure:"flow_cooldown_period"`
	FlowMaxSpreadMultiplier float64       `mapstructure:"flow_max_spread_multiplier"`
}

// RiskConfig sets hard limits that trigger order cancellation (kill switch).
//
//   - MaxPositionPerMarket: max USD exposure in any single market.
//   - MaxGlobalExposure: max USD exposure across ALL active markets combined.
//   - MaxMarketsActive: cap on how many markets the bot trades simultaneously.
//   - KillSwitchDropPct: if price moves this % within the window, kill switch fires.
//   - KillSwitchWindowSec: time window for measuring rapid price movement.
//   - MaxDailyLoss: max combined (realized + unrealized) loss before kill switch.
//   - CooldownAfterKill: how long the kill switch stays engaged after firing.
type RiskConfig struct {
	MaxPositionPerMarket float64       `mapstructure:"max_position_per_market"`
	MaxGlobalExposure    float64       `mapstructure:"max_global_exposure"`
	MaxMarketsActive     int           `mapstructure:"max_markets_active"`
	KillSwitchDropPct    float64       `mapstructure:"kill_switch_drop_pct"`
	KillSwitchWindowSec  int           `mapstructure:"kill_switch_window_sec"`
	MaxDailyLoss         float64       `mapstructure:"max_daily_loss"`
	CooldownAfterKill    time.Duration `mapstructure:"cooldown_after_kill"`
}

// ScannerConfig controls how the bot discovers and filters tradeable markets.
// The scanner polls the Gamma API and ranks markets by opportunity score:
// score = spread * sqrt(volume24h) * min(liquidity/10000, 1).
type ScannerConfig struct {
	PollInterval   time.Duration `mapstructure:"poll_interval"`
	MinLiquidity   float64       `mapstructure:"min_liquidity"`
	MinVolume24h   float64       `mapstructure:"min_volume_24h"`
	MinSpread      float64       `mapstructure:"min_spread"`
	MaxEndDateDays int           `mapstructure:"max_end_date_days"`
	ExcludeSlugs   []string      `mapstructure:"exclude_slugs"`
}

// StoreConfig sets where position data is persisted (JSON files).
type StoreConfig struct {
	DataDir string `mapstructure:"data_dir"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// DashboardConfig controls the web dashboard server.
type DashboardConfig struct {
	Enabled        bool     `mapstructure:"enabled"`
	Port           int      `mapstructure:"port"`
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// Load reads config from a YAML file with env var overrides.
// Sensitive fields use env vars: POLY_PRIVATE_KEY, POLY_API_KEY, POLY_API_SECRET, POLY_PASSPHRASE.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvPrefix("POLY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Override sensitive fields from env
	if key := os.Getenv("POLY_PRIVATE_KEY"); key != "" {
		cfg.Wallet.PrivateKey = key
	}
	if key := os.Getenv("POLY_API_KEY"); key != "" {
		cfg.API.ApiKey = key
	}
	if secret := os.Getenv("POLY_API_SECRET"); secret != "" {
		cfg.API.Secret = secret
	}
	if pass := os.Getenv("POLY_PASSPHRASE"); pass != "" {
		cfg.API.Passphrase = pass
	}
	if os.Getenv("POLY_DRY_RUN") == "true" || os.Getenv("POLY_DRY_RUN") == "1" {
		cfg.DryRun = true
	}

	return &cfg, nil
}

// Validate checks all required fields and value ranges.
func (c *Config) Validate() error {
	if c.Wallet.PrivateKey == "" {
		return fmt.Errorf("wallet.private_key is required (set POLY_PRIVATE_KEY)")
	}
	if c.Wallet.ChainID == 0 {
		return fmt.Errorf("wallet.chain_id is required (137 for mainnet)")
	}
	switch c.Wallet.SignatureType {
	case 0, 1, 2:
	default:
		return fmt.Errorf("wallet.signature_type must be one of: 0 (EOA), 1 (POLY_PROXY), 2 (GNOSIS_SAFE)")
	}
	if c.Wallet.SignatureType != 0 && c.Wallet.FunderAddress == "" {
		return fmt.Errorf("wallet.funder_address is required when wallet.signature_type is 1 or 2")
	}
	if c.API.CLOBBaseURL == "" {
		return fmt.Errorf("api.clob_base_url is required")
	}
	if c.Strategy.Gamma <= 0 {
		return fmt.Errorf("strategy.gamma must be > 0")
	}
	if c.Strategy.OrderSizeUSD <= 0 {
		return fmt.Errorf("strategy.order_size_usd must be > 0")
	}
	if c.Risk.MaxPositionPerMarket <= 0 {
		return fmt.Errorf("risk.max_position_per_market must be > 0")
	}
	if c.Risk.MaxGlobalExposure <= 0 {
		return fmt.Errorf("risk.max_global_exposure must be > 0")
	}
	if c.Risk.MaxMarketsActive <= 0 {
		return fmt.Errorf("risk.max_markets_active must be > 0")
	}
	return nil
}
