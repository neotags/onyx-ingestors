# Confluence ingestor: Onyx connector comparison & improvement plan

Analysis of the upstream Onyx Confluence connector
(`backend/onyx/connectors/confluence/`, ~6k lines) vs. this repo's
standalone ingestor (`internal/confluence`, ~800 lines), producing a
prioritized improvement plan. Line numbers reference upstream files.

## How Onyx does it (mechanics)

- **Transport** (`source_operations.py`): every API call goes through a
  `_OnyxConfluence` wrapper around `atlassian-python-api` with credential
  refresh and `_call_with_retries` — 5 attempts, honors the `Retry-After`
  header on 429, treats Server-side 403 as rate limiting (7 x 10s
  retries), exponential backoff (5s→60s) on 5xx, total-call timeout 600s
  (`utils.py:_handle_http_error`).
- **Pagination** (`source_operations.py:_paginate_url`): Cloud follows
  `_links.next` verbatim (cursor); Server rewrites `start` offsets. Stops
  on an empty batch even when `next` is present ("Confluence bug"),
  rewrites any problematic `expand` params and retries the same URL, and
  on 5xx halves `limit` (1000→…→5) before giving up; Server falls back to
  one-by-one offset fetching as a last resort.
- **Pagination checkpoint**: `ConfluenceCheckpoint{next_page_url}` is
  updated per result batch, so a crashed run resumes from the last cursor,
  not from the start.
- **CQL construction** (`connector.py`): scope CQL (`space` / `ancestor` /
  page-id / raw CQL) + `label not in (...)` exclusion (env
  `CONFLUENCE_CONNECTOR_LABELS_TO_SKIP`) + **server-side** time filter
  (`lastmodified >= '<since>'` with a configurable timezone offset), then
  `order by lastmodified asc`. Asc order keeps cursor checkpoints stable
  under a moving cutoff.
- **Incremental robustness**: `load_from_checkpoint` widens the `end`
  window by one day and retries once with a 1h offset when Atlassian
  rejects a date (DST edge).
- **Page→document** (`connector.py:_convert_page_to_document`): one
  `Document` per page, text extracted from **storage HTML** with
  BeautifulSoup (`utils.py:extract_text_from_confluence_html`): strips
  macro styling, unwraps date lozenges, replaces `ri:user` mentions with
  `@DisplayName` (user lookup, cached), resolves `include` macros by
  recursively inlining referenced pages (cycle-guarded by title set),
  inline links → `(LINK TEXT: …)`, inline attachments → placeholder tags.
  Metadata: space name, labels, last editor (`version.by`), and
  `doc_created_at` from `history.createdDate`.
- **Comments**: fetched per page (`type=comment and container=<id>`) and
  appended as a second section linked `<page>#comments`.
- **Attachments**: queried via CQL (`type=attachment and container=<id>`,
  cursor-paginated), downloaded, and text-extracted (pdf/docx/…) as
  **separate documents** carrying `parent_page_id`; type allow-list, size
  and char-count thresholds; images deferred to a vision pipeline when
  enabled.
- **Failure isolation**: a page that fails to convert yields a
  `ConnectorFailure` and the run continues; optionally the whole connector
  continues on repeated failures (`CONTINUE_ON_CONNECTOR_FAILURE`).
- **Out of scope for us**: permission/EE external-access sync, hierarchy
  nodes (spaces/ancestors), slim-document perm sync, OAuth app refresh —
  none of these apply to the ingestion-API path.

## Where we stand

Already at or beyond parity:

- **Cursor pagination**: we follow `_links.next` verbatim, terminate on
  missing link / empty batch / all-duplicate batch, and dedup page IDs per
  run. Same contracts Onyx relies on, plus per-run dedup they don't have.
- **ADF-first parsing**: we parse the structured Atlas Document Format and
  split sections by headings; Onyx only has HTML extraction, and "segment
  into sections" is literally their `#1` unmet TODO.
- **Incremental exactness**: client-side exact filter on `version.when`,
  no server-side date-literal timezone lottery. Onyx needs a config knob
  plus a DST retry dance to paper over the same problem.
- **Up-front page count** logged before streaming (`totalSize` via
  `/rest/api/search`); Onyx has no equivalent.

Gaps Onyx covers that we don't (feed the plan below).

## Improvement plan

### P0 — reliability of a single run

1. **Retry engine in `client.doSearch`/`CountPages`** — up to 5 attempts
   on 429 and 5xx, honor `Retry-After` (clamp 2–60s), else exponential
   backoff (5s, ×2, max 60s); treat network errors the same. One
   transient blip currently kills a multi-minute sync at arbitrary depth.
2. **Progressive page-size reduction on 5xx** — on 500/502/503/504,
   halve `limit` (min 5) for the current URL and retry, mirroring
   `_paginate_url`; Cloud 504s on deep windows are common.
3. **Per-page failure isolation** — conversion/emit failures become
   recorded failures (page ID + error, surfaced in the final stats/log)
   while the run continues; a hard failure limit (e.g. 10% or 50) aborts.
   Today one mappable-to-error page aborts everything scanned so far.
4. **Resumable cursor checkpoint** — persist `(cql, window, next_url)` to
   the state file per batch (exactly Onyx's
   `ConfluenceCheckpoint.next_page_url`); on restart, resume mid-list
   instead of re-scanning the window. Pairs with the existing rule that
   checkpoints only advance on a clean run (resume marker is separate
   from `last_sync`).

### P1 — content coverage

5. **Comments ingestion** — per page, CQL `type=comment and container=<id>`
   expand `body.storage.value`, render as plain text appended under a
   synthetic heading, section link `<page>#comments`. Cheap, big recall
   boost for discussion-heavy spaces.
6. **Rich page metadata** — extend `expand` with
   `space,metadata.labels,history`; map space name, labels, last editor
   (`version.by.displayName`) into document metadata, and use
   `history.createdDate` once the ingestion API accepts a created-at
   field.
7. **Label exclusion config** — `CONFLUENCE_LABELS_TO_SKIP` →
   `label not in ('a','b')` appended to the CQL (port of
   `build_label_filter`).
8. **Attachments beyond name-listing** — decision point: check whether the
   Onyx ingestion API accepts binary/file documents. If yes: fetch
   attachments via `type=attachment and container=<id>` (cursor
   pagination, fixing the current 25-item `children.attachment`
   truncation), size/type gates, and upload as separate documents linked
   by `parent_page_id`. If no: keep the reference list but paginate it so
   it is complete, and render size/type where available.

### P2 — fidelity & scale

9. **Include-macro resolution** — in the storage-HTML fallback, recurse
   into `ac:structured-macro[include]` referenced pages with a
   visited-set cycle guard (ADF path: equivalent handling for extension
   nodes if they occur in practice).
10. **Mention/user rendering** — replace `ri:user` with `@DisplayName`
    via a small cached lookup, only when mentions appear in the fetched
    body.
11. **Bounded server-side prefetch filter for large tenants** — keep the
    exact client-side cutoff (correctness) but add an intentionally broad
    server-side `lastmodified >= '<since - lookback - 1d>'` + `order by
    lastmodified asc` to skip definitively-old pages once spaces grow to
    tens of thousands of pages. The wide margin keeps the timezone edge
    from ever deleting data.
12. **Progress as "seen/total"** — pass the up-front `totalSize` into the
    runner's progress log for a completion percentage.

### Explicit non-goals (cost >> value here)

- Permission/restriction sync, hierarchy nodes, OAuth app flows — require
  server-side Onyx components the ingestion API does not expose.
- Server/Data-Center one-by-one offset fallback — only worth adding if a
  DC deployment materializes; the code path is Cloud-first today.

### Suggested sequencing

P0 1→2 (both touch the same transport function), then 3→4 (both touch
`Source.Stream` + runner stats). P1 items are independent; 5 and 7 are
smallest. Re-evaluate P2 after one production quarter of logs.
