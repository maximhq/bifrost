package semanticcache

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestPromptsWithDifferentValuesNeverMatch reproduces a first turn served another prompt's answer: two
// prompts that differ only in their IDs and values embed above the threshold, so the second request got
// the first one's stored reply, even from another model.
func TestPromptsWithDifferentValuesNeverMatch(t *testing.T) {
	plugin := &Plugin{config: &Config{ExcludeSystemPrompt: bifrost.Ptr(true)}}
	first := "edge-aa45916a. Reply with these strings joined in order without spaces: 4296a40a9ce075c4 and f2fae8e2537d7d54"
	second := "edge-e804dd91. Reply with these strings joined in order without spaces: 7a951e2ee9c6fe26 and 61d255852e1746d3"
	swapped := "edge-aa45916a. Reply with these strings joined in order without spaces: f2fae8e2537d7d54 and 4296a40a9ce075c4"

	for name, build := range map[string]func(...string) *schemas.BifrostRequest{"responses": responsesTurn, "chat": chatTurn} {
		t.Run(name, func(t *testing.T) {
			if paramsHashFor(t, plugin, build(first)) == paramsHashFor(t, plugin, build(second)) {
				t.Fatal("prompts with different values share the semantic search filter")
			}
			if paramsHashFor(t, plugin, build(first)) == paramsHashFor(t, plugin, build(swapped)) {
				t.Fatal("the same values in another order share the semantic search filter")
			}
			// The same values asked another way may still match by meaning.
			reworded := "edge-aa45916a. Join 4296a40a9ce075c4 and f2fae8e2537d7d54 with no spaces, in that order."
			if paramsHashFor(t, plugin, build(first)) != paramsHashFor(t, plugin, build(reworded)) {
				t.Fatal("prompts with the same values must share the semantic search filter")
			}
		})
	}
}

// TestPromptsWithoutDigitsKeepTheirKeys keeps entries written before the fix reachable for prompts with no
// literal values, which are most paraphrase hits the semantic cache exists for.
func TestPromptsWithoutDigitsKeepTheirKeys(t *testing.T) {
	plugin := &Plugin{config: &Config{ExcludeSystemPrompt: bifrost.Ptr(true)}}
	metadata, err := plugin.buildRequestMetadataForCaching(plugin.createCacheState("req"), responsesTurn("Explain quantum physics"))
	if err != nil {
		t.Fatalf("buildRequestMetadataForCaching failed: %v", err)
	}
	if _, ok := metadata["message_literals"]; ok {
		t.Fatal("a prompt with no digits must not carry a message literals key")
	}
}
