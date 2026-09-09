package anthropic

import (
	"encoding/base64"
	"fmt"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"mime"
	"strconv"
	"strings"
	"unicode/utf8"
)

// inlineTextDataURL decodes explicitly encoded text documents while preserving legacy unprefixed plaintext.
func inlineTextDataURL(data string) *AnthropicSource {
	header, encoded, ok := strings.Cut(data, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return nil
	}
	media, _, err := mime.ParseMediaType(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"))
	if err != nil || !isTextDocumentMediaType(media) {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !utf8.Valid(decoded) {
		return nil
	}
	return &AnthropicSource{Type: "text", MediaType: schemas.Ptr("text/plain"), Data: schemas.Ptr(string(decoded))}
}

// normalizeBase64TextSources preserves base64 transport while adapting text documents to Anthropic's text source wire type.
func normalizeBase64TextSources(body []byte) ([]byte, error) {
	var paths []string
	gjson.GetBytes(body, "messages").ForEach(func(i, message gjson.Result) bool {
		message.Get("content").ForEach(func(j, block gjson.Result) bool {
			if block.Get("type").String() == "document" && block.Get("source.type").String() == "base64" && isTextDocumentMediaType(block.Get("source.media_type").String()) {
				paths = append(paths, "messages."+strconv.Itoa(int(i.Int()))+".content."+strconv.Itoa(int(j.Int()))+".source")
			}
			return true
		})
		return true
	})
	for _, path := range paths {
		source := gjson.GetBytes(body, path)
		decoded, err := base64.StdEncoding.DecodeString(source.Get("data").String())
		if err != nil || !utf8.Valid(decoded) {
			return nil, fmt.Errorf("invalid base64 UTF-8 text document")
		}
		body, err = sjson.SetBytes(body, path+".type", "text")
		if err != nil {
			return nil, err
		}
		body, err = sjson.SetBytes(body, path+".media_type", "text/plain")
		if err != nil {
			return nil, err
		}
		body, err = sjson.SetBytes(body, path+".data", string(decoded))
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// isTextDocumentMediaType recognizes textual document formats that Anthropic accepts as text sources.
func isTextDocumentMediaType(value string) bool {
	media, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return strings.HasPrefix(media, "text/") || media == "application/json" || strings.HasPrefix(media, "application/") && strings.HasSuffix(media, "+json")
}
