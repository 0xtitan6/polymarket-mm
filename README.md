# Polymarket Market Maker

A Go-based automated market-making bot for Polymarket prediction markets using the Avellaneda-Stoikov algorithm.

## Features

### Core Strategy
- **Avellaneda-Stoikov Algorithm**: Dynamic spread pricing based on inventory and risk
- **Real-time Market Data**: WebSocket feeds for orderbook and user events
- **Market Scanner**: Automatically discovers and monitors active markets
- **Dashboard**: Web-based monitoring interface on port 8080
- **Dry Run Mode**: Test strategies without placing real orders

### Advanced Flow Detection (Phase 1) ✅
- **Toxic Flow Detection**: Identifies adverse selection patterns (e.g., getting picked off by informed traders)
- **Directional Imbalance Tracking**: Detects when fills are consistently one-sided (100% buys or sells)
- **Fill Velocity Analysis**: Identifies burst patterns suggesting sweeps or aggressive orders
- **Adaptive Spread Widening**: Automatically widens spreads 1.0x-3.0x when toxicity detected
- **Cooldown Protection**: Maintains wider spreads for 2 minutes after toxic flow to avoid repeat attacks

### Risk Management
- **Position Limits**: Per-market and global exposure caps
- **Kill Switch**: Auto-cancels all orders on rapid price moves (15% default)
- **Daily Loss Limit**: Stops trading after hitting daily loss threshold
- **Cooldown Period**: Enforced pause after kill switch activation
- **Stale Book Detection**: Cancels quotes if orderbook data becomes stale

## Architecture

```
cmd/bot/main.go          # Entry point
├── engine/              # Core trading engine
├── strategy/            # Avellaneda-Stoikov implementation
├── market/              # Market data & orderbook management
├── exchange/            # Polymarket CLOB API client
├── risk/                # Risk management & kill switches
└── store/               # JSON-based persistence
```

## Prerequisites

- Go 1.24+ (set `GOTOOLCHAIN=auto` if using older versions)
- Polymarket account with proxy wallet
- Polygon (MATIC) for gas fees
- USDC for trading

## Installation

```bash
git clone https://github.com/0xtitan6/polymarket-mm.git
cd polymarket-mm
go mod download
```

## Configuration

### 1. Environment Variables

Set your credentials via environment variables (never commit these):

```bash
export POLY_PRIVATE_KEY='0xYOUR_PRIVATE_KEY_HERE'
export POLY_API_KEY='your-api-key'           # Optional: auto-derived if not set
export POLY_API_SECRET='your-api-secret'     # Optional: auto-derived if not set
export POLY_PASSPHRASE='your-passphrase'     # Optional: auto-derived if not set
```

### 2. Update Config File

Edit `configs/config.yaml`:

```yaml
wallet:
  funder_address: "0xYourProxyWalletAddress"  # From polymarket.com/settings

strategy:
  gamma: 0.1              # Risk aversion (higher = tighter inventory control)
  sigma: 0.5              # Annualized volatility estimate
  order_size_usd: 1.0     # Quote size per side in USDC
  refresh_interval: 5s    # How often to re-quote

  # Phase 1: Toxic flow detection
  flow_window: 60s                    # Track fills in last 60 seconds
  flow_toxicity_threshold: 0.6        # Score > 0.6 triggers spread widening
  flow_cooldown_period: 120s          # Stay wide for 2 minutes after toxic flow
  flow_max_spread_multiplier: 3.0     # Max 3x spread widening

risk:
  max_position_per_market: 10.0
  max_global_exposure: 20.0
  kill_switch_drop_pct: 0.15    # 15% price move triggers emergency stop
```

### 3. Test Credentials

```bash
./scripts/test_credentials.sh
```

## Usage

### Build

```bash
go build -o bot cmd/bot/main.go
```

### Run

```bash
# Dry run mode (no real orders)
./bot

# Live trading (set dry_run: false in config.yaml)
./bot
```

### Dashboard

Access the web dashboard at `http://localhost:8080` to monitor:
- Active positions
- P&L tracking
- Order flow
- Risk metrics

## Strategy: Avellaneda-Stoikov + Flow Detection

The bot uses the Avellaneda-Stoikov market-making algorithm with advanced flow detection enhancements:

### Base Strategy (Avellaneda-Stoikov)

- **Dynamic Spreads**: Adjusts bid/ask spreads based on inventory risk
- **Inventory Management**: Skews quotes to mean-revert position to zero
- **Risk Aversion**: Parameter `gamma` controls how aggressively to reduce inventory
- **Volatility-Aware**: Uses `sigma` to estimate fair spread based on market volatility

**Key Parameters:**
- `gamma`: Risk aversion (0.05-0.3 typical range)
- `sigma`: Annualized volatility (0.3-0.8 for prediction markets)
- `k`: Order arrival intensity
- `T`: Time horizon in years (~0.00274 = 1 day)

### Phase 1: Toxic Flow Detection ✅ **IMPLEMENTED**

Protects against adverse selection by detecting when informed traders are picking off stale quotes.

**How It Works:**
1. **Tracks recent fills** in a 60-second rolling window
2. **Calculates toxicity score**:
   - **Directional Imbalance** (60% weight): % of fills in dominant direction
     - 100% same-side fills = 1.0 imbalance (very toxic)
     - 50/50 split = 0.5 imbalance (balanced)
   - **Fill Velocity** (40% weight): Fills per minute
     - >3 fills/min = potential sweep
   - **Composite Score**: `0.6 × imbalance + 0.4 × velocity`
3. **Widens spreads** when score > 0.6 (threshold)
   - Score 0.6 → ~2.0x spread
   - Score 1.0 → 3.0x spread (max)
4. **Cooldown period**: Stays wide for 2 minutes after toxicity detected

**Example:**
```
Normal: 2% spread (1.0x multiplier)
Toxic:  6% spread (3.0x multiplier)
```

**Configuration:**
```yaml
strategy:
  flow_window: 60s                    # Tracking window
  flow_toxicity_threshold: 0.6        # Trigger threshold
  flow_cooldown_period: 120s          # Post-toxicity cooldown
  flow_max_spread_multiplier: 3.0     # Max widening factor
```

**Metrics Logged:**
- `toxicity_score`: Composite adverse selection score [0, 1]
- `directional_imbalance`: % of fills in dominant direction
- `fill_velocity`: Fills per minute
- `flow_spread_multiplier`: Current spread multiplier [1.0, 3.0]

**Testing Phase 1:**
```bash
# Run unit tests
GOTOOLCHAIN=auto go test ./internal/strategy/... -v -run TestFlowTracker

# Build and run in dry-run
go build -o bot cmd/bot/main.go
./bot  # Monitor logs for toxicity_score metrics
```

### Future Enhancements (Planned)

**Phase 2: Order Flow Analytics** (Not Yet Implemented)
- Fill clustering detection (burst patterns)
- Sweep pattern recognition (large aggressive orders)
- Orderbook imbalance analysis (bid/ask depth ratio)
- Asymmetric spread adjustments based on flow pressure

**Phase 3: Resolution Proximity Management** (Not Yet Implemented)
- Time-to-resolution tracking
- Progressive risk reduction as markets approach expiry
- Forced inventory flattening in final hours
- Emergency stop quoting <30min before resolution

## Risk Management

### Hard Limits
- **Position Limits**: Per-market and global exposure caps
- **Kill Switch**: Auto-cancels all orders on rapid price moves (15% default)
- **Daily Loss Limit**: Stops trading after hitting daily loss threshold
- **Cooldown Period**: Enforced pause after kill switch activation
- **Stale Book Detection**: Cancels quotes if orderbook data becomes stale

### Adaptive Protection (Phase 1)
- **Toxic Flow Detection**: Automatic spread widening when adversely selected
- **Cooldown Protection**: Maintains wider spreads after toxic periods
- **Fill Pattern Analysis**: Tracks directional imbalance and velocity

## Project Structure

```
.
├── cmd/bot/                # Main application
├── configs/                # YAML configuration
├── data/                   # Position files (gitignored)
├── internal/
│   ├── config/            # Config loading & validation
│   ├── dashboard/         # Web UI
│   ├── engine/            # Trading engine orchestration
│   ├── exchange/          # Polymarket API client
│   ├── market/            # Orderbook & market data
│   ├── risk/              # Risk management
│   ├── scanner/           # Market discovery
│   ├── store/             # Persistence layer
│   └── strategy/          # Avellaneda-Stoikov
├── pkg/types/             # Shared types
├── scripts/               # Helper scripts
└── web/                   # Dashboard static files
```

## Development

### Running Tests

```bash
# All tests
GOTOOLCHAIN=auto go test ./...

# Strategy tests (includes flow detection)
GOTOOLCHAIN=auto go test ./internal/strategy/... -v

# Specific feature tests
GOTOOLCHAIN=auto go test ./internal/strategy/... -v -run TestFlowTracker
```

### Testing Credentials

```bash
./scripts/test_credentials.sh
```

### Linting

```bash
golangci-lint run
```

### Building

```bash
# Standard build
go build -o bot cmd/bot/main.go

# Build with Go 1.24 auto-download
GOTOOLCHAIN=auto go build -o bot cmd/bot/main.go
```

## Security Notes

- **Never commit private keys** - use environment variables only
- **Keep API credentials secure** - they provide full account access
- **Use dry run mode** for testing strategies
- **Start with small position sizes** when going live
- **Monitor the dashboard** during live trading

## License

MIT

## Disclaimer

This software is for educational purposes. Use at your own risk. Cryptocurrency trading involves substantial risk of loss. The authors are not responsible for any financial losses incurred while using this software.

## Contributing

Pull requests welcome! Please ensure:
- Code passes `go fmt` and `golangci-lint`
- Add tests for new features
- Update documentation as needed
