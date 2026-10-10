package openapimcp

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBaseURL(t *testing.T) {
	doc := &Document{Servers: []string{"/relative", "https://api.example.com/v1/"}, Self: "https://self.example.com/spec.yaml"}

	base, explicit, err := ResolveBaseURL(doc, str(" https://override.example.com/ "), "")
	require.NoError(t, err)
	assert.Equal(t, "https://override.example.com", base)
	assert.True(t, explicit)

	_, _, err = ResolveBaseURL(doc, str("ftp://x"), "")
	require.ErrorContains(t, err, "absolute http(s) URL")

	base, explicit, err = ResolveBaseURL(&Document{Servers: []string{"https://api.example.com/v1/"}}, nil, "")
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com/v1", base)
	assert.False(t, explicit)

	base, _, err = ResolveBaseURL(doc, str(""), "")
	require.NoError(t, err)
	assert.Equal(t, "https://self.example.com/relative", base, "a relative server resolves against $self before the absolute one is reached")

	base, _, err = ResolveBaseURL(&Document{Servers: []string{"/v2"}}, nil, "https://host.example.com/docs/openapi.json")
	require.NoError(t, err)
	assert.Equal(t, "https://host.example.com/v2", base)

	_, _, err = ResolveBaseURL(&Document{Servers: []string{"/v2"}}, nil, "")
	require.ErrorContains(t, err, "set openapi_config.base_url")
	_, _, err = ResolveBaseURL(nil, nil, "")
	require.Error(t, err)
}

func TestPreview(t *testing.T) {
	data := fixture(t, "petstore-3.0.yaml")
	result, syn, err := Preview(data, ParseOptions{}, SynthesizeOptions{ClientName: "petstore"}, nil)
	require.NoError(t, err)
	require.NotNil(t, syn)

	assert.Equal(t, "Petstore", result.Title)
	assert.Equal(t, "3.0.3", result.OpenAPIVersion)
	assert.Equal(t, "https://petstore.example.com/v1", result.BaseURL)
	assert.Equal(t, 5, result.ToolCount)
	assert.Len(t, result.Tools, 5)
	assert.Len(t, result.Unsupported, 2)
	assert.Equal(t, len(data), result.SpecSize)
	assert.Equal(t, SpecHash(data), result.SpecHash)
	assert.Len(t, result.SecuritySchemes, 5)
	assert.Equal(t, "ApiKeyAuth", result.SecuritySchemes[0].Name, "schemes are sorted by name")

	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	for _, key := range []string{"title", "version", "openapi_version", "servers", "base_url", "security_schemes", "tools", "unsupported", "warnings", "tool_count", "spec_size", "spec_hash"} {
		assert.Contains(t, wire, key)
	}
	firstTool := asMap(asSlice(wire["tools"])[0])
	for _, key := range []string{"name", "method", "path", "description", "input_schema", "annotations"} {
		assert.Contains(t, firstTool, key)
	}

	withOverride, _, err := Preview(data, ParseOptions{}, SynthesizeOptions{}, str("https://other.example.com"))
	require.NoError(t, err)
	assert.Equal(t, "https://other.example.com", withOverride.BaseURL)

	noServers := []byte(`{"openapi":"3.0.0","info":{"title":"x","version":"1"},"paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	result, _, err = Preview(noServers, ParseOptions{}, SynthesizeOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.BaseURL)
	assert.Contains(t, result.Warnings[len(result.Warnings)-1], "base_url")

	_, _, err = Preview([]byte("nope"), ParseOptions{}, SynthesizeOptions{}, nil)
	require.Error(t, err)
}

func TestApplyMetadata(t *testing.T) {
	data := fixture(t, "petstore-3.2.yaml")
	_, syn, err := Preview(data, ParseOptions{}, SynthesizeOptions{}, nil)
	require.NoError(t, err)
	cfg := &schemas.MCPOpenAPIConfig{Spec: string(data)}
	ApplyMetadata(cfg, data, syn.Document, syn)
	assert.Equal(t, len(data), cfg.SpecSize)
	assert.Equal(t, SpecHash(data), cfg.SpecHash)
	assert.Equal(t, "Petstore 3.2", cfg.SpecTitle)
	assert.Equal(t, "3.2.0", cfg.OpenAPIVersion)
	assert.Equal(t, 4, cfg.OperationCount)
	ApplyMetadata(nil, data, nil, nil)
}
