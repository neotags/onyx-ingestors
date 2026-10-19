# Onyx Ingestor

A small Go CLI that pulls content from external sources and ingests it into
[Onyx](https://www.onyx.app/) through the Onyx ingestion API. The first
supported source is **Confluence** (Cloud and Data Center); the CLI is
structured so additional sources can be added as new `sync <source>` commands.

## Quick start

```sh
make build          # produces bin/onyx-ingestor
bin/onyx-ingestor sync confluence --dry-run
```

## Usage

```text
onyx-ingestor sync confluence [--cql CQL] [--page-size N] [--max-pages N] [--dry-run]
                              [--incremental] [--state-file PATH] [--sync-lookback D]
onyx-ingestor version
```

Each run searches Confluence with [CQL](https://developer.atlassian.com/cloud/confluence/advanced-searching-using-cql/),
maps every page to an Onyx ingestion document (title, plain-text body,
deep link, last-modified timestamp), and pushes it to the configured Onyx
connector/credential pair. Pages are pre-split into multiple `sections`:

- On Confluence Cloud, the **Atlas Document Format** (`body.atlas_doc_format`)
  JSON tree is preferred: sections split at heading nodes of level 1-2, with
  paragraphs, lists, tables, code blocks, and expands rendered as plain text.
- If ADF is missing (legacy pages or Data Center), the storage-format HTML
  splitter is used as a fallback (splits at h1/h2 tags).

Output is capped at 64 sections per page so Onyx indexes coherent chunks.

**Attachments**: the Onyx ingestion API is text-only, so binary content is
never uploaded. Instead, each page's file attachments (via the
`children.attachment` expansion) are rendered as a trailing `Attachments:`
section with one line per file — title plus an absolute, authenticated
download URL — so files remain discoverable in search results and deep-linkable
in answers (access itself is still enforced by Confluence). Pages with more
than 25 attachments are truncated (see `TODO(onyx-ingestor)` in
`internal/confluence/attachments.go`). The run stops on the first failed document and
prints stats (`seen`/`ingested`/`skipped`/`failed`) on exit.

## Incremental syncs

`--incremental` syncs only pages modified since the last **successful**
run, using a checkpoint file (`--state-file`, default
`.onyx-ingestor-state.json` in the working directory):

```sh
bin/onyx-ingestor sync confluence --incremental
```

How it works:

- The first run has no checkpoint and performs a full sync, then records
  the run start time. Later runs only stream pages whose
  `version.when` is newer than `last_sync - --sync-lookback` (default 2m;
  the overlap cheaply absorbs Confluence index lag and timestamp ties;
  unchanged pages are skipped by Onyx via `doc_updated_at` anyway).
- Filtering is exact and client-side: CQL is reordered to
  `(CQL) order by lastmodified desc` and pagination stops early once it
  passes the cutoff. Server-side CQL date literals are deliberately not
  used — Confluence parses them in an undocumented tenant/timezone, which
  would silently drop pages.
- The checkpoint advances only when the whole run succeeds, so a failed
  page or network error triggers a larger (but still correct) re-scan next
  time. `--dry-run` never touches the checkpoint.
- If your `--cql` already contains `ORDER BY`, ordering is left untouched
  and early stop is disabled (filtering still applies).

Daily cron example (adjust paths; `godotenv` loads `.env` from the
working directory, so `cd` first):

```cron
0 6 * * * cd /opt/onyx-ingestor && ./bin/onyx-ingestor sync confluence --incremental >> ingest.log 2>&1
```

For rotation keep the checkpoint next to the binary; on Kubernetes run the
same command as a CronJob with a `PersistentVolumeClaim` mounted at the
state-file path.

## Configuration

Configuration comes from environment variables — no secrets are stored
in the repository. If a `.env` file exists in the working directory it is
loaded automatically ([godotenv](https://github.com/joho/godotenv));
variables already set in the real environment take precedence.

| Variable               | Required | Description |
| ---------------------- | -------- | ----------- |
| `CONFLUENCE_BASE_URL`  | yes      | Confluence URL including context path, e.g. `https://example.atlassian.net/wiki` (Cloud) or `https://confluence.example.com` (Data Center). |
| `CONFLUENCE_AUTH_TYPE` | no       | `auto` (default), `basic`, `bearer`, or `scoped`. See "Confluence authentication" below. |
| `CONFLUENCE_CLOUD_ID`  | scoped   | Atlassian cloud ID; required when using a scoped API token. |
| `CONFLUENCE_USERNAME`  | basic    | Auth username (Cloud = account email). Required for basic auth. |
| `CONFLUENCE_TOKEN`     | yes      | Confluence classic API token, scoped API token, or Data Center PAT. |
| `CONFLUENCE_CQL`       | yes*     | CQL query selecting the pages to sync, e.g. `type=page AND space=ENG AND lastModified >= now("-7d")`. Overridden by `--cql`. |
| `ONYX_BASE_URL`        | yes      | Onyx server URL, e.g. `https://onyx.example.com`. |
| `ONYX_API_KEY`         | yes      | Onyx API key (sent as `Authorization: Bearer ...`). |
| `ONYX_CC_PAIR_ID`      | yes      | Integer ID of the Onyx connector/credential pair to ingest into. |
| `ONYX_INGESTION_PATH`  | no       | Ingestion endpoint path (default `/onyx-api/ingestion`). Set to your deployment's mount path when it differs, e.g. `/api/ingestion` behind the chart nginx (it only forwards `/api/*`). |
| `HTTP_TIMEOUT`         | no       | HTTP timeout as Go duration (default `30s`). |

Copy `.env.example` to `.env` for a local template; `.env` is git-ignored
and picked up automatically, so `source .env` is not needed.

## Confluence authentication

Three auth modes are supported; `CONFLUENCE_AUTH_TYPE=auto` picks one from
the values you set:

| Mode     | `CONFLUENCE_AUTH_TYPE` | Credential setup | Requests go to |
| -------- | ---------------------- | ---------------- | -------------- |
| Classic API token | `basic`  | `CONFLUENCE_USERNAME` (account email) + `CONFLUENCE_TOKEN` | Site URL with HTTP basic auth |
| Data Center PAT   | `bearer` | `CONFLUENCE_TOKEN` only | Site URL with `Authorization: Bearer` |
| Scoped API token  | `scoped` | `CONFLUENCE_TOKEN` + `CONFLUENCE_CLOUD_ID` | Atlassian gateway `https://api.atlassian.com/ex/confluence/{cloudID}/...` with `Authorization: Bearer` |

Auto resolution: cloud ID set → `scoped`; username set → `basic`; otherwise
`bearer`. Set `CONFLUENCE_AUTH_TYPE` explicitly to force a mode. Only scoped
tokens need the cloud ID — find it via
`curl https://<site>.atlassian.net/_edge/tenant_info` (field `cloudId`).

## Onyx API notes

Documents are pushed to `POST <ONYX_BASE_URL>/onyx-api/ingestion` with a
payload of `{ "document": {...}, "cc_pair_id": N }`. This matches the Onyx
ingestion API; if your deployment reports an unexpected 404/422, verify the
endpoint path and schema against your Onyx version and adjust
`internal/onyx/client.go` accordingly (`TODO(onyx-ingestor)`).

## Layout

```text
cmd/onyx-ingestor/      CLI entry point; registers each `sync <source>` command
internal/config/        validated env-based configuration
internal/ingest/        source-agnostic pipeline: Source/Sink interfaces, stats, dry-run,
                        incremental sync checkpoints (state.go)
internal/onyx/          Onyx ingestion API client (implements ingest.Sink)
internal/confluence/
  client.go             Confluence REST client (CQL search, auth modes, pagination)
  source.go             ingest.Source implementation: CQL pagination + page mapping
  adf.go                Atlas Document Format (JSON) parsing, section splitting, text render
  attachments.go        attachment list -> trailing text section with download links
  sections.go           storage HTML -> section splitting at h1/h2 boundaries (fallback)
  html.go               rough storage-format HTML -> text converter (fallback)
```

## Adding a source

The pipeline is source-agnostic. To add e.g. Google Drive ingestion:

1. Create `internal/gdrive/` with a small API client (`client.go`)
   and a type implementing `ingest.Source` (`Stream(ctx, emit) error`) that
   fetches, maps to `onyx.Document`, and emits.
2. Add its env vars to `internal/config/config.go` (validate, never echo secrets).
3. Add a `case "gdrive":` branch in `cmd/onyx-ingestor/main.go` `runSync`
   that parses source-specific flags and builds the source.

`internal/ingest` stays untouched: it only knows the `Source`/`Sink`
interfaces, so stats, dry-run, cancellation, and error handling come for free.

## Development

```sh
make test   # go test -race ./...
make vet    # go vet ./...
make tidy   # go mod tidy -diff
make lint   # golangci-lint run (skipped if not installed)
make fmt    # go fmt ./...
make secrets # gitleaks scan of git-tracked content (.env never enters git)
make hooks  # enable .githooks/pre-commit (gitleaks on staged changes)
```

### Secret scanning

[gitleaks](https://github.com/gitleaks/gitleaks) guards the repo:

- `.gitleaks.toml` extends the default rules and allowlists the synthetic
  fixtures used in tests (e.g. `super-secret-token`).
- Run `make hooks` once after cloning to point `core.hooksPath` at
  `.githooks/`; the `pre-commit` hook then runs
  `gitleaks git --staged .` and blocks commits containing secrets
  (a missing gitleaks binary logs a warning and skips).
- `make secrets` scans the git history/staged content on demand. Because
  `.env` is git-ignored it is out of scope; do not allowlist it — if it is
  ever force-added, the scan will (and should) fail.

Never commit `.env` — it is git-ignored; `.env.example` is the committed
template and must only ever contain placeholders.

The module path is currently `onyx-ingestor`. Rename it to your Git host
path (e.g. `gitlab.example.com/aiops/onyx-ingestor`) before pushing, with a
single `sed` across `go.mod` and the `internal/` imports.

## Notes

- Dependencies are minimal: stdlib (`log/slog`, `net/http`, `flag`,
  `testing`) plus `github.com/joho/godotenv` for `.env` auto-loading.
- Confluence page bodies are converted to rough plain text
  (`internal/confluence/html.go`); swap in a full HTML converter if fidelity
  to Confluence storage format matters.
- TODO(onyx-ingestor): CI pipeline, container image, and deployment chart
  are intentionally not scaffolded yet.
