package logging

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/passthroughparse"
)

// isPassthroughLog reports whether entry is a passthrough call's row.
func isPassthroughLog(entry *logstore.Log) bool {
	return entry.Object == string(schemas.PassthroughRequest) || entry.Object == string(schemas.PassthroughStreamRequest)
}

// markPassthroughForParsing flags a completed passthrough row so the batch writer parses its bodies.
// It runs on the request path, so it does nothing but a string compare and a context read: rows that are
// not passthrough, and passthrough rows whose final content policy does not store content, are left
// untouched and never parsed. The wire format comes from the base provider the core worker stamped
// for this attempt, so a custom provider parses with the shape of the provider it wraps and a
// fallback attempt with its own.
func markPassthroughForParsing(ctx *schemas.BifrostContext, entry *logstore.Log, contentLoggingEnabled bool) {
	if !contentLoggingEnabled || !isPassthroughLog(entry) {
		return
	}
	format := schemas.ModelProvider(entry.Provider)
	if base, ok := ctx.Value(schemas.BifrostContextKeyBaseProviderType).(schemas.ModelProvider); ok && base != "" {
		format = base
	}
	entry.PassthroughParseFormat = format
}

// applyParsedPassthrough decodes a flagged passthrough row's raw bodies into the structured fields a
// typed request fills (input history, tools, output, stop reason, tool calls), so the row renders and
// searches like any other. It runs on the batch writer goroutine, never on the request path.
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
		Stream:       entry.Object == string(schemas.PassthroughStreamRequest),
	})
	if err != nil {
		p.logger.Debug("passthrough log parse for %s was partial: %v", entry.ID, err)
	}
	if result == nil {
		return
	}

	// Build every field first and assign at the end, so a panic part-way leaves the row untouched.
	var (
		inputHistory          []schemas.ChatMessage
		responsesInputHistory []schemas.ResponsesMessage
		modelParams           interface{}
		tools                 []schemas.ChatTool
	)
	if req := result.Request; req != nil {
		inputHistory, responsesInputHistory = p.extractInputHistory(req)
		modelParams, tools = textRequestParamsAndTools(req)
	}

	parsed := &logstore.Log{}
	var stopReason *string
	var toolCalls []schemas.ChatAssistantMessageToolCall
	if resp := result.Response; resp != nil {
		stopReason = responseStopReason(resp)
		applyTextOutputToEntry(parsed, resp)
		toolCalls = nonStreamingToolCalls(resp)
	}

	params.ParsedType = result.RequestType
	if format != schemas.ModelProvider(entry.Provider) {
		params.BaseProvider = format
	}
	params.ModelParams = modelParams
	if len(inputHistory) > 0 {
		entry.InputHistoryParsed = inputHistory
	}
	if len(responsesInputHistory) > 0 {
		entry.ResponsesInputHistoryParsed = responsesInputHistory
	}
	if len(tools) > 0 {
		entry.ToolsParsed = tools
	}
	if parsed.OutputMessageParsed != nil {
		entry.OutputMessageParsed = parsed.OutputMessageParsed
	}
	if len(parsed.ResponsesOutputParsed) > 0 {
		entry.ResponsesOutputParsed = parsed.ResponsesOutputParsed
	}
	if entry.StopReason == nil && stopReason != nil {
		entry.StopReason = stopReason
	}
	if len(toolCalls) > 0 {
		applyToolCallsToEntry(entry, toolCalls, true)
	}
}
