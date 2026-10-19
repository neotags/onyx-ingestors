# Ingestor Design Guidance

Guidance for designing a new ingestor (a `sync <source>` command) for this
project. It distills what we learned building the Confluence ingestor,
including real incidents — read the "Lessons from production" section
before finalizing any design.

## Principles

1. **Idempotent by construction.** Every document has a stable,
   deterministic ID (`<source>-<native-id>`) and a source-modified
   timestamp (`doc_updated_at`). Re-ingesting unchanged content must be
   safe and cheap: upsert semantics let Onyx skip unchanged docs on its
   side, and make resume-after-crash free.
2. **Resumable, not transactional.** The pipeline streams documents one
   at a time; a failure mid-run leaves ingested docs in place. Design so
   the next run converges, never so it "rolls back".
3. **Observable.** Structured logs (slog), progress every N docs, and
   end-of-run stats (seen/ingested/skipped/failed) are the minimum. An
   operator must be able to answer "is it ingesting, how many docs, did
   it fail" from logs alone.
4. **Least privilege & zero secret leakage.** Auth tokens come only from
   the environment / `.env` (git-ignored), are never logged or echoed in
   errors, and secret scanning (gitleaks) guards commits and CI.

## Functional design

### Sync modes: full and incremental

Every source must ship with both:

- **Full sync** (default): streams everything matched by the source
  query. Must be safe to run at any time — idempotency makes it cheap.
- **Incremental sync** (`--incremental`): streams only items modified
  since the last *fully successful* run.

Checkpoint contract (implemented by `internal/ingest/state.go`, reuse it):

- Source-agnostic JSON: `{ "source": "<name>", "last_sync": "<RFC3339>" }`.
- Atomic write (tmp + rename), mode `0600`.
- Advance **only when the entire run completes cleanly** — a single
  failed document keeps the old timestamp, so the next run re-scans the
  whole window. This trades a small re-scan for never missing content.
- `--dry-run` never writes the checkpoint.
- No checkpoint ⇒ full-sync bootstrap, then record the run start time.
- Capture the checkpoint timestamp at run *start*, so content modified
  during the run lands in the next window.
- Subtract a small configurable lookback (default 2m) from the stored
  timestamp to absorb source-side index lag and timestamp ties.

### Choosing the change-detection mechanism — carefully

Prefer, in order:

1. **Exact client-side filtering on machine timestamps** (e.g.
   Confluence `version.when`, RFC 3339 with offset) while enumerating the
   source in *modified-descending order* so pagination can stop early
   once past the cutoff. This is what the Confluence source does and is
   the recommended pattern.
2. **Server-side date filters**, only after empirically verifying their
   semantics on the live tenant. Confluence CQL parses `yyyy-MM-dd HH:mm`
   literals in an undocumented tenant/profile timezone (measured: UTC+8
   on our tenant) — filtering server-side would have silently dropped
   pages edited in the prior ~8 hours. Do not trust docs alone; probe
   with known-boundary queries before relying on a server filter.
3. **Status/webhook-driven** change feeds where available — still keep
   the checkpointed full-fallback path for reconciliation.

Design sources so over-fetching (a too-wide window) is merely wasteful,
never incorrect; under-fetching is a silent data-loss bug.

### Enumeration & pagination

- One request page at a time, honoring context cancellation on every
  network call.
- Detect termination by the API's own contract (short page *or* missing
  next link — Confluence needs both).
- Keep page size configurable (`--page-size`, capped at the API max).
- For incremental runs, ordering is only an optimization (early stop);
  correctness must come from the exact filter. If the caller controls
  ordering (user-supplied ORDER BY), fall back to full scan + filter.

### Content mapping

- Prefer the source's **structured** document format over rendered HTML:
  Confluence Cloud returns Atlas Document Format (ADF) JSON, which we
  parse directly; storage-format HTML is only a fallback. Verify what the
  API actually returns before writing converters.
- **Pre-split each item into sections** at structural boundaries
  (h1/h2 headings) so the search index gets coherent chunks; cap the
  section count per document (64 here).
- Rich content (tables, code blocks, expands) should degrade to clean
  plain text, not markup noise.
- **Binaries/attachments**: know your sink. The Onyx ingestion API is
  text-only, so attachments are rendered as a trailing `Attachments:`
  section listing title + authenticated download URL — files stay
  discoverable and deep-linkable while access control stays at the
  source. Document this trade-off explicitly wherever you apply it.
- Emit the sink's **complete schema**. Onyx returns 422 for *missing
  required keys with null values* (e.g. `primary_owners`); send every
  field the API defines, nulls included.

## Non-functional design

### Rate limits & transient failures

- Bounded retries (5 attempts) with jittered exponential backoff
  (start ~500ms, cap ~30s). Retry transport errors, 429, 502/503/504;
  fail anything else immediately. Honor `Retry-After` when present.
- **Use a fresh connection per retry.** Reusing keep-alive connections
  got us a mid-run `EOF` after ~1000 requests when the server's
  per-connection keep-alive budget ran out — the request never reached
  the application. Disable keep-alives on retry attempts (or cap
  `MaxConnsPerHost`/idle reuse conservatively).
- Retries live in the sink client, not the pipeline, so every source
  gets them for free.

### Error handling policy

- Abort on the **first** document failure, wrapping the error with
  document ID and title — with idempotent resume, failing fast is safer
  than continuing past a systematic error (bad schema, down server).
- Fail fast on configuration: validate all env vars up front and report
  every missing one *by variable name only* (never values).
- Non-zero exit code on any failure so cron/CI detects it.

### Security & configuration

- Env-only config; `.env` auto-loaded without overriding real env vars.
- Support every credential flavor the source realistically has (for
  Confluence: classic basic token, Data Center bearer PAT, scoped
  gateway token) with auto-resolution plus an explicit override, and
  validate mode-specific requirements (e.g. scoped ⇒ cloud ID).
- Secrets live only in git-ignored files; gitleaks runs in pre-commit
  and CI. Test fixtures with fake tokens go in the gitleaks allowlist,
  never real ones.
- Respect `HTTP_TIMEOUT`; cancel cleanly on SIGINT/SIGTERM.

### Performance

- Stream; never materialize the whole result set.
- Parallelism is optional — if added, preserve per-document error
  attribution and respect source rate limits first.

## Testing strategy

- Unit tests with fake HTTP transports (`http.RoundTripper` stubs) — no
  live network. Testify `assert`/`require` is the house style.
- Cover: mapping (including structured-format and fallback paths),
  pagination termination, incremental boundary conditions (past cutoff,
  within lookback, missing timestamps ⇒ keep, early-stop ordering,
  user-supplied ordering), retry behavior, and checkpoint save/load
  (round-trip, corrupt file, missing file, 0600 perms).
- Provide operator canaries: `--dry-run` and `--max-pages N` for a
  bounded live check before any bulk run. Always canary against staging
  before the first full import.

## New-ingestor checklist

1. **Identity**: define stable doc ID scheme + `doc_updated_at` source.
2. **Enumeration**: query/selection mechanism, pagination contract,
   modified-desc ordering available? (Build probe snippets and verify
   empirically.)
3. **Auth**: credential modes, auto-resolution, per-mode validation.
4. **Config**: namespaced env vars, validated in `internal/config`.
5. **Mapping**: structured format first; section splitting with cap;
   attachment/binary policy decision documented.
6. **Sink compliance**: confirm required schema keys against the live
   Onyx version (422 probe).
7. **Resilience**: gets retry/backoff + fresh-connection retries free
   from the sink client; verify source client timeouts + cancellation.
8. **Sync modes**: wire `ingest.State` checkpoint (CLI flags
   `--incremental`/`--state-file`/`--sync-lookback`); incremental filter
   design per "Choosing the change-detection mechanism".
9. **Tests**: fake-transport unit tests incl. incremental edges; lint,
   `go vet`, `-race` in CI.
10. **Canary**: `--dry-run --max-pages 1` live, then a small bounded real
    run, then full bootstrap; seed the checkpoint.
11. **Ops docs**: cron/CronJob example (checkpoint needs persistence —
    PVC on k8s), log/verification path.

## Lessons from production (Confluence)

| Incident | Root cause | Design rule |
| --- | --- | --- |
| Mid-run `EOF` after 1003 docs | server keep-alive per-connection budget exhausted; retried on the same dead conn | fresh connection per retry attempt |
| 422 on every ingest | schema omitted keys Onyx requires (`primary_owners` etc.) | emit the full sink schema, nulls included |
| 404 on ingestion endpoint | deployment mounts the API at `/api/ingestion`, not the default path | endpoint path configurable (`ONYX_INGESTION_PATH`) |
| Near-miss: pages silently excluded from incremental | CQL date literals parse in tenant timezone (UTC+8), not UTC | exact client-side time filter; server filters only after empirical verification |
| Full import of 1003 docs in ~4.5 min | unchanged-doc skips via `doc_updated_at` made re-runs cheap (seconds) | stable IDs + source timestamps ⇒ idempotent upserts |

