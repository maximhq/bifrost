package schemas

import (
	"slices"
	"strings"
	"unicode"
)

type anthropicBillingHeader struct {
	system *ResponsesMessageContent
	text   *string
	blocks []billingHeaderBlock
}

type billingHeaderBlock struct {
	index int
	block ResponsesMessageContentBlock
}

// ExtractAnthropicBillingHeader normalizes a freshly converted Messages request.
// Call only before sharing Input with plugins or attempts: compacting the owned
// system block slice here avoids cloning N messages on every non-Anthropic attempt.
// Only removed metadata is retained (O(H) space); prompt strings are never copied.
// RawRequestBody is untouched, so native passthrough still has the original body.
func (r *BifrostResponsesRequest) ExtractAnthropicBillingHeader() {
	if r == nil || r.anthropicBillingHeader != nil || len(r.Input) == 0 {
		return
	}
	message := &r.Input[0] // Messages ingress puts top-level system content first.
	content := message.Content
	if message.Role == nil || *message.Role != ResponsesInputMessageRoleSystem || content == nil {
		return
	}
	if content.ContentStr != nil {
		if isStandaloneBillingHeader(*content.ContentStr) {
			r.anthropicBillingHeader = &anthropicBillingHeader{text: content.ContentStr}
			r.Input = r.Input[1:]
		}
		return
	}
	kept := content.ContentBlocks[:0]
	for i, block := range content.ContentBlocks {
		if block.Type == ResponsesInputMessageContentBlockTypeText && block.Text != nil && isStandaloneBillingHeader(*block.Text) {
			if r.anthropicBillingHeader == nil {
				r.anthropicBillingHeader = &anthropicBillingHeader{system: content}
			}
			r.anthropicBillingHeader.blocks = append(r.anthropicBillingHeader.blocks, billingHeaderBlock{i, block})
		} else {
			kept = append(kept, block)
		}
	}
	if r.anthropicBillingHeader != nil {
		clear(content.ContentBlocks[len(kept):])
		content.ContentBlocks = kept
		if len(kept) == 0 {
			r.anthropicBillingHeader.system = nil
			r.Input = r.Input[1:]
		}
	}
}

// WithAnthropicBillingHeader restores attribution on an attempt-local copy.
// Only normalized Anthropic attempts pay O(N+B+H) space for message/block structs;
// non-Anthropic attempts reuse the header-free request. The shared input and text
// bytes remain unchanged, including across retries and cross-family fallbacks.
func (r *BifrostResponsesRequest) WithAnthropicBillingHeader() *BifrostResponsesRequest {
	if r == nil || r.anthropicBillingHeader == nil {
		return r
	}
	header := r.anthropicBillingHeader
	index := -1
	var blocks []ResponsesMessageContentBlock
	if header.system != nil {
		index = slices.IndexFunc(r.Input, func(m ResponsesMessage) bool { return m.Content == header.system })
		if index < 0 {
			return r // A plugin replaced the system message; do not resurrect it.
		}
		blocks = r.Input[index].Content.ContentBlocks
	}
	content := &ResponsesMessageContent{ContentStr: header.text}
	if header.text == nil {
		content.ContentBlocks = make([]ResponsesMessageContentBlock, 0, len(blocks)+len(header.blocks))
		start := 0
		for i, saved := range header.blocks {
			end := min(saved.index-i, len(blocks))
			content.ContentBlocks = append(content.ContentBlocks, blocks[start:end]...)
			content.ContentBlocks = append(content.ContentBlocks, saved.block)
			start = end
		}
		content.ContentBlocks = append(content.ContentBlocks, blocks[start:]...)
	}
	copy := *r
	copy.anthropicBillingHeader = nil // Restoring an already prepared copy is a no-op.
	if index >= 0 {
		copy.Input = slices.Clone(r.Input)
		copy.Input[index].Content = content
	} else {
		copy.Input = make([]ResponsesMessage, 0, len(r.Input)+1)
		copy.Input = append(copy.Input, ResponsesMessage{Role: Ptr(ResponsesInputMessageRoleSystem), Content: content})
		copy.Input = append(copy.Input, r.Input...)
	}
	return &copy
}

// Match an entire metadata line so mixed instruction blocks are left intact.
func isStandaloneBillingHeader(text string) bool {
	metadata, ok := strings.CutPrefix(strings.TrimSpace(text), "x-anthropic-billing-header:")
	if !ok || strings.ContainsAny(metadata, "\r\n") {
		return false
	}
	found := false
	for field := range strings.SplitSeq(metadata, ";") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" || value == "" || strings.IndexFunc(field, unicode.IsSpace) >= 0 {
			return false
		}
		found = true
	}
	return found
}
