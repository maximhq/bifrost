// Package logging provides trace-to-SpanLog conversion for the ObservabilityPlugin.Inject path.
package logging

import (
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// convertTraceToSpanLogs converts a completed schemas.Trace into a root SpanLog
// and its child SpanLogs for database persistence.
func (p *LoggerPlugin) convertTraceToSpanLogs(trace *schemas.Trace) (*logstore.SpanLog, []*logstore.SpanLog) {
	if trace == nil {
		return nil, nil
	}

	now := time.Now().UTC()

	// Build child spans first so we can aggregate into root
	childSpans := make([]*logstore.SpanLog, 0, len(trace.Spans))
	for _, span := range trace.Spans {
		if span == nil {
			continue
		}
		spanLog := p.convertSpan(span, trace.TraceID)
		if spanLog != nil {
			childSpans = append(childSpans, spanLog)
		}
	}

	// Build root span (kind="trace")
	rootSpan := &logstore.SpanLog{
		ID:        trace.TraceID,
		Kind:      string(schemas.SpanKindTrace),
		Timestamp: trace.StartTime,
		Status:    "success",
		CreatedAt: now,
	}

	// Set trace name from attributes or derive from first LLM span
	if name, ok := trace.Attributes["trace_name"].(string); ok && name != "" {
		rootSpan.Name = name
	}

	// Set user-agent from attributes
	if ua, ok := trace.Attributes["user_agent"].(string); ok {
		rootSpan.UserAgent = ua
		rootSpan.UserAgentLabel = ClassifyUserAgent(ua)
	}

	// Set tags from attributes
	if tags, ok := trace.Attributes["trace_tags"].(map[string]string); ok {
		rootSpan.TagsParsed = tags
	}

	// Set end time
	if !trace.EndTime.IsZero() {
		latency := float64(trace.EndTime.Sub(trace.StartTime).Milliseconds())
		rootSpan.Latency = &latency
	}

	// Aggregate child LLM spans into root
	p.aggregateChildSpansIntoRoot(rootSpan, childSpans)

	return rootSpan, childSpans
}

// convertSpan converts a single schemas.Span to a logstore.SpanLog.
func (p *LoggerPlugin) convertSpan(span *schemas.Span, traceID string) *logstore.SpanLog {
	if span == nil {
		return nil
	}

	spanLog := &logstore.SpanLog{
		ID:        span.SpanID,
		Kind:      string(span.Kind),
		Name:      span.Name,
		Timestamp: span.StartTime,
		Status:    convertSpanStatus(span.Status),
		StatusMessage: span.StatusMsg,
		CreatedAt: time.Now().UTC(),
	}

	// Set parent span ID — if empty, parent is the trace root
	if span.ParentID != "" {
		spanLog.ParentSpanID = &span.ParentID
	} else {
		spanLog.ParentSpanID = &traceID
	}

	// Set latency from span timing
	if !span.EndTime.IsZero() && !span.StartTime.IsZero() {
		latency := float64(span.EndTime.Sub(span.StartTime).Milliseconds())
		spanLog.Latency = &latency
	}

	// Set events
	if len(span.Events) > 0 {
		spanLog.EventsParsed = span.Events
	}

	// Extract span attributes into typed SpanLog fields
	p.extractSpanAttributes(spanLog, span)

	return spanLog
}

// extractSpanAttributes maps span.Attributes (OTel semantic convention keys) to typed SpanLog fields.
func (p *LoggerPlugin) extractSpanAttributes(spanLog *logstore.SpanLog, span *schemas.Span) {
	if span.Attributes == nil {
		return
	}

	attrs := span.Attributes

	// Provider/model info (OTel keys)
	if v, ok := attrs[schemas.AttrProviderName].(string); ok {
		spanLog.Provider = v
	}
	if v, ok := attrs[schemas.AttrRequestModel].(string); ok {
		spanLog.Model = v
	}
	// Response model may override request model
	if v, ok := attrs[schemas.AttrResponseModel].(string); ok && v != "" {
		spanLog.Model = v
	}
	if v, ok := attrs[schemas.AttrObject].(string); ok {
		spanLog.Object = v
	}

	// Key/routing info (Bifrost context attributes)
	if v, ok := attrs[schemas.AttrSelectedKeyID].(string); ok {
		spanLog.SelectedKeyID = v
	}
	if v, ok := attrs[schemas.AttrSelectedKeyName].(string); ok {
		spanLog.SelectedKeyName = v
	}
	if v, ok := attrs[schemas.AttrVirtualKeyID].(string); ok {
		spanLog.VirtualKeyID = &v
	}
	if v, ok := attrs[schemas.AttrVirtualKeyName].(string); ok {
		spanLog.VirtualKeyName = &v
	}
	if v, ok := attrs[schemas.AttrRoutingRuleID].(string); ok {
		spanLog.RoutingRuleID = &v
	}
	if v, ok := attrs[schemas.AttrRoutingRuleName].(string); ok {
		spanLog.RoutingRuleName = &v
	}

	// Retry/fallback info
	if v, ok := attrs[schemas.AttrNumberOfRetries].(int); ok {
		spanLog.NumberOfRetries = v
	}
	if v, ok := attrs[schemas.AttrFallbackIndex].(int); ok {
		spanLog.FallbackIndex = v
	}

	// Token usage — stored as individual int attributes by the tracer
	promptTokens := attrInt(attrs, schemas.AttrPromptTokens)
	completionTokens := attrInt(attrs, schemas.AttrCompletionTokens)
	totalTokens := attrInt(attrs, schemas.AttrTotalTokens)
	if totalTokens > 0 || promptTokens > 0 || completionTokens > 0 {
		spanLog.PromptTokens = promptTokens
		spanLog.CompletionTokens = completionTokens
		spanLog.TotalTokens = totalTokens
		spanLog.TokenUsageParsed = &schemas.BifrostLLMUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
		}
	}

	// Cost
	if v, ok := attrs[schemas.AttrUsageCost].(float64); ok && v > 0 {
		spanLog.Cost = &v
	}

	// Output messages — the tracer stores these as typed objects
	if v, ok := attrs[schemas.AttrOutputMessages].([]schemas.ChatMessage); ok {
		if len(v) > 0 {
			spanLog.OutputMessageParsed = &v[0]
		}
	} else if v, ok := attrs[schemas.AttrOutputMessages].(*schemas.ChatMessage); ok {
		spanLog.OutputMessageParsed = v
	}

	// Input messages
	if v, ok := attrs[schemas.AttrInputMessages].([]schemas.ChatMessage); ok {
		spanLog.InputHistoryParsed = v
	}

	// Finish reason
	if v, ok := attrs[schemas.AttrFinishReason].(string); ok {
		_ = v // stored in attributes, not a direct SpanLog field
	}

	// Error details — check both OTel key and direct struct
	if errMsg, ok := attrs[schemas.AttrError].(string); ok && errMsg != "" {
		var errType, errCode *string
		if v, ok := attrs[schemas.AttrErrorType].(string); ok {
			errType = &v
		}
		if v, ok := attrs[schemas.AttrErrorCode].(string); ok {
			errCode = &v
		}
		spanLog.ErrorDetailsParsed = &schemas.BifrostError{
			Error: &schemas.ErrorField{
				Message: errMsg,
				Type:    errType,
				Code:    errCode,
			},
		}
	}

	// Store all attributes as generic JSON for the detail view
	spanLog.AttributesParsed = attrs
}

// aggregateChildSpansIntoRoot computes root span aggregate fields from child spans.
func (p *LoggerPlugin) aggregateChildSpansIntoRoot(root *logstore.SpanLog, children []*logstore.SpanLog) {
	if len(children) == 0 {
		return
	}

	root.SpanCount = len(children)

	var (
		totalTokens      int
		promptTokens     int
		completionTokens int
		totalCost        float64
		hasCost          bool
		providers        = make(map[string]struct{})
		models           = make(map[string]struct{})
		objects          = make(map[string]struct{})
		hasError         bool
		hasProcessing    bool
		firstLLMInput    bool
	)

	for _, child := range children {
		if child.Kind != string(schemas.SpanKindLLMCall) {
			continue
		}

		// Aggregate tokens
		totalTokens += child.TotalTokens
		promptTokens += child.PromptTokens
		completionTokens += child.CompletionTokens

		// Aggregate cost
		if child.Cost != nil {
			totalCost += *child.Cost
			hasCost = true
		}

		// Collect distinct providers/models/objects
		if child.Provider != "" {
			providers[child.Provider] = struct{}{}
		}
		if child.Model != "" {
			models[child.Model] = struct{}{}
		}
		if child.Object != "" {
			objects[child.Object] = struct{}{}
		}

		// First input from first LLM span
		if !firstLLMInput {
			root.InputHistoryParsed = child.InputHistoryParsed
			root.InputHistory = child.InputHistory
			root.ResponsesInputHistoryParsed = child.ResponsesInputHistoryParsed
			root.ResponsesInputHistory = child.ResponsesInputHistory
			root.SelectedKeyID = child.SelectedKeyID
			root.SelectedKeyName = child.SelectedKeyName
			root.VirtualKeyID = child.VirtualKeyID
			root.VirtualKeyName = child.VirtualKeyName
			root.RoutingRuleID = child.RoutingRuleID
			root.RoutingRuleName = child.RoutingRuleName
			root.RoutingEnginesUsed = child.RoutingEnginesUsed
			firstLLMInput = true
		}

		// Last output from last LLM span (overwritten each time)
		root.OutputMessageParsed = child.OutputMessageParsed
		root.OutputMessage = child.OutputMessage
		root.ResponsesOutputParsed = child.ResponsesOutputParsed
		root.ResponsesOutput = child.ResponsesOutput

		// Track status
		if child.Status == "error" {
			hasError = true
			if root.ErrorDetailsParsed == nil {
				root.ErrorDetailsParsed = child.ErrorDetailsParsed
				root.ErrorDetails = child.ErrorDetails
			}
		}
		if child.Status == "processing" {
			hasProcessing = true
		}

		// Name defaults to first model if not set
		if root.Name == "" {
			root.Name = child.Model
		}
	}

	// Set aggregated fields
	root.TotalTokens = totalTokens
	root.PromptTokens = promptTokens
	root.CompletionTokens = completionTokens
	if hasCost {
		root.Cost = &totalCost
	}

	// Build aggregated token usage
	if totalTokens > 0 {
		root.TokenUsageParsed = &schemas.BifrostLLMUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
		}
	}

	// Set comma-separated providers/models/objects
	root.Provider = joinMapKeys(providers)
	root.Model = joinMapKeys(models)
	root.Object = joinMapKeys(objects)

	// Derive trace status
	if hasError {
		root.Status = "error"
	} else if hasProcessing {
		root.Status = "processing"
	} else {
		root.Status = "success"
	}

	// Build content summary from root's input/output
	root.ContentSummary = root.BuildContentSummary()
}

// convertSpanStatus converts schemas.SpanStatus to the string status used in logs.
func convertSpanStatus(status schemas.SpanStatus) string {
	switch status {
	case schemas.SpanStatusOk:
		return "success"
	case schemas.SpanStatusError:
		return "error"
	default:
		return "processing"
	}
}

// joinMapKeys joins map keys into a comma-separated string.
func joinMapKeys(m map[string]struct{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ",")
}

// attrInt extracts an int from a map[string]any, handling both int and float64 (JSON numbers).
func attrInt(attrs map[string]any, key string) int {
	v, ok := attrs[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}
