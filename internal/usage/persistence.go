// Package usage provides statistics persistence functionality.
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// PersistenceManager handles automatic saving and loading of usage statistics.
type PersistenceManager struct {
	mu           sync.Mutex
	stats        *RequestStatistics
	filePath     string
	saveInterval time.Duration
	stopCh       chan struct{}
	running      bool
}

var defaultPersistence *PersistenceManager
var persistenceOnce sync.Once

// GetPersistenceManager returns the shared persistence manager.
func GetPersistenceManager() *PersistenceManager {
	return defaultPersistence
}

// InitPersistence initializes the persistence manager with the given config directory.
func InitPersistence(configDir string, saveInterval time.Duration) *PersistenceManager {
	persistenceOnce.Do(func() {
		filePath := filepath.Join(configDir, "usage-statistics.json")
		defaultPersistence = &PersistenceManager{
			stats:        defaultRequestStatistics,
			filePath:     filePath,
			saveInterval: saveInterval,
			stopCh:       make(chan struct{}),
		}
	})
	return defaultPersistence
}

// Start begins automatic persistence.
func (m *PersistenceManager) Start() {
	if m == nil {
		return
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	// Load existing data
	if err := m.Load(); err != nil {
		log.WithError(err).Warn("failed to load existing usage statistics")
	}

	// Start periodic save
	go m.periodicSave()
}

// Stop ends automatic persistence and performs a final save.
func (m *PersistenceManager) Stop() {
	if m == nil {
		return
	}

	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	close(m.stopCh)
	m.mu.Unlock()

	// Final save
	if err := m.Save(); err != nil {
		log.WithError(err).Error("failed to save usage statistics on shutdown")
	}
}

// Save writes the current statistics to disk.
func (m *PersistenceManager) Save() error {
	if m == nil || m.stats == nil {
		return nil
	}

	snapshot := m.stats.Snapshot()
	payload := struct {
		Version   int                `json:"version"`
		SavedAt   time.Time          `json:"saved_at"`
		Usage     StatisticsSnapshot `json:"usage"`
	}{
		Version: 1,
		SavedAt: time.Now(),
		Usage:   snapshot,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Write to temp file first, then rename (atomic)
	tmpPath := m.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, m.filePath)
}

// Load reads statistics from disk and merges into memory.
func (m *PersistenceManager) Load() error {
	if m == nil || m.stats == nil {
		return nil
	}

	data, err := os.ReadFile(m.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing file, that's fine
		}
		return err
	}

	var payload struct {
		Version int                `json:"version"`
		SavedAt time.Time          `json:"saved_at"`
		Usage   StatisticsSnapshot `json:"usage"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	result := m.stats.MergeSnapshot(payload.Usage)
	log.WithFields(log.Fields{
		"added":     result.Added,
		"skipped":   result.Skipped,
		"file":      m.filePath,
		"saved_at":  payload.SavedAt,
	}).Info("loaded usage statistics from disk")

	return nil
}

func (m *PersistenceManager) periodicSave() {
	ticker := time.NewTicker(m.saveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := m.Save(); err != nil {
				log.WithError(err).Warn("periodic usage statistics save failed")
			} else {
				log.Debug("usage statistics saved to disk")
			}
		case <-m.stopCh:
			return
		}
	}
}

// FilePath returns the path where statistics are persisted.
func (m *PersistenceManager) FilePath() string {
	if m == nil {
		return ""
	}
	return m.filePath
}
