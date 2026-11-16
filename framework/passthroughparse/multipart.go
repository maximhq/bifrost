package passthroughparse

import (
	"bufio"
	"bytes"
	"mime"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// Bounds on a multipart summary, which is for reading, not for replay: one text field keeps at most
// maxMultipartFieldBytes (an oversized prompt is cut), and the summary as a whole keeps at most
// maxMultipartSummaryBytes of field text and maxMultipartParts parts before it stops recording text
// values. Either cut marks the summary truncated.
const (
	maxMultipartFieldBytes   = 64 << 10
	maxMultipartSummaryBytes = 256 << 10
	maxMultipartParts        = 1000
)

// MultipartSummary is the loggable form of a multipart/form-data passthrough body (audio
// transcriptions, image edits): its text fields as sent, and only the name, type and size of each
// file part. File contents are never kept.
type MultipartSummary struct {
	Fields    map[string]any  `json:"fields"` // a string, or a []string for a repeated field
	Files     []MultipartFile `json:"files,omitempty"`
	Truncated bool            `json:"truncated,omitempty"` // some field text was cut or left out to bound the summary
}

// MultipartFile describes one file part of a multipart body.
type MultipartFile struct {
	Field       string `json:"field"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Bytes       int64  `json:"bytes"`
}

// SummarizeMultipart turns a multipart/form-data body into the JSON of its MultipartSummary. It
// returns ok=false when contentType is not multipart or the body does not read as one, so the caller
// can keep its previous behaviour (no body logged).
//
// It runs on the request path (PreLLMHook, content logging on), so it never copies or decodes file
// parts: the body is already in memory, and each part's extent is found with bytes.Index on the
// delimiter, which skips file contents at memchr speed instead of streaming them through a reader.
func SummarizeMultipart(contentType string, body []byte) (summary []byte, ok bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || params["boundary"] == "" {
		return nil, false
	}
	dashBoundary := []byte("--" + params["boundary"])
	delimiter := append([]byte("\r\n"), dashBoundary...)

	start := bytes.Index(body, dashBoundary)
	if start < 0 {
		return nil, false
	}
	pos := start + len(dashBoundary)
	result := MultipartSummary{Fields: map[string]any{}}
	fieldParts, fieldBytes := 0, 0
	for {
		rest := body[pos:]
		if bytes.HasPrefix(rest, []byte("--")) {
			break // closing delimiter
		}
		if !bytes.HasPrefix(rest, []byte("\r\n")) {
			return nil, false
		}
		pos += 2
		headerEnd := bytes.Index(body[pos:], []byte("\r\n\r\n"))
		if headerEnd < 0 {
			return nil, false
		}
		header, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(body[pos : pos+headerEnd+4]))).ReadMIMEHeader()
		if err != nil {
			return nil, false
		}
		contentStart := pos + headerEnd + 4
		contentLen := bytes.Index(body[contentStart:], delimiter)
		if contentLen < 0 {
			return nil, false // truncated: no closing delimiter
		}
		content := body[contentStart : contentStart+contentLen]
		pos = contentStart + contentLen + len(delimiter)

		_, disposition, err := mime.ParseMediaType(header.Get("Content-Disposition"))
		if err != nil {
			// A part that does not read as a form part leaves the body unreadable as a form.
			return nil, false
		}
		name := disposition["name"]
		filename, isFile := disposition["filename"]
		partType := header.Get("Content-Type")
		if isFile || !isTextualPartType(partType) {
			result.Files = append(result.Files, MultipartFile{
				Field:       name,
				Filename:    filename,
				ContentType: partType,
				Bytes:       int64(len(content)),
			})
			continue
		}
		if name == "" {
			continue
		}
		if len(content) > maxMultipartFieldBytes {
			content = content[:maxMultipartFieldBytes]
			result.Truncated = true
		}
		if fieldParts >= maxMultipartParts || fieldBytes+len(content) > maxMultipartSummaryBytes {
			result.Truncated = true
			continue
		}
		fieldParts++
		fieldBytes += len(content)
		value := strings.ToValidUTF8(string(content), "")
		switch existing := result.Fields[name].(type) {
		case nil:
			result.Fields[name] = value
		case string:
			result.Fields[name] = []string{existing, value}
		case []string:
			result.Fields[name] = append(existing, value)
		}
	}
	out, err := providerUtils.MarshalSorted(result)
	if err != nil {
		return nil, false
	}
	return out, true
}

// isTextualPartType reports whether a part without a filename can be logged as a text field: it has
// no Content-Type (a plain form value) or a textual one. Anything else is recorded like a file, by
// name, type and size only.
func isTextualPartType(contentType string) bool {
	if contentType == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/x-www-form-urlencoded"
}

// parseMultipartSummary reads a request body logged by SummarizeMultipart; it returns nil for any
// other body (a raw multipart upload was never logged, so there is nothing else to read).
func parseMultipartSummary(body []byte) *MultipartSummary {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var summary MultipartSummary
	if err := sonic.Unmarshal(trimmed, &summary); err != nil || summary.Fields == nil {
		return nil
	}
	return &summary
}

func (s *MultipartSummary) str(key string) *string {
	switch v := s.Fields[key].(type) {
	case string:
		if v != "" {
			return &v
		}
	case []any:
		if len(v) > 0 {
			if first, ok := v[0].(string); ok && first != "" {
				return &first
			}
		}
	}
	return nil
}

// strs returns every value of a field, accepting both the bare and the PHP-style "name[]" spelling
// OpenAI clients use for arrays.
func (s *MultipartSummary) strs(key string) []string {
	var out []string
	for _, k := range []string{key, key + "[]"} {
		switch v := s.Fields[k].(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, item := range v {
				if str, ok := item.(string); ok {
					out = append(out, str)
				}
			}
		}
	}
	return out
}

func (s *MultipartSummary) integer(key string) *int {
	if v := s.str(key); v != nil {
		if n, err := strconv.Atoi(*v); err == nil {
			return &n
		}
	}
	return nil
}

func (s *MultipartSummary) float(key string) *float64 {
	if v := s.str(key); v != nil {
		if f, err := strconv.ParseFloat(*v, 64); err == nil {
			return &f
		}
	}
	return nil
}

func (s *MultipartSummary) files(fields ...string) []MultipartFile {
	var out []MultipartFile
	for _, f := range s.Files {
		for _, field := range fields {
			if f.Field == field {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func (s *MultipartSummary) model() string {
	if v := s.str("model"); v != nil {
		return *v
	}
	return ""
}

// parseOpenAIMultipartRequest builds the typed request of an OpenAI multipart route from its logged
// summary. File parts contribute their names only; their bytes were never logged.
func parseOpenAIMultipartRequest(requestType schemas.RequestType, body []byte) *schemas.BifrostRequest {
	summary := parseMultipartSummary(body)
	if summary == nil {
		return nil
	}
	switch requestType {
	case schemas.TranscriptionRequest:
		input := &schemas.TranscriptionInput{}
		if files := summary.files("file"); len(files) > 0 {
			input.Filename = files[0].Filename
		}
		return &schemas.BifrostRequest{RequestType: requestType, TranscriptionRequest: &schemas.BifrostTranscriptionRequest{
			Model: summary.model(),
			Input: input,
			Params: &schemas.TranscriptionParameters{
				Language:               summary.str("language"),
				Prompt:                 summary.str("prompt"),
				ResponseFormat:         summary.str("response_format"),
				Temperature:            summary.float("temperature"),
				TimestampGranularities: summary.strs("timestamp_granularities"),
				Include:                summary.strs("include"),
			},
		}}
	case schemas.ImageEditRequest:
		prompt := summary.str("prompt")
		if prompt == nil {
			return nil
		}
		input := &schemas.ImageEditInput{Prompt: *prompt}
		for range summary.files("image", "image[]") {
			input.Images = append(input.Images, schemas.ImageInput{})
		}
		return &schemas.BifrostRequest{RequestType: requestType, ImageEditRequest: &schemas.BifrostImageEditRequest{
			Model: summary.model(),
			Input: input,
			Params: &schemas.ImageEditParameters{
				Background:     summary.str("background"),
				InputFidelity:  summary.str("input_fidelity"),
				N:              summary.integer("n"),
				OutputFormat:   summary.str("output_format"),
				Quality:        summary.str("quality"),
				ResponseFormat: summary.str("response_format"),
				Size:           summary.str("size"),
				User:           summary.str("user"),
			},
		}}
	case schemas.ImageVariationRequest:
		return &schemas.BifrostRequest{RequestType: requestType, ImageVariationRequest: &schemas.BifrostImageVariationRequest{
			Model: summary.model(),
			Input: &schemas.ImageVariationInput{},
			Params: &schemas.ImageVariationParameters{
				N:              summary.integer("n"),
				ResponseFormat: summary.str("response_format"),
				Size:           summary.str("size"),
				User:           summary.str("user"),
			},
		}}
	}
	return nil
}
