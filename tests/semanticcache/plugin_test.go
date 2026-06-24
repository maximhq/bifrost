package semanticcache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

const pluginName = "semantic_cache"

// createPluginRequest mirrors handlers.CreatePluginRequest + ui/lib/types/plugins.ts.
// path is always sent (UI sends "" for built-ins; backend normalizes empty → nil).
type createPluginRequest struct {
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	Enabled   bool           `json:"enabled"`
	Config    map[string]any `json:"config"`
	Placement *string        `json:"placement,omitempty"`
	Order     *int           `json:"order,omitempty"`
}

// updatePluginRequest mirrors handlers.UpdatePluginRequest. The UI ALWAYS re-sends
// the current config alongside enabled, never PUTs `{enabled:false}` alone —
// that would wipe the DB config row (handlers/plugins.go:399).
type updatePluginRequest struct {
	Enabled   bool           `json:"enabled"`
	Path      *string        `json:"path,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
	Placement *string        `json:"placement,omitempty"`
	Order     *int           `json:"order,omitempty"`
}

type pluginStatus struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Logs   []string `json:"logs"`
}

type pluginResponse struct {
	Name       string         `json:"name"`
	ActualName string         `json:"actualName"`
	Enabled    bool           `json:"enabled"`
	Config     map[string]any `json:"config"`
	IsCustom   bool           `json:"isCustom"`
	Path       *string        `json:"path,omitempty"`
	Placement  *string        `json:"placement,omitempty"`
	Order      *int           `json:"order,omitempty"`
	Status     pluginStatus   `json:"status"`
}

type pluginEnvelope struct {
	Message string         `json:"message"`
	Plugin  pluginResponse `json:"plugin"`
}

// directOnlyConfig returns the plugin config blob for direct-only mode.
// Mirrors what cachingView.tsx buildPayload produces for mode="direct".
func directOnlyConfig(ttl string, defaultKey string) map[string]any {
	c := map[string]any{
		"dimension":                      1,
		"ttl":                            ttl,
		"threshold":                      0.8,
		"conversation_history_threshold": 3,
		"exclude_system_prompt":          false,
		"cache_by_model":                 true,
		"cache_by_provider":              true,
		"vector_store_namespace":         cfg.Namespace,
	}
	if defaultKey != "" {
		c["default_cache_key"] = defaultKey
	}
	return c
}

// semanticConfig returns the plugin config blob for semantic mode.
func semanticConfig(provider, embedModel string, dimension int, ttl string, threshold float64, defaultKey string) map[string]any {
	c := map[string]any{
		"provider":                       provider,
		"embedding_model":                embedModel,
		"dimension":                      dimension,
		"ttl":                            ttl,
		"threshold":                      threshold,
		"conversation_history_threshold": 3,
		"exclude_system_prompt":          false,
		"cache_by_model":                 true,
		"cache_by_provider":              true,
		"vector_store_namespace":         cfg.Namespace,
	}
	if defaultKey != "" {
		c["default_cache_key"] = defaultKey
	}
	return c
}

// pluginGet fetches the plugin row; returns (resp, true) if found, (nil, false) on 404.
func pluginGet(t *testing.T, lc logCtx, step int) (*pluginResponse, bool) {
	t.Helper()
	status, body, _, err := doJSON(t, "GET", "/api/plugins/"+pluginName, nil, nil)
	if err != nil {
		t.Fatalf("pluginGet http error: %v", err)
	}
	if status == http.StatusNotFound {
		logf(t, lc.at(step), "INFO", "plugin_get", map[string]any{"status": status, "exists": false})
		return nil, false
	}
	if status != http.StatusOK {
		t.Fatalf("pluginGet unexpected status=%d body=%s", status, truncate(string(body), 300))
	}
	var p pluginResponse
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("pluginGet decode: %v\nbody=%s", err, truncate(string(body), 300))
	}
	logf(t, lc.at(step), "INFO", "plugin_get", map[string]any{
		"status":  status,
		"exists":  true,
		"enabled": p.Enabled,
		"plugin_status": p.Status.Status,
	})
	return &p, true
}

// pluginCreate matches the UI flow: POST /api/plugins with path:"" for built-ins.
func pluginCreate(t *testing.T, lc logCtx, step int, enabled bool, config map[string]any) *pluginResponse {
	t.Helper()
	req := createPluginRequest{
		Name:    pluginName,
		Path:    "", // UI always sends "" for built-ins (cachingView.tsx:225)
		Enabled: enabled,
		Config:  config,
	}
	if reqJSON, _ := json.MarshalIndent(req, "", "  "); reqJSON != nil {
		dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.plugin_create.req.json", lc.phase, lc.name, step), reqJSON)
	}
	logf(t, lc.at(step), "INFO", "plugin_create", map[string]any{
		"enabled":   enabled,
		"mode":      modeFromConfig(config),
		"namespace": fmt.Sprintf("%v", config["vector_store_namespace"]),
	})
	status, body, _, err := doJSON(t, "POST", "/api/plugins", req, nil)
	if err != nil {
		t.Fatalf("pluginCreate http error: %v", err)
	}
	dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.plugin_create.resp.json", lc.phase, lc.name, step), body)
	if status != http.StatusCreated {
		t.Fatalf("pluginCreate status=%d body=%s", status, truncate(string(body), 500))
	}
	var env pluginEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("pluginCreate decode: %v\nbody=%s", err, truncate(string(body), 500))
	}
	logf(t, lc.at(step), "PASS", "plugin_created", map[string]any{
		"status":        env.Plugin.Status.Status,
		"enabled":       env.Plugin.Enabled,
	})
	return &env.Plugin
}

// pluginUpdate matches the UI flow: PUT with {enabled, config} — always re-send
// config when toggling enabled, never PUT bare {enabled:false} (would wipe DB row).
func pluginUpdate(t *testing.T, lc logCtx, step int, enabled bool, config map[string]any) *pluginResponse {
	t.Helper()
	req := updatePluginRequest{
		Enabled: enabled,
		Config:  config,
	}
	if reqJSON, _ := json.MarshalIndent(req, "", "  "); reqJSON != nil {
		dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.plugin_update.req.json", lc.phase, lc.name, step), reqJSON)
	}
	logf(t, lc.at(step), "INFO", "plugin_update", map[string]any{
		"enabled": enabled,
		"mode":    modeFromConfig(config),
	})
	status, body, _, err := doJSON(t, "PUT", "/api/plugins/"+pluginName, req, nil)
	if err != nil {
		t.Fatalf("pluginUpdate http error: %v", err)
	}
	dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.plugin_update.resp.json", lc.phase, lc.name, step), body)
	if status != http.StatusOK {
		t.Fatalf("pluginUpdate status=%d body=%s", status, truncate(string(body), 500))
	}
	var env pluginEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("pluginUpdate decode: %v\nbody=%s", err, truncate(string(body), 500))
	}
	logf(t, lc.at(step), "PASS", "plugin_updated", map[string]any{
		"status":  env.Plugin.Status.Status,
		"enabled": env.Plugin.Enabled,
	})
	return &env.Plugin
}

// pluginDelete removes the plugin row + in-memory instance.
func pluginDelete(t *testing.T, lc logCtx, step int) {
	t.Helper()
	status, body, _, err := doJSON(t, "DELETE", "/api/plugins/"+pluginName, nil, nil)
	if err != nil {
		t.Fatalf("pluginDelete http error: %v", err)
	}
	if status != http.StatusOK && status != http.StatusNotFound {
		t.Fatalf("pluginDelete status=%d body=%s", status, truncate(string(body), 300))
	}
	logf(t, lc.at(step), "INFO", "plugin_deleted", map[string]any{"status": status})
}

// modeFromConfig describes a config blob in one word for log fields.
func modeFromConfig(c map[string]any) string {
	if p, _ := c["provider"].(string); p != "" {
		return "semantic"
	}
	return "direct-only"
}
