package datasheet

import (
	"context"
	"net/url"
	"path/filepath"
	"slices"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestFilePathFromURL verifies that filePathFromURL resolves every shape of
// file:// URL to a usable filesystem path. url.Parse scatters a relative path
// across Opaque/Host/Path depending on its form, so the resolver must reassemble
// it. This is what lets the example configs reference datasheets with relative
// paths (e.g. file://../../examples/configs/.../pricing.json) the same way the
// sqlite config store references a relative "path".
func TestFilePathFromURL(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"file:///opt/bifrost/pricing.json", "/opt/bifrost/pricing.json"},
		{"file://localhost/opt/bifrost/pricing.json", "/opt/bifrost/pricing.json"},
		{"file://./pricing.json", "./pricing.json"},
		{"file:./pricing.json", "./pricing.json"},
		{"file://../../examples/configs/withlocalpricingfiles/pricing.json", "../../examples/configs/withlocalpricingfiles/pricing.json"},
		{"file:../../examples/configs/withlocalpricingfiles/pricing.json", "../../examples/configs/withlocalpricingfiles/pricing.json"},
	}
	for _, tc := range cases {
		parsed, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", tc.raw, err)
		}
		if got := filePathFromURL(parsed); got != tc.want {
			t.Errorf("filePathFromURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// fileURL builds an absolute file:// URL for a path relative to the test working
// directory (the package dir). Absolute and relative file:// URLs both load (see
// TestFilePathFromURL); this helper uses absolute paths so the assertions do not
// depend on the test's working directory.
func fileURL(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("failed to resolve %s: %v", rel, err)
	}
	return "file://" + abs
}

// TestLoadFromLocalFiles verifies that both the pricing datasheet and the model
// parameters datasheet load from local file:// URLs without any network access.
// This is the air-gapped / behind-a-proxy path from issue #4305: when both URLs
// point at local files, the store must read them off disk and never attempt to
// resolve getbifrost.ai.
//
// The fixtures in testdata/ are real entries extracted from the public
// datasheets at https://getbifrost.ai/datasheet and
// https://getbifrost.ai/datasheet/model-parameters.
func TestLoadFromLocalFiles(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)
	ctx := context.Background()

	store := New(nil, logger, Config{
		URL:                fileURL(t, "testdata/pricing.json"),
		ModelParametersURL: fileURL(t, "testdata/model-parameters.json"),
	})

	// --- Pricing from local file ---
	if err := store.LoadFromURLIntoMemory(ctx); err != nil {
		t.Fatalf("LoadFromURLIntoMemory (pricing) from local file failed: %v", err)
	}

	pricing := store.GetPricingEntryForModel("gpt-4o", schemas.OpenAI)
	if pricing == nil {
		t.Fatal("expected pricing entry for gpt-4o loaded from local file, got nil")
	}
	if pricing.InputCostPerToken == nil || *pricing.InputCostPerToken != 2.5e-06 {
		t.Fatalf("expected gpt-4o input_cost_per_token=2.5e-06, got %#v", pricing.InputCostPerToken)
	}
	if store.GetPricingEntryForModel("text-embedding-3-small", schemas.OpenAI) == nil {
		t.Fatal("expected pricing entry for text-embedding-3-small loaded from local file, got nil")
	}

	// --- Model parameters from local file ---
	if err := store.LoadModelParamsFromURLIntoMemory(ctx); err != nil {
		t.Fatalf("LoadModelParamsFromURLIntoMemory from local file failed: %v", err)
	}

	if !store.IsRequestTypeSupported("gpt-4o", schemas.ChatCompletionRequest) {
		t.Fatal("expected gpt-4o to support chat_completion after loading model parameters from local file")
	}

	params := store.GetSupportedParameters("gpt-4o")
	if len(params) == 0 {
		t.Fatal("expected non-empty supported parameters for gpt-4o loaded from local file")
	}
	for _, want := range []string{"temperature", "tools"} {
		if !slices.Contains(params, want) {
			t.Fatalf("expected supported parameters for gpt-4o to contain %q, got %v", want, params)
		}
	}
}

// TestLoadFromLocalFiles_NeverResolvesHostname guards the regression in #4305:
// even when the default getbifrost.ai URLs would be unreachable, a file:// URL
// must be read straight off disk. We point at the local fixtures and assert the
// load succeeds, which is only possible if the file scheme short-circuits the
// external-URL validation (and its hostname lookup) entirely.
func TestLoadFromLocalFiles_NeverResolvesHostname(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)
	ctx := context.Background()

	store := New(nil, logger, Config{
		URL:                fileURL(t, "testdata/pricing.json"),
		ModelParametersURL: fileURL(t, "testdata/model-parameters.json"),
	})

	if got := store.URL(); got != fileURL(t, "testdata/pricing.json") {
		t.Fatalf("pricing URL not retained as file scheme: %q", got)
	}
	if got := store.ModelParametersURL(); got != fileURL(t, "testdata/model-parameters.json") {
		t.Fatalf("model parameters URL not retained as file scheme: %q", got)
	}

	if err := store.LoadFromURLIntoMemory(ctx); err != nil {
		t.Fatalf("pricing load from file scheme must not require hostname resolution: %v", err)
	}
	if err := store.LoadModelParamsFromURLIntoMemory(ctx); err != nil {
		t.Fatalf("model parameters load from file scheme must not require hostname resolution: %v", err)
	}
}
