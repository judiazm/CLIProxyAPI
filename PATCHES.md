# judiazm patches on top of upstream CLIProxyAPI

This fork carries a short patch series on branch `jd/patches`, rebased onto each upstream release tag.
Every patch must stay small, self-contained, and documented here so a rebase (scripted, or done by an
agent when the script hits conflicts) can preserve its intent without reading the whole diff.

## Patch maintenance across upstream updates

An upstream release can change or overwrite local compatibility code. Do not blindly reapply every
patch on every update. For each release, inspect the upstream diff for an equivalent fix first. If
the behavior is still absent, carry the local patch forward and run its focused regression tests,
the required build/test gate, and the deployment readback checks in the fleet runbook. If upstream
already contains the behavior, drop the duplicate local code while keeping the regression coverage
when it still protects the supported behavior.

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

### Patch 1 note: cloaked IDs (fixed 2026-09-13)

The Claude-format model list and Claude requests carry non-Claude models under a cloaked
`claude-fable-5-dd-<reversed id>` name so Claude Code accepts them. The matcher must compare
patterns against the real ID only; the cloaked form itself never counts as a candidate, or every
disguised model satisfies `claude-*`. Regression test: `TestModelMatchesAllowListIgnoresCloakedClaudeID`.

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

## Rules for local patches

- Keep upstream style (gofmt, logrus, no log.Fatal). Keep changes out of `internal/translator/`.
- Each patch is one small, scoped commit with a clear message (historical commits use `jd:`; newer
  compatibility commits may use the affected component, such as `fix(claude):`).
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

### Details the spec left open, as implemented

- The time window is half-open: `ts_ms >= from AND ts_ms < to`.
- `tz` formats every timestamp in a response, not only bucket keys: `from`/`to`, `first_at`/
  `last_at`, `oldest`/`newest`, and the `ts` of each `requests` row.
- `week` buckets start on Monday. `day`, `hour`, `week` and `month` bucket boundaries are computed
  in Go in `tz`, so they stay correct across DST transitions.
- `stream` and `failed` group keys are emitted as JSON booleans; every other non-time group key is
  the stored string.
- `latency_ms_avg` and `ttft_ms_avg` are plain means over every row in the group, rounded to two
  decimals. Rows with a zero TTFT (non-streaming requests) are included in the TTFT mean.
- `latency_ms_p95` is the nearest-rank percentile, and is `null` for a group of more than 50 000
  rows.
- `group_by` accepts a repeated parameter as well as a comma list. A repeated column is a 400.
- `order_by` accepts a group column that is not in `group_by`; those rows all compare equal and
  keep their insertion order.
- A `limit` above the documented maximum is clamped to it; a non-positive or non-numeric `limit`
  is a 400.
- `next_before` is the last returned row id when the page came back full, otherwise `null`.
- `meta` omits empty strings from each dimension list, and `size_bytes` is the size of the main
  database file, excluding the `-wal` and `-shm` sidecars.
- All three endpoints, `meta` included, answer 503 while the store is disabled.

Tests: plugin writes a record and `summary` grouped by `api_key,model` returns it; filters and
`day` bucketing with a non-UTC `tz`; `requests` cursor pagination; retention prune deletes old rows.
Use a temp-file database in tests.

## Patch 4: preserve caller MCP names from Claude follow-up history

Problem: Claude Code follow-up requests can refer to an MCP tool in message history without
including that tool again in the request's `tools[]` array. When a virtual Claude OAuth MCP server
has the same deterministic name as the caller's server (for example, `fire_joke`), the proxy could
then misread the caller's `mcp__fire_joke__firecrawl_scrape` name as a mangled OAuth alias and return
HTTP 500: `no unique request-local match`.

Change: the Claude request remapper scans the full request JSON, including message history, for MCP
tool names. It records caller-owned names before reverse restoration and excludes aliases already
mapped by the request. This keeps caller MCP tools unchanged when a virtual-server namespace
collision exists and preserves the existing safe rejection for genuinely ambiguous aliases.

Files: `internal/runtime/executor/claude_executor_request.go` and
`internal/runtime/executor/claude_executor_request_remap_test.go`.

Required regression tests: `TestRemapRecordsCallerMCPToolsFromMessageHistory`,
`TestReverseRemapPassesThroughCallerMCPToolsOnVirtualServerCollision`,
`TestRemapKeepsReverseMapEmptyWhenOnlyCallerMCPToolsArePresent`, and
`TestReverseRemapOAuthToolNamesRejectsUnsafeMangledAliases`. Confirm these tests exist before running
them; Go exits successfully when a `-run` pattern matches no tests.

Verification: focused remap/reverse-remap tests pass. The fix was deployed to Vostro from source
commit `bb34469824d37966c5d7fe978c7d1b2ddef98c06`; the live binary SHA-256 is
`d0b071f596df3c935dbfb602b9530b8324306fd13f299e5e3f497562c064646a`, with a pre-deploy backup.
The service is active and authenticated `/v1/models` returns 16 Claude models.

Maintenance: on each upstream update, inspect whether upstream now preserves MCP names found in
message history. Keep this patch only when that behavior is still missing; otherwise remove the
duplicate code and retain the regression test. In either case, run the focused tests, build and
deployment readback before declaring the update usable. A separate Claude account quota/rate-limit
failure can still prevent a live `claude-px --chrome` canary even when this proxy fix is healthy.

## Patch 5: replay pinned Codex conversations after an upstream overload

Problem: a Codex WebSocket conversation pinned to an account could receive an upstream overload
after that account had accepted earlier turns. The retry decision recognized HTTP 401 and 429,
but not 503, preventing the existing full-conversation replay path from handling that overload.

Change: `shouldReplayResponsesWebsocketPinnedAuthFailure` also accepts HTTP 503. The existing replay
path rebuilds the request with conversation history so another eligible account can continue it.
This does not promise recovery when all eligible accounts or the upstream service are unavailable.

Files: `sdk/api/handlers/openai/openai_responses_websocket_forward.go` and
`sdk/api/handlers/openai/openai_responses_websocket_test.go`. Vostro source commit:
`0eb175060eb5f70a87c39f8cd7a13eab35361a23`.

Required regression tests: `TestShouldReplayResponsesWebsocketPinnedAuthFailure` (including the
service-unavailable case) and `TestResponsesWebsocketReplaysImmediatelyAfterPinnedAuthFailure`.
Carry this behavior forward until upstream provides equivalent handling.
