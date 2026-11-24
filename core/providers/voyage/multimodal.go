package voyage

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// supportedImageMIME and supportedVideoMIME are the media types of a base64 data URL.
var (
	supportedImageMIME = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true}
	supportedVideoMIME = map[string]bool{"video/mp4": true}
)

// dataURL returns the media as a data URL, which Voyage requires for base64 content: the data as
// given when it already is one, otherwise built from its MIME type. A bare base64 string without
// a MIME type cannot be sent, and is rejected instead of guessing a type.
func dataURL(kind string, media *schemas.EmbeddingMediaPart, allowed map[string]bool) (string, error) {
	data := *media.Data
	mime := ""
	if rest, ok := strings.CutPrefix(data, "data:"); ok {
		header, _, found := strings.Cut(rest, ",")
		if !found || !strings.HasSuffix(header, ";base64") {
			return "", providerUtils.InvalidRequestErrorf("%s data must be a base64 data URL (data:<type>;base64,<data>)", kind)
		}
		mime = strings.TrimSuffix(header, ";base64")
	} else {
		if media.MIMEType == nil || *media.MIMEType == "" {
			return "", providerUtils.InvalidRequestErrorf("%s data needs a mime_type or must be a data URL", kind)
		}
		mime = *media.MIMEType
		data = "data:" + mime + ";base64," + data
	}
	if !allowed[mime] {
		return "", providerUtils.InvalidRequestErrorf("%s media type %q is not supported by Voyage", kind, mime)
	}
	return data, nil
}

// ToVoyageMultimodalRequest converts a Bifrost request for a multimodal model to Voyage's
// /v1/multimodalembeddings body. Each input item becomes one input whose content keeps the order
// of its parts.
//
// Voyage requires one source style per request, so URL and base64 media cannot be mixed. Videos
// are accepted only by models that take them.
func ToVoyageMultimodalRequest(request *schemas.BifrostEmbeddingRequest) (*VoyageMultimodalRequest, error) {
	name, model, err := resolveEmbeddingModel(request)
	if err != nil {
		return nil, err
	}
	if model.endpoint != endpointMultimodal {
		return nil, providerUtils.InvalidRequestErrorf("%q is not a multimodal embedding model", name)
	}
	if err := checkInputCount(request.Input); err != nil {
		return nil, err
	}
	opts, err := readEmbeddingOptions(name, model, request.Params)
	if err != nil {
		return nil, err
	}
	if opts.dtype != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s returns float vectors, optionally base64 encoded; quantized output is not supported", name)
	}

	inputs := make([]VoyageMultimodalInput, len(request.Input))
	var sawURL, sawBase64 bool
	for i, item := range request.Input {
		if err := item.Content.Validate(); err != nil {
			return nil, providerUtils.InvalidRequestErrorf("input item %d: %s", i, err)
		}
		parts := make([]VoyageMultimodalPart, 0, len(item.Content))
		for _, part := range item.Content {
			converted, usesURL, err := toVoyageMultimodalPart(name, model, part)
			if err != nil {
				return nil, providerUtils.InvalidRequestErrorf("input item %d: %s", i, err)
			}
			switch {
			case converted.Type == "text":
			case usesURL:
				sawURL = true
			default:
				sawBase64 = true
			}
			parts = append(parts, converted)
		}
		inputs[i] = VoyageMultimodalInput{Content: parts}
	}
	if sawURL && sawBase64 {
		return nil, providerUtils.InvalidRequestErrorf("a Voyage request takes media by URL or by base64, not both")
	}

	body := &VoyageMultimodalRequest{
		Model:           name,
		Inputs:          inputs,
		InputType:       opts.inputType,
		Truncation:      opts.truncation,
		OutputDimension: opts.dimension,
	}
	if opts.base64 {
		body.OutputEncoding = schemas.Ptr(schemas.EmbeddingEncodingBase64)
	}
	if request.Params != nil {
		body.ExtraParams = request.Params.ExtraParams
	}
	return body, nil
}

// toVoyageMultimodalPart converts one content part and reports whether it carries media by URL.
func toVoyageMultimodalPart(name string, model voyageModel, part schemas.EmbeddingContentPart) (VoyageMultimodalPart, bool, error) {
	switch part.Type {
	case schemas.EmbeddingContentPartTypeText:
		return VoyageMultimodalPart{Type: "text", Text: part.Text}, false, nil
	case schemas.EmbeddingContentPartTypeImage:
		if part.Image.URL != nil {
			return VoyageMultimodalPart{Type: "image_url", ImageURL: part.Image.URL}, true, nil
		}
		url, err := dataURL("image", part.Image, supportedImageMIME)
		if err != nil {
			return VoyageMultimodalPart{}, false, err
		}
		return VoyageMultimodalPart{Type: "image_base64", ImageBase64: &url}, false, nil
	case schemas.EmbeddingContentPartTypeVideo:
		if !model.video {
			return VoyageMultimodalPart{}, false, providerUtils.InvalidRequestErrorf("%s does not accept video", name)
		}
		if part.VideoConfig != nil {
			return VoyageMultimodalPart{}, false, providerUtils.InvalidRequestErrorf("video_config is not supported by Voyage")
		}
		if part.Video.URL != nil {
			return VoyageMultimodalPart{Type: "video_url", VideoURL: part.Video.URL}, true, nil
		}
		url, err := dataURL("video", part.Video, supportedVideoMIME)
		if err != nil {
			return VoyageMultimodalPart{}, false, err
		}
		return VoyageMultimodalPart{Type: "video_base64", VideoBase64: &url}, false, nil
	}
	return VoyageMultimodalPart{}, false, providerUtils.InvalidRequestErrorf("%s embeds text, image and video parts only, not %q", name, part.Type)
}

// ToBifrostEmbeddingResponse maps a /v1/multimodalembeddings reply to Bifrost's shape.
//
// Voyage bills text per token and media per pixel. PromptTokens is the text tokens and ImageTokens
// the token equivalent of the image and video pixels, so total_tokens stays their sum.
func (r *VoyageMultimodalResponse) ToBifrostEmbeddingResponse(request *schemas.BifrostEmbeddingRequest, opts *embeddingOptions) (*schemas.BifrostEmbeddingResponse, error) {
	usage, err := r.bifrostUsage()
	if err != nil {
		return nil, err
	}
	data, err := decodeVectors(r.Data, opts)
	if err != nil {
		return nil, err
	}
	if err := checkResultCount(len(data), request); err != nil {
		return nil, err
	}
	return &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  responseModel(r.Model, request.Model),
		Usage:  usage,
	}, nil
}

func (r *VoyageMultimodalResponse) bifrostUsage() (*schemas.BifrostLLMUsage, error) {
	u := r.Usage
	if u == nil || u.TotalTokens == nil || u.TextTokens == nil {
		return nil, fmt.Errorf("voyage response carries no usage.text_tokens and usage.total_tokens, so the request cannot be billed")
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: *u.TextTokens, TotalTokens: *u.TotalTokens}
	if mediaTokens := *u.TotalTokens - *u.TextTokens; mediaTokens > 0 {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{ImageTokens: mediaTokens}
	}
	return usage, nil
}
