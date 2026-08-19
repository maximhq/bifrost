package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// thoughtSignatureFromEncryptedContent converts a Responses-API
// encrypted_content value into the raw bytes Part.ThoughtSignature expects.
//
// The decode is required, not cosmetic. encrypted_content is already a base64
// STRING, while ThoughtSignature is a []byte that Part.MarshalJSON base64s on
// the way out -- so assigning []byte(encryptedContent) directly ships
// base64(base64(signature)) and Gemini cannot verify it. The non-streaming
// converters always decoded first; the streaming one did not, which meant the
// two paths silently disagreed about the encoding for the same conversation.
//
// Returns nil when there is nothing usable, so callers can skip the part
// entirely rather than emit an empty signature: a malformed value is dropped
// rather than corrupted onwards.
func thoughtSignatureFromEncryptedContent(encryptedContent *string) []byte {
	if encryptedContent == nil || *encryptedContent == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(*encryptedContent)
	if err != nil || len(decoded) == 0 {
		return nil
	}
	return decoded
}

func (request *GeminiGenerationRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) *schemas.BifrostResponsesRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, "")

	// Create the BifrostResponsesRequest
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider:  provider,
		Model:     model,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}

	params := request.convertGenerationConfigToResponsesParameters()

	// Convert SystemInstruction to system messages first
	var inputMessages []schemas.ResponsesMessage
	if request.SystemInstruction != nil && len(request.SystemInstruction.Parts) > 0 {
		systemMsg := convertGeminiSystemInstructionToResponsesMessage(request.SystemInstruction)
		if systemMsg != nil {
			inputMessages = append(inputMessages, *systemMsg)
		}
	}

	// Convert Contents to Input messages
	if len(request.Contents) > 0 {
		contentsMessages := convertGeminiContentsToResponsesMessages(request.Contents)
		if len(contentsMessages) > 0 {
			inputMessages = append(inputMessages, contentsMessages...)
		}
	}

	if len(inputMessages) > 0 {
		bifrostReq.Input = inputMessages
	}

	if len(request.Tools) > 0 {
		params.Tools = convertGeminiToolsToResponsesTools(request.Tools)
	}

	if request.ToolConfig != nil {
		if request.ToolConfig.FunctionCallingConfig != nil {
			params.ToolChoice = convertGeminiToolConfigToToolChoice(request.ToolConfig)
		}
		params.IncludeServerSideToolInvocations = request.ToolConfig.IncludeServerSideToolInvocations

		// Search localization rides on the web_search tool, the only tool it applies to.
		if retrieval := request.ToolConfig.RetrievalConfig; retrieval != nil && retrieval.LatLng != nil {
			for i := range params.Tools {
				webSearch := params.Tools[i].ResponsesToolWebSearch
				if params.Tools[i].Type != schemas.ResponsesToolTypeWebSearch || webSearch == nil {
					continue
				}
				if webSearch.UserLocation == nil {
					webSearch.UserLocation = &schemas.ResponsesToolWebSearchUserLocation{}
				}
				webSearch.UserLocation.Latitude = retrieval.LatLng.Latitude
				webSearch.UserLocation.Longitude = retrieval.LatLng.Longitude
			}
		}
	}

	if request.SafetySettings != nil {
		params.ExtraParams["safety_settings"] = request.SafetySettings
	}

	if request.ServiceTier != "" {
		mapped := mapGeminiServiceTierToBifrost(request.ServiceTier)
		params.ServiceTier = &mapped
	}

	if request.CachedContent != "" {
		params.ExtraParams["cached_content"] = request.CachedContent
	}

	bifrostReq.Params = params

	return bifrostReq
}

func ToGeminiResponsesRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostResponsesRequest) (*GeminiGenerationRequest, error) {
	return ToGeminiResponsesRequestWithImageURLSchemes(ctx, bifrostReq, defaultGeminiImageURLSchemes...)
}

// ToGeminiResponsesRequestWithImageURLSchemes converts a Bifrost Responses request
// to Gemini format using the provider-specific allowlist for non-data image URLs.
func ToGeminiResponsesRequestWithImageURLSchemes(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostResponsesRequest, allowedImageURLSchemes ...string) (*GeminiGenerationRequest, error) {
	if bifrostReq == nil {
		return nil, nil
	}

	bifrostReq.Model = NormalizeModelName(bifrostReq.Model)

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Canonical model for capability gating only; wire model is untouched.
	capModel := NormalizeModelName(schemas.ResolveCanonicalModel(ctx, bifrostReq.Model))

	// Convert parameters to generation config
	if bifrostReq.Params != nil {
		var err error
		geminiReq.GenerationConfig, err = geminiReq.convertParamsToGenerationConfigResponses(bifrostReq.Params, capModel)
		if err != nil {
			return nil, err
		}
		geminiReq.ExtraParams = bifrostReq.Params.ExtraParams
		includeServerSideToolInvocations := bifrostReq.Params.IncludeServerSideToolInvocations != nil && *bifrostReq.Params.IncludeServerSideToolInvocations
		// Handle tool-related parameters
		if len(bifrostReq.Params.Tools) > 0 {
			geminiReq.Tools, err = convertResponsesToolsToGemini(bifrostReq.Params.Tools, includeServerSideToolInvocations, bifrostReq.Provider, bifrostReq.Model)
			if err != nil {
				return nil, err
			}

			// Convert tool choice if present, but only when function declarations exist.
			// Gemini rejects functionCallingConfig without function_declarations
			// (e.g. a web-search-only request has GoogleSearch but no declarations).
			if bifrostReq.Params.ToolChoice != nil {
				hasFunctionDeclarations := false
				for _, tool := range geminiReq.Tools {
					if len(tool.FunctionDeclarations) > 0 {
						hasFunctionDeclarations = true
						break
					}
				}
				if hasFunctionDeclarations {
					geminiReq.ToolConfig = convertResponsesToolChoiceToGemini(bifrostReq.Params.ToolChoice)
				}
			}

			// Rebuild search localization from the web_search tool that carried it, but
			// only when that tool survived conversion — localization without a search
			// tool is meaningless and Gemini rejects it.
			hasGoogleSearch := false
			for _, tool := range geminiReq.Tools {
				if tool.GoogleSearch != nil {
					hasGoogleSearch = true
					break
				}
			}
			for _, tool := range bifrostReq.Params.Tools {
				webSearch := tool.ResponsesToolWebSearch
				if !hasGoogleSearch || tool.Type != schemas.ResponsesToolTypeWebSearch || webSearch == nil || webSearch.UserLocation == nil {
					continue
				}
				if webSearch.UserLocation.Latitude == nil && webSearch.UserLocation.Longitude == nil {
					continue
				}
				if geminiReq.ToolConfig == nil {
					geminiReq.ToolConfig = &ToolConfig{}
				}
				geminiReq.ToolConfig.RetrievalConfig = &RetrievalConfig{
					LatLng: &LatLng{
						Latitude:  webSearch.UserLocation.Latitude,
						Longitude: webSearch.UserLocation.Longitude,
					},
				}
				break
			}
		}

		if includeServerSideToolInvocations {
			applyServerSideToolInvocations(geminiReq)
		}

		if bifrostReq.Params.ServiceTier != nil {
			geminiReq.ServiceTier = mapBifrostServiceTierToGemini(*bifrostReq.Params.ServiceTier)
		}
	}

	// Convert ResponsesInput messages to Gemini contents
	if bifrostReq.Input != nil {
		contents, systemInstruction, err := convertResponsesMessagesToGeminiContents(bifrostReq.Input, capModel, bifrostReq.Provider, allowedImageURLSchemes...)
		if err != nil {
			return nil, err
		}
		geminiReq.Contents = contents

		if systemInstruction != nil {
			geminiReq.SystemInstruction = systemInstruction
		}
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.Instructions != nil {
			// check if system instruction is already set
			if geminiReq.SystemInstruction == nil {
				geminiReq.SystemInstruction = &Content{
					Parts: []*Part{
						{Text: *bifrostReq.Params.Instructions},
					},
				}
			}
		}

		if bifrostReq.Params.ExtraParams != nil {
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				delete(geminiReq.ExtraParams, "safety_settings")
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					geminiReq.SafetySettings = settings
				}
			}
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				delete(geminiReq.ExtraParams, "cached_content")
				geminiReq.CachedContent = cachedContent
			}
		}
	}

	return geminiReq, nil
}

// ToResponsesBifrostResponsesResponse converts a Gemini GenerateContentResponse to a BifrostResponsesResponse
func (response *GenerateContentResponse) ToResponsesBifrostResponsesResponse() *schemas.BifrostResponsesResponse {
	if response == nil {
		return nil
	}

	// Create the BifrostResponse with Responses structure
	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:        schemas.Ptr("resp_" + providerUtils.GetRandomString(50)),
		CreatedAt: int(time.Now().Unix()),
		Model:     response.ModelVersion,
	}

	// Convert usage information
	bifrostResp.Usage = ConvertGeminiUsageMetadataToResponsesUsage(response.UsageMetadata)
	if len(response.Candidates) > 0 && response.Candidates[0] != nil {
		applyGeminiSearchQueryResponsesUsage(bifrostResp.Usage, response.Candidates[0].GroundingMetadata, response.ModelVersion)
	}

	if response.UsageMetadata != nil {
		if t := mapGeminiTrafficTypeToBifrost(response.UsageMetadata.TrafficType); t != nil {
			bifrostResp.ServiceTier = t
		} else if response.UsageMetadata.ServiceTier != "" {
			tier := mapGeminiServiceTierToBifrost(response.UsageMetadata.ServiceTier)
			bifrostResp.ServiceTier = &tier
		}
	}

	// Convert candidates to Responses output messages
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]

		// Persist finish reason as Bifrost canonical stop_reason
		if candidate.FinishReason != "" && candidate.FinishReason != FinishReasonUnspecified {
			stopReason := ConvertGeminiFinishReasonToBifrost(candidate.FinishReason)
			bifrostResp.StopReason = &stopReason

			if isErrorFinishReason(candidate.FinishReason) {
				failedStatus := "failed"
				bifrostResp.Status = &failedStatus

				errMsg := candidate.FinishMessage
				if errMsg == "" {
					errMsg = string(candidate.FinishReason)
				}
				bifrostResp.Error = &schemas.ResponsesResponseError{
					Code:    stopReason,
					Message: errMsg,
				}

				return bifrostResp
			}
		}

		outputMessages := convertGeminiCandidatesToResponsesOutput(response.Candidates)
		if len(outputMessages) > 0 {
			bifrostResp.Output = outputMessages
		}

		// safetyRatings, avgLogprobs, and the native responseId have no field in Bifrost's
		// OpenAI-shaped Responses schema. Preserve them here so ToGeminiResponsesResponse
		// can restore them on the GenAI generateContent egress path.
		extraFields := map[string]interface{}{}
		if response.ResponseID != "" {
			extraFields["responseId"] = response.ResponseID
		}
		if len(candidate.SafetyRatings) > 0 {
			extraFields["safetyRatings"] = candidate.SafetyRatings
		}
		if candidate.AvgLogprobs != 0 {
			extraFields["avgLogprobs"] = candidate.AvgLogprobs
		}
		// Server-side tool parts have no lossless home in Bifrost's OpenAI-shaped schema
		// (the web_search_call item keeps the queries, but not the raw tool response or the
		// thoughtSignature Gemini requires back on replay). Carry them verbatim so the
		// native GenAI response can be reproduced exactly.
		if parts := serverSideToolParts(candidate); len(parts) > 0 {
			extraFields["serverSideToolParts"] = parts
		}
		if len(extraFields) > 0 {
			bifrostResp.ProviderExtraFields = extraFields
		}
	}

	return bifrostResp
}

// thoughtTextParts renders a reasoning item's summary as Gemini thought parts.
//
// Gemini's thinking guide requires thought blocks to be resent unmodified, so
// wherever a reasoning item's signature is taken its text has to travel with it.
func thoughtTextParts(reasoning *schemas.ResponsesReasoning) []*Part {
	if reasoning == nil {
		return nil
	}
	var parts []*Part
	for _, summaryBlock := range reasoning.Summary {
		if summaryBlock.Text == "" {
			continue
		}
		parts = append(parts, &Part{Text: summaryBlock.Text, Thought: true})
	}
	return parts
}

func ToGeminiResponsesResponse(bifrostResp *schemas.BifrostResponsesResponse) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	geminiResp := &GenerateContentResponse{
		ModelVersion: bifrostResp.Model,
	}

	// Set response ID if available
	if bifrostResp.ID != nil {
		geminiResp.ResponseID = *bifrostResp.ID
	}

	// Set creation time
	if bifrostResp.CreatedAt > 0 {
		geminiResp.CreateTime = time.Unix(int64(bifrostResp.CreatedAt), 0)
	}

	// Server-side tool parts (toolCall/toolResponse) preserved verbatim at parse time.
	// They are replayed ahead of the generated content, reproducing Gemini's own ordering,
	// and they already carry their thoughtSignatures -- so the reasoning items derived from
	// those same signatures must not emit duplicate signature-only parts below.
	var preservedToolParts []*Part
	preservedToolSignatures := map[string]bool{}
	if bifrostResp.ProviderExtraFields != nil {
		preservedToolParts = extractServerSideToolParts(bifrostResp.ProviderExtraFields["serverSideToolParts"])
		for _, part := range preservedToolParts {
			if len(part.ThoughtSignature) > 0 {
				preservedToolSignatures[base64.StdEncoding.EncodeToString(part.ThoughtSignature)] = true
			}
		}
	}

	// Convert output messages to candidates
	if len(bifrostResp.Output) > 0 {
		candidates := []*Candidate{}

		// Group messages by their role to create candidates
		var currentParts []*Part
		var currentRole string

		// Track which message indices have been consumed as thought signatures
		consumedIndices := make(map[int]bool)

		// Find last web_search_call and collect annotations and rendered_content for grounding metadata
		var lastWebSearchCall *schemas.ResponsesMessage
		var webSearchAnnotations []schemas.ResponsesOutputMessageContentTextAnnotation
		var lastRenderedContent *string
		for i := range bifrostResp.Output {
			msg := &bifrostResp.Output[i]
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeWebSearchCall {
				// Grounding's sources are merged onto the FIRST search call on the forward
				// path, so with two or more rounds the last item carries none and rebuilding
				// groundingMetadata from it drops groundingChunks entirely. Prefer the item
				// that actually holds sources; fall back to the last when none does, which
				// is the single-round case where first and last are the same message.
				if lastWebSearchCall == nil || !webSearchCallHasSources(lastWebSearchCall) {
					lastWebSearchCall = msg
				}
				consumedIndices[i] = true
			}
			// Collect annotations (typically in message after web search)
			if msg.Content != nil && msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.ResponsesOutputMessageContentText != nil && len(block.ResponsesOutputMessageContentText.Annotations) > 0 {
						webSearchAnnotations = append(webSearchAnnotations, block.ResponsesOutputMessageContentText.Annotations...)
					}
					// Collect rendered_content
					if block.Type == schemas.ResponsesOutputMessageContentTypeRenderedContent &&
						block.ResponsesOutputMessageContentRenderedContent != nil &&
						block.ResponsesOutputMessageContentRenderedContent.RenderedContent != "" {
						lastRenderedContent = &block.ResponsesOutputMessageContentRenderedContent.RenderedContent
						consumedIndices[i] = true // Mark this message as consumed
					}
				}
			}
		}

		for i, msg := range bifrostResp.Output {
			// Skip web_search_call messages as they're converted to grounding metadata
			if consumedIndices[i] {
				continue
			}

			// Determine the role
			role := "model" // default
			if msg.Role != nil {
				if *msg.Role == schemas.ResponsesInputMessageRoleUser {
					role = "user"
				}
			}

			// If we're starting a new candidate (role changed), save the previous one.
			//
			// The guard is applied to what SURVIVES the filter, not to the raw slice: a
			// run of payload-free parts has a non-zero length but contributes nothing,
			// so testing before filtering flushed a candidate whose content was empty.
			if currentRole != "" && currentRole != role {
				if flushed := dropEmptyGeminiParts(currentParts); len(flushed) > 0 {
					candidates = append(candidates, &Candidate{
						Index: int32(len(candidates)),
						Content: &Content{
							Parts: flushed,
							Role:  currentRole,
						},
					})
				}
				currentParts = []*Part{}
			}
			currentRole = role

			// Convert message content to parts
			if msg.Content != nil {
				// Handle string content
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					currentParts = append(currentParts, &Part{
						Text: *msg.Content.ContentStr,
					})
				}

				// Handle content blocks
				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						part, err := convertContentBlockToGeminiPart(block)
						if err == nil && part != nil {
							currentParts = append(currentParts, part)
						}
					}
				}
			}

			// Handle tool calls (function calls)
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall && msg.ResponsesToolMessage != nil {
				argsRaw := json.RawMessage("{}")
				if msg.ResponsesToolMessage.Arguments != nil {
					rawArgs := strings.TrimSpace(*msg.ResponsesToolMessage.Arguments)
					if rawArgs == "" {
						rawArgs = "{}"
					}
					var buf bytes.Buffer
					if err := json.Compact(&buf, []byte(rawArgs)); err == nil {
						argsRaw = buf.Bytes()
					}
				}
				functionCall := &FunctionCall{
					Args: argsRaw,
				}
				if msg.ResponsesToolMessage.Name != nil {
					functionCall.Name = *msg.ResponsesToolMessage.Name
				}

				// Extract thought signature from CallID if present
				var thoughtSignature []byte
				// Thought text belonging to a reasoning item consumed for its signature below.
				var consumedThoughtText []*Part
				if msg.ResponsesToolMessage.CallID != nil {
					callID := *msg.ResponsesToolMessage.CallID
					// Check if the ID contains a thought signature (format: "ToolName_ts_base64signature")
					if strings.Contains(callID, thoughtSignatureSeparator) {
						parts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
						if len(parts) == 2 {
							// Try to decode the signature part
							if decodedSig, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
								thoughtSignature = decodedSig
							}
						}
					}
					functionCall.ID = callID
				}

				part := &Part{
					FunctionCall: functionCall,
				}

				// Use thought signature from CallID if we extracted one
				if len(thoughtSignature) > 0 {
					part.ThoughtSignature = thoughtSignature
				} else {
					// Otherwise, look ahead to see if the next message is a reasoning message with encrypted content
					// (thought signature for this function call)
					if i+1 < len(bifrostResp.Output) {
						nextMsg := bifrostResp.Output[i+1]
						if nextMsg.Type != nil && *nextMsg.Type == schemas.ResponsesMessageTypeReasoning &&
							nextMsg.ResponsesReasoning != nil && nextMsg.ResponsesReasoning.EncryptedContent != nil {
							decodedSig, err := base64.StdEncoding.DecodeString(*nextMsg.ResponsesReasoning.EncryptedContent)
							if err == nil {
								part.ThoughtSignature = decodedSig
								// Mark this reasoning message as consumed
								consumedIndices[i+1] = true
								// Consuming it takes its signature; its TEXT has
								// to come along or the block reaches Gemini
								// modified, which the thinking guide forbids
								// ("You should NOT remove or modify thought
								// blocks from the history"). Carried here rather
								// than by leaving the message unconsumed,
								// because the normal reasoning branch below also
								// emits a signature-only part - that path would
								// send the same signature twice.
								consumedThoughtText = thoughtTextParts(nextMsg.ResponsesReasoning)
							}
						}
					}
				}

				currentParts = append(currentParts, part)
				currentParts = append(currentParts, consumedThoughtText...)
			}

			// Handle function responses (function call outputs)
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput && msg.ResponsesToolMessage != nil {
				responseMap := make(map[string]any)

				if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
					output := *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
					if json.Valid([]byte(output)) {
						responseMap["output"] = json.RawMessage(output)
					} else {
						responseMap["output"] = output
					}
				}
				funcName := ""
				if msg.ResponsesToolMessage.Name != nil && strings.TrimSpace(*msg.ResponsesToolMessage.Name) != "" {
					funcName = *msg.ResponsesToolMessage.Name
				} else if msg.ResponsesToolMessage.CallID != nil {
					funcName = *msg.ResponsesToolMessage.CallID
				}

				responseBytes, _ := providerUtils.MarshalSorted(responseMap)
				functionResponse := &FunctionResponse{
					Name:     funcName,
					Response: json.RawMessage(responseBytes),
				}
				if msg.ResponsesToolMessage.CallID != nil {
					functionResponse.ID = *msg.ResponsesToolMessage.CallID
				}

				currentParts = append(currentParts, &Part{
					FunctionResponse: functionResponse,
				})
			}

			// Handle reasoning messages
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning && msg.ResponsesReasoning != nil {
				// Skip this reasoning message if it was already consumed as a thought signature
				if consumedIndices[i] {
					continue
				}

				// Reasoning content is in the Summary array
				if len(msg.ResponsesReasoning.Summary) > 0 {
					for _, summaryBlock := range msg.ResponsesReasoning.Summary {
						if summaryBlock.Text != "" {
							currentParts = append(currentParts, &Part{
								Text:    summaryBlock.Text,
								Thought: true,
							})
						}
					}
				}
				if msg.ResponsesReasoning.EncryptedContent != nil {
					decodedSig := thoughtSignatureFromEncryptedContent(msg.ResponsesReasoning.EncryptedContent)
					if decodedSig != nil && !preservedToolSignatures[base64.StdEncoding.EncodeToString(decodedSig)] {
						currentParts = append(currentParts, &Part{
							ThoughtSignature: decodedSig,
						})
					}
				}
			}
		}

		// Preserved server-side tool parts lead the FIRST candidate, not the terminal
		// group. They reproduce Gemini's own ordering, so once a role change has already
		// flushed a candidate, merging them into the trailing group would file them behind
		// content they are supposed to precede.
		//
		// The merge replays Gemini's whole parts array in Gemini's own order rather than
		// prepending the tool parts alone: prepending put them ahead of text the model had
		// emitted first, inverting the interleaving and with it the thought_signature
		// positional context Gemini validates on replay.
		if len(preservedToolParts) > 0 {
			if len(candidates) > 0 && candidates[0].Content != nil {
				candidates[0].Content.Parts = mergePreservedGeminiParts(
					preservedToolParts, candidates[0].Content.Parts)
			} else {
				currentParts = mergePreservedGeminiParts(preservedToolParts, currentParts)
			}
		}

		// Built once, then either appended or merged. Building it is what attaches the
		// finish reason, grounding metadata, safety ratings and avgLogprobs -- candidates
		// flushed by the role-change branch above carry only Index and Content, so a path
		// that skips this call drops all of that for the whole response. A grounded
		// web-search turn whose trailing role group produces nothing would lose its
		// groundingMetadata outright.
		terminal := buildGeminiTerminalCandidate(
			bifrostResp, currentParts, currentRole, len(candidates),
			lastWebSearchCall, webSearchAnnotations, lastRenderedContent)

		// Appending is right only while no candidate exists yet -- the contentless case
		// this branch was written for, where something has to carry the finish reason.
		// Once a role change has flushed one, an empty sibling would leave candidates[0]
		// holding content with no finish reason and candidates[1] a finish reason with no
		// content; a Gemini-shaped client reads candidates[0] and gets neither half whole.
		// So the terminal candidate's metadata moves onto the candidate that has the
		// content instead, rather than being appended or discarded.
		if len(dropEmptyGeminiParts(currentParts)) == 0 && len(candidates) > 0 {
			// candidates[0], not the last one. Gemini emits a single candidate per
			// response; more than one here is an artifact of Bifrost grouping output
			// items by role, and every Gemini-shaped client reads candidates[0]. With
			// one flushed candidate the two are the same object, so this only changes
			// the alternating-role case -- where merging onto the last candidate filed
			// the finish reason where nothing reads it.
			target := candidates[0]
			if target.FinishReason == "" {
				target.FinishReason = terminal.FinishReason
			}
			if target.GroundingMetadata == nil {
				target.GroundingMetadata = terminal.GroundingMetadata
			}
			if target.SafetyRatings == nil {
				target.SafetyRatings = terminal.SafetyRatings
			}
			if target.AvgLogprobs == 0 {
				target.AvgLogprobs = terminal.AvgLogprobs
			}
		} else {
			candidates = append(candidates, terminal)
		}

		geminiResp.Candidates = candidates
	} else {
		// No output items at all: a thinking model can spend its whole budget before
		// emitting a visible token. That is a successful 200 with an empty answer, and
		// it still needs a candidate to carry the finish reason.
		//
		// preservedToolParts is carried here for the same reason the loop branch
		// prepends it: those parts were kept verbatim at parse time so the turn can be
		// replayed losslessly, signatures included, and they are read before the loop
		// so they exist independently of whether any output item does. A turn whose
		// only act was a server-side tool call, cut short before it wrote anything
		// visible, has preserved parts and an empty Output at the same time.
		geminiResp.Candidates = []*Candidate{
			buildGeminiTerminalCandidate(bifrostResp, preservedToolParts, "", 0, nil, nil, nil),
		}
	}

	// Restore the native provider responseId (rather than the synthesized resp_... internal ID)
	// when this response originated from a Gemini/Vertex candidate that carried one.
	if bifrostResp.ProviderExtraFields != nil {
		if responseID, ok := bifrostResp.ProviderExtraFields["responseId"].(string); ok && responseID != "" {
			geminiResp.ResponseID = responseID
		}
	}

	// Convert usage metadata
	if bifrostResp.Usage != nil {
		geminiResp.UsageMetadata = ConvertBifrostResponsesUsageToGeminiUsageMetadata(bifrostResp.Usage)
	}
	if bifrostResp.ServiceTier != nil {
		if geminiResp.UsageMetadata == nil {
			geminiResp.UsageMetadata = &GenerateContentResponseUsageMetadata{}
		}
		if bifrostResp.ExtraFields.Provider == schemas.Vertex {
			geminiResp.UsageMetadata.TrafficType = mapBifrostServiceTierToVertexTrafficType(*bifrostResp.ServiceTier)
		} else {
			geminiResp.UsageMetadata.ServiceTier = mapBifrostServiceTierToGemini(*bifrostResp.ServiceTier)
		}
	}

	return geminiResp
}

// dropEmptyGeminiParts removes parts that carry no payload. Every Part field is
// omitempty, so such a part marshals to exactly `{}` -- never a valid answer, and it
// masks a contentless response by making the parts slice look non-empty. The harness
// observed one on the wire (a transcription request for an unintelligible tone came
// back as parts:[{}]), so this filters at the point the candidate is assembled,
// covering both a part this package built and one relayed from upstream.
//
// Emptiness is defined as "equal to the zero Part" rather than as a field checklist,
// so a Part gaining a new field does not silently start being discarded.
func dropEmptyGeminiParts(parts []*Part) []*Part {
	kept := parts[:0:0]
	for _, part := range parts {
		if part == nil {
			continue
		}
		// The comparison is against a zero Part with the signature normalised, not
		// against the raw value: a zero-length ThoughtSignature is not DeepEqual to a
		// nil one, yet Part.MarshalJSON writes the signature through a string alias
		// with omitempty, so an empty slice base64-encodes to "" and the key vanishes.
		// Judging on struct equality alone therefore kept a part that reaches the wire
		// as exactly the `{}` this filter exists to remove.
		probe := *part
		if len(probe.ThoughtSignature) == 0 {
			probe.ThoughtSignature = nil
		}
		if reflect.DeepEqual(probe, Part{}) {
			continue
		}
		kept = append(kept, part)
	}
	return kept
}

// buildGeminiTerminalCandidate assembles the final candidate of a generateContent
// response: the accumulated parts plus the response-level finish reason, grounding
// metadata, and the safety/logprob fields preserved on the way in.
//
// It is called even when parts is empty. A thinking model that spends its whole
// output budget before emitting a visible token returns MAX_TOKENS with reasoning
// tokens billed and nothing to show, which is a successful 200 with an empty answer
// rather than a malformed one. Because GenerateContentResponse.Candidates is tagged
// omitempty, dropping that candidate does not emit "candidates":[] -- it emits a
// body with no candidates key at all, leaving a lone usageMetadata object that every
// Gemini-shaped client dereferences blind. This is the generateContent twin of the
// null-Choices hazard TestContentlessCandidateStillYieldsAChoice pins for chat.
func buildGeminiTerminalCandidate(
	bifrostResp *schemas.BifrostResponsesResponse,
	parts []*Part,
	role string,
	index int,
	lastWebSearchCall *schemas.ResponsesMessage,
	webSearchAnnotations []schemas.ResponsesOutputMessageContentTextAnnotation,
	lastRenderedContent *string,
) *Candidate {
	if role == "" {
		role = string(RoleModel)
	}

	candidate := &Candidate{
		Index: int32(index),
		Content: &Content{
			Parts: dropEmptyGeminiParts(parts),
			Role:  role,
		},
	}

	// Determine finish reason: prefer StopReason (Bifrost canonical), fall back to IncompleteDetails
	if bifrostResp.StopReason != nil {
		candidate.FinishReason = ConvertBifrostFinishReasonToGemini(*bifrostResp.StopReason)
	} else if bifrostResp.IncompleteDetails != nil {
		// Match the schema's incomplete-reason vocabulary; a literal
		// "max_tokens" never occurs here, so truncations reported OTHER
		// instead of MAX_TOKENS (issue #5978).
		switch bifrostResp.IncompleteDetails.Reason {
		case schemas.ResponsesResponseIncompleteReasonMaxOutputTokens:
			candidate.FinishReason = FinishReasonMaxTokens
		case schemas.ResponsesResponseIncompleteReasonContentFilter:
			candidate.FinishReason = FinishReasonSafety
		default:
			candidate.FinishReason = FinishReasonOther
		}
	} else {
		candidate.FinishReason = FinishReasonStop
	}

	// Attach grounding metadata if web search was used
	if lastWebSearchCall != nil {
		candidate.GroundingMetadata = buildGroundingMetadataFromWebSearch(lastWebSearchCall, webSearchAnnotations, lastRenderedContent)
	}

	// Restore safetyRatings/avgLogprobs preserved by ToResponsesBifrostResponsesResponse
	// (they have no field in Bifrost's OpenAI-shaped Responses schema).
	if bifrostResp.ProviderExtraFields != nil {
		if ratings := extractGeminiSafetyRatings(bifrostResp.ProviderExtraFields["safetyRatings"]); ratings != nil {
			candidate.SafetyRatings = ratings
		}
		if avgLogprobs, ok := extractGeminiAvgLogprobs(bifrostResp.ProviderExtraFields["avgLogprobs"]); ok {
			candidate.AvgLogprobs = avgLogprobs
		}
	}

	return candidate
}

// extractGeminiSafetyRatings recovers []*SafetyRating from ProviderExtraFields["safetyRatings"].
// Handles both the in-memory pointer (normal non-streaming path) and a JSON-decoded
// []interface{}/map form (e.g. if the response was round-tripped through JSON).
func extractGeminiSafetyRatings(v interface{}) []*SafetyRating {
	if v == nil {
		return nil
	}
	if ratings, ok := v.([]*SafetyRating); ok {
		return ratings
	}
	b, err := sonic.Marshal(v)
	if err != nil {
		return nil
	}
	var ratings []*SafetyRating
	if err := sonic.Unmarshal(b, &ratings); err != nil {
		return nil
	}
	return ratings
}

// extractGeminiAvgLogprobs recovers a float64 from ProviderExtraFields["avgLogprobs"].
func extractGeminiAvgLogprobs(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case nil:
		return 0, false
	case float64:
		return val, true
	case float32:
		return float64(val), true
	default:
		b, err := sonic.Marshal(v)
		if err != nil {
			return 0, false
		}
		var f float64
		if err := sonic.Unmarshal(b, &f); err != nil {
			return 0, false
		}
		return f, true
	}
}

// BifrostToGeminiStreamState tracks state when converting Bifrost streams to Gemini format
type BifrostToGeminiStreamState struct {
	// Web search buffering
	WebSearchCall   *schemas.ResponsesMessage                             // Buffered web_search_call
	Annotations     []schemas.ResponsesOutputMessageContentTextAnnotation // Buffered annotations
	RenderedContent *string                                               // Buffered rendered content from search entry point
	HasWebSearch    bool                                                  // Whether we've seen web search

	// Tool call tracking (for FunctionCallArgumentsDone events that don't include Item)
	ToolCallNames map[int]string // Maps output_index to tool name
	ToolCallIDs   map[int]string // Maps output_index to tool call ID
}

// NewBifrostToGeminiStreamState creates a new state for Bifrost→Gemini streaming
func NewBifrostToGeminiStreamState() *BifrostToGeminiStreamState {
	return &BifrostToGeminiStreamState{
		Annotations:   make([]schemas.ResponsesOutputMessageContentTextAnnotation, 0),
		ToolCallNames: make(map[int]string),
		ToolCallIDs:   make(map[int]string),
	}
}

func ToGeminiResponsesStreamResponse(bifrostResp *schemas.BifrostResponsesStreamResponse, state *BifrostToGeminiStreamState) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	// Initialize state if not provided (backward compatibility)
	if state == nil {
		state = NewBifrostToGeminiStreamState()
	}

	// Buffer web search call
	if bifrostResp.Type == schemas.ResponsesStreamResponseTypeOutputItemDone &&
		bifrostResp.Item != nil &&
		bifrostResp.Item.Type != nil &&
		*bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {
		state.WebSearchCall = bifrostResp.Item
		state.HasWebSearch = true
		return nil // Don't emit yet, wait for completion
	}

	// Buffer annotations
	if bifrostResp.Type == schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded &&
		bifrostResp.Annotation != nil {
		state.Annotations = append(state.Annotations, *bifrostResp.Annotation)
		return nil // Don't emit yet, wait for completion
	}

	// Buffer rendered_content messages
	if bifrostResp.Type == schemas.ResponsesStreamResponseTypeOutputItemDone &&
		bifrostResp.Item != nil &&
		bifrostResp.Item.Content != nil &&
		bifrostResp.Item.Content.ContentBlocks != nil {
		for _, block := range bifrostResp.Item.Content.ContentBlocks {
			if block.Type == schemas.ResponsesOutputMessageContentTypeRenderedContent &&
				block.ResponsesOutputMessageContentRenderedContent != nil &&
				block.ResponsesOutputMessageContentRenderedContent.RenderedContent != "" {
				state.RenderedContent = &block.ResponsesOutputMessageContentRenderedContent.RenderedContent
				return nil // Don't emit yet, wait for completion
			}
		}
	}

	// Skip lifecycle events that don't have corresponding Gemini equivalents
	switch bifrostResp.Type {
	case schemas.ResponsesStreamResponseTypePing,
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded,
		schemas.ResponsesStreamResponseTypeQueued,
		// Skip web search lifecycle events - buffered above
		schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
		schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsAdded,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsCompleted:
		// These are lifecycle events with no Gemini equivalent or are buffered
		return nil
	}

	streamResp := &GenerateContentResponse{
		Candidates: []*Candidate{
			{
				Content: &Content{
					Parts: []*Part{},
					Role:  "model",
				},
			},
		},
	}

	candidate := streamResp.Candidates[0]

	switch bifrostResp.Type {
	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			candidate.Content.Parts = append(candidate.Content.Parts, &Part{
				Text: *bifrostResp.Delta,
			})
		}

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
		if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			candidate.Content.Parts = append(candidate.Content.Parts, &Part{
				Text:    *bifrostResp.Delta,
				Thought: true,
			})
		}

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		// For streaming, we'll accumulate these, but Gemini typically sends complete calls
		// We'll return nil here and let the done event handle it
		return nil

	// Function call completed
	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone:
		// Handle arguments from either Item.ResponsesToolMessage or directly from Arguments field
		var argsStr *string
		var name *string
		var callID *string

		if bifrostResp.Item != nil && bifrostResp.Item.ResponsesToolMessage != nil {
			argsStr = bifrostResp.Item.ResponsesToolMessage.Arguments
			name = bifrostResp.Item.ResponsesToolMessage.Name
			callID = bifrostResp.Item.ResponsesToolMessage.CallID
		}
		if argsStr == nil && bifrostResp.Arguments != nil {
			// Some providers (e.g., Anthropic) send Arguments directly on the response
			argsStr = bifrostResp.Arguments
			// Try to get name and callID from state if available
			if state != nil {
				outputIndex := 0
				if bifrostResp.OutputIndex != nil {
					outputIndex = *bifrostResp.OutputIndex
				}
				if name == nil {
					if n, ok := state.ToolCallNames[outputIndex]; ok {
						name = &n
					}
				}
				if callID == nil {
					if id, ok := state.ToolCallIDs[outputIndex]; ok {
						callID = &id
					}
				}
			}
		}

		if argsStr != nil {
			rawArgs := strings.TrimSpace(*argsStr)
			if rawArgs == "" {
				rawArgs = "{}"
			}
			var argsRaw json.RawMessage
			var buf bytes.Buffer
			if err := json.Compact(&buf, []byte(rawArgs)); err == nil {
				argsRaw = buf.Bytes()
			} else {
				argsRaw = json.RawMessage("{}")
			}
			functionCall := &FunctionCall{
				Name: "",
				Args: argsRaw,
			}
			if name != nil {
				functionCall.Name = *name
			}

			var thoughtSig string
			if callID != nil {
				// Extract thought signature from CallID if present
				if strings.Contains(*callID, thoughtSignatureSeparator) {
					parts := strings.SplitN(*callID, thoughtSignatureSeparator, 2)
					if len(parts) == 2 {
						thoughtSig = parts[1]
					}
				}
				functionCall.ID = *callID
			}
			functionCallPart := &Part{
				FunctionCall: functionCall,
			}
			if thoughtSig != "" {
				if decodedSig, err := base64.RawURLEncoding.DecodeString(thoughtSig); err == nil {
					functionCallPart.ThoughtSignature = decodedSig
				}
			}
			candidate.Content.Parts = append(candidate.Content.Parts, functionCallPart)
		}

	case schemas.ResponsesStreamResponseTypeOutputTextDone:
		// Text was already streamed via OutputTextDelta chunks, skip to avoid duplication
		return nil

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
		schemas.ResponsesStreamResponseTypeReasoningSummaryPartDone:
		// Already handled via deltas, skip
		return nil
	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		if bifrostResp.Item != nil && bifrostResp.Item.ResponsesReasoning != nil {
			// A server-side toolCall/toolResponse part travels whole on the item. Re-emit it
			// verbatim: it already carries the same thoughtSignature the reasoning item
			// encodes, so emitting both would hand the client the signature twice.
			if native := nativePartsFromItem(bifrostResp.Item); len(native) > 0 {
				candidate.Content.Parts = append(candidate.Content.Parts, native...)
			} else if sig := thoughtSignatureFromEncryptedContent(bifrostResp.Item.ResponsesReasoning.EncryptedContent); sig != nil {
				candidate.Content.Parts = append(candidate.Content.Parts, &Part{ThoughtSignature: sig})
			}
		}
		// Track function call metadata for later use in FunctionCallArgumentsDone
		if bifrostResp.Item != nil && bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall &&
			bifrostResp.Item.ResponsesToolMessage != nil {
			outputIndex := 0
			if bifrostResp.OutputIndex != nil {
				outputIndex = *bifrostResp.OutputIndex
			}
			if bifrostResp.Item.ResponsesToolMessage.Name != nil {
				state.ToolCallNames[outputIndex] = *bifrostResp.Item.ResponsesToolMessage.Name
			}
			if bifrostResp.Item.ResponsesToolMessage.CallID != nil {
				state.ToolCallIDs[outputIndex] = *bifrostResp.Item.ResponsesToolMessage.CallID
			}
		}
		// Fall through to the emptiness check below rather than returning unconditionally:
		// this case builds reasoning/native parts above, and returning nil here discarded
		// them, so thoughtSignatures never reached a /genai SSE client at all.

	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		return nil

	case schemas.ResponsesStreamResponseTypeContentPartAdded:
		// Handle content parts that contain images, audio, or files
		if bifrostResp.Part != nil {
			part, err := convertContentBlockToGeminiPart(*bifrostResp.Part)
			if err == nil && part != nil {
				candidate.Content.Parts = append(candidate.Content.Parts, part)
			}
		}

	case schemas.ResponsesStreamResponseTypeContentPartDone:
		// Already handled via ContentPartAdded
		return nil

	case schemas.ResponsesStreamResponseTypeCompleted:
		if bifrostResp.Response != nil {
			// Set model version if available
			if bifrostResp.Response.Model != "" {
				streamResp.ModelVersion = bifrostResp.Response.Model
			}

			// Convert usage metadata if available
			if bifrostResp.Response.Usage != nil {
				streamResp.UsageMetadata = ConvertBifrostResponsesUsageToGeminiUsageMetadata(bifrostResp.Response.Usage)
			}
			if bifrostResp.Response.ServiceTier != nil {
				if streamResp.UsageMetadata == nil {
					streamResp.UsageMetadata = &GenerateContentResponseUsageMetadata{}
				}
				if bifrostResp.Response.ExtraFields.Provider == schemas.Vertex {
					streamResp.UsageMetadata.TrafficType = mapBifrostServiceTierToVertexTrafficType(*bifrostResp.Response.ServiceTier)
				} else {
					streamResp.UsageMetadata.ServiceTier = mapBifrostServiceTierToGemini(*bifrostResp.Response.ServiceTier)
				}
			}

			// Derive finish reason from StopReason when present
			if bifrostResp.Response.StopReason != nil {
				candidate.FinishReason = ConvertBifrostFinishReasonToGemini(*bifrostResp.Response.StopReason)
			} else {
				candidate.FinishReason = FinishReasonStop
			}

			// Attach grounding metadata if we buffered web search data
			if state.HasWebSearch && state.WebSearchCall != nil {
				candidate.GroundingMetadata = buildGroundingMetadataFromWebSearch(state.WebSearchCall, state.Annotations, state.RenderedContent)
			}

			// Restore safetyRatings/avgLogprobs/responseId preserved by closeGeminiOpenItems
			// (they have no field in Bifrost's OpenAI-shaped Responses schema).
			if bifrostResp.Response.ProviderExtraFields != nil {
				if ratings := extractGeminiSafetyRatings(bifrostResp.Response.ProviderExtraFields["safetyRatings"]); ratings != nil {
					candidate.SafetyRatings = ratings
				}
				if avgLogprobs, ok := extractGeminiAvgLogprobs(bifrostResp.Response.ProviderExtraFields["avgLogprobs"]); ok {
					candidate.AvgLogprobs = avgLogprobs
				}
				if responseID, ok := bifrostResp.Response.ProviderExtraFields["responseId"].(string); ok && responseID != "" {
					streamResp.ResponseID = responseID
				}
			}
		}

	// Response failed
	case schemas.ResponsesStreamResponseTypeFailed:
		candidate.FinishReason = FinishReasonOther
		if bifrostResp.Response != nil && bifrostResp.Response.Error != nil {
			streamResp.PromptFeedback = &GenerateContentResponsePromptFeedback{
				BlockReason:        "ERROR",
				BlockReasonMessage: bifrostResp.Response.Error.Message,
			}
		}

	// Refusal
	case schemas.ResponsesStreamResponseTypeRefusalDelta:
		if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			candidate.Content.Parts = append(candidate.Content.Parts, &Part{
				Text: *bifrostResp.Delta,
			})
		}

	case schemas.ResponsesStreamResponseTypeRefusalDone:
		if bifrostResp.Refusal != nil && *bifrostResp.Refusal != "" {
			candidate.FinishReason = FinishReasonSafety
		}

	default:
		// For any other event types we don't explicitly handle, return nil
		return nil
	}

	// If we didn't add any parts and there's no metadata, return nil
	if len(candidate.Content.Parts) == 0 && streamResp.UsageMetadata == nil &&
		streamResp.PromptFeedback == nil && candidate.FinishReason == "" {
		return nil
	}

	return streamResp
}

// GeminiResponsesStreamState tracks state during streaming conversion for responses API
type GeminiResponsesStreamState struct {
	// Lifecycle flags
	HasEmittedCreated    bool // Whether response.created has been sent
	HasEmittedInProgress bool // Whether response.in_progress has been sent
	HasEmittedCompleted  bool // Whether response.completed has been sent

	// Item tracking
	CurrentOutputIndex int            // Current output index counter
	TextOutputIndex    int            // Output index of the current text item (cached for reuse)
	ItemIDs            map[int]string // Maps output_index to item ID
	TextItemClosed     bool           // Whether text item has been closed

	// Tool call tracking
	ToolCallIDs         map[int]string // Maps output_index to tool call ID
	ToolCallNames       map[int]string // Maps output_index to tool name
	ToolArgumentBuffers map[int]string // Accumulates tool arguments as JSON

	// Response metadata
	MessageID  *string // Generated message ID
	Model      *string // Model version
	CreatedAt  int     // Timestamp for consistency
	ResponseID *string // Gemini's responseId

	// Candidate metadata that only arrives on the terminal chunk (alongside finishReason).
	// Preserved here so closeGeminiOpenItems can stash them onto the response.completed
	// event's ProviderExtraFields for the reverse (Bifrost -> Gemini) conversion to restore.
	SafetyRatings []*SafetyRating
	AvgLogprobs   float64

	// Content tracking
	HasStartedText     bool            // Whether we've started text content
	HasStartedToolCall bool            // Whether we've started a tool call
	TextBuffer         strings.Builder // Accumulates text deltas for output_text.done

	// Web search tracking
	HasEmittedWebSearch bool // Whether web_search_call events have been emitted
	// Server-side searches reported by the model itself via toolCall/toolResponse parts,
	// one entry per round in the order the model ran them. Recorded as they stream in and
	// emitted as one web_search_call item each at finish, so the model's own call IDs and
	// per-round queries win over the grounding-derived ones without emitting a search twice.
	// A model may search several times in a single response, so this cannot collapse to a
	// single call ID -- the non-streaming converter keys its items the same way.
	ServerSearchRounds []GeminiServerSearchRound
}

// GeminiServerSearchRound is one server-side search the model reported running, identified
// by the tool call ID Gemini assigned it.
type GeminiServerSearchRound struct {
	CallID       string
	Queries      []string
	ImageQueries []string
}

// geminiResponsesStreamStatePool provides a pool for Gemini responses stream state objects.
var geminiResponsesStreamStatePool = sync.Pool{
	New: func() interface{} {
		return &GeminiResponsesStreamState{
			ItemIDs:              make(map[int]string),
			ToolCallIDs:          make(map[int]string),
			ToolCallNames:        make(map[int]string),
			ToolArgumentBuffers:  make(map[int]string),
			CurrentOutputIndex:   0,
			TextOutputIndex:      -1,
			CreatedAt:            int(time.Now().Unix()),
			HasEmittedCreated:    false,
			HasEmittedInProgress: false,
			HasEmittedCompleted:  false,
			TextItemClosed:       false,
			HasStartedText:       false,
			HasStartedToolCall:   false,
			HasEmittedWebSearch:  false,
		}
	},
}

// acquireGeminiResponsesStreamState gets a Gemini responses stream state from the pool.
func acquireGeminiResponsesStreamState() *GeminiResponsesStreamState {
	state := geminiResponsesStreamStatePool.Get().(*GeminiResponsesStreamState)
	state.flush()
	return state
}

// releaseGeminiResponsesStreamState returns a Gemini responses stream state to the pool.
func releaseGeminiResponsesStreamState(state *GeminiResponsesStreamState) {
	if state != nil {
		state.flush()
		geminiResponsesStreamStatePool.Put(state)
	}
}

func (state *GeminiResponsesStreamState) flush() {
	// Clear maps
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ToolCallIDs == nil {
		state.ToolCallIDs = make(map[int]string)
	} else {
		clear(state.ToolCallIDs)
	}
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[int]string)
	} else {
		clear(state.ToolCallNames)
	}
	if state.ToolArgumentBuffers == nil {
		state.ToolArgumentBuffers = make(map[int]string)
	} else {
		clear(state.ToolArgumentBuffers)
	}
	state.CurrentOutputIndex = 0
	state.TextOutputIndex = -1
	state.MessageID = nil
	state.Model = nil
	state.ResponseID = nil
	state.SafetyRatings = nil
	state.AvgLogprobs = 0
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedCompleted = false
	state.HasEmittedInProgress = false
	state.TextItemClosed = false
	state.HasStartedText = false
	state.HasStartedToolCall = false
	state.HasEmittedWebSearch = false
	state.ServerSearchRounds = nil
	state.TextBuffer.Reset()
}

// closeTextItemIfOpen closes the text item if it's open and returns the responses.
// Returns nil if no text item was open.
func (state *GeminiResponsesStreamState) closeTextItemIfOpen(sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	if state.HasStartedText && !state.TextItemClosed {
		return closeGeminiTextItem(state, sequenceNumber)
	}
	return nil
}

// nextOutputIndex returns the current output index and increments it for the next use.
func (state *GeminiResponsesStreamState) nextOutputIndex() int {
	index := state.CurrentOutputIndex
	state.CurrentOutputIndex++
	return index
}

// generateItemID creates a unique item ID with the given suffix.
// Falls back to index-based ID if MessageID is nil.
func (state *GeminiResponsesStreamState) generateItemID(suffix string, outputIndex int) string {
	if state.MessageID != nil {
		return fmt.Sprintf("msg_%s_%s_%d", *state.MessageID, suffix, outputIndex)
	}
	return fmt.Sprintf("%s_%d", suffix, outputIndex)
}

// ToBifrostResponsesStream converts a Gemini stream event to Bifrost Responses Stream responses
func (response *GenerateContentResponse) ToBifrostResponsesStream(sequenceNumber int, state *GeminiResponsesStreamState) ([]*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError) {
	var responses []*schemas.BifrostResponsesStreamResponse

	// First event: Emit response.created and response.in_progress
	if !state.HasEmittedCreated {
		// Generate message ID
		if state.MessageID == nil {
			messageID := fmt.Sprintf("msg_%d", state.CreatedAt)
			state.MessageID = &messageID
		}

		// Set model and response ID from Gemini
		if response.ModelVersion != "" && state.Model == nil {
			state.Model = &response.ModelVersion
		}
		if response.ResponseID != "" && state.ResponseID == nil {
			state.ResponseID = &response.ResponseID
		}

		// Emit response.created
		createdResp := &schemas.BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
		}
		if state.Model != nil {
			createdResp.Model = *state.Model
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCreated,
			SequenceNumber: sequenceNumber + len(responses),
			Response:       createdResp,
		})
		state.HasEmittedCreated = true

		// Emit response.in_progress
		inProgressResp := &schemas.BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
		}
		if state.Model != nil {
			inProgressResp.Model = *state.Model
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeInProgress,
			SequenceNumber: sequenceNumber + len(responses),
			Response:       inProgressResp,
		})
		state.HasEmittedInProgress = true
	}

	// Process candidates
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]

		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			for _, part := range candidate.Content.Parts {
				partResponses := processGeminiPart(part, state, sequenceNumber+len(responses))
				responses = append(responses, partResponses...)
			}
		}

		// safetyRatings/avgLogprobs only arrive on the terminal chunk, alongside finishReason.
		if len(candidate.SafetyRatings) > 0 {
			state.SafetyRatings = candidate.SafetyRatings
		}
		if candidate.AvgLogprobs != 0 {
			state.AvgLogprobs = candidate.AvgLogprobs
		}

		// Check for finish reason (indicates end of generation)
		// Only close if we've actually started emitting content (text, tool calls, etc.)
		// This prevents emitting response.completed for empty chunks with just finishReason
		if candidate.FinishReason != "" && len(state.ItemIDs) > 0 {
			// Check for grounding metadata (web search results), or a server-side search the
			// model reported itself via toolCall parts.
			if (candidate.GroundingMetadata != nil || len(state.ServerSearchRounds) > 0) && !state.HasEmittedWebSearch {
				// Emit web search events before closing
				webSearchResponses := emitWebSearchFromGroundingMetadata(
					candidate.GroundingMetadata,
					state,
					sequenceNumber+len(responses),
				)
				responses = append(responses, webSearchResponses...)
			}

			// Close any open items
			closeResponses := closeGeminiOpenItems(state, candidate.GroundingMetadata, response.UsageMetadata, sequenceNumber+len(responses), candidate.FinishReason, candidate.FinishMessage)
			responses = append(responses, closeResponses...)
		}
	}

	return responses, nil
}

// processGeminiPart processes a single Gemini part and returns appropriate lifecycle events
func processGeminiPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	switch {
	case part.Thought && part.Text != "":
		// Reasoning/thinking content
		responses = append(responses, processGeminiThoughtPart(part, state, sequenceNumber)...)
	case part.Text != "" && !part.Thought:
		// Regular text content
		responses = append(responses, processGeminiTextPart(part, state, sequenceNumber)...)

	case part.FunctionCall != nil:
		// Function call
		responses = append(responses, processGeminiFunctionCallPart(part, state, sequenceNumber)...)

	case part.ToolCall != nil:
		// Server-side tool invocation. The search events are emitted at finish, where
		// grounding metadata supplies the sources, so only record the round here. Each call
		// is kept separately -- a model that searches twice must produce two items, not one
		// merged one. The part also carries a thoughtSignature, which still has to reach
		// the client.
		if isSearchToolType(part.ToolCall.ToolType) {
			queries := toolCallSearchQueries(part.ToolCall)
			round := GeminiServerSearchRound{CallID: part.ToolCall.ID, Queries: queries}
			if strings.EqualFold(part.ToolCall.ToolType, "GOOGLE_SEARCH_IMAGE") {
				round.ImageQueries = queries
			}
			state.ServerSearchRounds = append(state.ServerSearchRounds, round)
		}
		responses = append(responses, processGeminiThoughtSignaturePart(part, state, sequenceNumber)...)

	case part.ToolResponse != nil:
		// Result of a server-side call; carries no data Bifrost models beyond its signature.
		responses = append(responses, processGeminiThoughtSignaturePart(part, state, sequenceNumber)...)

	case part.ThoughtSignature != nil:
		// Encrypted reasoning content (thoughtSignature)
		responses = append(responses, processGeminiThoughtSignaturePart(part, state, sequenceNumber)...)

	case part.FunctionResponse != nil:
		// Function response (tool result)
		responses = append(responses, processGeminiFunctionResponsePart(part, state, sequenceNumber)...)
	case part.InlineData != nil:
		// Inline data
		responses = append(responses, processGeminiInlineDataPart(part, state, sequenceNumber)...)
	case part.FileData != nil:
		// File data
		responses = append(responses, processGeminiFileDataPart(part, state, sequenceNumber)...)
	}

	return responses
}

// processGeminiTextPart handles regular text parts
func processGeminiTextPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	var outputIndex int
	// If this is the first text, emit output_item.added and content_part.added
	if !state.HasStartedText {
		outputIndex = state.nextOutputIndex()
		state.TextOutputIndex = outputIndex // Cache the text item's output index
		itemID := state.generateItemID("item", outputIndex)
		state.ItemIDs[outputIndex] = itemID

		// Emit output_item.added
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Status: schemas.Ptr("in_progress"),
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{},
				},
			},
		})

		// Emit content_part.added
		contentIndex := 0
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Part: &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: schemas.Ptr(""),
				ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
					LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
					Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
				},
			},
		})

		state.HasStartedText = true
	} else {
		// Text already started, reuse the cached text item's output index
		outputIndex = state.TextOutputIndex
	}

	// Emit output_text.delta for the text content
	if part.Text != "" {
		itemID := state.ItemIDs[outputIndex]
		contentIndex := 0
		text := part.Text

		// Accumulate text for output_text.done
		state.TextBuffer.WriteString(text)

		streamResponse := &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Delta:          &text,
			LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
		}
		if len(part.ThoughtSignature) > 0 {
			thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
			streamResponse.Signature = &thoughtSig
		}

		responses = append(responses, streamResponse)
	}

	return responses
}

// processGeminiThoughtPart handles reasoning/thought parts
func processGeminiThoughtPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// For Gemini thoughts/reasoning, we emit them as reasoning summary text deltas
	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("reasoning", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Emit output_item.added for reasoning
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		},
	})

	// Emit reasoning summary part added
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// Emit reasoning summary text delta with the thought content
	if part.Text != "" {
		text := part.Text
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Delta:          &text,
		})
	}

	// Emit reasoning summary text done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// Emit reasoning summary part done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// Emit output_item.done for reasoning
	statusCompleted := "completed"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &statusCompleted,
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			},
		},
	})

	return responses
}

// processGeminiThoughtSignaturePart handles encrypted reasoning content (thoughtSignature)
func processGeminiThoughtSignaturePart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Create a new reasoning item for the thought signature
	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("reasoning", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Convert thoughtSignature to string
	thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)

	// A server-side toolCall/toolResponse part reaches here for its signature, but the
	// part itself has no lossless home in the canonical schema. Carry it verbatim so the
	// native GenAI stream can re-emit it instead of a bare signature-only part -- riding
	// on this item keeps it in Gemini's original part order.
	nativeParts := nativePartPayload(part)

	// Emit output_item.added for reasoning with encrypted content
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{},
				EncryptedContent: &thoughtSig,
			},
			ProviderNativeParts: nativeParts,
		},
	})

	// Emit output_item.done for reasoning (thought signature is complete)
	statusCompleted := "completed"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &statusCompleted,
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{},
				EncryptedContent: &thoughtSig,
			},
			ProviderNativeParts: nativeParts,
		},
	})

	return responses
}

// nativePartPayload serializes a server-side tool part so a native-surface integration can
// replay it byte-for-byte. Returns nil for parts the canonical schema already represents
// losslessly, so only toolCall/toolResponse pay the marshal cost.
func nativePartPayload(part *Part) json.RawMessage {
	if part == nil || (part.ToolCall == nil && part.ToolResponse == nil) {
		return nil
	}
	encoded, err := sonic.Marshal([]*Part{part})
	if err != nil {
		return nil
	}
	return json.RawMessage(encoded)
}

// nativePartsFromItem recovers the parts stashed by nativePartPayload.
func nativePartsFromItem(item *schemas.ResponsesMessage) []*Part {
	if item == nil || len(item.ProviderNativeParts) == 0 {
		return nil
	}
	var parts []*Part
	if err := sonic.Unmarshal(item.ProviderNativeParts, &parts); err != nil {
		return nil
	}
	return parts
}

// processGeminiFunctionCallPart handles function call parts
func processGeminiFunctionCallPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Start new function call item
	outputIndex := state.nextOutputIndex()

	toolUseID := part.FunctionCall.ID
	if toolUseID == "" {
		toolUseID = part.FunctionCall.Name // Fallback to name as ID
	}

	state.ItemIDs[outputIndex] = toolUseID
	state.ToolCallIDs[outputIndex] = toolUseID
	state.ToolCallNames[outputIndex] = part.FunctionCall.Name

	// Convert args to JSON string
	argsJSON := ""
	if len(part.FunctionCall.Args) > 0 {
		argsJSON = string(part.FunctionCall.Args)
	}
	state.ToolArgumentBuffers[outputIndex] = argsJSON

	// Attach thought signature to ID if present
	if len(part.ThoughtSignature) > 0 && !strings.Contains(toolUseID, thoughtSignatureSeparator) {
		encoded := base64.RawURLEncoding.EncodeToString(part.ThoughtSignature)
		toolUseID = fmt.Sprintf("%s%s%s", toolUseID, thoughtSignatureSeparator, encoded)
	}

	// Emit output_item.added for function call
	status := "in_progress"
	addedEvent := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &toolUseID,
		Item: &schemas.ResponsesMessage{
			ID:     &toolUseID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			Status: &status,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    &toolUseID,
				Name:      &part.FunctionCall.Name,
				Arguments: schemas.Ptr(""),
			},
		},
	}

	responses = append(responses, addedEvent)

	// Generate synthetic argument deltas to simulate streaming behavior
	if argsJSON != "" {
		deltaEvents := generateSyntheticFunctionCallArgumentDeltas(
			argsJSON,
			&outputIndex,
			&toolUseID,
			sequenceNumber+len(responses),
		)
		responses = append(responses, deltaEvents...)
	}

	// Gemini sends complete function calls, so emit done event after synthetic deltas
	doneEvent := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &toolUseID,
		Arguments:      &argsJSON,
		Item: &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &toolUseID,
				Name:   &part.FunctionCall.Name,
			},
		},
	}

	responses = append(responses, doneEvent)

	outputItemDone := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &toolUseID,
		Item: &schemas.ResponsesMessage{
			ID:     &toolUseID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			Status: schemas.Ptr("completed"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    &toolUseID,
				Name:      &part.FunctionCall.Name,
				Arguments: &argsJSON,
			},
		},
	}

	responses = append(responses, outputItemDone)

	delete(state.ToolArgumentBuffers, outputIndex)

	state.HasStartedToolCall = true

	return responses
}

// processGeminiFunctionResponsePart handles function response (tool result) parts
func processGeminiFunctionResponsePart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Extract output from function response
	output := extractFunctionResponseOutput(part.FunctionResponse)

	// Create new output item for the function response
	outputIndex := state.nextOutputIndex()

	responseID := part.FunctionResponse.ID
	if responseID == "" {
		responseID = part.FunctionResponse.Name // Fallback to name
	}

	itemID := fmt.Sprintf("func_resp_%s", responseID)
	state.ItemIDs[outputIndex] = itemID

	// Emit output_item.added for function call output
	status := "completed"
	item := &schemas.ResponsesMessage{
		ID:     &itemID,
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Status: &status,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &responseID,
			Output: &schemas.ResponsesToolMessageOutputStruct{
				ResponsesToolCallOutputStr: &output,
			},
		},
	}

	// Set tool name if present
	if name := strings.TrimSpace(part.FunctionResponse.Name); name != "" {
		item.ResponsesToolMessage.Name = schemas.Ptr(name)
	}

	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item:           item,
	})

	// Immediately emit output_item.done since function responses are complete
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &status,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &responseID,
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &output,
				},
			},
		},
	})
	// Add tool name if present
	if name := strings.TrimSpace(part.FunctionResponse.Name); name != "" {
		last := responses[len(responses)-1]
		if last.Item != nil && last.Item.ResponsesToolMessage != nil {
			last.Item.ResponsesToolMessage.Name = schemas.Ptr(name)
		}
	}

	return responses
}

// processGeminiInlineDataPart handles inline data parts
func processGeminiInlineDataPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Convert inline data to content block
	block := convertGeminiInlineDataToContentBlock(part.InlineData)
	if block == nil {
		return responses
	}

	// Create new output item for the inline data
	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("item", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Emit output_item.added with the inline data content block
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{*block},
			},
		},
	})

	// Emit content_part.added
	contentIndex := 0
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           block,
	})

	// Emit content_part.done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           block,
	})

	// Emit output_item.done
	statusCompleted := "completed"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &statusCompleted,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{},
			},
		},
	})

	return responses
}

// processGeminiFileDataPart handles file data parts
func processGeminiFileDataPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Convert file data to content block
	block := convertGeminiFileDataToContentBlock(part.FileData)
	if block == nil {
		return responses
	}

	// Create new output item for the file data
	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("item", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Emit output_item.added with the file data content block
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{*block},
			},
		},
	})

	// Emit content_part.added
	contentIndex := 0
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           block,
	})

	// Emit content_part.done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           block,
	})

	// Emit output_item.done
	statusCompleted := "completed"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &statusCompleted,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{},
			},
		},
	})

	return responses
}

// closeGeminiTextItem closes the text item and emits appropriate done events
func closeGeminiTextItem(state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	outputIndex := state.TextOutputIndex
	itemID := state.ItemIDs[outputIndex]
	contentIndex := 0

	// Emit output_text.done
	fullText := state.TextBuffer.String()
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Text:           &fullText,
		LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
	})

	// Emit content_part.done with accumulated text
	partText := fullText
	part := &schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesOutputMessageContentTypeText,
		Text: &partText,
		ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
			LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
			Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
		},
	}
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           part,
	})

	// Emit output_item.done with content blocks
	itemText := fullText
	doneItem := &schemas.ResponsesMessage{
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Status: schemas.Ptr("completed"),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: &itemText,
					ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
						Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
						LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
					},
				},
			},
		},
	}
	if itemID != "" {
		doneItem.ID = &itemID
	}
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item:           doneItem,
	})

	state.TextItemClosed = true

	return responses
}

// closeGeminiOpenItems closes any open items and emits the final completed event
func closeGeminiOpenItems(state *GeminiResponsesStreamState, groundingMetadata *GroundingMetadata, usage *GenerateContentResponseUsageMetadata, sequenceNumber int, finishReason FinishReason, finishMessage string) []*schemas.BifrostResponsesStreamResponse {
	if state.HasEmittedCompleted {
		return nil
	}

	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if still open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Emit annotations from grounding supports if present
	if groundingMetadata != nil && len(groundingMetadata.GroundingSupports) > 0 && state.TextOutputIndex >= 0 {
		annotationResponses := emitAnnotationsFromGroundingSupports(
			groundingMetadata,
			state,
			sequenceNumber+len(responses),
		)
		responses = append(responses, annotationResponses...)
	}

	// Close any open tool calls
	for outputIndex := range state.ToolArgumentBuffers {
		itemID := state.ItemIDs[outputIndex]
		toolCallID := state.ToolCallIDs[outputIndex]
		toolName := state.ToolCallNames[outputIndex]
		toolArgs := state.ToolArgumentBuffers[outputIndex]
		if strings.TrimSpace(toolName) == "" {
			toolName = toolCallID
		}

		// Emit output_item.done for tool call
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolCallID,
					Name:      &toolName,
					Arguments: &toolArgs,
				},
			},
		})
	}

	// For error finish reasons with a finish message, emit the error as text content BEFORE completed event
	// This ensures the error message is visible to the client
	if isErrorFinishReason(finishReason) && finishMessage != "" {
		errorText := fmt.Sprintf("Error: %s - %s", finishReason, finishMessage)
		outputIndex := state.nextOutputIndex()
		itemID := state.generateItemID("error", outputIndex)
		state.ItemIDs[outputIndex] = itemID
		contentIndex := 0

		// Emit output_item.added for error message
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Status: schemas.Ptr("in_progress"),
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{},
				},
			},
		})

		// Emit content_part.added
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Part: &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: schemas.Ptr(""),
			},
		})

		// Emit output_text.delta with the error message
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Delta:          &errorText,
		})

		// Emit output_text.done
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Text:           &errorText,
		})

		// Emit content_part.done
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Part: &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: &errorText,
			},
		})

		// Emit output_item.done for error message
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Status: schemas.Ptr("completed"),
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: &errorText,
						},
					},
				},
			},
		})
	}

	// Emit response.completed with usage
	bifrostUsage := ConvertGeminiUsageMetadataToResponsesUsage(usage)
	if state.Model != nil {
		applyGeminiSearchQueryResponsesUsage(bifrostUsage, groundingMetadata, *state.Model)
	}

	completedResp := &schemas.BifrostResponsesResponse{
		ID:        state.MessageID,
		CreatedAt: state.CreatedAt,
		Usage:     bifrostUsage,
	}
	if usage != nil {
		if t := mapGeminiTrafficTypeToBifrost(usage.TrafficType); t != nil {
			completedResp.ServiceTier = t
		} else if usage.ServiceTier != "" {
			tier := mapGeminiServiceTierToBifrost(usage.ServiceTier)
			completedResp.ServiceTier = &tier
		}
	}
	if state.Model != nil {
		completedResp.Model = *state.Model
	}

	// Set stop reason from finish reason
	if finishReason != "" {
		stopReason := ConvertGeminiFinishReasonToBifrost(finishReason)
		completedResp.StopReason = &stopReason

		// For error finish reasons, set status to failed
		if isErrorFinishReason(finishReason) {
			failedStatus := "failed"
			completedResp.Status = &failedStatus
		}
	}

	// safetyRatings, avgLogprobs, and the native responseId have no field in Bifrost's
	// OpenAI-shaped Responses schema. Preserve them here so ToGeminiResponsesStreamResponse
	// can restore them on the GenAI streamGenerateContent egress path.
	extraFields := map[string]interface{}{}
	if state.ResponseID != nil && *state.ResponseID != "" {
		extraFields["responseId"] = *state.ResponseID
	}
	if len(state.SafetyRatings) > 0 {
		extraFields["safetyRatings"] = state.SafetyRatings
	}
	if state.AvgLogprobs != 0 {
		extraFields["avgLogprobs"] = state.AvgLogprobs
	}
	if len(extraFields) > 0 {
		completedResp.ProviderExtraFields = extraFields
	}

	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCompleted,
		SequenceNumber: sequenceNumber + len(responses),
		Response:       completedResp,
	})

	state.HasEmittedCompleted = true

	return responses
}

// FinalizeGeminiResponsesStream finalizes the stream by closing any open items and emitting completed event
func FinalizeGeminiResponsesStream(state *GeminiResponsesStreamState, usage *GenerateContentResponseUsageMetadata, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	return closeGeminiOpenItems(state, nil, usage, sequenceNumber, "", "")
}

// convertGeminiSystemInstructionToResponsesMessage converts Gemini SystemInstruction to a system role message
func convertGeminiSystemInstructionToResponsesMessage(systemInstruction *Content) *schemas.ResponsesMessage {
	if systemInstruction == nil || len(systemInstruction.Parts) == 0 {
		return nil
	}

	var contentBlocks []schemas.ResponsesMessageContentBlock
	var hasTextContent bool

	for _, part := range systemInstruction.Parts {
		if part.Text != "" {
			contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: &part.Text,
			})
			hasTextContent = true
		}
	}

	if !hasTextContent {
		return nil
	}

	// If single text block, use ContentStr
	if len(contentBlocks) == 1 {
		return &schemas.ResponsesMessage{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: contentBlocks[0].Text,
			},
		}
	}

	// Multiple blocks, use ContentBlocks
	return &schemas.ResponsesMessage{
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: contentBlocks,
		},
	}
}

// stripFunctionResponseMediaRefs returns the textual payload of a Gemini functionResponse.Response
// to carry alongside reconstructed media blocks. It drops top-level keys whose value is a
// {"$ref": ...} placeholder — those reference the media we materialize as content blocks, and
// re-emitting them would re-trigger the Gemini Developer API "$ref" bug — while preserving every
// other field so multimodal tool results are not lossy. The Gemini spec lets callers use any keys
// (output, result, error, ...), not just "output". When only the conventional "output" field
// remains it is unwrapped to keep the common round-trip shape; a media-only response yields "".
func stripFunctionResponseMediaRefs(response json.RawMessage) string {
	if len(response) == 0 {
		return ""
	}
	root := providerUtils.GetJSONField(response, "@this")
	if !root.IsObject() {
		return string(response)
	}

	cleaned := []byte(response)
	remaining := 0
	for key, value := range root.Map() {
		if value.IsObject() && value.Get("$ref").Exists() {
			if updated, err := providerUtils.DeleteJSONField(cleaned, key); err == nil {
				cleaned = updated
			}
			continue
		}
		remaining++
	}

	if remaining == 0 {
		return "" // media-only result; the forward path emits an empty "output" placeholder
	}
	if remaining == 1 {
		if out := providerUtils.GetJSONField(cleaned, "output"); out.Exists() {
			return out.String()
		}
	}
	return string(cleaned)
}

func convertGeminiContentsToResponsesMessages(contents []Content) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage
	// Track function call IDs by name to match with responses
	functionCallIDs := make(map[string]string)

	for _, content := range contents {
		// Determine the role for all messages from this Content
		var role *schemas.ResponsesMessageRoleType
		switch content.Role {
		case "model":
			role = schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant)
		case "user":
			role = schemas.Ptr(schemas.ResponsesInputMessageRoleUser)
		default:
			// Default to user for unknown roles
			role = schemas.Ptr(schemas.ResponsesInputMessageRoleUser)
		}

		// Process each part - each part can become a separate message
		for _, part := range content.Parts {
			switch {
			case part.FunctionCall != nil:
				// Function call message
				argsJSON := "{}"
				if len(part.FunctionCall.Args) > 0 {
					argsJSON = string(part.FunctionCall.Args)
				}

				callID := part.FunctionCall.ID
				if callID == "" {
					callID = part.FunctionCall.Name
				}

				// Track this function call ID by name for later matching with responses
				functionCallIDs[part.FunctionCall.Name] = callID

				msg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    &callID,
						Name:      &part.FunctionCall.Name,
						Arguments: &argsJSON,
					},
				}
				messages = append(messages, msg)

				// If this part also has a thought signature, create a separate reasoning message
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					reasoningMsg := schemas.ResponsesMessage{
						Role: role,
						Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
						ResponsesReasoning: &schemas.ResponsesReasoning{
							Summary:          []schemas.ResponsesReasoningSummary{},
							EncryptedContent: &thoughtSig,
						},
					}
					messages = append(messages, reasoningMsg)
				}

			case part.FunctionResponse != nil:
				// Function response message
				responseID := part.FunctionResponse.ID
				if responseID == "" {
					// Try to find the matching function call ID by name
					if callID, ok := functionCallIDs[part.FunctionResponse.Name]; ok {
						responseID = callID
					} else {
						// Fallback to function name if no matching call found
						responseID = part.FunctionResponse.Name
					}
				}

				// Convert response to string — extract output field if present
				responseStr := ""
				if part.FunctionResponse.Response != nil {
					if r := providerUtils.GetJSONField(part.FunctionResponse.Response, "output"); r.Exists() {
						responseStr = r.String()
					} else {
						responseStr = string(part.FunctionResponse.Response)
					}
				}

				output := &schemas.ResponsesToolMessageOutputStruct{}
				if len(part.FunctionResponse.Parts) > 0 {
					// Multimodal function response (Gemini 3 series): the tool returned images/files
					// nested in functionResponse.parts. Reconstruct them as content blocks so the media
					// is preserved on the way in, instead of being collapsed to the text "output" field.
					// Mirrors the forward conversion in convertResponsesMessagesToGeminiContents.
					var blocks []schemas.ResponsesMessageContentBlock
					// Preserve the structured response text alongside the media. The Gemini spec allows
					// any keys (output, result, error, ...), so keep the whole response object minus the
					// {"$ref": ...} placeholders (those point at the media we materialize as blocks below).
					if textPayload := stripFunctionResponseMediaRefs(part.FunctionResponse.Response); textPayload != "" {
						blocks = append(blocks, schemas.ResponsesMessageContentBlock{
							Type: schemas.ResponsesInputMessageContentBlockTypeText,
							Text: &textPayload,
						})
					}
					for _, p := range part.FunctionResponse.Parts {
						var block *schemas.ResponsesMessageContentBlock
						switch {
						case p.InlineData != nil:
							block = convertGeminiInlineDataToContentBlock(p.InlineData)
						case p.FileData != nil:
							block = convertGeminiFileDataToContentBlock(p.FileData)
						}
						if block != nil {
							blocks = append(blocks, *block)
						}
					}
					if len(blocks) > 0 {
						output.ResponsesFunctionToolCallOutputBlocks = blocks
					} else {
						output.ResponsesToolCallOutputStr = &responseStr
					}
				} else {
					output.ResponsesToolCallOutputStr = &responseStr
				}

				msg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: &responseID,
						Output: output,
					},
				}

				messages = append(messages, msg)

			case part.Thought && part.Text != "":
				// Thought/reasoning text content
				msg := schemas.ResponsesMessage{
					Role: role,
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeReasoning,
								Text: &part.Text,
							},
						},
					},
				}
				messages = append(messages, msg)

			case part.Text != "":
				// Regular text message
				msg := schemas.ResponsesMessage{
					Role: role,
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: func() schemas.ResponsesMessageContentBlockType {
									if content.Role == "model" {
										return schemas.ResponsesOutputMessageContentTypeText
									}
									return schemas.ResponsesInputMessageContentBlockTypeText
								}(),
								Text: &part.Text,
							},
						},
					},
				}

				// add signature to above text content block if present
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					msg.Content.ContentBlocks[len(msg.Content.ContentBlocks)-1].Signature = &thoughtSig
				}

				messages = append(messages, msg)

			case part.InlineData != nil:
				// Handle inline data (images, audio, files)
				block := convertGeminiInlineDataToContentBlock(part.InlineData)
				if block != nil {
					msg := schemas.ResponsesMessage{
						Role: role,
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{*block},
						},
					}
					messages = append(messages, msg)
				}

			case part.FileData != nil:
				// Handle file data (URI-based)
				block := convertGeminiFileDataToContentBlock(part.FileData)
				if block != nil {
					msg := schemas.ResponsesMessage{
						Role: role,
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{*block},
						},
					}
					messages = append(messages, msg)
				}
			}
		}
	}

	return messages
}

// convertGeminiInlineDataToContentBlock converts Gemini inline data (blob) to content block
func convertGeminiInlineDataToContentBlock(blob *Blob) *schemas.ResponsesMessageContentBlock {
	if blob == nil {
		return nil
	}

	// Determine content type based on MIME type
	mimeType := blob.MIMEType
	if mimeType == "" {
		return nil
	}

	// Handle images
	if isImageMimeType(mimeType) {
		// Convert to base64 data URL
		imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, blob.Data)
		return &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: &imageURL,
			},
		}
	}

	// Handle audio
	if strings.HasPrefix(mimeType, "audio/") {
		encodedData := blob.Data
		format := mimeType
		if strings.HasPrefix(mimeType, "audio/") {
			format = mimeType[6:] // Remove "audio/" prefix
		}

		return &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeAudio,
			Audio: &schemas.ResponsesInputMessageContentBlockAudio{
				Format: format,
				Data:   encodedData,
			},
		}
	}

	// Handle other files - format as data URL
	mimeTypeForFile := mimeType
	if mimeTypeForFile == "" {
		mimeTypeForFile = "application/pdf"
	}

	filename := blob.DisplayName
	if filename == "" {
		filename = "unnamed_file"
	}

	fileDataURL := blob.Data
	if !strings.HasPrefix(fileDataURL, "data:") {
		fileDataURL = fmt.Sprintf("data:%s;base64,%s", mimeTypeForFile, fileDataURL)
	}
	return &schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileData: &fileDataURL,
			FileType: &mimeTypeForFile,
			Filename: &filename,
		},
	}
}

// convertGeminiFileDataToContentBlock converts Gemini file data (URI) to content block
func convertGeminiFileDataToContentBlock(fileData *FileData) *schemas.ResponsesMessageContentBlock {
	if fileData == nil || fileData.FileURI == "" {
		return nil
	}

	// Preserve the caller's MIME as-is; do NOT fabricate a default. A wrong default
	// (application/pdf) on a non-PDF file propagates to the outgoing request and makes
	// Gemini reject it with INVALID_ARGUMENT. An empty MIME lets Gemini use the stored type.
	mimeType := fileData.MIMEType

	// Handle images
	if isImageMimeType(mimeType) {
		return &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: &fileData.FileURI,
			},
		}
	}

	// Handle other files
	block := &schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileURL: &fileData.FileURI,
		},
	}

	// Only carry a MIME type when the caller actually provided one.
	if mimeType != "" {
		block.ResponsesInputMessageContentBlockFile.FileType = &mimeType
	}

	return block
}

// OpenAI's search_content_types values, which carry Gemini's googleSearch.searchTypes.
// Gemini's webSearch returns text results, so it maps to "text".
const (
	geminiSearchContentTypeText  = "text"
	geminiSearchContentTypeImage = "image"
)

func convertGeminiToolsToResponsesTools(tools []Tool) []schemas.ResponsesTool {
	var responsesTools []schemas.ResponsesTool

	for _, tool := range tools {
		// A single tools[] entry may legitimately carry both kinds (Gemini 3+). Convert
		// everything it holds; convertResponsesToolsToGemini decides what survives on the
		// way back out to the provider.
		if tool.GoogleSearch != nil {
			responsesTool := schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeWebSearch,
			}
			responsesTool.ResponsesToolWebSearch = &schemas.ResponsesToolWebSearch{}
			if tool.GoogleSearch.TimeRangeFilter != nil || len(tool.GoogleSearch.ExcludeDomains) > 0 {
				filters := &schemas.ResponsesToolWebSearchFilters{
					BlockedDomains: tool.GoogleSearch.ExcludeDomains,
				}
				if tool.GoogleSearch.TimeRangeFilter != nil {
					filters.TimeRangeFilter = &schemas.Interval{
						StartTime: tool.GoogleSearch.TimeRangeFilter.StartTime,
						EndTime:   tool.GoogleSearch.TimeRangeFilter.EndTime,
					}
				}
				responsesTool.ResponsesToolWebSearch.Filters = filters
			}
			if searchTypes := tool.GoogleSearch.SearchTypes; searchTypes != nil {
				var contentTypes []string
				if searchTypes.WebSearch != nil {
					contentTypes = append(contentTypes, geminiSearchContentTypeText)
				}
				if searchTypes.ImageSearch != nil {
					contentTypes = append(contentTypes, geminiSearchContentTypeImage)
				}
				responsesTool.ResponsesToolWebSearch.SearchContentTypes = contentTypes
			}
			responsesTools = append(responsesTools, responsesTool)
		}
		if len(tool.FunctionDeclarations) > 0 {
			for _, fn := range tool.FunctionDeclarations {
				responsesTool := schemas.ResponsesTool{
					Type:                  schemas.ResponsesToolTypeFunction,
					Name:                  schemas.Ptr(fn.Name),
					Description:           schemas.Ptr(fn.Description),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{},
				}
				// Convert parameters schema if present
				if fn.Parameters != nil {
					params := convertSchemaToFunctionParameters(fn.Parameters)
					responsesTool.ResponsesToolFunction.Parameters = &params
				} else if fn.ParametersJSONSchema != nil {
					raw, err := providerUtils.MarshalSorted(fn.ParametersJSONSchema)
					if err != nil {
						continue
					}
					var params schemas.ToolFunctionParameters
					if err := json.Unmarshal(raw, &params); err != nil {
						continue
					}
					responsesTool.ResponsesToolFunction.Parameters = &params
				}
				responsesTools = append(responsesTools, responsesTool)
			}
		}
	}

	return responsesTools
}

func convertGeminiToolConfigToToolChoice(toolConfig *ToolConfig) *schemas.ResponsesToolChoice {
	if toolConfig == nil || toolConfig.FunctionCallingConfig == nil {
		return nil
	}

	// Type must describe the KIND of choice. It was hardcoded to "function", which tells every
	// downstream converter "a specific function is being forced" and makes them demand a name -
	// so AUTO, NONE and a nameless ANY all produced
	// "Missing required parameter: 'tool_choice.name'". Mode and Type are also mutually
	// exclusive downstream: emitting both on a forced function yields
	// "Unknown parameter: 'tool_choice.mode'". See toolconfigforcedchoice_test.go.
	toolChoice := &schemas.ResponsesToolChoiceStruct{}
	names := toolConfig.FunctionCallingConfig.AllowedFunctionNames

	switch toolConfig.FunctionCallingConfig.Mode {
	case FunctionCallingConfigModeAny:
		toolChoice.Mode = schemas.Ptr("required")
		toolChoice.Type = schemas.ResponsesToolChoiceTypeRequired
	case FunctionCallingConfigModeNone:
		toolChoice.Mode = schemas.Ptr("none")
		toolChoice.Type = schemas.ResponsesToolChoiceTypeNone
	case FunctionCallingConfigModeAuto:
		toolChoice.Mode = schemas.Ptr("auto")
		toolChoice.Type = schemas.ResponsesToolChoiceTypeAuto
	default:
		toolChoice.Mode = schemas.Ptr("auto")
		toolChoice.Type = schemas.ResponsesToolChoiceTypeAuto
	}

	if names != nil {
		for _, functionName := range names {
			toolChoice.Tools = append(toolChoice.Tools, schemas.ResponsesToolChoiceAllowedToolDef{
				Type: string(schemas.ResponsesToolTypeFunction),
				Name: schemas.Ptr(functionName),
			})
		}
		// Under ANY the names compel a call; under AUTO they only restrict what MAY be called,
		// so only ANY narrows the type away from a bare mode.
		if toolConfig.FunctionCallingConfig.Mode == FunctionCallingConfigModeAny {
			toolChoice.Type = schemas.ResponsesToolChoiceTypeAllowedTools
		}
	}

	// Mode ANY with exactly one allowed name is Gemini's spelling of "you must call this
	// function", which is a named forced choice rather than an allowed-tools list. Mode is
	// cleared here because a forced function carries a name instead of a mode.
	if toolConfig.FunctionCallingConfig.Mode == FunctionCallingConfigModeAny && len(names) == 1 {
		toolChoice.Type = schemas.ResponsesToolChoiceTypeFunction
		toolChoice.Name = schemas.Ptr(names[0])
		// A forced function carries a name instead of a mode, and instead of an allowed-tools
		// list: sending both yields "Unknown parameter: 'tool_choice.tools'".
		toolChoice.Mode = nil
		toolChoice.Tools = nil
	}

	return &schemas.ResponsesToolChoice{
		ResponsesToolChoiceStruct: toolChoice,
	}
}

// serverSideToolParts returns the candidate's whole parts array verbatim when it contains
// server-side tool parts, and nil otherwise.
//
// Capturing the full array rather than only the toolCall/toolResponse parts is what makes
// the order restorable. Gemini interleaves text, tool and thought parts freely, and
// thought_signature carries positional context that is invalidated by reordering -- but the
// reverse path rebuilds its parts from Bifrost's Responses items, which are a different
// shape and count, so a tool part's original index has nothing to be restored into. Keeping
// the original array means the native GenAI path can replay it exactly as Gemini sent it.
func serverSideToolParts(candidate *Candidate) []*Part {
	if candidate == nil || candidate.Content == nil {
		return nil
	}
	hasToolPart := false
	for _, part := range candidate.Content.Parts {
		if part != nil && (part.ToolCall != nil || part.ToolResponse != nil) {
			hasToolPart = true
			break
		}
	}
	if !hasToolPart {
		return nil
	}
	parts := make([]*Part, 0, len(candidate.Content.Parts))
	for _, part := range candidate.Content.Parts {
		if part != nil {
			parts = append(parts, part)
		}
	}
	return parts
}

// extractServerSideToolParts recovers the parts stashed by serverSideToolParts. Handles both
// the in-memory pointers (normal path) and the JSON-decoded form, matching how the other
// ProviderExtraFields values are read back.
func extractServerSideToolParts(v any) []*Part {
	if v == nil {
		return nil
	}
	if parts, ok := v.([]*Part); ok {
		return parts
	}
	b, err := sonic.Marshal(v)
	if err != nil {
		return nil
	}
	var parts []*Part
	if err := sonic.Unmarshal(b, &parts); err != nil {
		return nil
	}
	return parts
}

// geminiCallIDWithoutSignature strips the thought-signature suffix Bifrost encodes into a
// call ID ("name_ts_base64sig"), so the same call compares equal whether it came off the
// preserved Gemini part or was rebuilt from a Bifrost item.
func geminiCallIDWithoutSignature(callID string) string {
	if idx := strings.Index(callID, thoughtSignatureSeparator); idx != -1 {
		return callID[:idx]
	}
	return callID
}

// geminiPartIdentity returns a comparison key for a Gemini part, used to tell whether a
// rebuilt part is a lossy re-derivation of one already present in the preserved array.
// Parts that carry no identifying content return "" and are always treated as distinct.
func geminiPartIdentity(part *Part) string {
	if part == nil {
		return ""
	}
	switch {
	case part.ToolCall != nil:
		return "tc:" + geminiCallIDWithoutSignature(part.ToolCall.ID) + ":" + part.ToolCall.ToolType
	case part.ToolResponse != nil:
		return "tr:" + geminiCallIDWithoutSignature(part.ToolResponse.ID) + ":" + part.ToolResponse.ToolType
	case part.FunctionCall != nil:
		return "fc:" + geminiCallIDWithoutSignature(part.FunctionCall.ID) + ":" + part.FunctionCall.Name
	case part.Text != "":
		if part.Thought {
			return "th:" + part.Text
		}
		return "tx:" + part.Text
	case len(part.ThoughtSignature) > 0:
		return "ts:" + base64.StdEncoding.EncodeToString(part.ThoughtSignature)
	}
	return ""
}

// mergePreservedGeminiParts replays Gemini's original parts in their original order, then
// appends any rebuilt part the original does not already account for.
//
// The preserved array wins on ordering because it is what Gemini actually sent -- the
// rebuilt parts are derived from Bifrost's Responses items, which lose the interleaving.
// Rebuilt parts with no counterpart in the original are still appended rather than dropped,
// so anything added downstream (a plugin rewriting content, an item Bifrost models that
// Gemini did not send as its own part) survives instead of being silently discarded.
func mergePreservedGeminiParts(preserved, rebuilt []*Part) []*Part {
	seen := make(map[string]bool, len(preserved))
	for _, part := range preserved {
		if id := geminiPartIdentity(part); id != "" {
			seen[id] = true
		}
	}

	merged := make([]*Part, 0, len(preserved)+len(rebuilt))
	merged = append(merged, preserved...)
	for _, part := range rebuilt {
		id := geminiPartIdentity(part)
		if id != "" && seen[id] {
			continue
		}
		if id != "" {
			seen[id] = true
		}
		merged = append(merged, part)
	}
	return merged
}

// webSearchCallHasSources reports whether a web_search_call item carries the grounding
// sources that rebuilding Gemini's groundingChunks depends on.
func webSearchCallHasSources(msg *schemas.ResponsesMessage) bool {
	if msg == nil || msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.Action == nil {
		return false
	}
	action := msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	return action != nil && len(action.Sources) > 0
}

// firstSearchCallIndex returns the lowest message index among the server-side search calls,
// i.e. the item grounding metadata should be merged onto. Takes the indices directly rather
// than a keyed map so ID-less rounds, which never enter that map, are still considered.
func firstSearchCallIndex(indices []int) (int, bool) {
	first, found := 0, false
	for _, idx := range indices {
		if !found || idx < first {
			first, found = idx, true
		}
	}
	return first, found
}

// reasoningFromThoughtSignature builds the encrypted-reasoning item for a part that carries
// a thoughtSignature. Server-side toolCall/toolResponse parts carry one too, and Gemini
// requires signatures back on replay, so they must not be lost just because the part matched
// a more specific case than the thoughtSignature one.
func reasoningFromThoughtSignature(part *Part) (schemas.ResponsesMessage, bool) {
	if part == nil || len(part.ThoughtSignature) == 0 {
		return schemas.ResponsesMessage{}, false
	}
	thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
	return schemas.ResponsesMessage{
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: &thoughtSig,
		},
	}, true
}

// toolCallSearchQueries extracts Google Search's queries from a server-side tool call's args
// ({"queries": ["...", ...]}). Returns nil when the shape is anything else.
func toolCallSearchQueries(call *ToolCall) []string {
	if call == nil || call.Args == nil {
		return nil
	}
	raw, ok := call.Args["queries"].([]any)
	if !ok {
		return nil
	}
	queries := make([]string, 0, len(raw))
	for _, q := range raw {
		if s, ok := q.(string); ok && s != "" {
			queries = append(queries, s)
		}
	}
	if len(queries) == 0 {
		return nil
	}
	return queries
}

// newWebSearchActionFromToolCall maps a server-side Google Search invocation onto the neutral
// web_search_call action. Sources are not available here — they arrive in groundingMetadata
// and are merged onto this item afterwards.
func newWebSearchActionFromToolCall(call *ToolCall) *schemas.ResponsesWebSearchToolCallAction {
	action := &schemas.ResponsesWebSearchToolCallAction{Type: "search"}
	queries := toolCallSearchQueries(call)
	if len(queries) > 0 {
		action.Queries = queries
		action.Query = schemas.Ptr(queries[0])
	}
	if call != nil && strings.EqualFold(call.ToolType, "GOOGLE_SEARCH_IMAGE") {
		action.ImageQueries = queries
	}
	return action
}

// textBlockOf returns the text content block of messages[idx], or nil when idx is
// out of range or the message carries no text block.
func textBlockOf(messages []schemas.ResponsesMessage, idx int) *schemas.ResponsesOutputMessageContentText {
	if idx < 0 || idx >= len(messages) || messages[idx].Content == nil {
		return nil
	}
	for i := range messages[idx].Content.ContentBlocks {
		block := &messages[idx].Content.ContentBlocks[i]
		if block.Type == schemas.ResponsesOutputMessageContentTypeText && block.ResponsesOutputMessageContentText != nil {
			return block.ResponsesOutputMessageContentText
		}
	}
	return nil
}

// groundingChunkSource converts a grounding chunk to a neutral search source. Image chunks
// attribute to the containing page, as Google's display terms require, and carry the asset
// URL alongside. Reports false for chunk types that expose no citable URL.
func groundingChunkSource(chunk *GroundingChunk) (schemas.ResponsesWebSearchToolCallActionSearchSource, bool) {
	if chunk == nil {
		return schemas.ResponsesWebSearchToolCallActionSearchSource{}, false
	}
	source := schemas.ResponsesWebSearchToolCallActionSearchSource{Type: "url"}

	switch {
	case chunk.Web != nil && chunk.Web.URI != "":
		source.URL = chunk.Web.URI
		if chunk.Web.Title != "" {
			source.Title = schemas.Ptr(chunk.Web.Title)
		}
	case chunk.Image != nil && chunk.Image.SourceURI != "":
		source.URL = chunk.Image.SourceURI
		if chunk.Image.Title != "" {
			source.Title = schemas.Ptr(chunk.Image.Title)
		}
		if chunk.Image.ImageURI != "" {
			source.ImageURL = schemas.Ptr(chunk.Image.ImageURI)
		}
		if chunk.Image.Domain != "" {
			source.Domain = schemas.Ptr(chunk.Image.Domain)
		}
	default:
		return source, false
	}

	return source, true
}

// Helper functions for Responses conversion
// convertGeminiCandidatesToResponsesOutput converts Gemini candidates to Responses output messages
func convertGeminiCandidatesToResponsesOutput(candidates []*Candidate) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage

	for _, candidate := range candidates {
		if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
			continue
		}

		// Index into messages of this candidate's first text message; grounding
		// annotations are attached to it once all parts have been converted.
		textMessageIdx := -1

		// Index from server-side tool-call ID to the web_search_call item it opened.
		searchCallIndexByID := map[string]int{}
		// ToolCall.ID and ToolResponse.ID are both omitempty, so Gemini can leave them out.
		// Those rounds cannot pair by ID -- they pair by position instead: this FIFO holds
		// the message indices of ID-less calls still waiting for their response.
		var anonymousSearchCalls []int
		// Every web_search_call item's index in emission order, keyed or not, so grounding
		// metadata can find the first one regardless of whether IDs were present.
		var searchCallIndices []int

		for _, part := range candidate.Content.Parts {
			// Handle different types of parts
			switch {
			case part.Thought:
				// Thinking/reasoning message.
				//
				// The signature has to come across with the text. Gemini 3 puts
				// thoughtSignature on the thought part itself, and requires it back
				// on replay -- there is a dedicated finish reason for its absence
				// (FinishReasonMissingThoughtSignature). Reading only part.Text
				// dropped it on the floor, so a client could never send it back.
				//
				// Emitted even when the text is empty: a signature-only thought
				// part is a real Gemini shape, and skipping it loses the one field
				// the next turn actually needs.
				if part.Text != "" || len(part.ThoughtSignature) > 0 {
					text := part.Text
					msg := schemas.ResponsesMessage{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeReasoning,
									Text: &text,
								},
							},
						},
						Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					}
					if len(part.ThoughtSignature) > 0 {
						// Stored base64-encoded, which is the form
						// encrypted_content carries on the wire and the form
						// thoughtSignatureFromEncryptedContent decodes on the way
						// back out -- so the round trip is symmetric by construction.
						encoded := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
						msg.ResponsesReasoning = &schemas.ResponsesReasoning{
							Summary:          []schemas.ResponsesReasoningSummary{},
							EncryptedContent: &encoded,
						}
						msg.Content.ContentBlocks[0].Signature = &encoded
					}
					messages = append(messages, msg)
				}

			case part.Text != "":
				// Regular text message
				msg := schemas.ResponsesMessage{
					ID:     schemas.Ptr("msg_" + providerUtils.GetRandomString(50)),
					Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Status: schemas.Ptr("completed"),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: &part.Text,
								ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
									LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
									Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
								},
							},
						},
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				// add signature to above text content block if present
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					msg.Content.ContentBlocks[len(msg.Content.ContentBlocks)-1].Signature = &thoughtSig
				}
				messages = append(messages, msg)
				if textMessageIdx < 0 {
					textMessageIdx = len(messages) - 1
				}

			case part.FunctionCall != nil:
				// Function call message
				// Convert Args to JSON string if it's not already a string
				argumentsStr := ""
				if len(part.FunctionCall.Args) > 0 {
					argumentsStr = string(part.FunctionCall.Args)
				}

				callID := part.FunctionCall.ID
				if strings.TrimSpace(callID) == "" {
					callID = part.FunctionCall.Name
				}

				// Attach thought signature to callID (same as streaming path)
				if len(part.ThoughtSignature) > 0 && !strings.Contains(callID, thoughtSignatureSeparator) {
					thoughtSig := base64.RawURLEncoding.EncodeToString(part.ThoughtSignature)
					callID = fmt.Sprintf("%s%s%s", callID, thoughtSignatureSeparator, thoughtSig)
				}

				name := part.FunctionCall.Name
				toolMsg := &schemas.ResponsesToolMessage{
					CallID:    &callID,
					Name:      &name,
					Arguments: &argumentsStr,
				}
				msg := schemas.ResponsesMessage{
					ID:                   schemas.Ptr("fc_" + providerUtils.GetRandomString(50)),
					Role:                 schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status:               schemas.Ptr("completed"),
					ResponsesToolMessage: toolMsg,
				}
				messages = append(messages, msg)

			case part.FunctionResponse != nil:
				// Function response message
				output := extractFunctionResponseOutput(part.FunctionResponse)

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: schemas.Ptr(part.FunctionResponse.ID),
						Output: &schemas.ResponsesToolMessageOutputStruct{
							ResponsesToolCallOutputStr: &output,
						},
					},
				}

				// Also set the tool name if present (Gemini associates on name)
				if name := strings.TrimSpace(part.FunctionResponse.Name); name != "" {
					msg.ResponsesToolMessage.Name = schemas.Ptr(name)
				} else {
					// set name from call id
					// if it contains a thought signature, remove it
					if strings.Contains(part.FunctionResponse.ID, thoughtSignatureSeparator) {
						parts := strings.SplitN(part.FunctionResponse.ID, thoughtSignatureSeparator, 2)
						if len(parts) == 2 {
							name := parts[0]
							msg.ResponsesToolMessage.Name = schemas.Ptr(name)
						}
					} else {
						msg.ResponsesToolMessage.Name = schemas.Ptr(part.FunctionResponse.ID)
					}
				}
				messages = append(messages, msg)

			case part.InlineData != nil:
				// Handle inline data (images, audio, etc.)
				contentBlocks := []schemas.ResponsesMessageContentBlock{
					{
						Type: func() schemas.ResponsesMessageContentBlockType {
							if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
								return schemas.ResponsesInputMessageContentBlockTypeImage
							} else if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
								return schemas.ResponsesInputMessageContentBlockTypeAudio
							}
							return schemas.ResponsesInputMessageContentBlockTypeText
						}(),
						ResponsesInputMessageContentBlockImage: func() *schemas.ResponsesInputMessageContentBlockImage {
							if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
								return &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: schemas.Ptr("data:" + part.InlineData.MIMEType + ";base64," + part.InlineData.Data),
								}
							}
							return nil
						}(),
						Audio: func() *schemas.ResponsesInputMessageContentBlockAudio {
							if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
								// Extract format from MIME type (e.g., "audio/wav" -> "wav")
								format := strings.TrimPrefix(part.InlineData.MIMEType, "audio/")
								return &schemas.ResponsesInputMessageContentBlockAudio{
									Format: format,
									Data:   part.InlineData.Data,
								}
							}
							return nil
						}(),
					},
				}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: contentBlocks,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)

			case part.FileData != nil:
				// Handle file data
				block := schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						FileURL: schemas.Ptr(part.FileData.FileURI),
					},
				}
				if strings.HasPrefix(part.FileData.MIMEType, "image/") {
					block.Type = schemas.ResponsesInputMessageContentBlockTypeImage
					block.ResponsesInputMessageContentBlockImage = &schemas.ResponsesInputMessageContentBlockImage{
						ImageURL: schemas.Ptr(part.FileData.FileURI),
					}
				}
				contentBlocks := []schemas.ResponsesMessageContentBlock{block}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: contentBlocks,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)

			case part.CodeExecutionResult != nil:
				// Handle code execution results
				output := part.CodeExecutionResult.Output
				if part.CodeExecutionResult.Outcome != OutcomeOK {
					output = "Error: " + output
				}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: &output,
							},
						},
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
				}
				messages = append(messages, msg)

			case part.ExecutableCode != nil:
				// Handle executable code
				codeContent := "```" + part.ExecutableCode.Language + "\n" + part.ExecutableCode.Code + "\n```"

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: &codeContent,
							},
						},
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)
			case part.ToolCall != nil:
				// Server-side tool invocation (Google executed it; we only report it).
				// The part also carries a thoughtSignature, so keep emitting the reasoning
				// item the thoughtSignature case below would have produced.
				if msg, ok := reasoningFromThoughtSignature(part); ok {
					messages = append(messages, msg)
				}
				if !isSearchToolType(part.ToolCall.ToolType) {
					// Unmapped built-in tool: preserved verbatim on the native path above,
					// but Bifrost has no neutral item type for it.
					break
				}
				// An ID-less round still needs a distinct item ID -- keying the map on ""
				// would collapse every such round onto one entry, leaving all but the last
				// stuck in_progress and every emitted item carrying an empty ID. Mirrors
				// the streaming path's generateItemID("ws", outputIndex) fallback.
				itemID := part.ToolCall.ID
				if itemID == "" {
					itemID = fmt.Sprintf("ws_%d", len(messages))
					anonymousSearchCalls = append(anonymousSearchCalls, len(messages))
				} else {
					searchCallIndexByID[itemID] = len(messages)
				}
				searchCallIndices = append(searchCallIndices, len(messages))
				messages = append(messages, schemas.ResponsesMessage{
					ID:     schemas.Ptr(itemID),
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status: schemas.Ptr("in_progress"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						Action: &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: newWebSearchActionFromToolCall(part.ToolCall),
						},
					},
				})

			case part.ToolResponse != nil:
				// Result of a server-side ToolCall — completes the item its ID opened.
				if msg, ok := reasoningFromThoughtSignature(part); ok {
					messages = append(messages, msg)
				}
				if part.ToolResponse.ID != "" {
					if idx, ok := searchCallIndexByID[part.ToolResponse.ID]; ok && idx < len(messages) {
						messages[idx].Status = schemas.Ptr("completed")
					}
				} else if len(anonymousSearchCalls) > 0 {
					// No ID to match on: Gemini emits each round's response after its call,
					// so the oldest still-open ID-less call is the one this completes.
					idx := anonymousSearchCalls[0]
					anonymousSearchCalls = anonymousSearchCalls[1:]
					if idx < len(messages) {
						messages[idx].Status = schemas.Ptr("completed")
					}
				}

			case part.ThoughtSignature != nil:
				// Handle thought signature
				thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary:          []schemas.ResponsesReasoningSummary{},
						EncryptedContent: &thoughtSig,
					},
				}
				messages = append(messages, msg)
			}
		}

		// check if gemini used google search tool
		if candidate.GroundingMetadata != nil {
			webSearchmessage := schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Action: &schemas.ResponsesToolMessageActionStruct{
						ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
							Type:    "search",
							Queries: candidate.GroundingMetadata.WebSearchQueries,
						},
					},
				},
			}
			if len(candidate.GroundingMetadata.WebSearchQueries) > 0 {
				webSearchmessage.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Query = schemas.Ptr(candidate.GroundingMetadata.WebSearchQueries[0])
			}
			if len(candidate.GroundingMetadata.ImageSearchQueries) > 0 {
				webSearchmessage.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.ImageQueries = candidate.GroundingMetadata.ImageSearchQueries
			}

			sources := []schemas.ResponsesWebSearchToolCallActionSearchSource{}
			for _, chunk := range candidate.GroundingMetadata.GroundingChunks {
				if source, ok := groundingChunkSource(chunk); ok {
					sources = append(sources, source)
				}
			}

			if len(sources) > 0 {
				webSearchmessage.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Sources = sources
			}

			// When the model reported the search itself via server-side toolCall parts, that
			// item is authoritative — it carries the real call ID. Merge grounding's richer
			// data (sources, and any queries the call did not spell out) onto it rather than
			// emitting a second web_search_call for the same search.
			if idx, ok := firstSearchCallIndex(searchCallIndices); ok && idx < len(messages) {
				if existing := messages[idx].ResponsesToolMessage; existing != nil && existing.Action != nil {
					if action := existing.Action.ResponsesWebSearchToolCallAction; action != nil {
						if len(sources) > 0 {
							action.Sources = sources
						}
						if len(action.Queries) == 0 {
							action.Queries = candidate.GroundingMetadata.WebSearchQueries
							if len(action.Queries) > 0 {
								action.Query = schemas.Ptr(action.Queries[0])
							}
						}
						if len(action.ImageQueries) == 0 {
							action.ImageQueries = candidate.GroundingMetadata.ImageSearchQueries
						}
					}
				}
			} else {
				messages = append(messages, webSearchmessage)
			}

			// create a annotations message for grounding supports
			if len(candidate.GroundingMetadata.GroundingSupports) > 0 {
				annotations := []schemas.ResponsesOutputMessageContentTextAnnotation{}
				for _, support := range candidate.GroundingMetadata.GroundingSupports {
					if support.Segment == nil {
						continue
					}
					// One annotation per (support, chunk) pair so multi-source segments keep every source.
					for _, chunkIdx := range support.GroundingChunkIndices {
						if chunkIdx < 0 || int(chunkIdx) >= len(candidate.GroundingMetadata.GroundingChunks) {
							continue
						}
						source, ok := groundingChunkSource(candidate.GroundingMetadata.GroundingChunks[chunkIdx])
						if !ok {
							continue
						}
						annotation := schemas.ResponsesOutputMessageContentTextAnnotation{
							Type:       "url_citation",
							Text:       schemas.Ptr(support.Segment.Text),
							StartIndex: schemas.Ptr(int(support.Segment.StartIndex)),
							EndIndex:   schemas.Ptr(int(support.Segment.EndIndex)),
							URL:        schemas.Ptr(source.URL),
						}
						annotation.Title = source.Title
						annotations = append(annotations, annotation)
					}
				}
				// Attach to the candidate's text block: segment offsets index into that text,
				// and it is where the streaming path puts them. Only when the candidate
				// produced no text (offsets would reference nothing) fall back to a
				// standalone message so the citations are not dropped.
				if block := textBlockOf(messages, textMessageIdx); block != nil {
					block.Annotations = append(block.Annotations, annotations...)
				} else if len(annotations) > 0 {
					messages = append(messages, schemas.ResponsesMessage{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Status: schemas.Ptr("completed"),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr(""),
									ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
										LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
										Annotations: annotations,
									},
								},
							},
						},
					})
				}
			}

			// Emit rendered content if present
			if candidate.GroundingMetadata.SearchEntryPoint != nil &&
				candidate.GroundingMetadata.SearchEntryPoint.RenderedContent != "" {
				renderedContentMessage := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Status: schemas.Ptr("completed"),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeRenderedContent,
								ResponsesOutputMessageContentRenderedContent: &schemas.ResponsesOutputMessageContentRenderedContent{
									RenderedContent: candidate.GroundingMetadata.SearchEntryPoint.RenderedContent,
								},
							},
						},
					},
				}
				messages = append(messages, renderedContentMessage)
			}
		}
	}

	return messages
}

// convertTextConfigToGenerationConfig converts ResponsesTextConfig to Gemini's GenerationConfig fields
func convertTextConfigToGenerationConfig(textConfig *schemas.ResponsesTextConfig, config *GenerationConfig) error {
	if textConfig == nil || config == nil {
		return nil
	}

	if textConfig.Format == nil {
		return nil
	}

	switch textConfig.Format.Type {
	case "json_schema":
		config.ResponseMIMEType = "application/json"
		if textConfig.Format.JSONSchema != nil {
			schema, err := reconstructSchemaFromJSONSchema(textConfig.Format.JSONSchema)
			if err != nil {
				return err
			}
			if schema != nil {
				config.ResponseJSONSchema = schema
			}
			// no schema, mime type remains as is
		}

	case "json_object":
		config.ResponseMIMEType = "application/json"

	case "text":
		config.ResponseMIMEType = "text/plain"
	}
	return nil
}

// reconstructSchemaFromJSONSchema rebuilds a schema map from ResponsesTextConfigFormatJSONSchema
func reconstructSchemaFromJSONSchema(jsonSchema *schemas.ResponsesTextConfigFormatJSONSchema) (interface{}, error) {
	composite, acceptAll, err := jsonSchema.CompositeSchema()
	if err != nil {
		return nil, err
	}
	if composite != nil {
		// Composite object schema: use it directly. Normalize via the
		// OrderedMap-aware path so the client's key order survives end-to-end.
		return normalizeSchemaValueForGemini(composite), nil
	}
	if acceptAll {
		// Boolean schema `true` accepts any value. responseSchema must be a
		// Schema object, so the widest representable form is an unconstrained
		// object.
		return map[string]interface{}{"type": "object"}, nil
	}

	// New format: Schema is spread across individual fields
	schema := make(map[string]interface{})

	if jsonSchema.Defs != nil {
		schema["$defs"] = *jsonSchema.Defs
	}

	if jsonSchema.Type != nil {
		schema["type"] = *jsonSchema.Type
	}

	if jsonSchema.Properties != nil {
		schema["properties"] = *jsonSchema.Properties
	}

	if len(jsonSchema.Required) > 0 {
		schema["required"] = jsonSchema.Required
	}

	if jsonSchema.Description != nil {
		schema["description"] = *jsonSchema.Description
	}

	if jsonSchema.AdditionalProperties != nil {
		schema["additionalProperties"] = *jsonSchema.AdditionalProperties
	}

	if jsonSchema.Name != nil {
		schema["title"] = *jsonSchema.Name
	}

	if len(jsonSchema.PropertyOrdering) > 0 {
		schema["propertyOrdering"] = jsonSchema.PropertyOrdering
	}

	// Return nil if no fields were populated
	if len(schema) == 0 {
		return nil, nil
	}

	// Normalize the schema for Gemini compatibility (handle union types, etc.)
	return normalizeSchemaForGemini(schema), nil
}

// convertParamsToGenerationConfigResponses converts ChatParameters to GenerationConfig for Responses
func (r *GeminiGenerationRequest) convertParamsToGenerationConfigResponses(params *schemas.ResponsesParameters, capModel string) (GenerationConfig, error) {
	config := GenerationConfig{}

	if params.Temperature != nil {
		config.Temperature = schemas.Ptr(float64(*params.Temperature))
	}
	if params.TopP != nil {
		config.TopP = schemas.Ptr(float64(*params.TopP))
	}
	if params.MaxOutputTokens != nil {
		config.MaxOutputTokens = int32(*params.MaxOutputTokens)
	}
	// Only set ThinkingConfig if the model actually supports thinking
	if params.Reasoning != nil && supportsThinkingConfig(capModel) {
		config.ThinkingConfig = &GenerationConfigThinkingConfig{
			IncludeThoughts: true,
		}

		hasMaxTokens := params.Reasoning.MaxTokens != nil
		hasEffort := params.Reasoning.Effort != nil
		supportsLevel := isGemini3Plus(capModel) // Check if model is 3.0+

		// PRIORITY RULE: If both max_tokens and effort are present, use ONLY max_tokens (budget)
		// This ensures we send only thinkingBudget to Gemini, not thinkingLevel

		// Handle "none" effort explicitly (only if max_tokens not present)
		if !hasMaxTokens && hasEffort && *params.Reasoning.Effort == "none" {
			setThinkingBudgetZeroIfSupported(&config, capModel)
		} else if hasMaxTokens {
			// User provided max_tokens - use thinkingBudget (all Gemini models support this)
			// If both max_tokens and effort are present, we ignore effort and use ONLY max_tokens
			budget := *params.Reasoning.MaxTokens
			switch budget {
			case 0:
				setThinkingBudgetZeroIfSupported(&config, capModel)
			case DynamicReasoningBudget: // Special case: -1 means dynamic budget
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(DynamicReasoningBudget))
			default:
				if err := validateThinkingBudget(capModel, budget); err != nil {
					return config, err
				}
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(budget))
			}
		} else if hasEffort {
			// User provided effort only (no max_tokens)
			if supportsLevel {
				// Gemini 3.0+ - use thinkingLevel (more native)
				if level := effortToThinkingLevel(*params.Reasoning.Effort, capModel); level != "" {
					config.ThinkingConfig.ThinkingLevel = schemas.Ptr(level)
				}
			} else {
				maxTokens := providerUtils.GetMaxOutputTokensOrDefault(capModel, DefaultCompletionMaxTokens)
				if config.MaxOutputTokens > 0 {
					maxTokens = int(config.MaxOutputTokens)
				}
				budgetRange := getThinkingBudgetRange(capModel, maxTokens)
				// Gemini < 3.0 - must convert effort to budget
				budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(
					*params.Reasoning.Effort,
					budgetRange.Min,
					budgetRange.Max,
				)
				if err == nil {
					config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(budgetTokens))
				}
			}
		}
	}
	if params.Text != nil {
		if err := convertTextConfigToGenerationConfig(params.Text, &config); err != nil {
			return config, err
		}
	}

	if params.ExtraParams != nil {
		if topK, ok := params.ExtraParams["top_k"]; ok {
			delete(params.ExtraParams, "top_k")
			if val, success := schemas.SafeExtractInt(topK); success {
				config.TopK = schemas.Ptr(val)
			}
		}
		if frequencyPenalty, ok := params.ExtraParams["frequency_penalty"]; ok {
			delete(params.ExtraParams, "frequency_penalty")
			if val, success := schemas.SafeExtractFloat64(frequencyPenalty); success {
				config.FrequencyPenalty = schemas.Ptr(val)
			}
		}
		if presencePenalty, ok := params.ExtraParams["presence_penalty"]; ok {
			delete(params.ExtraParams, "presence_penalty")
			if val, success := schemas.SafeExtractFloat64(presencePenalty); success {
				config.PresencePenalty = schemas.Ptr(val)
			}
		}
		if stopSequences, ok := params.ExtraParams["stop_sequences"]; ok {
			delete(params.ExtraParams, "stop_sequences")
			if val, success := schemas.SafeExtractStringSlice(stopSequences); success {
				config.StopSequences = val
			}
		}
		if mediaResolution, ok := params.ExtraParams["media_resolution"]; ok {
			delete(params.ExtraParams, "media_resolution")
			if val, success := schemas.SafeExtractString(mediaResolution); success {
				config.MediaResolution = val
			}
		}

	}

	return config, nil
}

// modelSupportsToolCombination reports whether a model can accept built-in tools (Google
// Search) and function declarations in the same request. Tool combination arrived with
// Gemini 3; 2.x and older reject the mix with
// "Multiple tools are supported only when they are all search tools".
//
// Unknown or non-Gemini model names return false on purpose. Guessing "capable" costs a
// hard 400, guessing "not capable" costs only the built-in tool, so the safe default is to
// drop rather than to send.
func modelSupportsToolCombination(model string) bool {
	name := strings.ToLower(model)
	// Strip a provider prefix ("vertex/gemini-3.6-flash") and any publisher path.
	if idx := strings.LastIndex(name, "/"); idx != -1 {
		name = name[idx+1:]
	}
	const prefix = "gemini-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	// Read the leading major version: "3-flash-preview" -> 3, "2.5-pro" -> 2.
	rest := name[len(prefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return false
	}
	major, err := strconv.Atoi(rest[:end])
	if err != nil {
		return false
	}
	return major >= 3
}

// convertResponsesToolsToGemini converts Responses tools to Gemini tools.
// The Gemini Developer API rejects function declarations sent alongside Google Search
// unless includeServerSideToolInvocations opts into tool combination mode, so without it
// one of the two has to go. Function declarations win: they carry the caller's (or the MCP
// gateway's) tools, which the model cannot invoke at all if they never reach the wire,
// whereas losing Google Search only costs grounding.
//
// Vertex AI accepts the combination without the flag, but only on models that support tool
// combination at all — Gemini 3 and newer. Sending both kinds to an older Vertex model is
// rejected outright with "Multiple tools are supported only when they are all search tools",
// so the drop still applies there: a degraded answer beats a 400.
func convertResponsesToolsToGemini(tools []schemas.ResponsesTool, includeServerSideToolInvocations bool, provider schemas.ModelProvider, model string) ([]Tool, error) {
	var functionDeclarations []*FunctionDeclaration
	var googleSearch *GoogleSearch

	allowsMixedTools := includeServerSideToolInvocations ||
		(provider == schemas.Vertex && modelSupportsToolCombination(model))

	hasFunctionTool := false

	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeFunction && tool.ResponsesToolFunction != nil && tool.Name != nil {
			hasFunctionTool = true
			break
		}
	}

	dropGoogleSearch := hasFunctionTool && !allowsMixedTools

	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeFunction {
			// Extract function information from ResponsesExtendedTool
			if tool.ResponsesToolFunction != nil {
				if tool.Name != nil && tool.ResponsesToolFunction != nil {
					funcDecl := &FunctionDeclaration{
						Name: *tool.Name,
						Description: func() string {
							if tool.Description != nil {
								return *tool.Description
							}
							return ""
						}(),
					}
					if tool.ResponsesToolFunction.Parameters != nil {
						raw, err := providerUtils.MarshalSorted(tool.ResponsesToolFunction.Parameters)
						if err != nil {
							return []Tool{}, fmt.Errorf("marshal tool %q parameters: %w", *tool.Name, err)
						}
						funcDecl.ParametersJSONSchema = json.RawMessage(raw)
					}
					functionDeclarations = append(functionDeclarations, funcDecl)
				}
			}
		}
		if tool.Type == schemas.ResponsesToolTypeWebSearch && !dropGoogleSearch {
			googleSearch = &GoogleSearch{}
			if tool.ResponsesToolWebSearch != nil && tool.ResponsesToolWebSearch.Filters != nil {
				if tool.ResponsesToolWebSearch.Filters.TimeRangeFilter != nil {
					googleSearch.TimeRangeFilter = &Interval{
						StartTime: tool.ResponsesToolWebSearch.Filters.TimeRangeFilter.StartTime,
						EndTime:   tool.ResponsesToolWebSearch.Filters.TimeRangeFilter.EndTime,
					}
				}
				if len(tool.ResponsesToolWebSearch.Filters.BlockedDomains) > 0 {
					googleSearch.ExcludeDomains = tool.ResponsesToolWebSearch.Filters.BlockedDomains
				}
			}
			if tool.ResponsesToolWebSearch != nil && len(tool.ResponsesToolWebSearch.SearchContentTypes) > 0 {
				searchTypes := &SearchTypes{}
				for _, contentType := range tool.ResponsesToolWebSearch.SearchContentTypes {
					switch contentType {
					case geminiSearchContentTypeText:
						searchTypes.WebSearch = &WebSearch{}
					case geminiSearchContentTypeImage:
						searchTypes.ImageSearch = &ImageSearch{}
					}
				}
				if searchTypes.WebSearch != nil || searchTypes.ImageSearch != nil {
					googleSearch.SearchTypes = searchTypes
				}
			}
		}
	}

	// One Tool entry per tool type, matching Google's documented tool-combination shape.
	var geminiTools []Tool
	if len(functionDeclarations) > 0 {
		geminiTools = append(geminiTools, Tool{FunctionDeclarations: functionDeclarations})
	}
	if googleSearch != nil {
		geminiTools = append(geminiTools, Tool{GoogleSearch: googleSearch})
	}
	if len(geminiTools) == 0 {
		return []Tool{}, nil
	}
	return geminiTools, nil
}

// convertResponsesToolChoiceToGemini converts Responses tool choice to Gemini tool config
func convertResponsesToolChoiceToGemini(toolChoice *schemas.ResponsesToolChoice) *ToolConfig {
	config := &ToolConfig{}

	if toolChoice.ResponsesToolChoiceStruct != nil {
		funcConfig := &FunctionCallingConfig{}
		ext := toolChoice.ResponsesToolChoiceStruct

		if ext.Mode != nil {
			switch *ext.Mode {
			case "auto":
				funcConfig.Mode = FunctionCallingConfigModeAuto
			case "required":
				funcConfig.Mode = FunctionCallingConfigModeAny
			case "none":
				funcConfig.Mode = FunctionCallingConfigModeNone
			}
		}

		// Name and Tools describe the SAME allowed set - Name is the forced single function,
		// Tools the allowed list - and a forced choice legitimately carries both (allowed set
		// {X}, forced X). Appending them blindly duplicated the name, so collect through a seen
		// set and keep first-seen order.
		seen := make(map[string]struct{}, len(ext.Tools)+1)
		appendAllowed := func(name string) {
			if _, dup := seen[name]; dup {
				return
			}
			seen[name] = struct{}{}
			funcConfig.AllowedFunctionNames = append(funcConfig.AllowedFunctionNames, name)
		}

		if ext.Name != nil {
			if ext.Mode == nil {
				funcConfig.Mode = FunctionCallingConfigModeAny
			}
			appendAllowed(*ext.Name)
		}

		if len(ext.Tools) > 0 {
			if ext.Mode == nil {
				funcConfig.Mode = FunctionCallingConfigModeAny
			}
			for _, tool := range ext.Tools {
				if tool.Name != nil {
					appendAllowed(*tool.Name)
				}
			}
		}

		config.FunctionCallingConfig = funcConfig
		return config
	}

	// Handle string-based tool choice modes
	if toolChoice.ResponsesToolChoiceStr != nil {
		funcConfig := &FunctionCallingConfig{}
		switch *toolChoice.ResponsesToolChoiceStr {
		case "none":
			funcConfig.Mode = FunctionCallingConfigModeNone
		case "required", "any":
			funcConfig.Mode = FunctionCallingConfigModeAny
		default: // "auto" or any other value
			funcConfig.Mode = FunctionCallingConfigModeAuto
		}
		config.FunctionCallingConfig = funcConfig
	}

	return config
}

// convertResponsesMessagesToGeminiContents converts Responses messages to Gemini contents.
// model is used to gate features that are only valid on Gemini 3+ (e.g. multimodal function
// responses, where a tool returns images/files nested in functionResponse.parts). provider
// distinguishes Vertex AI from the Gemini Developer API, which differ in how multimodal
// function responses must be referenced (see the FunctionCallOutput handling below).
func convertResponsesMessagesToGeminiContents(messages []schemas.ResponsesMessage, model string, provider schemas.ModelProvider, allowedImageURLSchemes ...string) ([]Content, *Content, error) {
	if len(allowedImageURLSchemes) == 0 {
		allowedImageURLSchemes = defaultGeminiImageURLSchemes
	}

	isVertex := provider == schemas.Vertex
	// if only system / developer message is there, convert it to user message (since openai allows it)
	if len(messages) == 1 && messages[0].Role != nil && (*messages[0].Role == schemas.ResponsesInputMessageRoleSystem || *messages[0].Role == schemas.ResponsesInputMessageRoleDeveloper) {
		content := Content{Role: "user"}
		if messages[0].Content != nil {
			if messages[0].Content.ContentStr != nil && *messages[0].Content.ContentStr != "" {
				content.Parts = append(content.Parts, &Part{
					Text: *messages[0].Content.ContentStr,
				})
			}
			if messages[0].Content.ContentBlocks != nil {
				for _, block := range messages[0].Content.ContentBlocks {
					part, err := convertContentBlockToGeminiPart(block, allowedImageURLSchemes...)
					if err != nil {
						return nil, nil, fmt.Errorf("failed to convert system message content block: %w", err)
					}
					if part != nil {
						content.Parts = append(content.Parts, part)
					}
				}
			}
		}
		if len(content.Parts) > 0 {
			return []Content{content}, nil, nil
		}
	}

	var contents []Content
	var systemInstruction *Content

	// Build a map from callID → function name by scanning function_call messages.
	callIDToName := make(map[string]string)
	for i := range messages {
		m := &messages[i]
		if m.Type != nil && *m.Type == schemas.ResponsesMessageTypeFunctionCall &&
			m.ResponsesToolMessage != nil &&
			m.ResponsesToolMessage.CallID != nil &&
			m.ResponsesToolMessage.Name != nil {
			if name := strings.TrimSpace(*m.ResponsesToolMessage.Name); name != "" {
				callIDToName[*m.ResponsesToolMessage.CallID] = name
			}
		}
	}

	// Track consecutive function call output messages to group them for parallel function calling
	// According to Gemini docs, all function responses must be in a single message
	var pendingFunctionResponseParts []*Part

	for i, msg := range messages {
		// Standalone reasoning messages carry the model's thought blocks. Their
		// SIGNATURE is picked up by the look-ahead on the preceding function
		// call, so only the text is emitted here - sending the signature again
		// would put the same value on the wire twice.
		//
		// Skipping these outright, as this did, meant reasoning text never
		// reached Gemini on this path at all. The thinking guide is explicit
		// that history must keep its thought blocks intact ("You MUST always
		// resend all thought blocks exactly as they were received from the
		// model", https://ai.google.dev/gemini-api/docs/thinking), and a block
		// stripped to its signature is not the block that was received.
		//
		// A reasoning message with no text still has nothing to add here, so it
		// keeps being skipped.
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning && msg.ResponsesReasoning != nil {
			parts := thoughtTextParts(msg.ResponsesReasoning)

			// The signature is carried by the PRECEDING function call's
			// look-ahead - but only when there is one. A standalone signed
			// reasoning item with no function call before it had nothing
			// carrying its signature, so it was lost outright. Mirroring the
			// look-ahead's own positional rule here is what keeps the value on
			// the wire exactly once: emitted when nothing consumed it, omitted
			// when the look-ahead already did.
			consumedByLookAhead := i > 0 &&
				messages[i-1].Type != nil &&
				*messages[i-1].Type == schemas.ResponsesMessageTypeFunctionCall
			if !consumedByLookAhead {
				if sig := thoughtSignatureFromEncryptedContent(msg.ResponsesReasoning.EncryptedContent); sig != nil {
					parts = append(parts, &Part{ThoughtSignature: sig})
				}
			}

			if len(parts) > 0 {
				contents = append(contents, Content{
					Parts: parts,
					Role:  "model",
				})
			}
			continue
		}

		// Handle system messages separately
		if msg.Role != nil && (*msg.Role == schemas.ResponsesInputMessageRoleSystem || *msg.Role == schemas.ResponsesInputMessageRoleDeveloper) {
			if systemInstruction == nil {
				systemInstruction = &Content{}
			}

			// Convert system message content
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					systemInstruction.Parts = append(systemInstruction.Parts, &Part{
						Text: *msg.Content.ContentStr,
					})
				}
				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						part, err := convertContentBlockToGeminiPart(block, allowedImageURLSchemes...)
						if err != nil {
							return nil, nil, fmt.Errorf("failed to convert system message content block: %w", err)
						}
						if part != nil {
							systemInstruction.Parts = append(systemInstruction.Parts, part)
						}
					}
				}
			}

			continue
		}

		// Check if this is a function call output message
		isFunctionOutput := msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput && msg.ResponsesToolMessage != nil

		// If we have pending function responses and current message is NOT a function output,
		// flush the pending responses as a single Content (for parallel function calling)
		if len(pendingFunctionResponseParts) > 0 && !isFunctionOutput {
			contents = append(contents, Content{
				Parts: pendingFunctionResponseParts,
				Role:  "user", // Function responses use "user" role in Gemini
			})
			pendingFunctionResponseParts = nil
		}

		// Handle regular messages
		content := Content{}

		if msg.Role != nil {
			// Map Responses roles to Gemini roles (Gemini only supports "user" and "model")
			switch *msg.Role {
			case schemas.ResponsesInputMessageRoleAssistant:
				content.Role = "model"
			case schemas.ResponsesInputMessageRoleUser, schemas.ResponsesInputMessageRoleDeveloper:
				content.Role = "user"
			default:
				// Default to "user" for input messages (any instructions/context)
				content.Role = "user"
			}
		}

		// Handle tool calls/responses
		if msg.ResponsesToolMessage != nil && msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeFunctionCall:
				// Convert function call to Gemini FunctionCall
				if msg.ResponsesToolMessage.Name != nil {
					var argsRaw json.RawMessage
					if msg.ResponsesToolMessage.Arguments != nil {
						rawArgs := strings.TrimSpace(*msg.ResponsesToolMessage.Arguments)
						if rawArgs == "" {
							rawArgs = "{}"
						}
						var buf bytes.Buffer
						if err := json.Compact(&buf, []byte(rawArgs)); err != nil {
							return nil, nil, fmt.Errorf("failed to decode function call arguments: %w", err)
						}
						argsRaw = buf.Bytes()
					}
					if argsRaw == nil {
						argsRaw = json.RawMessage("{}")
					}

					var thoughtSig string
					part := &Part{
						FunctionCall: &FunctionCall{
							Name: *msg.ResponsesToolMessage.Name,
							Args: argsRaw,
						},
					}
					if msg.ResponsesToolMessage.CallID != nil {
						if strings.Contains(*msg.ResponsesToolMessage.CallID, thoughtSignatureSeparator) {
							parts := strings.SplitN(*msg.ResponsesToolMessage.CallID, thoughtSignatureSeparator, 2)
							if len(parts) == 2 {
								thoughtSig = parts[1] // Extract signature (after separator)
							}
						}
						// Keep the full CallID as-is (don't strip thought signature)
						part.FunctionCall.ID = *msg.ResponsesToolMessage.CallID
					}
					if thoughtSig != "" {
						var err error
						part.ThoughtSignature, err = base64.RawURLEncoding.DecodeString(thoughtSig)
						if err != nil {
							// Silently ignore decode errors - ID will be used without signature
							thoughtSig = ""
						}
					}

					// Preserve thought signature from ResponsesReasoning message (required for Gemini 3 Pro)
					// Look ahead to see if the next message is a reasoning message with encrypted content
					if i+1 < len(messages) {
						nextMsg := messages[i+1]
						if nextMsg.Type != nil && *nextMsg.Type == schemas.ResponsesMessageTypeReasoning &&
							nextMsg.ResponsesReasoning != nil && nextMsg.ResponsesReasoning.EncryptedContent != nil {
							decodedSig, err := base64.StdEncoding.DecodeString(*nextMsg.ResponsesReasoning.EncryptedContent)
							if err == nil {
								part.ThoughtSignature = decodedSig
							}
						}
					}

					if part.ThoughtSignature == nil {
						part.ThoughtSignature = []byte(skipThoughtSignatureValidator)
					}

					content.Parts = append(content.Parts, part)
				}

			case schemas.ResponsesMessageTypeFunctionCallOutput:
				// Convert function response - collect for grouping
				// According to Gemini parallel function calling docs, multiple function responses
				// must be sent in a single message with only functionResponse parts (no text/content parts)
				if msg.ResponsesToolMessage.CallID != nil {
					responseMap := make(map[string]any)
					// Multimodal blocks (images, files) returned by the function are collected here
					// and attached to FunctionResponse.Parts (Gemini 3+ only).
					var funcMediaParts []*Part

					// Extract output from ResponsesToolMessage.Output
					if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
						output := *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
						if json.Valid([]byte(output)) {
							responseMap["output"] = json.RawMessage(output)
						} else {
							responseMap["output"] = output
						}
					} else if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
						// Handle structured output blocks (e.g. from the OpenAI/Anthropic Responses API
						// format where output is an array of content blocks like
						// [{"type":"input_text","text":"..."}, {"type":"input_image","image_url":"..."}]).
						//
						// Text blocks go into responseMap["output"]. Multimodal blocks (images, files)
						// cannot live inside the structured response; per the Gemini docs they must be
						// nested as sibling FunctionResponse.Parts (inlineData/fileData). This is a
						// Gemini 3+ feature, so for older models we drop the media and keep text only
						// (sending parts to e.g. gemini-2.5 returns a hard "not supported" 400).
						//
						// Referencing the media from the structured response differs by provider:
						//   - Vertex AI: emit a "<displayName>_ref": {"$ref": "<displayName>"} entry into
						//     the response (the documented format; Vertex resolves the ref to the part).
						//   - Gemini Developer API: do NOT emit $ref — the API rejects it
						//     ("does not match to a display_name", a known upstream bug). The model
						//     still reads the media directly from parts.
						supportsMultimodalToolOutput := isGemini3Plus(model)
						var textParts []string
						for _, block := range msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
							if block.Text != nil && *block.Text != "" {
								textParts = append(textParts, *block.Text)
								continue
							}
							if !supportsMultimodalToolOutput {
								continue // older models can't accept media in a function response
							}
							mediaPart, err := convertContentBlockToGeminiPart(block, allowedImageURLSchemes...)
							if err != nil {
								return nil, nil, fmt.Errorf("failed to convert function output content block: %w", err)
							}
							if mediaPart == nil {
								continue
							}
							displayName := fmt.Sprintf("media_%d", len(funcMediaParts))
							if mediaPart.InlineData != nil {
								mediaPart.InlineData.DisplayName = displayName
							} else if mediaPart.FileData != nil {
								mediaPart.FileData.DisplayName = displayName
							}
							funcMediaParts = append(funcMediaParts, mediaPart)
							if isVertex {
								responseMap[displayName+"_ref"] = map[string]string{"$ref": displayName}
							}
						}
						if len(textParts) > 0 {
							combined := strings.Join(textParts, "\n")
							if json.Valid([]byte(combined)) {
								responseMap["output"] = json.RawMessage(combined)
							} else {
								responseMap["output"] = combined
							}
						} else if len(funcMediaParts) > 0 {
							// Media-only result: the content lives in parts. We intentionally emit
							// {"output": ""} rather than leaving response as {} — an empty object would
							// be treated by Gemini as the full (empty) function output. The reverse
							// converter's stripFunctionResponseMediaRefs reads this "" back as no text
							// block, so the media-only round-trip stays clean.
							responseMap["output"] = ""
						}
					} else if msg.Content != nil && msg.Content.ContentStr != nil {
						// Fallback to Content.ContentStr for backward compatibility
						output := *msg.Content.ContentStr
						if json.Valid([]byte(output)) {
							responseMap["output"] = json.RawMessage(output)
						} else {
							responseMap["output"] = output
						}
					}

					// Prefer the declared tool name; fallback to callIDToName lookup, then raw CallID
					funcName := ""
					if msg.ResponsesToolMessage.Name != nil && strings.TrimSpace(*msg.ResponsesToolMessage.Name) != "" {
						funcName = *msg.ResponsesToolMessage.Name
					} else if name, ok := callIDToName[*msg.ResponsesToolMessage.CallID]; ok && strings.TrimSpace(name) != "" {
						funcName = name
					} else {
						funcName = *msg.ResponsesToolMessage.CallID
					}

					responseBytes, _ := providerUtils.MarshalSorted(responseMap)
					part := &Part{
						FunctionResponse: &FunctionResponse{
							Name:     funcName,
							Response: json.RawMessage(responseBytes),
							ID:       *msg.ResponsesToolMessage.CallID,
							Parts:    funcMediaParts,
						},
					}
					pendingFunctionResponseParts = append(pendingFunctionResponseParts, part)

					// If this is the last message, flush pending responses
					if i == len(messages)-1 && len(pendingFunctionResponseParts) > 0 {
						contents = append(contents, Content{
							Parts: pendingFunctionResponseParts,
							Role:  "user",
						})
						pendingFunctionResponseParts = nil
					}

					continue // Skip normal content handling
				}
			}
		}

		// For non-function-output messages, convert message content normally
		if !isFunctionOutput {
			// Convert message content
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					content.Parts = append(content.Parts, &Part{
						Text: *msg.Content.ContentStr,
					})
				}

				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						part, err := convertContentBlockToGeminiPart(block, allowedImageURLSchemes...)
						if err != nil {
							return nil, nil, fmt.Errorf("failed to convert message content block: %w", err)
						}
						if part != nil {
							content.Parts = append(content.Parts, part)
						}
					}
				}
			}
		}

		if len(content.Parts) > 0 {
			contents = append(contents, content)
		}
	}

	return contents, systemInstruction, nil
}

// convertContentBlockToGeminiPart converts a content block to Gemini part
func convertContentBlockToGeminiPart(block schemas.ResponsesMessageContentBlock, allowedImageURLSchemes ...string) (*Part, error) {
	if len(allowedImageURLSchemes) == 0 {
		allowedImageURLSchemes = defaultGeminiImageURLSchemes
	}

	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeText,
		schemas.ResponsesOutputMessageContentTypeText:
		if block.Text != nil && *block.Text != "" {
			part := &Part{
				Text: *block.Text,
			}
			if block.Signature != nil {
				decodedSig, err := base64.StdEncoding.DecodeString(*block.Signature)
				if err == nil {
					part.ThoughtSignature = decodedSig
				}
			}
			return part, nil
		}

	case schemas.ResponsesOutputMessageContentTypeReasoning:
		if block.Text != nil && *block.Text != "" {
			return &Part{
				Text:    *block.Text,
				Thought: true,
			}, nil
		}

	case schemas.ResponsesOutputMessageContentTypeRefusal:
		// Refusals are treated as regular text in Gemini
		if block.ResponsesOutputMessageContentRefusal != nil {
			return &Part{
				Text: block.ResponsesOutputMessageContentRefusal.Refusal,
			}, nil
		}

	case schemas.ResponsesOutputMessageContentTypeCompaction:
		// Convert compaction to text block for Gemini (compaction is Anthropic-specific)
		if block.ResponsesOutputMessageContentCompaction != nil {
			if summary := strings.TrimSpace(block.ResponsesOutputMessageContentCompaction.Summary); summary != "" {
				return &Part{Text: summary}, nil
			}
		}

	case schemas.ResponsesOutputMessageContentTypeFallback:
		// Anthropic-specific server-side fallback boundary marker. Unlike compaction it
		// carries no user content (only from/to model names), so drop it rather than
		// rendering it as text.
		return nil, nil

	case schemas.ResponsesInputMessageContentBlockTypeImage:
		if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
			imageURL := *block.ResponsesInputMessageContentBlockImage.ImageURL

			// Use existing utility functions to handle URL parsing
			sanitizedURL, err := schemas.SanitizeImageURLWithAllowedSchemes(imageURL, allowedImageURLSchemes...)
			if err != nil {
				return nil, fmt.Errorf("failed to sanitize image URL: %w", err)
			}

			urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)
			mimeType := "image/jpeg" // default
			if urlInfo.MediaType != nil {
				mimeType = *urlInfo.MediaType
			}

			if urlInfo.Type == schemas.ImageContentTypeBase64 {
				data := ""
				if urlInfo.DataURLWithoutPrefix != nil {
					data = *urlInfo.DataURLWithoutPrefix
				}

				// Decode base64 data (handles both standard and URL-safe base64)
				decodedData, err := decodeBase64StringToBytes(data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode base64 image data: %w", err)
				}

				return &Part{
					InlineData: &Blob{
						MIMEType: mimeType,
						Data:     encodeBytesToBase64String(decodedData),
					},
				}, nil
			} else {
				return &Part{
					FileData: &FileData{
						MIMEType: mimeType,
						FileURI:  sanitizedURL,
					},
				}, nil
			}
		}

	case schemas.ResponsesInputMessageContentBlockTypeAudio:
		if block.Audio != nil {
			// Decode base64 audio data (handles both standard and URL-safe base64)
			decodedData, err := decodeBase64StringToBytes(block.Audio.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to decode base64 audio data: %w", err)
			}

			return &Part{
				InlineData: &Blob{
					MIMEType: func() string {
						f := strings.ToLower(strings.TrimSpace(block.Audio.Format))
						if f == "" {
							return "audio/mpeg"
						}
						if strings.HasPrefix(f, "audio/") {
							return f
						}
						return "audio/" + f
					}(),
					Data: encodeBytesToBase64String(decodedData),
				},
			}, nil
		}

	case schemas.ResponsesInputMessageContentBlockTypeFile:
		if block.ResponsesInputMessageContentBlockFile != nil {
			fileBlock := block.ResponsesInputMessageContentBlockFile

			// Handle FileURL (URI-based file)
			if fileBlock.FileURL != nil {
				// Prefer the caller's MIMEType; otherwise take whatever the URI itself states.
				// Vertex rejects a fileData with no mimeType outright - see mimeTypeFromURI.
				fileData := &FileData{FileURI: *fileBlock.FileURL}
				if fileBlock.FileType != nil {
					fileData.MIMEType = *fileBlock.FileType
				} else {
					fileData.MIMEType = mimeTypeFromURI(*fileBlock.FileURL)
				}
				return &Part{FileData: fileData}, nil
			}

			// Handle FileData (inline file data)
			if fileBlock.FileData != nil {
				mimeType := "application/pdf"
				if fileBlock.FileType != nil {
					mimeType = *fileBlock.FileType
				}

				// Convert file data to bytes using the helper function
				dataBytes, extractedMimeType := convertFileDataToBytes(*fileBlock.FileData)
				if extractedMimeType != "" {
					mimeType = extractedMimeType
				}

				if len(dataBytes) > 0 {
					part := &Part{
						InlineData: &Blob{
							MIMEType: mimeType,
							Data:     encodeBytesToBase64String(dataBytes),
						},
					}

					return part, nil
				}
			}
		}
	}

	return nil, nil
}

// buildGroundingMetadataFromWebSearch converts a Bifrost web_search_call message to Gemini GroundingMetadata
func buildGroundingMetadataFromWebSearch(webSearchCall *schemas.ResponsesMessage, annotations []schemas.ResponsesOutputMessageContentTextAnnotation, renderedContent *string) *GroundingMetadata {
	if webSearchCall == nil || webSearchCall.ResponsesToolMessage == nil || webSearchCall.ResponsesToolMessage.Action == nil {
		return nil
	}

	action := webSearchCall.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	if action == nil {
		return nil
	}

	groundingMetadata := &GroundingMetadata{}

	// Add SearchEntryPoint with rendered content if provided
	if renderedContent != nil && *renderedContent != "" {
		groundingMetadata.SearchEntryPoint = &SearchEntryPoint{
			RenderedContent: *renderedContent,
		}
	}

	// Extract web search queries
	if len(action.Queries) > 0 {
		groundingMetadata.WebSearchQueries = action.Queries
	} else if action.Query != nil {
		groundingMetadata.WebSearchQueries = []string{*action.Query}
	}
	if len(action.ImageQueries) > 0 {
		groundingMetadata.ImageSearchQueries = action.ImageQueries
	}

	// Extract grounding chunks from sources
	var groundingChunks []*GroundingChunk
	urlToIndexMap := make(map[string]int32) // Map URL to chunk index for annotation processing

	for _, source := range action.Sources {
		if source.URL == "" {
			continue
		}

		title := source.URL // Use URL as fallback
		if source.Title != nil && *source.Title != "" {
			title = *source.Title
		}

		// An asset URL marks the source as an image-search hit; URL stays the containing page.
		chunk := &GroundingChunk{}
		if source.ImageURL != nil && *source.ImageURL != "" {
			chunk.Image = &GroundingChunkImage{
				SourceURI: source.URL,
				ImageURI:  *source.ImageURL,
				Title:     title,
			}
			if source.Domain != nil {
				chunk.Image.Domain = *source.Domain
			}
		} else {
			chunk.Web = &GroundingChunkWeb{
				URI:   source.URL,
				Title: title,
			}
		}
		groundingChunks = append(groundingChunks, chunk)
		urlToIndexMap[source.URL] = int32(len(groundingChunks) - 1)
	}

	if len(groundingChunks) > 0 {
		groundingMetadata.GroundingChunks = groundingChunks
	}

	// Convert annotations to grounding supports. Annotations carry one entry per
	// (support, chunk) pair, so regroup by segment to restore multi-source supports.
	var groundingSupports []*GroundingSupport
	supportBySegment := make(map[string]*GroundingSupport)
	for _, annotation := range annotations {
		if annotation.Type != "url_citation" {
			continue
		}

		segment := &Segment{}

		// Set segment text
		if annotation.Text != nil {
			segment.Text = *annotation.Text
		}

		// Set segment indices
		if annotation.StartIndex != nil {
			segment.StartIndex = int32(*annotation.StartIndex)
		}
		if annotation.EndIndex != nil {
			segment.EndIndex = int32(*annotation.EndIndex)
		}

		// Map annotation URL to chunk indices
		var chunkIndices []int32
		if annotation.URL != nil {
			if chunkIdx, exists := urlToIndexMap[*annotation.URL]; exists {
				chunkIndices = []int32{chunkIdx}
			}
		}

		// Only add support if we have valid segment or chunk indices
		if segment.Text == "" && len(chunkIndices) == 0 {
			continue
		}

		key := fmt.Sprintf("%d|%d|%s", segment.StartIndex, segment.EndIndex, segment.Text)
		if support, exists := supportBySegment[key]; exists {
			for _, chunkIdx := range chunkIndices {
				if !slices.Contains(support.GroundingChunkIndices, chunkIdx) {
					support.GroundingChunkIndices = append(support.GroundingChunkIndices, chunkIdx)
				}
			}
			continue
		}

		support := &GroundingSupport{Segment: segment, GroundingChunkIndices: chunkIndices}
		supportBySegment[key] = support
		groundingSupports = append(groundingSupports, support)
	}

	if len(groundingSupports) > 0 {
		groundingMetadata.GroundingSupports = groundingSupports
	}

	// Return nil if no meaningful data was extracted
	if len(groundingMetadata.WebSearchQueries) == 0 && len(groundingMetadata.ImageSearchQueries) == 0 &&
		len(groundingMetadata.GroundingChunks) == 0 {
		return nil
	}

	return groundingMetadata
}

// emitWebSearchFromGroundingMetadata converts grounding metadata to web search event stream
func emitWebSearchFromGroundingMetadata(
	metadata *GroundingMetadata,
	state *GeminiResponsesStreamState,
	sequenceNumber int,
) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// A server-side toolCall is enough to emit an item even when grounding metadata is
	// absent or query-less: the model told us it searched.
	hasGroundingQueries := metadata != nil && (len(metadata.WebSearchQueries) > 0 || len(metadata.ImageSearchQueries) > 0)
	if !hasGroundingQueries && len(state.ServerSearchRounds) == 0 {
		return responses
	}

	// Convert groundingChunks to sources. Grounding reports them for the response as a
	// whole rather than per round, so they attach to the first item -- mirroring how
	// convertGeminiCandidatesToResponsesOutput merges them onto the first search call.
	var sources []schemas.ResponsesWebSearchToolCallActionSearchSource
	if metadata != nil {
		for _, chunk := range metadata.GroundingChunks {
			if source, ok := groundingChunkSource(chunk); ok {
				sources = append(sources, source)
			}
		}
	}

	// One item per server-side round, in the order the model ran them. With no rounds
	// reported, grounding metadata alone still yields the single legacy item.
	rounds := state.ServerSearchRounds
	if len(rounds) == 0 {
		rounds = []GeminiServerSearchRound{{}}
	}

	for i, round := range rounds {
		outputIndex := state.nextOutputIndex()
		// Prefer the model's own tool-call ID so the item is traceable back to Gemini's call.
		itemID := round.CallID
		if itemID == "" {
			itemID = state.generateItemID("ws", outputIndex)
		}
		state.ItemIDs[outputIndex] = itemID

		// Queries reported by the server-side call take precedence. Grounding fills in only
		// what the call did not spell out, and only for the first item -- its query list
		// covers the whole response, so replaying it per round would attribute another
		// round's queries to this one.
		action := &schemas.ResponsesWebSearchToolCallAction{
			Type:         "search",
			Queries:      round.Queries,
			ImageQueries: round.ImageQueries,
		}
		if i == 0 && metadata != nil {
			if len(action.Queries) == 0 {
				action.Queries = metadata.WebSearchQueries
			}
			if len(action.ImageQueries) == 0 {
				action.ImageQueries = metadata.ImageSearchQueries
			}
			action.Sources = sources
		}
		if len(action.Queries) > 0 {
			action.Query = &action.Queries[0]
		}

		// 1. output_item.added (web_search_call, in_progress)
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
				Status: schemas.Ptr("in_progress"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Action: &schemas.ResponsesToolMessageActionStruct{
						ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
							Type: "search",
						},
					},
				},
			},
		})

		// 2. web_search_call.in_progress
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
		})

		// 3. web_search_call.searching
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
		})

		// 4. web_search_call.completed
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
		})

		// 5. output_item.done (with full action including sources)
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Action: &schemas.ResponsesToolMessageActionStruct{
						ResponsesWebSearchToolCallAction: action,
					},
				},
			},
		})
	}

	state.HasEmittedWebSearch = true

	// Emit rendered content if present
	if metadata != nil && metadata.SearchEntryPoint != nil && metadata.SearchEntryPoint.RenderedContent != "" {
		renderedIndex := state.nextOutputIndex()
		renderedItemID := state.generateItemID("rc", renderedIndex)
		state.ItemIDs[renderedIndex] = renderedItemID

		// output_item.added with rendered_content
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &renderedIndex,
			Item: &schemas.ResponsesMessage{
				ID:     &renderedItemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeRenderedContent,
							ResponsesOutputMessageContentRenderedContent: &schemas.ResponsesOutputMessageContentRenderedContent{
								RenderedContent: metadata.SearchEntryPoint.RenderedContent,
							},
						},
					},
				},
			},
		})

		// output_item.done for rendered content. It must repeat the item: output_item.done
		// carries the completed item, and consumers that rebuild Gemini's searchEntryPoint
		// read the rendered content off this event.
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &renderedIndex,
			ItemID:         &renderedItemID,
			Item: &schemas.ResponsesMessage{
				ID:     &renderedItemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeRenderedContent,
							ResponsesOutputMessageContentRenderedContent: &schemas.ResponsesOutputMessageContentRenderedContent{
								RenderedContent: metadata.SearchEntryPoint.RenderedContent,
							},
						},
					},
				},
			},
		})
	}

	return responses
}

// emitAnnotationsFromGroundingSupports converts grounding supports to annotation events
func emitAnnotationsFromGroundingSupports(
	metadata *GroundingMetadata,
	state *GeminiResponsesStreamState,
	sequenceNumber int,
) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	if metadata == nil || len(metadata.GroundingSupports) == 0 || state.TextOutputIndex < 0 {
		return responses
	}

	itemID := state.ItemIDs[state.TextOutputIndex]

	emmitedIndex := 0
	// Convert each grounding support to an annotation event
	for _, support := range metadata.GroundingSupports {
		if support.Segment == nil {
			continue
		}

		// One annotation per (support, chunk) pair so multi-source segments keep every source.
		for _, chunkIdx := range support.GroundingChunkIndices {
			if chunkIdx < 0 || int(chunkIdx) >= len(metadata.GroundingChunks) {
				continue
			}
			source, ok := groundingChunkSource(metadata.GroundingChunks[chunkIdx])
			if !ok {
				continue
			}

			annotation := schemas.ResponsesOutputMessageContentTextAnnotation{
				Type: "url_citation",
				URL:  &source.URL,
			}
			if support.Segment.Text != "" {
				annotation.Text = &support.Segment.Text
			}
			annotation.StartIndex = schemas.Ptr(int(support.Segment.StartIndex))
			annotation.EndIndex = schemas.Ptr(int(support.Segment.EndIndex))
			annotation.Title = source.Title

			// Emit annotation.added event
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:            schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded,
				SequenceNumber:  sequenceNumber + len(responses),
				OutputIndex:     &state.TextOutputIndex,
				ItemID:          &itemID,
				ContentIndex:    schemas.Ptr(0),
				Annotation:      &annotation,
				AnnotationIndex: schemas.Ptr(emmitedIndex),
			})
			emmitedIndex++
		}
	}

	return responses
}

// generateSyntheticFunctionCallArgumentDeltas creates synthetic FunctionCallArgumentsDelta events
// from complete JSON arguments to simulate streaming behavior for providers that don't natively stream
func generateSyntheticFunctionCallArgumentDeltas(argumentsJSON string, outputIndex *int, itemID *string, baseSequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var events []*schemas.BifrostResponsesStreamResponse

	// Chunk size for synthetic streaming (matching realistic streaming patterns)
	chunkSize := 8 // Small chunks to simulate realistic streaming

	// Break the JSON into chunks
	runes := []rune(argumentsJSON)
	for i := 0; i < len(runes); i += chunkSize {
		end := min(i+chunkSize, len(runes))

		chunk := string(runes[i:end])
		deltaEvent := &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			SequenceNumber: baseSequenceNumber + len(events),
			OutputIndex:    outputIndex,
			ItemID:         itemID,
			Delta:          &chunk,
		}
		events = append(events, deltaEvent)
	}

	return events
}
