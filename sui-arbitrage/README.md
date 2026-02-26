# Sui Arbitrage Bot

Automated cross-DEX arbitrage bot for the Sui blockchain. Monitors price discrepancies across Cetus, Turbos Finance, and DeepBook, then executes atomic multi-leg swaps to capture profit.

## How It Works

```
┌─────────────┐     ┌──────────────┐     ┌───────────────┐
│ Pool Scanner │────▶│  Price Feed   │────▶│  Arb Detector │
│  (discover)  │     │  (monitor)    │     │  (analyze)    │
└─────────────┘     └──────────────┘     └───────┬───────┘
                                                  │
                                          profitable?
                                                  │
                                         ┌────────▼────────┐
                                         │   Arb Executor   │
                                         │  (atomic swap)   │
                                         └─────────────────┘
```

1. **Pool Scanner** discovers tradeable pools across all enabled DEXes
2. **Price Feed** continuously fetches quotes for each token pair on every DEX
3. **Arb Detector** compares prices to find profitable routes (direct + triangular)
4. **Arb Executor** builds and submits atomic Sui transactions — all legs succeed or all revert

### Arbitrage Strategies

| Strategy | Description |
|----------|-------------|
| **Direct** | Buy on DEX A, sell on DEX B for the same pair |
| **Triangular** | Route through 3 tokens across DEXes: A→B→C→A |

## Supported DEXes

- **Cetus** — Concentrated liquidity AMM (CLMM)
- **Turbos Finance** — Concentrated liquidity AMM
- **DeepBook** — Sui-native on-chain order book (CLOB)

## Quick Start

### Prerequisites

- Node.js >= 20
- A Sui wallet with some SUI for gas

### Install

```bash
cd sui-arbitrage
npm install
```

### Configure

```bash
cp .env.example .env
```

Edit `.env` with your settings:

```env
SUI_PRIVATE_KEY=suiprivkey1...      # Your Sui private key (bech32)
SUI_NETWORK=mainnet
DRY_RUN=true                        # Start with dry-run!
MIN_PROFIT_BPS=30                   # Minimum 0.3% profit to trade
MAX_TRADE_SIZE_SUI=100              # Max input per trade
SCAN_INTERVAL_MS=1000               # How often to scan (ms)
```

### Run

```bash
# Development (with hot reload)
npm run dev

# Production
npm run build && npm start
```

## Architecture

```
src/
├── index.ts                 # Entry point & orchestrator
├── config.ts                # Environment-based configuration
├── types.ts                 # Shared type definitions
├── dex/
│   ├── base.ts              # DEX registry & well-known tokens
│   ├── cetus.ts             # Cetus CLMM integration
│   ├── turbos.ts            # Turbos Finance integration
│   └── deepbook.ts          # DeepBook V3 order book integration
├── arbitrage/
│   ├── detector.ts          # Opportunity detection (direct + triangular)
│   ├── executor.ts          # Atomic transaction execution
│   └── calculator.ts        # Optimal sizing & opportunity scoring
├── monitor/
│   ├── price-feed.ts        # Cross-DEX price monitoring
│   └── pool-scanner.ts      # Pool discovery & indexing
└── utils/
    ├── logger.ts            # Structured logging (pino)
    ├── sui-client.ts        # Sui SDK wrapper
    └── math.ts              # Decimal math utilities
```

## Configuration Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `SUI_RPC_URL` | mainnet fullnode | Sui JSON-RPC endpoint |
| `SUI_NETWORK` | `mainnet` | Network: mainnet, testnet, devnet |
| `SUI_PRIVATE_KEY` | — | Bech32-encoded private key |
| `DRY_RUN` | `true` | Simulate trades without submitting |
| `MIN_PROFIT_BPS` | `30` | Minimum profit (basis points) to execute |
| `MAX_TRADE_SIZE_SUI` | `100` | Maximum input per trade in SUI |
| `GAS_BUDGET` | `50000000` | Gas budget per transaction (MIST) |
| `SCAN_INTERVAL_MS` | `1000` | Price scan interval in milliseconds |
| `CETUS_ENABLED` | `true` | Enable Cetus DEX |
| `TURBOS_ENABLED` | `true` | Enable Turbos DEX |
| `DEEPBOOK_ENABLED` | `true` | Enable DeepBook DEX |

## Testing

```bash
npm test
```

## Safety

- **Always start with `DRY_RUN=true`** to verify the bot detects real opportunities before risking funds
- Atomic transactions ensure no partial fills — if any leg fails, the entire trade reverts
- Never commit your `.env` file or expose your private key
- The bot includes a staleness check — opportunities older than 5 seconds are rejected
- Slippage tolerance defaults to 0.5% (configurable)

## License

MIT
