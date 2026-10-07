package deepseek

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// DeepSeek's Anthropic-compatible API documents document content blocks as
// unsupported ("Documents: Not Supported"; images are supported) but does not
// reject them: a request carrying a document returns HTTP 200 with the
// document silently dropped from the model's view. An upstream-error-driven
// fallback can therefore never enforce the contract, so the provider checks
// for document blocks itself before conversion or egress and returns a typed,
// fallback-eligible error. The guard reads block type discriminators only; it
// never reads file data, URLs, filenames, or text.
//
// Source: https://api-docs.deepseek.com/guides/anthropic_api

const unsupportedDocumentCode = "deepseek_unsupported_document_content"

// rejectUnsupportedChatContent fails a Chat request bound for the Anthropic
// endpoint when any message carries a file (document) content block.
func rejectUnsupportedChatContent(request *schemas.BifrostChatRequest) *schemas.BifrostError {
	if request == nil {
		return nil
	}
	for i := range request.Input {
		content := request.Input[i].Content
		if content == nil {
			continue
		}
		for j := range content.ContentBlocks {
			if content.ContentBlocks[j].Type == schemas.ChatContentBlockTypeFile {
				return newUnsupportedDocumentError()
			}
		}
	}
	return nil
}

// rejectUnsupportedResponsesContent applies the same contract to Responses
// requests, including file blocks inside function tool-call outputs, which
// reach the model as content the same way a user turn does.
func rejectUnsupportedResponsesContent(request *schemas.BifrostResponsesRequest) *schemas.BifrostError {
	if request == nil {
		return nil
	}
	for i := range request.Input {
		message := &request.Input[i]
		if message.Content != nil && hasResponsesFileBlock(message.Content.ContentBlocks) {
			return newUnsupportedDocumentError()
		}
		if message.ResponsesToolMessage == nil {
			continue
		}
		if message.ResponsesToolMessage.Output != nil &&
			hasResponsesFileBlock(message.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks) {
			return newUnsupportedDocumentError()
		}
		// A replayed web_fetch result carries its page as a web_fetch_document,
		// not as a file content block, so reading only content blocks would let
		// it reach DeepSeek as exactly the shape this guard refuses. What the
		// egress actually emits decides: convertBifrostWebFetchCallToAnthropicBlocks
		// passes the document's own type through and defaults only an EMPTY one
		// to "document", so a document declaring another type -- "text", say --
		// becomes a text block DeepSeek accepts, and refusing it would cost the
		// caller a request the provider would have served.
		if wf := message.ResponsesToolMessage.ResponsesWebFetchCall; wf != nil && wf.Document != nil &&
			emittedWebFetchDocumentBlockType(wf.Document) == webFetchDocumentBlockType {
			return newUnsupportedDocumentError()
		}
	}
	return nil
}

// webFetchDocumentBlockType is the Anthropic content-block type a web-fetch
// document lands on when it declares none, mirroring the egress converter's own
// default. Kept as a literal rather than imported so this guard stays a pure
// type-discriminator read with no dependency on the Anthropic package.
const webFetchDocumentBlockType = "document"

// emittedWebFetchDocumentBlockType reports the block type the Anthropic egress
// will give this web-fetch document: its own type, or "document" when it
// declares none. The guard asks what gets EMITTED, not what the field is called.
func emittedWebFetchDocumentBlockType(doc *schemas.ResponsesWebFetchDocument) string {
	if doc == nil || doc.Type == "" {
		return webFetchDocumentBlockType
	}
	return doc.Type
}

// hasResponsesFileBlock reports whether any block is a file (document) block,
// reading the type discriminator only.
func hasResponsesFileBlock(blocks []schemas.ResponsesMessageContentBlock) bool {
	for i := range blocks {
		if blocks[i].Type == schemas.ResponsesInputMessageContentBlockTypeFile {
			return true
		}
	}
	return false
}

// newUnsupportedDocumentError is an Anthropic-shaped invalid_request_error that
// explicitly permits fallbacks, so a routing rule with an enumerated next
// provider carries the untouched request onward instead of failing the caller.
func newUnsupportedDocumentError() *schemas.BifrostError {
	statusCode := 400
	errorType := "invalid_request_error"
	allowFallbacks := true
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		AllowFallbacks: &allowFallbacks,
		Error: &schemas.ErrorField{
			Type:    &errorType,
			Code:    schemas.Ptr(unsupportedDocumentCode),
			Message: "deepseek's anthropic-compatible api does not support document content blocks; the request was not sent upstream",
		},
	}
}
