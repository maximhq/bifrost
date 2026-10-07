package bedrock

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"path/filepath"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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

// encodeEmbeddingsAsBase64 replaces float vectors with base64 of little-endian float32 - the
// exact bytes Bedrock Cohere returns for embedding_types base64, and the representation
// OpenAI's encoding_format base64 produces. Entries already carrying a string are left alone.
func encodeEmbeddingsAsBase64(response *schemas.BifrostEmbeddingResponse) {
	if response == nil {
		return
	}
	for i := range response.Data {
		if response.Data[i].Embedding.EmbeddingStr != nil {
			continue
		}
		values := response.Data[i].Embedding.EmbeddingArray
		if values == nil {
			continue
		}
		buf := make([]byte, 4*len(values))
		for j, v := range values {
			binary.LittleEndian.PutUint32(buf[j*4:], math.Float32bits(float32(v)))
		}
		encoded := base64.StdEncoding.EncodeToString(buf)
		response.Data[i].Embedding = schemas.EmbeddingStruct{EmbeddingStr: &encoded}
		response.Data[i].EncodingFormat = schemas.EmbeddingEncodingBase64
	}
}

// shouldEncodeEmbeddingsAsBase64 reports whether the caller asked for base64 through the
// standard encoding_format field. A request carrying Bedrock's own embedding_types /
// embeddingTypes came in through the Bedrock integration, where the caller's wire field owns
// the response envelope, so the conversion stays out of it.
func shouldEncodeEmbeddingsAsBase64(request *schemas.BifrostEmbeddingRequest) bool {
	if request == nil || request.Params == nil || request.Params.EncodingFormat == nil {
		return false
	}
	if *request.Params.EncodingFormat != schemas.EmbeddingEncodingBase64 {
		return false
	}
	if _, ok := request.Params.ExtraParams["embedding_types"]; ok {
		return false
	}
	if _, ok := request.Params.ExtraParams["embeddingTypes"]; ok {
		return false
	}
	return true
}

// titanImagePayload returns the bare base64 Titan expects, stripping a data URI prefix.
// Titan cannot fetch remote images, so a url-only part is rejected instead of dropped.
func titanImagePayload(media *schemas.EmbeddingMediaPart) (string, error) {
	if media == nil || (media.Data == nil && media.URL == nil) {
		return "", providerUtils.InvalidRequestErrorf("image part carries neither data nor url")
	}
	if media.Data == nil {
		return "", providerUtils.InvalidRequestErrorf("amazon Titan multimodal embedding models require inline base64 image data, not a url")
	}
	data := *media.Data
	if strings.HasPrefix(data, "data:") {
		if info := schemas.ExtractURLTypeInfo(data); info.DataURLWithoutPrefix != nil {
			data = *info.DataURLWithoutPrefix
		}
	}
	if data == "" {
		return "", providerUtils.InvalidRequestErrorf("image part has empty data")
	}
	return data, nil
}

// ToBedrockTitanEmbeddingRequest converts a Bifrost embedding request to Bedrock Titan format
func ToBedrockTitanEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*BedrockTitanEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	if len(bifrostReq.Input) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("no input provided for Titan embedding")
	}

	if len(bifrostReq.Input) != 1 {
		return nil, providerUtils.InvalidRequestErrorf("amazon Titan embedding models support exactly one content item per request; got %d", len(bifrostReq.Input))
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams("amazon Titan"); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	multimodal := schemas.IsTitanMultimodalEmbeddingModel(bifrostReq.Model)

	var sb strings.Builder
	var inputImage *string
	for _, part := range bifrostReq.Input[0].Content {
		switch part.Type {
		case schemas.EmbeddingContentPartTypeText:
			if part.Text == nil {
				return nil, providerUtils.InvalidRequestErrorf("text part carries no text")
			}
			if sb.Len() > 0 {
				sb.WriteString(" \n")
			}
			sb.WriteString(*part.Text)
		case schemas.EmbeddingContentPartTypeImage:
			if inputImage != nil {
				return nil, providerUtils.InvalidRequestErrorf("amazon Titan embedding models accept at most one image per request")
			}
			encoded, err := titanImagePayload(part.Image)
			if err != nil {
				return nil, err
			}
			inputImage = &encoded
		default:
			return nil, providerUtils.InvalidRequestErrorf("amazon Titan embedding models do not support %q parts", part.Type)
		}
	}

	if sb.Len() == 0 && inputImage == nil {
		return nil, providerUtils.InvalidRequestErrorf("no input provided for Titan embedding")
	}

	titanReq := &BedrockTitanEmbeddingRequest{
		InputText:  sb.String(),
		InputImage: inputImage,
	}

	if bifrostReq.Params != nil {
		if multimodal {
			// titan-embed-image-v1 has no top-level dimensions field.
			if bifrostReq.Params.Dimensions != nil {
				titanReq.EmbeddingConfig = &BedrockTitanEmbeddingConfig{OutputEmbeddingLength: bifrostReq.Params.Dimensions}
			}
		} else {
			titanReq.Dimensions = bifrostReq.Params.Dimensions
		}
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
		// encoding_format is the standard field. embeddingTypes is Titan's own name for
		// the same thing and reaches here only through the Bedrock integration, so it wins.
		// Only binary is worth sending, float is what Titan returns anyway, and G1 and the
		// multimodal model reject the key itself.
		if len(titanReq.EmbeddingTypes) == 0 && !multimodal && bifrostReq.Params.EncodingFormat != nil &&
			*bifrostReq.Params.EncodingFormat == schemas.EmbeddingEncodingBinary {
			titanReq.EmbeddingTypes = []string{schemas.EmbeddingEncodingBinary}
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
		return nil, providerUtils.InvalidRequestErrorf("no input provided for Cohere embedding")
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams("bedrock cohere"); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	req := &BedrockCohereEmbeddingRequest{}

	// Text-only batches use texts[]; anything carrying media uses the mixed inputs[] shape.
	if schemas.EmbeddingInput(bifrostReq.Input).AllSingleText() {
		req.Texts = make([]string, len(bifrostReq.Input))
		for i, item := range bifrostReq.Input {
			req.Texts[i] = *item.Content[0].Text
		}
	} else {
		inputs := make([]BedrockCohereEmbeddingInput, 0, len(bifrostReq.Input))
		for _, item := range bifrostReq.Input {
			content := item.Content
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
					return nil, providerUtils.InvalidRequestErrorf("bedrock cohere embeddings support only text and image parts, got %q", part.Type)
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
		// encoding_format is the standard field. embedding_types is Bedrock's own name for
		// the same thing and reaches here only through the Bedrock integration, so it wins.
		if len(req.EmbeddingTypes) == 0 && bifrostReq.Params.EncodingFormat != nil &&
			*bifrostReq.Params.EncodingFormat != "" && *bifrostReq.Params.EncodingFormat != schemas.EmbeddingEncodingBase64 {
			req.EmbeddingTypes = []string{*bifrostReq.Params.EncodingFormat}
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

// novaEmbeddingFormats maps the media types and file extensions a caller is likely to
// carry onto Nova's per-modality format enum. AWS checks the declared format against
// the bytes it detects, so an underivable format is refused here rather than guessed.
var novaEmbeddingFormats = map[schemas.EmbeddingContentPartType]map[string]string{
	schemas.EmbeddingContentPartTypeImage: {
		"image/png": "png", "png": "png",
		"image/jpeg": "jpeg", "image/jpg": "jpeg", "jpeg": "jpeg", "jpg": "jpeg",
		"image/webp": "webp", "webp": "webp",
		"image/gif": "gif", "gif": "gif",
	},
	schemas.EmbeddingContentPartTypeAudio: {
		"audio/mpeg": "mp3", "audio/mp3": "mp3", "mp3": "mp3",
		"audio/wav": "wav", "audio/x-wav": "wav", "audio/wave": "wav", "wav": "wav",
		"audio/ogg": "ogg", "ogg": "ogg",
	},
	schemas.EmbeddingContentPartTypeVideo: {
		"video/mp4": "mp4", "mp4": "mp4",
		"video/quicktime": "mov", "mov": "mov",
		"video/x-matroska": "mkv", "mkv": "mkv",
		"video/webm": "webm", "webm": "webm",
		"video/x-flv": "flv", "flv": "flv",
		"video/mpeg": "mpeg", "mpeg": "mpeg", "mpg": "mpeg",
		"video/x-ms-wmv": "wmv", "wmv": "wmv",
		"video/3gpp": "three_gp", "3gp": "three_gp",
	},
}

// novaEmbeddingMediaTypes is the reverse of novaEmbeddingFormats, used by the Bedrock
// integration to hand a native format name back as a media type. Every value here is a
// key of the table above, so a format survives the round trip unchanged.
//
// mpg and 3gp are inbound spellings only. AWS's reference documents 3gp, but the API
// rejects it and takes three_gp, so a body copied from the reference normalizes to the
// spelling that works rather than failing format derivation on the way out.
var novaEmbeddingMediaTypes = map[string]string{
	"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp", "gif": "image/gif",
	"mp3": "audio/mpeg", "wav": "audio/wav", "ogg": "audio/ogg",
	"mp4": "video/mp4", "mov": "video/quicktime", "mkv": "video/x-matroska",
	"webm": "video/webm", "flv": "video/x-flv", "mpeg": "video/mpeg", "mpg": "video/mpeg",
	"wmv": "video/x-ms-wmv", "three_gp": "video/3gpp", "3gp": "video/3gpp",
}

// novaEmbeddingFormatHints names the media types accepted per modality, for the error
// raised when neither mime_type nor the URL says which one a part carries.
var novaEmbeddingFormatHints = map[schemas.EmbeddingContentPartType]string{
	schemas.EmbeddingContentPartTypeImage: "image/png, image/jpeg, image/webp or image/gif",
	schemas.EmbeddingContentPartTypeAudio: "audio/mpeg, audio/wav or audio/ogg",
	schemas.EmbeddingContentPartTypeVideo: "video/mp4, video/quicktime, video/x-matroska, video/webm, video/x-flv, video/mpeg, video/x-ms-wmv or video/3gpp",
}

// novaEmbeddingFormat resolves Nova's format name from the declared media type, falling
// back to the extension of an s3:// key when no type was given.
func novaEmbeddingFormat(partType schemas.EmbeddingContentPartType, mimeType, rawURL string) (string, error) {
	table := novaEmbeddingFormats[partType]
	if table == nil {
		return "", providerUtils.InvalidRequestErrorf("amazon Nova embedding models do not support %q parts", partType)
	}
	if mimeType != "" {
		if parsed, _, err := mime.ParseMediaType(mimeType); err == nil {
			mimeType = parsed
		}
		if format, ok := table[strings.ToLower(strings.TrimSpace(mimeType))]; ok {
			return format, nil
		}
	}
	if rawURL != "" {
		path := rawURL
		if i := strings.IndexAny(path, "?#"); i >= 0 {
			path = path[:i]
		}
		if format, ok := table[strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))]; ok {
			return format, nil
		}
	}
	return "", providerUtils.InvalidRequestErrorf("cannot determine the %s format for amazon Nova: pass mime_type as one of %s", partType, novaEmbeddingFormatHints[partType])
}

// novaEmbeddingMediaPart converts one media part into Nova's format and source pair.
// s3:// travels as s3Location, which Nova resolves itself; inline data travels as bytes.
// An http(s) URL has no home here - Nova fetches neither - so it is refused with the
// two shapes that do work.
func novaEmbeddingMediaPart(partType schemas.EmbeddingContentPartType, media *schemas.EmbeddingMediaPart) (string, BedrockNovaEmbeddingSource, error) {
	var empty BedrockNovaEmbeddingSource
	if err := media.Validate(); err != nil {
		return "", empty, providerUtils.InvalidRequestErrorf("%s", err)
	}

	mimeType := ""
	if media.MIMEType != nil {
		mimeType = *media.MIMEType
	}

	if media.Data != nil {
		data := *media.Data
		if strings.HasPrefix(data, "data:") {
			mediaType, _, payload, ok := schemas.ParseDataURL(data)
			if !ok {
				return "", empty, providerUtils.InvalidRequestErrorf("%s part carries a malformed data url", partType)
			}
			data = payload
			if mimeType == "" {
				mimeType = mediaType
			}
		}
		if data == "" {
			return "", empty, providerUtils.InvalidRequestErrorf("%s part has empty data", partType)
		}
		format, err := novaEmbeddingFormat(partType, mimeType, "")
		if err != nil {
			return "", empty, err
		}
		return format, BedrockNovaEmbeddingSource{Bytes: &data}, nil
	}

	url := *media.URL
	s3Location, ok := bedrockS3LocationFromURL(url)
	if !ok {
		if strings.HasPrefix(url, "s3://") {
			return "", empty, providerUtils.InvalidRequestErrorf("invalid s3:// %s reference %q: expected s3://bucket/key", partType, url)
		}
		return "", empty, providerUtils.InvalidRequestErrorf("amazon Nova embedding models read media from s3:// or inline base64 only, so %q cannot be used; send the %s inline or presign it into S3", url, partType)
	}
	format, err := novaEmbeddingFormat(partType, mimeType, url)
	if err != nil {
		return "", empty, err
	}
	return format, BedrockNovaEmbeddingSource{S3Location: s3Location}, nil
}

// ToBedrockNovaEmbeddingRequest converts a Bifrost embedding request to Bedrock Nova format.
// SINGLE_EMBEDDING embeds one content item carrying one modality, so a batch or a mixed
// item is refused here rather than turned into an AWS schema error naming a key the
// caller never wrote.
func ToBedrockNovaEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*BedrockNovaEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	if len(bifrostReq.Input) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("no input provided for Nova embedding")
	}

	if len(bifrostReq.Input) != 1 {
		return nil, providerUtils.InvalidRequestErrorf("amazon Nova embedding models support exactly one content item per request; got %d", len(bifrostReq.Input))
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams("amazon Nova"); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	params := &BedrockNovaSingleEmbeddingParams{EmbeddingPurpose: BedrockNovaEmbeddingPurposeGenericIndex}
	truncationMode := BedrockNovaTruncationModeEnd
	videoMode := BedrockNovaVideoModeAudioVideoCombined
	var detailLevel *BedrockNovaDetailLevel
	extra := map[string]interface{}{}

	if bifrostReq.Params != nil {
		params.EmbeddingDimension = bifrostReq.Params.Dimensions
		if bifrostReq.Params.TaskType != nil && *bifrostReq.Params.TaskType != "" {
			params.EmbeddingPurpose = BedrockNovaEmbeddingPurpose(*bifrostReq.Params.TaskType)
		}
		for k, v := range bifrostReq.Params.ExtraParams {
			s, isString := v.(string)
			switch {
			case k == BedrockNovaExtraParamEmbeddingPurpose && isString && s != "":
				params.EmbeddingPurpose = BedrockNovaEmbeddingPurpose(s)
			case k == BedrockNovaExtraParamTruncationMode && isString && s != "":
				truncationMode = BedrockNovaTruncationMode(s)
			case k == BedrockNovaExtraParamEmbeddingMode && isString && s != "":
				videoMode = BedrockNovaVideoMode(s)
			case k == BedrockNovaExtraParamDetailLevel && isString && s != "":
				detailLevel = schemas.Ptr(BedrockNovaDetailLevel(s))
			default:
				extra[k] = v
			}
		}
	}

	var sb strings.Builder
	hasText := false
	modalities := 0
	for _, part := range bifrostReq.Input[0].Content {
		switch part.Type {
		case schemas.EmbeddingContentPartTypeText:
			if part.Text == nil {
				return nil, providerUtils.InvalidRequestErrorf("text part carries no text")
			}
			if hasText {
				sb.WriteString("\n")
			} else {
				hasText = true
				modalities++
			}
			sb.WriteString(*part.Text)
		case schemas.EmbeddingContentPartTypeImage, schemas.EmbeddingContentPartTypeAudio, schemas.EmbeddingContentPartTypeVideo:
			media := part.Image
			if part.Type == schemas.EmbeddingContentPartTypeAudio {
				media = part.Audio
			} else if part.Type == schemas.EmbeddingContentPartTypeVideo {
				media = part.Video
			}
			format, source, err := novaEmbeddingMediaPart(part.Type, media)
			if err != nil {
				return nil, err
			}
			switch part.Type {
			case schemas.EmbeddingContentPartTypeImage:
				params.Image = &BedrockNovaEmbeddingImage{Format: format, Source: source, DetailLevel: detailLevel}
			case schemas.EmbeddingContentPartTypeAudio:
				params.Audio = &BedrockNovaEmbeddingMedia{Format: format, Source: source}
			default:
				params.Video = &BedrockNovaEmbeddingVideo{Format: format, EmbeddingMode: videoMode, Source: source}
			}
			modalities++
		default:
			return nil, providerUtils.InvalidRequestErrorf("amazon Nova embedding models do not support %q parts", part.Type)
		}
	}

	if modalities == 0 {
		return nil, providerUtils.InvalidRequestErrorf("no input provided for Nova embedding")
	}
	if modalities > 1 {
		return nil, providerUtils.InvalidRequestErrorf("amazon Nova embedding models embed one modality per request: text, image, audio or video, not a combination")
	}

	if hasText {
		value := sb.String()
		if strings.TrimSpace(value) == "" {
			return nil, providerUtils.InvalidRequestErrorf("text part carries no text")
		}
		params.Text = &BedrockNovaEmbeddingText{TruncationMode: truncationMode, Value: &value}
	}

	req := &BedrockNovaEmbeddingRequest{
		TaskType:              TaskTypeSingleEmbedding,
		SingleEmbeddingParams: params,
	}
	if len(extra) > 0 {
		req.ExtraParams = extra
	}
	return req, nil
}

// novaEmbeddingModalities maps Nova's per-vector embeddingType label onto the canonical
// modality. AUDIO_VIDEO_COMBINED is one vector covering both, which the separate mode
// splits into an AUDIO and a VIDEO entry.
var novaEmbeddingModalities = map[string]schemas.EmbeddingModality{
	"TEXT":                 schemas.EmbeddingModalityText,
	"IMAGE":                schemas.EmbeddingModalityImage,
	"AUDIO":                schemas.EmbeddingModalityAudio,
	"VIDEO":                schemas.EmbeddingModalityVideo,
	"AUDIO_VIDEO_COMBINED": schemas.EmbeddingModalityAudioVideo,
}

// novaEmbeddingTypes is the reverse of novaEmbeddingModalities, for rebuilding the
// native envelope on the Bedrock integration's invoke route.
var novaEmbeddingTypes = map[schemas.EmbeddingModality]string{
	schemas.EmbeddingModalityText:       "TEXT",
	schemas.EmbeddingModalityImage:      "IMAGE",
	schemas.EmbeddingModalityAudio:      "AUDIO",
	schemas.EmbeddingModalityVideo:      "VIDEO",
	schemas.EmbeddingModalityAudioVideo: "AUDIO_VIDEO_COMBINED",
}

// ToBifrostEmbeddingResponse converts a Bedrock Nova embedding response to Bifrost format.
// Usage is left unset: Nova reports input tokens only in the
// X-Amzn-Bedrock-Input-Token-Count header, which the caller backfills from.
func (r *BedrockNovaEmbeddingResponse) ToBifrostEmbeddingResponse() (*schemas.BifrostEmbeddingResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("nil Bedrock Nova embedding response")
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{Object: "list"}
	for i, embedding := range r.Embeddings {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
			Index:     i,
			Object:    "embedding",
			Modality:  novaEmbeddingModalities[embedding.EmbeddingType],
			Embedding: schemas.EmbeddingStruct{EmbeddingArray: embedding.Embedding},
		})
	}
	return bifrostResponse, nil
}

// DetermineEmbeddingModelType determines the embedding model type for the
// current attempt. It consults the resolved alias family first
// (model_family / model_name / model_id / alias key) and falls back to the
// substring detectors against the wire model — so an alias to an opaque
// Bedrock deployment that's tagged with the right family routes correctly.
func DetermineEmbeddingModelType(ctx *schemas.BifrostContext, model string) (string, error) {
	switch {
	case schemas.IsNovaModelFamily(ctx, model):
		return "nova", nil
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