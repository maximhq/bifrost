package gigachat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	gigaChatResponsesRoleReasoning          = "reasoning"
	gigaChatResponsesGeneratedCallIDPrefix  = "gigachat_call_"
	gigaChatResponsesGeneratedCallIDVersion = "v1"
)

// ToGigaChatResponsesRequest converts a Bifrost Responses request to GigaChat v2 chat completions format.
func ToGigaChatResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) (*GigaChatResponsesRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost responses request is nil")
	}
	if strings.TrimSpace(bifrostReq.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}

	messages := make([]GigaChatResponsesMessage, 0, len(bifrostReq.Input)+1)
	if bifrostReq.Params != nil && bifrostReq.Params.Instructions != nil && strings.TrimSpace(*bifrostReq.Params.Instructions) != "" {
		messages = append(messages, GigaChatResponsesMessage{
			Role: string(schemas.ResponsesInputMessageRoleSystem),
			Content: []GigaChatResponsesContentPart{{
				Text: schemas.Ptr(*bifrostReq.Params.Instructions),
			}},
		})
	}

	functionCallNamesByID := collectGigaChatResponsesFunctionCallNames(bifrostReq.Input)
	for index, message := range bifrostReq.Input {
		convertedMessages, err := toGigaChatResponsesMessages(message, functionCallNamesByID)
		if err != nil {
			return nil, fmt.Errorf("input[%d]: %w", index, err)
		}
		messages = append(messages, convertedMessages...)
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages are required")
	}

	gigaChatReq := &GigaChatResponsesRequest{
		Model:    bifrostReq.Model,
		Messages: messages,
	}
	params := bifrostReq.Params
	if params == nil {
		params = &schemas.ResponsesParameters{}
	}

	if unsupportedParams := unsupportedGigaChatResponsesParams(params); len(unsupportedParams) > 0 {
		return nil, fmt.Errorf("GigaChat Responses do not support parameter(s): %s", strings.Join(unsupportedParams, ", "))
	}
	if err := applyGigaChatResponsesParams(gigaChatReq, params); err != nil {
		return nil, err
	}
	if hasGigaChatResponsesThreadID(params) {
		gigaChatReq.Model = ""
	}
	return gigaChatReq, nil
}

// ToGigaChatResponsesStreamRequest converts a Bifrost Responses request to a streaming GigaChat v2 request.
func ToGigaChatResponsesStreamRequest(bifrostReq *schemas.BifrostResponsesRequest) (*GigaChatResponsesRequest, error) {
	gigaChatReq, err := ToGigaChatResponsesRequest(bifrostReq)
	if err != nil {
		return nil, err
	}
	gigaChatReq.Stream = schemas.Ptr(true)
	return gigaChatReq, nil
}

// ToBifrostResponsesResponse converts a GigaChat v2 chat completions response to Bifrost Responses format.
func ToBifrostResponsesResponse(providerName schemas.ModelProvider, response *GigaChatResponsesResponse) *schemas.BifrostResponsesResponse {
	if response == nil {
		return nil
	}

	outputCapacity := len(response.Messages)
	if outputCapacity == 0 {
		outputCapacity = len(response.Choices)
	}
	output := make([]schemas.ResponsesMessage, 0, outputCapacity)
	var status *string
	var incompleteDetails *schemas.ResponsesResponseIncompleteDetails
	var stopReason *string
	functionCallIDs := newGigaChatResponsesCallIDTracker()
	applyFinishReason := func(finishReason *string) bool {
		if mappedStatus, mappedIncompleteDetails, mappedStopReason := toBifrostGigaChatResponsesStatus(finishReason); mappedStatus != nil {
			status = mappedStatus
			incompleteDetails = mappedIncompleteDetails
			stopReason = mappedStopReason
			return *mappedStatus == "incomplete"
		}
		return false
	}

	if len(response.Messages) > 0 {
		for _, message := range response.Messages {
			output = append(output, toBifrostGigaChatResponsesMessageOutput(message, response.MessageID, response.ToolsStateID, functionCallIDs)...)

			finishReason := message.FinishReason
			if finishReason == nil {
				finishReason = response.FinishReason
			}
			if applyFinishReason(finishReason) {
				break
			}
		}
	} else {
		for _, choice := range response.Choices {
			output = append(output, toBifrostGigaChatResponsesChoiceOutput(choice, response.MessageID, response.ToolsStateID, functionCallIDs)...)

			finishReason := choice.FinishReason
			if finishReason == nil && choice.Message != nil {
				finishReason = choice.Message.FinishReason
			}
			if applyFinishReason(finishReason) {
				break
			}
		}
	}
	if status == nil {
		applyFinishReason(response.FinishReason)
	}

	createdAt := response.CreatedAt
	if createdAt == 0 {
		createdAt = response.Created
	}
	responseID := toBifrostGigaChatResponsesResponseID(response)

	bifrostResponse := &schemas.BifrostResponsesResponse{
		Object:            "response",
		CreatedAt:         createdAt,
		Conversation:      toBifrostGigaChatResponsesConversation(response.ThreadID),
		Model:             response.Model,
		Output:            output,
		Status:            status,
		IncompleteDetails: incompleteDetails,
		StopReason:        stopReason,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
		},
	}
	if strings.TrimSpace(responseID) != "" {
		bifrostResponse.ID = &responseID
	}
	if usage := toBifrostGigaChatUsage(response.Usage); usage != nil {
		bifrostResponse.Usage = usage.ToResponsesResponseUsage()
	}
	bifrostResponse.ProviderExtraFields = toBifrostGigaChatResponsesProviderExtraFields(response)

	return bifrostResponse
}

// ToBifrostResponsesStreamResponse converts a GigaChat v2 SSE chunk to Bifrost Responses stream events.
func ToBifrostResponsesStreamResponse(providerName schemas.ModelProvider, response *GigaChatResponsesResponse, state *schemas.ChatToResponsesStreamState) []*schemas.BifrostResponsesStreamResponse {
	if response == nil || state == nil {
		return nil
	}

	if response.CreatedAt != 0 && !state.HasEmittedCreated {
		state.CreatedAt = response.CreatedAt
	} else if response.Created != 0 && !state.HasEmittedCreated {
		state.CreatedAt = response.Created
	}

	chatResponse := toBifrostGigaChatResponsesChatStreamResponse(providerName, response)
	if chatResponse == nil {
		return nil
	}
	ensureGigaChatResponsesStreamLifecycleRole(chatResponse, state)

	events := chatResponse.ToBifrostResponsesStreamResponse(state)
	conversation := toBifrostGigaChatResponsesConversation(response.ThreadID)
	for _, event := range events {
		if event == nil {
			continue
		}
		applyGigaChatResponsesStreamExtraFields(event, providerName)
		if event.Response != nil && event.Response.Conversation == nil {
			event.Response.Conversation = conversation
		}
	}
	return events
}

func applyGigaChatResponsesStreamExtraFields(event *schemas.BifrostResponsesStreamResponse, providerName schemas.ModelProvider) {
	if event == nil {
		return
	}
	event.ExtraFields.Provider = providerName
	event.ExtraFields.RequestType = schemas.ResponsesStreamRequest
	if event.Response == nil {
		return
	}
	event.Response.ExtraFields.Provider = providerName
	event.Response.ExtraFields.RequestType = schemas.ResponsesStreamRequest
}

func toBifrostGigaChatResponsesResponseID(response *GigaChatResponsesResponse) string {
	if response == nil {
		return ""
	}
	if responseID := strings.TrimSpace(response.ID); responseID != "" {
		return responseID
	}
	if response.ThreadID != nil {
		if threadID := strings.TrimSpace(*response.ThreadID); threadID != "" {
			return threadID
		}
	}
	if response.MessageID != nil {
		return strings.TrimSpace(*response.MessageID)
	}
	return ""
}

func toBifrostGigaChatResponsesConversation(threadID *string) *schemas.ResponsesResponseConversation {
	if threadID == nil || strings.TrimSpace(*threadID) == "" {
		return nil
	}
	return &schemas.ResponsesResponseConversation{
		ResponsesResponseConversationStruct: &schemas.ResponsesResponseConversationStruct{
			ID: strings.TrimSpace(*threadID),
		},
	}
}

func toBifrostGigaChatResponsesChatStreamResponse(providerName schemas.ModelProvider, response *GigaChatResponsesResponse) *schemas.BifrostChatResponse {
	if response == nil {
		return nil
	}

	createdAt := response.CreatedAt
	if createdAt == 0 {
		createdAt = response.Created
	}
	responseID := toBifrostGigaChatResponsesResponseID(response)

	streamResponse := &GigaChatChatStreamResponse{
		ID:                responseID,
		Created:           createdAt,
		Model:             response.Model,
		Object:            response.Object,
		SystemFingerprint: response.SystemFingerprint,
		Usage:             response.Usage,
		ExtraParams:       response.ExtraParams,
	}

	if len(response.Messages) > 0 {
		streamResponse.Choices = make([]GigaChatChatStreamChoice, 0, len(response.Messages))
		for index, message := range response.Messages {
			finishReason := message.FinishReason
			if finishReason == nil {
				finishReason = response.FinishReason
			}
			streamResponse.Choices = append(streamResponse.Choices, GigaChatChatStreamChoice{
				Index:        index,
				Delta:        toGigaChatResponsesMessageStreamDelta(&message, response.ToolsStateID),
				FinishReason: finishReason,
			})
		}
		return ToBifrostChatStreamResponse(providerName, streamResponse)
	}

	if len(response.Choices) > 0 {
		streamResponse.Choices = make([]GigaChatChatStreamChoice, 0, len(response.Choices))
		for _, choice := range response.Choices {
			delta := choice.Delta
			if delta == nil && choice.Message != nil {
				delta = toGigaChatResponsesMessageStreamDelta(choice.Message, response.ToolsStateID)
			}
			streamResponse.Choices = append(streamResponse.Choices, GigaChatChatStreamChoice{
				Index:        choice.Index,
				Delta:        delta,
				FinishReason: choice.FinishReason,
				LogProbs:     choice.LogProbs,
			})
		}
		return ToBifrostChatStreamResponse(providerName, streamResponse)
	}

	if response.FinishReason != nil {
		streamResponse.Choices = []GigaChatChatStreamChoice{{
			Index:        0,
			Delta:        &GigaChatChatStreamDelta{},
			FinishReason: response.FinishReason,
		}}
		return ToBifrostChatStreamResponse(providerName, streamResponse)
	}

	return nil
}

func toGigaChatResponsesMessageStreamDelta(message *GigaChatResponsesMessage, fallbackToolsStateID *string) *GigaChatChatStreamDelta {
	if message == nil {
		return &GigaChatChatStreamDelta{}
	}

	delta := &GigaChatChatStreamDelta{}
	if strings.TrimSpace(message.Role) != "" {
		role := message.Role
		if isGigaChatResponsesReasoningRole(role) {
			role = string(schemas.ChatMessageRoleAssistant)
		}
		delta.Role = &role
	}

	var textBuilder strings.Builder
	var functionCall *GigaChatResponsesFunctionCall
	for index := range message.Content {
		part := message.Content[index]
		if part.Text != nil {
			textBuilder.WriteString(*part.Text)
		}
		if functionCall == nil && part.FunctionCall != nil {
			functionCall = part.FunctionCall
		}
	}
	if text := textBuilder.String(); text != "" {
		if isGigaChatResponsesReasoningRole(message.Role) {
			delta.Reasoning = &text
		} else {
			delta.Content = &text
		}
	}
	if functionCall == nil {
		functionCall = message.FunctionCall
	}
	if functionCall != nil {
		delta.FunctionCall = toGigaChatLegacyFunctionCall(functionCall)
		delta.FunctionsStateID = toGigaChatResponsesMessageToolStateID(*message)
		if delta.FunctionsStateID == nil {
			delta.FunctionsStateID = fallbackToolsStateID
		}
	}

	return delta
}

func toGigaChatLegacyFunctionCall(functionCall *GigaChatResponsesFunctionCall) *GigaChatFunctionCall {
	if functionCall == nil {
		return nil
	}
	arguments := json.RawMessage(stringifyGigaChatResponsesPayload(functionCall.Arguments))
	return &GigaChatFunctionCall{
		Name:      toBifrostGigaChatResponsesFunctionName(functionCall.Name),
		Arguments: arguments,
	}
}

func ensureGigaChatResponsesStreamLifecycleRole(response *schemas.BifrostChatResponse, state *schemas.ChatToResponsesStreamState) {
	if response == nil || state == nil || state.HasEmittedCreated || len(response.Choices) == 0 {
		return
	}
	choice := response.Choices[0]
	if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
		return
	}
	delta := choice.ChatStreamResponseChoice.Delta
	if delta.Role != nil {
		return
	}
	hasContent := delta.Content != nil && *delta.Content != ""
	if hasContent || len(delta.ToolCalls) > 0 {
		role := string(schemas.ChatMessageRoleAssistant)
		delta.Role = &role
	}
}

func updateGigaChatResponsesStreamUsage(target *schemas.BifrostLLMUsage, source *schemas.BifrostLLMUsage) {
	if target == nil || source == nil {
		return
	}
	if source.PromptTokens > target.PromptTokens {
		target.PromptTokens = source.PromptTokens
	}
	if source.CompletionTokens > target.CompletionTokens {
		target.CompletionTokens = source.CompletionTokens
	}
	if source.TotalTokens > target.TotalTokens {
		target.TotalTokens = source.TotalTokens
	}
	if calculatedTotal := target.PromptTokens + target.CompletionTokens; calculatedTotal > target.TotalTokens {
		target.TotalTokens = calculatedTotal
	}
	if source.PromptTokensDetails != nil {
		target.PromptTokensDetails = source.PromptTokensDetails
	}
	if source.CompletionTokensDetails != nil {
		target.CompletionTokensDetails = source.CompletionTokensDetails
	}
	if source.Cost != nil {
		target.Cost = source.Cost
	}
}

func toBifrostGigaChatResponsesChoiceOutput(choice GigaChatResponsesChoice, fallbackMessageID *string, fallbackToolsStateID *string, functionCallIDs *gigaChatResponsesCallIDTracker) []schemas.ResponsesMessage {
	if choice.Message == nil {
		return nil
	}

	return toBifrostGigaChatResponsesMessageOutput(*choice.Message, fallbackMessageID, fallbackToolsStateID, functionCallIDs)
}

func toBifrostGigaChatResponsesMessageOutput(message GigaChatResponsesMessage, fallbackMessageID *string, fallbackToolsStateID *string, functionCallIDs *gigaChatResponsesCallIDTracker) []schemas.ResponsesMessage {
	messageID := message.MessageID
	if messageID == nil || strings.TrimSpace(*messageID) == "" {
		messageID = fallbackMessageID
	}
	if isGigaChatResponsesReasoningRole(message.Role) {
		return toBifrostGigaChatResponsesReasoningOutput(message, messageID)
	}

	toolsStateID := toGigaChatResponsesMessageToolStateID(message)
	if toolsStateID == nil || strings.TrimSpace(*toolsStateID) == "" {
		toolsStateID = fallbackToolsStateID
	}
	output := make([]schemas.ResponsesMessage, 0, len(message.Content)+1)
	contentBlocks := make([]schemas.ResponsesMessageContentBlock, 0, len(message.Content))
	hasFunctionCall := false
	for index, part := range message.Content {
		sourceRefs := toBifrostGigaChatResponsesInlineSources(part.InlineData)
		if part.Text != nil {
			annotations := toBifrostGigaChatResponsesSourceAnnotations(*part.Text, sourceRefs)
			if annotations == nil {
				annotations = []schemas.ResponsesOutputMessageContentTextAnnotation{}
			}
			contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: part.Text,
				ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
					Annotations: annotations,
					LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
				},
			})
		}
		if webSearchCall := toBifrostGigaChatResponsesWebSearchCall(messageID, index, sourceRefs); webSearchCall != nil {
			output = append(output, *webSearchCall)
		}
		for fileIndex, file := range part.Files {
			if imageCall := toBifrostGigaChatResponsesImageGenerationCall(messageID, index, fileIndex, file); imageCall != nil {
				output = append(output, *imageCall)
			}
		}
		if part.FunctionCall != nil {
			hasFunctionCall = true
			if toolCall := toBifrostGigaChatResponsesFunctionCall(messageID, toolsStateID, index, functionCallIDs, part.FunctionCall); toolCall != nil {
				output = append(output, *toolCall)
			}
		}
		if part.FunctionResult != nil {
			if toolResult := toBifrostGigaChatResponsesFunctionResult(messageID, toolsStateID, index, functionCallIDs, part.FunctionResult); toolResult != nil {
				output = append(output, *toolResult)
			}
		}
	}
	if len(contentBlocks) > 0 {
		messageType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		if strings.TrimSpace(message.Role) != "" {
			role = schemas.ResponsesMessageRoleType(message.Role)
		}
		output = append([]schemas.ResponsesMessage{{
			ID:     messageID,
			Type:   &messageType,
			Role:   &role,
			Status: schemas.Ptr("completed"),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: contentBlocks,
			},
		}}, output...)
	}
	if !hasFunctionCall && message.FunctionCall != nil {
		if toolCall := toBifrostGigaChatResponsesFunctionCall(messageID, toolsStateID, 0, functionCallIDs, message.FunctionCall); toolCall != nil {
			output = append(output, *toolCall)
		}
	}
	return output
}

type gigaChatResponsesInlineSource struct {
	Key      string
	Order    int
	HasOrder bool
	URL      string
	Title    string
}

func toBifrostGigaChatResponsesWebSearchCall(messageID *string, partIndex int, sources []gigaChatResponsesInlineSource) *schemas.ResponsesMessage {
	if len(sources) == 0 {
		return nil
	}

	actionSources := make([]schemas.ResponsesWebSearchToolCallActionSearchSource, 0, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		actionSource := schemas.ResponsesWebSearchToolCallActionSearchSource{
			Type: "url",
			URL:  source.URL,
		}
		if strings.TrimSpace(source.Title) != "" {
			actionSource.Title = schemas.Ptr(source.Title)
		}
		actionSources = append(actionSources, actionSource)
	}
	if len(actionSources) == 0 {
		return nil
	}

	itemID := toBifrostGigaChatResponsesFileItemID("ws", messageID, partIndex, 0)
	messageType := schemas.ResponsesMessageTypeWebSearchCall

	return &schemas.ResponsesMessage{
		ID:     &itemID,
		Type:   &messageType,
		Status: schemas.Ptr("completed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			Action: &schemas.ResponsesToolMessageActionStruct{
				ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
					Type:    "search",
					Sources: actionSources,
				},
			},
		},
	}
}

func toBifrostGigaChatResponsesSourceAnnotations(text string, sources []gigaChatResponsesInlineSource) []schemas.ResponsesOutputMessageContentTextAnnotation {
	if len(sources) == 0 {
		return nil
	}

	citedSources, startIndex, endIndex := toBifrostGigaChatResponsesCitedSources(text)
	useAllSources := len(citedSources) == 0
	if useAllSources && text != "" {
		startIndex = schemas.Ptr(0)
		endIndex = schemas.Ptr(len(text))
	}

	annotations := make([]schemas.ResponsesOutputMessageContentTextAnnotation, 0, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		if !useAllSources {
			if _, ok := citedSources[source.Key]; !ok {
				continue
			}
		}

		annotation := schemas.ResponsesOutputMessageContentTextAnnotation{
			Type:       "url_citation",
			URL:        schemas.Ptr(source.URL),
			StartIndex: startIndex,
			EndIndex:   endIndex,
		}
		if strings.TrimSpace(source.Title) != "" {
			annotation.Title = schemas.Ptr(source.Title)
		}
		annotations = append(annotations, annotation)
	}
	if len(annotations) == 0 {
		return nil
	}
	return annotations
}

func toBifrostGigaChatResponsesCitedSources(text string) (map[string]struct{}, *int, *int) {
	const markerPrefix = "[sources=["

	start := strings.LastIndex(text, markerPrefix)
	if start < 0 {
		return nil, nil, nil
	}

	sourceListStart := start + len(markerPrefix)
	remainder := text[sourceListStart:]
	sourceListEnd := strings.Index(remainder, "]]")
	markerSuffixLen := 2
	if sourceListEnd < 0 {
		sourceListEnd = strings.Index(remainder, "]")
		markerSuffixLen = 1
	}
	if sourceListEnd < 0 {
		return nil, nil, nil
	}

	citedSources := make(map[string]struct{})
	for _, rawSource := range strings.Split(remainder[:sourceListEnd], ",") {
		sourceKey := strings.TrimSpace(rawSource)
		if sourceKey != "" {
			citedSources[sourceKey] = struct{}{}
		}
	}
	if len(citedSources) == 0 {
		return nil, nil, nil
	}

	end := sourceListStart + sourceListEnd + markerSuffixLen
	return citedSources, &start, &end
}

func toBifrostGigaChatResponsesInlineSources(inlineData map[string]interface{}) []gigaChatResponsesInlineSource {
	if len(inlineData) == 0 {
		return nil
	}

	rawSources, ok := inlineData["sources"]
	if !ok {
		return nil
	}

	sourcesMap, ok := schemas.SafeExtractOrderedMap(rawSources)
	if !ok || sourcesMap.Len() == 0 {
		return nil
	}

	sources := make([]gigaChatResponsesInlineSource, 0, sourcesMap.Len())
	sourcesMap.Range(func(key string, value interface{}) bool {
		if source, ok := toBifrostGigaChatResponsesInlineSource(key, value); ok {
			sources = append(sources, source)
		}
		return true
	})
	if len(sources) == 0 {
		return nil
	}

	sort.SliceStable(sources, func(i, j int) bool {
		left := sources[i]
		right := sources[j]
		if left.HasOrder && right.HasOrder {
			return left.Order < right.Order
		}
		if left.HasOrder != right.HasOrder {
			return left.HasOrder
		}
		return left.Key < right.Key
	})
	return sources
}

func toBifrostGigaChatResponsesInlineSource(key string, value interface{}) (gigaChatResponsesInlineSource, bool) {
	sourceMap, ok := schemas.SafeExtractOrderedMap(value)
	if !ok {
		return gigaChatResponsesInlineSource{}, false
	}

	rawURL, ok := sourceMap.Get("url")
	if !ok {
		return gigaChatResponsesInlineSource{}, false
	}
	url, ok := schemas.SafeExtractString(rawURL)
	if !ok || strings.TrimSpace(url) == "" {
		return gigaChatResponsesInlineSource{}, false
	}

	source := gigaChatResponsesInlineSource{
		Key: strings.TrimSpace(key),
		URL: strings.TrimSpace(url),
	}
	if order, err := strconv.Atoi(source.Key); err == nil {
		source.Order = order
		source.HasOrder = true
	}
	if rawTitle, ok := sourceMap.Get("title"); ok {
		if title, ok := schemas.SafeExtractString(rawTitle); ok {
			source.Title = strings.TrimSpace(title)
		}
	}
	return source, true
}

func toBifrostGigaChatResponsesImageGenerationCall(messageID *string, partIndex int, fileIndex int, file GigaChatResponsesContentFile) *schemas.ResponsesMessage {
	fileID := strings.TrimSpace(file.ID)
	if fileID == "" || !isGigaChatResponsesImageFile(file) {
		return nil
	}

	itemID := toBifrostGigaChatResponsesFileItemID("ig", messageID, partIndex, fileIndex)
	messageType := schemas.ResponsesMessageTypeImageGenerationCall

	return &schemas.ResponsesMessage{
		ID:     &itemID,
		Type:   &messageType,
		Status: schemas.Ptr("completed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			ResponsesImageGenerationCall: &schemas.ResponsesImageGenerationCall{
				Result: fileID,
			},
		},
	}
}

func isGigaChatResponsesImageFile(file GigaChatResponsesContentFile) bool {
	if file.Target != nil && strings.EqualFold(strings.TrimSpace(*file.Target), "image") {
		return true
	}
	return file.MIME != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(*file.MIME)), "image/")
}

func toBifrostGigaChatResponsesReasoningOutput(message GigaChatResponsesMessage, messageID *string) []schemas.ResponsesMessage {
	var textBuilder strings.Builder
	for _, part := range message.Content {
		if part.Text != nil {
			textBuilder.WriteString(*part.Text)
		}
	}

	reasoningText := textBuilder.String()
	if strings.TrimSpace(reasoningText) == "" {
		return nil
	}

	messageType := schemas.ResponsesMessageTypeReasoning
	role := schemas.ResponsesInputMessageRoleAssistant
	itemID := toBifrostGigaChatResponsesReasoningItemID(messageID)
	return []schemas.ResponsesMessage{{
		ID:     itemID,
		Type:   &messageType,
		Role:   &role,
		Status: schemas.Ptr("completed"),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary: []schemas.ResponsesReasoningSummary{{
				Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
				Text: reasoningText,
			}},
		},
	}}
}

func toBifrostGigaChatResponsesFunctionCall(messageID *string, toolsStateID *string, index int, functionCallIDs *gigaChatResponsesCallIDTracker, functionCall *GigaChatResponsesFunctionCall) *schemas.ResponsesMessage {
	if functionCall == nil || strings.TrimSpace(functionCall.Name) == "" {
		return nil
	}

	itemID := toBifrostGigaChatResponsesItemID("fc", messageID, index)
	callID := functionCallIDs.FunctionCallID(toolsStateID, itemID, functionCall.Name)
	arguments := stringifyGigaChatResponsesPayload(functionCall.Arguments)
	messageType := schemas.ResponsesMessageTypeFunctionCall
	role := schemas.ResponsesInputMessageRoleAssistant
	return &schemas.ResponsesMessage{
		ID:     &itemID,
		Type:   &messageType,
		Role:   &role,
		Status: schemas.Ptr("completed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    &callID,
			Name:      schemas.Ptr(toBifrostGigaChatResponsesFunctionName(functionCall.Name)),
			Arguments: &arguments,
		},
	}
}

func toBifrostGigaChatResponsesFunctionResult(messageID *string, toolsStateID *string, index int, functionCallIDs *gigaChatResponsesCallIDTracker, functionResult *GigaChatResponsesFunctionResult) *schemas.ResponsesMessage {
	if functionResult == nil || strings.TrimSpace(functionResult.Name) == "" {
		return nil
	}

	itemID := toBifrostGigaChatResponsesItemID("fr", messageID, index)
	callID := functionCallIDs.FunctionResultID(toolsStateID, itemID, functionResult.Name)
	output := stringifyGigaChatResponsesPayload(functionResult.Result)
	messageType := schemas.ResponsesMessageTypeFunctionCallOutput
	return &schemas.ResponsesMessage{
		ID:     &itemID,
		Type:   &messageType,
		Status: schemas.Ptr("completed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &callID,
			Name:   schemas.Ptr(toBifrostGigaChatResponsesFunctionName(functionResult.Name)),
			Output: &schemas.ResponsesToolMessageOutputStruct{
				ResponsesToolCallOutputStr: &output,
			},
		},
	}
}

type gigaChatResponsesCallIDTracker struct {
	counts              map[string]int
	pendingByInvocation map[string][]string
}

func newGigaChatResponsesCallIDTracker() *gigaChatResponsesCallIDTracker {
	return &gigaChatResponsesCallIDTracker{
		counts:              make(map[string]int),
		pendingByInvocation: make(map[string][]string),
	}
}

func (tracker *gigaChatResponsesCallIDTracker) FunctionCallID(toolsStateID *string, fallback string, name string) string {
	trimmed := trimStringPtr(toolsStateID)
	if trimmed == "" {
		return fallback
	}
	publicID := tracker.nextCallID(trimmed, name)
	if tracker != nil {
		key := tracker.invocationKey(trimmed, name)
		tracker.pendingByInvocation[key] = append(tracker.pendingByInvocation[key], publicID)
	}
	return publicID
}

func (tracker *gigaChatResponsesCallIDTracker) FunctionResultID(toolsStateID *string, fallback string, name string) string {
	trimmed := trimStringPtr(toolsStateID)
	if trimmed == "" {
		return fallback
	}
	if tracker != nil {
		key := tracker.invocationKey(trimmed, name)
		if pending := tracker.pendingByInvocation[key]; len(pending) > 0 {
			publicID := pending[0]
			if len(pending) == 1 {
				delete(tracker.pendingByInvocation, key)
			} else {
				tracker.pendingByInvocation[key] = pending[1:]
			}
			return publicID
		}
	}
	return tracker.nextCallID(trimmed, name)
}

func (tracker *gigaChatResponsesCallIDTracker) nextCallID(toolsStateID string, name string) string {
	ordinal := 0
	if tracker != nil {
		ordinal = tracker.counts[toolsStateID]
		tracker.counts[toolsStateID] = ordinal + 1
	}
	return newGigaChatResponsesGeneratedCallID(toolsStateID, name, ordinal)
}

func (tracker *gigaChatResponsesCallIDTracker) invocationKey(toolsStateID string, name string) string {
	return toolsStateID + "\x00" + strings.TrimSpace(name)
}

func newGigaChatResponsesGeneratedCallID(toolsStateID string, name string, ordinal int) string {
	encodedToolsStateID := base64.RawURLEncoding.EncodeToString([]byte(toolsStateID))
	encodedName := base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(name)))
	return fmt.Sprintf("%s%s.%s.%s.%d", gigaChatResponsesGeneratedCallIDPrefix, gigaChatResponsesGeneratedCallIDVersion, encodedToolsStateID, encodedName, ordinal)
}

func toGigaChatResponsesMessageToolStateID(message GigaChatResponsesMessage) *string {
	if message.ToolsStateID != nil && strings.TrimSpace(*message.ToolsStateID) != "" {
		return schemas.Ptr(strings.TrimSpace(*message.ToolsStateID))
	}
	if message.ToolStateID != nil && strings.TrimSpace(*message.ToolStateID) != "" {
		return schemas.Ptr(strings.TrimSpace(*message.ToolStateID))
	}
	return nil
}

func toBifrostGigaChatResponsesItemID(prefix string, messageID *string, index int) string {
	if messageID != nil && strings.TrimSpace(*messageID) != "" {
		if index == 0 {
			return strings.TrimSpace(*messageID)
		}
		return fmt.Sprintf("%s_%s_%d", prefix, strings.TrimSpace(*messageID), index)
	}
	return fmt.Sprintf("%s_%d", prefix, index)
}

func toBifrostGigaChatResponsesFileItemID(prefix string, messageID *string, partIndex int, fileIndex int) string {
	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix == "" {
		trimmedPrefix = "file"
	}
	if messageID != nil && strings.TrimSpace(*messageID) != "" {
		if fileIndex == 0 {
			return fmt.Sprintf("%s_%s_%d", trimmedPrefix, strings.TrimSpace(*messageID), partIndex)
		}
		return fmt.Sprintf("%s_%s_%d_%d", trimmedPrefix, strings.TrimSpace(*messageID), partIndex, fileIndex)
	}
	if fileIndex == 0 {
		return fmt.Sprintf("%s_%d", trimmedPrefix, partIndex)
	}
	return fmt.Sprintf("%s_%d_%d", trimmedPrefix, partIndex, fileIndex)
}

func toBifrostGigaChatResponsesReasoningItemID(messageID *string) *string {
	if messageID == nil || strings.TrimSpace(*messageID) == "" {
		return nil
	}
	itemID := "rs_" + strings.TrimSpace(*messageID)
	return &itemID
}

func isGigaChatResponsesReasoningRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), gigaChatResponsesRoleReasoning)
}

func stringifyGigaChatResponsesPayload(payload interface{}) string {
	if payload == nil {
		return "{}"
	}
	if text, ok := payload.(string); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return "{}"
		}
		if sonic.ValidString(trimmed) {
			return compactGigaChatResponsesJSON(trimmed)
		}
		return trimmed
	}

	raw, err := sonic.ConfigStd.Marshal(payload)
	if err != nil {
		return "{}"
	}
	if sonic.Valid(raw) {
		return compactGigaChatResponsesJSON(string(raw))
	}
	return string(raw)
}

func compactGigaChatResponsesJSON(raw string) string {
	var builder strings.Builder
	builder.Grow(len(raw))
	inString := false
	escaped := false
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		if inString {
			builder.WriteByte(character)
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
			builder.WriteByte(character)
		case ' ', '\n', '\r', '\t':
			continue
		default:
			builder.WriteByte(character)
		}
	}
	return builder.String()
}

func toBifrostGigaChatResponsesStatus(finishReason *string) (*string, *schemas.ResponsesResponseIncompleteDetails, *string) {
	mappedFinishReason := toBifrostGigaChatFinishReason(finishReason)
	if mappedFinishReason == nil || strings.TrimSpace(*mappedFinishReason) == "" {
		return nil, nil, nil
	}

	stopReason := strings.TrimSpace(*mappedFinishReason)
	switch stopReason {
	case string(schemas.BifrostFinishReasonLength):
		return schemas.Ptr("incomplete"), &schemas.ResponsesResponseIncompleteDetails{Reason: "max_output_tokens"}, &stopReason
	default:
		return schemas.Ptr("completed"), nil, &stopReason
	}
}

func toBifrostGigaChatResponsesProviderExtraFields(response *GigaChatResponsesResponse) map[string]interface{} {
	if response == nil {
		return nil
	}

	fields := make(map[string]interface{})
	if response.ThreadID != nil && strings.TrimSpace(*response.ThreadID) != "" {
		fields["thread_id"] = *response.ThreadID
	}
	if response.MessageID != nil && strings.TrimSpace(*response.MessageID) != "" {
		fields["message_id"] = *response.MessageID
	}
	if response.ToolsStateID != nil && strings.TrimSpace(*response.ToolsStateID) != "" {
		fields["tools_state_id"] = *response.ToolsStateID
	}
	if messageToolStateIDs := toBifrostGigaChatResponsesMessageToolStateIDs(response); len(messageToolStateIDs) > 0 {
		fields["message_tools_state_ids"] = messageToolStateIDs
	}
	if response.ToolExecution != nil {
		fields["tool_execution"] = response.ToolExecution
	}
	if response.AdditionalData != nil {
		fields["additional_data"] = response.AdditionalData
	}
	if strings.TrimSpace(response.SystemFingerprint) != "" {
		fields["system_fingerprint"] = response.SystemFingerprint
	}
	if len(response.ExtraParams) > 0 {
		fields["gigachat_extra"] = response.ExtraParams
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func toBifrostGigaChatResponsesMessageToolStateIDs(response *GigaChatResponsesResponse) []map[string]interface{} {
	if response == nil {
		return nil
	}

	if len(response.Messages) > 0 {
		return collectBifrostGigaChatResponsesMessageToolStateIDs(response.Messages)
	}

	if len(response.Choices) == 0 {
		return nil
	}
	messages := make([]GigaChatResponsesMessage, 0, len(response.Choices))
	for _, choice := range response.Choices {
		if choice.Message != nil {
			messages = append(messages, *choice.Message)
		}
	}
	return collectBifrostGigaChatResponsesMessageToolStateIDs(messages)
}

func collectBifrostGigaChatResponsesMessageToolStateIDs(messages []GigaChatResponsesMessage) []map[string]interface{} {
	messageToolStateIDs := make([]map[string]interface{}, 0)
	for index, message := range messages {
		toolsStateID := toGigaChatResponsesMessageToolStateID(message)
		if toolsStateID == nil || strings.TrimSpace(*toolsStateID) == "" || hasGigaChatResponsesToolPayload(message) {
			continue
		}

		entry := map[string]interface{}{
			"index":          index,
			"tools_state_id": strings.TrimSpace(*toolsStateID),
		}
		if message.MessageID != nil && strings.TrimSpace(*message.MessageID) != "" {
			entry["message_id"] = strings.TrimSpace(*message.MessageID)
		}
		if strings.TrimSpace(message.Role) != "" {
			entry["role"] = strings.TrimSpace(message.Role)
		}
		messageToolStateIDs = append(messageToolStateIDs, entry)
	}
	return messageToolStateIDs
}

func hasGigaChatResponsesToolPayload(message GigaChatResponsesMessage) bool {
	if message.FunctionCall != nil {
		return true
	}
	for _, part := range message.Content {
		if part.FunctionCall != nil || part.FunctionResult != nil {
			return true
		}
	}
	return false
}

func applyGigaChatResponsesParams(gigaChatReq *GigaChatResponsesRequest, params *schemas.ResponsesParameters) error {
	modelOptions := &GigaChatResponsesModelOptions{
		Temperature: params.Temperature,
		TopP:        params.TopP,
		MaxTokens:   params.MaxOutputTokens,
		TopLogProbs: params.TopLogProbs,
	}

	if params.Reasoning != nil && params.Reasoning.Effort != nil && strings.TrimSpace(*params.Reasoning.Effort) != "" && *params.Reasoning.Effort != "none" {
		modelOptions.Reasoning = &GigaChatResponsesReasoning{Effort: *params.Reasoning.Effort}
	}
	if params.Text != nil && params.Text.Format != nil {
		responseFormat, err := toGigaChatResponsesResponseFormat(params.Text.Format)
		if err != nil {
			return err
		}
		modelOptions.ResponseFormat = responseFormat
	}
	if hasGigaChatResponsesModelOptions(modelOptions) {
		gigaChatReq.ModelOptions = modelOptions
	}

	toolsConversion, err := toGigaChatResponsesTools(params.Tools)
	if err != nil {
		return err
	}
	gigaChatReq.Tools = toolsConversion.Tools
	if len(toolsConversion.UserInfo) > 0 {
		gigaChatReq.UserInfo = toolsConversion.UserInfo
	}

	toolConfig, err := toGigaChatResponsesToolConfig(params.ToolChoice, params.Tools)
	if err != nil {
		return err
	}
	gigaChatReq.ToolConfig = toolConfig

	if err := applyGigaChatResponsesStorage(gigaChatReq, params); err != nil {
		return err
	}

	return applyGigaChatResponsesExtraParams(gigaChatReq, params.ExtraParams)
}

func applyGigaChatResponsesStorage(gigaChatReq *GigaChatResponsesRequest, params *schemas.ResponsesParameters) error {
	if gigaChatReq == nil || params == nil {
		return nil
	}

	if params.Store != nil && !*params.Store {
		if hasGigaChatResponsesStorageParams(params) {
			return fmt.Errorf("GigaChat Responses cannot combine store=false with conversation, previous_response_id, or metadata")
		}
		gigaChatReq.Storage = false
		return nil
	}

	storage := &GigaChatResponsesStorage{}
	conversationID := trimStringPtr(params.Conversation)
	previousResponseID := trimStringPtr(params.PreviousResponseID)
	switch {
	case conversationID != "" && previousResponseID != "" && conversationID != previousResponseID:
		return fmt.Errorf("GigaChat Responses requires conversation and previous_response_id to reference the same thread_id")
	case conversationID != "":
		storage.ThreadID = &conversationID
	case previousResponseID != "":
		storage.ThreadID = &previousResponseID
	}

	if params.Metadata != nil && len(*params.Metadata) > 0 {
		storage.Metadata = make(map[string]interface{}, len(*params.Metadata))
		for key, value := range *params.Metadata {
			storage.Metadata[key] = value
		}
	}

	gigaChatReq.Storage = storage
	return nil
}

func hasGigaChatResponsesStorageParams(params *schemas.ResponsesParameters) bool {
	if params == nil {
		return false
	}
	return hasGigaChatResponsesThreadID(params) ||
		(params.Metadata != nil && len(*params.Metadata) > 0)
}

func hasGigaChatResponsesThreadID(params *schemas.ResponsesParameters) bool {
	if params == nil {
		return false
	}
	return trimStringPtr(params.Conversation) != "" ||
		trimStringPtr(params.PreviousResponseID) != ""
}

func trimStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func collectGigaChatResponsesFunctionCallNames(messages []schemas.ResponsesMessage) map[string]string {
	functionNamesByID := make(map[string]string)
	for _, message := range messages {
		if message.ResponsesToolMessage == nil || message.ResponsesToolMessage.Name == nil {
			continue
		}
		name := strings.TrimSpace(*message.ResponsesToolMessage.Name)
		if name == "" {
			continue
		}
		if message.ResponsesToolMessage.CallID != nil {
			if callID := strings.TrimSpace(*message.ResponsesToolMessage.CallID); callID != "" {
				functionNamesByID[callID] = name
				if toolsStateID := toGigaChatResponsesToolsStateIDFromCallID(callID); toolsStateID != callID && toolsStateID != "" {
					if _, exists := functionNamesByID[toolsStateID]; !exists {
						functionNamesByID[toolsStateID] = name
					}
				}
			}
		}
		if message.ID != nil {
			if id := strings.TrimSpace(*message.ID); id != "" {
				functionNamesByID[id] = name
			}
		}
	}
	return functionNamesByID
}

func toGigaChatResponsesMessages(message schemas.ResponsesMessage, functionCallNamesByID map[string]string) ([]GigaChatResponsesMessage, error) {
	messageType := schemas.ResponsesMessageTypeMessage
	if message.Type != nil {
		messageType = *message.Type
	}

	switch messageType {
	case schemas.ResponsesMessageTypeMessage:
		return toGigaChatResponsesChatMessages(message)
	case schemas.ResponsesMessageTypeFunctionCall:
		return toGigaChatResponsesFunctionCallMessage(message)
	case schemas.ResponsesMessageTypeFunctionCallOutput:
		return toGigaChatResponsesFunctionResultMessage(message, functionCallNamesByID)
	case schemas.ResponsesMessageTypeReasoning:
		return toGigaChatResponsesReasoningMessage(message)
	default:
		return nil, fmt.Errorf("item type %q is not supported by GigaChat Responses", messageType)
	}
}

func toGigaChatResponsesChatMessages(message schemas.ResponsesMessage) ([]GigaChatResponsesMessage, error) {
	role := schemas.ResponsesInputMessageRoleUser
	if message.Role != nil {
		role = *message.Role
	}
	switch role {
	case schemas.ResponsesInputMessageRoleSystem, schemas.ResponsesInputMessageRoleUser, schemas.ResponsesInputMessageRoleAssistant:
	case schemas.ResponsesInputMessageRoleDeveloper:
		return nil, fmt.Errorf("developer messages are not supported by GigaChat Responses")
	default:
		return nil, fmt.Errorf("role %q is not supported by GigaChat Responses", role)
	}

	content, err := toGigaChatResponsesContentParts(message.Content)
	if err != nil {
		return nil, err
	}
	return []GigaChatResponsesMessage{{
		Role:      string(role),
		MessageID: message.ID,
		Content:   content,
	}}, nil
}

func toGigaChatResponsesFunctionCallMessage(message schemas.ResponsesMessage) ([]GigaChatResponsesMessage, error) {
	if message.ResponsesToolMessage == nil {
		return nil, fmt.Errorf("function_call item requires tool message fields")
	}
	if message.ResponsesToolMessage.Name == nil || strings.TrimSpace(*message.ResponsesToolMessage.Name) == "" {
		return nil, fmt.Errorf("function_call item name is required")
	}

	arguments, err := parseGigaChatFunctionArguments(message.ResponsesToolMessage.Arguments)
	if err != nil {
		return nil, err
	}
	functionCall := &GigaChatResponsesFunctionCall{
		Name:      toGigaChatResponsesFunctionName(*message.ResponsesToolMessage.Name),
		Arguments: arguments,
	}
	return []GigaChatResponsesMessage{{
		Role:         string(schemas.ResponsesInputMessageRoleAssistant),
		MessageID:    message.ID,
		ToolsStateID: toGigaChatResponsesToolsStateID(message),
		Content: []GigaChatResponsesContentPart{{
			FunctionCall: functionCall,
		}},
		FunctionCall: functionCall,
	}}, nil
}

func toGigaChatResponsesFunctionResultMessage(message schemas.ResponsesMessage, functionCallNamesByID map[string]string) ([]GigaChatResponsesMessage, error) {
	if message.ResponsesToolMessage == nil {
		return nil, fmt.Errorf("function_call_output item requires tool message fields")
	}
	name := ""
	if message.ResponsesToolMessage.Name != nil {
		name = strings.TrimSpace(*message.ResponsesToolMessage.Name)
	}
	if name == "" {
		name = functionCallNamesByID[trimStringPtr(message.ResponsesToolMessage.CallID)]
	}
	if name == "" {
		name = functionCallNamesByID[trimStringPtr(message.ID)]
	}
	if name == "" {
		name = toGigaChatResponsesFunctionNameFromCallID(trimStringPtr(message.ResponsesToolMessage.CallID))
	}
	if name == "" {
		return nil, fmt.Errorf("function_call_output item name is required")
	}

	result, err := toGigaChatFunctionResultPayload(message)
	if err != nil {
		return nil, err
	}
	return []GigaChatResponsesMessage{{
		Role:         "tool",
		MessageID:    message.ID,
		ToolsStateID: toGigaChatResponsesToolsStateID(message),
		Content: []GigaChatResponsesContentPart{{
			FunctionResult: &GigaChatResponsesFunctionResult{
				Name:   toGigaChatResponsesFunctionName(name),
				Result: result,
			},
		}},
	}}, nil
}

func toGigaChatResponsesToolsStateID(message schemas.ResponsesMessage) *string {
	if message.ResponsesToolMessage != nil && message.ResponsesToolMessage.CallID != nil && strings.TrimSpace(*message.ResponsesToolMessage.CallID) != "" {
		return schemas.Ptr(toGigaChatResponsesToolsStateIDFromCallID(*message.ResponsesToolMessage.CallID))
	}
	if message.ID != nil && strings.TrimSpace(*message.ID) != "" {
		return schemas.Ptr(strings.TrimSpace(*message.ID))
	}
	return nil
}

func toGigaChatResponsesToolsStateIDFromCallID(callID string) string {
	trimmed := strings.TrimSpace(callID)
	if trimmed == "" {
		return ""
	}
	toolsStateID, _, ok := decodeGigaChatResponsesGeneratedCallID(trimmed)
	if !ok {
		return trimmed
	}
	return toolsStateID
}

func toGigaChatResponsesFunctionNameFromCallID(callID string) string {
	_, name, ok := decodeGigaChatResponsesGeneratedCallID(callID)
	if !ok {
		return ""
	}
	return name
}

func decodeGigaChatResponsesGeneratedCallID(callID string) (string, string, bool) {
	trimmed := strings.TrimSpace(callID)
	if trimmed == "" || !strings.HasPrefix(trimmed, gigaChatResponsesGeneratedCallIDPrefix) {
		return "", "", false
	}

	encoded := strings.TrimPrefix(trimmed, gigaChatResponsesGeneratedCallIDPrefix)
	parts := strings.Split(encoded, ".")
	if (len(parts) != 3 && len(parts) != 4) || parts[0] != gigaChatResponsesGeneratedCallIDVersion {
		return "", "", false
	}
	ordinalIndex := len(parts) - 1
	if _, err := strconv.Atoi(parts[ordinalIndex]); err != nil {
		return "", "", false
	}
	decodedToolsStateID, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	toolsStateID := strings.TrimSpace(string(decodedToolsStateID))
	if toolsStateID == "" {
		return "", "", false
	}
	if len(parts) == 3 {
		return toolsStateID, "", true
	}
	decodedName, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", "", false
	}
	return toolsStateID, strings.TrimSpace(string(decodedName)), true
}

func toGigaChatResponsesReasoningMessage(message schemas.ResponsesMessage) ([]GigaChatResponsesMessage, error) {
	content := make([]GigaChatResponsesContentPart, 0)
	if message.ResponsesReasoning != nil {
		for _, summary := range message.ResponsesReasoning.Summary {
			text := summary.Text
			content = append(content, GigaChatResponsesContentPart{Text: &text})
		}
	}
	if message.Content != nil {
		parts, err := toGigaChatResponsesContentParts(message.Content)
		if err != nil {
			return nil, err
		}
		content = append(content, parts...)
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("reasoning item content is required")
	}
	return []GigaChatResponsesMessage{{
		Role:      gigaChatResponsesRoleReasoning,
		MessageID: message.ID,
		Content:   content,
	}}, nil
}

func toGigaChatResponsesContentParts(content *schemas.ResponsesMessageContent) ([]GigaChatResponsesContentPart, error) {
	if content == nil {
		return nil, nil
	}
	if content.ContentStr != nil {
		return []GigaChatResponsesContentPart{{Text: content.ContentStr}}, nil
	}
	if content.ContentBlocks == nil {
		return nil, nil
	}

	parts := make([]GigaChatResponsesContentPart, 0, len(content.ContentBlocks))
	for index, block := range content.ContentBlocks {
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText,
			schemas.ResponsesOutputMessageContentTypeText,
			schemas.ResponsesOutputMessageContentTypeReasoning:
			if block.Text != nil {
				parts = append(parts, GigaChatResponsesContentPart{Text: block.Text})
			}
		case schemas.ResponsesInputMessageContentBlockTypeFile:
			file, err := toGigaChatResponsesContentFile(index, block)
			if err != nil {
				return nil, err
			}
			parts = append(parts, GigaChatResponsesContentPart{Files: []GigaChatResponsesContentFile{*file}})
		case schemas.ResponsesInputMessageContentBlockTypeImage:
			file, err := toGigaChatResponsesContentImage(index, block)
			if err != nil {
				return nil, err
			}
			parts = append(parts, GigaChatResponsesContentPart{Files: []GigaChatResponsesContentFile{*file}})
		case schemas.ResponsesInputMessageContentBlockTypeAudio:
			return nil, fmt.Errorf("content block %d: input_audio is not supported by GigaChat Responses request conversion yet", index)
		default:
			return nil, fmt.Errorf("content block %d: type %q is not supported by GigaChat Responses", index, block.Type)
		}
	}
	return parts, nil
}

func toGigaChatResponsesContentImage(index int, block schemas.ResponsesMessageContentBlock) (*GigaChatResponsesContentFile, error) {
	if block.ResponsesInputMessageContentBlockImage != nil &&
		block.ResponsesInputMessageContentBlockImage.ImageURL != nil &&
		strings.TrimSpace(*block.ResponsesInputMessageContentBlockImage.ImageURL) != "" {
		return nil, fmt.Errorf("content block %d: GigaChat Responses supports pre-uploaded file_id references for input_image; upload image_url with the Files API before calling Responses", index)
	}

	fileID := ""
	if block.FileID != nil {
		fileID = strings.TrimSpace(*block.FileID)
	}
	if fileID == "" {
		return nil, fmt.Errorf("content block %d: input_image requires file_id; upload image_url with the Files API before calling Responses", index)
	}
	return &GigaChatResponsesContentFile{ID: fileID}, nil
}

func toGigaChatResponsesContentFile(index int, block schemas.ResponsesMessageContentBlock) (*GigaChatResponsesContentFile, error) {
	if block.ResponsesInputMessageContentBlockFile != nil && (block.FileData != nil || block.FileURL != nil) {
		return nil, fmt.Errorf("content block %d: GigaChat Responses supports pre-uploaded file_id references only; upload inline file content with the Files API before calling Responses", index)
	}

	fileID := ""
	if block.FileID != nil {
		fileID = strings.TrimSpace(*block.FileID)
	}
	if fileID == "" {
		return nil, fmt.Errorf("content block %d: GigaChat file content requires file_id; upload the file with the Files API before calling Responses", index)
	}

	file := &GigaChatResponsesContentFile{ID: fileID}
	if block.ResponsesInputMessageContentBlockFile != nil && block.FileType != nil {
		if mime := strings.TrimSpace(*block.FileType); mime != "" {
			file.MIME = &mime
		}
	}
	return file, nil
}

func parseGigaChatFunctionArguments(arguments *string) (interface{}, error) {
	if arguments == nil || strings.TrimSpace(*arguments) == "" {
		return map[string]interface{}{}, nil
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(*arguments), &parsed); err != nil {
		return nil, fmt.Errorf("function_call arguments must be a JSON object: %w", err)
	}
	return parsed, nil
}

func toGigaChatFunctionResultPayload(message schemas.ResponsesMessage) (interface{}, error) {
	if message.ResponsesToolMessage != nil && message.ResponsesToolMessage.Output != nil {
		output := message.ResponsesToolMessage.Output
		if output.ResponsesToolCallOutputStr != nil {
			return *output.ResponsesToolCallOutputStr, nil
		}
		if output.ResponsesFunctionToolCallOutputBlocks != nil {
			return textFromGigaChatResponsesBlocks(output.ResponsesFunctionToolCallOutputBlocks)
		}
	}
	if message.Content != nil {
		if message.Content.ContentStr != nil {
			return *message.Content.ContentStr, nil
		}
		if message.Content.ContentBlocks != nil {
			return textFromGigaChatResponsesBlocks(message.Content.ContentBlocks)
		}
	}
	return "", nil
}

func textFromGigaChatResponsesBlocks(blocks []schemas.ResponsesMessageContentBlock) (string, error) {
	var builder strings.Builder
	for index, block := range blocks {
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText, schemas.ResponsesOutputMessageContentTypeText:
			if block.Text != nil {
				builder.WriteString(*block.Text)
			}
		default:
			return "", fmt.Errorf("function result block %d with type %q is not supported by GigaChat Responses", index, block.Type)
		}
	}
	return builder.String(), nil
}

func toGigaChatResponsesResponseFormat(format *schemas.ResponsesTextConfigFormat) (*GigaChatResponsesResponseFormat, error) {
	switch format.Type {
	case "text":
		return &GigaChatResponsesResponseFormat{Type: "text"}, nil
	case "json_schema":
		if format.JSONSchema == nil {
			return nil, fmt.Errorf("response_format json_schema requires schema")
		}
		schema, err := cloneGigaChatSchemaValue(format.JSONSchema.ToMap())
		if err != nil {
			return nil, fmt.Errorf("response_format json_schema is invalid: %w", err)
		}
		if schema == nil {
			return nil, fmt.Errorf("response_format json_schema requires non-empty schema")
		}
		schema = withGigaChatResponseFormatSchemaMetadata(schema, format.Name, format.Description)
		strict := format.Strict
		if strict == nil && format.JSONSchema.Strict != nil {
			strict = format.JSONSchema.Strict
		}
		return &GigaChatResponsesResponseFormat{
			Type:   "json_schema",
			Schema: schema,
			Strict: strict,
		}, nil
	default:
		return nil, fmt.Errorf("response_format type %q is not supported by GigaChat Responses", format.Type)
	}
}

func withGigaChatResponseFormatSchemaMetadata(schema interface{}, name *string, description *string) interface{} {
	switch schemaMap := schema.(type) {
	case map[string]interface{}:
		if name != nil && strings.TrimSpace(*name) != "" {
			if _, exists := schemaMap["title"]; !exists {
				schemaMap["title"] = strings.TrimSpace(*name)
			}
		}
		if description != nil && strings.TrimSpace(*description) != "" {
			if _, exists := schemaMap["description"]; !exists {
				schemaMap["description"] = strings.TrimSpace(*description)
			}
		}
		return schemaMap
	case *schemas.OrderedMap:
		if schemaMap == nil {
			return schema
		}
		if name != nil && strings.TrimSpace(*name) != "" {
			if _, exists := schemaMap.Get("title"); !exists {
				schemaMap.Set("title", strings.TrimSpace(*name))
			}
		}
		if description != nil && strings.TrimSpace(*description) != "" {
			if _, exists := schemaMap.Get("description"); !exists {
				schemaMap.Set("description", strings.TrimSpace(*description))
			}
		}
		return schemaMap
	case schemas.OrderedMap:
		schemaCopy := schemaMap.Clone()
		return withGigaChatResponseFormatSchemaMetadata(schemaCopy, name, description)
	default:
		return schema
	}
}

func applyGigaChatResponsesExtraParams(gigaChatReq *GigaChatResponsesRequest, extraParams map[string]interface{}) error {
	if len(extraParams) == 0 {
		return nil
	}

	remaining := make(map[string]interface{}, len(extraParams))
	for name, value := range extraParams {
		remaining[name] = value
	}

	if value, ok, err := consumeStringExtraParam(remaining, "assistant_id"); err != nil {
		return err
	} else if ok {
		gigaChatReq.AssistantID = &value
	}
	if value, ok, err := consumeStringExtraParam(remaining, "tools_state_id"); err != nil {
		return err
	} else if ok {
		gigaChatReq.ToolsStateID = &value
	}
	if value, ok, err := consumeBoolExtraParam(remaining, "disable_filter"); err != nil {
		return err
	} else if ok {
		gigaChatReq.DisableFilter = &value
	}
	if value, ok, err := consumeStringSliceExtraParam(remaining, "flags"); err != nil {
		return err
	} else if ok {
		gigaChatReq.Flags = value
	}
	if value, ok, err := consumeMapExtraParam(remaining, "filter_config"); err != nil {
		return err
	} else if ok {
		gigaChatReq.FilterConfig = value
	}
	if value, ok, err := consumeMapExtraParam(remaining, "ranker_options"); err != nil {
		return err
	} else if ok {
		gigaChatReq.RankerOptions = value
	}
	if value, ok, err := consumeMapExtraParam(remaining, "user_info"); err != nil {
		return err
	} else if ok {
		gigaChatReq.UserInfo = value
	}
	if value, ok := remaining["storage"]; ok {
		gigaChatReq.Storage = value
		delete(remaining, "storage")
	}

	modelOptions := ensureGigaChatResponsesModelOptions(gigaChatReq)
	if value, ok, err := consumeStringExtraParam(remaining, "preset"); err != nil {
		return err
	} else if ok {
		modelOptions.Preset = &value
	}
	if value, ok, err := consumeFloatExtraParam(remaining, "repetition_penalty"); err != nil {
		return err
	} else if ok {
		modelOptions.RepetitionPenalty = &value
	}
	if value, ok, err := consumeFloatExtraParam(remaining, "update_interval"); err != nil {
		return err
	} else if ok {
		modelOptions.UpdateInterval = &value
	}
	if value, ok, err := consumeBoolExtraParam(remaining, "unnormalized_history"); err != nil {
		return err
	} else if ok {
		modelOptions.UnnormalizedHistory = &value
	}
	if !hasGigaChatResponsesModelOptions(modelOptions) {
		gigaChatReq.ModelOptions = nil
	}
	if len(remaining) > 0 {
		gigaChatReq.ExtraParams = remaining
	}
	return nil
}

func unsupportedGigaChatResponsesParams(params *schemas.ResponsesParameters) []string {
	if params == nil {
		return nil
	}

	unsupported := make([]string, 0)
	addIf := func(condition bool, name string) {
		if condition {
			unsupported = append(unsupported, name)
		}
	}

	addIf(params.Background != nil, "background")
	addIf(len(params.Include) > 0, "include")
	addIf(params.MaxToolCalls != nil, "max_tool_calls")
	addIf(params.ParallelToolCalls != nil && *params.ParallelToolCalls, "parallel_tool_calls")
	addIf(params.PromptCacheKey != nil, "prompt_cache_key")
	addIf(params.SafetyIdentifier != nil, "safety_identifier")
	addIf(params.ServiceTier != nil, "service_tier")
	addIf(params.StreamOptions != nil, "stream_options")
	addIf(params.Truncation != nil, "truncation")
	addIf(params.User != nil, "user")
	if params.Reasoning != nil {
		addIf(params.Reasoning.GenerateSummary != nil, "reasoning.generate_summary")
		addIf(params.Reasoning.Summary != nil, "reasoning.summary")
		addIf(params.Reasoning.MaxTokens != nil, "reasoning.max_tokens")
	}
	if params.Text != nil {
		addIf(params.Text.Verbosity != nil, "text.verbosity")
	}
	unsupported = append(unsupported, unsupportedGigaChatToolControlExtraParams(params.ExtraParams, "functions", "function_call", "tools", "tool_config", "parallel_tool_calls")...)

	sort.Strings(unsupported)
	return unsupported
}

func ensureGigaChatResponsesModelOptions(gigaChatReq *GigaChatResponsesRequest) *GigaChatResponsesModelOptions {
	if gigaChatReq.ModelOptions == nil {
		gigaChatReq.ModelOptions = &GigaChatResponsesModelOptions{}
	}
	return gigaChatReq.ModelOptions
}

func hasGigaChatResponsesModelOptions(options *GigaChatResponsesModelOptions) bool {
	if options == nil {
		return false
	}
	return options.Preset != nil ||
		options.Temperature != nil ||
		options.TopP != nil ||
		options.MaxTokens != nil ||
		options.RepetitionPenalty != nil ||
		options.UpdateInterval != nil ||
		options.UnnormalizedHistory != nil ||
		options.TopLogProbs != nil ||
		options.Reasoning != nil ||
		options.ResponseFormat != nil ||
		len(options.ExtraParams) > 0
}

func consumeStringExtraParam(params map[string]interface{}, name string) (string, bool, error) {
	value, ok := params[name]
	if !ok {
		return "", false, nil
	}
	converted, ok := schemas.SafeExtractString(value)
	if !ok {
		return "", true, fmt.Errorf("extra parameter %q must be a string", name)
	}
	delete(params, name)
	return strings.TrimSpace(converted), true, nil
}

func consumeBoolExtraParam(params map[string]interface{}, name string) (bool, bool, error) {
	value, ok := params[name]
	if !ok {
		return false, false, nil
	}
	converted, ok := schemas.SafeExtractBool(value)
	if !ok {
		return false, true, fmt.Errorf("extra parameter %q must be a boolean", name)
	}
	delete(params, name)
	return converted, true, nil
}

func consumeFloatExtraParam(params map[string]interface{}, name string) (float64, bool, error) {
	value, ok := params[name]
	if !ok {
		return 0, false, nil
	}
	converted, ok := schemas.SafeExtractFloat64(value)
	if !ok {
		return 0, true, fmt.Errorf("extra parameter %q must be a number", name)
	}
	delete(params, name)
	return converted, true, nil
}

func consumeStringSliceExtraParam(params map[string]interface{}, name string) ([]string, bool, error) {
	value, ok := params[name]
	if !ok {
		return nil, false, nil
	}
	converted, ok := schemas.SafeExtractStringSlice(value)
	if !ok {
		return nil, true, fmt.Errorf("extra parameter %q must be an array of strings", name)
	}
	delete(params, name)
	return converted, true, nil
}

func consumeMapExtraParam(params map[string]interface{}, name string) (map[string]interface{}, bool, error) {
	value, ok := params[name]
	if !ok {
		return nil, false, nil
	}
	converted, ok := value.(map[string]interface{})
	if !ok {
		return nil, true, fmt.Errorf("extra parameter %q must be an object", name)
	}
	delete(params, name)
	return converted, true, nil
}
