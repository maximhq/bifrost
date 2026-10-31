package logging

import (
	"context"
	"reflect"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/passthroughparse"
)

// isPassthroughLog reports whether entry is a passthrough call's row.
func isPassthroughLog(entry *logstore.Log) bool {
	return entry.Object == string(schemas.PassthroughRequest) || entry.Object == string(schemas.PassthroughStreamRequest)
}

// tagPassthroughEntry types a completed passthrough row and flags it for body parsing. It runs on
// the request path, so it reads no body: rows that are not passthrough return after a string
// compare. A passthrough row is tagged is_passthrough and, when its route has a typed equivalent,
// stored under that type (chat_completion, responses_stream, embedding, ...) so it is counted and
// filtered like a typed request; an unknown route stays passthrough. Typing needs only the route,
// so it applies whatever the content policy. Only when the final policy stores content is the row
// also flagged for the batch writer to parse its bodies.
//
// The wire format comes from the base provider the core worker stamped for this attempt, so a
// custom provider is read with the shape of the provider it wraps and a fallback attempt with its
// own.
func tagPassthroughEntry(ctx *schemas.BifrostContext, entry *logstore.Log, initial *InitialLogData, contentLoggingEnabled bool) {
	if !isPassthroughLog(entry) {
		return
	}
	format := schemas.ModelProvider(entry.Provider)
	if base, ok := ctx.Value(schemas.BifrostContextKeyBaseProviderType).(schemas.ModelProvider); ok && base != "" {
		format = base
	}
	entry.IsPassthrough = true
	if initial != nil {
		stream := entry.Object == string(schemas.PassthroughStreamRequest)
		if inferred := passthroughparse.InferRequestType(format, entry.Model, initial.PassthroughMethod, initial.PassthroughPath, stream); inferred != "" {
			entry.Object = string(inferred)
		}
	}
	if contentLoggingEnabled {
		entry.PassthroughParseFormat = format
		entry.PassthroughParseMaxBytes = largePayloadRequestThreshold(ctx)
	}
}

// applyParsedPassthrough decodes a flagged passthrough row's raw bodies into the structured fields a
// typed request fills (input history, tools, per-kind inputs and outputs, stop reason, tool calls),
// through the same captureRequestInput / applyContentOutputToEntry the typed path uses, so the row
// renders and searches like any other. It runs on the batch writer goroutine, never on the request path.
//
// Parsing is best-effort. A body that does not decode, a route with no typed equivalent, or a panic
// anywhere in the conversion leaves the row exactly as it is without parsing: raw bodies only.
// Whatever side did decode is kept. Failures are logged at debug level only.
func (p *LoggerPlugin) applyParsedPassthrough(entry *logstore.Log) {
	format := entry.PassthroughParseFormat
	if format == "" {
		return
	}
	// Parse once: a batch that fails to write re-enters processBatch with the same entry.
	entry.PassthroughParseFormat = ""
	params, ok := entry.ParamsParsed.(*schemas.PassthroughLogParams)
	if !ok || params == nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			p.logger.Debug("passthrough log parse for %s panicked, keeping raw bodies: %v", entry.ID, r)
		}
	}()

	result, err := passthroughparse.Parse(passthroughparse.Input{
		Provider:     format,
		Model:        params.Model,
		Method:       params.Method,
		Path:         params.Path,
		StatusCode:   params.StatusCode,
		RequestBody:  []byte(entry.PassthroughRequestBody),
		ResponseBody: []byte(entry.PassthroughResponseBody),
		Stream:       entry.Stream,
	})
	if err != nil {
		p.logger.Debug("passthrough log parse for %s was partial: %v", entry.ID, err)
	}
	if result == nil {
		return
	}

	// Capture the input into scratch first: it is the step that can trip over an unexpected shape,
	// and a panic there must leave the row untouched.
	var captured InitialLogData
	if req := result.Request; req != nil {
		var thresholdCtx *schemas.BifrostContext
		if entry.PassthroughParseMaxBytes > 0 {
			thresholdCtx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			thresholdCtx.SetValue(schemas.BifrostContextKeyLargePayloadRequestThreshold, entry.PassthroughParseMaxBytes)
		}
		p.captureRequestInput(thresholdCtx, req, &captured)
	}
	var stopReason *string
	var toolCalls []schemas.ChatAssistantMessageToolCall
	if resp := result.Response; resp != nil {
		stopReason = responseStopReason(resp)
		toolCalls = nonStreamingToolCalls(resp)
	}

	params.ParsedType = result.RequestType
	if format != schemas.ModelProvider(entry.Provider) {
		params.BaseProvider = format
	}
	if !isNilValue(captured.Params) {
		params.ModelParams = captured.Params
	}
	applyCapturedInputToEntry(entry, &captured)
	if resp := result.Response; resp != nil {
		applyContentOutputToEntry(entry, resp)
	}
	if entry.StopReason == nil && stopReason != nil {
		entry.StopReason = stopReason
	}
	if len(toolCalls) > 0 {
		applyToolCallsToEntry(entry, toolCalls, true)
	}
}

// applyCapturedInputToEntry sets the input fields captureRequestInput recorded, leaving fields it
// did not record (the passthrough row's own params and raw bodies) as they are.
func applyCapturedInputToEntry(entry *logstore.Log, captured *InitialLogData) {
	if len(captured.InputHistory) > 0 {
		entry.InputHistoryParsed = captured.InputHistory
	}
	if len(captured.ResponsesInputHistory) > 0 {
		entry.ResponsesInputHistoryParsed = captured.ResponsesInputHistory
	}
	if len(captured.Tools) > 0 {
		entry.ToolsParsed = captured.Tools
	}
	if len(captured.EmbeddingInput) > 0 {
		entry.EmbeddingInputParsed = captured.EmbeddingInput
	}
	if captured.SpeechInput != nil {
		entry.SpeechInputParsed = captured.SpeechInput
	}
	if captured.TranscriptionInput != nil {
		entry.TranscriptionInputParsed = captured.TranscriptionInput
	}
	if captured.OCRInput != nil {
		entry.OCRInputParsed = captured.OCRInput
	}
	if captured.ImageGenerationInput != nil {
		entry.ImageGenerationInputParsed = captured.ImageGenerationInput
	}
	if captured.ImageEditInput != nil {
		entry.ImageEditInputParsed = captured.ImageEditInput
	}
	if captured.ImageVariationInput != nil {
		entry.ImageVariationInputParsed = captured.ImageVariationInput
	}
	if captured.VideoGenerationInput != nil {
		entry.VideoGenerationInputParsed = captured.VideoGenerationInput
	}
	if captured.VideoEditInput != nil {
		entry.VideoEditInputParsed = captured.VideoEditInput
	}
}

// isNilValue reports whether v is nil or a nil pointer/map/slice held in an interface, which would
// otherwise serialize as an explicit null.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
