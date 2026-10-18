package onyx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func testDocument() Document {
	updated := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	return Document{
		ID:                 "confluence-123",
		Sections:           []Section{{Text: "Body text", Link: "https://ex/wiki/x"}},
		Source:             "confluence",
		SemanticIdentifier: "Hello",
		Metadata:           map[string]string{"confluence_id": "123"},
		DocUpdatedAt:       &updated,
		Title:              "Hello",
		FromIngestionAPI:   true,
	}
}

func TestIngestPayload(t *testing.T) {
	var payload map[string]any
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, ingestionPath, r.URL.Path)
		assert.Equal(t, "Bearer onyx-key", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &payload))
		return cannedResponse(http.StatusOK, "{}"), nil
	})}

	c := NewClient("https://onyx.example.com", "onyx-key", 12, hc)
	require.NoError(t, c.Ingest(context.Background(), testDocument()))

	assert.Equal(t, float64(12), payload["cc_pair_id"])
	require.NotNil(t, payload["document"])
	doc, ok := payload["document"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "confluence-123", doc["id"])
	assert.Equal(t, "confluence", doc["source"])
	assert.Equal(t, true, doc["from_ingestion_api"])
	assert.Len(t, doc["sections"], 1)
}

func TestIngestHTTPError(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusUnauthorized, "unauthorized"), nil
	})}

	c := NewClient("https://onyx.example.com", "bad-key", 12, hc)
	err := c.Ingest(context.Background(), testDocument())
	require.Error(t, err)
	assert.ErrorContains(t, err, "401")
	assert.NotContains(t, err.Error(), "bad-key", "error must not leak the API key")
}
