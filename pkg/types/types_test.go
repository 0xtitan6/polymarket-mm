package types

import "testing"

func TestTickSizeDecimals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tick TickSize
		want int
	}{
		{Tick01, 1},
		{Tick001, 2},
		{Tick0001, 3},
		{Tick00001, 4},
		{TickSize("unknown"), 2}, // default
	}

	for _, tt := range tests {
		if got := tt.tick.Decimals(); got != tt.want {
			t.Errorf("TickSize(%q).Decimals() = %d, want %d", tt.tick, got, tt.want)
		}
	}
}

func TestTickSizeAmountDecimals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tick TickSize
		want int
	}{
		{Tick01, 3},
		{Tick001, 4},
		{Tick0001, 5},
		{Tick00001, 6},
		{TickSize("unknown"), 4}, // default
	}

	for _, tt := range tests {
		if got := tt.tick.AmountDecimals(); got != tt.want {
			t.Errorf("TickSize(%q).AmountDecimals() = %d, want %d", tt.tick, got, tt.want)
		}
	}
}
