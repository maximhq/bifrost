package clis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// bifrostClient queries the Bifrost management API to (a) confirm the server
// is reachable and (b) discover which providers are configured, so we can
// skip matrix cells whose provider has no key.
type bifrostClient struct {
	baseURL string
	http    *http.Client
}

func newBifrostClient(baseURL string) *bifrostClient {
	return &bifrostClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *bifrostClient) Health(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/providers", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("bifrost /api/providers -> %d", resp.StatusCode)
	}
	return nil
}

type providerEntry struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	ID       string `json:"id"`
}

// ConfiguredProviders returns the set of provider IDs Bifrost reports as
// having a configured key. The shape of /api/providers has varied across
// versions, so we accept both an array root and an object with a known field.
func (c *bifrostClient) ConfiguredProviders(ctx context.Context) (map[string]bool, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/providers", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bifrost /api/providers -> %d: %s", resp.StatusCode, string(body))
	}

	var asArray []providerEntry
	if err := json.Unmarshal(body, &asArray); err == nil && len(asArray) > 0 {
		return collectEntries(asArray), nil
	}
	var asObject struct {
		Providers []providerEntry `json:"providers"`
		Data      []providerEntry `json:"data"`
	}
	if err := json.Unmarshal(body, &asObject); err != nil {
		return nil, fmt.Errorf("decode providers: %w", err)
	}
	return collectEntries(append(asObject.Providers, asObject.Data...)), nil
}

func collectEntries(entries []providerEntry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		for _, v := range []string{e.Provider, e.Name, e.ID} {
			if v != "" {
				out[v] = true
			}
		}
	}
	return out
}
