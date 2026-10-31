package apis

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
)

// Model represents a single model entry returned by the /v1/models API.
type Model struct {
	ID string `json:"id"`
}

type listModelsResp struct {
	Data []Model `json:"data"`
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
