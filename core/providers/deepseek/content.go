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
		if message.ResponsesToolMessage != nil && message.ResponsesToolMessage.Output != nil &&
			hasResponsesFileBlock(message.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks) {
			return newUnsupportedDocumentError()
		}
	}
	return nil
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
