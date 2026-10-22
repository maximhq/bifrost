package lib

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormalizeBasePath pins the canonical form NormalizeBasePath returns and
// the values it rejects.
func TestNormalizeBasePath(t *testing.T) {
	valid := map[string]string{
		"":                "",
		"/":               "",
		"/bifrost":        "/bifrost",
		"/bifrost/":       "/bifrost",
		"/tools/llm-gw":   "/tools/llm-gw",
		"/a_b.c~d/e-f/":   "/a_b.c~d/e-f",
		"/Gateway2/v1.x":  "/Gateway2/v1.x",
		"/team/bifrost-1": "/team/bifrost-1",
	}
	for raw, want := range valid {
		got, err := NormalizeBasePath(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}

	invalid := []string{
		"bifrost",
		"bifrost/",
		"//bifrost",
		"/a//b",
		"/a/../b",
		"/..",
		"/.",
		"/bifrost?x=1",
		"/bifrost#frag",
		"/bi frost",
		`/bi\frost`,
		"/bi\"frost",
		"/bi<frost>",
		"/bi%2Ffrost",
		"/bifröst",
		// Surrounding whitespace is rejected, as the config schema does.
		"  ",
		" /bifrost ",
		"/bifrost\n",
	}
	for _, raw := range invalid {
		_, err := NormalizeBasePath(raw)
		assert.Error(t, err, raw)
	}
}

// TestResolveBasePath pins the precedence between the flag/env value and
// server.base_path in config.json.
func TestResolveBasePath(t *testing.T) {
	// The flag (or the BIFROST_BASE_PATH env it defaults from) wins over config.json.
	got, err := ResolveBasePath("/from-flag/", &ServerConfig{BasePath: "/from-config"})
	require.NoError(t, err)
	assert.Equal(t, "/from-flag", got)

	got, err = ResolveBasePath("", &ServerConfig{BasePath: "/from-config/"})
	require.NoError(t, err)
	assert.Equal(t, "/from-config", got)

	// "/" is how the flag or env forces the domain root over a config.json
	// prefix; an empty value counts as unset, like BIFROST_HOST.
	got, err = ResolveBasePath("/", &ServerConfig{BasePath: "/from-config"})
	require.NoError(t, err)
	assert.Equal(t, "", got)

	got, err = ResolveBasePath("", nil)
	require.NoError(t, err)
	assert.Equal(t, "", got)

	_, err = ResolveBasePath("", &ServerConfig{BasePath: "no-slash"})
	assert.Error(t, err)

	// A whitespace-only flag or env value is set, not unset: it is rejected like
	// any other whitespace rather than falling back to config.json.
	_, err = ResolveBasePath("   ", &ServerConfig{BasePath: "/from-config"})
	assert.Error(t, err)
}

// TestCheckBasePathCollision pins that only a prefix whose first segment is
// used by a registered route is rejected.
func TestCheckBasePathCollision(t *testing.T) {
	routes := map[string][]string{
		"GET":  {"/", "/{filepath:*}", "/health", "/api/providers", "/workspace"},
		"POST": {"/v1/chat/completions", "/openai/v1/responses"},
	}
	assert.NoError(t, CheckBasePathCollision("", routes, nil))
	assert.NoError(t, CheckBasePathCollision("/bifrost", routes, nil))
	// Only the first segment matters: a nested prefix under a free segment is fine.
	assert.NoError(t, CheckBasePathCollision("/gateway/api", routes, nil))
	assert.NoError(t, CheckBasePathCollision("/apis", routes, nil))

	for _, basePath := range []string{"/api", "/v1", "/openai", "/health", "/workspace", "/api/bifrost"} {
		assert.Error(t, CheckBasePathCollision(basePath, routes, nil), basePath)
	}
}

// TestCheckBasePathCollision_UIFiles pins that a prefix whose first segment is
// a top-level entry of the embedded UI is rejected. The UI is served by the
// "/{filepath:*}" catch-all, so these never show up as router paths; behind a
// prefix-stripping proxy, base_path "/assets" would strip "/assets/index.js"
// a second time and 404 the bundle.
func TestCheckBasePathCollision_UIFiles(t *testing.T) {
	routes := map[string][]string{"GET": {"/", "/{filepath:*}", "/health"}}
	ui := fstest.MapFS{
		"ui/index.html":            {Data: []byte("<html></html>")},
		"ui/favicon.ico":           {Data: []byte("ico")},
		"ui/assets/index-abc.js":   {Data: []byte("js")},
		"ui/images/kafka-logo.svg": {Data: []byte("svg")},
	}
	for _, basePath := range []string{"/assets", "/images", "/favicon.ico", "/assets/bifrost"} {
		assert.Error(t, CheckBasePathCollision(basePath, routes, ui), basePath)
	}
	for _, basePath := range []string{"/bifrost", "/asset", "/tools/assets"} {
		assert.NoError(t, CheckBasePathCollision(basePath, routes, ui), basePath)
	}
	// Without an embedded UI (e.g. a zero embed.FS) only router paths count.
	assert.NoError(t, CheckBasePathCollision("/assets", routes, nil))
	assert.NoError(t, CheckBasePathCollision("/assets", routes, embed.FS{}))
}

// basePathCandidates covers values the base_path schema pattern must agree
// with NormalizeBasePath on.
var basePathCandidates = []string{
	"", "/", "/bifrost", "/bifrost/", "/tools/llm-gw", "/a_b.c~d/e-f/", "/.well", "/v1.x", "/...",
	"/.", "/..", "/./", "/../", "/a/.", "/a/..", "/../a", "/a/./b", "bifrost", "//bifrost", "/a//b", "/bi frost",
	"  ", " /bifrost ", "/bifrost ", "\t/bifrost",
}

// assertBasePathPatternMatchesNormalize fails when the schema pattern and
// NormalizeBasePath disagree: a value the pattern accepts but startup rejects
// passes validation and then stops the server.
func assertBasePathPatternMatchesNormalize(t *testing.T, schemaFile string, pattern string) {
	t.Helper()
	re, err := regexp.Compile(pattern)
	require.NoError(t, err, "%s pattern must compile as RE2", schemaFile)
	for _, candidate := range basePathCandidates {
		_, err := NormalizeBasePath(candidate)
		if re.MatchString(candidate) {
			assert.NoError(t, err, "%s pattern accepts %q but NormalizeBasePath rejects it", schemaFile, candidate)
		} else {
			assert.Error(t, err, "%s pattern rejects %q but NormalizeBasePath accepts it", schemaFile, candidate)
		}
	}
}

// repoRoot returns the repository root, resolved from this file's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(filename), "..", "..", "..")
}

// TestBasePathSchemaPatternMatchesNormalize checks that server.base_path in
// config.schema.json accepts exactly the values NormalizeBasePath accepts.
func TestBasePathSchemaPatternMatchesNormalize(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "transports", "config.schema.json"))
	require.NoError(t, err)
	var schema struct {
		Properties struct {
			Server struct {
				Properties struct {
					BasePath struct {
						Pattern string `json:"pattern"`
					} `json:"base_path"`
				} `json:"properties"`
			} `json:"server"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(data, &schema))
	pattern := schema.Properties.Server.Properties.BasePath.Pattern
	require.NotEmpty(t, pattern)
	assertBasePathPatternMatchesNormalize(t, "config.schema.json", pattern)
}
