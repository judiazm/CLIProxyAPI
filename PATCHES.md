# judiazm patches on top of upstream CLIProxyAPI

This fork carries a short patch series on branch `jd/patches`, rebased onto each upstream release tag.
Every patch must stay small, self-contained, and documented here so a rebase (scripted, or done by an
agent when the script hits conflicts) can preserve its intent without reading the whole diff.

## Patch 1: per-API-key model allowlist (`allowed-models`)

Problem: the proxy publishes one model catalog to every client key. A Codex client sees Claude models
(selecting one would send a Claude subscription through a non-Claude-Code client) and every prefixed
alias of a pinned account. There is no per-client view.

Change: an `api-keys` entry may be a string (unchanged) or an object:

```yaml
api-keys:
  - "plain-key"                       # unchanged: sees everything
  - api-key: "codex-client-key"
    allowed-models: ["gpt-*"]         # wildcards, same matcher as excluded-models
  - api-key: "hermes-key"
    allowed-models: ["gpt-*", "natacha/*"]
```

Behaviour: when a request authenticates with a key that has `allowed-models`, (a) every model-list
endpoint (OpenAI `/v1/models`, Claude `/v1/models`, Gemini and Codex equivalents) returns only matching
models, and (b) a chat/responses/messages request for a non-matching model is rejected with the same
`model_not_found` error shape the proxy already uses. Keys without `allowed-models` behave exactly as
today. Management API config get/set must round-trip the object form; the hot-reload watcher must
pick up changes. Add unit tests for the matcher, the list filtering, and the request rejection.

## Patch 2: local model catalog overlay (`models-file`)

Problem: the model catalog is embedded and refreshed from router-for-me/models with hardcoded URLs.
A model that exists upstream at OpenAI but not in that catalog (example: `gpt-daybreak-blue-latest`,
display name "Daybreak Blue", a Codex preview model) is rejected with `unknown provider for model`.

Change: a top-level config key `models-file: "~/cliproxyapi/models.local.json"` whose file uses the
same top-level structure as the embedded `models.json` (per-channel arrays). On startup, after every
remote refresh, and on file change (config watcher), entries from this file are merged into the
registry: an entry whose id already exists replaces the embedded/remote definition; a new id is added
to that channel. Missing or unreadable file logs a warning and changes nothing. Add a unit test that a
new Codex id from the overlay is listed and routable, and one for replacement.

### Overlay file shape

The overlay is a JSON object of per-channel arrays. Channel keys are the ones in the embedded
`models.json` — `claude`, `gemini`, `vertex`, `aistudio`, `codex-free`, `codex-team`, `codex-plus`,
`codex-pro`, `kimi`, `antigravity`, `xai` — plus `codex`, a shorthand that applies to all four Codex
plan tiers so an entry is served whatever plan the credential reports. Channels you do not list are
left alone. Each entry is a model definition with the same field names as the embedded catalog; `id`
is the only required field, and everything else follows the entry it stands next to.

Minimal working overlay for the Codex preview model that the upstream catalog omits:

```json
{
  "codex": [
    {
      "id": "gpt-daybreak-blue-latest",
      "object": "model",
      "created": 1770912000,
      "owned_by": "openai",
      "type": "openai",
      "display_name": "Daybreak Blue",
      "version": "gpt-daybreak-blue",
      "description": "Codex preview model missing from the upstream catalog.",
      "context_length": 272000,
      "max_completion_tokens": 128000,
      "supported_parameters": ["tools"],
      "thinking": { "levels": ["low", "medium", "high", "xhigh"] },
      "supportedInputModalities": ["text", "image"],
      "supportedOutputModalities": ["text"]
    }
  ]
}
```

With that file in place and `models-file` pointing at it, `gpt-daybreak-blue-latest` is listed by the
Codex model accessors, registered for every Codex OAuth credential, and routes instead of failing with
`unknown provider for model`.

### Implementation notes

The registry keeps the catalog as loaded from the embed or a remote fetch (`modelStore.base`) apart
from the effective catalog the accessors read (`modelStore.data`), and recomputes the effective catalog
whenever either input changes. `registry.ApplyModelOverlayFile` sets the path, reloads the file and
returns the providers whose definitions changed; the watcher feeds that into
`registry.NotifyModelCatalogChange` so already-registered credentials re-register. An overlay that
fails to read, parse or validate leaves the last good overlay in place.

## Rules for both

- Keep upstream style (gofmt, logrus, no log.Fatal). Keep changes out of `internal/translator/`.
- Each patch is one commit whose message starts with `jd:`.
- `go build -o /dev/null ./cmd/server && go test ./...` must pass.
