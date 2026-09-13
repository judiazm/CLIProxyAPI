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

### Patch 1 addendum: `label` on the object form

An object entry may carry `label: "mac"` (free text, optional). It has no routing effect. It exists so
the management panel can name a client key (a device) in usage views without keeping a side table.
Config get/set must round-trip it like `allowed-models`.

## Patch 3: persistent usage store (`usage-store`)

Problem: every request produces a full usage record (client API key, model, credential, token
breakdown incl. cache read/write, latency, TTFT, failure) but upstream only holds it in an in-memory
queue for `redis-usage-queue-retention-seconds` (default 60 s, max 3600). There is no per-key or
per-model history, so "how much does each device and each model use" cannot be answered from the
proxy itself.

Change: a usage plugin (`internal/usagestore`) registered on the default usage manager that persists
every record into a SQLite database, plus three read-only management endpoints that aggregate it.
SQLite via `modernc.org/sqlite` (pure Go, no cgo). WAL journal mode. One writer goroutine drains a
buffered channel and inserts in batched transactions (flush every 1 s or 200 rows). Never block the
request path; if the channel is full, drop the record and log at warn once per minute.

```yaml
usage-store:
  enabled: true                            # default false
  path: "~/.cli-proxy-api/usage.db"        # default; "~" expands
  retention-days: 0                        # 0 = keep forever; >0 prunes rows older than N days hourly
```

Hot reload: enabling/disabling and path changes take effect on config reload (close and reopen).
`usage-statistics-enabled` is independent: the store records whenever `usage-store.enabled` is true.

### Table `usage_requests`

One row per record. Columns (all NOT NULL unless noted; strings default ''):

| column | type | source |
|---|---|---|
| id | INTEGER PK autoincrement | |
| ts_ms | INTEGER | `record.RequestedAt` (or now) as unix ms |
| api_key | TEXT | `record.APIKey` (the client key, full value, same as `/usage-queue`) |
| model | TEXT | `record.Model` |
| alias | TEXT | `record.Alias` |
| auth_id | TEXT | `record.AuthID` |
| auth_index | TEXT | `record.AuthIndex` |
| provider | TEXT | `record.Provider` |
| auth_type | TEXT | `record.AuthType` |
| executor_type | TEXT | `record.ExecutorType` |
| endpoint | TEXT | same resolver the redisqueue plugin uses |
| request_id | TEXT | logging request id from ctx |
| session_id | TEXT | normalized like redisqueue plugin |
| parent_session_id | TEXT | |
| reasoning_effort | TEXT | resolved like redisqueue plugin |
| service_tier | TEXT | |
| response_service_tier | TEXT | |
| stream | INTEGER 0/1 | |
| generate | INTEGER 0/1 | |
| failed | INTEGER 0/1 | resolved like redisqueue plugin (record.Failed OR ctx not success) |
| status_code | INTEGER | fail status code, 0 when not failed |
| latency_ms | INTEGER | |
| ttft_ms | INTEGER | |
| input_tokens | INTEGER | from `EnsureTokenBreakdownForProvider(record.Detail, …)` |
| output_tokens | INTEGER | |
| reasoning_tokens | INTEGER | |
| cached_tokens | INTEGER | |
| cache_read_tokens | INTEGER | |
| cache_creation_tokens | INTEGER | |
| total_tokens | INTEGER | |
| client_ip | TEXT | client request metadata |
| user_agent | TEXT | |

Indexes: `(ts_ms)`, `(api_key, ts_ms)`, `(model, ts_ms)`, `(auth_id, ts_ms)`. Do not store the
failure body or response headers.

### What `auth_id` and `auth_index` contain (verified)

`UsageReporter` copies `AuthID` straight from `auth.ID` and `AuthIndex` from `auth.EnsureIndex()`
(`internal/runtime/executor/helps/usage_helpers.go`).

- **`auth_id` is the auth file name.** For a file-based OAuth credential the watcher synthesizer
  sets `Auth.ID` to the path of the JSON file relative to the auth dir, extension included, e.g.
  `codex-foo@bar.com.json` (`internal/watcher/synthesizer/file.go`; `sdk/auth/filestore.go` uses
  the same rule and additionally sets `Auth.FileName` to it). A credential in a subdirectory of the
  auth dir yields a relative path with separators; a file outside the auth dir falls back to its
  absolute path; on Windows the value is lowercased. **No `auth_file` column is needed.** The one
  exception is credentials that never came from a file: an auth registered without an ID is given a
  UUID (`sdk/cliproxy/auth/conductor_lifecycle.go`), which is what config-derived API-key auths and
  some plugin auths carry.
- **`auth_index` is not a number.** It is `hex(sha256(seed)[:8])`, 16 lowercase hex characters,
  where the seed for a file-based credential is `"<auth type>:<absolute file path>"`
  (`stableAuthIndex` / `indexSeed` in `sdk/cliproxy/auth/types.go`). It is stable for a given file
  at a given absolute path, is not reversible to a name, and changes if the file moves. Treat it as
  an opaque credential fingerprint; use `auth_id` for anything the panel displays.

### Endpoints (management auth, same middleware as the rest of `/v0/management`)

Common query parameters:
- `from`, `to`: RFC3339 or unix milliseconds. Default `to` = now, `from` = `to` − 24 h.
- `tz`: IANA zone for `day`/`hour` bucketing, default `UTC`.
- Filters, each repeatable (OR within a parameter, AND across parameters): `api_key`, `model`,
  `alias`, `auth_id`, `provider`, `auth_type`, `session_id`, `reasoning_effort`, `endpoint`,
  `failed` (`true`/`false`, single), `stream` (`true`/`false`, single).

`GET /v0/management/usage-store/summary`
- `group_by`: comma list, 0–3 of: `api_key`, `model`, `alias`, `auth_id`, `provider`, `auth_type`,
  `endpoint`, `session_id`, `reasoning_effort`, `service_tier`, `stream`, `failed`, `day`, `hour`,
  `week`, `month`. Empty = one totals row.
- `order_by`: any metric name below or any group column; default `total_tokens`. `order`: `asc|desc`
  (default `desc`). `limit`: default 500, max 5000.
- Response:

```json
{
  "from": "2026-09-12T00:00:00Z", "to": "2026-09-13T00:00:00Z", "tz": "UTC",
  "group_by": ["api_key", "model"],
  "rows": [
    {
      "keys": {"api_key": "sk-…", "model": "gpt-5.6-sol"},
      "requests": 120, "failed": 2,
      "input_tokens": 0, "cache_read_tokens": 0, "cache_creation_tokens": 0, "cached_tokens": 0,
      "output_tokens": 0, "reasoning_tokens": 0, "total_tokens": 0,
      "latency_ms_avg": 0, "latency_ms_p95": 0, "ttft_ms_avg": 0,
      "first_at": "…", "last_at": "…"
    }
  ],
  "totals": { "requests": 0, "failed": 0, "...same metrics..." : 0 }
}
```
`day`/`hour`/`week`/`month` keys are emitted as the bucket start in RFC3339 in `tz`. p95 may be
computed in Go from the row's latencies when the bucket is ≤ 50k rows; otherwise omit (null).

`GET /v0/management/usage-store/requests`
- Filters as above, plus `limit` (default 200, max 2000) and `before` (row id cursor). Ordered by
  `ts_ms DESC, id DESC`.
- Response: `{"rows": [ {every column of usage_requests with `ts` as RFC3339 instead of ts_ms} ],
  "next_before": <id or null>}`.

`GET /v0/management/usage-store/meta`
- Response: `{"enabled": true, "path": "…", "retention_days": 0, "rows": n, "oldest": "…"|null,
  "newest": "…"|null, "size_bytes": n, "dimensions": {"api_key": [...], "model": [...],
  "alias": [...], "auth_id": [...], "provider": [...], "auth_type": [...], "reasoning_effort": [...],
  "endpoint": [...]}}` where each dimension lists distinct values seen in the last 90 days.

Errors: 400 with `{"error": "…"}` for bad parameters; 503 `{"error": "usage store disabled"}` when
the store is off.

Tests: plugin writes a record and `summary` grouped by `api_key,model` returns it; filters and
`day` bucketing with a non-UTC `tz`; `requests` cursor pagination; retention prune deletes old rows.
Use a temp-file database in tests.
