# Polymarket US Market Maker

A Go-based automated market-making bot for **Polymarket US** (`api.polymarket.us`) prediction markets using the Avellaneda-Stoikov algorithm with Ed25519 authentication.

> **Note**: This bot targets the **Polymarket US API** — not the international CLOB API. It uses Ed25519 key-pair authentication instead of HMAC/EIP-712. If you're looking for the international version, check the `main` branch history.

## Features

### Core Strategy
- **Avellaneda-Stoikov Algorithm**: Dynamic spread pricing based on inventory and risk
- **Real-time Market Data**: WebSocket feeds for orderbook and private order/fill events
- **Market Scanner**: Automatically discovers and filters active markets by keyword, slug, or spread
- **Game-Day Focus**: Configurable for sports betting markets (NBA, NHL, CBB, etc.)
- **Dashboard**: Web-based monitoring interface on port 8080
- **Dry Run Mode**: Test strategies without placing real orders

### Instant Fill Detection
- **REST Response Fills**: Detects fills returned directly in the PlaceOrder REST response (critical for US API)
- **WebSocket Fills**: Real-time fill detection via private WebSocket feed
- **Duplicate Prevention**: Deduplicates fills seen in both REST and WS channels

### Toxic Flow Detection
- **Directional Imbalance Tracking**: Detects when fills are consistently one-sided
- **Fill Velocity Analysis**: Identifies burst patterns suggesting sweeps
- **Adaptive Spread Widening**: Automatically widens spreads 1.0x-2.5x when toxicity detected
- **Cooldown Protection**: Maintains wider spreads for 2 minutes after toxic flow
- **Minimum Fill Threshold**: Requires 3+ fills before making toxicity judgments (avoids false positives)

### Risk Management
- **Position Limits**: Per-market and global exposure caps
- **Kill Switch**: Auto-cancels all orders on rapid price moves (30% default)
- **Daily Loss Limit**: Stops trading after hitting daily loss threshold
- **Cooldown Period**: Enforced pause after kill switch activation
- **Stale Book Detection**: Falls back to REST when WebSocket data is stale

## Architecture

```
cmd/
├── bot/main.go              # Main entry point
├── cancel_all/              # Bulk cancel open orders
├── check_orders/            # Inspect open order state
├── raw_check/               # Raw API connectivity test
├── scan_test/               # Market scanner test
└── today_markets/           # Find today's game-day markets

internal/
├── config/                  # Config loading & validation
├── dashboard/               # Web UI
├── engine/                  # Trading engine orchestration
├── exchange/                # Polymarket US API client (Ed25519 auth)
├── market/                  # Market scanner & filtering
├── store/                   # JSON-based position persistence
└── strategy/                # Avellaneda-Stoikov + flow detection

pkg/types/                   # Shared types (US API models)
```

## Prerequisites

- Go 1.24+
- Polymarket US account ([polymarket.us](https://polymarket.us))
- API key pair from [polymarket.us/developer](https://polymarket.us/developer) (Ed25519)
- USDC for trading

## Installation

```bash
git clone https://github.com/0xtitan6/polymarket-mm.git
cd polymarket-mm
go mod download
```

## Configuration

### 1. Environment Variables

Set your Polymarket US API credentials (never commit these):

```bash
export POLY_API_KEY_ID='your-uuid-api-key-id'        # UUID from polymarket.us/developer
export POLY_PRIVATE_KEY_B64='your-base64-private-key' # Base64-encoded Ed25519 private key
```

### 2. Update Config File

Edit `configs/config.yaml`:

```yaml
dry_run: true  # set to false for live trading

auth:
  api_key_id: ""          # set via POLY_API_KEY_ID env
  private_key_b64: ""     # set via POLY_PRIVATE_KEY_B64 env

api:
  base_url: "https://api.polymarket.us"
  ws_market_url: "wss://api.polymarket.us/v1/ws/markets"
  ws_private_url: "wss://api.polymarket.us/v1/ws/private"

strategy:
  gamma: 0.20              # Risk aversion (higher = tighter inventory control)
  sigma: 0.6               # Volatility estimate
  k: 1.5                   # Order arrival intensity
  t: 0.00137               # Time horizon (~0.5 day)
  default_spread_bps: 100  # 1% minimum spread
  order_size_usd: 3.0      # Quote size per side in USDC
  refresh_interval: 10s    # How often to re-quote

  # Toxic flow detection
  flow_window: 60s
  flow_toxicity_threshold: 0.5
  flow_cooldown_period: 120s
  flow_max_spread_multiplier: 2.5

risk:
  max_position_per_market: 3.0
  max_global_exposure: 10.0
  kill_switch_drop_pct: 0.30
  max_daily_loss: 3.0

scanner:
  poll_interval: 60s
  max_end_date_days: 30
  include_keywords: []     # e.g. ["aec-nba-det-orl-2026-03-01"]
  exclude_keywords: []
```

### 3. Market Targeting

Filter markets using the scanner config:

- `scanner.include_slugs`: exact market slugs to allow
- `scanner.include_keywords`: text match on market slug (case-insensitive)
- `scanner.exclude_keywords`: text-based denylist
- `scanner.max_end_date_days`: only markets expiring within N days

For game-day trading, use keywords like `"aec-nba-"` or specific game slugs like `"aec-nba-det-orl-2026-03-01"`.

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

### Utilities

```bash
# Cancel all open orders
go run cmd/cancel_all/main.go

# Check open orders
go run cmd/check_orders/main.go

# Test API connectivity
go run cmd/raw_check/main.go

# Test market scanner
go run cmd/scan_test/main.go

# Find today's markets
go run cmd/today_markets/main.go
```

### Dashboard

Access the web dashboard at `http://localhost:8080` to monitor:
- Active positions and P&L
- Order flow and fill history
- Risk metrics

## Strategy: Avellaneda-Stoikov

The bot uses the Avellaneda-Stoikov market-making algorithm:

- **Dynamic Spreads**: Adjusts bid/ask spreads based on inventory risk
- **Inventory Management**: Skews quotes to mean-revert position to zero
- **Risk Aversion**: Parameter `gamma` controls how aggressively to reduce inventory
- **Volatility-Aware**: Uses `sigma` to estimate fair spread

**Key Parameters:**
- `gamma`: Risk aversion (0.05-0.3 typical range)
- `sigma`: Annualized volatility (0.3-0.8 for prediction markets)
- `k`: Order arrival intensity
- `T`: Time horizon in years (~0.00137 = 0.5 day for game-day)

## US API Differences

Key differences from the international Polymarket CLOB API:

| Feature | International | US |
|---------|--------------|-----|
| Auth | HMAC + EIP-712 signing | Ed25519 key pair |
| Base URL | `clob.polymarket.com` | `api.polymarket.us` |
| WebSocket | Separate market/user feeds | `/v1/ws/markets` + `/v1/ws/private` |
| Order placement | POST with CLOB signature | POST with Ed25519 signed headers |
| Cancel | DELETE per order | Bulk cancel via POST `/v1/orders/open/cancel` |
| Fills | WebSocket only | REST response + WebSocket (must handle both) |

## Development

### Running Tests

```bash
# All tests
go test ./...

# Strategy tests (includes flow detection + negative tests)
go test ./internal/strategy/... -v

# Exchange client tests
go test ./internal/exchange/... -v

# Negative tests only (covers known bug patterns)
go test ./internal/strategy/... -v -run TestNeg
go test ./internal/exchange/... -v -run TestNeg
```

### Test Coverage

The test suite includes **37 negative tests** covering known production bug patterns:
1. **Side string mismatch**: Raw `ORDER_INTENT_BUY_LONG` vs normalized side
2. **Unknown order tracking**: Fills on orders not in activeOrders map
3. **FlowTracker sensitivity**: Single-fill false positives
4. **Instant fill handling**: Fills in PlaceOrder REST response

### Building

```bash
go build -o bot cmd/bot/main.go
```

## Security Notes

- **Never commit API keys** — use environment variables (`POLY_API_KEY_ID`, `POLY_PRIVATE_KEY_B64`)
- **dry_run defaults to true** — explicitly set `false` for live trading
- **Start with small position sizes** when going live
- **Monitor the dashboard** during live trading
- Config file contains empty credential placeholders by design

## License

MIT

## Disclaimer

This software is for educational purposes. Use at your own risk. Prediction market trading involves substantial risk of loss. The authors are not responsible for any financial losses incurred while using this software.

## Contributing

Pull requests welcome! Please ensure:
- Code passes `go fmt` and `go vet`
- Add tests for new features (including negative tests for edge cases)
- Update documentation as needed
