package bedrock

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// bedrockInputTokenCountHeader is the HTTP response header Bedrock uses to report input
// token counts for models — notably Cohere embed and rerank — that omit token usage from
// the response body.
const bedrockInputTokenCountHeader = "X-Amzn-Bedrock-Input-Token-Count"

// inputTokensFromHeaders extracts the X-Amzn-Bedrock-Input-Token-Count value from a provider
// response-headers map (case-insensitive, since header casing depends on the transport).
// It returns (count, true) only when the header is present and parses as a non-negative int.
func inputTokensFromHeaders(headers map[string]string) (int, bool) {
	for k, v := range headers {
		if strings.EqualFold(k, bedrockInputTokenCountHeader) {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}

// ToBedrockTitanEmbeddingRequest converts a Bifrost embedding request to Bedrock Titan format
func ToBedrockTitanEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*BedrockTitanEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	if len(bifrostReq.Input) == 0 {
		return nil, fmt.Errorf("no input provided for Titan embedding")
	}

	if len(bifrostReq.Input) != 1 {
		return nil, fmt.Errorf("amazon Titan embedding models support exactly one content item per request; got %d", len(bifrostReq.Input))
	}

	var sb strings.Builder
	for _, part := range bifrostReq.Input[0] {
		if part.Type != schemas.EmbeddingContentPartTypeText || part.Text == nil {
			return nil, fmt.Errorf("amazon Titan embedding models only support text input")
		}
		if sb.Len() > 0 {
			sb.WriteString(" \n")
		}
		sb.WriteString(*part.Text)
	}

	titanReq := &BedrockTitanEmbeddingRequest{
		InputText: sb.String(),
	}

	if bifrostReq.Params != nil {
		titanReq.Dimensions = bifrostReq.Params.Dimensions
		if normalize, ok := bifrostReq.Params.ExtraParams["normalize"]; ok {
			if b, ok := normalize.(bool); ok {
				titanReq.Normalize = &b
			}
		}
		embeddingTypesExtracted := false
		if rawEmbeddingTypes, exists := bifrostReq.Params.ExtraParams["embeddingTypes"]; exists {
			if embeddingTypes, ok := schemas.SafeExtractStringSlice(rawEmbeddingTypes); ok {
				titanReq.EmbeddingTypes = embeddingTypes
				embeddingTypesExtracted = true
			}
		}
		// Forward remaining extra params. Keep an invalid embeddingTypes value in
		// ExtraParams so passthrough mode preserves the caller's request and lets
		// Bedrock return its native validation error instead of silently dropping it.
		if len(bifrostReq.Params.ExtraParams) > 0 {
			extra := make(map[string]interface{})
			for k, v := range bifrostReq.Params.ExtraParams {
				if k != "normalize" && !(k == "embeddingTypes" && embeddingTypesExtracted) {
					extra[k] = v
				}
			}
			if len(extra) > 0 {
				titanReq.ExtraParams = extra
			}
		}
	}

	return titanReq, nil
}

// ToBifrostEmbeddingResponse converts a Bedrock Titan embedding response to Bifrost format
func (response *BedrockTitanEmbeddingResponse) ToBifrostEmbeddingResponse() *schemas.BifrostEmbeddingResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens: response.InputTextTokenCount,
			TotalTokens:  response.InputTextTokenCount,
		},
	}

	// Titan V2 always returns embeddingsByType, carrying float alone for an ordinary
	// request. Only a genuinely multi-representation response is labelled, so the
	// common case keeps the exact shape it had before embeddingTypes was supported.
	if response.EmbeddingsByType != nil && response.EmbeddingsByType.Binary != nil {
		if response.EmbeddingsByType.Float != nil {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Index:          0,
				Object:         "embedding",
				Embedding:      schemas.EmbeddingStruct{EmbeddingArray: response.EmbeddingsByType.Float},
				EncodingFormat: schemas.EmbeddingEncodingFloat,
			})
		}
		if response.EmbeddingsByType.Binary != nil {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Index:          0,
				Object:         "embedding",
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt8Array: response.EmbeddingsByType.Binary},
				EncodingFormat: schemas.EmbeddingEncodingBinary,
			})
		}
	}

	// The ordinary single-vector response, left unlabelled so it serializes exactly
	// as before. Titan G1 only sets the top-level field; V2 repeats it under
	// embeddingsByType.float, which is the fallback when the top level is absent.
	if len(bifrostResponse.Data) == 0 {
		values := response.Embedding
		if values == nil && response.EmbeddingsByType != nil {
			values = response.EmbeddingsByType.Float
		}
		bifrostResponse.Data = []schemas.EmbeddingData{{
			Index:     0,
			Object:    "embedding",
			Embedding: schemas.EmbeddingStruct{EmbeddingArray: values},
		}}
	}

	return bifrostResponse
}

// ToBedrockCohereEmbeddingRequest converts a Bifrost embedding request to Bedrock Cohere format.
// Bedrock's invoke body omits the model field the direct Cohere API carries.
func ToBedrockCohereEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*BedrockCohereEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	if len(bifrostReq.Input) == 0 {
		return nil, fmt.Errorf("no input provided for Cohere embedding")
	}

	req := &BedrockCohereEmbeddingRequest{}

	// Text-only batches use texts[]; anything carrying media uses the mixed inputs[] shape.
	texts := make([]string, 0, len(bifrostReq.Input))
	allSingleText := true
	for _, content := range bifrostReq.Input {
		if len(content) == 1 && content[0].Type == schemas.EmbeddingContentPartTypeText && content[0].Text != nil {
			texts = append(texts, *content[0].Text)
			continue
		}
		allSingleText = false
		break
	}

	if allSingleText {
		req.Texts = texts
	} else {
		inputs := make([]BedrockCohereEmbeddingInput, 0, len(bifrostReq.Input))
		for _, content := range bifrostReq.Input {
			blocks := make([]BedrockCohereEmbeddingContentBlock, 0, len(content))
			for _, part := range content {
				if err := part.Validate(); err != nil {
					return nil, err
				}
				switch part.Type {
				case schemas.EmbeddingContentPartTypeText:
					text := *part.Text
					blocks = append(blocks, BedrockCohereEmbeddingContentBlock{Type: "text", Text: &text})
				case schemas.EmbeddingContentPartTypeImage:
					url := part.Image.URL
					if url == nil {
						url = part.Image.Data
					}
					blocks = append(blocks, BedrockCohereEmbeddingContentBlock{
						Type:     "image_url",
						ImageURL: &BedrockCohereEmbeddingImageURL{URL: *url},
					})
				default:
					return nil, fmt.Errorf("bedrock cohere embeddings support only text and image parts, got %q", part.Type)
				}
			}
			inputs = append(inputs, BedrockCohereEmbeddingInput{Content: blocks})
		}
		req.Inputs = inputs
	}

	if bifrostReq.Params != nil {
		extra := make(map[string]interface{}, len(bifrostReq.Params.ExtraParams))
		for k, v := range bifrostReq.Params.ExtraParams {
			extra[k] = v
		}

		if v, ok := extra["input_type"]; ok {
			if s, ok := v.(string); ok {
				req.InputType = s
				delete(extra, "input_type")
			}
		}
		if v, ok := extra["truncate"]; ok {
			if s, ok := v.(string); ok {
				req.Truncate = &s
				delete(extra, "truncate")
			}
		}
		if v, ok := extra["embedding_types"]; ok {
			if ss, ok := schemas.SafeExtractStringSlice(v); ok {
				req.EmbeddingTypes = ss
				delete(extra, "embedding_types")
			}
		}
		if v, ok := extra["max_tokens"]; ok {
			switch n := v.(type) {
			case int:
				req.MaxTokens = &n
				delete(extra, "max_tokens")
			case float64:
				i := int(n)
				req.MaxTokens = &i
				delete(extra, "max_tokens")
			}
		}
		if bifrostReq.Params.Dimensions != nil {
			req.OutputDimension = bifrostReq.Params.Dimensions
		}
		if len(extra) > 0 {
			req.ExtraParams = extra
		}
	}

	// AWS requires input_type and defines no default, so an absent value would be
	// serialized as "" and rejected. SDKs that target Titan's single-field shape
	// (LangChain's BedrockEmbeddings) never send it; both LangChain Python clients
	// pick search_document client-side for exactly this case, so match them.
	if req.InputType == "" {
		req.InputType = BedrockCohereInputTypeSearchDocument
	}

	return req, nil
}

// DetermineEmbeddingModelType determines the embedding model type for the
// current attempt. It consults the resolved alias family first
// (model_family / model_name / model_id / alias key) and falls back to the
// substring detectors against the wire model — so an alias to an opaque
// Bedrock deployment that's tagged with the right family routes correctly.
func DetermineEmbeddingModelType(ctx *schemas.BifrostContext, model string) (string, error) {
	switch {
	case schemas.IsTitanModelFamily(ctx, model):
		return "titan", nil
	case schemas.IsCohereModelFamily(ctx, model):
		return "cohere", nil
	default:
		return "", fmt.Errorf("unsupported embedding model: %s", model)
	}
}

// ToBifrostEmbeddingResponse converts a BedrockCohereEmbeddingResponse to Bifrost format.
// Bedrock returns embeddings as a raw [][]float32 when response_type is "embeddings_floats"
// (the default, when no embedding_types are requested), and as a typed object when
// response_type is "embeddings_by_type".
func (r *BedrockCohereEmbeddingResponse) ToBifrostEmbeddingResponse() (*schemas.BifrostEmbeddingResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("nil Bedrock Cohere embedding response")
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{Object: "list"}

	switch r.ResponseType {
	case "embeddings_by_type":
		// Object form: {"float": [[...]], "int8": [[...]], "uint8": [[...]], "binary": [[...]], "ubinary": [[...]], "base64": [...]}
		// Each entry is labelled with the encoding it arrived as, since int8/binary and
		// uint8/ubinary decode into the same EmbeddingStruct field.
		var typed BedrockCohereEmbeddingsByType
		if err := json.Unmarshal(r.Embeddings, &typed); err != nil {
			return nil, fmt.Errorf("error parsing embeddings_by_type: %w", err)
		}
		for i, emb := range typed.Float {
			float64Emb := make([]float64, len(emb))
			for j, v := range emb {
				float64Emb[j] = float64(v)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingArray: float64Emb},
				EncodingFormat: schemas.EmbeddingEncodingFloat,
			})
		}
		for i, emb := range typed.Base64 {
			e := emb
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingStr: &e},
				EncodingFormat: schemas.EmbeddingEncodingBase64,
			})
		}
		for i, emb := range typed.Int8 {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt8Array: emb},
				EncodingFormat: schemas.EmbeddingEncodingInt8,
			})
		}
		for i, emb := range typed.Binary {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt8Array: emb},
				EncodingFormat: schemas.EmbeddingEncodingBinary,
			})
		}
		for i, emb := range typed.Uint8 {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt32Array: emb},
				EncodingFormat: schemas.EmbeddingEncodingUint8,
			})
		}
		for i, emb := range typed.Ubinary {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt32Array: emb},
				EncodingFormat: schemas.EmbeddingEncodingUbinary,
			})
		}

	default:
		// Default / "embeddings_floats": raw array form [[...], [...]]
		var floats [][]float32
		if err := json.Unmarshal(r.Embeddings, &floats); err != nil {
			return nil, fmt.Errorf("error parsing embeddings_floats: %w", err)
		}
		for i, emb := range floats {
			float64Emb := make([]float64, len(emb))
			for j, v := range emb {
				float64Emb[j] = float64(v)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:    "embedding",
				Index:     i,
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: float64Emb},
			})
		}
	}

	return bifrostResponse, nil
}
