package bedrock

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// DeepSeek V3.2 writes tool calls as DSML markup. Bedrock (Converse and Mantle) parses
// them into structured tool calls but leaves the opening tag at the end of the text
// before them; these helpers remove that tag and nothing else.
const (
	dsmlMarker  = "<｜DSML｜"
	dsmlLeak    = dsmlMarker + "function_calls"
	dsmlMaxHeld = len(dsmlLeak) + 16
)

// IsDSMLLeakModel reports whether model is DeepSeek V3.2, the only family that leaks the tag.
func IsDSMLLeakModel(ctx *schemas.BifrostContext, model string) bool {
	return strings.Contains(schemas.ResolveCanonicalModel(ctx, model), "deepseek.v3.2")
}

// StripDSMLLeak removes a trailing leaked DSML opening tag from text.
func StripDSMLLeak(text string) string {
	i := strings.LastIndex(text, dsmlMarker)
	if i < 0 || !isDSMLLeakTail(text[i:]) {
		return text
	}
	if strings.TrimSpace(text[:i]) == "" {
		return ""
	}
	return text[:i]
}

// isDSMLLeakTail reports whether s (starting at the marker) is the leaked tag, or a prefix of it, plus whitespace.
func isDSMLLeakTail(s string) bool {
	return len(s) <= dsmlMaxHeld && strings.HasPrefix(dsmlLeak, strings.TrimRight(s, " \t\r\n"))
}

// StripDSMLLeakChat removes the leaked tag from the text of choices that carry tool calls.
func StripDSMLLeakChat(resp *schemas.BifrostChatResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Choices {
		c := resp.Choices[i].ChatNonStreamResponseChoice
		if c == nil || c.Message == nil || c.Message.Content == nil ||
			c.Message.ChatAssistantMessage == nil || len(c.Message.ChatAssistantMessage.ToolCalls) == 0 {
			continue
		}
		if s := c.Message.Content.ContentStr; s != nil {
			if out := StripDSMLLeak(*s); out != *s {
				c.Message.Content.ContentStr = &out
			}
		}
		for j := range c.Message.Content.ContentBlocks {
			if t := c.Message.Content.ContentBlocks[j].Text; t != nil {
				if out := StripDSMLLeak(*t); out != *t {
					c.Message.Content.ContentBlocks[j].Text = &out
				}
			}
		}
	}
}

// stripDSMLLeakConverse removes the leaked tag from a text block directly followed by a toolUse block.
func stripDSMLLeakConverse(resp *BedrockConverseResponse) {
	if resp == nil || resp.Output == nil || resp.Output.Message == nil {
		return
	}
	blocks := resp.Output.Message.Content
	content := blocks[:0]
	for i, block := range blocks {
		if block.Text != nil && i+1 < len(blocks) && blocks[i+1].ToolUse != nil {
			out := StripDSMLLeak(*block.Text)
			if out == "" && *block.Text != "" {
				continue // the block held nothing but the tag
			}
			if out != *block.Text {
				block.Text = &out
			}
		}
		content = append(content, block)
	}
	resp.Output.Message.Content = content
}

// DSMLStreamFilter holds back a leaked tag across text deltas of one stream; nil disables it.
type DSMLStreamFilter struct {
	held    string
	holding bool
	index   int // Converse content block the held text came from
}

// NewDSMLStreamFilter returns a filter for model, or nil when the model cannot leak the tag.
func NewDSMLStreamFilter(ctx *schemas.BifrostContext, model string) *DSMLStreamFilter {
	if !IsDSMLLeakModel(ctx, model) {
		return nil
	}
	return &DSMLStreamFilter{}
}

// Text filters one text delta and returns the text to emit now.
//
// Text before the marker goes out at once. From the marker on, text is held while it is
// still the leaked tag (at most dsmlMaxHeld bytes) and released whole as soon as it is not.
// A trailing partial marker is held too: "<" and "｜DSML｜" are separate tokens, so a chunk
// boundary can fall between them.
func (f *DSMLStreamFilter) Text(delta string) string {
	if f == nil {
		return delta
	}
	if !f.holding {
		return f.scan(delta)
	}
	f.held += delta
	if isDSMLMarkerPrefix(f.held) || isDSMLLeakTail(f.held) {
		return ""
	}
	held := f.held
	f.held, f.holding = "", false
	return f.scan(held)
}

// scan emits s, holding back a leaked tag or a trailing partial marker.
func (f *DSMLStreamFilter) scan(s string) string {
	if strings.IndexByte(s, '<') < 0 {
		return s // no marker and no partial marker without a '<'
	}
	if i := strings.LastIndex(s, dsmlMarker); i >= 0 {
		if !isDSMLLeakTail(s[i:]) {
			return s
		}
		f.held, f.holding = s[i:], true
		return s[:i]
	}
	// The marker has a single '<', so a partial marker can only start at the last one.
	if i := strings.LastIndexByte(s, '<'); i >= 0 && isDSMLMarkerPrefix(s[i:]) {
		f.held, f.holding = s[i:], true
		return s[:i]
	}
	return s
}

// isDSMLMarkerPrefix reports whether s is a strict prefix of the marker.
func isDSMLMarkerPrefix(s string) bool {
	return len(s) < len(dsmlMarker) && strings.HasPrefix(dsmlMarker, s)
}

// Flush returns all held text, for a block that ended without a tool call.
func (f *DSMLStreamFilter) Flush() string {
	if f == nil {
		return ""
	}
	out := f.held
	f.held, f.holding = "", false
	return out
}

// End drops a held tag at a block or message boundary and returns held text that was not one.
func (f *DSMLStreamFilter) End() string {
	if f == nil {
		return ""
	}
	var out string
	if isDSMLMarkerPrefix(f.held) {
		out = f.held // a partial marker the tag never followed
	}
	f.held, f.holding = "", false
	return out
}

// FilterChatChunk filters an OpenAI-shaped chat stream chunk in place.
func (f *DSMLStreamFilter) FilterChatChunk(resp *schemas.BifrostChatResponse) {
	if f == nil || resp == nil {
		return
	}
	for i := range resp.Choices {
		choice := &resp.Choices[i]
		d := choice.ChatStreamResponseChoice
		if d == nil || d.Delta == nil {
			if choice.FinishReason == nil {
				continue
			}
			// A closing chunk may omit the delta; settle held text the same way and add one to carry it.
			var rel string
			if *choice.FinishReason == string(schemas.BifrostFinishReasonToolCalls) {
				rel = f.End()
			} else {
				rel = f.Flush()
			}
			if rel == "" || choice.ChatNonStreamResponseChoice != nil {
				continue // nothing to carry, or a message-shaped frame a delta would displace
			}
			if d == nil {
				d = &schemas.ChatStreamResponseChoice{}
				choice.ChatStreamResponseChoice = d
			}
			d.Delta = &schemas.ChatStreamResponseChoiceDelta{Content: &rel}
			continue
		}
		in, out := "", ""
		if d.Delta.Content != nil {
			in = *d.Delta.Content
			out = f.Text(in)
		}
		switch {
		case len(d.Delta.ToolCalls) > 0 || (choice.FinishReason != nil && *choice.FinishReason == string(schemas.BifrostFinishReasonToolCalls)):
			out += f.End() // a parsed tool call confirms the leak; a held partial marker still goes out
		case choice.FinishReason != nil:
			out += f.Flush() // no tool call: the held text was the answer
		}
		if out == in {
			continue
		}
		if out == "" {
			d.Delta.Content = nil
		} else {
			d.Delta.Content = schemas.Ptr(out)
		}
	}
}

// filterConverseEvent filters a Converse stream event in place and returns held text to
// send as its own text delta before ev, or nil.
//
// Held text waits across contentBlockStop, which neither converter acts on; the text block
// stays open until the next start or messageStop. A toolUse start confirms the leak and
// drops it; any other event means it was the answer, so it goes out ahead of that event.
func (f *DSMLStreamFilter) filterConverseEvent(ev *BedrockStreamEvent) *BedrockStreamEvent {
	if f == nil {
		return nil
	}
	if ev.Delta != nil && ev.Delta.Text != nil {
		if out := f.Text(*ev.Delta.Text); out != *ev.Delta.Text {
			ev.Delta.Text = schemas.Ptr(out)
		}
		if f.holding && ev.ContentBlockIndex != nil {
			f.index = *ev.ContentBlockIndex
		}
		return nil
	}
	if !f.holding || (ev.ContentBlockIndex != nil && ev.Delta == nil && ev.Start == nil && ev.StopReason == nil) {
		return nil // nothing held, or contentBlockStop: keep holding
	}
	var release string
	if ev.Start != nil && ev.Start.ToolUse != nil {
		release = f.End()
	} else {
		release = f.Flush()
	}
	if release == "" {
		return nil
	}
	return &BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(f.index), Delta: &BedrockContentBlockDelta{Text: &release}}
}
