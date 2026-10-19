package confluence

import (
	"context"
	"fmt"
	"log/slog"
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
//
// Pagination follows the relative _links.next URL verbatim: Cloud's
// scoped-token gateway paginates CQL search with an opaque cursor and
// ignores the start offset, so recomputing offsets client-side just
// refetches the first batch forever. Already-emitted page IDs are skipped
// anyway, and a batch with no new pages ends the stream — belt and braces
// against the index shifting under a live search.
func (s *Source) Stream(ctx context.Context, emit func(onyx.Document) error) error {
	pageSize := s.pageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	cql, earlyStop := s.cql, false
	if !s.since.IsZero() {
		cql, earlyStop = incrementalCQL(cql)
	}
	next := ""
	seenIDs := make(map[string]struct{})
	for pageNum := 0; ; pageNum++ {
		if s.maxPages > 0 && pageNum >= s.maxPages {
			return nil
		}
		var res SearchResult
		var err error
		if next == "" {
			res, err = s.client.SearchPages(ctx, cql, 0, pageSize)
		} else {
			res, err = s.client.SearchPageURL(ctx, next)
		}
		if err != nil {
			return fmt.Errorf("fetch confluence pages: %w", err)
		}
		newPages := 0
		for _, page := range res.Results {
			if _, dup := seenIDs[page.ID]; dup {
				continue
			}
			seenIDs[page.ID] = struct{}{}
			newPages++
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
		next = res.Links.Next
		if len(res.Results) == 0 || next == "" {
			return nil
		}
		if newPages == 0 {
			slog.Info("confluence search returned only previously seen pages; stopping pagination",
				"page_num", pageNum)
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

	metadata := map[string]string{"confluence_id": page.ID}
	if link != "" {
		metadata["confluence_url"] = link
	}

	return onyx.Document{
		ID:                 "confluence-" + page.ID,
		Sections:           sections,
		Source:             "confluence",
		SemanticIdentifier: page.Title,
		Metadata:           metadata,
		DocUpdatedAt:       updated,
		Title:              page.Title,
		FromIngestionAPI:   true,
	}
}
