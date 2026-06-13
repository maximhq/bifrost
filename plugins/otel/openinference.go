package otel

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	openInferenceSpanKind = "openinference.span.kind"
	oiInputValue          = "input.value"
	oiInputMIMEType       = "input.mime_type"
	oiOutputValue         = "output.value"
	oiOutputMIMEType      = "output.mime_type"
)

// convertSpanToOpenInferenceAttributes maps Bifrost's canonical attributes to a clean
// OpenInference attribute set. The source attributes are used only as conversion input
// and are not exported by OpenInference profiles.
func convertSpanToOpenInferenceAttributes(trace *schemas.Trace, span *schemas.Span, disableContentLogging bool) []*KeyValue {
	attrs := span.Attributes
	kind := openInferenceKind(span.Kind)
	result := []*KeyValue{kvStr(openInferenceSpanKind, kind)}

	if sessionID, ok := trace.GetAttribute(schemas.TraceAttrSessionID); ok {
		if kv := anyToKeyValue("session.id", sessionID); kv != nil {
			result = append(result, kv)
		}
	}

	result = appendMappedAttribute(result, attrs, "user.id", schemas.AttrRequestUser)
	result = appendMappedAttribute(result, attrs, "metadata", schemas.AttrRespMetadata)

	switch kind {
	case "LLM":
		provider, system := openInferenceProviderAndSystem(attrs)
		if provider != "" {
			result = append(result, kvStr("llm.provider", provider))
		}
		if system != "" {
			result = append(result, kvStr("llm.system", system))
		}
		result = appendMappedAttribute(result, attrs, "llm.model_name", schemas.AttrResponseModel, schemas.AttrRequestModel)
		result = appendOpenInferenceTokenAttributes(result, attrs)
		result = appendMappedAttribute(result, attrs, "llm.cost.total", schemas.AttrUsageCost)
		result = appendMappedAttribute(result, attrs, "llm.finish_reason", schemas.AttrFinishReason)
		if invocation := openInferenceInvocationParameters(attrs); invocation != "" {
			result = append(result, kvStr("llm.invocation_parameters", invocation))
		}
	case "EMBEDDING":
		result = appendMappedAttribute(result, attrs, "embedding.model_name", schemas.AttrResponseModel, schemas.AttrRequestModel)
		result = appendOpenInferenceTokenAttributes(result, attrs)
		if invocation := openInferenceInvocationParameters(attrs); invocation != "" {
			result = append(result, kvStr("embedding.invocation_parameters", invocation))
		}
	case "TOOL":
		result = appendOpenInferenceToolIdentity(result, attrs)
	}

	if disableContentLogging {
		return result
	}

	return appendOpenInferenceContent(result, attrs)
}

func appendOpenInferenceToolIdentity(result []*KeyValue, attrs map[string]any) []*KeyValue {
	result = appendMappedAttribute(result, attrs, "tool.name", schemas.AttrToolName)
	result = appendMappedAttribute(result, attrs, "tool_call.function.name", schemas.AttrToolName)
	result = appendMappedAttribute(result, attrs, "tool.id", schemas.AttrToolCallID)
	return appendMappedAttribute(result, attrs, "tool_call.id", schemas.AttrToolCallID)
}

func appendOpenInferenceTokenAttributes(result []*KeyValue, attrs map[string]any) []*KeyValue {
	result = appendMappedAttribute(result, attrs, "llm.token_count.prompt", schemas.AttrInputTokens, schemas.AttrPromptTokens)
	result = appendMappedAttribute(result, attrs, "llm.token_count.completion", schemas.AttrOutputTokens, schemas.AttrCompletionTokens)
	result = appendMappedAttribute(result, attrs, "llm.token_count.total", schemas.AttrTotalTokens)
	result = appendMappedAttribute(result, attrs, "llm.token_count.prompt_details.cache_read", schemas.AttrUsageCacheReadInputTokens, schemas.AttrPromptTokenDetailsCachedRead)
	result = appendMappedAttribute(result, attrs, "llm.token_count.prompt_details.cache_write", schemas.AttrUsageCacheCreationInputTokens, schemas.AttrPromptTokenDetailsCachedWrite)
	result = appendMappedAttribute(result, attrs, "llm.token_count.completion_details.reasoning", schemas.AttrUsageReasoningOutputTokens, schemas.AttrCompletionTokenDetailsReason, schemas.AttrOutputTokenDetailsReason)
	return result
}

func openInferenceProviderAndSystem(attrs map[string]any) (provider, system string) {
	value, ok := firstAttribute(attrs, schemas.AttrBifrostProviderName, schemas.AttrProviderName)
	if !ok {
		return "", ""
	}
	raw := fmt.Sprint(value)
	switch raw {
	case "bedrock", "aws.bedrock":
		return "aws", "amazon"
	case "vertex", "gcp.vertex_ai":
		return "google", "vertexai"
	case "gemini", "gcp.gemini":
		return "google", "google"
	case "azure", "azure.ai.openai":
		return "azure", "openai"
	case "mistral", "mistral_ai":
		return "mistralai", "mistralai"
	case "xai", "x_ai":
		return "xai", "xai"
	default:
		return raw, raw
	}
}

func openInferenceKind(kind schemas.SpanKind) string {
	switch kind {
	case schemas.SpanKindLLMCall, schemas.SpanKindSpeech, schemas.SpanKindTranscription:
		return "LLM"
	case schemas.SpanKindEmbedding:
		return "EMBEDDING"
	case schemas.SpanKindMCPTool:
		return "TOOL"
	default:
		return "CHAIN"
	}
}

func appendMappedAttribute(result []*KeyValue, attrs map[string]any, target string, sources ...string) []*KeyValue {
	value, ok := firstAttribute(attrs, sources...)
	if !ok {
		return result
	}
	if target == "llm.finish_reason" {
		if values, ok := value.([]string); ok && len(values) > 0 {
			value = values[0]
		}
	}
	if kv := anyToKeyValue(target, value); kv != nil {
		result = append(result, kv)
	}
	return result
}

func firstAttribute(attrs map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := attrs[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func openInferenceInvocationParameters(attrs map[string]any) string {
	params := make(map[string]any)
	for key, value := range attrs {
		if !strings.HasPrefix(key, "gen_ai.request.") {
			continue
		}
		name := strings.TrimPrefix(key, "gen_ai.request.")
		switch name {
		case "prompt", "tools", "user", "message_count", "instructions":
			continue
		}
		params[name] = value
	}
	if len(params) == 0 {
		return ""
	}
	data, err := schemas.MarshalSorted(params)
	if err != nil {
		return ""
	}
	return string(data)
}

func appendOpenInferenceContent(result []*KeyValue, attrs map[string]any) []*KeyValue {
	if value, ok := firstAttribute(attrs, schemas.AttrInputMessages); ok {
		result = appendJSONContent(result, "llm.input_messages", oiInputValue, oiInputMIMEType, value)
	} else if value, ok := firstAttribute(attrs, schemas.AttrInputText, schemas.AttrInputSpeech, schemas.AttrInputEmbedding); ok {
		result = appendTextContent(result, "llm.prompts.0.prompt.text", oiInputValue, oiInputMIMEType, value)
	}

	if value, ok := firstAttribute(attrs, schemas.AttrOutputMessages); ok {
		if choices, ok := value.([]string); ok {
			for i, choice := range choices {
				result = append(result, kvStr(fmt.Sprintf("llm.choices.%d.completion.text", i), choice))
			}
			result = appendTextContent(result, "", oiOutputValue, oiOutputMIMEType, value)
		} else {
			result = appendJSONContent(result, "llm.output_messages", oiOutputValue, oiOutputMIMEType, value)
		}
	}

	if value, ok := firstAttribute(attrs, schemas.AttrTools); ok {
		result = appendOpenInferenceTools(result, value)
	}

	if value, ok := firstAttribute(attrs, schemas.AttrToolCallArguments); ok {
		result = appendTextContent(result, "tool_call.function.arguments", oiInputValue, oiInputMIMEType, value)
	}
	if value, ok := firstAttribute(attrs, schemas.AttrToolCallResult); ok {
		result = appendTextContent(result, "", oiOutputValue, oiOutputMIMEType, value)
	}
	return result
}

func appendJSONContent(result []*KeyValue, prefix, valueKey, mimeKey string, value any) []*KeyValue {
	raw := openInferenceContentValue(value)
	var items []map[string]any
	if err := schemas.Unmarshal([]byte(raw), &items); err == nil {
		for i, item := range items {
			result = appendOpenInferenceMessage(result, fmt.Sprintf("%s.%d", prefix, i), item)
		}
		result = append(result, kvStr(valueKey, raw), kvStr(mimeKey, "application/json"))
		return result
	}
	return appendTextContent(result, "", valueKey, mimeKey, value)
}

func appendTextContent(result []*KeyValue, semanticKey, valueKey, mimeKey string, value any) []*KeyValue {
	raw := openInferenceContentValue(value)
	if raw == "" {
		return result
	}
	if semanticKey != "" {
		result = append(result, kvStr(semanticKey, raw))
	}
	result = append(result, kvStr(valueKey, raw), kvStr(mimeKey, contentMIMEType(raw)))
	return result
}

func openInferenceContentValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	data, err := schemas.MarshalSorted(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func appendOpenInferenceMessage(result []*KeyValue, prefix string, message map[string]any) []*KeyValue {
	result = appendMappedAttribute(result, message, prefix+".message.role", "role")
	if content, ok := message["content"].([]any); ok {
		for i, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			blockPrefix := fmt.Sprintf("%s.message.contents.%d.message_content", prefix, i)
			result = appendMappedAttribute(result, block, blockPrefix+".type", "type")
			result = appendMappedAttribute(result, block, blockPrefix+".text", "text")
			if image, ok := block["image_url"].(map[string]any); ok {
				result = appendMappedAttribute(result, image, blockPrefix+".image.image.url", "url")
			}
		}
	} else {
		result = appendMappedAttribute(result, message, prefix+".message.content", "content")
	}
	result = appendMappedAttribute(result, message, prefix+".message.name", "name")
	result = appendMappedAttribute(result, message, prefix+".message.tool_call_id", "tool_call_id")

	if calls, ok := message["tool_calls"].([]any); ok {
		for i, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			callPrefix := fmt.Sprintf("%s.message.tool_calls.%d.tool_call", prefix, i)
			result = appendMappedAttribute(result, call, callPrefix+".id", "id")
			result = appendMappedAttribute(result, call, callPrefix+".function.name", "name")
			result = appendMappedAttribute(result, call, callPrefix+".function.arguments", "args")
		}
	}
	return result
}

func appendOpenInferenceTools(result []*KeyValue, value any) []*KeyValue {
	raw := openInferenceContentValue(value)
	var tools []any
	if err := schemas.Unmarshal([]byte(raw), &tools); err != nil {
		return result
	}
	for i, tool := range tools {
		data, err := schemas.MarshalSorted(tool)
		if err == nil {
			result = append(result, kvStr(fmt.Sprintf("llm.tools.%d.tool.json_schema", i), string(data)))
		}
	}
	return result
}

func contentMIMEType(value string) string {
	var parsed any
	if schemas.Unmarshal([]byte(value), &parsed) == nil {
		return "application/json"
	}
	return "text/plain"
}
