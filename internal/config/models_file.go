package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveModelsFile normalizes the local model catalog overlay path.
// It expands a leading tilde (~) to the user's home directory and keeps empty values empty,
// which disables the overlay.
func ResolveModelsFile(modelsFile string) (string, error) {
	modelsFile = strings.TrimSpace(modelsFile)
	if modelsFile == "" {
		return "", nil
	}
	if strings.HasPrefix(modelsFile, "~") {
		homeDir, errUserHomeDir := os.UserHomeDir()
		if errUserHomeDir != nil {
			return "", fmt.Errorf("resolve models file: %w", errUserHomeDir)
		}
		remainder := strings.TrimPrefix(modelsFile, "~")
		remainder = strings.TrimLeft(remainder, "/\\")
		if remainder == "" {
			return filepath.Clean(homeDir), nil
		}
		normalized := strings.ReplaceAll(remainder, "\\", "/")
		return filepath.Clean(filepath.Join(homeDir, filepath.FromSlash(normalized))), nil
	}
	return filepath.Clean(modelsFile), nil
}

// ResolveModelsFile resolves and stores the effective model catalog overlay path.
// The configured value is left untouched when the home directory cannot be resolved.
func (cfg *Config) ResolveModelsFile() error {
	if cfg == nil {
		return nil
	}
	modelsFile, errResolveModelsFile := ResolveModelsFile(cfg.ModelsFile)
	if errResolveModelsFile != nil {
		return errResolveModelsFile
	}
	cfg.ModelsFile = modelsFile
	return nil
}
