# Onyx Ingestor — agent notes

Go CLI that syncs external sources into Onyx via its ingestion API.
Sources live in `internal/<source>` and implement the `ingest.Source`
interface; the pipeline in `internal/ingest` (stats, dry-run, retries via
sink, incremental checkpoints) is source-agnostic.

## Commands

- Build: `make build` (output `bin/onyx-ingestor`)
- Test: `make test` (`go test -race ./...`); vet: `make vet`; lint:
  `make lint` (golangci-lint v2); fmt: `make fmt`
- Secrets: `make secrets` (gitleaks); enable hook once: `make hooks`
- Live canary: `./bin/onyx-ingestor sync confluence --dry-run --max-pages 1`
  (`.env` auto-loads; never commit it)

## Conventions

- Stdlib-first: `flag` subcommands, `log/slog`, `net/http`. Only prod dep
  is `godotenv`. Tests use `stretchr/testify` (`assert`/`require`).
- One package per source under `internal/`; register new sources as a
  `case` in `cmd/onyx-ingestor/main.go` — do not add framework deps.
- Config is env-only, validated up front in `internal/config`; never log
  secrets; report missing vars by name only.
- Fake HTTP transports (`http.RoundTripper`) for unit tests; never hit
  live APIs from tests.
- git: feature branches + PRs; never commit `.env`; don't push to `main`
  or merge PRs without explicit user approval.

## Design tasks

When designing or extending an ingestor (new source, sync semantics,
retry/rate-limit behavior, content mapping), follow
[DESIGN_GUIDANCE.md](DESIGN_GUIDANCE.md) — it encodes the functional and
non-functional requirements (full/incremental sync contract, retry with
fresh connections, idempotent identity mapping, section pre-splitting,
secret hygiene, testing and canary strategy) learned from production
incidents on this project.
