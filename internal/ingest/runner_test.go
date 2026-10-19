package ingest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"onyx-ingestor/internal/onyx"
)

type fakeSource struct {
	docs []onyx.Document
	err  error
}

func (f *fakeSource) Stream(_ context.Context, emit func(onyx.Document) error) error {
	for _, doc := range f.docs {
		if err := emit(doc); err != nil {
			return err
		}
	}
	return f.err
}

type fakeSink struct {
	docs    []onyx.Document
	failAt  int
	failErr error
}

func (f *fakeSink) Ingest(_ context.Context, doc onyx.Document) error {
	f.docs = append(f.docs, doc)
	if f.failErr != nil && len(f.docs) == f.failAt {
		return f.failErr
	}
	return nil
}

func docs(ids ...string) []onyx.Document {
	out := make([]onyx.Document, len(ids))
	for i, id := range ids {
		out[i] = onyx.Document{ID: id, Title: "Title " + id, Source: "fake"}
	}
	return out
}

func TestRunSuccess(t *testing.T) {
	source := &fakeSource{docs: docs("a", "b", "c")}
	sink := &fakeSink{}

	stats, err := Run(context.Background(), source, sink, Options{})
	require.NoError(t, err)
	assert.Equal(t, Stats{Seen: 3, Ingested: 3}, stats)
	assert.Len(t, sink.docs, 3)
}

func TestRunDryRun(t *testing.T) {
	source := &fakeSource{docs: docs("a", "b")}
	sink := &fakeSink{}

	stats, err := Run(context.Background(), source, sink, Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, Stats{Seen: 2, Skipped: 2}, stats)
	assert.Empty(t, sink.docs)
}

func TestRunSinkErrorAborts(t *testing.T) {
	source := &fakeSource{docs: docs("a", "b", "c")}
	sink := &fakeSink{failAt: 2, failErr: errors.New("onyx down")}

	stats, err := Run(context.Background(), source, sink, Options{})
	require.Error(t, err)
	assert.ErrorContains(t, err, `"b"`)
	assert.Equal(t, Stats{Seen: 2, Ingested: 1, Failed: 1}, stats)
}

func TestRunSourceError(t *testing.T) {
	source := &fakeSource{docs: docs("a"), err: errors.New("fetch failed")}
	sink := &fakeSink{}

	stats, err := Run(context.Background(), source, sink, Options{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "fetch failed")
	assert.Equal(t, Stats{Seen: 1, Ingested: 1}, stats)
}

func TestRunProgressLogIncludesTitleAndLink(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	all := docs("p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10",
		"p11", "p12", "p13", "p14", "p15", "p16", "p17", "p18", "p19", "p20",
		"p21", "p22", "p23", "p24", "p25")
	all[24].Sections = []onyx.Section{{Text: "x", Link: "https://ex.atlassian.net/wiki/x/25"}}
	source := &fakeSource{docs: all}
	sink := &fakeSink{}

	_, err := Run(context.Background(), source, sink, Options{})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "seen=25")
	assert.Contains(t, out, `title="Title p25"`)
	assert.Contains(t, out, "link=https://ex.atlassian.net/wiki/x/25")
}
