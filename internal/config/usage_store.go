package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultUsageStorePath is the SQLite database path used when `usage-store.path` is empty.
const DefaultUsageStorePath = "~/.cli-proxy-api/usage.db"

// UsageStoreConfig configures the persistent usage store.
//
//	usage-store:
//	  enabled: true                            # default false
//	  path: "~/.cli-proxy-api/usage.db"        # default; "~" expands
//	  retention-days: 0                        # 0 = keep forever; >0 prunes rows older than N days hourly
//
// The store is independent of `usage-statistics-enabled`: it records whenever Enabled is true.
type UsageStoreConfig struct {
	// Enabled turns persistence on. When false no database file is opened.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Path is the SQLite database file. A leading tilde (~) expands to the user's home
	// directory. Empty falls back to DefaultUsageStorePath.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`

	// RetentionDays prunes rows older than N days once an hour. 0 keeps every row forever.
	RetentionDays int `yaml:"retention-days" json:"retention-days"`
}

// ResolveUsageStorePath normalizes the usage store database path.
// It expands a leading tilde (~) to the user's home directory and substitutes the default
// path for an empty value.
func ResolveUsageStorePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultUsageStorePath
	}
	if strings.HasPrefix(path, "~") {
		homeDir, errUserHomeDir := os.UserHomeDir()
		if errUserHomeDir != nil {
			return "", fmt.Errorf("resolve usage store path: %w", errUserHomeDir)
		}
		remainder := strings.TrimPrefix(path, "~")
		remainder = strings.TrimLeft(remainder, "/\\")
		if remainder == "" {
			return filepath.Clean(homeDir), nil
		}
		normalized := strings.ReplaceAll(remainder, "\\", "/")
		return filepath.Clean(filepath.Join(homeDir, filepath.FromSlash(normalized))), nil
	}
	return filepath.Clean(path), nil
}

// ResolveUsageStore expands an explicitly configured usage store path and clamps
// retention-days. An empty path is left empty so the configuration round-trips unchanged;
// the store substitutes DefaultUsageStorePath when it opens. The configured value is left
// untouched when the home directory cannot be resolved.
func (cfg *Config) ResolveUsageStore() error {
	if cfg == nil {
		return nil
	}
	if cfg.UsageStore.RetentionDays < 0 {
		cfg.UsageStore.RetentionDays = 0
	}
	if strings.TrimSpace(cfg.UsageStore.Path) == "" {
		cfg.UsageStore.Path = ""
		return nil
	}
	path, errResolveUsageStorePath := ResolveUsageStorePath(cfg.UsageStore.Path)
	if errResolveUsageStorePath != nil {
		return errResolveUsageStorePath
	}
	cfg.UsageStore.Path = path
	return nil
}
