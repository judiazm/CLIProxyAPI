// model_overlay.go keeps the local model catalog overlay (the `models-file` config key)
// in sync with the configuration and with edits to the overlay file itself.
package watcher

import (
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"

	log "github.com/sirupsen/logrus"
)

// syncModelOverlay points the registry at path, (re)registers the filesystem watch
// for it, and re-applies the overlay. fsnotify drops a watch when the watched file
// is replaced, so the watch is added again on every call.
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
		if previous != "" && previous != path {
			if errRemove := w.watcher.Remove(previous); errRemove != nil {
				log.Debugf("failed to stop watching model overlay file %s: %v", previous, errRemove)
			}
		}
		if path != "" {
			if errAdd := w.watcher.Add(path); errAdd != nil {
				log.Warnf("failed to watch model overlay file %s: %v", path, errAdd)
			} else {
				log.Debugf("watching model overlay file: %s", path)
			}
		}
	}

	w.applyModelOverlay(path)
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
