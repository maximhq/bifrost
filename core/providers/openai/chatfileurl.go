package openai

import (
	"context"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// mediaTypeExtensions maps the document types Chat Completions accepts onto a filename
// extension. OpenAI requires a filename whenever file_data is used, and rejects names whose
// extension disagrees with the payload.
var mediaTypeExtensions = map[string]string{
	"application/pdf":  ".pdf",
	"text/plain":       ".txt",
	"text/markdown":    ".md",
	"text/html":        ".html",
	"text/csv":         ".csv",
	"application/json": ".json",
}

// buildChatFileFromFetch turns a fetched document into the pair Chat Completions expects:
// file_data as a base64 data URI, and a filename.
//
// Content-Type frequently carries parameters ("application/pdf; charset=binary"); those must be
// stripped or the data URI is malformed.
func buildChatFileFromFetch(mediaType, encoded, sourceURL string) (fileData string, filename string) {
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil && parsed != "" {
		mediaType = parsed
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	fileData = fmt.Sprintf("data:%s;base64,%s", mediaType, encoded)

	// Prefer the URL's basename; fall back to a synthetic name, since a filename is mandatory
	// alongside file_data.
	if parsed, err := url.Parse(sourceURL); err == nil {
		if base := path.Base(parsed.Path); base != "" && base != "." && base != "/" && strings.Contains(base, ".") {
			return fileData, base
		}
	}
	ext := mediaTypeExtensions[mediaType]
	if ext == "" {
		ext = ".bin"
	}
	return fileData, "document" + ext
}

// inlineChatFileURLs resolves URL-sourced document blocks into inline base64, because Chat
// Completions accepts only file_id or file_data - never file_url. The canonical block shape
// documents this as "provider fetches at convert time" (schemas/chatcompletions.go); without it
// OpenAIChatRequest.MarshalJSON strips FileURL and the document is silently lost.
//
// Blocks that already carry a file_id or file_data are left untouched. A fetch failure also
// leaves the block as-is: this conversion cannot return an error, and the provider's own
// validation reports the problem with more context than a silently dropped attachment would.
func inlineChatFileURLs(ctx context.Context, messages []OpenAIMessage) {
	if ctx == nil {
		ctx = context.Background()
	}
	for i := range messages {
		msg := &messages[i]
		if msg.Content == nil || msg.Content.ContentBlocks == nil {
			continue
		}
		for j := range msg.Content.ContentBlocks {
			file := msg.Content.ContentBlocks[j].File
			if file == nil || file.FileURL == nil || *file.FileURL == "" {
				continue
			}
			if file.FileID != nil && *file.FileID != "" {
				continue
			}
			if file.FileData != nil && *file.FileData != "" {
				continue
			}

			mediaType, encoded, err := providerUtils.FetchAndEncodeURL(ctx, *file.FileURL)
			if err != nil {
				continue
			}

			fileData, filename := buildChatFileFromFetch(mediaType, encoded, *file.FileURL)
			fileCopy := *file
			fileCopy.FileData = schemas.Ptr(fileData)
			if fileCopy.Filename == nil || *fileCopy.Filename == "" {
				fileCopy.Filename = schemas.Ptr(filename)
			}
			fileCopy.FileURL = nil
			msg.Content.ContentBlocks[j].File = &fileCopy
		}
	}
}
