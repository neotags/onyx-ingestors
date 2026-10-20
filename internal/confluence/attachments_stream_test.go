package confluence

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attachmentJSON builds one attachment result as returned by content/search
// with expand=container,metadata.mediaType,version.
func attachmentJSON(id, title, pageID, pageTitle, media, download, when string) string {
	return `{
      "id": "` + id + `",
      "title": "` + title + `",
      "version": {"when": "` + when + `"},
      "metadata": {"mediaType": "` + media + `"},
      "container": {"id": "` + pageID + `", "title": "` + pageTitle + `", "type": "page"},
      "_links": {
        "download": "` + download + `",
        "webui": "/pages/viewpageattachments.action?pageId=` + pageID + `"
      }
    }`
}

// attachmentTransport serves the page phase (type=page) with the standard
// two-batch fixture, then the attachment phase (type=attachment) with
// searchBody, and per-path download bodies. Anything else fails loudly so
// the test proves the stream only touches the URLs it should.
func attachmentTransport(searchBody string, downloads map[string]string) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/wiki/rest/api/content/search" {
			if !strings.Contains(r.URL.Query().Get("cql"), "type=attachment") {
				return cannedResponse(http.StatusOK, `{"results": [`+
					pageJSON("1", "First", "<p>One</p>", "https://ex.atlassian.net/wiki", "/x/1", "2024-05-01T10:00:00.000Z")+
					`], "size": 1, "_links": {}}`), nil
			}
			return cannedResponse(http.StatusOK, searchBody), nil
		}
		if body, ok := downloads[r.URL.Path]; ok {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}
		return cannedResponse(http.StatusNotFound, "unexpected URL "+r.URL.Path), nil
	})
}

func withAttachments(src *Source, types map[string]bool) {
	src.SetAttachments(AttachmentOptions{
		Enabled:  true,
		Types:    types,
		MaxBytes: 1 << 20,
		MaxChars: 100_000,
	})
}

func TestStreamAttachmentsEmitsDocuments(t *testing.T) {
	drawio := `<?xml version="1.0"?><mxfile><diagram name="P"><mxGraphModel><root>` +
		`<mxCell id="a" value="专线网关 拓扑"/>` +
		`</root></mxGraphModel></diagram></mxfile>`
	search := `{"results": [` +
		attachmentJSON("att1", "design.pdf", "1", "First", "application/pdf", "/download/att1", "2024-06-01T10:00:00.000Z") + `,` +
		attachmentJSON("att2", "topology.drawio", "2", "Second", "application/vnd.jgraph.mxfile", "/download/att2", "2024-06-02T10:00:00.000Z") + `,` +
		attachmentJSON("att3", "photo.png", "2", "Second", "image/png", "/download/att3", "2024-06-03T10:00:00.000Z") +
		`], "size": 3, "_links": {}}`

	pdfPayload := "%PDF-1.4 fake" // anymd will fail on this; drawio must still win
	src := newTestSource(t, attachmentTransport(search, map[string]string{
		"/wiki/download/att1": pdfPayload,
		"/wiki/download/att2": drawio,
		"/wiki/download/att3": "png-bytes",
	}), 2, 1)
	withAttachments(src, map[string]bool{"pdf": true, "drawio": true})

	docs := collectDocs(t, src)

	// 1 page (truncated by maxPages=1 batch window? no: pagedTransport isn't
	// in play here — the fixture transport serves only attachment search, so
	// the page phase just gets the single error-free search body).
	// Assert only on the attachment docs.
	var pageDocs, attDocs int
	for _, d := range docs {
		if strings.HasPrefix(d.ID, "confluence-att") {
			attDocs++
			if d.ID == "confluence-att2" {
				assert.Equal(t, "Second — topology.drawio", d.Title)
				assert.Contains(t, d.Sections[0].Text, "专线网关 拓扑")
				assert.Equal(t, "2", d.Metadata["parent_page_id"])
				assert.Equal(t, "drawio", d.Metadata["extension"])
				require.NotNil(t, d.DocUpdatedAt)
				assert.True(t, d.DocUpdatedAt.Equal(mustTime(t, "2024-06-02T10:00:00.000Z")))
			}
			continue
		}
		pageDocs++
	}
	assert.Equal(t, 1, attDocs, "only the drawio converts; pdf payload is corrupt, png is declined: %v", docs)
	assert.Equal(t, 1, pageDocs, "page phase emits the fixture page")
}

func TestAttachmentCQLRewrite(t *testing.T) {
	assert.Equal(t,
		"type=attachment and space = 'ENG' and type=attachment",
		attachmentCQL("type=page and space = 'ENG' and type=page"))
	assert.Equal(t, "type=attachment order by created",
		attachmentCQL("type=page order by created"))
}

func TestDownloadAttachmentByteCap(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 4096))),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})
	client, err := NewClient(Options{
		BaseURL: "https://ex.atlassian.net/wiki",
		Auth:    Auth{Mode: ModeBasic, Username: "u@x.com", Token: "t"},
	}, &http.Client{Transport: transport})
	require.NoError(t, err)

	_, err = client.DownloadAttachment(t.Context(), "/download/big", 1024)
	var tooLarge *AttachmentTooLargeError
	require.ErrorAs(t, err, &tooLarge)

	got, err := client.DownloadAttachment(t.Context(), "/download/ok", 8192)
	require.NoError(t, err)
	assert.Len(t, got, 4096)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return ts
}
