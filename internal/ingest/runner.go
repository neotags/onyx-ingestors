// Package ingest is the source-agnostic ingestion pipeline: it streams
// documents from any source into a sink (Onyx) with stats and error handling.
package ingest

import (
	"context"
	"fmt"
	"log/slog"

	"onyx-ingestor/internal/onyx"
)

// Source streams Onyx documents. Sources are responsible for fetching and
// mapping their own content; they must honor context cancellation and abort
// the stream when emit returns an error.
type Source interface {
	// Stream calls emit for each document, in source order. It must stop
	// and return the error if emit fails.
	Stream(ctx context.Context, emit func(onyx.Document) error) error
}

// Sink ingests a single Onyx document.
type Sink interface {
	Ingest(ctx context.Context, doc onyx.Document) error
}

// Options controls one ingestion run.
type Options struct {
	DryRun bool
}

// progressEvery controls how often in-run progress lines are logged.
const progressEvery = 25

// Stats summarizes a finished run.
type Stats struct {
	Seen     int
	Ingested int
	Skipped  int
	Failed   int
}

// Run streams documents from source into sink until the stream ends, the
// sink fails, or the context is canceled. The first failure aborts the run.
func Run(ctx context.Context, source Source, sink Sink, opts Options) (Stats, error) {
	var stats Stats
	err := source.Stream(ctx, func(doc onyx.Document) error {
		stats.Seen++
		if opts.DryRun {
			stats.Skipped++
			slog.Info("dry run: mapped document", "doc_id", doc.ID, "title", doc.Title, "source", doc.Source)
			return nil
		}
		if err := sink.Ingest(ctx, doc); err != nil {
			stats.Failed++
			return fmt.Errorf("document %q (%q): %w", doc.ID, doc.Title, err)
		}
		stats.Ingested++
		if stats.Seen%progressEvery == 0 {
			slog.Info("ingest progress",
				"seen", stats.Seen, "ingested", stats.Ingested, "failed", stats.Failed)
		}
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("sync: %w", err)
	}
	return stats, nil
}
