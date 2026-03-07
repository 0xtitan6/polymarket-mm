#!/bin/bash
# Synthesis MM Bot — Quick Start Script
# 
# This script:
#   1. Tests your API connection
#   2. Lists your open orders (to find market token_ids)
#   3. Lists your positions
#   4. Starts the bot in dry-run mode
#
# Usage:
#   export SYNTH_API_KEY="sk_your_key_here"
#   export SYNTH_WALLET_ID="019c3399-2f8d-7462-a6ef-07fa6ad9399b"
#   bash run_synthesis_bot.sh

set -e

API_BASE="https://synthesis.trade/api/v1"

# ─── Validate env vars ───────────────────────────────────────────────
if [ -z "$SYNTH_API_KEY" ]; then
    echo "ERROR: SYNTH_API_KEY is not set"
    echo "  export SYNTH_API_KEY=\"sk_your_key_here\""
    exit 1
fi

if [ -z "$SYNTH_WALLET_ID" ]; then
    echo "ERROR: SYNTH_WALLET_ID is not set"
    echo "  export SYNTH_WALLET_ID=\"your-wallet-id\""
    exit 1
fi

echo "=== Synthesis MM Bot — Pre-flight Check ==="
echo ""
echo "API Key:   ${SYNTH_API_KEY:0:8}..."
echo "Wallet ID: $SYNTH_WALLET_ID"
echo ""

# ─── Step 1: Test connection (Get Wallets) ────────────────────────────
echo "--- Step 1: Testing API connection (Get Wallets) ---"
WALLETS=$(curl -s -X GET "$API_BASE/wallet" \
    -H "X-API-KEY: $SYNTH_API_KEY")

SUCCESS=$(echo "$WALLETS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success', False))" 2>/dev/null || echo "false")

if [ "$SUCCESS" != "True" ]; then
    echo "FAILED: API returned: $WALLETS"
    exit 1
fi
echo "OK — Connected to Synthesis API"
echo "Wallets: $(echo "$WALLETS" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for w in data.get('response', []):
    print(f\"  - {w.get('name', 'unnamed')} ({w.get('wallet_id', '?')})\")
" 2>/dev/null || echo "$WALLETS")"
echo ""

# ─── Step 2: Get Orders ───────────────────────────────────────────────
echo "--- Step 2: Checking Open Orders ---"
ORDERS=$(curl -s -X GET "$API_BASE/wallet/$SYNTH_WALLET_ID/orders" \
    -H "X-API-KEY: $SYNTH_API_KEY")

echo "Orders response:"
echo "$ORDERS" | python3 -c "
import sys, json
data = json.load(sys.stdin)
orders = data.get('response', [])
if not orders:
    print('  No open orders found.')
else:
    print(f'  Found {len(orders)} order(s):')
    token_ids = set()
    for o in orders:
        tid = o.get('token_id', '?')
        token_ids.add(tid)
        print(f\"    {o.get('side','?')} {o.get('amount','?')} @ {o.get('price','?')} — {o.get('status','?')} — token: {tid[:40]}...\")
    print()
    print('  Unique token_ids for include_slugs config:')
    for t in token_ids:
        print(f'    - \"{t}\"')
" 2>/dev/null || echo "$ORDERS"
echo ""

# ─── Step 3: Get Positions ────────────────────────────────────────────
echo "--- Step 3: Checking Positions ---"
POSITIONS=$(curl -s -X GET "$API_BASE/wallet/$SYNTH_WALLET_ID/positions" \
    -H "X-API-KEY: $SYNTH_API_KEY")

echo "Positions response:"
echo "$POSITIONS" | python3 -c "
import sys, json
data = json.load(sys.stdin)
positions = data.get('response', [])
if not positions:
    print('  No positions found.')
else:
    print(f'  Found {len(positions)} position(s):')
    for p in positions:
        print(f\"    {p.get('venue','?')} | token: {p.get('token_id','?')[:40]}... | size: {p.get('size','?')} | avg_price: {p.get('avg_price','?')} | pnl: {p.get('pnl','?')}\")
" 2>/dev/null || echo "$POSITIONS"
echo ""

# ─── Step 4: Start bot in dry-run mode ─────────────────────────────
echo "--- Step 4: Starting Synthesis Bot (DRY-RUN) ---"
echo ""
echo "The bot will:"
echo "  - Poll your orders every 5s to build the order book"
echo "  - Calculate Avellaneda-Stoikov quotes"
echo "  - LOG what it would do (no real orders in dry-run)"
echo "  - Dashboard on http://localhost:8081"
echo ""
echo "Press Ctrl+C to stop."
echo ""

cd "$(dirname "$0")"
export SYNTH_DRY_RUN=true
exec go run ./cmd/synthesis-bot/...
