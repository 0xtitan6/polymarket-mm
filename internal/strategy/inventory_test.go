package strategy

import (
	"math"
	"testing"

	"polymarket-mm/pkg/types"
)

const (
	yesToken = "yes-token"
	noToken  = "no-token"
	mktID    = "market-1"
)

func newTestInventory() *Inventory {
	return NewInventory(mktID, yesToken, noToken)
}

func TestOnFillBuyYes(t *testing.T) {
	t.Parallel()
	inv := newTestInventory()

	inv.OnFill(Fill{Side: types.BUY, TokenID: yesToken, Price: 0.50, Size: 10})

	pos := inv.Snapshot()
	if pos.YesQty != 10 {
		t.Errorf("YesQty = %v, want 10", pos.YesQty)
	}
	if pos.AvgEntryYes != 0.50 {
		t.Errorf("AvgEntryYes = %v, want 0.50", pos.AvgEntryYes)
	}
}

func TestOnFillBuyYesMultiple(t *testing.T) {
	t.Parallel()
	inv := newTestInventory()

	inv.OnFill(Fill{Side: types.BUY, TokenID: yesToken, Price: 0.50, Size: 10})
	inv.OnFill(Fill{Side: types.BUY, TokenID: yesToken, Price: 0.60, Size: 10})

	pos := inv.Snapshot()
	if pos.YesQty != 20 {
		t.Errorf("YesQty = %v, want 20", pos.YesQty)
	}
	// avg = (0.50*10 + 0.60*10) / 20 = 11/20 = 0.55
	if math.Abs(pos.AvgEntryYes-0.55) > 1e-10 {
		t.Errorf("AvgEntryYes = %v, want 0.55", pos.AvgEntryYes)
	}
}

func TestOnFillSellYes(t *testing.T) {
	t.Parallel()
	inv := newTestInventory()

	inv.OnFill(Fill{Side: types.BUY, TokenID: yesToken, Price: 0.50, Size: 10})
	inv.OnFill(Fill{Side: types.SELL, TokenID: yesToken, Price: 0.60, Size: 5})

	pos := inv.Snapshot()
	if pos.YesQty != 5 {
		t.Errorf("YesQty = %v, want 5", pos.YesQty)
	}
	// realized = (0.60 - 0.50) * 5 = 0.50
	if math.Abs(pos.RealizedPnL-0.50) > 1e-10 {
		t.Errorf("RealizedPnL = %v, want 0.50", pos.RealizedPnL)
	}
}

func TestOnFillSellAllYes(t *testing.T) {
	t.Parallel()
	inv := newTestInventory()

	inv.OnFill(Fill{Side: types.BUY, TokenID: yesToken, Price: 0.40, Size: 10})
	inv.OnFill(Fill{Side: types.SELL, TokenID: yesToken, Price: 0.50, Size: 10})

	pos := inv.Snapshot()
	if pos.YesQty != 0 {
		t.Errorf("YesQty = %v, want 0", pos.YesQty)
	}
	if pos.AvgEntryYes != 0 {
		t.Errorf("AvgEntryYes = %v, want 0 after full close", pos.AvgEntryYes)
	}
	if math.Abs(pos.RealizedPnL-1.0) > 1e-10 {
		t.Errorf("RealizedPnL = %v, want 1.0", pos.RealizedPnL)
	}
}

func TestNetDelta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yesQty  float64
		noQty   float64
		want    float64
	}{
		{"no position", 0, 0, 0},
		{"fully long YES", 10, 0, 1.0},
		{"fully long NO", 0, 10, -1.0},
		{"balanced", 10, 10, 0},
		{"slightly long YES", 7, 3, 0.4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inv := newTestInventory()
			if tt.yesQty > 0 {
				inv.OnFill(Fill{Side: types.BUY, TokenID: yesToken, Price: 0.50, Size: tt.yesQty})
			}
			if tt.noQty > 0 {
				inv.OnFill(Fill{Side: types.BUY, TokenID: noToken, Price: 0.50, Size: tt.noQty})
			}

			got := inv.NetDelta()
			if math.Abs(got-tt.want) > 1e-10 {
				t.Errorf("NetDelta() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTotalExposureUSD(t *testing.T) {
	t.Parallel()
	inv := newTestInventory()

	inv.OnFill(Fill{Side: types.BUY, TokenID: yesToken, Price: 0.50, Size: 10})
	inv.OnFill(Fill{Side: types.BUY, TokenID: noToken, Price: 0.50, Size: 5})

	mid := 0.60
	// YES exposure: 10 * 0.60 = 6.0
	// NO exposure:  5 * (1 - 0.60) = 2.0
	// Total: 8.0
	got := inv.TotalExposureUSD(mid)
	if math.Abs(got-8.0) > 1e-10 {
		t.Errorf("TotalExposureUSD = %v, want 8.0", got)
	}
}

func TestUpdateMarkToMarket(t *testing.T) {
	t.Parallel()
	inv := newTestInventory()

	inv.OnFill(Fill{Side: types.BUY, TokenID: yesToken, Price: 0.50, Size: 10})
	inv.UpdateMarkToMarket(0.60)

	pos := inv.Snapshot()
	// unrealized = 10 * (0.60 - 0.50) = 1.0
	if math.Abs(pos.UnrealizedPnL-1.0) > 1e-10 {
		t.Errorf("UnrealizedPnL = %v, want 1.0", pos.UnrealizedPnL)
	}
}

func TestSetPosition(t *testing.T) {
	t.Parallel()
	inv := newTestInventory()

	inv.SetPosition(Position{YesQty: 42, AvgEntryYes: 0.55})

	pos := inv.Snapshot()
	if pos.YesQty != 42 {
		t.Errorf("YesQty = %v, want 42", pos.YesQty)
	}
}
