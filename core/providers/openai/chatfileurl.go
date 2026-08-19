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

	ext := mediaTypeExtensions[mediaType]

	// Prefer the URL's basename; fall back to a synthetic name, since a filename is mandatory
	// alongside file_data.
	if parsed, err := url.Parse(sourceURL); err == nil {
		if base := path.Base(parsed.Path); base != "" && base != "." && base != "/" && strings.Contains(base, ".") {
			if extensionMatchesMediaType(path.Ext(base), mediaType) {
				return fileData, base
			}
			// The URL's extension contradicts what the server actually sent - a ".txt" URL that
			// responds application/pdf, say. OpenAI validates the filename against file_data's
			// media type and rejects the pair, so the FETCHED type wins: keep the stem, which is
			// the part that makes the attachment recognisable, and correct the suffix.
			if ext != "" {
				return fileData, strings.TrimSuffix(base, path.Ext(base)) + ext
			}
		}
	}
	if ext == "" {
		ext = ".bin"
	}
	return fileData, "document" + ext
}

// mediaTypeExtensionAliases lists additional extensions that are legitimate for a media type
// beyond the canonical one in mediaTypeExtensions. Deliberately small: the goal is not to accept
// every spelling in the wild, only to avoid renaming a file whose extension was already correct.
var mediaTypeExtensionAliases = map[string][]string{
	"text/html":     {".htm"},
	"text/markdown": {".markdown"},
}

// extensionMatchesMediaType reports whether a filename extension is consistent with the fetched
// media type. An unrecognised media type matches anything: without a mapping there is no basis to
// claim the caller's extension is wrong, and renaming on a guess would be worse than leaving it.
func extensionMatchesMediaType(ext, mediaType string) bool {
	canonical, known := mediaTypeExtensions[mediaType]
	if !known {
		return true
	}
	ext = strings.ToLower(ext)
	if ext == canonical {
		return true
	}
	for _, alias := range mediaTypeExtensionAliases[mediaType] {
		if ext == alias {
			return true
		}
	}
	return false
}

// ResolveChatFileURLs resolves URL-sourced document blocks into inline base64, because Chat
// Completions accepts only file_id or file_data - never file_url. The canonical block shape
// documents this as "provider fetches at convert time" (schemas/chatcompletions.go); without it
// OpenAIChatRequest.MarshalJSON strips FileURL and the document is silently lost.
//
// Deliberately NOT part of ToOpenAIChatRequest. That function is a pure transformation, and this
// one performs network I/O whose failures have to reach the caller. While it lived inside the
// converter there was nowhere to report a failed fetch: the block kept its FileURL, MarshalJSON
// stripped it, and an empty {"type":"file","file":{}} went upstream - so the operator saw a
// provider 400 about a missing file_id rather than the fetch error that actually happened.
//
// Scoped to OpenAI and Azure because they are the surfaces that reject file_url. Every other
// provider routed through this package either accepts the block as-is or resolves it elsewhere,
// and fetching on their behalf would be network I/O spent for nothing.
//
// Blocks that already carry a file_id or file_data are left untouched.
// Returns a plain error rather than a *schemas.BifrostError: every caller runs inside
// CheckContextAndGetRequestBody's converter closure, which already wraps a returned error as
// NewBifrostOperationError(ErrRequestBodyConversion, err). Building the structured error here would
// duplicate that and then be discarded.
func ResolveChatFileURLs(ctx *schemas.BifrostContext, provider schemas.ModelProvider, req *OpenAIChatRequest) error {
	if req == nil || (provider != schemas.OpenAI && provider != schemas.Azure) {
		return nil
	}

	var fetchCtx context.Context = ctx
	if ctx == nil {
		fetchCtx = context.Background()
	}

	messages := req.Messages
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

			// Leave references bifrost cannot download in place. Bifrost inlines only
			// what it can actually fetch; whether the surface accepts the reference is
			// the provider's call, and it now travels on the wire (MarshalJSON no longer
			// drops file_url) so the provider can answer for itself instead of the block
			// arriving empty.
			if scheme := urlSourceScheme(*file.FileURL); scheme != "http" && scheme != "https" {
				continue
			}

			mediaType, encoded, err := providerUtils.FetchAndEncodeURL(fetchCtx, *file.FileURL)
			if err != nil {
				return fmt.Errorf("failed to fetch document at %s for messages[%d].content[%d]: %w",
					*file.FileURL, i, j, err)
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
	return nil
}

// urlSourceScheme returns the lowercase scheme of rawURL, or "" when it has none or
// does not parse. Used only to phrase the rejection in ResolveChatFileURLs;
// FetchAndEncodeURL remains the actual gate on what gets dialled.
func urlSourceScheme(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Scheme)
}
