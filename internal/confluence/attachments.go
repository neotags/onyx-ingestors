package confluence

import (
	"fmt"
	"strings"
)

// attachmentsText renders a page's attachment list as plain text: one line
// per attachment, with its absolute download URL when available. Returns ""
// when the page has no attachments. The ingestion API cannot carry binary
// content, so this reference keeps files discoverable and deep-linkable.
//
// Note: the children.attachment expansion returns only the first page of
// attachments (25); pages with more are truncated.
// TODO(onyx-ingestor): paginate attachment fetching if that ever matters.
func attachmentsText(page Page) string {
	results := page.Children.Attachment.Results
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Attachments:")
	for _, att := range results {
		fmt.Fprintf(&b, "\n• %s", att.Title)
		if url := absDownloadURL(page.Links.Base, att.Links.Download); url != "" {
			fmt.Fprintf(&b, " — %s", url)
		}
	}
	return b.String()
}

// absDownloadURL resolves a possibly-relative Confluence download path
// against the response's base URL.
func absDownloadURL(base, download string) string {
	switch {
	case download == "":
		return ""
	case strings.HasPrefix(download, "http://"), strings.HasPrefix(download, "https://"):
		return download
	case base == "":
		return ""
	default:
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(download, "/")
	}
}
