// Package onyx implements the Onyx document ingestion API client.
package onyx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
type Document struct {
	ID                 string            `json:"id"`
	Sections           []Section         `json:"sections"`
	Source             string            `json:"source"`
	SemanticIdentifier string            `json:"semantic_identifier"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	DocUpdatedAt       *time.Time        `json:"doc_updated_at"`
	Title              string            `json:"title"`
	FromIngestionAPI   bool              `json:"from_ingestion_api"`
}

type ingestRequest struct {
	Document Document `json:"document"`
	CCPairID int      `json:"cc_pair_id"`
}

// Client pushes documents to a single Onyx connector/credential pair
// over the Onyx ingestion API.
//
// TODO(onyx-ingestor): the ingestion endpoint path is "/onyx-api/ingestion"
// in current Onyx releases; confirm against your deployment and adjust
// ingestionPath if needed.
const ingestionPath = "/onyx-api/ingestion"

// Client ingests documents into Onyx via its ingestion API.
type Client struct {
	baseURL  string
	apiKey   string
	ccPairID int
	hc       *http.Client
}

// NewClient creates a Client scoped to one base URL and CC pair.
func NewClient(baseURL, apiKey string, ccPairID int, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		ccPairID: ccPairID,
		hc:       hc,
	}
}

// Ingest pushes one document to the configured CC pair.
func (c *Client) Ingest(ctx context.Context, doc Document) error {
	body, err := json.Marshal(ingestRequest{Document: doc, CCPairID: c.ccPairID})
	if err != nil {
		return fmt.Errorf("serialize document %q: %w", doc.ID, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+ingestionPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ingestion request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("onyx ingestion for document %q: %w", doc.ID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("onyx ingestion for document %q: unexpected status %s: %s",
			doc.ID, resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}
