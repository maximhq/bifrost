package openai

import (
	"errors"
	"strconv"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/valyala/fasthttp"
)

// OpenAIRouter holds route registrations for OpenAI endpoints.
// It supports standard chat completions, speech synthesis, audio transcription, and streaming capabilities with OpenAI-specific formatting.
type OpenAIRouter struct {
	*integrations.GenericRouter
}

// NewOpenAIRouter creates a new OpenAIRouter with the given bifrost client.
func NewOpenAIRouter(client *bifrost.Bifrost) *OpenAIRouter {
	var routes []integrations.RouteConfig

	// Chat completions endpoint
	for _, path := range []string{
		"/openai/v1/chat/completions",
		"/openai/chat/completions",
	} {
		routes = append(routes, integrations.RouteConfig{
			Path:   path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAIChatRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*OpenAIChatRequest); ok {
					return openaiReq.ConvertToBifrostRequest(), nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveOpenAIFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveOpenAIErrorFromBifrostError(err)
			},
			StreamConfig: &integrations.StreamConfig{
				ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
					return DeriveOpenAIStreamFromBifrostResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return DeriveOpenAIStreamFromBifrostError(err)
				},
			},
		})
	}

	// Embeddings endpoint
	for _, path := range []string{
		"/openai/v1/embeddings",
		"/openai/embeddings",
	} {
		routes = append(routes, integrations.RouteConfig{
			Path:   path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAIEmbeddingRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if embeddingReq, ok := req.(*OpenAIEmbeddingRequest); ok {
					return embeddingReq.ConvertToBifrostRequest(), nil
				}
				return nil, errors.New("invalid embedding request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveOpenAIEmbeddingFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveOpenAIErrorFromBifrostError(err)
			},
		})
	}

	// Speech synthesis endpoint
	for _, path := range []string{
		"/openai/v1/audio/speech",
		"/openai/audio/speech",
	} {
		routes = append(routes, integrations.RouteConfig{
			Path:   path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAISpeechRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if speechReq, ok := req.(*OpenAISpeechRequest); ok {
					return speechReq.ConvertToBifrostRequest(), nil
				}
				return nil, errors.New("invalid speech request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				speechResp := DeriveOpenAISpeechFromBifrostResponse(resp)
				if speechResp == nil {
					return nil, errors.New("failed to convert speech response")
				}
				// For speech, we return the raw audio data directly
				return speechResp.Audio, nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveOpenAIErrorFromBifrostError(err)
			},
			StreamConfig: &integrations.StreamConfig{
				ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
					return DeriveOpenAISpeechFromBifrostResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return DeriveOpenAIErrorFromBifrostError(err)
				},
			},
		})
	}

	// Audio transcription endpoint
	for _, path := range []string{
		"/openai/v1/audio/transcriptions",
		"/openai/audio/transcriptions",
	} {
		routes = append(routes, integrations.RouteConfig{
			Path:   path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAITranscriptionRequest{}
			},
			RequestParser: parseTranscriptionMultipartRequest, // Handle multipart form parsing
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if transcriptionReq, ok := req.(*OpenAITranscriptionRequest); ok {
					return transcriptionReq.ConvertToBifrostRequest(), nil
				}
				return nil, errors.New("invalid transcription request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveOpenAITranscriptionFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveOpenAIErrorFromBifrostError(err)
			},
			StreamConfig: &integrations.StreamConfig{
				ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
					return DeriveOpenAITranscriptionFromBifrostResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return DeriveOpenAIErrorFromBifrostError(err)
				},
			},
		})
	}

	return &OpenAIRouter{
		GenericRouter: integrations.NewGenericRouter(client, routes),
	}
}

// parseTranscriptionMultipartRequest is a RequestParser that handles multipart/form-data for transcription requests
func parseTranscriptionMultipartRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	transcriptionReq, ok := req.(*OpenAITranscriptionRequest)
	if !ok {
		return errors.New("invalid request type for transcription")
	}

	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}

	// Extract model (required)
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		return errors.New("model field is required")
	}
	transcriptionReq.Model = modelValues[0]

	// Extract file (required)
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		return errors.New("file field is required")
	}

	fileHeader := fileHeaders[0]
	file, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	// Read file data
	fileData := make([]byte, fileHeader.Size)
	if _, err := file.Read(fileData); err != nil {
		return err
	}
	transcriptionReq.File = fileData

	// Extract optional parameters
	if languageValues := form.Value["language"]; len(languageValues) > 0 && languageValues[0] != "" {
		language := languageValues[0]
		transcriptionReq.Language = &language
	}

	if promptValues := form.Value["prompt"]; len(promptValues) > 0 && promptValues[0] != "" {
		prompt := promptValues[0]
		transcriptionReq.Prompt = &prompt
	}

	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		responseFormat := responseFormatValues[0]
		transcriptionReq.ResponseFormat = &responseFormat
	}

	if temperatureValues := form.Value["temperature"]; len(temperatureValues) > 0 && temperatureValues[0] != "" {
		temp, err := strconv.ParseFloat(temperatureValues[0], 64)
		if err != nil {
			return errors.New("invalid temperature value")
		}
		transcriptionReq.Temperature = &temp
	}

	// Handle include[] array format used by OpenAI
	if includeValues := form.Value["include[]"]; len(includeValues) > 0 {
		transcriptionReq.Include = includeValues
	} else if includeValues := form.Value["include"]; len(includeValues) > 0 && includeValues[0] != "" {
		// Fallback: Handle comma-separated values for backwards compatibility
		includes := strings.Split(includeValues[0], ",")
		// Trim whitespace from each value
		for i, v := range includes {
			includes[i] = strings.TrimSpace(v)
		}
		transcriptionReq.Include = includes
	}

	// Handle timestamp_granularities[] array format used by OpenAI
	if timestampValues := form.Value["timestamp_granularities[]"]; len(timestampValues) > 0 {
		transcriptionReq.TimestampGranularities = timestampValues
	} else if timestampValues := form.Value["timestamp_granularities"]; len(timestampValues) > 0 && timestampValues[0] != "" {
		// Fallback: Handle comma-separated values for backwards compatibility
		granularities := strings.Split(timestampValues[0], ",")
		// Trim whitespace from each value
		for i, v := range granularities {
			granularities[i] = strings.TrimSpace(v)
		}
		transcriptionReq.TimestampGranularities = granularities
	}

	if streamValues := form.Value["stream"]; len(streamValues) > 0 && streamValues[0] != "" {
		stream, err := strconv.ParseBool(streamValues[0])
		if err != nil {
			return errors.New("invalid stream value")
		}
		transcriptionReq.Stream = &stream
	}

	return nil
}
