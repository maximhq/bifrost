package gemini

import (
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ExtractGeminiPassthroughUsage extracts usage from a completed Gemini/Vertex
// passthrough response. Handles both SSE streaming (last event) and plain JSON
// (non-streaming) for all billable endpoint types.
//
// :generateContent handles all modalities — text, speech, transcription, and non-Imagen
// image generation. Output modality detection routes to the correct BifrostPassthroughUsage
// shape so the pricing engine uses the appropriate cost function.
//
// :predict is Imagen priced per-image. :predictLongRunning is Veo priced per-second.
// /interactions paths use the Interactions API usage shape.
func ExtractGeminiPassthroughUsage(path string, reqBody, body []byte) *schemas.BifrostPassthroughUsage {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}

	// Interactions API uses /interactions paths — no colon suffix.
	if strings.Contains(path, "/interactions") {
		return extractGeminiInteractionsUsage(body)
	}

	colonIdx := strings.LastIndexByte(path, ':')
	action := ""
	if colonIdx >= 0 {
		action = path[colonIdx+1:]
	}

	switch action {
	case "generateContent", "streamGenerateContent":
		return extractGeminiGenerateContentUsage(body)

	case "embedContent", "batchEmbedContents":
		return extractGeminiEmbeddingUsage(body)

	case "predict":
		return extractGeminiPredictUsage(reqBody, body)

	case "predictLongRunning":
		// Veo video generation — priced per second of video.
		return extractGeminiVeoUsage(reqBody)
	}

	// Unknown action (e.g. countTokens, generateVideos) — try usageMetadata as best-effort.
	return extractGeminiGenerateContentUsage(body)
}

func HasGeminiPassthroughUsage(event []byte) bool {
	return providerUtils.GetJSONField(event, "usageMetadata").Exists() ||
		providerUtils.GetJSONField(event, "usage").Exists() ||
		providerUtils.GetJSONField(event, "interaction.usage").Exists()
}

// ---- :generateContent / :streamGenerateContent ----

type geminiPassthroughResp struct {
	UsageMetadata *GenerateContentResponseUsageMetadata `json:"usageMetadata"`
}

// extractGeminiGenerateContentUsage routes to the correct BifrostPassthroughUsage shape
// based on output modality from the response's candidatesTokensDetails:
//
//   - IMAGE tokens in output → ImageUsage + LLMUsage → ImageGenerationRequest → computeImageCost
//   - AUDIO tokens in output → LLMUsage with CompletionTokensDetails.AudioTokens → ResponsesRequest → computeTextCost audio differential
//   - TEXT / default → LLMUsage → ResponsesRequest → computeTextCost
//
// Gemini TTS bills by input tokens (not chars), so AUDIO output stays in the ResponsesRequest
// path where computeTextCost applies the OutputCostPerAudioToken rate differential.
func extractGeminiGenerateContentUsage(body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}

	var resp geminiPassthroughResp
	if err := sonic.Unmarshal(body, &resp); err != nil || resp.UsageMetadata == nil {
		return nil
	}

	// ConvertGeminiUsageMetadataToResponsesUsage handles thinking tokens, cached content,
	// and per-modality breakdowns (text, audio, image) for all :generateContent request types.
	ru := ConvertGeminiUsageMetadataToResponsesUsage(resp.UsageMetadata)
	if ru == nil || ru.TotalTokens == 0 {
		return nil
	}

	// IMAGE output → ImageUsage routes to computeImageCost via ImageGenerationRequest.
	// LLMUsage is also set so the logging plugin can display tokens in/out.
	if ru.OutputTokensDetails != nil && ru.OutputTokensDetails.ImageTokens != nil && *ru.OutputTokensDetails.ImageTokens > 0 {
		imageUsage := &schemas.ImageUsage{
			InputTokens:  ru.InputTokens,
			OutputTokens: ru.OutputTokens,
			TotalTokens:  ru.TotalTokens,
			OutputTokensDetails: &schemas.ImageTokenDetails{
				ImageTokens: *ru.OutputTokensDetails.ImageTokens,
			},
		}
		if ru.InputTokensDetails != nil && (ru.InputTokensDetails.TextTokens > 0 || ru.InputTokensDetails.ImageTokens > 0) {
			imageUsage.InputTokensDetails = &schemas.ImageTokenDetails{
				TextTokens:  ru.InputTokensDetails.TextTokens,
				ImageTokens: ru.InputTokensDetails.ImageTokens,
			}
		}
		return &schemas.BifrostPassthroughUsage{
			ImageUsage: imageUsage,
			LLMUsage: &schemas.BifrostLLMUsage{
				PromptTokens:     ru.InputTokens,
				CompletionTokens: ru.OutputTokens,
				TotalTokens:      ru.TotalTokens,
			},
		}
	}

	// TEXT / AUDIO / default → LLMUsage with full modality details for computeTextCost.
	// For AUDIO output, CompletionTokensDetails.AudioTokens is set so computeTextCost applies
	// the OutputCostPerAudioToken rate differential: cost = tokens * (audioRate - textRate).
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     ru.InputTokens,
		CompletionTokens: ru.OutputTokens,
		TotalTokens:      ru.TotalTokens,
	}
	if ru.InputTokensDetails != nil {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			CachedReadTokens: ru.InputTokensDetails.CachedReadTokens,
			TextTokens:       ru.InputTokensDetails.TextTokens,
			AudioTokens:      ru.InputTokensDetails.AudioTokens,
			ImageTokens:      ru.InputTokensDetails.ImageTokens,
		}
	}
	if ru.OutputTokensDetails != nil {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: ru.OutputTokensDetails.ReasoningTokens,
			AudioTokens:     ru.OutputTokensDetails.AudioTokens,
		}
	}

	return &schemas.BifrostPassthroughUsage{LLMUsage: usage}
}

// ---- :embedContent / :batchEmbedContents ----

func extractGeminiEmbeddingUsage(body []byte) *schemas.BifrostPassthroughUsage {
	// Embeddings are never streamed, so body is plain JSON.
	if len(body) == 0 {
		return nil
	}

	var resp geminiPassthroughResp
	if err := sonic.Unmarshal(body, &resp); err != nil || resp.UsageMetadata == nil {
		return nil
	}

	m := resp.UsageMetadata
	total := int(m.TotalTokenCount)
	prompt := int(m.PromptTokenCount)
	if total == 0 && prompt == 0 {
		return nil
	}
	if total == 0 {
		total = prompt
	}
	return &schemas.BifrostPassthroughUsage{
		LLMUsage: &schemas.BifrostLLMUsage{
			PromptTokens: prompt,
			TotalTokens:  total,
		},
	}
}

// ---- /interactions (Interactions API) ----
// Non-streaming: usage sits at top level.
// Streaming InteractionCompletedEvent: usage is nested under "interaction".
// Fields: total_input_tokens, total_output_tokens, total_thought_tokens (reasoning),
// total_cached_tokens. service_tier "standard" is the default and is not forwarded.

type geminiInteractionsUsage struct {
	TotalTokens   int `json:"total_tokens"`
	InputTokens   int `json:"total_input_tokens"`
	OutputTokens  int `json:"total_output_tokens"`
	ThoughtTokens int `json:"total_thought_tokens"`
	CachedTokens  int `json:"total_cached_tokens"`
}

type geminiInteractionsWrapper struct {
	Usage       *geminiInteractionsUsage `json:"usage"`
	ServiceTier *string                  `json:"service_tier"`
	// Streaming InteractionCompletedEvent nests the completed interaction object.
	Interaction *struct {
		Usage       *geminiInteractionsUsage `json:"usage"`
		ServiceTier *string                  `json:"service_tier"`
	} `json:"interaction"`
}

func extractGeminiInteractionsUsage(body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}

	var w geminiInteractionsWrapper
	if err := sonic.Unmarshal(body, &w); err != nil {
		return nil
	}

	// Streaming takes priority: nested under "interaction" with a non-zero total.
	u, tier := w.Usage, w.ServiceTier
	if w.Interaction != nil && w.Interaction.Usage != nil && w.Interaction.Usage.TotalTokens > 0 {
		u, tier = w.Interaction.Usage, w.Interaction.ServiceTier
	}
	if u == nil || u.TotalTokens == 0 {
		return nil
	}

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens + u.ThoughtTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.CachedTokens > 0 {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			CachedReadTokens: u.CachedTokens,
		}
	}
	if u.ThoughtTokens > 0 {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: u.ThoughtTokens,
		}
	}

	result := &schemas.BifrostPassthroughUsage{LLMUsage: usage}
	// "standard" is the default tier — only forward non-standard values.
	if tier != nil && *tier != "" && *tier != "standard" {
		t := schemas.BifrostServiceTier(*tier)
		result.ServiceTier = &t
	}
	return result
}

// ---- :predictLongRunning (Veo video generation) ----

func extractGeminiVeoUsage(reqBody []byte) *schemas.BifrostPassthroughUsage {
	// Default matches the native Gemini/Vertex path (schemas.DefaultVideoDuration).
	secs := 8
	if d, err := strconv.Atoi(schemas.DefaultVideoDuration); err == nil {
		secs = d
	}
	if len(reqBody) > 0 {
		if d := providerUtils.GetJSONField(reqBody, "parameters.durationSeconds"); d.Exists() && d.Int() > 0 {
			secs = int(d.Int())
		}
	}
	return &schemas.BifrostPassthroughUsage{VideoSeconds: &secs}
}

// ---- :predict dispatch (Vertex/Gemini prediction endpoint) ----
// The :predict action is shared by embeddings and Imagen image generation, distinguished by
// the per-prediction structure:
//
//   - embedding (text or multimodal): predictions[] carry an `embeddings` block (text, with
//     statistics.token_count) or modality vectors (textEmbedding/imageEmbedding/videoEmbeddings,
//     multimodal — no token count) → token usage
//   - Imagen image gen: predictions[].bytesBase64Encoded → per-image count
//
// An embedding response carries only LLMUsage, so it resolves to EmbeddingRequest via
// detectPassthroughRequestType's :predict mapping. Imagen sets ImageUsage and is resolved by
// its usage shape before that fallback, so the fallback only ever classifies embeddings.
type geminiPredictResponse struct {
	Predictions []struct {
		Embeddings *struct {
			Statistics *struct {
				TokenCount int `json:"token_count"`
			} `json:"statistics"`
		} `json:"embeddings"`
		// Multimodal embedding modality vectors — used only to recognize the response as an
		// embedding (multimodal responses carry no token count to bill).
		TextEmbedding   []float64 `json:"textEmbedding"`
		ImageEmbedding  []float64 `json:"imageEmbedding"`
		VideoEmbeddings []any     `json:"videoEmbeddings"`
	} `json:"predictions"`
}

func extractGeminiPredictUsage(reqBody, body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) > 0 {
		var resp geminiPredictResponse
		if err := sonic.Unmarshal(body, &resp); err == nil {
			if u := resp.embeddingUsage(); u != nil {
				return u
			}
		}
	}
	// Default: Imagen image generation (priced per image).
	return extractGeminiImagenUsage(reqBody, body)
}

// embeddingUsage returns token usage when the :predict response is an embedding response
// (text or multimodal), else nil. It returns non-nil for any embedding response — even when no
// token count is present (multimodal) — so an embedding is billed as such rather than misrouted
// to the Imagen per-image path.
func (r *geminiPredictResponse) embeddingUsage() *schemas.BifrostPassthroughUsage {
	total, isEmbedding := 0, false
	for i := range r.Predictions {
		p := r.Predictions[i]
		if p.Embeddings != nil {
			isEmbedding = true
			if p.Embeddings.Statistics != nil {
				total += p.Embeddings.Statistics.TokenCount
			}
		}
		if len(p.TextEmbedding) > 0 || len(p.ImageEmbedding) > 0 || len(p.VideoEmbeddings) > 0 {
			isEmbedding = true
		}
	}
	if !isEmbedding {
		return nil
	}
	return &schemas.BifrostPassthroughUsage{
		LLMUsage: &schemas.BifrostLLMUsage{PromptTokens: total, TotalTokens: total},
	}
}

// ---- :predict (Imagen) ----
// Imagen is priced per image. Extract count from predictions in the response,
// with the requested sampleCount from the request body as a fallback.

func extractGeminiImagenUsage(reqBody, body []byte) *schemas.BifrostPassthroughUsage {
	u := &schemas.BifrostPassthroughUsage{
		ImageUsage: &schemas.ImageUsage{},
	}

	// Request body: sampleCount is the requested number of images.
	if len(reqBody) > 0 {
		var req GeminiImagenRequest
		if err := sonic.Unmarshal(reqBody, &req); err == nil &&
			req.Parameters.SampleCount != nil && *req.Parameters.SampleCount > 0 {
			if u.ImageUsage.OutputTokensDetails == nil {
				u.ImageUsage.OutputTokensDetails = &schemas.ImageTokenDetails{}
			}
			u.ImageUsage.OutputTokensDetails.NImages = *req.Parameters.SampleCount
		}
	}

	// Response body: actual delivered predictions (may be fewer than requested).
	if len(body) > 0 {
		var resp GeminiImagenResponse
		if err := sonic.Unmarshal(body, &resp); err == nil && len(resp.Predictions) > 0 {
			if u.ImageUsage.OutputTokensDetails == nil {
				u.ImageUsage.OutputTokensDetails = &schemas.ImageTokenDetails{}
			}
			u.ImageUsage.OutputTokensDetails.NImages = len(resp.Predictions)
		}
	}

	if u.ImageUsage.OutputTokensDetails == nil || u.ImageUsage.OutputTokensDetails.NImages == 0 {
		return nil
	}
	return u
}
