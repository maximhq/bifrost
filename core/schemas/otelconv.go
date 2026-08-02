package schemas

// OTelOperationNameExecuteTool is the gen_ai.operation.name value for MCP tool
// executions. execute_tool is an MCPRequestType, not a Bifrost RequestType, so it
// can't flow through OTelOperationName.
const OTelOperationNameExecuteTool = "execute_tool"

// OTelOperationName maps a Bifrost RequestType to the value that should be
// emitted under gen_ai.operation.name. Values not modeled by the spec fall
// through to the raw RequestType string.
func OTelOperationName(rt RequestType) string {
	switch rt {
	case ChatCompletionRequest, ChatCompletionStreamRequest,
		ResponsesRequest, ResponsesStreamRequest:
		return "chat"
	case TextCompletionRequest, TextCompletionStreamRequest:
		return "text_completion"
	case EmbeddingRequest:
		return "embeddings"
	case SpeechRequest, SpeechStreamRequest,
		TranscriptionRequest, TranscriptionStreamRequest,
		ImageGenerationRequest, ImageGenerationStreamRequest,
		ImageEditRequest, ImageEditStreamRequest:
		return "generate_content"
	default:
		return string(rt)
	}
}

// OTelProviderName maps a Bifrost ModelProvider to the value that should be
// emitted under gen_ai.provider.name. Providers not covered by the spec keep
// their Bifrost short name.
func OTelProviderName(p ModelProvider) string {
	switch p {
	case Bedrock:
		return "aws.bedrock"
	case Vertex:
		return "gcp.vertex_ai"
	case Gemini:
		return "gcp.gemini"
	case XAI:
		return "x_ai"
	case Mistral:
		return "mistral_ai"
	case Azure:
		return "azure.ai.openai"
	default:
		return string(p)
	}
}
