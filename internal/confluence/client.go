// Package confluence provides a Confluence ingestion source: a REST API
// client with CQL pagination plus mapping to Onyx ingestion documents.
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		Next string `json:"next"`
	} `json:"_links"`
}

// Options configures a Client. BaseURL must be the site URL including any
// context path, e.g. "https://example.atlassian.net/wiki".
type Options struct {
	BaseURL string
	Auth    Auth
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

// SearchPages runs a CQL query and returns one batch of results with page
// storage bodies and version timestamps. start/limit follow the Confluence
// offset pagination contract (limit is capped at 100 by Confluence).
func (c *Client) SearchPages(ctx context.Context, cql string, start, limit int) (SearchResult, error) {
	var out SearchResult
	if limit <= 0 {
		limit = 100
	} else if limit > 100 {
		limit = 100
	}

	u, err := url.Parse(c.apiBase() + "/rest/api/content/search")
	if err != nil {
		return out, fmt.Errorf("confluence API URL: %w", err)
	}
	q := u.Query()
	q.Set("cql", cql)
	q.Set("start", strconv.Itoa(start))
	q.Set("limit", strconv.Itoa(limit))
	// atlas_doc_format gives a structured JSON document on Cloud; storage is
	// kept as a fallback for legacy pages and Data Center.
	q.Set("expand", "body.atlas_doc_format,body.storage,children.attachment,version")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return out, fmt.Errorf("confluence search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	c.authorize(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return out, fmt.Errorf("confluence search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return out, fmt.Errorf("confluence search: unexpected status %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("confluence search: decode response: %w", err)
	}
	return out, nil
}
