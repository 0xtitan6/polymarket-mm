package api

import (
	"time"

	"polymarket-mm/internal/config"
)

// DashboardSnapshot represents the complete dashboard state
type DashboardSnapshot struct {
	Timestamp time.Time `json:"timestamp"`

	// Active markets
	Markets []MarketStatus `json:"markets"`

	// Aggregate P&L
	TotalRealized   float64 `json:"total_realized"`
	TotalUnrealized float64 `json:"total_unrealized"`
	TotalPnL        float64 `json:"total_pnl"`

	// Risk status
	Risk RiskSnapshot `json:"risk"`

	// Configuration
	Config ConfigSummary `json:"config"`

	// Scanner info
	Scanner ScannerInfo `json:"scanner"`
}

// MarketStatus represents per-market state
type MarketStatus struct {
	ConditionID string `json:"condition_id"`
	Slug        string `json:"slug"`
	Question    string `json:"question"`

	// Book state
	MidPrice     float64   `json:"mid_price"`
	BestBid      float64   `json:"best_bid"`
	BestAsk      float64   `json:"best_ask"`
	Spread       float64   `json:"spread"`
	SpreadBps    float64   `json:"spread_bps"` // Spread in basis points
	LastUpdated  time.Time `json:"last_updated"`
	IsStale      bool      `json:"is_stale"`

	// Position
	Position PositionSnapshot `json:"position"`

	// Current quotes (if active)
	ActiveBid        *QuoteInfo `json:"active_bid,omitempty"`
	ActiveAsk        *QuoteInfo `json:"active_ask,omitempty"`
	ReservationPrice float64    `json:"reservation_price"`
	OptimalSpread    float64    `json:"optimal_spread"`

	// Market metadata
	TickSize  float64   `json:"tick_size"`
	EndDate   time.Time `json:"end_date"`
	Liquidity float64   `json:"liquidity"`
	Volume24h float64   `json:"volume_24h"`
}

// PositionSnapshot represents position and P&L for a market
type PositionSnapshot struct {
	YesQty        float64 `json:"yes_qty"`
	NoQty         float64 `json:"no_qty"`
	AvgEntryYes   float64 `json:"avg_entry_yes"`
	AvgEntryNo    float64 `json:"avg_entry_no"`
	RealizedPnL   float64 `json:"realized_pnl"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	ExposureUSD   float64 `json:"exposure_usd"`
	Skew          float64 `json:"skew"` // NetDelta in [-1, 1]
	LastUpdated   time.Time `json:"last_updated"`
}

// QuoteInfo represents a single quote (bid or ask)
type QuoteInfo struct {
	Price     float64   `json:"price"`
	Size      float64   `json:"size"`
	OrderID   string    `json:"order_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// RiskSnapshot represents aggregate risk metrics
type RiskSnapshot struct {
	// Exposure
	GlobalExposure    float64 `json:"global_exposure"`
	MaxGlobalExposure float64 `json:"max_global_exposure"`
	ExposurePct       float64 `json:"exposure_pct"` // % of max

	// Kill switch
	KillSwitchActive bool      `json:"kill_switch_active"`
	KillSwitchUntil  time.Time `json:"kill_switch_until,omitempty"`
	KillSwitchReason string    `json:"kill_switch_reason,omitempty"`

	// P&L tracking
	TotalRealizedPnL   float64 `json:"total_realized_pnl"`
	TotalUnrealizedPnL float64 `json:"total_unrealized_pnl"`

	// Limits
	MaxPositionPerMarket float64 `json:"max_position_per_market"`
	MaxDailyLoss         float64 `json:"max_daily_loss"`
	MaxMarketsActive     int     `json:"max_markets_active"`
	CurrentMarketsActive int     `json:"current_markets_active"`
}

// ConfigSummary represents strategy and risk configuration
type ConfigSummary struct {
	// Strategy parameters
	Gamma              float64 `json:"gamma"`
	Sigma              float64 `json:"sigma"`
	K                  float64 `json:"k"`
	T                  float64 `json:"t"`
	DefaultSpreadBps   int     `json:"default_spread_bps"`
	OrderSizeUSD       float64 `json:"order_size_usd"`
	RefreshInterval    string  `json:"refresh_interval"`
	StaleBookTimeout   string  `json:"stale_book_timeout"`

	// Risk parameters
	MaxPositionPerMarket float64 `json:"max_position_per_market"`
	MaxGlobalExposure    float64 `json:"max_global_exposure"`
	MaxMarketsActive     int     `json:"max_markets_active"`
	KillSwitchDropPct    float64 `json:"kill_switch_drop_pct"`
	KillSwitchWindowSec  int     `json:"kill_switch_window_sec"`
	MaxDailyLoss         float64 `json:"max_daily_loss"`
	CooldownAfterKill    string  `json:"cooldown_after_kill"`

	// Scanner parameters
	ScannerPollInterval string  `json:"scanner_poll_interval"`
	MinLiquidity        float64 `json:"min_liquidity"`
	MinVolume24h        float64 `json:"min_volume_24h"`
	MinSpread           float64 `json:"min_spread"`
	MaxEndDateDays      int     `json:"max_end_date_days"`

	// Operational
	DryRun bool `json:"dry_run"`
}

// ScannerInfo represents scanner state
type ScannerInfo struct {
	LastScanTime     time.Time `json:"last_scan_time"`
	MarketsScanned   int       `json:"markets_scanned"`
	MarketsFiltered  int       `json:"markets_filtered"`
	MarketsSelected  int       `json:"markets_selected"`
}

// NewConfigSummary creates config summary from config
func NewConfigSummary(cfg config.Config) ConfigSummary {
	return ConfigSummary{
		// Strategy
		Gamma:            cfg.Strategy.Gamma,
		Sigma:            cfg.Strategy.Sigma,
		K:                cfg.Strategy.K,
		T:                cfg.Strategy.T,
		DefaultSpreadBps: cfg.Strategy.DefaultSpreadBps,
		OrderSizeUSD:     cfg.Strategy.OrderSizeUSD,
		RefreshInterval:  cfg.Strategy.RefreshInterval.String(),
		StaleBookTimeout: cfg.Strategy.StaleBookTimeout.String(),

		// Risk
		MaxPositionPerMarket: cfg.Risk.MaxPositionPerMarket,
		MaxGlobalExposure:    cfg.Risk.MaxGlobalExposure,
		MaxMarketsActive:     cfg.Risk.MaxMarketsActive,
		KillSwitchDropPct:    cfg.Risk.KillSwitchDropPct,
		KillSwitchWindowSec:  cfg.Risk.KillSwitchWindowSec,
		MaxDailyLoss:         cfg.Risk.MaxDailyLoss,
		CooldownAfterKill:    cfg.Risk.CooldownAfterKill.String(),

		// Scanner
		ScannerPollInterval: cfg.Scanner.PollInterval.String(),
		MinLiquidity:        cfg.Scanner.MinLiquidity,
		MinVolume24h:        cfg.Scanner.MinVolume24h,
		MinSpread:           cfg.Scanner.MinSpread,
		MaxEndDateDays:      cfg.Scanner.MaxEndDateDays,

		// Operational
		DryRun: cfg.DryRun,
	}
}
