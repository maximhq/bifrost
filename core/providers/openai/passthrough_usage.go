package openai

import (
	"bytes"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ExtractOpenAIPassthroughUsage extracts usage from a passthrough response payload. method is the
// HTTP method (used to bill only generation routes); path is the stripped request path; reqBody is
// the original request body (needed for speech char count and image/video parameters); body is a
// single SSE data event (streaming) or the full response body (non-streaming).
func ExtractOpenAIPassthroughUsage(method, path string, reqBody, body []byte) *schemas.BifrostPassthroughUsage {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}

	switch {
	case strings.HasSuffix(path, "/chat/completions"),
		strings.HasSuffix(path, "/completions"):
		return extractOAIChatUsage(body)

	case strings.HasSuffix(path, "/responses"):
		return extractOAIResponsesUsage(body)

	case strings.HasSuffix(path, "/embeddings"):
		return extractOAIEmbeddingUsage(body)

	case strings.HasSuffix(path, "/audio/speech"):
		return extractOAISpeechUsage(reqBody)

	case strings.HasSuffix(path, "/audio/transcriptions"),
		strings.HasSuffix(path, "/audio/translations"):
		return extractOAITranscriptionUsage(body)

	case strings.HasSuffix(path, "/images/generations"),
		strings.HasSuffix(path, "/images/edits"),
		strings.HasSuffix(path, "/images/variations"):
		return extractOAIImageUsage(reqBody, body)

	case strings.Contains(path, "/video"):
		if strings.EqualFold(method, "POST") {
			return extractOAIVideoUsage(reqBody)
		}
		return nil

	case strings.HasSuffix(path, "/containers"):
		// Collection path serves both create (POST, billable) and list (GET, free);
		// extractOAIContainerUsage disambiguates by response shape. Retrieve/delete use
		// /containers/{id} and never match this suffix.
		return extractOAIContainerUsage(body)
	}

	return nil
}

func HasOpenAIPassthroughUsage(event []byte) bool {
	return providerUtils.GetJSONField(event, "usage").Exists() ||
		providerUtils.GetJSONField(event, "response.usage").Exists()
}

// ---- video generation ----
const openAIVideoDefaultSeconds = 4

func extractOAIVideoUsage(reqBody []byte) *schemas.BifrostPassthroughUsage {
	secs := openAIVideoDefaultSeconds
	if len(reqBody) > 0 {
		// JSON body: OpenAI documents `seconds` as a top-level request field. gjson .Float()
		// handles both the numeric and string forms the API accepts.
		if v := providerUtils.GetJSONField(reqBody, "seconds"); v.Exists() && v.Float() > 0 {
			secs = int(v.Float())
		} else if form := parseMultipartFormValues(reqBody); form != nil {
			// Multipart body (binary asset upload): `seconds` rides as a form field.
			if v := firstFormValue(form, "seconds"); v != "" {
				if f, parseErr := strconv.ParseFloat(v, 64); parseErr == nil && f > 0 {
					secs = int(f)
				}
			}
		}
	}
	return &schemas.BifrostPassthroughUsage{VideoSeconds: &secs}
}

// parseMultipartFormValues sniffs the multipart boundary from the first line of body and returns
// the parsed form values, or nil when body is not multipart/form-data (e.g. JSON). OpenAI sends
// /v1/images/{edits,variations} and binary-asset video requests as multipart; their scalar
// params (seconds, size, quality, n) ride along as form fields.
func parseMultipartFormValues(body []byte) map[string][]string {
	if len(body) == 0 {
		return nil
	}
	firstLine, _, _ := bytes.Cut(body, []byte("\n"))
	boundary := strings.TrimRight(strings.TrimPrefix(string(firstLine), "--"), "\r")
	if boundary == "" {
		return nil
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	form, err := mr.ReadForm(32 << 20)
	if err != nil {
		return nil
	}
	// ReadForm spills parts over maxMemory to temp files; clean them up. form.Value is held in
	// memory and stays valid after RemoveAll (which only purges spilled file parts).
	defer form.RemoveAll()
	return form.Value
}

func firstFormValue(form map[string][]string, key string) string {
	if v := form[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// ---- chat / text completions ----
// BifrostLLMUsage is OpenAI-compatible so we can unmarshal directly.

type oaiChatUsageWrapper struct {
	Usage       *schemas.BifrostLLMUsage `json:"usage"`
	ServiceTier *string                  `json:"service_tier"`
}

func extractOAIChatUsage(body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}
	var w oaiChatUsageWrapper
	if err := sonic.Unmarshal(body, &w); err != nil || w.Usage == nil || w.Usage.TotalTokens == 0 {
		return nil
	}
	u := &schemas.BifrostPassthroughUsage{LLMUsage: w.Usage}
	if w.ServiceTier != nil {
		t := schemas.BifrostServiceTier(*w.ServiceTier)
		u.ServiceTier = &t
	}
	return u
}

// ---- responses API ----
// A single wrapper handles both response formats in one unmarshal pass:
//   - streaming: "response.completed" event nests usage under "response"
//   - non-streaming: usage sits at the top level
type oaiResponsesWrapper struct {
	Response *struct {
		Usage       *schemas.ResponsesResponseUsage `json:"usage"`
		ServiceTier *string                         `json:"service_tier"`
	} `json:"response"`
	Usage       *schemas.ResponsesResponseUsage `json:"usage"`
	ServiceTier *string                         `json:"service_tier"`
}

func extractOAIResponsesUsage(body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}

	var w oaiResponsesWrapper
	if err := sonic.Unmarshal(body, &w); err != nil {
		return nil
	}

	// Streaming takes priority: nested under "response" with a non-zero total.
	ru, tier := w.Usage, w.ServiceTier
	if w.Response != nil && w.Response.Usage != nil && w.Response.Usage.TotalTokens > 0 {
		ru, tier = w.Response.Usage, w.Response.ServiceTier
	}
	if ru == nil || ru.TotalTokens == 0 {
		return nil
	}
	return buildOAIResponsesUsage(ru, tier)
}

func buildOAIResponsesUsage(ru *schemas.ResponsesResponseUsage, serviceTier *string) *schemas.BifrostPassthroughUsage {
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     ru.InputTokens,
		CompletionTokens: ru.OutputTokens,
		TotalTokens:      ru.TotalTokens,
	}
	if ru.InputTokensDetails != nil {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  ru.InputTokensDetails.CachedReadTokens,
			CachedWriteTokens: ru.InputTokensDetails.CachedWriteTokens,
		}
	}
	if ru.OutputTokensDetails != nil {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: ru.OutputTokensDetails.ReasoningTokens,
		}
		if ru.OutputTokensDetails.NumSearchQueries != nil {
			usage.CompletionTokensDetails.NumSearchQueries = ru.OutputTokensDetails.NumSearchQueries
		}
	}
	u := &schemas.BifrostPassthroughUsage{LLMUsage: usage}
	if serviceTier != nil {
		t := schemas.BifrostServiceTier(*serviceTier)
		u.ServiceTier = &t
	}
	return u
}

// ---- embeddings ----
// Embeddings are not typically streamed; body is plain JSON.

func extractOAIEmbeddingUsage(body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}
	var w oaiChatUsageWrapper
	if err := sonic.Unmarshal(body, &w); err != nil || w.Usage == nil || w.Usage.TotalTokens == 0 {
		return nil
	}
	return &schemas.BifrostPassthroughUsage{LLMUsage: w.Usage}
}

// ---- speech (TTS) ----
// Response is binary audio; pricing is based on input character count from the request.

func extractOAISpeechUsage(reqBody []byte) *schemas.BifrostPassthroughUsage {
	if len(reqBody) == 0 {
		return nil
	}
	var req OpenAISpeechRequest
	if err := sonic.Unmarshal(reqBody, &req); err != nil || req.Input == "" {
		return nil
	}
	return &schemas.BifrostPassthroughUsage{
		AudioInputChars: len([]rune(req.Input)),
	}
}

// ---- transcription / translation ----

type oaiTranscriptionResponseWrapper struct {
	Usage    *schemas.TranscriptionUsage `json:"usage"`
	Duration float64                     `json:"duration"` // seconds fallback for older models
}

func extractOAITranscriptionUsage(body []byte) *schemas.BifrostPassthroughUsage {
	var r oaiTranscriptionResponseWrapper
	if err := sonic.Unmarshal(body, &r); err != nil {
		return nil
	}
	u := &schemas.BifrostPassthroughUsage{}
	if r.Usage != nil && r.Usage.TotalTokens != nil && *r.Usage.TotalTokens > 0 {
		promptTokens := 0
		if r.Usage.InputTokens != nil {
			promptTokens = *r.Usage.InputTokens
		}
		u.LLMUsage = &schemas.BifrostLLMUsage{
			PromptTokens: promptTokens,
			TotalTokens:  *r.Usage.TotalTokens,
		}
		if r.Usage.InputTokenDetails != nil {
			u.AudioTokenDetails = &schemas.TranscriptionUsageInputTokenDetails{
				AudioTokens: r.Usage.InputTokenDetails.AudioTokens,
				TextTokens:  r.Usage.InputTokenDetails.TextTokens,
			}
		}
		u.AudioSeconds = r.Usage.Seconds
	} else if r.Duration > 0 {
		secs := int(r.Duration)
		u.AudioSeconds = &secs
	}
	if u.LLMUsage == nil && u.AudioSeconds == nil {
		return nil
	}
	return u
}

// ---- image generation / edit / variation ----
// Size, Quality, N come from the request body; usage/data count from the response.

func extractOAIImageUsage(reqBody, body []byte) *schemas.BifrostPassthroughUsage {
	u := &schemas.BifrostPassthroughUsage{}

	// Request body: size, quality, n. /v1/images/{edits,variations} are sent as
	// multipart/form-data (binary image upload) with these as form fields; /v1/images/generations
	// (and JSON-mode edits) carry them as a JSON OpenAIImageGenerationRequest.
	if len(reqBody) > 0 {
		var size, quality string
		var n int
		if form := parseMultipartFormValues(reqBody); form != nil {
			size = firstFormValue(form, "size")
			quality = firstFormValue(form, "quality")
			if v := firstFormValue(form, "n"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil {
					n = parsed
				}
			}
		} else {
			var req OpenAIImageGenerationRequest
			if err := sonic.Unmarshal(reqBody, &req); err == nil {
				if req.Size != nil {
					size = *req.Size
				}
				if req.Quality != nil {
					quality = *req.Quality
				}
				if req.N != nil {
					n = *req.N
				}
			}
		}
		if size != "" {
			u.ImageSize = size
		}
		if quality != "" {
			u.ImageQuality = quality
		}
		if n > 0 {
			if u.ImageUsage == nil {
				u.ImageUsage = &schemas.ImageUsage{}
			}
			if u.ImageUsage.OutputTokensDetails == nil {
				u.ImageUsage.OutputTokensDetails = &schemas.ImageTokenDetails{}
			}
			u.ImageUsage.OutputTokensDetails.NImages = n
		}
	}

	// Response body: use OpenAIImageStreamResponse (streaming SSE event) or fall back to
	// plain JSON for non-streaming passthrough routes.
	if len(body) > 0 {
		var resp OpenAIImageStreamResponse
		if err := sonic.Unmarshal(body, &resp); err == nil {
			if resp.Usage != nil {
				u.ImageUsage = resp.Usage
			}
			if resp.Size != "" && u.ImageSize == "" {
				u.ImageSize = resp.Size
			}
			if resp.Quality != "" && u.ImageQuality == "" {
				u.ImageQuality = resp.Quality
			}
		}
		// Mirror the native path (populateOutputImageCount): count delivered images from
		// the `data` array when the request didn't specify n and no token usage was
		// returned (e.g. DALL·E, which has no usage block).
		if dataLen := int(providerUtils.GetJSONField(body, "data.#").Int()); dataLen > 0 {
			if u.ImageUsage == nil {
				u.ImageUsage = &schemas.ImageUsage{}
			}
			if u.ImageUsage.OutputTokensDetails == nil {
				u.ImageUsage.OutputTokensDetails = &schemas.ImageTokenDetails{}
			}
			if u.ImageUsage.OutputTokensDetails.NImages == 0 {
				u.ImageUsage.OutputTokensDetails.NImages = dataLen
			}
		}
	}

	if u.ImageUsage == nil {
		u.ImageUsage = &schemas.ImageUsage{}
	}
	// Populate LLMUsage from image token counts so logs show token totals.
	if u.ImageUsage.TotalTokens > 0 {
		u.LLMUsage = &schemas.BifrostLLMUsage{
			PromptTokens:     u.ImageUsage.InputTokens,
			CompletionTokens: u.ImageUsage.OutputTokens,
			TotalTokens:      u.ImageUsage.TotalTokens,
		}
	}
	return u
}

// ---- containers (code interpreter sessions) ----
// Only the create call (POST /v1/containers) is billable — a flat per-session fee priced
// under the synthetic "container-{memory_limit}" model key (falling back to "container").
// The collection path also serves list (GET /v1/containers), so disambiguate by response
// shape: a create returns a single {"object":"container", "id":...} object, while a list
// returns {"object":"list", "data":[...]}. Retrieve/delete hit /containers/{id} and never
// reach this extractor. Containers are never streamed, so body is plain JSON.

func extractOAIContainerUsage(body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}
	var resp struct {
		Object      string `json:"object"`
		ID          string `json:"id"`
		MemoryLimit string `json:"memory_limit"`
	}
	if err := sonic.Unmarshal(body, &resp); err != nil {
		return nil
	}
	// Bill only a created container (single object), not list/other shapes.
	if resp.Object != "container" || resp.ID == "" {
		return nil
	}
	identifier := "container"
	if resp.MemoryLimit != "" {
		identifier = "container-" + resp.MemoryLimit
	}
	return &schemas.BifrostPassthroughUsage{ContainerIdentifier: identifier}
}
