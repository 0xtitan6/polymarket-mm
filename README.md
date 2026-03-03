# Polymarket US Market Maker

A Go-based automated market-making bot for **Polymarket US** (`api.polymarket.us`) prediction markets using the Avellaneda-Stoikov algorithm with Ed25519 authentication.

> **Note**: This bot targets the **Polymarket US API** (`api.polymarket.us`) — not the international CLOB API. It uses Ed25519 key-pair authentication instead of HMAC/EIP-712. If you're looking for the international version, check the `main` branch history.
>
> **Primary targets**: US sports markets on Polymarket US — specifically **NBA** and **NHL** game-day markets (slugs matching `aec-nba-*` and `aec-nhl-*`).

## Features

### Core Strategy
- **Avellaneda-Stoikov Algorithm**: Dynamic spread pricing based on inventory and risk
- **Real-time Market Data**: WebSocket feeds for orderbook and private order/fill events
- **Market Scanner**: Automatically discovers and filters active markets by keyword, slug, or spread
- **Game-Day Focus**: Targets NBA and NHL game-day markets on Polymarket US
- **Dashboard**: Web-based monitoring interface on port 8080
- **Dry Run Mode**: Test strategies without placing real orders (`dry_run: true` is the default in repo config)

### v4 Features (Current)

#### Inverse Depth Scoring
The scanner now ranks markets using **inverse depth scoring**:

```
score = spread / (avgDepth + 1)
```

Markets with wider spreads and **thinner** order books rank highest. This is the opposite of the old scoring (spread × depth), which favored whale-dominated markets where small 1–5 token orders were invisible. Now the bot targets markets where it can actually compete near the top of the book.

#### BBO Matching
When the best bid or ask has fewer than `bbo_match_max_depth` tokens at the top of book, the bot **snaps quotes to the BBO price** to get queue priority. Instead of quoting one tick behind and never getting filled, the bot steps in at the best price when the book is thin.

- Configurable via `strategy.bbo_match_max_depth` (default: 200 tokens)
- Only activates when both the depth condition and minimum spread floor are satisfied

#### Hysteresis (Sticky Markets)
Markets the bot is currently trading receive a `sticky_bonus` boost to their opportunity score, preventing premature rotation. Combined with a longer `poll_interval` (180s vs 60s), this keeps the bot on profitable markets longer and reduces cancel/replace churn.

- Configurable via `scanner.sticky_bonus` (default: 1.0)

#### Thin-Market Targeting
The `max_top_of_book_depth` filter skips markets where the BBO has more tokens than the bot can realistically compete with. Only markets with thin books (shallow top-of-book depth) pass this filter.

- Configurable via `scanner.max_top_of_book_depth` (default: 1000 tokens)
- Set to 0 to disable

#### Duplicate Fill Fix
A critical bug in `handleFillFromOrder` was removed: when a WS fill event delivered a cumulative quantity equal to what was already tracked (delta = 0), the old fallback logic would re-apply the full cumQty as a new fill, double-counting. The fix returns early when `fillSize <= 0`.

### Instant Fill Detection
- **REST Response Fills**: Detects fills returned directly in the PlaceOrder REST response (critical for US API)
- **WebSocket Fills**: Real-time fill detection via private WebSocket feed
- **Duplicate Prevention**: CumQty delta logic prevents double-counting fills seen in both REST and WS channels

### Toxic Flow Detection
- **Directional Imbalance Tracking**: Detects when fills are consistently one-sided
- **Fill Velocity Analysis**: Identifies burst patterns suggesting sweeps
- **Adaptive Spread Widening**: Automatically widens spreads 1.0x–2.5x when toxicity detected
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
├── market/                  # Market scanner & filtering (v4: inverse depth scoring)
├── risk/                    # Risk manager & kill switch
├── store/                   # JSON-based position persistence
└── strategy/                # Avellaneda-Stoikov + BBO matching + flow detection

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

The bot reads `POLY_API_KEY_ID` and `POLY_PRIVATE_KEY_B64` at startup and overrides any values in `config.yaml`. The config file always has empty credential placeholders (`api_key_id: ""`, `private_key_b64: ""`).

To enable live trading via env:
```bash
export POLY_DRY_RUN=false  # or set dry_run: false in config.yaml
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
  gamma: 0.40              # Risk aversion
  sigma: 0.6               # Base volatility estimate
  k: 1.5                   # Order arrival intensity
  t: 0.00137               # Time horizon (~0.5 day for game-day)
  default_spread_bps: 300  # 3% minimum spread (must clear 2% winner fee)
  order_size_usd: 1.0      # Quote size per side in USDC

  # v4: BBO matching
  bbo_match_max_depth: 200  # Match BBO when top-of-book has < 200 tokens

  # Toxic flow detection
  flow_window: 60s
  flow_toxicity_threshold: 0.5
  flow_cooldown_period: 120s
  flow_max_spread_multiplier: 2.5

risk:
  max_position_per_market: 1.0
  max_global_exposure: 2.0
  max_markets_active: 3
  kill_switch_drop_pct: 0.30
  max_daily_loss: 0.50

scanner:
  poll_interval: 180s
  min_spread: 0.005
  max_end_date_days: 30
  include_keywords: ["aec-nba", "aec-nhl"]  # NBA and NHL game-day markets

  # v4: Thin-market targeting
  max_top_of_book_depth: 1000  # Skip markets with > 1000 tokens at BBO
  sticky_bonus: 1.0            # Hysteresis: active markets get +1.0 score
```

### 3. Market Targeting

Filter markets using the scanner config:

- `scanner.include_slugs`: exact market slugs to allow
- `scanner.include_keywords`: text match on market slug/question (case-insensitive)
- `scanner.exclude_keywords`: text-based denylist
- `scanner.max_end_date_days`: only markets expiring within N days

For NBA/NHL game-day trading, use keywords `"aec-nba"` and `"aec-nhl"`. For specific games:
```yaml
include_keywords: ["aec-nba-det-orl-2026-03-01"]
```

## Usage

### Build

```bash
go build -o bot cmd/bot/main.go
```

### Run

```bash
# Dry run mode (no real orders — default in repo config)
./bot

# Live trading — set dry_run: false in config.yaml OR:
POLY_DRY_RUN=false ./bot
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
- **Volatility-Aware**: Uses `sigma` to estimate fair spread; realized vol overlay widens quotes during turbulent periods

**Key Parameters:**
- `gamma`: Risk aversion (0.2–0.4 typical range for sports markets)
- `sigma`: Base volatility (0.3–0.8 for prediction markets)
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
| Market ID | ConditionID (hex) | Market slug (human-readable) |

## Development

### Running Tests

```bash
# All tests
go test ./...

# Strategy tests (includes flow detection + negative tests)
go test ./internal/strategy/... -v

# Market scanner tests
go test ./internal/market/... -v

# Risk manager tests
go test ./internal/risk/... -v

# Negative tests only (covers known bug patterns)
go test ./internal/strategy/... -v -run TestBug
go test ./internal/strategy/... -v -run TestNeg
go test ./internal/market/... -v -run TestNeg
go test ./internal/risk/... -v -run TestNeg
```

### Test Coverage

The test suite includes comprehensive **negative tests** covering known production bug patterns:

1. **Bug #1 — Instant fill discarded**: Orders that cross the spread fill immediately in the PlaceOrder REST response. The old code threw away `resp.Executions`. Fix: `processInstantFill()` processes REST-response fills immediately.
2. **Bug #2 — Duplicate fill double-counting**: WS reconnects can deliver the same fill event twice. Old fallback applied cumQty as new fill size. Fix: return early when `fillSize <= 0`.
3. **Bug #3 — Fill on unknown order**: WS disconnect means EXECUTION_TYPE_NEW may be missed. Fix: register and process fills on untracked orders, then add to tracking map.
4. **Bug #4 — Side string mismatch**: US API WS private feed sends `ORDER_INTENT_BUY_LONG` instead of `BUY`. Fix: `isBuySide()`/`isSellSide()` handle both formats.
5. **Scanner negative tests**: Inactive markets, closed markets, keyword filtering, end-date filtering, hysteresis.
6. **Book negative tests**: `BestBidAskWithDepth` on empty/partial books.
7. **Risk manager negative tests**: Per-market exposure breach, global exposure breach, daily loss kill, cooldown behavior, price movement kill switch.

### Building

```bash
go build -o bot cmd/bot/main.go
```

## Security Notes

- **Never commit API keys** — use environment variables (`POLY_API_KEY_ID`, `POLY_PRIVATE_KEY_B64`)
- **`dry_run` defaults to `true`** in the repo config — explicitly set `false` for live trading
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
