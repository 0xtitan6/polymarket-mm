// Package store provides crash-safe position persistence using JSON files.
//
// Each market's position is stored as a separate file: pos_<marketID>.json.
// Writes use atomic file replacement (write to .tmp, then rename) to prevent
// corruption from partial writes or crashes mid-save. The strategy layer
// calls SavePosition after each fill, and LoadPosition on startup to restore
// inventory state.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"polymarket-mm/internal/strategy"
)

// Store persists positions to JSON files in a designated directory.
// All operations are mutex-protected to prevent concurrent file corruption.
type Store struct {
	dir string     // directory containing pos_*.json files
	mu  sync.Mutex // serializes all file operations
}

// Open creates a store backed by the given directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Close is a no-op for file-based storage.
func (s *Store) Close() error {
	return nil
}

// SavePosition atomically persists the current position for a market.
// It writes to a .tmp file first, then renames over the target to ensure
// the file is never left in a partial state (crash-safe).
func (s *Store) SavePosition(marketID string, pos strategy.Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(pos)
	if err != nil {
		return fmt.Errorf("marshal position: %w", err)
	}

	path := filepath.Join(s.dir, "pos_"+marketID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write position: %w", err)
	}
	return os.Rename(tmp, path)
}

// LoadPosition restores position for a market from disk.
// Returns nil, nil if no saved position exists (fresh market).
func (s *Store) LoadPosition(marketID string) (*strategy.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, "pos_"+marketID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read position: %w", err)
	}

	var pos strategy.Position
	if err := json.Unmarshal(data, &pos); err != nil {
		return nil, fmt.Errorf("unmarshal position: %w", err)
	}
	return &pos, nil
}
