# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

### Phase 2: Order Flow Analytics (Planned)
- Fill clustering detection
- Sweep pattern recognition
- Orderbook imbalance analysis
- Asymmetric spread adjustments

### Phase 3: Resolution Proximity Management (Planned)
- Time-to-resolution tracking
- Progressive risk reduction near expiry
- Forced inventory flattening
- Emergency stop quoting before resolution

---

## [0.2.0] - 2026-02-12

### Added - Phase 1: Toxic Flow Detection ✅

**Core Components:**
- `internal/strategy/flow_tracker.go` - Toxicity detection engine
- `internal/strategy/flow_tracker_test.go` - Comprehensive unit tests (8 test cases)

**Features:**
- **Directional Imbalance Detection**: Identifies when fills are consistently one-sided (adverse selection)
- **Fill Velocity Analysis**: Detects burst patterns indicating sweeps or aggressive orders
- **Adaptive Spread Widening**: Automatically widens spreads 1.0x-3.0x based on toxicity score
- **Cooldown Protection**: Maintains wider spreads for configurable period (default 120s) after toxic flow
- **Fill History Retention**: Bounded circular buffer (1000 fills per market, ~80KB memory)

**Configuration Parameters:**
- `flow_window` - Rolling window for fill tracking (default: 60s)
- `flow_toxicity_threshold` - Trigger threshold for spread widening (default: 0.6)
- `flow_cooldown_period` - Post-toxicity cooldown duration (default: 120s)
- `flow_max_spread_multiplier` - Maximum spread widening factor (default: 3.0x)

**Metrics Logged:**
- `toxicity_score` - Composite adverse selection score [0, 1]
- `directional_imbalance` - Percentage of fills in dominant direction
- `fill_velocity` - Fills per minute
- `flow_spread_multiplier` - Current spread adjustment factor

**Testing:**
- All unit tests passing (8/8)
- Build verified with Go 1.24
- Integration complete with existing A-S strategy

**Files Modified:**
- `internal/strategy/inventory.go` - Added fill history tracking
- `internal/strategy/maker.go` - Integrated FlowTracker into quote cycle
- `internal/config/config.go` - Added flow detection config fields
- `configs/config.yaml` - Added Phase 1 parameters
- `README.md` - Documented Phase 1 features and usage

### Changed
- Quote computation now applies toxicity-based spread multiplier before A-S calculation
- Fill handling now feeds FlowTracker and logs warnings on toxic flow detection
- Debug logs include toxicity metrics on every quote cycle

---

## [0.1.0] - 2026-02-12

### Initial Release

**Core Features:**
- Avellaneda-Stoikov market-making algorithm for Polymarket
- WebSocket integration for real-time market data (book + user feeds)
- Risk management with kill switches and position limits
- Market scanner for automatic market discovery
- JSON-based position persistence
- Web dashboard for monitoring (port 8080)
- Dry run mode for safe testing

**Architecture:**
- Clean separation: engine → {strategy, market, exchange, risk, store}
- Per-market strategy instances with independent order management
- Concurrent WebSocket feeds with channel-based event routing

**Configuration:**
- YAML-based config with environment variable overrides
- Support for POLY_PRIVATE_KEY, POLY_API_KEY, etc.
- Configurable A-S parameters (gamma, sigma, k, T)
- Risk limits (position caps, kill switch thresholds, daily loss)

**Security:**
- Private keys via environment variables only
- Sensitive data excluded from git (.gitignore configured)
- Proxy wallet support (POLY_PROXY signature type)

---

## Version Numbering

- **0.x.x**: Pre-production, actively developed
- **Phase 1**: Toxic flow detection (0.2.0)
- **Phase 2**: Order flow analytics (planned)
- **Phase 3**: Resolution proximity management (planned)
- **1.0.0**: Production-ready after all phases complete and tested

## Links

- GitHub: https://github.com/0xtitan6/polymarket-mm
- Issues: https://github.com/0xtitan6/polymarket-mm/issues
