// Package onyx implements the Onyx document ingestion API client.
package onyx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// Section is a chunk of document content with an optional deep link.
type Section struct {
	Text string `json:"text"`
	Link string `json:"link"`
}

// Document matches the Onyx ingestion API document payload.
// TODO(onyx-ingestor): verify this schema against your Onyx version; the
// ingestion API may add fields over time.
// The Onyx ingestion model requires every key to be present (null is
// allowed, absence is not), so no omitempty here.
type Document struct {
	ID                 string            `json:"id"`
	Sections           []Section         `json:"sections"`
	Source             string            `json:"source"`
	SemanticIdentifier string            `json:"semantic_identifier"`
	Metadata           map[string]string `json:"metadata"`
	DocUpdatedAt       *time.Time        `json:"doc_updated_at"`
	PrimaryOwners      any               `json:"primary_owners"`
	SecondaryOwners    any               `json:"secondary_owners"`
	Title              string            `json:"title"`
	FromIngestionAPI   bool              `json:"from_ingestion_api"`
}

type ingestRequest struct {
	Document Document `json:"document"`
	CCPairID int      `json:"cc_pair_id"`
}

// DefaultIngestionPath matches the official Onyx ingestion API. Deployments
// behind the Onyx chart's nginx reverse proxy only forward /api/* to the API
// server, so those need ONYX_INGESTION_PATH="/api/onyx-api/ingestion".
const DefaultIngestionPath = "/onyx-api/ingestion"

// Client pushes documents to a single Onyx connector/credential pair
// over the Onyx ingestion API.

// Client ingests documents into Onyx via its ingestion API.
type Client struct {
	baseURL       string
	apiKey        string
	ccPairID      int
	ingestionPath string
	hc            *http.Client
}

// NewClient creates a Client scoped to one base URL and CC pair. Pass an
// empty ingestionPath to use DefaultIngestionPath.
func NewClient(baseURL, apiKey string, ccPairID int, ingestionPath string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	if ingestionPath == "" {
		ingestionPath = DefaultIngestionPath
	}
	if !strings.HasPrefix(ingestionPath, "/") {
		ingestionPath = "/" + ingestionPath
	}
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		apiKey:        apiKey,
		ccPairID:      ccPairID,
		ingestionPath: ingestionPath,
		hc:            hc,
	}
}

// Ingest pushes one document to the configured CC pair, retrying transient
// failures (closed keep-alive connections, 429, 502-504) with backoff. Bulk
// imports reuse keep-alive connections across hundreds of requests; proxies
// in front of Onyx cap requests per connection and close them silently, so
// every retry is forced onto a fresh connection.
func (c *Client) Ingest(ctx context.Context, doc Document) error {
	body, err := json.Marshal(ingestRequest{Document: doc, CCPairID: c.ccPairID})
	if err != nil {
		return fmt.Errorf("serialize document %q: %w", doc.ID, err)
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("onyx ingestion for document %q: %w", doc.ID, err)
		}
		if attempt > 1 {
			if !sleep(ctx, backoff(attempt)) {
				return fmt.Errorf("onyx ingestion for document %q: %w", doc.ID, ctx.Err())
			}
		}

		status, respBody, err := c.doRound(ctx, body, attempt > 1)
		switch {
		case err == nil && status/100 == 2:
			return nil
		case !retryable(err, status):
			return wrapErr(err, doc.ID, status, respBody)
		}
		lastErr = wrapErr(err, doc.ID, status, respBody)
		slog.Warn("onyx ingest retry",
			"doc_id", doc.ID, "attempt", attempt, "status", status, "error", lastErr)
	}
	return lastErr
}

// doRound performs one ingestion request and returns the status and a
// truncated response body. When freshConn is set the request uses a new
// connection rather than reusing the keep-alive pool.
func (c *Client) doRound(ctx context.Context, body []byte, freshConn bool) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.ingestionPath, bytes.NewReader(body))
	if err != nil {
		return 0, "", fmt.Errorf("build ingestion request: %w", err)
	}
	req.Close = freshConn
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, "", err
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	return resp.StatusCode, strings.TrimSpace(string(respBody)), nil
}

// retryable reports whether the failure may succeed on a later attempt.
func retryable(transportErr error, status int) bool {
	if transportErr != nil {
		return true
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func wrapErr(err error, docID string, status int, respBody string) error {
	if err != nil {
		return fmt.Errorf("onyx ingestion for document %q: %w", docID, err)
	}
	return fmt.Errorf("onyx ingestion for document %q: unexpected status %s (%d): %s",
		docID, http.StatusText(status), status, respBody)
}

// backoff grows exponentially from 500ms, capped at 30s, with jitter.
func backoff(attempt int) time.Duration {
	d := 500 * time.Millisecond << (attempt - 1)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// sleep waits for d or until ctx is canceled. Returns false on cancellation.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

const maxAttempts = 5
