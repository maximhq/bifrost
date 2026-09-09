package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These pin the two claims the doc comment on inlineMidConversationSystem makes about
// cache markers, which are not the same claim and were once conflated into "lossless":
//
//   - it never SYNTHESIZES a marker, because inventing one spends a checkpoint out of a
//     budget of four and changes the caller's billing profile behind their back; and
//   - it deliberately COLLAPSES markers, keeping only the last one in a message, since an
//     intermediate marker buys nothing that the final one does not already cover.
//
// Only the first is a losslessness guarantee. The second is a documented, intentional loss.

func plainTextBlock(text string) AnthropicContentBlock {
	return AnthropicContentBlock{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr(text)}
}

// cachedBlockTTL gives a marker a distinguishing payload, so an assertion can tell the
// caller's own marker apart from a freshly minted default one. Without that, a
// regression that synthesized a new marker in the right position would still pass.
func cachedBlockTTL(text, ttl string) AnthropicContentBlock {
	return AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeText,
		Text:         schemas.Ptr(text),
		CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: schemas.Ptr(ttl)},
	}
}

func markedBlockCount(msg *AnthropicMessage) int {
	n := 0
	for _, b := range msg.Content.ContentBlocks {
		if b.CacheControl != nil {
			n++
		}
	}
	return n
}

func TestInlineMidConversationSystem_NeverSynthesizesAMarker(t *testing.T) {
	t.Run("string form", func(t *testing.T) {
		msg := inlineMidConversationSystem(&AnthropicContent{ContentStr: schemas.Ptr("be concise")})
		require.NotNil(t, msg)
		assert.Zero(t, markedBlockCount(msg),
			"the string form carries no marker, so the inlined turn must carry none either")
	})

	t.Run("block form with no markers", func(t *testing.T) {
		msg := inlineMidConversationSystem(&AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			plainTextBlock("first"),
			plainTextBlock("second"),
		}})
		require.NotNil(t, msg)
		require.Len(t, msg.Content.ContentBlocks, 2)
		assert.Zero(t, markedBlockCount(msg),
			"an unmarked system block must not acquire a breakpoint in translation")
	})
}

func TestInlineMidConversationSystem_CollapsesMarkersOntoTheLastBlock(t *testing.T) {
	t.Run("intermediate markers are dropped", func(t *testing.T) {
		// Distinct TTLs so the surviving marker can be identified, not merely counted.
		first := cachedBlockTTL("first", "1h")
		last := cachedBlock("third")
		msg := inlineMidConversationSystem(&AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			first,
			plainTextBlock("second"),
			last,
		}})
		require.NotNil(t, msg)
		require.Len(t, msg.Content.ContentBlocks, 3)
		assert.Equal(t, 1, markedBlockCount(msg), "a message may spend at most one checkpoint here")

		surviving := msg.Content.ContentBlocks[2].CacheControl
		require.NotNil(t, surviving, "the surviving marker is the last one")
		assert.Equal(t, *last.CacheControl, *surviving,
			"the caller's LAST marker must survive verbatim, not a freshly synthesized one")
		assert.NotEqual(t, *first.CacheControl, *surviving, "the earlier marker must not be the one kept")
	})

	// The marker RELOCATES when the caller's last marked block is not the last block. The
	// final block still closes over every preceding one, so the cached prefix is unchanged
	// or longer, never shorter.
	t.Run("a trailing unmarked block inherits the marker", func(t *testing.T) {
		marked := cachedBlockTTL("first", "1h")
		msg := inlineMidConversationSystem(&AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			marked,
			plainTextBlock("second"),
		}})
		require.NotNil(t, msg)
		require.Len(t, msg.Content.ContentBlocks, 2)
		assert.Equal(t, 1, markedBlockCount(msg))

		moved := msg.Content.ContentBlocks[1].CacheControl
		require.NotNil(t, moved, "the marker moves to the final block rather than being dropped outright")
		assert.Equal(t, *marked.CacheControl, *moved,
			"the caller's marker moves verbatim, TTL included; a default marker here would "+
				"silently downgrade a 1h request to the 5m default")
	})
}
