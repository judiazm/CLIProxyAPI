package config

import (
	"path/filepath"
	"testing"
)

func TestParseConfigBytes_UsageStoreExpandsLeadingTilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	cfg, errParse := ParseConfigBytes([]byte("usage-store:\n  enabled: true\n  path: \"~/cliproxyapi/usage.db\"\n  retention-days: 30\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	want := filepath.Join(homeDir, "cliproxyapi", "usage.db")
	if !cfg.UsageStore.Enabled {
		t.Fatalf("UsageStore.Enabled = false, want true")
	}
	if cfg.UsageStore.Path != want {
		t.Fatalf("UsageStore.Path = %q, want %q", cfg.UsageStore.Path, want)
	}
	if cfg.UsageStore.RetentionDays != 30 {
		t.Fatalf("UsageStore.RetentionDays = %d, want 30", cfg.UsageStore.RetentionDays)
	}
}

func TestParseConfigBytes_UsageStoreDefaultsToDisabled(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.UsageStore.Enabled {
		t.Fatalf("UsageStore.Enabled = true, want false")
	}
	if cfg.UsageStore.Path != "" {
		t.Fatalf("UsageStore.Path = %q, want empty so the store applies its own default", cfg.UsageStore.Path)
	}
	if cfg.UsageStore.RetentionDays != 0 {
		t.Fatalf("UsageStore.RetentionDays = %d, want 0", cfg.UsageStore.RetentionDays)
	}
}

func TestResolveUsageStorePath_EmptyUsesDefault(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	path, errResolve := ResolveUsageStorePath("")
	if errResolve != nil {
		t.Fatalf("ResolveUsageStorePath() error = %v", errResolve)
	}
	want := filepath.Join(homeDir, ".cli-proxy-api", "usage.db")
	if path != want {
		t.Fatalf("ResolveUsageStorePath(\"\") = %q, want %q", path, want)
	}
}

func TestResolveUsageStore_ClampsNegativeRetention(t *testing.T) {
	cfg := &Config{}
	cfg.UsageStore.RetentionDays = -5
	if errResolve := cfg.ResolveUsageStore(); errResolve != nil {
		t.Fatalf("ResolveUsageStore() error = %v", errResolve)
	}
	if cfg.UsageStore.RetentionDays != 0 {
		t.Fatalf("RetentionDays = %d, want 0", cfg.UsageStore.RetentionDays)
	}
}
