package usagestore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestApplyOpensClosesAndReopensOnConfigChange(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	t.Cleanup(Close)

	// Disabled: nothing is opened.
	cfg := &config.Config{}
	Apply(cfg)
	if Current() != nil {
		t.Fatalf("Current() = %v while disabled, want nil", Current())
	}

	// Enabled with an empty path: the default path is used.
	cfg.UsageStore.Enabled = true
	Apply(cfg)
	store := Current()
	if store == nil {
		t.Fatal("Current() = nil after enabling")
	}
	wantPath := filepath.Join(homeDir, ".cli-proxy-api", "usage.db")
	if store.Path() != wantPath {
		t.Fatalf("Path() = %q, want %q", store.Path(), wantPath)
	}

	// Re-applying an unchanged config keeps the same store open.
	Apply(cfg)
	if Current() != store {
		t.Fatal("Apply reopened the store for an unchanged config")
	}

	// A path change reopens against the new file and closes the old one.
	movedPath := filepath.Join(t.TempDir(), "moved.db")
	cfg.UsageStore.Path = movedPath
	cfg.UsageStore.RetentionDays = 7
	Apply(cfg)
	moved := Current()
	if moved == nil || moved == store {
		t.Fatalf("Current() = %v after path change, want a new store", moved)
	}
	if moved.Path() != movedPath || moved.RetentionDays() != 7 {
		t.Fatalf("reopened store = %q/%d, want %q/7", moved.Path(), moved.RetentionDays(), movedPath)
	}
	if errWrite := writeOne(moved); errWrite != nil {
		t.Fatalf("write after reopen: %v", errWrite)
	}

	// Disabling closes the store.
	cfg.UsageStore.Enabled = false
	Apply(cfg)
	if Current() != nil {
		t.Fatalf("Current() = %v after disabling, want nil", Current())
	}
}

func writeOne(store *Store) error {
	store.Enqueue(&Row{TsMs: time.Now().UnixMilli(), Model: "gpt-5.6-sol"})
	return store.Flush(context.Background())
}
