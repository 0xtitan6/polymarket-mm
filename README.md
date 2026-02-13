# Polymarket Market Maker

A Go-based automated market-making bot for Polymarket prediction markets using the Avellaneda-Stoikov algorithm.

## Features

- **Avellaneda-Stoikov Strategy**: Dynamic spread pricing based on inventory and risk
- **Real-time Market Data**: WebSocket feeds for orderbook and user events
- **Risk Management**: Position limits, kill switches, daily loss limits
- **Market Scanner**: Automatically discovers and monitors active markets
- **Dashboard**: Web-based monitoring interface on port 8080
- **Dry Run Mode**: Test strategies without placing real orders

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

## Strategy: Avellaneda-Stoikov

The bot uses the Avellaneda-Stoikov market-making algorithm:

- **Dynamic Spreads**: Adjusts bid/ask spreads based on inventory risk
- **Inventory Management**: Skews quotes to mean-revert position to zero
- **Risk Aversion**: Parameter `gamma` controls how aggressively to reduce inventory
- **Volatility-Aware**: Uses `sigma` to estimate fair spread based on market volatility

### Key Parameters

- `gamma`: Risk aversion (0.05-0.3 typical range)
- `sigma`: Annualized volatility (0.3-0.8 for prediction markets)
- `k`: Order arrival intensity
- `T`: Time horizon in years (~0.00274 = 1 day)

## Risk Management

- **Position Limits**: Per-market and global exposure caps
- **Kill Switch**: Auto-cancels all orders on rapid price moves
- **Daily Loss Limit**: Stops trading after hitting daily loss threshold
- **Cooldown Period**: Enforced pause after kill switch activation
- **Stale Book Detection**: Cancels quotes if orderbook data becomes stale

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

### Testing Credentials

```bash
./scripts/test_credentials.sh
```

### Linting

```bash
golangci-lint run
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
