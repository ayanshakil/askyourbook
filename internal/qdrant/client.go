// Package qdrant is a small client for Qdrant's REST API.
package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 30 * time.Second}}
}

type Point struct {
	ID      string         `json:"id"` // must be a UUID or unsigned int
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

type SearchResult struct {
	ID      any            `json:"id"`
	Score   float32        `json:"score"`
	Payload map[string]any `json:"payload"`
}

// EnsureCollection creates the collection (cosine distance) if it doesn't exist,
// plus a keyword index on doc_id so per-document filtering stays fast.
func (c *Client) EnsureCollection(ctx context.Context, name string, dim int) error {
	status, _, err := c.raw(ctx, http.MethodGet, "/collections/"+name, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	body := map[string]any{"vectors": map[string]any{"size": dim, "distance": "Cosine"}}
	if err := c.do(ctx, http.MethodPut, "/collections/"+name, body, nil); err != nil {
		return fmt.Errorf("create collection: %w", err)
	}
	idx := map[string]any{"field_name": "doc_id", "field_schema": "keyword"}
	return c.do(ctx, http.MethodPut, "/collections/"+name+"/index", idx, nil)
}

func (c *Client) Upsert(ctx context.Context, collection string, points []Point) error {
	return c.do(ctx, http.MethodPut, "/collections/"+collection+"/points?wait=true",
		map[string]any{"points": points}, nil)
}

// Search returns the top-k points nearest to vector, restricted to one document.
func (c *Client) Search(ctx context.Context, collection string, vector []float32, k int, docID string) ([]SearchResult, error) {
	body := map[string]any{
		"vector":       vector,
		"limit":        k,
		"with_payload": true,
		"filter": map[string]any{
			"must": []any{map[string]any{"key": "doc_id", "match": map[string]any{"value": docID}}},
		},
	}
	var out struct {
		Result []SearchResult `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, "/collections/"+collection+"/points/search", body, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	status, data, err := c.raw(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("qdrant %s %s: %d %s", method, path, status, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) raw(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}
