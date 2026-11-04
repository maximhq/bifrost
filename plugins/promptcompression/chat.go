package promptcompression

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// compressChat shortens old tool outputs in a Chat Completions history. Every
// message, its order, role and tool-call IDs are preserved; only text values
// shrink. The cache-protected prefix is returned untouched so provider prompt
// caches still hit. The input slice and everything it points to is never
// written: changed messages get a copied struct and a fresh Content.
func compressChat(msgs []schemas.ChatMessage, s settings) []schemas.ChatMessage {
	out := make([]schemas.ChatMessage, len(msgs))
	copy(out, msgs)
	n := chatProtectedPrefix(msgs, s.ProtectedLeading)

	deduped := make([]bool, len(msgs))
	if s.Dedupe {
		dedupeChat(msgs, out, n, deduped)
	}

	// age[i] = assistant messages after i; computed back to front.
	age := make([]int, len(msgs))
	assistants := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		age[i] = assistants
		if msgs[i].Role == schemas.ChatMessageRoleAssistant {
			assistants++
		}
	}

	for i := n; i < len(msgs); i++ {
		if deduped[i] {
			continue
		}
		trim := msgs[i].Role == schemas.ChatMessageRoleTool && age[i] >= s.MinAgeTurns
		if !trim && !s.Whitespace {
			continue
		}
		f := textTransform(s, trim)
		if m, changed := mapChatText(msgs[i], f); changed {
			out[i] = m
		}
	}
	return out
}

// dedupeChat replaces older identical all-text tool outputs with a pointer to
// the most recent copy, so the model still knows where the content lives. It
// scans back to front so the first copy seen is the newest. Originals are
// compared, never already-replaced text, and a stub is used only when shorter.
func dedupeChat(msgs, out []schemas.ChatMessage, start int, deduped []bool) {
	newest := map[string]*string{} // text -> ToolCallID of newest copy
	for i := len(msgs) - 1; i >= start; i-- {
		m := &msgs[i]
		if m.Role != schemas.ChatMessageRoleTool {
			continue
		}
		text, ok := allText(m.Content)
		if !ok {
			continue
		}
		id, seen := newest[text]
		if !seen {
			var cid *string
			if m.ChatToolMessage != nil {
				cid = m.ChatToolMessage.ToolCallID
			}
			newest[text] = cid
			continue
		}
		stub, ok := dedupeStub(id, text)
		if !ok {
			continue
		}
		c := &schemas.ChatMessageContent{ContentStr: &stub}
		cp := *m
		cp.Content = c
		out[i] = cp
		deduped[i] = true
	}
}

// allText returns the joined text of a content that consists solely of text
// (ContentStr, or only text blocks). Mixed or empty content reports false.
func allText(c *schemas.ChatMessageContent) (string, bool) {
	if c == nil {
		return "", false
	}
	if c.ContentStr != nil {
		return *c.ContentStr, true
	}
	if len(c.ContentBlocks) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(c.ContentBlocks))
	for _, b := range c.ContentBlocks {
		if b.Type != schemas.ChatContentBlockTypeText || b.Text == nil {
			return "", false
		}
		parts = append(parts, *b.Text)
	}
	return strings.Join(parts, "\n"), true
}

// mapChatText applies f to every text value of m. When any value changes it
// returns a copy of m with a new Content (new block slice, new string
// pointers); otherwise it returns m and false. Non-text blocks are carried
// over as is.
func mapChatText(m schemas.ChatMessage, f func(string) string) (schemas.ChatMessage, bool) {
	if m.Content == nil {
		return m, false
	}
	c := &schemas.ChatMessageContent{}
	changed := false
	if m.Content.ContentStr != nil {
		r := f(*m.Content.ContentStr)
		changed = r != *m.Content.ContentStr
		c.ContentStr = &r
	}
	if m.Content.ContentBlocks != nil {
		c.ContentBlocks = make([]schemas.ChatContentBlock, len(m.Content.ContentBlocks))
		copy(c.ContentBlocks, m.Content.ContentBlocks)
		for i := range c.ContentBlocks {
			b := &c.ContentBlocks[i]
			if b.Type != schemas.ChatContentBlockTypeText || b.Text == nil {
				continue
			}
			r := f(*b.Text)
			if r != *b.Text {
				changed = true
			}
			b.Text = &r
		}
	}
	if !changed {
		return m, false
	}
	m.Content = c
	return m, true
}

// estimateChat sums estimateTokens over every text value in msgs. The plugin
// uses it to decide whether compression saved enough to be worth applying.
func estimateChat(msgs []schemas.ChatMessage) int {
	total := 0
	for i := range msgs {
		c := msgs[i].Content
		if c == nil {
			continue
		}
		if c.ContentStr != nil {
			total += estimateTokens(*c.ContentStr)
		}
		for _, b := range c.ContentBlocks {
			if b.Type == schemas.ChatContentBlockTypeText && b.Text != nil {
				total += estimateTokens(*b.Text)
			}
		}
	}
	return total
}
