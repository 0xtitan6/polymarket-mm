package store

import (
	"testing"

	"polymarket-mm/internal/strategy"
)

func TestSaveAndLoadPosition(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	pos := strategy.Position{
		YesQty:      10.5,
		NoQty:       3.2,
		AvgEntryYes: 0.55,
		AvgEntryNo:  0.45,
		RealizedPnL: 1.23,
	}

	if err := s.SavePosition("mkt1", pos); err != nil {
		t.Fatalf("SavePosition: %v", err)
	}

	loaded, err := s.LoadPosition("mkt1")
	if err != nil {
		t.Fatalf("LoadPosition: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadPosition returned nil")
	}

	if loaded.YesQty != pos.YesQty {
		t.Errorf("YesQty = %v, want %v", loaded.YesQty, pos.YesQty)
	}
	if loaded.AvgEntryYes != pos.AvgEntryYes {
		t.Errorf("AvgEntryYes = %v, want %v", loaded.AvgEntryYes, pos.AvgEntryYes)
	}
	if loaded.RealizedPnL != pos.RealizedPnL {
		t.Errorf("RealizedPnL = %v, want %v", loaded.RealizedPnL, pos.RealizedPnL)
	}
}

func TestLoadPositionMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	loaded, err := s.LoadPosition("nonexistent")
	if err != nil {
		t.Fatalf("LoadPosition: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for missing position, got %+v", loaded)
	}
}

func TestSavePositionOverwrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	pos1 := strategy.Position{YesQty: 10}
	pos2 := strategy.Position{YesQty: 20}

	_ = s.SavePosition("mkt1", pos1)
	_ = s.SavePosition("mkt1", pos2)

	loaded, err := s.LoadPosition("mkt1")
	if err != nil {
		t.Fatalf("LoadPosition: %v", err)
	}
	if loaded.YesQty != 20 {
		t.Errorf("YesQty = %v, want 20 (latest save)", loaded.YesQty)
	}
}
