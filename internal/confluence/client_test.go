package confluence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const searchJSON = `{
  "results": [
    {
      "id": "123",
      "title": "Hello",
      "version": {"when": "2024-05-01T10:00:00.000Z"},
      "body": {"storage": {"value": "<p>Hi</p>"}},
      "_links": {"base": "https://ex.atlassian.net/wiki", "webui": "/spaces/X/pages/123"}
    }
  ],
  "start": 0,
  "limit": 50,
  "size": 1,
  "_links": {}
}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func cannedResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func basicAuth() Auth {
	return Auth{Mode: ModeBasic, Username: "user@example.com", Token: "secret"}
}

func TestResolveModeAuto(t *testing.T) {
	cases := []struct {
		authType, username, cloudID string
		want                        Mode
	}{
		{"", "u", "", ModeBasic},
		{"auto", "", "cloud-1", ModeScopedBearer},
		{"", "", "", ModeBearer},
		{"BASIC", "u", "", ModeBasic},
		{"bearer", "", "", ModeBearer},
		{"scoped", "", "c", ModeScopedBearer},
	}
	for _, tc := range cases {
		got, err := ResolveMode(tc.authType, tc.username, tc.cloudID)
		require.NoError(t, err, "authType=%q", tc.authType)
		assert.Equal(t, tc.want, got,
			"ResolveMode(%q, %q, %q)", tc.authType, tc.username, tc.cloudID)
	}

	_, err := ResolveMode("oauth", "", "")
	assert.Error(t, err)
}

func TestSearchPagesBasicOK(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.True(t, strings.HasPrefix(r.URL.String(),
			"https://ex.atlassian.net/wiki/rest/api/content/search"),
			"unexpected url %q", r.URL.String())
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok, "basic auth header missing")
		assert.Equal(t, "user@example.com", user)
		assert.Equal(t, "secret", pass)
		q := r.URL.Query()
		for key, want := range map[string]string{
			"cql": "type=page", "start": "0", "limit": "50", "expand": "body.atlas_doc_format,body.storage,children.attachment,version",
		} {
			assert.Equal(t, want, q.Get(key), "query %s", key)
		}
		return cannedResponse(http.StatusOK, searchJSON), nil
	})}

	c, err := NewClient(Options{BaseURL: "https://ex.atlassian.net/wiki", Auth: basicAuth()}, hc)
	require.NoError(t, err)

	res, err := c.SearchPages(context.Background(), "type=page", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Size)
	require.Len(t, res.Results, 1)

	page := res.Results[0]
	assert.False(t, page.Version.When.IsZero(), "version.when did not parse")
	assert.Equal(t, "https://ex.atlassian.net/wiki/spaces/X/pages/123", page.WebURL())
}

func TestSearchPagesScopedGoesThroughGateway(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.True(t, strings.HasPrefix(r.URL.String(),
			"https://api.atlassian.com/ex/confluence/cloud-123/wiki/rest/api/content/search"),
			"unexpected url %q", r.URL.String())
		assert.Equal(t, "Bearer scoped-token", r.Header.Get("Authorization"))
		return cannedResponse(http.StatusOK, `{"results":[],"size":0,"_links":{}}`), nil
	})}

	auth := Auth{Mode: ModeScopedBearer, Token: "scoped-token", CloudID: "cloud-123"}
	c, err := NewClient(Options{BaseURL: "https://ex.atlassian.net/wiki", Auth: auth}, hc)
	require.NoError(t, err)

	_, err = c.SearchPages(context.Background(), "type=page", 0, 10)
	require.NoError(t, err)
}

func TestSearchPagesBearerWithoutUsername(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "Bearer pat-token", r.Header.Get("Authorization"))
		return cannedResponse(http.StatusOK, `{"results":[],"size":0,"_links":{}}`), nil
	})}

	c, err := NewClient(Options{
		BaseURL: "https://confluence.example.com",
		Auth:    Auth{Mode: ModeBearer, Token: "pat-token"},
	}, hc)
	require.NoError(t, err)

	_, err = c.SearchPages(context.Background(), "type=page", 0, 0)
	require.NoError(t, err)
}

func TestSearchPageURLFollowsCursorVerbatim(t *testing.T) {
	// The scoped gateway paginates CQL search via an opaque cursor carried in
	// _links.next; the client must join it to the API base unchanged.
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t,
			"https://api.atlassian.com/ex/confluence/cloud-123/wiki/rest/api/content/search?next=true&cursor=abc%3D&limit=25",
			r.URL.String())
		return cannedResponse(http.StatusOK, `{"results":[],"size":0,"_links":{}}`), nil
	})}
	auth := Auth{Mode: ModeScopedBearer, Token: "scoped-token", CloudID: "cloud-123"}
	c, err := NewClient(Options{BaseURL: "https://ex.atlassian.net/wiki", Auth: auth}, hc)
	require.NoError(t, err)

	_, err = c.SearchPageURL(context.Background(),
		"/rest/api/content/search?next=true&cursor=abc%3D&limit=25")
	require.NoError(t, err)

	_, err = c.SearchPageURL(context.Background(), "https://evil.example.com/x")
	require.Error(t, err)
}

func TestCountPagesReadsTotalSize(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "/wiki/rest/api/search", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "type=page", q.Get("cql"))
		assert.Equal(t, "1", q.Get("limit"))
		return cannedResponse(http.StatusOK, `{"size":1,"totalSize":1587}`), nil
	})}
	c, err := NewClient(Options{BaseURL: "https://ex.atlassian.net/wiki", Auth: basicAuth()}, hc)
	require.NoError(t, err)

	total, err := c.CountPages(context.Background(), "type=page")
	require.NoError(t, err)
	assert.Equal(t, 1587, total)
}

func TestNewClientValidation(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"bad base URL", Options{BaseURL: "not-a-url", Auth: basicAuth()}},
		{"missing token", Options{BaseURL: "https://x/wiki", Auth: Auth{Mode: ModeBasic, Username: "u"}}},
		{"basic without username", Options{BaseURL: "https://x/wiki", Auth: Auth{Mode: ModeBasic, Token: "t"}}},
		{"scoped without cloud id", Options{BaseURL: "https://x/wiki", Auth: Auth{Mode: ModeScopedBearer, Token: "t"}}},
		{"unknown mode", Options{BaseURL: "https://x/wiki", Auth: Auth{Mode: Mode(99), Token: "t"}}},
	}
	for _, tc := range cases {
		_, err := NewClient(tc.opts, nil)
		assert.Error(t, err, tc.name)
	}
}

func TestSearchPagesLimitClamp(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "100", r.URL.Query().Get("limit"))
		return cannedResponse(http.StatusOK, `{"results":[],"size":0,"_links":{}}`), nil
	})}

	c, err := NewClient(Options{
		BaseURL: "https://confluence.example.com",
		Auth:    Auth{Mode: ModeBearer, Token: "t"},
	}, hc)
	require.NoError(t, err)

	_, err = c.SearchPages(context.Background(), "type=page", 0, 500)
	require.NoError(t, err)
}

func TestSearchPagesHTTPError(t *testing.T) {
	noSleep(t)
	hc := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusInternalServerError, "boom"), nil
	})}

	c, err := NewClient(Options{BaseURL: "https://confluence.example.com", Auth: basicAuth()}, hc)
	require.NoError(t, err)

	_, err = c.SearchPages(context.Background(), "type=page", 0, 10)
	require.Error(t, err)
	assert.ErrorContains(t, err, "500")
	assert.NotContains(t, err.Error(), "secret", "error must not leak the token")
}

// noSleep disables retry backoff in tests.
func noSleep(t *testing.T) {
	t.Helper()
	orig := sleepCtx
	sleepCtx = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { sleepCtx = orig })
}

func TestSearchPagesRetries429HonoringRetryAfter(t *testing.T) {
	noSleep(t)
	var attempts int
	var sawRetryAfter bool
	hc := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		if attempts < 3 {
			sawRetryAfter = true
			resp := cannedResponse(http.StatusTooManyRequests, "rate limited")
			resp.Header.Set("Retry-After", "1")
			return resp, nil
		}
		return cannedResponse(http.StatusOK, searchJSON), nil
	})}
	c, err := NewClient(Options{BaseURL: "https://ex.atlassian.net/wiki", Auth: basicAuth()}, hc)
	require.NoError(t, err)

	res, err := c.SearchPages(context.Background(), "type=page", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, 3, attempts)
	assert.True(t, sawRetryAfter)
	require.Len(t, res.Results, 1)
}

func TestSearchPagesHalvesLimitOnServerError(t *testing.T) {
	noSleep(t)
	var limits []string
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		limits = append(limits, r.URL.Query().Get("limit"))
		if r.URL.Query().Get("limit") == "100" {
			return cannedResponse(http.StatusGatewayTimeout, "gateway timeout"), nil
		}
		return cannedResponse(http.StatusOK, searchJSON), nil
	})}
	c, err := NewClient(Options{BaseURL: "https://ex.atlassian.net/wiki", Auth: basicAuth()}, hc)
	require.NoError(t, err)

	_, err = c.SearchPages(context.Background(), "type=page", 0, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"100", "50"}, limits)
}

func TestSearchPagesRetryExhausted(t *testing.T) {
	noSleep(t)
	var attempts int
	hc := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		return cannedResponse(http.StatusInternalServerError, "boom"), nil
	})}
	c, err := NewClient(Options{BaseURL: "https://confluence.example.com", Auth: basicAuth()}, hc)
	require.NoError(t, err)

	_, err = c.SearchPages(context.Background(), "type=page", 0, minSearchLimit)
	require.Error(t, err)
	assert.ErrorContains(t, err, "500")
	assert.Equal(t, maxRequestAttempts, attempts)
}

func TestCountPagesRetriesTransportError(t *testing.T) {
	noSleep(t)
	var attempts int
	hc := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("connection reset")
		}
		return cannedResponse(http.StatusOK, `{"totalSize": 42}`), nil
	})}
	c, err := NewClient(Options{BaseURL: "https://ex.atlassian.net/wiki", Auth: basicAuth()}, hc)
	require.NoError(t, err)

	total, err := c.CountPages(context.Background(), "type=page")
	require.NoError(t, err)
	assert.Equal(t, 42, total)
}

func TestHalveLimitParam(t *testing.T) {
	next, ok := halveLimitParam("https://x/wiki/rest/api/content/search?cql=a&limit=100")
	assert.True(t, ok)
	assert.Contains(t, next, "limit=50")

	next, ok = halveLimitParam("https://x/wiki/rest/api/content/search?cql=a&limit=10")
	assert.True(t, ok)
	assert.Contains(t, next, "limit=5")

	_, ok = halveLimitParam("https://x/wiki/rest/api/content/search?cql=a&limit=5")
	assert.False(t, ok, "already at the floor")

	_, ok = halveLimitParam("https://x/wiki/rest/api/content/search?cql=a")
	assert.False(t, ok, "no limit param")
}

// TestSearchPagesFallsBackToEnvelopeBase mirrors Confluence Cloud behavior:
// the search envelope carries _links.base while result pages only carry
// webui. WebURL must still resolve, and a page-provided base wins.
func TestSearchPagesFallsBackToEnvelopeBase(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusOK, `{
  "results": [
    {"id": "1", "title": "A", "_links": {"webui": "/spaces/X/pages/1"}},
    {"id": "2", "title": "B", "_links": {"base": "https://page.override/wiki", "webui": "/x/2"}}
  ],
  "size": 2,
  "_links": {"base": "https://ex.atlassian.net/wiki"}
}`), nil
	})}

	c, err := NewClient(Options{BaseURL: "https://ex.atlassian.net/wiki", Auth: basicAuth()}, hc)
	require.NoError(t, err)

	res, err := c.SearchPages(context.Background(), "type=page", 0, 50)
	require.NoError(t, err)
	require.Len(t, res.Results, 2)
	assert.Equal(t, "https://ex.atlassian.net/wiki/spaces/X/pages/1", res.Results[0].WebURL())
	assert.Equal(t, "https://page.override/wiki/x/2", res.Results[1].WebURL())
}
