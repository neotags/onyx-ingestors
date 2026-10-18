package confluence

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"onyx-ingestor/internal/onyx"
)

func pageJSON(id, title, body, base, webui, when string) string {
	return `{
      "id": "` + id + `",
      "title": "` + title + `",
      "version": {"when": "` + when + `"},
      "body": {"storage": {"value": "` + body + `"}},
      "_links": {"base": "` + base + `", "webui": "` + webui + `"}
    }`
}

// pagedTransport returns result batches keyed by the "start" query param:
// start=0 -> two pages with a next link; start=2 -> one page, no next link.
func pagedTransport() http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Query().Get("start") {
		case "0":
			return cannedResponse(http.StatusOK, `{"results": [`+
				pageJSON("1", "First", "<p>Hello <b>world</b></p>", "https://ex.atlassian.net/wiki", "/x/1", "2024-05-01T10:00:00.000Z")+`,`+
				pageJSON("2", "Second", "<p>Two</p>", "https://ex.atlassian.net/wiki", "/x/2", "2024-06-01T10:00:00.000Z")+
				`], "start": 0, "limit": 2, "size": 2, "_links": {"next": "/rest/api/content/search?start=2"}}`), nil
		default:
			return cannedResponse(http.StatusOK, `{"results": [`+
				pageJSON("3", "Third", "<p>Three</p>", "https://ex.atlassian.net/wiki", "/x/3", "2024-07-01T10:00:00.000Z")+
				`], "start": 2, "limit": 2, "size": 1, "_links": {}}`), nil
		}
	})
}

func newTestSource(t *testing.T, transport http.RoundTripper, pageSize, maxPages int) *Source {
	t.Helper()
	client, err := NewClient(Options{
		BaseURL: "https://ex.atlassian.net/wiki",
		Auth:    Auth{Mode: ModeBasic, Username: "u@x.com", Token: "t"},
	}, &http.Client{Transport: transport})
	require.NoError(t, err)
	return NewSource(client, "type=page", pageSize, maxPages)
}

func collectDocs(t *testing.T, src *Source) []onyx.Document {
	t.Helper()
	var docs []onyx.Document
	err := src.Stream(context.Background(), func(doc onyx.Document) error {
		docs = append(docs, doc)
		return nil
	})
	require.NoError(t, err)
	return docs
}

func TestStreamPaginatesAndMaps(t *testing.T) {
	docs := collectDocs(t, newTestSource(t, pagedTransport(), 2, 0))
	require.Len(t, docs, 3)

	first := docs[0]
	assert.Equal(t, "confluence-1", first.ID)
	assert.Equal(t, "confluence", first.Source)
	assert.Equal(t, "First", first.SemanticIdentifier)
	assert.True(t, first.FromIngestionAPI)

	require.Len(t, first.Sections, 1)
	assert.Equal(t, "Hello world", first.Sections[0].Text)
	assert.Equal(t, "https://ex.atlassian.net/wiki/x/1", first.Sections[0].Link)

	wantWhen := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NotNil(t, first.DocUpdatedAt)
	assert.True(t, first.DocUpdatedAt.Equal(wantWhen), "doc_updated_at = %v", first.DocUpdatedAt)

	assert.Equal(t, "2", docs[1].Metadata["confluence_id"])
}

func TestStreamStopsAtMaxPages(t *testing.T) {
	docs := collectDocs(t, newTestSource(t, pagedTransport(), 2, 1))
	assert.Len(t, docs, 2)
}

func TestStreamPropagatesEmitError(t *testing.T) {
	src := newTestSource(t, pagedTransport(), 2, 0)
	err := src.Stream(context.Background(), func(_ onyx.Document) error {
		return errors.New("sink down")
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "First")
	assert.ErrorContains(t, err, "sink down")
}

func TestStreamFetchError(t *testing.T) {
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusBadGateway, "bad gateway"), nil
	})
	src := newTestSource(t, transport, 50, 0)
	err := src.Stream(context.Background(), func(_ onyx.Document) error { return nil })
	require.Error(t, err)
	assert.ErrorContains(t, err, "fetch confluence pages")
}

func TestMapPageSplitsByHeadings(t *testing.T) {
	var p Page
	p.ID = "9"
	p.Title = "Doc"
	p.Links.Base = "https://ex.atlassian.net/wiki"
	p.Links.WebUI = "/x/9"
	p.Body.Storage.Value = `<p>Intro</p><h1>Alpha</h1><p>A body</p><h2>Beta</h2><p>B body</p>`

	doc := MapPage(p)
	require.Len(t, doc.Sections, 3)
	for _, s := range doc.Sections {
		assert.Equal(t, "https://ex.atlassian.net/wiki/x/9", s.Link)
	}
	assert.Equal(t, "Intro", doc.Sections[0].Text)
	assert.Equal(t, "Alpha\nA body", doc.Sections[1].Text)
	assert.Equal(t, "Beta\nB body", doc.Sections[2].Text)
}

func TestMapPagePrefersADF(t *testing.T) {
	var p Page
	p.ID = "9"
	p.Title = "Doc"
	p.Links.Base = "https://ex.atlassian.net/wiki"
	p.Links.WebUI = "/x/9"
	p.Body.Storage.Value = `<p>storage body</p>` // would produce different text
	p.Body.AtlasDocFormat.Value = `{"type":"doc","content":[
		{"type":"paragraph","content":[{"type":"text","text":"adf intro"}]},
		{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"ADF H"}]},
		{"type":"paragraph","content":[{"type":"text","text":"adf body"}]}
	]}`

	doc := MapPage(p)
	require.Len(t, doc.Sections, 2)
	assert.Equal(t, "adf intro", doc.Sections[0].Text)
	assert.Equal(t, "ADF H\nadf body", doc.Sections[1].Text)
	for _, s := range doc.Sections {
		assert.Equal(t, "https://ex.atlassian.net/wiki/x/9", s.Link)
	}
}

func TestMapPageFallsBackToStorage(t *testing.T) {
	var p Page
	p.ID = "9"
	p.Title = "Doc"
	p.Body.Storage.Value = `<p>storage <b>body</b></p>`
	// no atlas_doc_format

	doc := MapPage(p)
	require.Len(t, doc.Sections, 1)
	assert.Equal(t, "storage body", doc.Sections[0].Text)
}
