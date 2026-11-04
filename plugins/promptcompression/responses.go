package promptcompression

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// compressResponses is the Responses API counterpart of compressChat: it
// shortens old function_call_output text while keeping every item, its order,
// type, role and call ID, and returns the cache-protected prefix untouched.
// The input is never written: changed items get a copied struct and fresh
// Content / Output values.
func compressResponses(msgs []schemas.ResponsesMessage, s settings) []schemas.ResponsesMessage {
	out := make([]schemas.ResponsesMessage, len(msgs))
	copy(out, msgs)
	n := responsesProtectedPrefix(msgs, s.ProtectedLeading)

	deduped := make([]bool, len(msgs))
	if s.Dedupe {
		dedupeResponses(msgs, out, n, deduped)
	}

	// age[i] = assistant turns after i. A turn is a maximal run of assistant
	// items (calls, reasoning, assistant messages), so parallel calls count once.
	age := make([]int, len(msgs))
	turns := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		age[i] = turns
		if respIsAssistantItem(&msgs[i]) && (i == len(msgs)-1 || !respIsAssistantItem(&msgs[i+1])) {
			turns++
		}
	}

	for i := n; i < len(msgs); i++ {
		if deduped[i] {
			continue
		}
		// Only trim-eligible old tool output is rewritten (trim and whitespace
		// normalisation); everything else is sent exactly as received.
		if !respIsToolOutput(&msgs[i]) || age[i] < s.MinAgeTurns {
			continue
		}
		if m, changed := mapResponsesText(msgs[i], textTransform(s, true)); changed {
			out[i] = m
		}
	}
	return out
}

func respIsToolOutput(m *schemas.ResponsesMessage) bool {
	return m.Type != nil && *m.Type == schemas.ResponsesMessageTypeFunctionCallOutput
}

func respIsAssistantItem(m *schemas.ResponsesMessage) bool {
	if m.Type != nil {
		switch *m.Type {
		case schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesMessageTypeReasoning:
			return true
		case schemas.ResponsesMessageTypeMessage:
			// role decides below
		default:
			return false
		}
	}
	return m.Role != nil && *m.Role == schemas.ResponsesInputMessageRoleAssistant
}

func respIsTextBlock(b *schemas.ResponsesMessageContentBlock) bool {
	return (b.Type == schemas.ResponsesInputMessageContentBlockTypeText ||
		b.Type == schemas.ResponsesOutputMessageContentTypeText) && b.Text != nil
}

// dedupeResponses replaces each older identical all-text function_call_output
// item with a pointer to the call ID of the immediately next newer copy, so an
// existing stub never changes when another duplicate arrives later. Scanning
// back to front makes the last copy seen for a text the next newer one;
// originals are compared and a stub is used only when strictly shorter.
func dedupeResponses(msgs, out []schemas.ResponsesMessage, start int, deduped []bool) {
	next := map[string]*string{} // text -> CallID of the next newer copy
	for i := len(msgs) - 1; i >= start; i-- {
		m := &msgs[i]
		if !respIsToolOutput(m) {
			continue
		}
		text, ok := respOutputAllText(m)
		if !ok {
			continue
		}
		id, seen := next[text]
		next[text] = m.ResponsesToolMessage.CallID
		if !seen {
			continue
		}
		stub, ok := dedupeStub(id, text)
		if !ok {
			continue
		}
		tm := *m.ResponsesToolMessage
		tm.Output = &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &stub}
		cp := *m
		cp.ResponsesToolMessage = &tm
		out[i] = cp
		deduped[i] = true
	}
}

// respOutputAllText returns the text of a tool output made solely of text
// (the string, or only text blocks joined by newlines). Anything else,
// including a missing tool message or output, reports false.
func respOutputAllText(m *schemas.ResponsesMessage) (string, bool) {
	if m.ResponsesToolMessage == nil || m.ResponsesToolMessage.Output == nil {
		return "", false
	}
	o := m.ResponsesToolMessage.Output
	if o.ResponsesToolCallOutputStr != nil {
		return *o.ResponsesToolCallOutputStr, true
	}
	if len(o.ResponsesFunctionToolCallOutputBlocks) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(o.ResponsesFunctionToolCallOutputBlocks))
	for i := range o.ResponsesFunctionToolCallOutputBlocks {
		b := &o.ResponsesFunctionToolCallOutputBlocks[i]
		if !respIsTextBlock(b) {
			return "", false
		}
		parts = append(parts, *b.Text)
	}
	return strings.Join(parts, "\n"), true
}

// mapBlocks applies f to the text blocks of src into a new slice; other blocks
// are carried over as is. It reports whether any text changed.
func mapBlocks(src []schemas.ResponsesMessageContentBlock, f func(string) string) ([]schemas.ResponsesMessageContentBlock, bool) {
	dst := make([]schemas.ResponsesMessageContentBlock, len(src))
	copy(dst, src)
	changed := false
	for i := range dst {
		if !respIsTextBlock(&dst[i]) {
			continue
		}
		r := f(*dst[i].Text)
		changed = changed || r != *dst[i].Text
		dst[i].Text = &r
	}
	return dst, changed
}

// mapResponsesText applies f to every text value of m: the Output of a
// function_call_output, or the Content of any other item. When anything
// changes it returns a copy with fresh Output/Content (new block slice, new
// string pointers); otherwise m and false. Other fields (arguments, reasoning,
// computer/shell outputs) are never touched.
func mapResponsesText(m schemas.ResponsesMessage, f func(string) string) (schemas.ResponsesMessage, bool) {
	if respIsToolOutput(&m) {
		if m.ResponsesToolMessage == nil || m.ResponsesToolMessage.Output == nil {
			return m, false
		}
		o := *m.ResponsesToolMessage.Output
		changed := false
		if o.ResponsesToolCallOutputStr != nil {
			r := f(*o.ResponsesToolCallOutputStr)
			changed = r != *o.ResponsesToolCallOutputStr
			o.ResponsesToolCallOutputStr = &r
		}
		if o.ResponsesFunctionToolCallOutputBlocks != nil {
			var c bool
			o.ResponsesFunctionToolCallOutputBlocks, c = mapBlocks(o.ResponsesFunctionToolCallOutputBlocks, f)
			changed = changed || c
		}
		if !changed {
			return m, false
		}
		tm := *m.ResponsesToolMessage
		tm.Output = &o
		m.ResponsesToolMessage = &tm
		return m, true
	}
	if m.Content == nil {
		return m, false
	}
	c := &schemas.ResponsesMessageContent{}
	changed := false
	if m.Content.ContentStr != nil {
		r := f(*m.Content.ContentStr)
		changed = r != *m.Content.ContentStr
		c.ContentStr = &r
	}
	if m.Content.ContentBlocks != nil {
		var bc bool
		c.ContentBlocks, bc = mapBlocks(m.Content.ContentBlocks, f)
		changed = changed || bc
	}
	if !changed {
		return m, false
	}
	m.Content = c
	return m, true
}

// estimateResponses sums estimateTokens over every text value in msgs (item
// content and function_call_output text), the size signal the plugin uses to
// decide whether compression saved enough to apply.
func estimateResponses(msgs []schemas.ResponsesMessage) int {
	total := 0
	add := func(blocks []schemas.ResponsesMessageContentBlock) {
		for i := range blocks {
			if respIsTextBlock(&blocks[i]) {
				total += estimateTokens(*blocks[i].Text)
			}
		}
	}
	for i := range msgs {
		m := &msgs[i]
		if respIsToolOutput(m) {
			if m.ResponsesToolMessage != nil && m.ResponsesToolMessage.Output != nil {
				o := m.ResponsesToolMessage.Output
				if o.ResponsesToolCallOutputStr != nil {
					total += estimateTokens(*o.ResponsesToolCallOutputStr)
				}
				add(o.ResponsesFunctionToolCallOutputBlocks)
			}
			continue
		}
		if m.Content == nil {
			continue
		}
		if m.Content.ContentStr != nil {
			total += estimateTokens(*m.Content.ContentStr)
		}
		add(m.Content.ContentBlocks)
	}
	return total
}
