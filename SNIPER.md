# 5M Crypto Sniper Bot

Directional betting bot for Polymarket's 5-minute Up/Down crypto markets. Exploits the 13-27 second latency gap between Coinbase spot prices and Chainlink Data Streams.

## How It Works

```
CoinbaseFeed (500ms poll) → SniperStrategy → SynthesisClient → Polymarket
```

1. Polls Coinbase REST API every 500ms for BTC, ETH, SOL, XRP prices
2. At the start of each 5-minute window, records the opening price
3. Between 2-4.5 minutes in, checks if price moved >0.05% from open
4. If yes, buys the corresponding Up/Down token via Synthesis Trade API
5. Market resolves based on Chainlink — which lags Coinbase by 13-27 seconds

## Prerequisites

- **Go 1.24+**
- **Synthesis Trade account** — [synthesis.trade](https://synthesis.trade)
  - API key (`sk_...`)
  - Wallet ID
  - USDC funded on Polygon

## Quick Start

```bash
# Clone
git clone https://github.com/0xtitan6/polymarket-mm.git
cd polymarket-mm

# Build
go build -o sniper ./cmd/sniper

# Set credentials
export SYNTH_API_KEY="sk_your_api_key_here"
export SYNTH_WALLET_ID="your-wallet-id-here"

# Dry run (no real orders)
SYNTH_DRY_RUN=true ./sniper

# Monitor only (watch prices + signals, never trade)
./sniper -monitor

# Live trading
./sniper
```

## Configuration

Edit `configs/sniper_config.yaml`:

```yaml
dry_run: false

auth:
  api_key: ""      # overridden by SYNTH_API_KEY env var
  wallet_id: ""    # overridden by SYNTH_WALLET_ID env var
  venue: "pol"

price_feed:
  poll_interval_ms: 500
  assets: [BTC, ETH, SOL, XRP]

strategy:
  earliest_entry_seconds: 120   # when to start looking for signals
  latest_entry_seconds: 270     # stop looking (30s before close)
  min_edge_pct: 0.0005          # 5bps minimum price move
  strong_edge_pct: 0.002        # 20bps = high confidence
  order_size_usd: 5.00          # bet size ($5 minimum on Synthesis)
  max_position_usd: 5.00        # max total exposure
  max_concurrent: 1             # simultaneous positions
  aggressive_bid_offset: 0.03   # bid 3c below fair value
```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `SYNTH_API_KEY` | Yes | Synthesis Trade API key |
| `SYNTH_WALLET_ID` | Yes | Synthesis wallet ID |
| `SYNTH_DRY_RUN` | No | Set `true` to disable real orders |

## Risk Controls

These are hardcoded from lessons learned (lost $144 paying 78-90c on coin flips):

- **60c hard cap** on all bids — never overpay (`strategy.go` line 294-296)
- **$5 per bet** — Synthesis minimum, also our maximum
- **1 concurrent position** — no stacking risk
- **5bps edge threshold** — only trade on real moves, not noise
- **Retry prevention** — if an order fails, that market window is skipped
- **Feed staleness guard** — skips trading if Coinbase data is >10s old

## Modes

### Live Trading
```bash
./sniper
```
Places real orders with real money. Make sure `dry_run: false` in config.

### Dry Run
```bash
SYNTH_DRY_RUN=true ./sniper
```
Emits signals and logs what it would trade, but no orders are placed.

### Monitor Only
```bash
./sniper -monitor
```
Watches prices and discovers markets. Logs every signal. Never places orders (even if `dry_run: false`).

## Running as a Service

For 24/7 operation on a VPS:

```bash
# Using systemd
sudo cat > /etc/systemd/system/sniper.service << EOF
[Unit]
Description=Polymarket 5M Sniper Bot
After=network.target

[Service]
Type=simple
User=deploy
Environment=SYNTH_API_KEY=sk_your_key
Environment=SYNTH_WALLET_ID=your_wallet_id
WorkingDirectory=/opt/polymarket-mm
ExecStart=/opt/polymarket-mm/sniper
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl enable sniper
sudo systemctl start sniper

# View logs
journalctl -u sniper -f
```

Or with a simple watchdog script:

```bash
#!/bin/bash
while true; do
    ./sniper 2>&1 | tee -a sniper.log
    echo "Bot exited, restarting in 5s..."
    sleep 5
done
```

## Project Structure

```
cmd/sniper/
  main.go              # entry point, config loading, order placement

internal/pricefeed/
  coinbase.go          # Coinbase REST price feed (500ms polling, staleness detection)

internal/sniper/
  discovery.go         # Gamma API market discovery (finds active 5M markets)
  strategy.go          # signal generation, edge calculation, order logic

configs/
  sniper_config.yaml   # strategy parameters
```

## API Reference

### Synthesis Trade API
- Base URL: `https://synthesis.trade/api/v1`
- Auth: `X-API-KEY` header
- Place order: `POST /wallet/{venue}/{wallet_id}/order`
  ```json
  {
    "token_id": "...",
    "side": "buy",
    "amount": "5.00",
    "type": "limit",
    "units": "USDC",
    "price": "0.57"
  }
  ```

### Gamma API (Market Discovery)
- Base URL: `https://gamma-api.polymarket.com`
- Requires `User-Agent` header
- Find market: `GET /events?slug={asset}-updown-5m-{epoch}`
- `clobTokenIds[0]` = Up token, `clobTokenIds[1]` = Down token

### Coinbase (Price Feed)
- `GET https://api.coinbase.com/v2/prices/{ASSET}-USD/spot`
- No auth needed, free, ~50ms latency
