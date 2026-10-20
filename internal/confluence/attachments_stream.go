package confluence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"onyx-ingestor/internal/extract"
	"onyx-ingestor/internal/onyx"
)

// AttachmentOptions gates the attachment stream.
type AttachmentOptions struct {
	Enabled  bool
	Types    map[string]bool // lowercase, dot-less; empty map = extract.DefaultTypes
	MaxBytes int64           // download cap; <=0 unlimited
	MaxChars int             // converted-text cap; <=0 unlimited
}

const (
	// attachmentPageSize stays below the page search's 50: attachment
	// batches are lighter metadata, and the volume (~3x pages on this
	// tenant) makes fewer, larger round trips worthwhile.
	attachmentPageSize = 100
	// attachmentProgressEvery logs a heartbeat so long attachment scans
	// don't look frozen between page-level progress lines.
	attachmentProgressEvery = 500
)

// SetAttachments enables attachment ingestion after the page stream. Page
// pagination and its resume cursor are untouched; attachments re-scan every
// run but are filtered by the same incremental cutoff, so steady-state runs
// only download what changed.
func (s *Source) SetAttachments(opts AttachmentOptions) {
	if opts.Types == nil {
		opts.Types = map[string]bool{}
		for _, t := range extract.DefaultTypes {
			opts.Types[t] = true
		}
	}
	s.attachments = opts
	s.attachSet = true
}

// streamAttachments emits one Onyx document per downloadable attachment in
// the source's scope. It runs after the page stream so every attachment's
// parent page is already indexed. Conversion and download failures are
// logged and skipped — P1's contract is best-effort coverage, never
// blocking page ingestion on one corrupt file.
//
// The attachment CQL is the page CQL with every "type=page" swapped for
// "type=attachment": space scoping, ancestor filters and label exclusions
// all carry over untouched.
func (s *Source) streamAttachments(ctx context.Context, emit func(onyx.Document) error) error {
	cql := attachmentCQL(s.cql)
	if cql == "" {
		return nil // scope contains no type=page to rewrite; nothing to do
	}
	ordered := false
	if !s.since.IsZero() {
		cql, ordered = incrementalCQL(cql)
	}
	seen := make(map[string]struct{})
	scanned, emitted, skipped := 0, 0, 0
	next := ""
	for pageNum := 0; ; pageNum++ {
		var res AttachmentSearchResult
		var err error
		if next == "" {
			res, err = s.client.SearchAttachments(ctx, cql, attachmentPageSize)
		} else {
			res, err = s.client.SearchAttachmentPageURL(ctx, next)
		}
		if err != nil {
			return fmt.Errorf("fetch confluence attachments: %w", err)
		}
		fresh := 0
		for _, att := range res.Results {
			if _, dup := seen[att.ID]; dup {
				continue
			}
			seen[att.ID] = struct{}{}
			fresh++
			scanned++
			if scanned%attachmentProgressEvery == 0 {
				slog.Info("attachment scan progress", "scanned", scanned, "emitted", emitted, "skipped", skipped)
			}
			if s.tooOldAttachment(att) {
				if ordered && att.Version.When.Before(s.since.Add(-scanGrace)) {
					goto done
				}
				skipped++
				continue
			}
			ok, err := s.ingestAttachment(ctx, emit, att)
			switch {
			case err != nil:
				return err
			case ok:
				emitted++
			default:
				skipped++
			}
		}
		next = res.Links.Next
		if len(res.Results) == 0 || next == "" || fresh == 0 {
			break
		}
	}
done:
	slog.Info("attachment scan complete", "scanned", scanned, "emitted", emitted, "skipped", skipped)
	return nil
}

// tooOldAttachment mirrors tooOld for the attachment's own version
// timestamp; attachments change independently of their parent page.
func (s *Source) tooOldAttachment(att AttachmentFull) bool {
	return !s.since.IsZero() && !att.Version.When.IsZero() && !att.Version.When.After(s.since)
}

// ingestAttachment downloads, converts, and emits one attachment. Returns
// ok=false for documented skips (wrong type, too large, no text layer,
// corrupt file) after logging the reason. A hard error aborts the run.
func (s *Source) ingestAttachment(ctx context.Context, emit func(onyx.Document) error, att AttachmentFull) (bool, error) {
	attrs := []any{"attachment_id", att.ID, "title", att.Title, "page", att.Container.Title}
	if !s.attachments.Types[extract.ExtensionOf(att.Title)] {
		return false, nil
	}
	data, err := s.client.DownloadAttachment(ctx, att.Links.Download, s.attachments.MaxBytes)
	if err != nil {
		var tooLarge *AttachmentTooLargeError
		if errors.As(err, &tooLarge) {
			slog.Warn("attachment skipped", append(attrs, "reason", "too large")...)
			return false, nil
		}
		slog.Warn("attachment download failed; skipping", append(attrs, "error", err)...)
		return false, nil
	}
	text, err := extract.Convert(att.Title, data)
	if err != nil {
		if errors.Is(err, extract.ErrSkip) {
			slog.Debug("attachment skipped", append(attrs, "reason", err.Error())...)
			return false, nil
		}
		slog.Warn("attachment conversion failed; skipping", append(attrs, "error", err)...)
		return false, nil
	}
	text = extract.Truncate(text, s.attachments.MaxChars)
	if err := emit(MapAttachment(att, text)); err != nil {
		return false, fmt.Errorf("ingest attachment %s (%q): %w", att.ID, att.Title, err)
	}
	return true, nil
}

// attachmentCQL rewrites the page CQL for attachments. Returns "" when the
// scope contains no type=page discriminator: in that case emitting
// attachments would need its own dedicated scope, which the operator can
// set via CONFLUENCE_ATTACHMENT_CQL (not yet implemented — P1 starts with
// the 99% case).
func attachmentCQL(cql string) string {
	return typePageRE.ReplaceAllString(cql, "type=attachment")
}

// MapAttachment converts an attachment plus its extracted text into an
// Onyx ingestion document, linked to its parent page via metadata so
// retrieval can surface "the design doc attached to page X".
func MapAttachment(att AttachmentFull, text string) onyx.Document {
	var updated *time.Time
	if !att.Version.When.IsZero() {
		when := att.Version.When
		updated = &when
	}
	link := ""
	if att.Links.Base != "" && att.Links.WebUI != "" {
		link = att.Links.Base + att.Links.WebUI
	}
	title := att.Title
	if att.Container.Title != "" {
		title = att.Container.Title + " — " + att.Title
	}
	metadata := map[string]string{
		"confluence_id": att.ID,
		"extension":     extract.ExtensionOf(att.Title),
	}
	if att.Container.ID != "" {
		metadata["parent_page_id"] = att.Container.ID
	}
	if att.Container.Title != "" {
		metadata["parent_page_title"] = att.Container.Title
	}
	return onyx.Document{
		ID:                 "confluence-" + att.ID,
		Sections:           []onyx.Section{{Text: text, Link: link}},
		Source:             "confluence",
		SemanticIdentifier: title,
		Metadata:           metadata,
		DocUpdatedAt:       updated,
		Title:              title,
	}
}
