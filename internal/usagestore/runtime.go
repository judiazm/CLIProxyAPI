package usagestore

import (
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

var (
	currentMu sync.RWMutex
	current   *Store
)

// Current returns the active store, or nil when the store is disabled.
func Current() *Store {
	currentMu.RLock()
	defer currentMu.RUnlock()
	return current
}

// Enabled reports whether a store is open.
func Enabled() bool { return Current() != nil }

// Apply reconciles the active store with the supplied configuration. It opens the store
// when it becomes enabled, closes it when it becomes disabled and reopens it when the
// path or retention window changes. It is safe to call on every config reload.
func Apply(cfg *config.Config) {
	desiredEnabled := cfg != nil && cfg.UsageStore.Enabled
	if !desiredEnabled {
		Close()
		return
	}

	path, errResolvePath := config.ResolveUsageStorePath(cfg.UsageStore.Path)
	if errResolvePath != nil {
		log.WithError(errResolvePath).Warn("usage store: failed to resolve path; store disabled")
		Close()
		return
	}
	retentionDays := cfg.UsageStore.RetentionDays
	if retentionDays < 0 {
		retentionDays = 0
	}

	currentMu.RLock()
	existing := current
	currentMu.RUnlock()
	if existing != nil && existing.Path() == path && existing.RetentionDays() == retentionDays {
		return
	}

	store, errOpen := Open(Options{Path: path, RetentionDays: retentionDays})
	if errOpen != nil {
		log.WithError(errOpen).Error("usage store: failed to open database; store disabled")
		Close()
		return
	}

	currentMu.Lock()
	previous := current
	current = store
	currentMu.Unlock()

	if previous != nil {
		if errClose := previous.Close(); errClose != nil {
			log.Errorf("usage store: failed to close previous database: %v", errClose)
		}
	}
	log.WithFields(log.Fields{"path": path, "retention_days": retentionDays}).Info("usage store enabled")
}

// Close shuts the active store down and clears it. Calling it while disabled is a no-op.
func Close() {
	currentMu.Lock()
	previous := current
	current = nil
	currentMu.Unlock()
	if previous == nil {
		return
	}
	if errClose := previous.Close(); errClose != nil {
		log.Errorf("usage store: failed to close database: %v", errClose)
	}
}

// SetCurrentForTest swaps the active store and returns a function restoring the previous
// one. It exists so packages outside usagestore can exercise the management endpoints.
func SetCurrentForTest(store *Store) func() {
	currentMu.Lock()
	previous := current
	current = store
	currentMu.Unlock()
	return func() {
		currentMu.Lock()
		current = previous
		currentMu.Unlock()
	}
}
