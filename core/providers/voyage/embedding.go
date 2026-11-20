package voyage

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// maxEmbeddingInputs is Voyage's per-request cap on inputs, for every embedding endpoint.
	maxEmbeddingInputs = 1000

	// contextualizedChunkSize is the chunk size, in tokens, requested when Voyage chunks a document
	// itself. It is the API maximum (32K), so a document is split only when it cannot fit in one
	// chunk.
	contextualizedChunkSize = 32_000

	inputTypeQuery    = "query"
	inputTypeDocument = "document"
)

// embeddingOptions are the request parameters shared by the three embedding endpoints, read once
// from Params and validated against the model.
type embeddingOptions struct {
	inputType      *string
	truncation     *bool
	dimension      *int
	dtype          *string // output_dtype, nil for float
	base64         bool    // encoding_format=base64
	dtypeRequested string  // dtype label of the vectors in the response: float, int8, uint8, binary, ubinary
}

// resolveEmbeddingModel looks the request's model up and checks it is served by the wanted endpoint.
// A model of another endpoint, or of another provider, is a caller error and never reaches Voyage.
func resolveEmbeddingModel(request *schemas.BifrostEmbeddingRequest) (string, voyageModel, error) {
	if request == nil || strings.TrimSpace(request.Model) == "" {
		return "", voyageModel{}, providerUtils.InvalidRequestErrorf("voyage embedding needs a model: there is no default model")
	}
	name, model, ok := lookupModel(request.Model)
	if !ok {
		return "", voyageModel{}, providerUtils.InvalidRequestErrorf("%q is not a Voyage model this provider serves", request.Model)
	}
	switch model.endpoint {
	case endpointEmbeddings, endpointContextualized, endpointMultimodal:
		return name, model, nil
	}
	return "", voyageModel{}, providerUtils.InvalidRequestErrorf("%q is a %s model, not an embedding model", name, model.endpoint)
}

// readEmbeddingOptions validates Params for one model. Only what the caller set is sent: Voyage
// applies its own defaults to the rest.
func readEmbeddingOptions(name string, model voyageModel, params *schemas.EmbeddingParameters) (*embeddingOptions, error) {
	opts := &embeddingOptions{dtypeRequested: schemas.EmbeddingEncodingFloat}
	if params == nil {
		return opts, nil
	}

	if params.Dimensions != nil {
		if !model.acceptsDimension(*params.Dimensions) {
			return nil, providerUtils.InvalidRequestErrorf("%s does not support dimensions=%d; supported values: %v", name, *params.Dimensions, model.dimensions)
		}
		opts.dimension = params.Dimensions
	}
	if params.AutoTruncate != nil {
		opts.truncation = params.AutoTruncate
	}

	extra := params.ExtraParams
	if raw, present := extra["input_type"]; present {
		inputType, ok := schemas.SafeExtractString(raw)
		if !ok || (inputType != inputTypeQuery && inputType != inputTypeDocument) {
			return nil, providerUtils.InvalidRequestErrorf("input_type must be %q or %q", inputTypeQuery, inputTypeDocument)
		}
		opts.inputType = &inputType
	}
	if raw, present := extra["truncation"]; present {
		truncation, ok := schemas.SafeExtractBool(raw)
		if !ok {
			return nil, providerUtils.InvalidRequestErrorf("truncation must be a boolean")
		}
		opts.truncation = &truncation
	}

	// encoding_format carries either the base64 transport encoding or, as in the other embedding
	// providers, the output dtype. output_dtype (extra param) sets the dtype next to base64.
	dtype := ""
	if params.EncodingFormat != nil {
		switch format := *params.EncodingFormat; format {
		case schemas.EmbeddingEncodingFloat, "":
		case schemas.EmbeddingEncodingBase64:
			opts.base64 = true
		case schemas.EmbeddingEncodingInt8, schemas.EmbeddingEncodingUint8, schemas.EmbeddingEncodingBinary, schemas.EmbeddingEncodingUbinary:
			dtype = format
		default:
			return nil, providerUtils.InvalidRequestErrorf("encoding_format %q is not supported; use float, base64, int8, uint8, binary or ubinary", format)
		}
	}
	if raw, present := extra["output_dtype"]; present {
		value, ok := schemas.SafeExtractString(raw)
		if !ok {
			return nil, providerUtils.InvalidRequestErrorf("output_dtype must be a string")
		}
		switch value {
		case schemas.EmbeddingEncodingFloat:
			if dtype != "" {
				return nil, providerUtils.InvalidRequestErrorf("encoding_format=%s conflicts with output_dtype=%s", dtype, value)
			}
		case schemas.EmbeddingEncodingInt8, schemas.EmbeddingEncodingUint8, schemas.EmbeddingEncodingBinary, schemas.EmbeddingEncodingUbinary:
			if dtype != "" && dtype != value {
				return nil, providerUtils.InvalidRequestErrorf("encoding_format=%s conflicts with output_dtype=%s", dtype, value)
			}
			dtype = value
		default:
			return nil, providerUtils.InvalidRequestErrorf("output_dtype %q is not supported; use float, int8, uint8, binary or ubinary", value)
		}
	}
	if dtype != "" {
		if !model.quantized {
			return nil, providerUtils.InvalidRequestErrorf("%s returns float vectors only; output dtype %s is not supported", name, dtype)
		}
		opts.dtype = &dtype
		opts.dtypeRequested = dtype
	}
	return opts, nil
}

// singleText returns the text of an input item that is exactly one text part: the only shape the
// text and contextualized endpoints take.
func singleText(index int, item schemas.EmbeddingInputItem) (string, error) {
	content := item.Content
	if len(content) != 1 || content[0].Type != schemas.EmbeddingContentPartTypeText || content[0].Text == nil {
		return "", providerUtils.InvalidRequestErrorf("input item %d must be a single text part; this model embeds text only", index)
	}
	if err := content[0].Validate(); err != nil {
		return "", providerUtils.InvalidRequestErrorf("input item %d: %s", index, err)
	}
	return *content[0].Text, nil
}

func checkInputCount(input []schemas.EmbeddingInputItem) error {
	if len(input) == 0 {
		return providerUtils.InvalidRequestErrorf("embedding input is not provided")
	}
	if len(input) > maxEmbeddingInputs {
		return providerUtils.InvalidRequestErrorf("voyage accepts at most %d inputs per request, got %d", maxEmbeddingInputs, len(input))
	}
	if err := schemas.EmbeddingInput(input).RejectPerItemParams(); err != nil {
		return providerUtils.InvalidRequestErrorf("%s", err)
	}
	return nil
}

// ToVoyageEmbeddingRequest converts a Bifrost request for a text embedding model to Voyage's
// /v1/embeddings body.
func ToVoyageEmbeddingRequest(request *schemas.BifrostEmbeddingRequest) (*VoyageEmbeddingRequest, error) {
	name, model, err := resolveEmbeddingModel(request)
	if err != nil {
		return nil, err
	}
	if model.endpoint != endpointEmbeddings {
		return nil, providerUtils.InvalidRequestErrorf("%q is a %s model; it is not served by the text embedding endpoint", name, model.endpoint)
	}
	if err := checkInputCount(request.Input); err != nil {
		return nil, err
	}
	opts, err := readEmbeddingOptions(name, model, request.Params)
	if err != nil {
		return nil, err
	}

	texts := make([]string, len(request.Input))
	for i, item := range request.Input {
		if texts[i], err = singleText(i, item); err != nil {
			return nil, err
		}
	}

	body := &VoyageEmbeddingRequest{
		Model:           name,
		Input:           texts,
		InputType:       opts.inputType,
		Truncation:      opts.truncation,
		OutputDimension: opts.dimension,
		OutputDtype:     opts.dtype,
	}
	if opts.base64 {
		body.EncodingFormat = schemas.Ptr(schemas.EmbeddingEncodingBase64)
	}
	if request.Params != nil {
		body.ExtraParams = request.Params.ExtraParams
	}
	return body, nil
}

// ToVoyageContextualizedRequest converts a Bifrost request for a contextualized model to Voyage's
// /v1/contextualizedembeddings body.
//
// Each input item is one document (or one query) as a single text part, and input_type is
// required because the two modes send different bodies. Documents go out as a flat list of
// strings with auto-chunking on, since Voyage rejects auto-chunking over nested lists; queries go
// out as one single-query list each, with no chunking. A pre-chunked document (several text
// parts in one item) is rejected rather than silently joined.
func ToVoyageContextualizedRequest(request *schemas.BifrostEmbeddingRequest) (*VoyageContextualizedRequest, error) {
	name, model, err := resolveEmbeddingModel(request)
	if err != nil {
		return nil, err
	}
	if model.endpoint != endpointContextualized {
		return nil, providerUtils.InvalidRequestErrorf("%q is not a contextualized embedding model", name)
	}
	if err := checkInputCount(request.Input); err != nil {
		return nil, err
	}
	opts, err := readEmbeddingOptions(name, model, request.Params)
	if err != nil {
		return nil, err
	}
	if opts.inputType == nil {
		return nil, providerUtils.InvalidRequestErrorf("%s needs input_type %q or %q: auto-chunking works on documents only and queries are sent unchunked", name, inputTypeDocument, inputTypeQuery)
	}
	if opts.dtype != nil || opts.base64 {
		return nil, providerUtils.InvalidRequestErrorf("%s is served as float vectors only", name)
	}
	if opts.truncation != nil {
		return nil, providerUtils.InvalidRequestErrorf("truncation is not a parameter of contextualized embeddings")
	}

	texts := make([]string, len(request.Input))
	for i, item := range request.Input {
		if len(item.Content) > 1 {
			return nil, providerUtils.InvalidRequestErrorf("input item %d has %d parts: send one text per item, Voyage chunks documents itself", i, len(item.Content))
		}
		if texts[i], err = singleText(i, item); err != nil {
			return nil, err
		}
	}

	body := &VoyageContextualizedRequest{
		Model:           name,
		InputType:       *opts.inputType,
		OutputDimension: opts.dimension,
	}
	if *opts.inputType == inputTypeDocument {
		body.Inputs = texts
		body.EnableAutoChunking = schemas.Ptr(true)
		body.ChunkSize = schemas.Ptr(contextualizedChunkSize)
	} else {
		queries := make([][]string, len(texts))
		for i, text := range texts {
			queries[i] = []string{text}
		}
		body.Inputs = queries
	}
	if request.Params != nil {
		body.ExtraParams = request.Params.ExtraParams
	}
	return body, nil
}

// totalTokens reads the billed token count and fails when the response has none: a response
// without usage cannot be priced, and pricing it at zero would hide the spend.
func totalTokens(usage *VoyageUsage) (int, error) {
	if usage == nil || usage.TotalTokens == nil {
		return 0, fmt.Errorf("voyage response carries no usage.total_tokens, so the request cannot be billed")
	}
	return *usage.TotalTokens, nil
}

// ToBifrostEmbeddingResponse maps a /v1/embeddings reply to Bifrost's shape. dtype is the output
// dtype that was requested: it decides how each vector is decoded and how it is labelled.
func (r *VoyageEmbeddingResponse) ToBifrostEmbeddingResponse(request *schemas.BifrostEmbeddingRequest, opts *embeddingOptions) (*schemas.BifrostEmbeddingResponse, error) {
	tokens, err := totalTokens(r.Usage)
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
		Usage:  &schemas.BifrostLLMUsage{PromptTokens: tokens, TotalTokens: tokens},
	}, nil
}

// checkResultCount compares the number of results with the number of inputs sent. In large
// payload mode the request body is streamed through unparsed, so request.Input is empty and there
// is nothing to compare against.
func checkResultCount(results int, request *schemas.BifrostEmbeddingRequest) error {
	if len(request.Input) == 0 || results == len(request.Input) {
		return nil
	}
	return fmt.Errorf("voyage returned %d results for %d inputs", results, len(request.Input))
}

// responseModel prefers the model Voyage reports and falls back to the one requested.
func responseModel(reported, requested string) string {
	if strings.TrimSpace(reported) != "" {
		return reported
	}
	return strings.TrimSpace(requested)
}

// decodeVectors decodes each vector of a text or multimodal reply by the requested encoding; nil
// opts means float vectors, which is what a reply to a request whose body was never parsed gets.
func decodeVectors(items []VoyageEmbeddingItem, opts *embeddingOptions) ([]schemas.EmbeddingData, error) {
	if opts == nil {
		opts = &embeddingOptions{dtypeRequested: schemas.EmbeddingEncodingFloat}
	}
	out := make([]schemas.EmbeddingData, len(items))
	for i, item := range items {
		embedding := schemas.EmbeddingStruct{}
		label := opts.dtypeRequested
		// The JSON kind of the vector is authoritative for base64: in large payload mode the request
		// parameters are not available to say how it was encoded.
		isString := len(item.Embedding) > 0 && item.Embedding[0] == '"'
		switch {
		case opts.base64 || isString:
			var encoded string
			if err := schemas.Unmarshal(item.Embedding, &encoded); err != nil {
				return nil, fmt.Errorf("voyage embedding %d is not a base64 string: %w", item.Index, err)
			}
			embedding.EmbeddingStr = &encoded
			label = schemas.EmbeddingEncodingBase64
		case label == schemas.EmbeddingEncodingInt8 || label == schemas.EmbeddingEncodingBinary:
			var values []int8
			if err := schemas.Unmarshal(item.Embedding, &values); err != nil {
				return nil, fmt.Errorf("voyage embedding %d is not an int8 array: %w", item.Index, err)
			}
			embedding.EmbeddingInt8Array = values
		case label == schemas.EmbeddingEncodingUint8 || label == schemas.EmbeddingEncodingUbinary:
			var values []int32
			if err := schemas.Unmarshal(item.Embedding, &values); err != nil {
				return nil, fmt.Errorf("voyage embedding %d is not a uint8 array: %w", item.Index, err)
			}
			embedding.EmbeddingInt32Array = values
		default:
			var values []float64
			if err := schemas.Unmarshal(item.Embedding, &values); err != nil {
				return nil, fmt.Errorf("voyage embedding %d is not a float array: %w", item.Index, err)
			}
			embedding.EmbeddingArray = values
		}
		out[i] = schemas.EmbeddingData{
			Index:          item.Index,
			Object:         "embedding",
			Embedding:      embedding,
			EncodingFormat: label,
		}
	}
	return out, nil
}

// ToBifrostEmbeddingResponse maps a /v1/contextualizedembeddings reply to Bifrost's shape.
//
// Each input stays one entry, at its own index. A document carries one vector per chunk as a 2D
// embedding, and Chunks holds the chunk texts when Voyage chunked it; a query carries its single
// vector flat.
func (r *VoyageContextualizedResponse) ToBifrostEmbeddingResponse(request *schemas.BifrostEmbeddingRequest, inputType string) (*schemas.BifrostEmbeddingResponse, error) {
	tokens, err := totalTokens(r.Usage)
	if err != nil {
		return nil, err
	}
	if err := checkResultCount(len(r.Data), request); err != nil {
		return nil, err
	}
	data := make([]schemas.EmbeddingData, len(r.Data))
	for i, group := range r.Data {
		if len(group.Data) == 0 {
			return nil, fmt.Errorf("voyage returned no vectors for input %d", group.Index)
		}
		entry := schemas.EmbeddingData{Index: group.Index, Object: "embedding", EncodingFormat: schemas.EmbeddingEncodingFloat}
		if inputType == inputTypeQuery {
			if len(group.Data) != 1 {
				return nil, fmt.Errorf("voyage returned %d vectors for query %d, want 1", len(group.Data), group.Index)
			}
			entry.Embedding = schemas.EmbeddingStruct{EmbeddingArray: group.Data[0].Embedding}
		} else {
			vectors := make([][]float64, len(group.Data))
			chunks := make([]string, 0, len(group.Data))
			for j, chunk := range group.Data {
				vectors[j] = chunk.Embedding
				if chunk.Text != nil {
					chunks = append(chunks, *chunk.Text)
				}
			}
			// Texts come back only when Voyage chunked the document; a partial set would misalign
			// chunk texts with vectors, so it is dropped as a whole.
			if len(chunks) == len(vectors) {
				entry.Chunks = chunks
			}
			entry.Embedding = schemas.EmbeddingStruct{Embedding2DArray: vectors}
		}
		data[i] = entry
	}
	return &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  responseModel(r.Model, request.Model),
		Usage:  &schemas.BifrostLLMUsage{PromptTokens: tokens, TotalTokens: tokens},
	}, nil
}
