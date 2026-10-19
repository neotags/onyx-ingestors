package confluence

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"onyx-ingestor/internal/onyx"
)

// scanGrace is how far past the incremental cutoff pagination continues,
// absorbing second-level ordering ties and index skew before stopping.
const scanGrace = time.Minute

var orderByRE = regexp.MustCompile(`(?i)\border\s+by\b`)

// Source streams Confluence pages matching a CQL query as Onyx documents.
// It implements ingest.Source (in internal/ingest) structurally; importing
// that package is intentionally avoided so sources stay decoupled.
type Source struct {
	client   *Client
	cql      string
	pageSize int
	maxPages int
	// since, when non-zero, filters out pages last modified at or before
	// it (exact comparison on version.when) and stops pagination early.
	since time.Time
}

// NewSource creates a Source over client for cql. pageSize<=0 defaults to
// 50; maxPages<=0 means no limit.
func NewSource(client *Client, cql string, pageSize, maxPages int) *Source {
	return &Source{client: client, cql: cql, pageSize: pageSize, maxPages: maxPages}
}

// NewIncrementalSource creates a Source that only emits pages modified
// after since. The search is ordered by lastmodified desc so pagination
// stops once it passes the cutoff. Filtering happens client-side on each
// page's version.when (exact RFC 3339 timestamps): Confluence parses CQL
// date literals in an undocumented tenant/profile timezone, so a
// server-side date filter cannot be made correct in general.
func NewIncrementalSource(client *Client, cql string, since time.Time, pageSize, maxPages int) *Source {
	s := NewSource(client, cql, pageSize, maxPages)
	s.since = since
	return s
}

// incrementalCQL returns the query to run and whether it is ordered by
// lastmodified desc. A user-supplied ORDER BY is left untouched (no early
// stop is possible then).
func incrementalCQL(cql string) (string, bool) {
	if orderByRE.MatchString(cql) {
		return cql, false
	}
	return "(" + cql + ") order by lastmodified desc", true
}

// Stream paginates the CQL search and emits one Onyx document per page.
// It stops when emit returns an error, when the incremental cutoff is
// passed, when maxPages is reached, or when pagination is exhausted.
func (s *Source) Stream(ctx context.Context, emit func(onyx.Document) error) error {
	pageSize := s.pageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	cql, earlyStop := s.cql, false
	if !s.since.IsZero() {
		cql, earlyStop = incrementalCQL(cql)
	}
	start := 0
	for pageNum := 0; ; pageNum++ {
		if s.maxPages > 0 && pageNum >= s.maxPages {
			return nil
		}
		res, err := s.client.SearchPages(ctx, cql, start, pageSize)
		if err != nil {
			return fmt.Errorf("fetch confluence pages: %w", err)
		}
		for _, page := range res.Results {
			if s.tooOld(page) {
				if earlyStop && page.Version.When.Before(s.since.Add(-scanGrace)) {
					return nil
				}
				continue
			}
			if err := emit(MapPage(page)); err != nil {
				return fmt.Errorf("ingest page %s (%q): %w", page.ID, page.Title, err)
			}
		}
		start += len(res.Results)
		if len(res.Results) < pageSize || res.Links.Next == "" {
			return nil
		}
	}
}

// tooOld reports whether the page is excluded by the incremental cutoff.
// Pages without a version timestamp are kept: their age cannot be ruled
// out, so it is safer to re-ingest them.
func (s *Source) tooOld(page Page) bool {
	return !s.since.IsZero() && !page.Version.When.IsZero() && !page.Version.When.After(s.since)
}

// MapPage converts a Confluence page into an Onyx ingestion document.
func MapPage(page Page) onyx.Document {
	var updated *time.Time
	if !page.Version.When.IsZero() {
		when := page.Version.When
		updated = &when
	}
	link := page.WebURL()
	// Prefer the structured ADF payload when present (Confluence Cloud);
	// fall back to the storage-format HTML splitter otherwise.
	texts := adfToSections(page.Body.AtlasDocFormat.Value)
	if texts == nil {
		texts = splitSections(page.Body.Storage.Value)
	}
	sections := make([]onyx.Section, 0, len(texts))
	for _, txt := range texts {
		sections = append(sections, onyx.Section{Text: txt, Link: link})
	}
	if att := attachmentsText(page); att != "" {
		if len(sections) < maxSections {
			sections = append(sections, onyx.Section{Text: att, Link: link})
		} else {
			sections[len(sections)-1].Text += "\n\n" + att
		}
	}

	return onyx.Document{
		ID:                 "confluence-" + page.ID,
		Sections:           sections,
		Source:             "confluence",
		SemanticIdentifier: page.Title,
		Metadata: map[string]string{
			"confluence_id": page.ID,
		},
		DocUpdatedAt:     updated,
		Title:            page.Title,
		FromIngestionAPI: true,
	}
}
