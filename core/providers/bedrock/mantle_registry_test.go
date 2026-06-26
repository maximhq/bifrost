package bedrock

import "testing"

func TestBareMantleID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// region path prefix
		{"us-east-1/openai.gpt-oss-120b", "gpt-oss-120b"},
		// geo inference-profile prefix
		{"us.openai.gpt-oss-120b", "gpt-oss-120b"},
		{"eu.google.gemma-4-31b", "gemma-4-31b"},
		// vendor prefix only
		{"google.gemma-4-31b", "gemma-4-31b"},
		{"openai.gpt-5.5", "gpt-5.5"},
		// other mantle vendors (regression: all known prefixes must strip)
		{"deepseek.v3.1", "v3.1"},
		{"zai.glm-4.6", "glm-4.6"},
		{"qwen.qwen3-235b-a22b-2507", "qwen3-235b-a22b-2507"},
		{"qwen.qwen3-coder-480b-a35b-instruct", "qwen3-coder-480b-a35b-instruct"},
		{"us.qwen.qwen3-32b", "qwen3-32b"},
		// bare names unchanged; version dot preserved
		{"gpt-5.5", "gpt-5.5"},
		{"gpt-oss-120b", "gpt-oss-120b"},
		// case + whitespace normalization
		{"  OpenAI.GPT-OSS-120B  ", "gpt-oss-120b"},
		// empty
		{"", ""},
	}
	for _, tc := range cases {
		if got := bareMantleID(tc.in); got != tc.want {
			t.Errorf("bareMantleID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMantleRegistry(t *testing.T) {
	r := newMantleRegistry()

	// Cold start: nothing observed yet → not mantle-only for any region.
	if r.isMantleOnly("us-east-1", "some-new-mantle-model") {
		t.Fatal("expected cold-start registry to report false")
	}

	// Populate us-east-1 with a mantle-only catalog (as ListModels would).
	r.addRegion("us-east-1", []string{
		"openai.gpt-oss-120b",
		"some-new-mantle-model", // a model the name heuristic would miss
	})

	// Looked up via various prefix forms → all resolve to the bare id.
	for _, m := range []string{
		"some-new-mantle-model",
		"us.some-new-mantle-model",
		"us-east-1/some-new-mantle-model",
	} {
		if !r.isMantleOnly("us-east-1", m) {
			t.Errorf("expected %q to be mantle-only in us-east-1", m)
		}
	}

	// Region isolation: the same model in an unpopulated region is unknown.
	if r.isMantleOnly("eu-west-1", "some-new-mantle-model") {
		t.Error("expected model to be unknown in a region with no catalog")
	}

	// A model not in the catalog is not mantle-only.
	if r.isMantleOnly("us-east-1", "claude-opus-4-8") {
		t.Error("expected non-catalog model to report false")
	}

	// addRegion accumulates (union) rather than replacing the region's set.
	r.addRegion("us-east-1", []string{"google.gemma-4-31b"})
	if !r.isMantleOnly("us-east-1", "some-new-mantle-model") {
		t.Error("expected earlier entry to survive a later addRegion()")
	}
	if !r.isMantleOnly("us-east-1", "gemma-4-31b") {
		t.Error("expected newly added entry to be present")
	}
}
