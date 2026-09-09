package anthropic

import (
	"encoding/base64"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// urlSourceRef locates a single {"type":"url"} content source inside an
// Anthropic-shaped request body. Rewrites are collected before any are applied:
// gjson.Result values hold offsets into the body they were parsed from, so
// mutating the body mid-walk would invalidate every path still to be visited.
type urlSourceRef struct {
	path      string // sjson path of the source object, e.g. messages.0.content.1.source
	url       string
	blockType string // "document" | "image" | other
}

// InlineURLContentSources replaces every URL-sourced image and document in an
// Anthropic-shaped request body with an inline (base64 or text) source, fetching
// the bytes over HTTP.
//
// The native Anthropic API accepts {"source":{"type":"url"}}, but AWS-hosted Claude
// does not: Bedrock Mantle rejects it with "URL content sources are not yet supported
// for this model". Bedrock's Converse path already inlines these (see
// core/providers/bedrock/utils.go), so this brings the native-Anthropic surface to
// parity rather than introducing a new behaviour.
//
// Fetching goes through providerUtils.FetchAndEncodeURL, which enforces http/https
// only, an SSRF-safe dialer, and a size cap. A failed fetch aborts the request: a
// silently dropped attachment would produce a confidently wrong answer about a
// document the model never saw.
func InlineURLContentSources(ctx *schemas.BifrostContext, body []byte) ([]byte, error) {
	refs := collectURLSourceRefs(body)
	if len(refs) == 0 {
		return body, nil
	}

	for _, ref := range refs {
		// Leave references bifrost cannot download in place rather than failing the
		// request here. Bifrost only inlines what it can actually fetch; deciding that a
		// scheme is unusable is the provider's call, and its answer stays accurate as its
		// capabilities change. The source travels as {"type":"url"} and the platform
		// responds for itself.
		if scheme := urlScheme(ref.url); scheme != "http" && scheme != "https" {
			continue
		}
		mediaType, encoded, err := providerUtils.FetchAndEncodeURL(ctx, ref.url)
		if err != nil {
			return nil, fmt.Errorf("failed to inline URL content source %q: %w", providerUtils.RedactURLForError(ref.url), err)
		}
		body, err = applyInlinedSource(body, ref, mediaType, encoded)
		if err != nil {
			return nil, err
		}
	}

	return body, nil
}

// urlScheme returns the lowercase scheme of rawURL, or "" when it has none or does not
// parse. Used only to phrase the rejection above; FetchAndEncodeURL remains the actual
// gate on what gets dialled.
func urlScheme(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Scheme)
}

// applyInlinedSource rewrites one url source into its inline form. Split out from the
// fetch loop so it is unit-testable: FetchAndEncodeURL dials through
// network.SSRFSafeDialContext, which rejects loopback unconditionally and has no test
// seam, so an httptest server is unreachable by design (same constraint documented in
// core/providers/openai/chatfileurl_test.go).
func applyInlinedSource(body []byte, ref urlSourceRef, mediaType, encoded string) ([]byte, error) {
	// Content-Type wins over the URL extension, but servers commonly answer with
	// a generic octet-stream for PDFs, so fall back to the extension in that case.
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = strings.ToLower(parsed)
	}
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = mediaTypeFromURL(ref.url, ref.blockType)
	}

	// The URL is named by its redacted form only: a pre-signed URL's query is a bearer
	// credential, and this error reaches the client through the provider request error
	// path (requestbuilder.go wraps it as ErrProviderRequestMarshal). Neither cause here
	// carries the URL, so wrapping them is safe.
	source, err := buildInlineSource(mediaType, encoded)
	if err != nil {
		return nil, fmt.Errorf("failed to inline URL content source %q: %w", providerUtils.RedactURLForError(ref.url), err)
	}

	body, err = sjson.SetBytes(body, ref.path, source)
	if err != nil {
		return nil, fmt.Errorf("failed to inline URL content source %q: %w", providerUtils.RedactURLForError(ref.url), err)
	}
	return body, nil
}

// collectURLSourceRefs walks messages[].content[] and returns every url-typed source.
// Content that is a plain string (the shorthand form) carries no sources and is skipped.
func collectURLSourceRefs(body []byte) []urlSourceRef {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return nil
	}

	var refs []urlSourceRef
	messages.ForEach(func(msgIdx, message gjson.Result) bool {
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(blockIdx, block gjson.Result) bool {
			source := block.Get("source")
			if !source.Exists() || source.Get("type").String() != "url" {
				return true
			}
			resourceURL := source.Get("url").String()
			if resourceURL == "" {
				return true
			}
			refs = append(refs, urlSourceRef{
				path:      fmt.Sprintf("messages.%d.content.%d.source", msgIdx.Int(), blockIdx.Int()),
				url:       resourceURL,
				blockType: block.Get("type").String(),
			})
			return true
		})
		return true
	})
	return refs
}

// buildInlineSource renders the replacement source object. Anthropic carries text
// documents as a "text" source holding the decoded characters, and everything else
// as a "base64" source holding the encoded bytes.
func buildInlineSource(mediaType, encoded string) (map[string]any, error) {
	if strings.HasPrefix(mediaType, "text/") {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decoding fetched text content: %w", err)
		}
		return map[string]any{
			"type": "text",
			// Anthropic only models text/plain for text sources; markdown, csv and
			// html all arrive as characters and are read the same way.
			"media_type": "text/plain",
			"data":       string(decoded),
		}, nil
	}

	return map[string]any{
		"type":       "base64",
		"media_type": mediaType,
		"data":       encoded,
	}, nil
}

// mediaTypeFromURL infers a media type from the URL's file extension, used only when
// the response carried no usable Content-Type. Documents default to PDF because that
// is overwhelmingly what URL-sourced Anthropic documents are; an image with no usable
// signal keeps octet-stream so the provider reports a clear media-type error rather
// than silently misreading the bytes.
func mediaTypeFromURL(resourceURL, blockType string) string {
	if parsed, err := url.Parse(resourceURL); err == nil {
		if ext := path.Ext(parsed.Path); ext != "" {
			if byExt, _, err := mime.ParseMediaType(mime.TypeByExtension(ext)); err == nil && byExt != "" {
				return strings.ToLower(byExt)
			}
		}
	}
	if blockType == "document" {
		return "application/pdf"
	}
	return "application/octet-stream"
}
