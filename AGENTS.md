# Onyx Ingestor — agent notes

Go CLI that syncs external sources into Onyx via its ingestion API.
Sources live in `internal/<source>` and implement the `ingest.Source`
interface; the pipeline in `internal/ingest` is source-agnostic.

## Commands

- Build: `make build` (output `bin/onyx-ingestor`); fmt: `make fmt`
- Test: `make test` (`go test -race ./...`); vet: `make vet`; lint: `make lint` (golangci-lint v2)
- Secrets scan: `make secrets` (gitleaks)
- Live canary: `./bin/onyx-ingestor sync confluence --dry-run --max-pages 1` (loads `.env`)

## Conventions

- Stdlib-first: `flag` subcommands, `log/slog`, `net/http`; only prod dep is `godotenv` — do not add framework deps. Tests use `stretchr/testify`.
- One package per source under `internal/`; register new sources as a `case` in `cmd/onyx-ingestor/main.go`.
- Config is env-only, validated up front in `internal/config`; report missing vars by name only, never values.
- Unit tests use fake HTTP transports (`http.RoundTripper`); never hit live APIs from tests.
- git: feature branches + PRs; don't push to `main` or merge without explicit user approval.

## Security: this is a public repo

- Never commit secrets, tokens, internal hostnames, AWS account IDs, company names, or local paths. Use placeholders (`example.com`, `OWNER`). The gitleaks hook and CI scan enforce the secret part; the rest is on you.
- Secrets come from env / a Kubernetes Secret (`neotags-secrets`). Don't read other projects' `.env` files into this repo.
- Workflows: pin third-party actions by commit SHA with a version comment; keep permissions minimal; don't add `pull_request` triggers that touch secrets or registries.

## Design tasks

When designing or extending an ingestor — new source, sync semantics,
retry/rate-limit behavior, content mapping — follow
[DESIGN_GUIDANCE.md](DESIGN_GUIDANCE.md).
