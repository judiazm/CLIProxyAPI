// model_overlay.go keeps the local model catalog overlay (the `models-file` config key)
// in sync with the configuration and with edits to the overlay file itself.
package watcher

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"

	log "github.com/sirupsen/logrus"
)

// syncModelOverlay points the registry at path, watches its parent directory, and
// re-applies the overlay. Watching the directory keeps creation and atomic file
// replacement observable even when the configured file is currently absent.
func (w *Watcher) syncModelOverlay(path string) {
	if w == nil {
		return
	}
	path = strings.TrimSpace(path)

	w.clientsMutex.Lock()
	previous := w.modelsFilePath
	w.modelsFilePath = path
	w.clientsMutex.Unlock()

	if w.watcher != nil {
		previousDir := modelOverlayWatchDir(previous)
		watchDir := modelOverlayWatchDir(path)
		if previousDir != "" && previousDir != watchDir && w.normalizeAuthPath(previousDir) != w.normalizeAuthPath(w.authDir) {
			if errRemove := w.watcher.Remove(previousDir); errRemove != nil {
				log.Debugf("failed to stop watching model overlay directory %s: %v", previousDir, errRemove)
			}
		}
		if watchDir != "" {
			if errAdd := w.watcher.Add(watchDir); errAdd != nil {
				log.Warnf("failed to watch model overlay directory %s: %v", watchDir, errAdd)
			} else {
				log.Debugf("watching model overlay directory: %s", watchDir)
			}
		}
	}

	w.applyModelOverlay(path)
}

func modelOverlayWatchDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Dir(filepath.Clean(path))
}

// applyModelOverlay re-reads the overlay file and notifies model consumers when the
// effective catalog changed, so already registered credentials pick up the change.
func (w *Watcher) applyModelOverlay(path string) {
	changed := registry.ApplyModelOverlayFile(path)
	if len(changed) == 0 {
		return
	}
	log.Infof("local model catalog overlay applied, changes detected for providers: %v", changed)
	registry.NotifyModelCatalogChange(changed)
}

// currentModelOverlayPath returns the overlay path the watcher is tracking.
func (w *Watcher) currentModelOverlayPath() string {
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	return w.modelsFilePath
}

func (w *Watcher) stopModelOverlayTimer() {
	w.modelOverlayMu.Lock()
	if w.modelOverlayTimer != nil {
		w.modelOverlayTimer.Stop()
		w.modelOverlayTimer = nil
	}
	w.modelOverlayMu.Unlock()
}

// scheduleModelOverlayReload debounces overlay file events, which editors emit in bursts.
func (w *Watcher) scheduleModelOverlayReload() {
	w.modelOverlayMu.Lock()
	defer w.modelOverlayMu.Unlock()
	if w.modelOverlayTimer != nil {
		w.modelOverlayTimer.Stop()
	}
	w.modelOverlayTimer = time.AfterFunc(configReloadDebounce, func() {
		w.modelOverlayMu.Lock()
		w.modelOverlayTimer = nil
		w.modelOverlayMu.Unlock()
		w.syncModelOverlay(w.currentModelOverlayPath())
	})
}
