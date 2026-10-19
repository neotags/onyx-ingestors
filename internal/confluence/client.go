// Package confluence provides a Confluence ingestion source: a REST API
// client with CQL pagination plus mapping to Onyx ingestion documents.
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Mode selects how requests to Confluence are authenticated.
type Mode int

const (
	// ModeBasic is HTTP basic auth against the site URL: Confluence Cloud
	// with a classic API token (email + token, no scopes) or Data Center
	// with username + password/PAT.
	ModeBasic Mode = iota
	// ModeBearer sends the token as a bearer token against the site URL:
	// Confluence Data Center Personal Access Token.
	ModeBearer
	// ModeScopedBearer is for Atlassian scoped API tokens. Requests go to
	// the Atlassian API gateway (api.atlassian.com/ex/confluence/{cloudID})
	// with a bearer token instead of the site URL.
	ModeScopedBearer
)

func (m Mode) String() string {
	switch m {
	case ModeBasic:
		return "basic"
	case ModeBearer:
		return "bearer"
	case ModeScopedBearer:
		return "scoped"
	default:
		return "unknown"
	}
}

// ResolveMode picks an auth mode from an explicit type and the available
// credentials. authType must be "", "auto", "basic", "bearer", or "scoped".
// In auto mode a cloud ID selects scoped auth, a username selects basic,
// and token-only selects bearer.
func ResolveMode(authType, username, cloudID string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "", "auto":
		switch {
		case cloudID != "":
			return ModeScopedBearer, nil
		case username != "":
			return ModeBasic, nil
		default:
			return ModeBearer, nil
		}
	case "basic":
		return ModeBasic, nil
	case "bearer":
		return ModeBearer, nil
	case "scoped":
		return ModeScopedBearer, nil
	default:
		return 0, fmt.Errorf("unknown CONFLUENCE_AUTH_TYPE %q: want auto, basic, bearer, or scoped", authType)
	}
}

// scopedGatewayBase is the Atlassian API gateway prefix used with scoped
// API tokens: {gatewayBase}{cloudID}{contextPath}/rest/api/...
const scopedGatewayBase = "https://api.atlassian.com/ex/confluence/"

// Auth holds Confluence credentials for one auth Mode. Token is always
// required; Username only for ModeBasic; CloudID only for ModeScopedBearer.
type Auth struct {
	Mode     Mode
	Username string
	Token    string
	CloudID  string
}

// Page is one content item returned by the Confluence search API.
type Page struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version struct {
		When time.Time `json:"when"`
	} `json:"version"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
		AtlasDocFormat struct {
			Value string `json:"value"`
		} `json:"atlas_doc_format"`
	} `json:"body"`
	Links struct {
		Base  string `json:"base"`
		WebUI string `json:"webui"`
	} `json:"_links"`
	Children struct {
		Attachment struct {
			Results []Attachment `json:"results"`
		} `json:"attachment"`
	} `json:"children"`
}

// Attachment is a file attached to a Confluence page.
type Attachment struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Links struct {
		Download string `json:"download"`
	} `json:"_links"`
}

// WebURL returns an absolute link to the page, or "" when unavailable.
func (p Page) WebURL() string {
	if p.Links.Base != "" && p.Links.WebUI != "" {
		return p.Links.Base + p.Links.WebUI
	}
	return ""
}

// SearchResult is one batch of results from /rest/api/content/search.
type SearchResult struct {
	Results []Page `json:"results"`
	Start   int    `json:"start"`
	Limit   int    `json:"limit"`
	Size    int    `json:"size"`
	Links   struct {
		// Base is the site base URL; Confluence Cloud omits per-page
		// `_links.base` in search results, so pages inherit this one.
		Base string `json:"base"`
		Next string `json:"next"`
	} `json:"_links"`
}

// Options configures a Client. BaseURL must be the site URL including any
// context path, e.g. "https://example.atlassian.net/wiki".
type Options struct {
	BaseURL string
	Auth    Auth
}

// Retry policy for Confluence calls, modeled on the Onyx connector's
// _handle_http_error: bounded attempts, Retry-After honored, exponential
// backoff otherwise, and progressive page-size reduction on 5xx.
const (
	// maxRequestAttempts bounds retries per request (initial + retries).
	maxRequestAttempts = 5
	// backoffBase/backoffMax define exponential delay between attempts.
	backoffBase = 5 * time.Second
	backoffMax  = 60 * time.Second
	// retryAfterBounds clamp proven Retry-After values: Atlassian can send
	// pathologically large values, and sub-second pauses don't help.
	retryAfterMin = 2
	retryAfterMax = 60
	// minSearchLimit prevents the progressive reduction on 5xx from
	// degenerating into single-item fetches.
	minSearchLimit = 5
)

// sleepCtx sleeps for d or until ctx is done. A package variable (not a
// Client field) so tests can replace it globally.
var sleepCtx = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Client calls the Confluence REST API.
type Client struct {
	siteBase    string // e.g. https://example.atlassian.net/wiki
	contextPath string // e.g. /wiki ("" for root installs)
	auth        Auth
	hc          *http.Client
}

// NewClient creates and validates a Client.
func NewClient(opts Options, hc *http.Client) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(opts.BaseURL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("confluence base URL %q is not an absolute http(s) URL", opts.BaseURL)
	}
	if opts.Auth.Token == "" {
		return nil, errors.New("confluence token is required")
	}
	switch opts.Auth.Mode {
	case ModeBasic:
		if opts.Auth.Username == "" {
			return nil, errors.New("basic auth requires CONFLUENCE_USERNAME")
		}
	case ModeScopedBearer:
		if opts.Auth.CloudID == "" {
			return nil, errors.New("scoped auth requires CONFLUENCE_CLOUD_ID")
		}
	case ModeBearer:
	default:
		return nil, fmt.Errorf("unsupported auth mode %d", opts.Auth.Mode)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{
		siteBase:    strings.TrimRight(opts.BaseURL, "/"),
		contextPath: strings.TrimRight(base.Path, "/"),
		auth:        opts.Auth,
		hc:          hc,
	}, nil
}

// apiBase returns the URL prefix that API paths are appended to. Scoped
// tokens must go through the Atlassian API gateway rather than the site.
func (c *Client) apiBase() string {
	if c.auth.Mode == ModeScopedBearer {
		return scopedGatewayBase + c.auth.CloudID + c.contextPath
	}
	return c.siteBase
}

func (c *Client) authorize(req *http.Request) {
	if c.auth.Mode == ModeBasic {
		req.SetBasicAuth(c.auth.Username, c.auth.Token)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.auth.Token)
}

// SearchPages runs a CQL query and returns the first batch of results with
// page storage bodies and version timestamps (limit is capped at 100 by
// Confluence). Continue pagination with SearchPageURL: Cloud's scoped-token
// gateway ignores the start offset entirely and paginates via the opaque
// cursor embedded in _links.next, so offsets must never be recomputed
// client-side.
func (c *Client) SearchPages(ctx context.Context, cql string, start, limit int) (SearchResult, error) {
	if limit <= 0 {
		limit = 100
	} else if limit > 100 {
		limit = 100
	}

	u, err := url.Parse(c.apiBase() + "/rest/api/content/search")
	if err != nil {
		return SearchResult{}, fmt.Errorf("confluence API URL: %w", err)
	}
	q := u.Query()
	q.Set("cql", cql)
	q.Set("start", strconv.Itoa(start))
	q.Set("limit", strconv.Itoa(limit))
	// atlas_doc_format gives a structured JSON document on Cloud; storage is
	// kept as a fallback for legacy pages and Data Center.
	q.Set("expand", "body.atlas_doc_format,body.storage,children.attachment,version")
	u.RawQuery = q.Encode()
	return c.doSearch(ctx, u.String())
}

// SearchPageURL fetches the next batch of a CQL search by following the
// relative _links.next URL verbatim. The cursor it carries is the only
// pagination state Cloud honors on the scoped gateway. Anything that is
// not a site-relative /rest/ path is refused defensively.
func (c *Client) SearchPageURL(ctx context.Context, next string) (SearchResult, error) {
	var out SearchResult
	if !strings.HasPrefix(next, "/rest/") {
		return out, fmt.Errorf("refusing non-API next link %q", next)
	}
	return c.doSearch(ctx, c.apiBase()+next)
}

// CountPages returns how many results a CQL query matches, via the legacy
// /rest/api/search endpoint — the only CQL search API that reports
// totalSize. It exists purely for pre-run reporting ("N pages to scan")
// and performs a minimal request (limit=1), so callers should treat an
// error as non-fatal.
func (c *Client) CountPages(ctx context.Context, cql string) (int, error) {
	u, err := url.Parse(c.apiBase() + "/rest/api/search")
	if err != nil {
		return 0, fmt.Errorf("confluence API URL: %w", err)
	}
	q := u.Query()
	q.Set("cql", cql)
	q.Set("limit", "1")
	u.RawQuery = q.Encode()

	body, err := c.get(ctx, u.String(), false)
	if err != nil {
		return 0, fmt.Errorf("confluence count: %w", err)
	}
	var out struct {
		TotalSize int `json:"totalSize"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("confluence count: decode response: %w", err)
	}
	return out.TotalSize, nil
}

// doSearch executes one content-search request and normalizes the response:
// Cloud puts the base URL on the result envelope, not on each page.
func (c *Client) doSearch(ctx context.Context, reqURL string) (SearchResult, error) {
	var out SearchResult
	body, err := c.get(ctx, reqURL, true)
	if err != nil {
		return out, fmt.Errorf("confluence search: %w", err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("confluence search: decode response: %w", err)
	}
	// Cloud search responses put the base URL on the result envelope, not
	// on each page; without it section links and attachment URLs would
	// silently come out empty.
	for i := range out.Results {
		if out.Results[i].Links.Base == "" {
			out.Results[i].Links.Base = out.Links.Base
		}
	}
	return out, nil
}

// get fetches reqURL with bounded retries. Transport errors, 429, and 5xx
// are retryable; Retry-After is honored and otherwise delay grows
// exponentially. When halveLimit is true, a 5xx also halves the limit
// query parameter (floor minSearchLimit) and retries the rewritten URL —
// Cloud frequently 504s on heavyweight result windows, and lighter
// windows get through.
func (c *Client) get(ctx context.Context, reqURL string, halveLimit bool) ([]byte, error) {
	attemptURL := reqURL
	var lastErr error
	for attempt := 1; attempt <= maxRequestAttempts; attempt++ {
		body, status, retryAfter, err := c.doOnce(ctx, attemptURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		// status 0 = transport failure; HTTP failures are retryable per
		// Onyx's model: 429 (rate limit) and 5xx (server/transient).
		retryable := status == 0 || status == http.StatusTooManyRequests || (status >= 500 && status < 600)
		if attempt == maxRequestAttempts || !retryable {
			break
		}
		if halveLimit && status >= 500 && status < 600 {
			if next, ok := halveLimitParam(attemptURL); ok {
				slog.Warn("confluence request failed; retrying with a smaller limit",
					"status", status, "attempt", attempt, "limit", limitParamOf(next))
				attemptURL = next
			}
		}
		delay := retryDelay(retryAfter, attempt)
		slog.Warn("confluence request failed; retrying",
			"attempt", attempt, "of", maxRequestAttempts, "delay", delay.String(), "error", err)
		if serr := sleepCtx(ctx, delay); serr != nil {
			return nil, serr
		}
	}
	return nil, fmt.Errorf("failed after %d attempts: %w", maxRequestAttempts, lastErr)
}

// doOnce performs a single GET and returns the body (always fully read and
// closed), the status code, the Retry-After header value, and any error.
// err is non-nil for transport failures and non-200 statuses.
func (c *Client) doOnce(ctx context.Context, reqURL string) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("request construction: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	c.authorize(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, resp.StatusCode, resp.Header.Get("Retry-After"),
			fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, "", fmt.Errorf("read response: %w", err)
	}
	return body, resp.StatusCode, "", nil
}

// retryDelay computes the wait before the next attempt: Retry-After when
// parseable (clamped), otherwise exponential backoff backoffBase*2^(n-1)
// capped at backoffMax. n is 1-based (the attempt that just failed).
func retryDelay(retryAfter string, n int) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && strings.TrimSpace(retryAfter) != "" {
		if secs < retryAfterMin {
			secs = retryAfterMin
		}
		if secs > retryAfterMax {
			secs = retryAfterMax
		}
		return time.Duration(secs) * time.Second
	}
	delay := backoffBase << (n - 1)
	if delay > backoffMax {
		delay = backoffMax
	}
	return delay
}

// halveLimitParam rewrites URL with limit halved (floor minSearchLimit).
// ok is false when the URL has no integer limit or is already at the floor.
func halveLimitParam(rawurl string) (string, bool) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return rawurl, false
	}
	q := u.Query()
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= minSearchLimit {
		return rawurl, false
	}
	limit /= 2
	if limit < minSearchLimit {
		limit = minSearchLimit
	}
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()
	return u.String(), true
}

// limitParamOf extracts the limit param for logging; "" when absent.
func limitParamOf(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil {
		return ""
	}
	return u.Query().Get("limit")
}
