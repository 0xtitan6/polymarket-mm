#!/bin/bash
# Test Polymarket API Credentials
# This script helps you verify your setup before running the bot

echo "🔍 Polymarket Credentials Test"
echo "================================"
echo ""

# Check if private key is set
if [ -z "$POLY_PRIVATE_KEY" ]; then
    echo "❌ POLY_PRIVATE_KEY not set"
    echo ""
    echo "To set it, run:"
    echo "  export POLY_PRIVATE_KEY='0xYOUR_PRIVATE_KEY_HERE'"
    echo ""
else
    echo "✅ POLY_PRIVATE_KEY is set"
    # Don't print the actual key, just confirm it's there
    KEY_LEN=${#POLY_PRIVATE_KEY}
    echo "   Length: $KEY_LEN characters"
fi

echo ""

# Check funder address in config
echo "📋 Checking config.yaml..."
FUNDER=$(grep "funder_address:" configs/config.yaml | awk '{print $2}' | tr -d '"')

if [ -z "$FUNDER" ] || [ "$FUNDER" = '""' ]; then
    echo "❌ funder_address not set in configs/config.yaml"
    echo ""
    echo "Edit configs/config.yaml and set:"
    echo "  funder_address: \"0xYOUR_WALLET_ADDRESS\""
    echo ""
else
    echo "✅ funder_address is set: $FUNDER"
fi

echo ""

# Check signature type
SIG_TYPE=$(grep "signature_type:" configs/config.yaml | awk '{print $2}')
echo "📝 Signature type: $SIG_TYPE"
case $SIG_TYPE in
    0)
        echo "   → EOA (Standard wallet like MetaMask)"
        ;;
    1)
        echo "   → POLY_PROXY (Email/Magic wallet)"
        ;;
    2)
        echo "   → GNOSIS_SAFE (Multi-sig wallet)"
        ;;
    *)
        echo "   → Unknown type"
        ;;
esac

echo ""
echo "================================"
echo ""

if [ -n "$POLY_PRIVATE_KEY" ] && [ -n "$FUNDER" ] && [ "$FUNDER" != '""' ]; then
    echo "✅ All credentials configured!"
    echo ""
    echo "You're ready to run the bot:"
    echo "  ./bin/bot"
    echo ""
    echo "Dashboard will be available at:"
    echo "  http://localhost:8080"
else
    echo "⚠️  Missing credentials - see instructions above"
fi
