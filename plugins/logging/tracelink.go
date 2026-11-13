package logging

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
)

// Trace ids and span ids are client-supplied header values; clamp them to their column widths.
const (
	maxPersistedTraceIDLen            = 128
	maxPersistedProviderResponseIDLen = 255
)

// traceLinkData is what a gateway row records so a trace pushed by an observability SDK can be
// matched to it: the trace position the request named, and a fingerprint of its last user message.
type traceLinkData struct {
	TraceID          string
	ParentSpanID     string
	InputFingerprint string
}

// traceLinkFromContext reads the trace link of the request on ctx. data, when given, supplies the
// captured input for the fingerprint. The fingerprint is a short hash, never content, so it is
// recorded whatever the content logging policy says.
func traceLinkFromContext(ctx *schemas.BifrostContext, data *InitialLogData) traceLinkData {
	var link traceLinkData
	if ctx == nil {
		return link
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	exportTraceID, _ := ctx.Value(schemas.BifrostContextKeyExportTraceID).(string)
	parentSpanID, _ := ctx.Value(schemas.BifrostContextKeyParentSpanID).(string)
	if named := traceingest.LinkFromRequest(headers, exportTraceID, parentSpanID); named != nil {
		link.TraceID = clampString(named.TraceID, maxPersistedTraceIDLen)
		link.ParentSpanID = clampString(named.ParentSpanID, maxPersistedTraceIDLen)
	}
	if data != nil {
		link.InputFingerprint = traceingest.ChatFingerprint(data.InputHistory)
		if link.InputFingerprint == "" {
			link.InputFingerprint = traceingest.ResponsesFingerprint(data.ResponsesInputHistory)
		}
	}
	return link
}

// applyTraceLink writes the link onto an LLM row. Empty values stay NULL.
func applyTraceLink(entry *logstore.Log, link traceLinkData) {
	if link.TraceID != "" {
		entry.TraceID = &link.TraceID
	}
	if link.ParentSpanID != "" {
		entry.ParentSpanID = &link.ParentSpanID
	}
	if link.InputFingerprint != "" {
		entry.InputFingerprint = &link.InputFingerprint
	}
}

// applyProviderResponseID records the id the provider gave its response (chatcmpl-..., resp_...,
// msg_...). SDKs log the same id on their span, which makes it a join key that needs no header.
func applyProviderResponseID(entry *logstore.Log, result *schemas.BifrostResponse) {
	if entry == nil || result == nil {
		return
	}
	id := ""
	switch {
	case result.ChatResponse != nil:
		id = result.ChatResponse.ID
	case result.TextCompletionResponse != nil:
		id = result.TextCompletionResponse.ID
	case result.ResponsesResponse != nil && result.ResponsesResponse.ID != nil:
		id = *result.ResponsesResponse.ID
	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.ID != nil:
		id = *result.ResponsesStreamResponse.Response.ID
	}
	if id != "" {
		entry.ProviderResponseID = new(clampString(id, maxPersistedProviderResponseIDLen))
	}
}
