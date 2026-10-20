package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AttachmentFull is one attachment result from a content/search CQL query
// (type=attachment), carrying everything the attachment stream needs: the
// download link, the media type, the parent container, and the version
// timestamp that drives incremental sync.
type AttachmentFull struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version struct {
		When time.Time `json:"when"`
	} `json:"version"`
	Metadata struct {
		MediaType string `json:"mediaType"`
	} `json:"metadata"`
	Container struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Type  string `json:"type"`
	} `json:"container"`
	Links struct {
		Base     string `json:"base"`
		Download string `json:"download"`
		WebUI    string `json:"webui"`
	} `json:"_links"`
}

// AttachmentSearchResult mirrors the shared pagination envelope.
type AttachmentSearchResult struct {
	Results []AttachmentFull `json:"results"`
	Size    int              `json:"size"`
	Links   struct {
		Base string `json:"base"`
		Next string `json:"next"`
	} `json:"_links"`
}

// SearchAttachments runs one attachment-CQL search request. Pagination and
// expand parameters live in cql's URL construction; like SearchPages it must
// only be called for the first batch (continue with SearchAttachmentPageURL).
func (c *Client) SearchAttachments(ctx context.Context, cql string, limit int) (AttachmentSearchResult, error) {
	if limit <= 0 {
		limit = 100
	} else if limit > 100 {
		limit = 100
	}
	u, err := url.Parse(c.apiBase() + "/rest/api/content/search")
	if err != nil {
		return AttachmentSearchResult{}, fmt.Errorf("confluence API URL: %w", err)
	}
	q := u.Query()
	q.Set("cql", cql)
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("expand", "container,metadata.mediaType,version")
	u.RawQuery = q.Encode()
	return c.doAttachmentSearch(ctx, u.String())
}

// SearchAttachmentPageURL follows a relative _links.next cursor verbatim —
// same contract as SearchPageURL, returned here so the attachment stream
// shares the pagination discipline.
func (c *Client) SearchAttachmentPageURL(ctx context.Context, next string) (AttachmentSearchResult, error) {
	if !strings.HasPrefix(next, "/rest/") {
		return AttachmentSearchResult{}, fmt.Errorf("refusing non-API next link %q", next)
	}
	return c.doAttachmentSearch(ctx, c.apiBase()+next)
}

func (c *Client) doAttachmentSearch(ctx context.Context, reqURL string) (AttachmentSearchResult, error) {
	var out AttachmentSearchResult
	body, err := c.get(ctx, reqURL, true)
	if err != nil {
		return out, fmt.Errorf("confluence attachment search: %w", err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("confluence attachment search: decode response: %w", err)
	}
	for i := range out.Results {
		if out.Results[i].Links.Base == "" {
			out.Results[i].Links.Base = out.Links.Base
		}
	}
	return out, nil
}

// DownloadAttachment fetches the bytes of an attachment, honoring maxBytes
// (0 means unbounded). The response body is capped before any conversion
// happens so a 4 GB video can't OOM the process before the type gate runs.
// Relative download links are resolved against the API base (scoped gateway
// or site, depending on the auth mode).
func (c *Client) DownloadAttachment(ctx context.Context, downloadLink string, maxBytes int64) ([]byte, error) {
	var reqURL string
	switch {
	case strings.HasPrefix(downloadLink, "http://") || strings.HasPrefix(downloadLink, "https://"):
		reqURL = downloadLink
	default:
		reqURL = c.apiBase() + downloadLink
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("confluence download request: %w", err)
	}
	c.authorize(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("confluence download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("confluence download: unexpected status %s: %s",
			resp.Status, strings.TrimSpace(string(snippet)))
	}
	reader := io.Reader(resp.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(resp.Body, maxBytes+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("confluence download: read body: %w", err)
	}
	if maxBytes > 0 && int64(len(body)) > maxBytes {
		return nil, &AttachmentTooLargeError{Limit: maxBytes}
	}
	return body, nil
}

// AttachmentTooLargeError reports an attachment skipped for exceeding the
// configured byte cap. Treated as a skip (scan continues), never run-fatal.
type AttachmentTooLargeError struct {
	Limit int64
}

func (e *AttachmentTooLargeError) Error() string {
	return fmt.Sprintf("attachment exceeds byte cap %d", e.Limit)
}
