package traceingest

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
)

// OTelSpan is one OTLP span after wire decoding: ids as hex, attributes as a flat map.
type OTelSpan struct {
	TraceID       string
	SpanID        string
	ParentSpanID  string
	Name          string
	Kind          string // OTel span kind: client, server, internal, producer, consumer
	StartTime     time.Time
	EndTime       time.Time
	StatusError   bool
	StatusMessage string
	Attributes    map[string]any
	Resource      map[string]any
	Scope         string
	Events        []OTelEvent
}

// OTelEvent is one span event.
type OTelEvent struct {
	Name       string
	Time       time.Time
	Attributes map[string]any
}

// MapOTelSpan maps an OTLP span onto a span part. It reads every LLM convention in use on the wire:
// Langfuse (langfuse.*), OpenInference (Arize and Phoenix), OTel GenAI (gen_ai.*), OpenLLMetry
// (traceloop.*), the Vercel AI SDK (ai.*) and MLflow (mlflow.*). A span that matches none of them
// is still returned, as kind unknown, with all of its attributes kept.
func MapOTelSpan(span *OTelSpan) *SpanPart {
	attrs := attributes(span.Attributes)
	part := &SpanPart{
		TraceID: span.TraceID, SpanID: span.SpanID, Name: span.Name,
		Attributes: make(map[string]any, len(span.Attributes)+4),
	}
	if span.ParentSpanID != "" {
		part.ParentSpanIDs = []string{span.ParentSpanID}
	}
	if !span.StartTime.IsZero() {
		part.StartTime = &span.StartTime
	}
	if !span.EndTime.IsZero() {
		part.EndTime = &span.EndTime
	}
	if span.StatusError {
		part.Status = logstore.TraceSpanStatusError
		part.Error = span.StatusMessage
	}

	mapLangfuse(part, attrs)
	mapOpenInference(part, attrs)
	mapGenAI(part, attrs, span.Events)
	mapTraceloop(part, attrs)
	mapVercelAI(part, attrs)
	mapMLflow(part, attrs)
	mapCommon(part, attrs, span)

	// Every attribute not folded into input or output is kept as received.
	for key, value := range span.Attributes {
		if !attrs.consumed(key) {
			part.Attributes[key] = value
		}
	}
	if span.Kind != "" {
		part.Attributes["otel.span.kind"] = span.Kind
	}
	if span.Scope != "" {
		part.Attributes["otel.scope.name"] = span.Scope
	}
	if len(span.Resource) > 0 {
		part.Attributes["resource"] = span.Resource
	}
	if len(span.Events) > 0 {
		events := make([]any, len(span.Events))
		for i, event := range span.Events {
			events[i] = map[string]any{"name": event.Name, "time": event.Time.UTC().Format(time.RFC3339Nano), "attributes": event.Attributes}
		}
		part.Attributes["events"] = events
	}
	return part
}

// attrs reads typed values out of a flat attribute map and records which content keys were folded
// into a field, so they are not stored twice.
type attrs struct {
	values map[string]any
	used   map[string]struct{}
	// usedPrefixes are indexed families (llm.input_messages.*) folded into a field.
	usedPrefixes []string
}

func attributes(values map[string]any) *attrs {
	return &attrs{values: values, used: make(map[string]struct{})}
}

func (a *attrs) consumed(key string) bool {
	if _, ok := a.used[key]; ok {
		return true
	}
	for _, prefix := range a.usedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (a *attrs) has(key string) bool {
	_, ok := a.values[key]
	return ok
}

// str returns the first of keys that holds a non-empty string.
func (a *attrs) str(keys ...string) string {
	for _, key := range keys {
		switch v := a.values[key].(type) {
		case string:
			if v != "" {
				return v
			}
		case nil:
		default:
			if s := strings.TrimSpace(encodeValue(v)); s != "" {
				return s
			}
		}
	}
	return ""
}

// num returns the first of keys that holds a number.
func (a *attrs) num(keys ...string) (float64, bool) {
	for _, key := range keys {
		switch v := a.values[key].(type) {
		case float64:
			return v, true
		case int64:
			return float64(v), true
		case int:
			return float64(v), true
		case string:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func (a *attrs) integer(keys ...string) int {
	n, _ := a.num(keys...)
	return int(n)
}

// content takes the first of keys that holds a value and marks it folded. A string holding a JSON
// object or array is decoded, since every convention ships structured content as a JSON string.
func (a *attrs) content(keys ...string) any {
	for _, key := range keys {
		value, ok := a.values[key]
		if !ok || value == nil || value == "" {
			continue
		}
		a.used[key] = struct{}{}
		if text, isString := value.(string); isString {
			return decodeValue(text)
		}
		return value
	}
	return nil
}

// object decodes a JSON-object attribute, nil when the key is absent or not an object.
func (a *attrs) object(key string) map[string]any {
	switch v := a.values[key].(type) {
	case map[string]any:
		return v
	case string:
		var decoded map[string]any
		if sonic.Unmarshal([]byte(v), &decoded) == nil {
			return decoded
		}
	}
	return nil
}

// family rebuilds the nested value an SDK flattened into dotted keys under prefix:
// "p.0.message.role" and "p.0.message.content" become [{"message":{"role":..,"content":..}}].
// It marks the whole family folded. nil when no key has the prefix.
func (a *attrs) family(prefix string) any {
	root := make(map[string]any)
	found := false
	for key, value := range a.values {
		rest, ok := strings.CutPrefix(key, prefix+".")
		if !ok {
			continue
		}
		found = true
		node := root
		segments := strings.Split(rest, ".")
		for i, segment := range segments {
			if i == len(segments)-1 {
				if text, isString := value.(string); isString {
					node[segment] = decodeValue(text)
				} else {
					node[segment] = value
				}
				break
			}
			next, isMap := node[segment].(map[string]any)
			if !isMap {
				next = make(map[string]any)
				node[segment] = next
			}
			node = next
		}
	}
	if !found {
		return nil
	}
	a.usedPrefixes = append(a.usedPrefixes, prefix+".")
	return listify(root)
}

// listify turns every map whose keys are 0..n-1 into a list, depth first.
func listify(value any) any {
	node, ok := value.(map[string]any)
	if !ok {
		return value
	}
	for key, child := range node {
		node[key] = listify(child)
	}
	list := make([]any, len(node))
	for key, child := range node {
		index, err := strconv.Atoi(key)
		if err != nil || index < 0 || index >= len(node) {
			return node
		}
		list[index] = child
	}
	if len(node) == 0 {
		return node
	}
	return list
}

func setIfEmpty(dst *string, value string) {
	if *dst == "" {
		*dst = value
	}
}

func setIntIfZero(dst *int, value int) {
	if *dst == 0 {
		*dst = value
	}
}

func setValueIfNil(dst *any, value any) {
	if *dst == nil {
		*dst = value
	}
}

// kindNames maps each convention's span type names onto Bifrost's kinds.
var kindNames = map[string]string{
	// Langfuse observation types
	"generation": logstore.TraceSpanKindLLM, "span": logstore.TraceSpanKindTask, "event": logstore.TraceSpanKindEvent,
	"evaluator": logstore.TraceSpanKindEval,
	// OpenInference span kinds
	"llm": logstore.TraceSpanKindLLM, "tool": logstore.TraceSpanKindTool, "chain": logstore.TraceSpanKindChain,
	"agent": logstore.TraceSpanKindAgent, "retriever": logstore.TraceSpanKindRetriever, "embedding": logstore.TraceSpanKindEmbedding,
	"reranker": logstore.TraceSpanKindReranker, "guardrail": logstore.TraceSpanKindGuardrail, "prompt": logstore.TraceSpanKindTask,
	// OTel GenAI operation names
	"chat": logstore.TraceSpanKindLLM, "text_completion": logstore.TraceSpanKindLLM, "generate_content": logstore.TraceSpanKindLLM,
	"embeddings": logstore.TraceSpanKindEmbedding, "execute_tool": logstore.TraceSpanKindTool,
	"invoke_agent": logstore.TraceSpanKindAgent, "create_agent": logstore.TraceSpanKindAgent,
	// OpenLLMetry span kinds
	"workflow": logstore.TraceSpanKindChain, "task": logstore.TraceSpanKindTask,
	// MLflow span types
	"chat_model": logstore.TraceSpanKindLLM, "parser": logstore.TraceSpanKindFunction, "memory": logstore.TraceSpanKindFunction,
	// Braintrust span types
	"function": logstore.TraceSpanKindFunction, "score": logstore.TraceSpanKindScore, "eval": logstore.TraceSpanKindEval,
	"automation": logstore.TraceSpanKindTask, "facet": logstore.TraceSpanKindTask, "preprocessor": logstore.TraceSpanKindFunction,
	"classifier": logstore.TraceSpanKindFunction, "review": logstore.TraceSpanKindScore,
}

// KindFor maps a vendor's span type name onto a Bifrost kind, empty when the name is not known.
func KindFor(name string) string {
	return kindNames[strings.ToLower(strings.TrimSpace(name))]
}

func mapLangfuse(part *SpanPart, a *attrs) {
	setIfEmpty(&part.Kind, KindFor(a.str("langfuse.observation.type")))
	setValueIfNil(&part.Input, a.content("langfuse.observation.input", "langfuse.trace.input"))
	setValueIfNil(&part.Output, a.content("langfuse.observation.output", "langfuse.trace.output"))
	setIfEmpty(&part.Model, a.str("langfuse.observation.model.name"))
	setIfEmpty(&part.SessionID, a.str("langfuse.session.id"))
	setIfEmpty(&part.UserID, a.str("langfuse.user.id"))
	setIfEmpty(&part.Environment, a.str("langfuse.environment"))
	if usage := a.object("langfuse.observation.usage_details"); usage != nil {
		u := attributes(usage)
		setIntIfZero(&part.PromptTokens, u.integer("input", "prompt_tokens", "input_tokens"))
		setIntIfZero(&part.CompletionTokens, u.integer("output", "completion_tokens", "output_tokens"))
		setIntIfZero(&part.TotalTokens, u.integer("total", "total_tokens"))
		setIntIfZero(&part.CachedReadTokens, u.integer("cache_read_input_tokens", "input_cached_tokens", "input_cache_read"))
		setIntIfZero(&part.CachedWriteTokens, u.integer("cache_creation_input_tokens", "input_cache_creation"))
		setIntIfZero(&part.ReasoningTokens, u.integer("output_reasoning_tokens", "output_reasoning"))
	}
	if cost := a.object("langfuse.observation.cost_details"); cost != nil && part.Cost == nil {
		if total, ok := attributes(cost).num("total"); ok {
			part.Cost = &total
		}
	}
	if level := a.str("langfuse.observation.level"); strings.EqualFold(level, "ERROR") {
		part.Status = logstore.TraceSpanStatusError
		setIfEmpty(&part.Error, a.str("langfuse.observation.status_message"))
	}
	if metadata, ok := a.family("langfuse.observation.metadata").(map[string]any); ok {
		part.Metadata = mergeMap(part.Metadata, metadata)
	}
	if metadata, ok := a.family("langfuse.trace.metadata").(map[string]any); ok {
		part.Metadata = mergeMap(metadata, part.Metadata)
	}
	part.Tags = appendTags(part.Tags, a.values["langfuse.trace.tags"])
	if first := a.str("langfuse.observation.completion_start_time"); first != "" && part.StartTime != nil {
		if at, err := time.Parse(time.RFC3339Nano, strings.Trim(first, `"`)); err == nil {
			part.TimeToFirstToken = new(float64(at.Sub(*part.StartTime)) / float64(time.Millisecond))
		}
	}
}

func mapOpenInference(part *SpanPart, a *attrs) {
	setIfEmpty(&part.Kind, KindFor(a.str("openinference.span.kind")))
	if a.has("llm.input_messages.0.message.role") || a.has("llm.input_messages.0.message.content") {
		setValueIfNil(&part.Input, a.family("llm.input_messages"))
	}
	if a.has("llm.output_messages.0.message.role") || a.has("llm.output_messages.0.message.content") {
		setValueIfNil(&part.Output, a.family("llm.output_messages"))
	}
	setValueIfNil(&part.Input, a.content("input.value"))
	setValueIfNil(&part.Output, a.content("output.value"))
	setIfEmpty(&part.Model, a.str("llm.model_name", "embedding.model_name", "reranker.model_name"))
	setIfEmpty(&part.Provider, a.str("llm.provider", "llm.system"))
	setIntIfZero(&part.PromptTokens, a.integer("llm.token_count.prompt"))
	setIntIfZero(&part.CompletionTokens, a.integer("llm.token_count.completion"))
	setIntIfZero(&part.TotalTokens, a.integer("llm.token_count.total"))
	setIntIfZero(&part.CachedReadTokens, a.integer("llm.token_count.prompt_details.cache_read"))
	setIntIfZero(&part.CachedWriteTokens, a.integer("llm.token_count.prompt_details.cache_write"))
	setIntIfZero(&part.ReasoningTokens, a.integer("llm.token_count.completion_details.reasoning"))
	if cost, ok := a.num("llm.cost.total"); ok && part.Cost == nil {
		part.Cost = &cost
	}
	if metadata := a.object("metadata"); metadata != nil {
		a.used["metadata"] = struct{}{}
		part.Metadata = mergeMap(metadata, part.Metadata)
	}
	part.Tags = appendTags(part.Tags, a.values["tag.tags"])
	// The provider's own response id sits inside the raw output of a model call.
	if output, ok := part.Output.(map[string]any); ok && part.ProviderResponseID == "" && part.Kind == logstore.TraceSpanKindLLM {
		if id, isString := output["id"].(string); isString {
			part.ProviderResponseID = id
		}
	}
}

func mapGenAI(part *SpanPart, a *attrs, events []OTelEvent) {
	setIfEmpty(&part.Kind, KindFor(a.str("gen_ai.operation.name")))
	if part.Kind == "" && (a.has("gen_ai.request.model") || a.has("gen_ai.system") || a.has("gen_ai.provider.name")) {
		part.Kind = logstore.TraceSpanKindLLM
	}
	setIfEmpty(&part.Model, a.str("gen_ai.response.model", "gen_ai.request.model"))
	setIfEmpty(&part.Provider, a.str("gen_ai.provider.name", "gen_ai.system"))
	setIfEmpty(&part.ProviderResponseID, a.str("gen_ai.response.id"))
	setIfEmpty(&part.SessionID, a.str("gen_ai.conversation.id"))
	setIntIfZero(&part.PromptTokens, a.integer("gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens"))
	setIntIfZero(&part.CompletionTokens, a.integer("gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens"))
	setIntIfZero(&part.TotalTokens, a.integer("gen_ai.usage.total_tokens", "llm.usage.total_tokens"))
	setIntIfZero(&part.CachedReadTokens, a.integer("gen_ai.usage.cache_read_input_tokens", "gen_ai.usage.cache_read.input_tokens"))
	setIntIfZero(&part.CachedWriteTokens, a.integer("gen_ai.usage.cache_creation_input_tokens", "gen_ai.usage.cache_creation.input_tokens"))
	setIntIfZero(&part.ReasoningTokens, a.integer("gen_ai.usage.reasoning_tokens", "gen_ai.usage.reasoning.output_tokens"))
	if cost, ok := a.num("gen_ai.usage.cost"); ok && part.Cost == nil {
		part.Cost = &cost
	}
	setValueIfNil(&part.Input, a.content("gen_ai.input.messages", "gen_ai.tool.call.arguments"))
	setValueIfNil(&part.Output, a.content("gen_ai.output.messages", "gen_ai.tool.call.result"))
	if part.Input == nil && (a.has("gen_ai.prompt.0.role") || a.has("gen_ai.prompt.0.content")) {
		part.Input = a.family("gen_ai.prompt")
	}
	if part.Output == nil && (a.has("gen_ai.completion.0.role") || a.has("gen_ai.completion.0.content")) {
		part.Output = a.family("gen_ai.completion")
	}
	// Older GenAI instrumentation sends messages as span events.
	var prompts, choices []any
	for _, event := range events {
		switch {
		case event.Name == "gen_ai.choice":
			choices = append(choices, event.Attributes)
		case strings.HasPrefix(event.Name, "gen_ai.") && strings.HasSuffix(event.Name, ".message"):
			message := map[string]any{"role": strings.TrimSuffix(strings.TrimPrefix(event.Name, "gen_ai."), ".message")}
			for key, value := range event.Attributes {
				message[key] = value
			}
			prompts = append(prompts, message)
		}
	}
	if part.Input == nil && len(prompts) > 0 {
		part.Input = prompts
	}
	if part.Output == nil && len(choices) > 0 {
		part.Output = choices
	}
	if part.Kind == logstore.TraceSpanKindTool {
		setIfEmpty(&part.Name, a.str("gen_ai.tool.name"))
	}
}

func mapTraceloop(part *SpanPart, a *attrs) {
	setIfEmpty(&part.Kind, KindFor(a.str("traceloop.span.kind")))
	setValueIfNil(&part.Input, a.content("traceloop.entity.input"))
	setValueIfNil(&part.Output, a.content("traceloop.entity.output"))
	setIfEmpty(&part.SessionID, a.str("traceloop.association.properties.session_id"))
	setIfEmpty(&part.UserID, a.str("traceloop.association.properties.user_id"))
}

func mapVercelAI(part *SpanPart, a *attrs) {
	operation := a.str("ai.operationId")
	if operation == "" {
		return
	}
	switch {
	case strings.HasSuffix(operation, ".doGenerate"), strings.HasSuffix(operation, ".doStream"):
		setIfEmpty(&part.Kind, logstore.TraceSpanKindLLM)
	case operation == "ai.toolCall":
		setIfEmpty(&part.Kind, logstore.TraceSpanKindTool)
		setIfEmpty(&part.Name, a.str("ai.toolCall.name"))
	case strings.HasSuffix(operation, ".doEmbed"):
		setIfEmpty(&part.Kind, logstore.TraceSpanKindEmbedding)
	default:
		// The outer ai.generateText span repeats its inner call's usage, so it is not a model call.
		setIfEmpty(&part.Kind, logstore.TraceSpanKindFunction)
	}
	setValueIfNil(&part.Input, a.content("ai.prompt.messages", "ai.prompt", "ai.toolCall.args", "ai.value", "ai.values"))
	setValueIfNil(&part.Output, a.content("ai.response.text", "ai.response.object", "ai.response.toolCalls", "ai.toolCall.result", "ai.embedding", "ai.embeddings"))
	setIfEmpty(&part.Model, a.str("ai.response.model", "ai.model.id"))
	setIfEmpty(&part.Provider, a.str("ai.model.provider"))
	setIfEmpty(&part.ProviderResponseID, a.str("ai.response.id"))
	setIntIfZero(&part.PromptTokens, a.integer("ai.usage.inputTokens", "ai.usage.promptTokens"))
	setIntIfZero(&part.CompletionTokens, a.integer("ai.usage.outputTokens", "ai.usage.completionTokens"))
	setIntIfZero(&part.CachedReadTokens, a.integer("ai.usage.cachedInputTokens"))
	setIntIfZero(&part.ReasoningTokens, a.integer("ai.usage.reasoningTokens"))
	if ms, ok := a.num("ai.response.msToFirstChunk"); ok && part.TimeToFirstToken == nil {
		part.TimeToFirstToken = &ms
	}
	if metadata, ok := a.family("ai.telemetry.metadata").(map[string]any); ok {
		part.Metadata = mergeMap(metadata, part.Metadata)
	}
}

func mapMLflow(part *SpanPart, a *attrs) {
	setIfEmpty(&part.Kind, KindFor(strings.Trim(a.str("mlflow.spanType"), `"`)))
	setValueIfNil(&part.Input, a.content("mlflow.spanInputs"))
	setValueIfNil(&part.Output, a.content("mlflow.spanOutputs"))
	if usage := a.object("mlflow.chat.tokenUsage"); usage != nil {
		u := attributes(usage)
		setIntIfZero(&part.PromptTokens, u.integer("input_tokens"))
		setIntIfZero(&part.CompletionTokens, u.integer("output_tokens"))
		setIntIfZero(&part.TotalTokens, u.integer("total_tokens"))
	}
	setIfEmpty(&part.SessionID, strings.Trim(a.str("mlflow.trace.session"), `"`))
	setIfEmpty(&part.UserID, strings.Trim(a.str("mlflow.trace.user"), `"`))
}

// mapCommon reads the attributes every convention shares and classifies what is left.
func mapCommon(part *SpanPart, a *attrs, span *OTelSpan) {
	setIfEmpty(&part.SessionID, a.str("session.id"))
	setIfEmpty(&part.UserID, a.str("user.id", "enduser.id"))
	setIfEmpty(&part.Environment, a.str("deployment.environment.name", "deployment.environment"))
	if part.Environment == "" {
		part.Environment = attributes(span.Resource).str("deployment.environment.name", "deployment.environment")
	}
	if part.Error == "" {
		for _, event := range span.Events {
			if event.Name == "exception" {
				part.Error = attributes(event.Attributes).str("exception.message", "exception.type")
				part.Status = logstore.TraceSpanStatusError
				break
			}
		}
	}
	if part.Kind == "" {
		switch {
		case a.has("tool.name"):
			part.Kind = logstore.TraceSpanKindTool
		case a.has("http.request.method") || a.has("http.method") || a.has("url.full"):
			part.Kind = logstore.TraceSpanKindHTTP
		}
	}
}

// appendTags adds the tags held by an attribute: a list, a JSON list, or one string.
func appendTags(tags []string, value any) []string {
	var found []any
	switch v := value.(type) {
	case []any:
		found = v
	case []string:
		for _, tag := range v {
			found = append(found, tag)
		}
	case string:
		if decoded, ok := decodeValue(v).([]any); ok {
			found = decoded
		} else if v != "" {
			found = []any{v}
		}
	}
	for _, tag := range found {
		if text, ok := tag.(string); ok && text != "" && !slices.Contains(tags, text) {
			tags = append(tags, text)
		}
	}
	return tags
}
