package confluence

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pageWithAttachments(base string, atts ...Attachment) Page {
	var p Page
	p.ID = "9"
	p.Title = "Doc"
	p.Links.Base = base
	p.Links.WebUI = "/x/9"
	p.Body.Storage.Value = `<p>body</p>`
	p.Children.Attachment.Results = atts
	return p
}

func TestAttachmentsTextEmpty(t *testing.T) {
	assert.Equal(t, "", attachmentsText(pageWithAttachments("https://ex.atlassian.net/wiki")))
}

func TestAttachmentsTextRendersLinks(t *testing.T) {
	att := Attachment{ID: "att1", Title: "report.pdf"}
	att.Links.Download = "/rest/api/content/9/child/attachment/att1/download"
	txt := attachmentsText(pageWithAttachments("https://ex.atlassian.net/wiki", att))
	want := "Attachments:\n• report.pdf — https://ex.atlassian.net/wiki/rest/api/content/9/child/attachment/att1/download"
	assert.Equal(t, want, txt)
}

func TestAttachmentsTextAbsolutePassthrough(t *testing.T) {
	att := Attachment{ID: "att1", Title: "shot.png"}
	att.Links.Download = "https://cdn.example.com/file.png"
	txt := attachmentsText(pageWithAttachments("https://ex/wiki", att))
	assert.Contains(t, txt, "https://cdn.example.com/file.png")
}

func TestAttachmentsTextNoDownload(t *testing.T) {
	att := Attachment{ID: "att1", Title: "name only.txt"}
	txt := attachmentsText(pageWithAttachments("", att))
	assert.Equal(t, "Attachments:\n• name only.txt", txt)
}

func TestMapPageAppendsAttachmentsSection(t *testing.T) {
	att := Attachment{ID: "att1", Title: "diagram.png"}
	att.Links.Download = "/rest/api/content/9/child/attachment/att1/download"
	p := pageWithAttachments("https://ex.atlassian.net/wiki", att)

	doc := MapPage(p)
	require.Len(t, doc.Sections, 2)
	assert.Equal(t, "body", doc.Sections[0].Text)
	assert.Contains(t, doc.Sections[1].Text, "diagram.png")
	assert.Contains(t, doc.Sections[1].Text, "https://ex.atlassian.net/wiki/rest/api/content/9/")
	assert.Equal(t, doc.Sections[0].Link, doc.Sections[1].Link)
}

func TestMapPageAttachmentsMergedAtCap(t *testing.T) {
	var b []string
	for range maxSections {
		b = append(b, "x")
	}
	_ = b // construct via splitSections instead
	att := Attachment{ID: "att1", Title: "f.pdf"}
	att.Links.Download = "/d"
	var p Page
	p.ID = "9"
	p.Title = "Doc"
	p.Links.Base = "https://ex/wiki"
	p.Links.WebUI = "/x/9"
	body := "<p>intro</p>"
	for i := 0; i < maxSections+5; i++ {
		body += "<h1>H</h1><p>y</p>"
	}
	p.Body.Storage.Value = body
	p.Children.Attachment.Results = []Attachment{att}

	doc := MapPage(p)
	require.Len(t, doc.Sections, maxSections)
	assert.Contains(t, doc.Sections[len(doc.Sections)-1].Text, "f.pdf")
}
