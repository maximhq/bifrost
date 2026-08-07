package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Anthropic, Bedrock, and Vertex all reject a request carrying more than 4 blocks with
// cache_control ("A maximum of 4 blocks with cache_control may be provided. Found 5."), verified
// live against all three. These tests pin the clamp that keeps an over-marked request from
// failing outright, and — more importantly — pin WHICH markers it sacrifices.
//
// The direction is the whole design. Caching is cumulative up to each breakpoint, so a marker
// later in render order (tools -> system -> messages) anchors a strictly longer prefix than an
// earlier one. Dropping the earliest costs an intermediate checkpoint; dropping the latest would
// surrender the longest cached prefix, which is the exact failure inlineMidConversationSystem
// exists to prevent. A clamp that trimmed the wrong end would look correct (no 400s) while
// silently reintroducing the bug this package spent a fix on.

func cachedBlock(text string) AnthropicContentBlock {
	return AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeText,
		Text:         schemas.Ptr(text),
		CacheControl: &schemas.CacheControl{Type: "ephemeral"},
	}
}

// markerTexts returns the text of every block still carrying a cache_control marker, in render
// order, so a test can assert on identity rather than count alone.
func markerTexts(req *AnthropicMessageRequest) []string {
	var out []string
	for i := range req.Tools {
		if req.Tools[i].CacheControl != nil {
			out = append(out, "tool:"+req.Tools[i].Name)
		}
	}
	if req.System != nil {
		for _, b := range req.System.ContentBlocks {
			if b.CacheControl != nil && b.Text != nil {
				out = append(out, *b.Text)
			}
		}
	}
	for _, m := range req.Messages {
		for _, b := range m.Content.ContentBlocks {
			if b.CacheControl != nil && b.Text != nil {
				out = append(out, *b.Text)
			}
		}
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestClampAnthropicCacheBreakpoints_UnderCapUntouched is the guard against over-eager clamping:
// a request at or below the cap must pass through byte-identical, since removing a marker there
// would cost cache coverage for no reason.
func TestClampAnthropicCacheBreakpoints_UnderCapUntouched(t *testing.T) {
	for _, n := range []int{0, 1, AnthropicMaxCacheBreakpoints} {
		req := &AnthropicMessageRequest{}
		var want []string
		for i := 0; i < n; i++ {
			text := string(rune('a' + i))
			req.Messages = append(req.Messages, AnthropicMessage{
				Role:    AnthropicMessageRoleUser,
				Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock(text)}},
			})
			want = append(want, text)
		}
		if dropped := clampAnthropicCacheBreakpoints(req); dropped != 0 {
			t.Errorf("n=%d: dropped %d, want 0", n, dropped)
		}
		if got := markerTexts(req); !eq(got, want) {
			t.Errorf("n=%d: markers = %v, want %v", n, got, want)
		}
	}
}

// TestClampAnthropicCacheBreakpoints_DropsEarliest covers the load-bearing behavior: with six
// markers spread across all three render regions, the two earliest go and the four latest stay.
func TestClampAnthropicCacheBreakpoints_DropsEarliest(t *testing.T) {
	req := &AnthropicMessageRequest{
		Tools: []AnthropicTool{
			{Name: "alpha", CacheControl: &schemas.CacheControl{Type: "ephemeral"}},
		},
		System: &AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			cachedBlock("sys1"),
			cachedBlock("sys2"),
		}},
		Messages: []AnthropicMessage{
			{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("msg1")}}},
			{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("msg2")}}},
			{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("msg3")}}},
		},
	}

	dropped := clampAnthropicCacheBreakpoints(req)
	if want := 6 - AnthropicMaxCacheBreakpoints; dropped != want {
		t.Fatalf("dropped = %d, want %d", dropped, want)
	}
	// tool:alpha and sys1 are the two earliest in render order and must be the ones sacrificed.
	want := []string{"sys2", "msg1", "msg2", "msg3"}
	if got := markerTexts(req); !eq(got, want) {
		t.Errorf("surviving markers = %v, want %v (the LATEST four — dropping the tail would "+
			"surrender the longest cached prefix)", got, want)
	}
}

// TestClampAnthropicCacheBreakpoints_KeepsConversationAnchor is the regression guard tying this
// clamp back to the mid-conversation fix. A Claude-Code-shaped request sits exactly at the cap
// (2 system + 1 tool result + 1 inlined reminder); adding one more marker must not cost the
// reminder's anchor, because that anchor is what keeps the conversation body cacheable.
func TestClampAnthropicCacheBreakpoints_KeepsConversationAnchor(t *testing.T) {
	req := &AnthropicMessageRequest{
		System: &AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			cachedBlock("system-prompt"),
			cachedBlock("tool-definitions"),
		}},
		Messages: []AnthropicMessage{
			{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("tool-result-1")}}},
			{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("tool-result-2")}}},
			{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("<system-reminder>")}}},
		},
	}

	if dropped := clampAnthropicCacheBreakpoints(req); dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	got := markerTexts(req)
	if len(got) != AnthropicMaxCacheBreakpoints {
		t.Fatalf("markers = %v, want %d", got, AnthropicMaxCacheBreakpoints)
	}
	if got[len(got)-1] != "<system-reminder>" {
		t.Errorf("the conversation-level anchor was dropped (markers = %v); clamping must never "+
			"sacrifice the last breakpoint — that is the whole point of the inlining fix", got)
	}
}

// TestClampAnthropicCacheBreakpoints_TopLevelMarkerSurvives pins the ordering choice for the
// top-level cache_control, which auto-places on the last cacheable block and is therefore
// effectively the final breakpoint.
func TestClampAnthropicCacheBreakpoints_TopLevelMarkerSurvives(t *testing.T) {
	req := &AnthropicMessageRequest{
		CacheControl: &schemas.CacheControl{Type: "ephemeral"},
		System: &AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			cachedBlock("sys1"), cachedBlock("sys2"), cachedBlock("sys3"), cachedBlock("sys4"),
		}},
	}

	if dropped := clampAnthropicCacheBreakpoints(req); dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if req.CacheControl == nil {
		t.Error("top-level cache_control was cleared; it auto-places on the last cacheable block, "+
			"so it is the final breakpoint and should outrank earlier explicit ones")
	}
	if got, want := markerTexts(req), []string{"sys2", "sys3", "sys4"}; !eq(got, want) {
		t.Errorf("surviving block markers = %v, want %v", got, want)
	}
}

// TestClampAnthropicCacheBreakpoints_NilSafe guards the trivial inputs.
func TestClampAnthropicCacheBreakpoints_NilSafe(t *testing.T) {
	if dropped := clampAnthropicCacheBreakpoints(nil); dropped != 0 {
		t.Errorf("nil request: dropped = %d, want 0", dropped)
	}
	if dropped := clampAnthropicCacheBreakpoints(&AnthropicMessageRequest{}); dropped != 0 {
		t.Errorf("empty request: dropped = %d, want 0", dropped)
	}
}
