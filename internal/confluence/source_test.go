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

// cursorTransport serves batch one at start=0 (with a next link), and
// batch two either via the next link or a direct cursor request.
func cursorTransport() http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Query().Get("cursor") == "resume-me" || r.URL.Query().Get("start") == "2":
			return cannedResponse(http.StatusOK, `{"results": [`+
				pageJSON("3", "Third", "<p>Three</p>", "https://ex.atlassian.net/wiki", "/x/3", "2024-07-01T10:00:00.000Z")+
				`], "size": 1, "_links": {}}`), nil
		default:
			return cannedResponse(http.StatusOK, `{"results": [`+
				pageJSON("1", "First", "<p>One</p>", "https://ex.atlassian.net/wiki", "/x/1", "2024-05-01T10:00:00.000Z")+
				`], "size": 1, "_links": {"next": "/rest/api/content/search?cursor=resume-me&start=2"}}`), nil
		}
	})
}

func TestResumeFromSkipsToSavedCursor(t *testing.T) {
	src := newTestSource(t, pagedTransport(), 2, 0)
	var cursors []string
	src.SetCursorHook(func(next string) { cursors = append(cursors, next) })

	docs := collectDocs(t, src)

	require.Len(t, docs, 3)
	// The hook fires after each batch: batch 1 reports its next link,
	// batch 2 (the last) reports "".
	assert.Equal(t, []string{
		"/rest/api/content/search?start=2",
		"",
	}, cursors)

	resumed := newTestSource(t, cursorTransport(), 2, 0)
	resumed.ResumeFrom("/rest/api/content/search?cursor=resume-me")
	resumedDocs := collectDocs(t, resumed)
	require.Len(t, resumedDocs, 1)
	assert.Equal(t, "confluence-3", resumedDocs[0].ID)
}

func TestResumeFromStaleCursorRestartsFromBeginning(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("cursor") == "expired" {
			return cannedResponse(http.StatusBadRequest, "invalid cursor"), nil
		}
		return cannedResponse(http.StatusOK, `{"results": [], "size": 0, "_links": {}}`), nil
	})
	src := newTestSource(t, transport, 2, 0)
	src.ResumeFrom("/rest/api/content/search?cursor=expired")

	docs := collectDocs(t, src)

	assert.Empty(t, docs)
	assert.Equal(t, 2, calls, "stale resume must fall back to a fresh scan")
}

// Reproduces the unstable live index: every request returns the same
// already-seen pages but keeps serving a next link, so without dedup the
// stream would loop forever re-ingesting them.
func TestStreamStopsWhenServerRepeatsResults(t *testing.T) {
	body := `{"results": [` +
		pageJSON("1", "First", "<p>One</p>", "https://ex.atlassian.net/wiki", "/x/1", "2024-05-01T10:00:00.000Z") + `,` +
		pageJSON("2", "Second", "<p>Two</p>", "https://ex.atlassian.net/wiki", "/x/2", "2024-06-01T10:00:00.000Z") +
		`], "start": 0, "limit": 2, "size": 2, "_links": {"next": "/rest/api/content/search?start=2"}}`
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusOK, body), nil
	})
	docs := collectDocs(t, newTestSource(t, transport, 2, 0))
	require.Len(t, docs, 2)
	assert.Equal(t, "confluence-1", docs[0].ID)
	assert.Equal(t, "confluence-2", docs[1].ID)
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
	noSleep(t)
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
	assert.Equal(t, "https://ex.atlassian.net/wiki/x/9", doc.Metadata["confluence_url"])
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
	_, hasURL := doc.Metadata["confluence_url"]
	assert.False(t, hasURL, "no URL metadata when the page has no links")
}

func TestIncrementalCQL(t *testing.T) {
	q, ordered := incrementalCQL("type=page AND space=AIC")
	assert.True(t, ordered)
	assert.Equal(t, "(type=page AND space=AIC) order by lastmodified desc", q)

	q, ordered = incrementalCQL("type=page ORDER BY title")
	assert.False(t, ordered)
	assert.Equal(t, "type=page ORDER BY title", q)
}

// incTransport serves pages newest-first (2024-05-10): 10:05 and 10:03 in
// batch 0, then 09:58 in batch 1, which advertises a next link to batch 2
// containing 09:50. requests records every "start" and "cql" seen.
func incTransport(requests *[]string, cqlSeen *string) http.RoundTripper {
	page := func(id, when string) string {
		return pageJSON(id, "P"+id, "<p>body</p>", "https://ex.atlassian.net/wiki", "/x/"+id, when)
	}
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if cql := r.URL.Query().Get("cql"); cql != "" {
			*cqlSeen = cql
		}
		start := r.URL.Query().Get("start")
		*requests = append(*requests, start)
		switch start {
		case "0":
			return cannedResponse(http.StatusOK, `{"results": [`+
				page("a", "2024-05-10T10:05:00.000Z")+`,`+
				page("b", "2024-05-10T10:03:00.000Z")+
				`], "size": 2, "_links": {"next": "/rest/api/content/search?start=2"}}`), nil
		case "2":
			return cannedResponse(http.StatusOK, `{"results": [`+
				page("c", "2024-05-10T09:58:00.000Z")+`,`+
				page("e", "2024-05-10T09:57:00.000Z")+
				`], "size": 2, "_links": {"next": "/rest/api/content/search?start=4"}}`), nil
		default:
			return cannedResponse(http.StatusOK, `{"results": [`+
				page("d", "2024-05-10T09:50:00.000Z")+
				`], "size": 1, "_links": {}}`), nil
		}
	})
}

func newIncTestSource(t *testing.T, transport http.RoundTripper, cql string, since time.Time) *Source {
	t.Helper()
	client, err := NewClient(Options{
		BaseURL: "https://ex.atlassian.net/wiki",
		Auth:    Auth{Mode: ModeBasic, Username: "u@x.com", Token: "t"},
	}, &http.Client{Transport: transport})
	require.NoError(t, err)
	return NewIncrementalSource(client, cql, since, 2, 0)
}

func TestStreamIncrementalFiltersAndStopsEarly(t *testing.T) {
	var requests []string
	var cqlSeen string
	since := time.Date(2024, 5, 10, 10, 2, 0, 0, time.UTC)
	src := newIncTestSource(t, incTransport(&requests, &cqlSeen), "type=page", since)

	docs := collectDocs(t, src)

	require.Len(t, docs, 2)
	assert.Equal(t, "confluence-a", docs[0].ID)
	assert.Equal(t, "confluence-b", docs[1].ID)
	// Page c at 09:58 is beyond the 1m scan grace past the cutoff, so
	// pagination stopped without requesting batch 2.
	assert.Equal(t, []string{"0", "2"}, requests)
	assert.Equal(t, "(type=page) order by lastmodified desc", cqlSeen)
}

func TestStreamIncrementalWithUserOrderByScansAll(t *testing.T) {
	var requests []string
	var cqlSeen string
	since := time.Date(2024, 5, 10, 10, 2, 0, 0, time.UTC)
	src := newIncTestSource(t, incTransport(&requests, &cqlSeen), "type=page order by title", since)

	docs := collectDocs(t, src)

	// Still filtered client-side, but no early stop is possible when the
	// user controls ordering, so all batches are scanned.
	require.Len(t, docs, 2)
	assert.Equal(t, []string{"0", "2", "4"}, requests)
	assert.Equal(t, "type=page order by title", cqlSeen)
}

func TestStreamKeepsPagesWithoutVersionTimestamp(t *testing.T) {
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusOK, `{"results": [{
		  "id": "x", "title": "NoWhen",
		  "body": {"storage": {"value": "<p>b</p>"}}
		}], "size": 1, "_links": {}}`), nil
	})
	since := time.Date(2024, 5, 10, 10, 2, 0, 0, time.UTC)
	src := newIncTestSource(t, transport, "type=page", since)

	docs := collectDocs(t, src)
	require.Len(t, docs, 1)
	assert.Nil(t, docs[0].DocUpdatedAt)
}
