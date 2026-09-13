package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIKeyEntry is a single entry of the top-level `api-keys` list.
//
// Two shapes are accepted, and both round-trip through YAML and the management API:
//
//	api-keys:
//	  - "plain-key"                       # string form: the key sees every model
//	  - api-key: "codex-client-key"       # object form
//	    allowed-models: ["gpt-*"]         # wildcards, same matcher as excluded-models
//	    label: "mac"                      # optional free text, no routing effect
//
// An entry written as a plain string is marshalled back as a plain string, so configs that
// use neither `allowed-models` nor `label` are byte-for-byte unchanged.
type APIKeyEntry struct {
	// APIKey is the client key presented as a bearer token, x-api-key, x-goog-api-key or query key.
	APIKey string `yaml:"api-key" json:"api-key"`

	// AllowedModels restricts this key to matching model IDs. Empty means no restriction.
	// Patterns support '*' wildcards and are matched against model IDs as they are listed,
	// including any credential prefix (for example "natacha/gpt-6-astra").
	AllowedModels []string `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`

	// Label is optional free text naming the client this key belongs to, such as a device.
	// It has no routing effect; the management panel uses it to name a key in usage views.
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
}

// APIKeyEntries is the top-level `api-keys` list.
type APIKeyEntries []APIKeyEntry

// apiKeyEntryFields mirrors APIKeyEntry without its custom codecs, so the object form can be
// decoded and encoded with the standard struct tags without recursing back into them.
type apiKeyEntryFields struct {
	APIKey        string   `yaml:"api-key" json:"api-key"`
	AllowedModels []string `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`
	Label         string   `yaml:"label,omitempty" json:"label,omitempty"`
}

// UnmarshalYAML accepts either a scalar key or an object with `api-key`, `allowed-models`
// and `label`.
func (e *APIKeyEntry) UnmarshalYAML(value *yaml.Node) error {
	if e == nil || value == nil {
		return nil
	}
	if value.Kind == yaml.ScalarNode {
		var key string
		if errDecode := value.Decode(&key); errDecode != nil {
			return fmt.Errorf("parse api-keys entry: %w", errDecode)
		}
		e.APIKey = key
		e.AllowedModels = nil
		e.Label = ""
		return nil
	}
	var fields apiKeyEntryFields
	if errDecode := value.Decode(&fields); errDecode != nil {
		return fmt.Errorf("parse api-keys entry: %w", errDecode)
	}
	e.APIKey = fields.APIKey
	e.AllowedModels = fields.AllowedModels
	e.Label = fields.Label
	return nil
}

// MarshalYAML writes the plain string form unless the entry carries per-key settings.
func (e APIKeyEntry) MarshalYAML() (any, error) {
	if !e.hasSettings() {
		return e.APIKey, nil
	}
	return apiKeyEntryFields{APIKey: e.APIKey, AllowedModels: e.AllowedModels, Label: e.Label}, nil
}

// hasSettings reports whether the entry needs the object form.
func (e APIKeyEntry) hasSettings() bool {
	return len(e.AllowedModels) > 0 || strings.TrimSpace(e.Label) != ""
}

// UnmarshalJSON accepts either a JSON string or an object, mirroring the YAML forms.
func (e *APIKeyEntry) UnmarshalJSON(data []byte) error {
	if e == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		e.APIKey = ""
		e.AllowedModels = nil
		e.Label = ""
		return nil
	}
	var key string
	if errString := json.Unmarshal(trimmed, &key); errString == nil {
		e.APIKey = key
		e.AllowedModels = nil
		e.Label = ""
		return nil
	}
	var fields apiKeyEntryFields
	if errObject := json.Unmarshal(trimmed, &fields); errObject != nil {
		return fmt.Errorf("parse api-keys entry: %w", errObject)
	}
	e.APIKey = fields.APIKey
	e.AllowedModels = fields.AllowedModels
	e.Label = fields.Label
	return nil
}

// MarshalJSON writes the plain string form unless the entry carries per-key settings.
func (e APIKeyEntry) MarshalJSON() ([]byte, error) {
	if !e.hasSettings() {
		return json.Marshal(e.APIKey)
	}
	return json.Marshal(apiKeyEntryFields{APIKey: e.APIKey, AllowedModels: e.AllowedModels, Label: e.Label})
}

// NewAPIKeyEntries builds plain-string entries for the supplied keys.
func NewAPIKeyEntries(keys ...string) APIKeyEntries {
	if len(keys) == 0 {
		return nil
	}
	entries := make(APIKeyEntries, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, APIKeyEntry{APIKey: key})
	}
	return entries
}

// Values returns the raw key strings in configuration order, preserving duplicates and
// surrounding whitespace so existing consumers keep their current behaviour.
func (l APIKeyEntries) Values() []string {
	if len(l) == 0 {
		return nil
	}
	values := make([]string, 0, len(l))
	for _, entry := range l {
		values = append(values, entry.APIKey)
	}
	return values
}

// AllowedModelsFor returns the normalized allow patterns configured for the supplied key.
// It returns nil when the key is unknown or unrestricted, which callers treat as "allow all".
func (l APIKeyEntries) AllowedModelsFor(apiKey string) []string {
	trimmedKey := strings.TrimSpace(apiKey)
	if trimmedKey == "" || len(l) == 0 {
		return nil
	}
	for _, entry := range l {
		if strings.TrimSpace(entry.APIKey) != trimmedKey {
			continue
		}
		return NormalizeExcludedModels(entry.AllowedModels)
	}
	return nil
}

// HasAllowedModels reports whether any entry restricts the models it may reach.
func (l APIKeyEntries) HasAllowedModels() bool {
	for _, entry := range l {
		if len(NormalizeExcludedModels(entry.AllowedModels)) > 0 {
			return true
		}
	}
	return false
}
