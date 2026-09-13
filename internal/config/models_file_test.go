package config

import (
	"path/filepath"
	"testing"
)

func TestParseConfigBytes_ModelsFileExpandsLeadingTilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	cfg, errParse := ParseConfigBytes([]byte("models-file: \"~/cliproxyapi/models.local.json\"\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	want := filepath.Join(homeDir, "cliproxyapi", "models.local.json")
	if cfg.ModelsFile != want {
		t.Fatalf("ModelsFile = %q, want %q", cfg.ModelsFile, want)
	}
}

func TestParseConfigBytes_ModelsFileDefaultsToEmpty(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.ModelsFile != "" {
		t.Fatalf("ModelsFile = %q, want empty", cfg.ModelsFile)
	}
}
