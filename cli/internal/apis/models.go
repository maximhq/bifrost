package apis

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// Model represents a single model entry returned by the /v1/models API.
type Model struct {
	ID string `json:"id"`
}

type listModelsResp struct {
	Data []Model `json:"data"`
}

// Client wraps HTTP calls used by the legacy interactive launcher. The
// command stack replaces this adapter with the shared authenticated client in
// the subsequent application-integration change.
type Client struct {
	http *http.Client
}

// NewClient creates a model client with a bounded request timeout.
func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 20 * time.Second}}
}

// NormalizeBaseURL trims whitespace and trailing slashes from a base URL.
func NormalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, "/")
	return raw
}

// BuildEndpoint joins a base URL with a path suffix, returning the full endpoint URL.
func BuildEndpoint(baseURL, suffix string) (string, error) {
	baseURL = NormalizeBaseURL(baseURL)
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid base url %q", baseURL)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + suffix
	return u.String(), nil
}

// ListModels fetches the model catalog for the legacy interactive launcher.
func (c *Client) ListModels(ctx context.Context, baseURL, virtualKey string) ([]string, error) {
	endpoint, err := BuildEndpoint(baseURL, "/v1/models")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if strings.TrimSpace(virtualKey) != "" {
		req.Header.Set("x-bf-vk", strings.TrimSpace(virtualKey))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request /v1/models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("/v1/models status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	const maxModelsResponseBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read model response: %w", err)
	}
	if len(body) > maxModelsResponseBytes {
		return nil, fmt.Errorf("model response exceeds %d bytes", maxModelsResponseBytes)
	}
	return ParseModels(body)
}

// ParseModels returns the sorted, de-duplicated model IDs from /v1/models.
func ParseModels(body []byte) ([]string, error) {
	var parsed listModelsResp
	if err := sonic.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse model response: %w", err)
	}

	set := map[string]struct{}{}
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
	models := make([]string, 0, len(set))
	for m := range set {
		models = append(models, m)
	}
	sort.Strings(models)
	return models, nil
}
