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

// ResumableSource can pause and resume pagination mid-stream so an
// interrupted run continues instead of re-scanning. Callers type-assert
// Source values against it.
type ResumableSource interface {
	Source
	// ResumeFrom positions the next Stream call at cursor instead of the
	// start. The cursor must have been produced by the same query scope.
	ResumeFrom(cursor string)
	// SetCursorHook registers a callback invoked after each successfully
	// fetched batch with the cursor a future run may resume from.
	SetCursorHook(hook func(cursor string))
}

// Sink ingests a single Onyx document.
type Sink interface {
	Ingest(ctx context.Context, doc onyx.Document) error
}

// Options controls one ingestion run.
type Options struct {
	DryRun bool
	// MaxFailures tolerates failed documents: up to MaxFailures sink
	// failures are logged and skipped before the run aborts. Values <= 0
	// abort on the first failure.
	MaxFailures int
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
// failure budget is exhausted, or the context is canceled. Skipped
// failures count toward Stats.Failed so callers can refuse to advance
// checkpoints on dirty runs.
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
			if stats.Failed <= opts.MaxFailures {
				slog.Error("document failed; continuing",
					"doc_id", doc.ID, "title", doc.Title,
					"failures", stats.Failed, "budget", opts.MaxFailures,
					"error", err)
				return nil
			}
			return fmt.Errorf("document %q (%q): %w", doc.ID, doc.Title, err)
		}
		stats.Ingested++
		if stats.Seen%progressEvery == 0 {
			slog.Info("ingest progress", progressAttrs(stats, doc)...)
		}
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("sync: %w", err)
	}
	return stats, nil
}

// progressAttrs adds the title and link of the most recently ingested
// document so long runs can be followed by page, not just counters. The
// link is taken from the first section that has one.
func progressAttrs(stats Stats, doc onyx.Document) []any {
	title := doc.Title
	if title == "" {
		title = doc.SemanticIdentifier
	}
	attrs := []any{"seen", stats.Seen, "ingested", stats.Ingested, "failed", stats.Failed, "title", title}
	for _, s := range doc.Sections {
		if s.Link != "" {
			return append(attrs, "link", s.Link)
		}
	}
	return attrs
}
