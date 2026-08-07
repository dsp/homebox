package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiKeyCtxKey carries the caller's HomeBox API key (from the MCP request's
// Authorization header) through to REST calls. The server is stateless: every
// request re-authenticates with whatever key it carried.
type apiKeyCtxKey struct{}

func withAPIKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, apiKeyCtxKey{}, key)
}

func apiKeyFromContext(ctx context.Context) string {
	key, _ := ctx.Value(apiKeyCtxKey{}).(string)
	return key
}

// homeboxClient is a minimal client for the HomeBox REST API, covering only
// what the MCP tools need. It holds no credentials of its own — the per-user
// API key travels in the request context.
type homeboxClient struct {
	baseURL *url.URL
	httpc   *http.Client
}

func newHomeboxClient(baseURL string) (*homeboxClient, error) {
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid HomeBox URL %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("HomeBox URL must use http or https, got %q", baseURL)
	}
	return &homeboxClient{
		baseURL: u,
		httpc:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Wire types mirror the subset of the HomeBox v1 API responses the tools
// consume. Field names must track backend/internal/data/repo.
type (
	entitySummary struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Quantity    float64 `json:"quantity"`
		Archived    bool    `json:"archived"`

		Parent *entitySummary `json:"parent,omitempty"`
	}

	entityListResult struct {
		Items []entitySummary `json:"items"`
		Total int             `json:"total"`
	}

	entityPathSegment struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
	}

	treeItem struct {
		ID       string      `json:"id"`
		Name     string      `json:"name"`
		Type     string      `json:"type"`
		Children []*treeItem `json:"children"`
	}

	entityCreateRequest struct {
		Name        string  `json:"name"`
		Description string  `json:"description,omitempty"`
		Quantity    float64 `json:"quantity"`
		ParentID    string  `json:"parentId,omitempty"`
	}

	entityPatchRequest struct {
		ID       string   `json:"id"`
		Quantity *float64 `json:"quantity,omitempty"`
		ParentID string   `json:"parentId,omitempty"`
	}
)

// apiError is returned for non-2xx REST responses so tools can distinguish
// auth failures from other errors.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "HomeBox rejected the API key; check the Authorization header (expects a HomeBox API key, e.g. hbox_...)"
	default:
		// Pass validation detail through — "status 422" alone sends the
		// calling model into a retry loop with the same bad payload.
		detail := strings.TrimSpace(e.Body)
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		if detail == "" {
			return fmt.Sprintf("HomeBox API returned status %d", e.Status)
		}
		return fmt.Sprintf("HomeBox API returned status %d: %s", e.Status, detail)
	}
}

func (c *homeboxClient) do(ctx context.Context, method, apiPath string, query url.Values, body, out any) error {
	key := apiKeyFromContext(ctx)
	if key == "" {
		return fmt.Errorf("no HomeBox API key on this request; send it as 'Authorization: Bearer <hbox_... key>'")
	}

	u := *c.baseURL
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/v1" + apiPath
	if query != nil {
		u.RawQuery = query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("request to HomeBox failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &apiError{Status: resp.StatusCode, Body: string(detail)}
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func (c *homeboxClient) searchEntities(ctx context.Context, q string, pageSize int) (entityListResult, error) {
	var result entityListResult
	query := url.Values{}
	if q != "" {
		query.Set("q", q)
	}
	query.Set("pageSize", fmt.Sprint(pageSize))
	err := c.do(ctx, http.MethodGet, "/entities", query, nil, &result)
	return result, err
}

func (c *homeboxClient) getEntity(ctx context.Context, id string) (entitySummary, error) {
	var result entitySummary
	err := c.do(ctx, http.MethodGet, "/entities/"+url.PathEscape(id), nil, nil, &result)
	return result, err
}

func (c *homeboxClient) entityPath(ctx context.Context, id string) ([]entityPathSegment, error) {
	var result []entityPathSegment
	err := c.do(ctx, http.MethodGet, "/entities/"+url.PathEscape(id)+"/path", nil, nil, &result)
	return result, err
}

func (c *homeboxClient) locationTree(ctx context.Context) ([]*treeItem, error) {
	var result []*treeItem
	err := c.do(ctx, http.MethodGet, "/entities/tree", nil, nil, &result)
	return result, err
}

func (c *homeboxClient) createEntity(ctx context.Context, req entityCreateRequest) (entitySummary, error) {
	var result entitySummary
	err := c.do(ctx, http.MethodPost, "/entities", nil, req, &result)
	return result, err
}

func (c *homeboxClient) patchEntity(ctx context.Context, req entityPatchRequest) (entitySummary, error) {
	var result entitySummary
	err := c.do(ctx, http.MethodPatch, "/entities/"+url.PathEscape(req.ID), nil, req, &result)
	return result, err
}
