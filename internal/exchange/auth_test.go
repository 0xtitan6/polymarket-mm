package exchange

import (
	"math"
	"math/big"
	"testing"

	"polymarket-mm/pkg/types"
)

func TestRoundDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		val      float64
		decimals int
		want     float64
	}{
		{"truncate 2 decimals", 1.2345, 2, 1.23},
		{"truncate 4 decimals", 0.55559, 4, 0.5555},
		{"exact value unchanged", 0.55, 2, 0.55},
		{"zero", 0.0, 2, 0.0},
		{"negative truncates toward zero", -1.239, 2, -1.23},
		{"high precision", 0.123456789, 6, 0.123456},
		{"whole number", 5.0, 2, 5.0},
		{"zero decimals", 3.99, 0, 3.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := roundDown(tt.val, tt.decimals)
			if math.Abs(got-tt.want) > 1e-10 {
				t.Errorf("roundDown(%v, %d) = %v, want %v", tt.val, tt.decimals, got, tt.want)
			}
		})
	}
}

func TestPriceToAmounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		price    float64
		size     float64
		side     types.Side
		tickSize types.TickSize
		wantMkr  int64 // expected makerAmount (6 decimal USDC)
		wantTkr  int64 // expected takerAmount (6 decimal USDC)
	}{
		{
			name:     "BUY at 0.50, size 100",
			price:    0.50,
			size:     100.0,
			side:     types.BUY,
			tickSize: types.Tick001,
			wantMkr:  50_000_000,  // 100 * 0.50 = 50 USDC
			wantTkr:  100_000_000, // 100 tokens
		},
		{
			name:     "SELL at 0.50, size 100",
			price:    0.50,
			size:     100.0,
			side:     types.SELL,
			tickSize: types.Tick001,
			wantMkr:  100_000_000, // 100 tokens
			wantTkr:  50_000_000,  // 100 * 0.50 = 50 USDC
		},
		{
			name:     "BUY at 0.75, size 10",
			price:    0.75,
			size:     10.0,
			side:     types.BUY,
			tickSize: types.Tick001,
			wantMkr:  7_500_000,  // 10 * 0.75 = 7.5 USDC
			wantTkr:  10_000_000, // 10 tokens
		},
		{
			name:     "BUY small size truncated",
			price:    0.55,
			size:     1.999, // truncated to 1.99
			side:     types.BUY,
			tickSize: types.Tick001,
			wantMkr:  1_094_500, // roundDown(1.99 * 0.55, 4) = 1.0945 → 1094500
			wantTkr:  1_990_000, // 1.99 tokens
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mkr, tkr := PriceToAmounts(tt.price, tt.size, tt.side, tt.tickSize)

			if mkr.Cmp(big.NewInt(tt.wantMkr)) != 0 {
				t.Errorf("makerAmount = %s, want %d", mkr.String(), tt.wantMkr)
			}
			if tkr.Cmp(big.NewInt(tt.wantTkr)) != 0 {
				t.Errorf("takerAmount = %s, want %d", tkr.String(), tt.wantTkr)
			}
		})
	}
}

func TestPriceToAmountsSellMirrorsBuy(t *testing.T) {
	t.Parallel()

	// For the same price/size, BUY's maker == SELL's taker (tokens)
	// and BUY's taker == SELL's maker (USDC)
	buyMkr, buyTkr := PriceToAmounts(0.60, 50.0, types.BUY, types.Tick001)
	sellMkr, sellTkr := PriceToAmounts(0.60, 50.0, types.SELL, types.Tick001)

	if buyMkr.Cmp(sellTkr) != 0 {
		t.Errorf("BUY maker (%s) != SELL taker (%s)", buyMkr, sellTkr)
	}
	if buyTkr.Cmp(sellMkr) != 0 {
		t.Errorf("BUY taker (%s) != SELL maker (%s)", buyTkr, sellMkr)
	}
}
