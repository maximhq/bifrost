package schemas

import (
	"encoding/base64"
	"encoding/json"
	neturl "net/url"
	"strings"
)

// InlineAttachmentCap bounds an inline attachment payload carried on a span.
// Larger payloads are summarized to media type and size only.
const InlineAttachmentCap = 256 << 10

// RawPayloadCap bounds a raw body on a span. Over-cap bodies are dropped, not truncated.
const RawPayloadCap = 256 << 10

// EncodeRawPayload JSON-encodes a raw body, returning "" if absent or over RawPayloadCap.
func EncodeRawPayload(v any) string {
	if v == nil {
		return ""
	}
	// Byte slices are already-encoded bodies; marshalling would base64 them.
	switch b := v.(type) {
	case string:
		if len(b) > RawPayloadCap {
			return ""
		}
		return b
	case []byte:
		if len(b) > RawPayloadCap {
			return ""
		}
		return string(b)
	case json.RawMessage:
		if len(b) > RawPayloadCap {
			return ""
		}
		return string(b)
	}
	data, err := MarshalString(v)
	if err != nil || len(data) > RawPayloadCap {
		return ""
	}
	return data
}

// AttachmentOptions controls how non-text content blocks are summarized.
// The zero value carries references only, which is the default.
type AttachmentOptions struct {
	// Inline copies base64 payloads onto the span when they fit under Cap.
	// Off by default: spans outlive the request across an async flush.
	Inline bool
	// Cap overrides InlineAttachmentCap when non-zero.
	Cap int
}

func (o AttachmentOptions) cap() int {
	if o.Cap > 0 {
		return o.Cap
	}
	return InlineAttachmentCap
}

// ExtractChatMessages summarizes chat messages for a span.
func ExtractChatMessages(messages []ChatMessage, opts AttachmentOptions) []MessageSummary {
	if len(messages) == 0 {
		return nil
	}
	result := make([]MessageSummary, 0, len(messages))
	for i := range messages {
		result = append(result, ExtractMessageSummary(&messages[i], opts))
	}
	return result
}

// ExtractChatResponseMessages summarizes the output messages of a chat response.
func ExtractChatResponseMessages(resp *BifrostChatResponse, opts AttachmentOptions) []MessageSummary {
	if resp == nil {
		return nil
	}
	result := make([]MessageSummary, 0, len(resp.Choices))
	for _, choice := range resp.Choices {
		if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
			continue
		}
		result = append(result, ExtractMessageSummary(choice.ChatNonStreamResponseChoice.Message, opts))
	}
	return result
}

// ExtractMessageSummary summarizes one chat message, including its attachments.
func ExtractMessageSummary(msg *ChatMessage, opts AttachmentOptions) MessageSummary {
	if msg == nil {
		return MessageSummary{}
	}

	summary := MessageSummary{
		Role:        string(ChatMessageRoleAssistant),
		Content:     ExtractChatContentText(msg.Content),
		Attachments: extractChatAttachments(msg.Content, opts),
	}
	if msg.Role != "" {
		summary.Role = string(msg.Role)
	}
	if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
		summary.ToolCallID = *msg.ChatToolMessage.ToolCallID
	}

	am := msg.ChatAssistantMessage
	if am == nil {
		return summary
	}
	if am.Refusal != nil && *am.Refusal != "" {
		summary.Refusal = *am.Refusal
	}
	if am.Reasoning != nil && *am.Reasoning != "" {
		summary.Reasoning = *am.Reasoning
	}
	if len(am.ReasoningDetails) > 0 {
		summary.ReasoningDetails = make([]ReasoningDetailSummary, 0, len(am.ReasoningDetails))
		for _, rd := range am.ReasoningDetails {
			detail := ReasoningDetailSummary{Type: string(rd.Type)}
			if rd.Text != nil {
				detail.Text = *rd.Text
			}
			summary.ReasoningDetails = append(summary.ReasoningDetails, detail)
		}
	}
	if am.Audio != nil {
		summary.Audio = &AudioSummary{ID: am.Audio.ID, Transcript: am.Audio.Transcript}
	}
	if len(am.ToolCalls) > 0 {
		summary.ToolCalls = make([]ToolCallSummary, 0, len(am.ToolCalls))
		for _, tc := range am.ToolCalls {
			call := ToolCallSummary{Type: "function"}
			if tc.ID != nil {
				call.ID = *tc.ID
			}
			if tc.Type != nil {
				call.Type = *tc.Type
			}
			if tc.Function.Name != nil {
				call.Name = *tc.Function.Name
			}
			call.Args = tc.Function.Arguments
			summary.ToolCalls = append(summary.ToolCalls, call)
		}
	}
	return summary
}

// ExtractChatContentText concatenates the text blocks of a message, matching the
// behaviour span content has always had. Non-text blocks become attachments.
func ExtractChatContentText(content *ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	if content.ContentBlocks == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range content.ContentBlocks {
		if block.Text != nil {
			b.WriteString(*block.Text)
		}
	}
	return b.String()
}

// extractChatAttachments summarizes the non-text blocks of a chat message.
func extractChatAttachments(content *ChatMessageContent, opts AttachmentOptions) []AttachmentSummary {
	if content == nil || len(content.ContentBlocks) == 0 {
		return nil
	}
	var out []AttachmentSummary
	for _, block := range content.ContentBlocks {
		switch {
		case block.ImageURLStruct != nil:
			a := AttachmentSummary{Kind: AttachmentImage}
			if block.ImageURLStruct.Detail != nil {
				a.Detail = *block.ImageURLStruct.Detail
			}
			if block.ImageURLStruct.FileID != nil {
				a.FileID = *block.ImageURLStruct.FileID
			}
			applyPayload(&a, block.ImageURLStruct.URL, opts)
			out = append(out, a)

		case block.InputAudio != nil:
			a := AttachmentSummary{Kind: AttachmentAudio}
			if block.InputAudio.Format != nil {
				a.Format = *block.InputAudio.Format
				a.MediaType = "audio/" + a.Format
			}
			applyBase64Payload(&a, block.InputAudio.Data, opts)
			out = append(out, a)

		case block.File != nil:
			a := AttachmentSummary{Kind: AttachmentFile}
			if block.File.Filename != nil {
				a.Filename = *block.File.Filename
			}
			if block.File.FileType != nil {
				a.MediaType = *block.File.FileType
			}
			if block.File.FileID != nil {
				a.FileID = *block.File.FileID
			}
			if block.File.FileURL != nil {
				applyPayload(&a, *block.File.FileURL, opts)
			}
			if block.File.FileData != nil {
				applyBase64Payload(&a, *block.File.FileData, opts)
			}
			out = append(out, a)
		}
	}
	return out
}

// ExtractResponsesAttachments summarizes the non-text blocks of a Responses-API
// message, mirroring extractChatAttachments for the chat shape.
func ExtractResponsesAttachments(content *ResponsesMessageContent, opts AttachmentOptions) []AttachmentSummary {
	if content == nil || len(content.ContentBlocks) == 0 {
		return nil
	}
	var out []AttachmentSummary
	for _, block := range content.ContentBlocks {
		switch {
		case block.ResponsesInputMessageContentBlockImage != nil:
			a := AttachmentSummary{Kind: AttachmentImage}
			if block.Detail != nil {
				a.Detail = *block.Detail
			}
			if block.FileID != nil {
				a.FileID = *block.FileID
			}
			if block.ImageURL != nil {
				applyPayload(&a, *block.ImageURL, opts)
			}
			out = append(out, a)

		case block.Audio != nil:
			a := AttachmentSummary{Kind: AttachmentAudio, Format: block.Audio.Format}
			if a.Format != "" {
				a.MediaType = "audio/" + a.Format
			}
			applyBase64Payload(&a, block.Audio.Data, opts)
			out = append(out, a)

		case block.ResponsesInputMessageContentBlockFile != nil:
			a := AttachmentSummary{Kind: AttachmentFile}
			if block.Filename != nil {
				a.Filename = *block.Filename
			}
			if block.FileType != nil {
				a.MediaType = *block.FileType
			}
			if block.FileID != nil {
				a.FileID = *block.FileID
			}
			if block.FileURL != nil {
				applyPayload(&a, *block.FileURL, opts)
			}
			if block.FileData != nil {
				applyBase64Payload(&a, *block.FileData, opts)
			}
			out = append(out, a)
		}
	}
	return out
}

// applyPayload routes a URL that may be either a reference or an inline data: URL.
func applyPayload(a *AttachmentSummary, url string, opts AttachmentOptions) {
	if url == "" {
		return
	}
	if !strings.HasPrefix(url, "data:") {
		a.URL = url
		return
	}
	// ParseDataURL is the shared parser and reports whether ";base64" was present;
	// RFC 2397 makes it optional, and without it the payload is percent-encoded.
	mediaType, isBase64, payload, ok := ParseDataURL(url)
	if !ok {
		// Unparseable, but still inline: a data: URL must never reach a.URL,
		// which is reference-only.
		a.Inline = true
		return
	}
	if a.MediaType == "" {
		a.MediaType = mediaType
	}
	if isBase64 {
		applyBase64Payload(a, payload, opts)
		return
	}
	applyPercentEncodedPayload(a, payload, opts)
}

// applyBase64Payload records the decoded size of an inline payload, copying the
// bytes only when the caller opted in and the payload fits under the cap.
func applyBase64Payload(a *AttachmentSummary, payload string, opts AttachmentOptions) {
	if payload == "" {
		return
	}
	a.Inline = true
	a.ByteSize = base64DecodedSize(payload)
	if opts.Inline && a.ByteSize <= opts.cap() {
		a.Data = payload
	}
}

// applyPercentEncodedPayload handles a data URL without ";base64". The bytes are
// re-encoded because AttachmentSummary.Data is base64 by contract.
func applyPercentEncodedPayload(a *AttachmentSummary, payload string, opts AttachmentOptions) {
	if payload == "" {
		return
	}
	decoded, err := neturl.QueryUnescape(payload)
	if err != nil {
		decoded = payload
	}
	a.Inline = true
	a.ByteSize = len(decoded)
	if opts.Inline && a.ByteSize <= opts.cap() {
		a.Data = base64.StdEncoding.EncodeToString([]byte(decoded))
	}
}

// base64DecodedSize is the exact decoded length of a canonical base64 payload.
// base64.DecodedLen is an upper bound that ignores padding, and decoding just to
// measure would copy the whole payload on the span path.
func base64DecodedSize(payload string) int {
	n := len(payload) / 4 * 3
	for i := len(payload) - 1; i >= 0 && payload[i] == '='; i-- {
		n--
	}
	if n < 0 {
		return 0
	}
	return n
}
