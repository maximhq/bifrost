package promptcompression

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// compressChat shortens old tool outputs in a Chat Completions history. Every
// message, its order, role and tool-call IDs are preserved; only text values
// shrink. Only eligible outputs are edited: role tool, outside the
// cache-protected prefix, and at least MinAgeTurns assistant turns old. A
// younger output was seen by the model once and is the tail of the provider's
// cached prefix, so rewriting it would break the next request's cache hit.
// The input slice and everything it points to is never written: changed
// messages get a copied struct and a fresh Content.
//
// Dedupe candidates are all-text tool outputs of any age (a young copy is a
// valid pointer target), but only an eligible older copy is replaced, and a
// replaced message is not trimmed as well.
//
// This runs on every request, and for a long agent loop the usual outcome is
// "nothing to do". A first pass over the history therefore allocates nothing
// and only decides whether any edit is possible; when none is, msgs itself is
// returned. The output slice is allocated lazily at the first real change.
func compressChat(msgs []schemas.ChatMessage, s settings) ([]schemas.ChatMessage, stats) {
	n := chatProtectedPrefix(msgs, s.ProtectedLeading)

	// Pass 1 (back to front, no allocation): sum eligible tokens and look for
	// any trigger that would make trimming or whitespace cleanup change text.
	var st stats
	editable := false
	candidates, eligibleCandidates := 0, 0
	assistants := 0 // assistant messages after i, so assistants is the age of i
	for i := len(msgs) - 1; i >= n; i-- {
		m := &msgs[i]
		if m.Role == schemas.ChatMessageRoleAssistant {
			assistants++
			continue
		}
		if m.Role != schemas.ChatMessageRoleTool {
			continue
		}
		eligible := assistants >= s.MinAgeTurns
		allTxt := isAllText(m.Content)
		if allTxt {
			candidates++
		}
		if !eligible {
			continue
		}
		if allTxt {
			eligibleCandidates++
		}
		st.EligibleBefore += chatMsgTokens(m)
		if !editable {
			editable = chatMayEdit(m, s)
		}
	}
	st.EligibleAfter = st.EligibleBefore

	// The duplicate lookup needs a map, so it only runs when a pair of
	// candidates exists and one of them is eligible; the plan it returns is
	// reused below instead of being computed twice.
	var stubs []string
	if s.Dedupe && candidates >= 2 && eligibleCandidates >= 1 {
		stubs = planChatDedupe(msgs, n, s.MinAgeTurns)
		if stubs != nil {
			editable = true
		}
	}
	if !editable {
		return msgs, st
	}

	// Pass 2: apply edits to eligible outputs only.
	var out []schemas.ChatMessage
	st.EligibleAfter = 0
	f := textTransform(s, true)
	assistants = 0
	for i := len(msgs) - 1; i >= n; i-- {
		m := msgs[i]
		if m.Role == schemas.ChatMessageRoleAssistant {
			assistants++
			continue
		}
		if m.Role != schemas.ChatMessageRoleTool || assistants < s.MinAgeTurns {
			continue
		}
		if stubs != nil && stubs[i] != "" {
			stub := stubs[i]
			m.Content = &schemas.ChatMessageContent{ContentStr: &stub}
		} else if cm, changed := mapChatText(m, f); changed {
			m = cm
		}
		if m.Content != msgs[i].Content {
			if out == nil {
				out = make([]schemas.ChatMessage, len(msgs))
				copy(out, msgs)
			}
			out[i] = m
		}
		st.EligibleAfter += chatMsgTokens(&m)
	}
	if out == nil {
		return msgs, st
	}
	st.Changed = true
	return out, st
}

// chatMayEdit reports whether textTransform could change any text value of an
// eligible output: a value large enough to trim, or one whitespace
// normalisation would rewrite. It errs towards true; the real transform still
// keeps an edit only when it is strictly shorter.
func chatMayEdit(m *schemas.ChatMessage, s settings) bool {
	c := m.Content
	if c == nil {
		return false
	}
	may := func(text string) bool {
		return estimateTokens(text) >= s.MinTokens || (s.Whitespace && needsWhitespaceNormalization(text))
	}
	if c.ContentStr != nil && may(*c.ContentStr) {
		return true
	}
	for i := range c.ContentBlocks {
		b := &c.ContentBlocks[i]
		if b.Type == schemas.ChatContentBlockTypeText && b.Text != nil && may(*b.Text) {
			return true
		}
	}
	return false
}

// planChatDedupe returns, per message index, the stub text that replaces an
// eligible older copy of a tool output, or nil when no copy qualifies. It
// scans back to front, so the last copy seen for a text is its next newer
// one, whether or not that newer copy is eligible; the stub names that copy so
// an existing stub never changes when another duplicate arrives later (which
// would invalidate provider prompt caches). Originals are compared, never
// already-replaced text, and a stub is used only when shorter.
//
// The decision is fixed at eligibility time: copy i (age a, minAge K) is
// stubbed only if its next copy j (age b) already existed in the request where
// i first reached age K, i.e. j lies before the (K+1)-th assistant message
// after i, which is a-b <= K. A duplicate that arrives later must not turn an
// already-sent trimmed output into a stub, since that would rewrite a cached
// prefix; such a copy is only ever trimmed.
func planChatDedupe(msgs []schemas.ChatMessage, start, minAge int) []string {
	var stubs []string
	type copy struct{ idx, age int }
	next := map[string]copy{} // text -> next newer copy
	assistants := 0
	for i := len(msgs) - 1; i >= start; i-- {
		m := &msgs[i]
		if m.Role == schemas.ChatMessageRoleAssistant {
			assistants++
			continue
		}
		if m.Role != schemas.ChatMessageRoleTool {
			continue
		}
		text, ok := allText(m.Content)
		if !ok {
			continue
		}
		j, seen := next[text]
		next[text] = copy{i, assistants}
		if !seen || assistants < minAge || assistants-j.age > minAge {
			continue
		}
		var id *string
		if msgs[j.idx].ChatToolMessage != nil {
			id = msgs[j.idx].ChatToolMessage.ToolCallID
		}
		stub, ok := dedupeStub(id, text)
		if !ok {
			continue
		}
		if stubs == nil {
			stubs = make([]string, len(msgs))
		}
		stubs[i] = stub
	}
	return stubs
}

// isAllText reports whether allText would succeed, without building the text.
// It must mirror allText's conditions exactly; change them together.
func isAllText(c *schemas.ChatMessageContent) bool {
	if c == nil {
		return false
	}
	if c.ContentStr != nil {
		return true
	}
	if len(c.ContentBlocks) == 0 {
		return false
	}
	for i := range c.ContentBlocks {
		if c.ContentBlocks[i].Type != schemas.ChatContentBlockTypeText || c.ContentBlocks[i].Text == nil {
			return false
		}
	}
	return true
}

// chatMsgTokens sums estimateTokens over the text values of one message.
func chatMsgTokens(m *schemas.ChatMessage) int {
	c := m.Content
	if c == nil {
		return 0
	}
	total := 0
	if c.ContentStr != nil {
		total += estimateTokens(*c.ContentStr)
	}
	for i := range c.ContentBlocks {
		b := &c.ContentBlocks[i]
		if b.Type == schemas.ChatContentBlockTypeText && b.Text != nil {
			total += estimateTokens(*b.Text)
		}
	}
	return total
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
		total += chatMsgTokens(&msgs[i])
	}
	return total
}
