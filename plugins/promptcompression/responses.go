package promptcompression

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// compressResponses is the Responses API counterpart of compressChat and
// follows the same contract: it shortens old function_call_output text while
// keeping every item, its order, type, role and call ID, and returns the
// cache-protected prefix untouched. Only eligible outputs are edited: type
// function_call_output, outside the protected prefix, and at least
// MinAgeTurns assistant turns old. A younger output was seen by the model once
// and is the tail of the provider's cached prefix, so rewriting it would break
// the next request's cache hit. Other output types (shell, computer, custom
// tool, ...) are never edited. The input is never written: changed items get a
// copied struct and fresh Output values.
//
// Dedupe candidates are all-text function_call_output items of any age (a
// young copy is a valid pointer target), but only an eligible older copy is
// replaced, and a replaced item is not trimmed as well.
//
// This runs on every request, and for a long agent loop the usual outcome is
// "nothing to do". A first pass over the history therefore allocates nothing
// and only decides whether any edit is possible; when none is, msgs itself is
// returned. The output slice is allocated lazily at the first real change.
func compressResponses(msgs []schemas.ResponsesMessage, s settings) ([]schemas.ResponsesMessage, stats) {
	n := responsesProtectedPrefix(msgs, s.ProtectedLeading)

	// Pass 1 (back to front, no allocation): sum eligible tokens and look for
	// any trigger that would make trimming or whitespace cleanup change text.
	var st stats
	editable := false
	candidates, eligibleCandidates := 0, 0
	turns := 0 // assistant turns after i, so turns is the age of i
	afterAssistant := false
	for i := len(msgs) - 1; i >= n; i-- {
		m := &msgs[i]
		if respIsAssistantItem(m) {
			if !afterAssistant {
				turns++
			}
			afterAssistant = true
			continue
		}
		afterAssistant = false
		if !respIsToolOutput(m) {
			continue
		}
		eligible := turns >= s.MinAgeTurns
		allTxt := respIsAllText(m)
		if allTxt {
			candidates++
		}
		if !eligible {
			continue
		}
		if allTxt {
			eligibleCandidates++
		}
		st.EligibleBefore += respOutputTokens(m)
		if !editable {
			editable = respMayEdit(m, s)
		}
	}
	st.EligibleAfter = st.EligibleBefore

	// The duplicate lookup needs a map, so it only runs when a pair of
	// candidates exists and one of them is eligible; the plan it returns is
	// reused below instead of being computed twice.
	var stubs []string
	if s.Dedupe && candidates >= 2 && eligibleCandidates >= 1 {
		stubs = planResponsesDedupe(msgs, n, s.MinAgeTurns)
		if stubs != nil {
			editable = true
		}
	}
	if !editable {
		return msgs, st
	}

	// Pass 2: apply edits to eligible outputs only.
	var out []schemas.ResponsesMessage
	st.EligibleAfter = 0
	f := textTransform(s, true)
	turns = 0
	afterAssistant = false
	for i := len(msgs) - 1; i >= n; i-- {
		m := msgs[i]
		if respIsAssistantItem(&m) {
			if !afterAssistant {
				turns++
			}
			afterAssistant = true
			continue
		}
		afterAssistant = false
		if !respIsToolOutput(&m) || turns < s.MinAgeTurns {
			continue
		}
		if stubs != nil && stubs[i] != "" {
			stub := stubs[i]
			tm := *m.ResponsesToolMessage
			tm.Output = &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &stub}
			m.ResponsesToolMessage = &tm
		} else if cm, changed := mapResponsesText(m, f); changed {
			m = cm
		}
		if m.ResponsesToolMessage != msgs[i].ResponsesToolMessage {
			if out == nil {
				out = make([]schemas.ResponsesMessage, len(msgs))
				copy(out, msgs)
			}
			out[i] = m
		}
		st.EligibleAfter += respOutputTokens(&m)
	}
	if out == nil {
		return msgs, st
	}
	st.Changed = true
	return out, st
}

// respIsToolOutput reports whether m is a function_call_output, the only item
// type this plugin edits.
func respIsToolOutput(m *schemas.ResponsesMessage) bool {
	return m.Type != nil && *m.Type == schemas.ResponsesMessageTypeFunctionCallOutput
}

// respIsAssistantItem reports whether m belongs to the model's side of the
// conversation: everything that is neither user-supplied input nor a tool
// result. That covers assistant messages (also with no Type), reasoning, and
// every call item, including server-side tools (web_search_call, mcp_call, ...)
// and shell/computer/custom calls, because a turn made only of those still
// advances the conversation and the model has seen the outputs before it.
// Results are recognised by the "_output" type suffix rather than a list, so a
// result type added later is covered too. A maximal run of consecutive
// assistant items is one turn.
func respIsAssistantItem(m *schemas.ResponsesMessage) bool {
	if m.Role != nil {
		switch *m.Role {
		case schemas.ResponsesInputMessageRoleUser,
			schemas.ResponsesInputMessageRoleSystem,
			schemas.ResponsesInputMessageRoleDeveloper:
			return false
		}
	}
	return m.Type == nil || !strings.HasSuffix(string(*m.Type), "_output")
}

func respIsTextBlock(b *schemas.ResponsesMessageContentBlock) bool {
	return (b.Type == schemas.ResponsesInputMessageContentBlockTypeText ||
		b.Type == schemas.ResponsesOutputMessageContentTypeText) && b.Text != nil
}

// planResponsesDedupe returns, per item index, the stub text that replaces an
// eligible older copy of a function_call_output, or nil when no copy
// qualifies. It scans back to front, so the last copy seen for a text is its
// next newer one, whether or not that newer copy is eligible; the stub names
// that copy's call ID so an existing stub never changes when another duplicate
// arrives later (which would invalidate provider prompt caches). Originals are
// compared, never already-replaced text, and a stub is used only when shorter.
//
// The decision is fixed at eligibility time, in assistant turns exactly as in
// planChatDedupe: copy i (age a, minAge K) is stubbed only if its next copy j
// (age b) already existed when i first reached age K, which is a-b <= K. A
// duplicate that arrives later must not turn an already-sent trimmed output
// into a stub; such a copy is only ever trimmed.
func planResponsesDedupe(msgs []schemas.ResponsesMessage, start, minAge int) []string {
	var stubs []string
	type seenCopy struct {
		callID *string
		age    int
	}
	next := map[string]seenCopy{} // text -> next newer copy
	turns := 0
	afterAssistant := false
	for i := len(msgs) - 1; i >= start; i-- {
		m := &msgs[i]
		if respIsAssistantItem(m) {
			if !afterAssistant {
				turns++
			}
			afterAssistant = true
			continue
		}
		afterAssistant = false
		if !respIsToolOutput(m) {
			continue
		}
		text, ok := respOutputAllText(m)
		if !ok {
			continue
		}
		j, seen := next[text]
		next[text] = seenCopy{m.ResponsesToolMessage.CallID, turns}
		if !seen || turns < minAge || turns-j.age > minAge {
			continue
		}
		stub, ok := dedupeStub(j.callID, text)
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

// respIsAllText reports whether respOutputAllText would succeed, without
// building the text. It must mirror respOutputAllText's conditions exactly;
// change them together.
func respIsAllText(m *schemas.ResponsesMessage) bool {
	if m.ResponsesToolMessage == nil || m.ResponsesToolMessage.Output == nil {
		return false
	}
	o := m.ResponsesToolMessage.Output
	if o.ResponsesToolCallOutputStr != nil {
		return true
	}
	if len(o.ResponsesFunctionToolCallOutputBlocks) == 0 {
		return false
	}
	for i := range o.ResponsesFunctionToolCallOutputBlocks {
		if !respIsTextBlock(&o.ResponsesFunctionToolCallOutputBlocks[i]) {
			return false
		}
	}
	return true
}

// respOutputTokens sums estimateTokens over the text values of a
// function_call_output (the output string and its text blocks), the same
// definition estimateResponses uses.
func respOutputTokens(m *schemas.ResponsesMessage) int {
	if m.ResponsesToolMessage == nil || m.ResponsesToolMessage.Output == nil {
		return 0
	}
	o := m.ResponsesToolMessage.Output
	total := 0
	if o.ResponsesToolCallOutputStr != nil {
		total += estimateTokens(*o.ResponsesToolCallOutputStr)
	}
	for i := range o.ResponsesFunctionToolCallOutputBlocks {
		b := &o.ResponsesFunctionToolCallOutputBlocks[i]
		if respIsTextBlock(b) {
			total += estimateTokens(*b.Text)
		}
	}
	return total
}

// respMayEdit reports whether textTransform could change any text value of an
// eligible output: a value large enough to trim, or one whitespace
// normalisation would rewrite. It errs towards true; the real transform still
// keeps an edit only when it is strictly shorter.
func respMayEdit(m *schemas.ResponsesMessage, s settings) bool {
	if m.ResponsesToolMessage == nil || m.ResponsesToolMessage.Output == nil {
		return false
	}
	o := m.ResponsesToolMessage.Output
	may := func(text string) bool {
		return estimateTokens(text) >= s.MinTokens || (s.Whitespace && needsWhitespaceNormalization(text))
	}
	if o.ResponsesToolCallOutputStr != nil && may(*o.ResponsesToolCallOutputStr) {
		return true
	}
	for i := range o.ResponsesFunctionToolCallOutputBlocks {
		b := &o.ResponsesFunctionToolCallOutputBlocks[i]
		if respIsTextBlock(b) && may(*b.Text) {
			return true
		}
	}
	return false
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
