package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAPIKeyEntriesUnmarshalYAMLAcceptsBothForms(t *testing.T) {
	data := strings.Join([]string{
		"api-keys:",
		"  - plain-key",
		"  - api-key: codex-client-key",
		"    allowed-models:",
		`      - "gpt-*"`,
		`      - "natacha/*"`,
		"",
	}, "\n")

	var cfg SDKConfig
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("len(APIKeys) = %d, want 2", len(cfg.APIKeys))
	}
	if cfg.APIKeys[0].APIKey != "plain-key" || len(cfg.APIKeys[0].AllowedModels) != 0 {
		t.Fatalf("APIKeys[0] = %#v, want unrestricted plain-key", cfg.APIKeys[0])
	}
	if cfg.APIKeys[1].APIKey != "codex-client-key" {
		t.Fatalf("APIKeys[1].APIKey = %q, want codex-client-key", cfg.APIKeys[1].APIKey)
	}
	if want := []string{"gpt-*", "natacha/*"}; !reflect.DeepEqual(cfg.APIKeys[1].AllowedModels, want) {
		t.Fatalf("APIKeys[1].AllowedModels = %#v, want %#v", cfg.APIKeys[1].AllowedModels, want)
	}
	if want := []string{"plain-key", "codex-client-key"}; !reflect.DeepEqual(cfg.APIKeys.Values(), want) {
		t.Fatalf("Values() = %#v, want %#v", cfg.APIKeys.Values(), want)
	}
}

func TestAPIKeyEntriesMarshalYAMLKeepsPlainStrings(t *testing.T) {
	entries := APIKeyEntries{
		{APIKey: "plain-key"},
		{APIKey: "codex-client-key", AllowedModels: []string{"gpt-*"}},
	}

	rendered, err := yaml.Marshal(entries)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	want := "- plain-key\n- api-key: codex-client-key\n  allowed-models:\n    - gpt-*\n"
	if got := string(rendered); got != want {
		t.Fatalf("yaml.Marshal() = %q, want %q", got, want)
	}

	var decoded APIKeyEntries
	if errDecode := yaml.Unmarshal(rendered, &decoded); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	if !reflect.DeepEqual(decoded, entries) {
		t.Fatalf("round-tripped entries = %#v, want %#v", decoded, entries)
	}
}

func TestAPIKeyEntriesJSONRoundTrip(t *testing.T) {
	var entries APIKeyEntries
	body := `["plain-key",{"api-key":"codex-client-key","allowed-models":["gpt-*"]}]`
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(entries) != 2 || entries[0].APIKey != "plain-key" || entries[1].AllowedModels[0] != "gpt-*" {
		t.Fatalf("entries = %#v, want the string and object forms", entries)
	}

	rendered, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if got := string(rendered); got != body {
		t.Fatalf("json.Marshal() = %s, want %s", got, body)
	}
}

func TestSaveConfigPreserveCommentsRoundTripsAPIKeyEntries(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	original := strings.Join([]string{
		"# client keys",
		"api-keys:",
		"  - plain-key",
		"  - api-key: codex-client-key",
		"    allowed-models:",
		`      - "gpt-*"`,
		"",
	}, "\n")
	if errWrite := os.WriteFile(configPath, []byte(original), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	saved, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	if !strings.Contains(string(saved), "# client keys") {
		t.Fatalf("saved config lost its comment:\n%s", saved)
	}

	reloaded, errReload := LoadConfig(configPath)
	if errReload != nil {
		t.Fatalf("LoadConfig() after save error = %v; saved config:\n%s", errReload, saved)
	}
	if !reflect.DeepEqual(reloaded.APIKeys, cfg.APIKeys) {
		t.Fatalf("reloaded api-keys = %#v, want %#v; saved config:\n%s", reloaded.APIKeys, cfg.APIKeys, saved)
	}
}

func TestAPIKeyEntriesAllowedModelsFor(t *testing.T) {
	entries := APIKeyEntries{
		{APIKey: "plain-key"},
		{APIKey: " codex-client-key ", AllowedModels: []string{" GPT-* ", "gpt-*", ""}},
	}

	if got := entries.AllowedModelsFor("plain-key"); got != nil {
		t.Fatalf("AllowedModelsFor(plain-key) = %#v, want nil", got)
	}
	if got := entries.AllowedModelsFor("unknown-key"); got != nil {
		t.Fatalf("AllowedModelsFor(unknown-key) = %#v, want nil", got)
	}
	if got := entries.AllowedModelsFor(""); got != nil {
		t.Fatalf("AllowedModelsFor(empty) = %#v, want nil", got)
	}
	// Patterns are trimmed, lower-cased and de-duplicated like excluded-models.
	if want := []string{"gpt-*"}; !reflect.DeepEqual(entries.AllowedModelsFor("codex-client-key"), want) {
		t.Fatalf("AllowedModelsFor(codex-client-key) = %#v, want %#v", entries.AllowedModelsFor("codex-client-key"), want)
	}
	if !entries.HasAllowedModels() {
		t.Fatal("HasAllowedModels() = false, want true")
	}
	if NewAPIKeyEntries("a", "b").HasAllowedModels() {
		t.Fatal("HasAllowedModels() = true for plain keys, want false")
	}
}

func TestAPIKeyEntryLabelRoundTrips(t *testing.T) {
	data := strings.Join([]string{
		"api-keys:",
		"  - plain-key",
		"  - api-key: labelled-only",
		"    label: mac",
		"  - api-key: codex-client-key",
		"    allowed-models:",
		`      - "gpt-*"`,
		"    label: phone",
		"",
	}, "\n")

	var cfg SDKConfig
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(cfg.APIKeys) != 3 {
		t.Fatalf("len(APIKeys) = %d, want 3", len(cfg.APIKeys))
	}
	if cfg.APIKeys[0].Label != "" {
		t.Fatalf("APIKeys[0].Label = %q, want empty", cfg.APIKeys[0].Label)
	}
	if cfg.APIKeys[1].Label != "mac" || len(cfg.APIKeys[1].AllowedModels) != 0 {
		t.Fatalf("APIKeys[1] = %#v, want label-only entry", cfg.APIKeys[1])
	}
	if cfg.APIKeys[2].Label != "phone" {
		t.Fatalf("APIKeys[2].Label = %q, want phone", cfg.APIKeys[2].Label)
	}

	rendered, errYAML := yaml.Marshal(cfg.APIKeys)
	if errYAML != nil {
		t.Fatalf("yaml.Marshal() error = %v", errYAML)
	}
	wantYAML := strings.Join([]string{
		"- plain-key",
		"- api-key: labelled-only",
		"  label: mac",
		"- api-key: codex-client-key",
		"  allowed-models:",
		"    - gpt-*",
		"  label: phone",
		"",
	}, "\n")
	if got := string(rendered); got != wantYAML {
		t.Fatalf("yaml.Marshal() = %q, want %q", got, wantYAML)
	}

	encoded, errJSON := json.Marshal(cfg.APIKeys)
	if errJSON != nil {
		t.Fatalf("json.Marshal() error = %v", errJSON)
	}
	wantJSON := `["plain-key",{"api-key":"labelled-only","label":"mac"},{"api-key":"codex-client-key","allowed-models":["gpt-*"],"label":"phone"}]`
	if got := string(encoded); got != wantJSON {
		t.Fatalf("json.Marshal() = %s, want %s", got, wantJSON)
	}

	var decoded APIKeyEntries
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, cfg.APIKeys) {
		t.Fatalf("json round-trip = %#v, want %#v", decoded, cfg.APIKeys)
	}
}
