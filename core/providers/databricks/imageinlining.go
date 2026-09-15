package databricks

import (
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// fetchAndEncodeURL is the download seam for image inlining. It is a variable so stub tests
// can stand in for the network: providerUtils.FetchAndEncodeURL dials through the SSRF-safe
// dialer, which rejects loopback unconditionally, so an httptest server is unreachable by
// design (the same constraint documented in core/providers/openai/chatfileurl_test.go).
var fetchAndEncodeURL = providerUtils.FetchAndEncodeURL

// inlineChatImageURLs replaces every http(s) image_url in a chat request with a data URL,
// fetching the bytes over HTTP. Databricks forwards image parts to the serving model as-is,
// and the Claude-backed endpoints reject remote URLs with "Http data URLs are not supported
// by Claude" (INVALID_PARAMETER_VALUE), so remote images have to be inlined before they
// leave Bifrost. Bedrock and Anthropic-on-Vertex already do this for the same reason.
//
// Work on a copy: the original request may be reused by fallbacks and post-hooks. A failed
// fetch aborts the request; a silently dropped image would produce a confidently wrong answer
// about a picture the model never saw. URLs Bifrost cannot fetch (data:, file IDs) are left
// untouched and the endpoint answers for itself.
func inlineChatImageURLs(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest) (*schemas.BifrostChatRequest, *schemas.BifrostError) {
	if request == nil || !chatRequestHasRemoteImage(request) {
		return request, nil
	}

	requestCopy := *request
	requestCopy.Input = make([]schemas.ChatMessage, len(request.Input))
	copy(requestCopy.Input, request.Input)

	for i := range requestCopy.Input {
		message := &requestCopy.Input[i]
		if message.Content == nil || len(message.Content.ContentBlocks) == 0 {
			continue
		}
		contentCopy := *message.Content
		contentCopy.ContentBlocks = make([]schemas.ChatContentBlock, len(message.Content.ContentBlocks))
		copy(contentCopy.ContentBlocks, message.Content.ContentBlocks)
		for j := range contentCopy.ContentBlocks {
			block := &contentCopy.ContentBlocks[j]
			if block.Type != schemas.ChatContentBlockTypeImage || block.ImageURLStruct == nil || !isRemoteHTTPURL(block.ImageURLStruct.URL) {
				continue
			}
			dataURL, err := fetchImageAsDataURL(ctx, block.ImageURLStruct.URL)
			if err != nil {
				return nil, imageInliningError(err)
			}
			imageCopy := *block.ImageURLStruct
			imageCopy.URL = dataURL
			block.ImageURLStruct = &imageCopy
		}
		message.Content = &contentCopy
	}
	return &requestCopy, nil
}

// inlineResponsesImageURLs is inlineChatImageURLs for the native Responses surface, where an
// input_image block carries the URL directly.
func inlineResponsesImageURLs(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesRequest, *schemas.BifrostError) {
	if request == nil || !responsesRequestHasRemoteImage(request) {
		return request, nil
	}

	requestCopy := *request
	requestCopy.Input = make([]schemas.ResponsesMessage, len(request.Input))
	copy(requestCopy.Input, request.Input)

	for i := range requestCopy.Input {
		message := &requestCopy.Input[i]
		if message.Content == nil || len(message.Content.ContentBlocks) == 0 {
			continue
		}
		contentCopy := *message.Content
		contentCopy.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, len(message.Content.ContentBlocks))
		copy(contentCopy.ContentBlocks, message.Content.ContentBlocks)
		for j := range contentCopy.ContentBlocks {
			block := &contentCopy.ContentBlocks[j]
			if !responsesBlockHasRemoteImage(block) {
				continue
			}
			dataURL, err := fetchImageAsDataURL(ctx, *block.ResponsesInputMessageContentBlockImage.ImageURL)
			if err != nil {
				return nil, imageInliningError(err)
			}
			imageCopy := *block.ResponsesInputMessageContentBlockImage
			imageCopy.ImageURL = schemas.Ptr(dataURL)
			block.ResponsesInputMessageContentBlockImage = &imageCopy
		}
		message.Content = &contentCopy
	}
	return &requestCopy, nil
}

func chatRequestHasRemoteImage(request *schemas.BifrostChatRequest) bool {
	for _, message := range request.Input {
		if message.Content == nil {
			continue
		}
		for _, block := range message.Content.ContentBlocks {
			if block.Type == schemas.ChatContentBlockTypeImage && block.ImageURLStruct != nil && isRemoteHTTPURL(block.ImageURLStruct.URL) {
				return true
			}
		}
	}
	return false
}

func responsesRequestHasRemoteImage(request *schemas.BifrostResponsesRequest) bool {
	for _, message := range request.Input {
		if message.Content == nil {
			continue
		}
		for i := range message.Content.ContentBlocks {
			if responsesBlockHasRemoteImage(&message.Content.ContentBlocks[i]) {
				return true
			}
		}
	}
	return false
}

func responsesBlockHasRemoteImage(block *schemas.ResponsesMessageContentBlock) bool {
	return block.Type == schemas.ResponsesInputMessageContentBlockTypeImage &&
		block.ResponsesInputMessageContentBlockImage != nil &&
		block.ResponsesInputMessageContentBlockImage.ImageURL != nil &&
		isRemoteHTTPURL(*block.ResponsesInputMessageContentBlockImage.ImageURL)
}

// isRemoteHTTPURL reports whether raw is something Bifrost can download. data: URLs are
// already inline and anything else (a file ID, an unsupported scheme) is not ours to judge.
func isRemoteHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "http" || scheme == "https"
}

// fetchImageAsDataURL downloads resourceURL and returns it as a data URL. Content-Type wins
// over the URL extension, but servers commonly answer with a generic octet-stream, so the
// extension is the fallback; with neither the endpoint reports the media-type error itself.
func fetchImageAsDataURL(ctx *schemas.BifrostContext, resourceURL string) (string, error) {
	mediaType, encoded, err := fetchAndEncodeURL(ctx, resourceURL)
	if err != nil {
		return "", fmt.Errorf("failed to inline image URL %q: %w", providerUtils.RedactURLForError(resourceURL), err)
	}
	if parsed, _, parseErr := mime.ParseMediaType(mediaType); parseErr == nil {
		mediaType = strings.ToLower(parsed)
	}
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = imageMediaTypeFromURL(resourceURL)
	}
	return "data:" + mediaType + ";base64," + encoded, nil
}

func imageMediaTypeFromURL(resourceURL string) string {
	if parsed, err := url.Parse(resourceURL); err == nil {
		if ext := path.Ext(parsed.Path); ext != "" {
			if byExt, _, err := mime.ParseMediaType(mime.TypeByExtension(ext)); err == nil && byExt != "" {
				return strings.ToLower(byExt)
			}
		}
	}
	return "application/octet-stream"
}

// imageInliningError surfaces a fetch failure as a client-visible 400: the request as written
// cannot be served, and the caller can fix the URL. The message carries the redacted URL only.
func imageInliningError(err error) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(400),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr("invalid_request_error"),
			Message: err.Error(),
			Error:   err,
		},
	}
}
