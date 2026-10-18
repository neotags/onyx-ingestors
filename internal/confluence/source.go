package confluence

import (
	"context"
	"fmt"
	"time"

	"onyx-ingestor/internal/onyx"
)

// Source streams Confluence pages matching a CQL query as Onyx documents.
// It implements ingest.Source (in internal/ingest) structurally; importing
// that package is intentionally avoided so sources stay decoupled.
type Source struct {
	client   *Client
	cql      string
	pageSize int
	maxPages int
}

// NewSource creates a Source over client for cql. pageSize<=0 defaults to
// 50; maxPages<=0 means no limit.
func NewSource(client *Client, cql string, pageSize, maxPages int) *Source {
	return &Source{client: client, cql: cql, pageSize: pageSize, maxPages: maxPages}
}

// Stream paginates the CQL search and emits one Onyx document per page.
// It stops when emit returns an error, when maxPages is reached, or when
// pagination is exhausted.
func (s *Source) Stream(ctx context.Context, emit func(onyx.Document) error) error {
	pageSize := s.pageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	start := 0
	for pageNum := 0; ; pageNum++ {
		if s.maxPages > 0 && pageNum >= s.maxPages {
			return nil
		}
		res, err := s.client.SearchPages(ctx, s.cql, start, pageSize)
		if err != nil {
			return fmt.Errorf("fetch confluence pages: %w", err)
		}
		for _, page := range res.Results {
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
