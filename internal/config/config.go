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
	Auth      AuthConfig      `mapstructure:"auth"`
	API       APIConfig       `mapstructure:"api"`
	Strategy  StrategyConfig  `mapstructure:"strategy"`
	Risk      RiskConfig      `mapstructure:"risk"`
	Scanner   ScannerConfig   `mapstructure:"scanner"`
	Store     StoreConfig     `mapstructure:"store"`
	Logging   LoggingConfig   `mapstructure:"logging"`
	Dashboard DashboardConfig `mapstructure:"dashboard"`
}

// AuthConfig holds the Ed25519 API credentials for the Polymarket US API.
// APIKeyID is the UUID assigned when the API key is created.
// PrivateKeyB64 is the base64-encoded Ed25519 private key (seed in first 32 bytes).
type AuthConfig struct {
	APIKeyID      string `mapstructure:"api_key_id"`      // Ed25519 API key UUID
	PrivateKeyB64 string `mapstructure:"private_key_b64"` // base64-encoded Ed25519 private key
}

// APIConfig holds Polymarket US API endpoints.
type APIConfig struct {
	BaseURL      string `mapstructure:"base_url"`       // https://api.polymarket.us
	WSMarketURL  string `mapstructure:"ws_market_url"`  // wss://api.polymarket.us/v1/ws/markets
	WSPrivateURL string `mapstructure:"ws_private_url"` // wss://api.polymarket.us/v1/ws/private

	// WSUserURL is a compatibility alias for WSPrivateURL, retained so
	// existing engine code that reads cfg.API.WSUserURL continues to compile.
	// Configure either field; WSUserURL takes precedence if both are set.
	WSUserURL string `mapstructure:"ws_user_url"`

	// GammaBaseURL is retained for the market scanner which may still poll
	// the Polymarket Gamma metadata API for market discovery.
	GammaBaseURL string `mapstructure:"gamma_base_url"`

	// PolyCLOBBaseURL is the base URL for the Polymarket public CLOB API.
	// Used by the Synthesis bot to read public market data (order books,
	// midpoints, prices) while routing orders through Synthesis.
	// Defaults to "https://clob.polymarket.com" if empty.
	PolyCLOBBaseURL string `mapstructure:"poly_clob_base_url"`
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

	// Half-Kelly position sizing
	KellyFraction float64 `mapstructure:"kelly_fraction"`   // Kelly multiplier (0.5 = half-Kelly)
	MinEdgeBps    int     `mapstructure:"min_edge_bps"`     // Minimum edge in bps to trade (filters noise)
	WinnerFeePct  float64 `mapstructure:"winner_fee_pct"`   // Polymarket winner fee (e.g., 0.02 = 2%)

	// Volatility-aware spread
	VolLookbackFills int     `mapstructure:"vol_lookback_fills"` // Number of recent price samples for realized vol
	VolSpreadFloor   float64 `mapstructure:"vol_spread_floor"`   // Minimum vol-based spread multiplier (1.0 = no effect)
	VolSpreadCeiling float64 `mapstructure:"vol_spread_ceiling"` // Maximum vol-based spread multiplier

	// BBO matching: when the best bid/ask has fewer than this many tokens,
	// quote AT the BBO price to get queue priority. 0 = always use A-S model.
	// Recommended: 100-500 tokens for small accounts.
	BBOMatchMaxDepth float64 `mapstructure:"bbo_match_max_depth"`
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
// The scanner polls the Markets API and ranks markets by opportunity score.
//
// Scoring: score = spread / (avgDepth + 1). Thinner books = higher score.
// This lets our small orders sit near the top of the book instead of behind
// thousands of tokens from whales.
//
// Hysteresis: markets currently being traded get a StickyBonus boost to their
// score, preventing the scanner from rotating away too quickly. Combined with
// a longer PollInterval, this keeps the bot on profitable markets.
//
// Depth filter: MaxTopOfBookDepth filters out markets where the best bid/ask
// has more tokens than we can realistically compete with.
type ScannerConfig struct {
	PollInterval        time.Duration `mapstructure:"poll_interval"`
	MinLiquidity        float64       `mapstructure:"min_liquidity"`
	MinVolume24h        float64       `mapstructure:"min_volume_24h"`
	MinSpread           float64       `mapstructure:"min_spread"`
	MaxEndDateDays      int           `mapstructure:"max_end_date_days"`
	IncludeConditionIDs []string      `mapstructure:"include_condition_ids"` // retained for Gamma-based scanner
	IncludeSlugs        []string      `mapstructure:"include_slugs"`
	IncludeKeywords     []string      `mapstructure:"include_keywords"`
	ExcludeKeywords     []string      `mapstructure:"exclude_keywords"`
	ExcludeSlugs        []string      `mapstructure:"exclude_slugs"`

	// Thin-market targeting: skip markets where top-of-book depth exceeds this.
	// 0 = no filter (disabled). Recommended: 500-2000 tokens.
	MaxTopOfBookDepth int `mapstructure:"max_top_of_book_depth"`

	// Hysteresis: bonus score for markets the bot is currently trading.
	// Prevents premature rotation. 0 = no bonus. Recommended: 0.5-2.0.
	StickyBonus float64 `mapstructure:"sticky_bonus"`
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
// Sensitive fields use env vars: POLY_API_KEY_ID, POLY_PRIVATE_KEY_B64.
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

	cfg.SetDefaults()

	// Override sensitive auth fields from env
	if key := os.Getenv("POLY_API_KEY_ID"); key != "" {
		cfg.Auth.APIKeyID = key
	}
	if pk := os.Getenv("POLY_PRIVATE_KEY_B64"); pk != "" {
		cfg.Auth.PrivateKeyB64 = pk
	}
	if os.Getenv("POLY_DRY_RUN") == "true" || os.Getenv("POLY_DRY_RUN") == "1" {
		cfg.DryRun = true
	}

	return &cfg, nil
}

// Validate checks all required fields and value ranges.
func (c *Config) Validate() error {
	if c.Auth.APIKeyID == "" {
		return fmt.Errorf("auth.api_key_id is required (set POLY_API_KEY_ID)")
	}
	if c.Auth.PrivateKeyB64 == "" {
		return fmt.Errorf("auth.private_key_b64 is required (set POLY_PRIVATE_KEY_B64)")
	}
	if c.API.BaseURL == "" {
		return fmt.Errorf("api.base_url is required")
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

// SetDefaults fills in zero-valued new EV-strategy fields with sensible defaults.
// Called automatically by Load() after Unmarshal.
func (c *Config) SetDefaults() {
	if c.Strategy.KellyFraction == 0 {
		c.Strategy.KellyFraction = 0.5
	}
	if c.Strategy.WinnerFeePct == 0 {
		c.Strategy.WinnerFeePct = 0.02
	}
	if c.Strategy.VolLookbackFills == 0 {
		c.Strategy.VolLookbackFills = 30
	}
	if c.Strategy.VolSpreadFloor == 0 {
		c.Strategy.VolSpreadFloor = 1.0
	}
	if c.Strategy.VolSpreadCeiling == 0 {
		c.Strategy.VolSpreadCeiling = 3.0
	}
	if c.Strategy.MinEdgeBps == 0 {
		c.Strategy.MinEdgeBps = 50
	}
}
