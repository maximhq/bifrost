package xai

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// XAITranscriptionResponse is the JSON body of xAI's POST /v1/stt.
type XAITranscriptionResponse struct {
	Text     string                 `json:"text"`
	Language *string                `json:"language,omitempty"`
	Duration *float64               `json:"duration,omitempty"`
	Words    []XAITranscriptionWord `json:"words,omitempty"`
}

// XAITranscriptionWord is one word with its timing in seconds. xAI names the word
// "text" where OpenAI's verbose_json uses "word".
type XAITranscriptionWord struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// ToBifrostTranscriptionResponse maps an xAI STT response onto the Bifrost shape.
// xAI bills speech-to-text by audio duration, so usage is reported in seconds.
func (r *XAITranscriptionResponse) ToBifrostTranscriptionResponse() *schemas.BifrostTranscriptionResponse {
	out := &schemas.BifrostTranscriptionResponse{
		Text:     r.Text,
		Language: r.Language,
		Duration: r.Duration,
		Task:     schemas.Ptr("transcribe"),
	}
	if len(r.Words) > 0 {
		out.Words = make([]schemas.TranscriptionWord, len(r.Words))
		for i, w := range r.Words {
			out.Words[i] = schemas.TranscriptionWord{Word: w.Text, Start: w.Start, End: w.End}
		}
	}
	if r.Duration != nil {
		out.Usage = &schemas.TranscriptionUsage{Type: "duration", Seconds: r.Duration}
	}
	return out
}

// handleXAITranscriptionResponse decodes xAI's STT body for
// openai.HandleOpenAITranscriptionRequest.
func handleXAITranscriptionResponse(responseBody []byte, response *schemas.BifrostTranscriptionResponse, _ []byte, _ bool, sendBackRawResponse bool) (interface{}, interface{}, *schemas.BifrostError) {
	var xaiResp XAITranscriptionResponse
	if err := sonic.Unmarshal(responseBody, &xaiResp); err != nil {
		return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	*response = *xaiResp.ToBifrostTranscriptionResponse()

	var rawResponse interface{}
	if sendBackRawResponse {
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err)
		}
	}
	return nil, rawResponse, nil
}

// maxPartHeaderBytes bounds the multipart part headers fileLastGuard buffers. A part
// whose headers run longer is not a form field the guard can judge, so it stops checking.
const maxPartHeaderBytes = 8 << 10

const (
	guardSeekDelimiter = iota
	guardReadHeaders
	guardDone
)

// fileLastGuard passes a staged multipart body through unchanged and fails the read
// once a part follows the "file" part, since /v1/stt ignores every field after the file.
// It keeps only a delimiter-sized tail or the current part's headers, never the body.
type fileLastGuard struct {
	r        io.Reader
	delim    []byte // "\r\n--" + boundary
	buf      []byte // seek: unmatched tail of the last read; headers: the part's header bytes so far
	state    int
	seenFile bool
	err      error
}

// guardStagedTranscriptionBody wraps the staged large-payload reader in a fileLastGuard
// and returns it, or nil when the staged body is not multipart with a boundary.
func guardStagedTranscriptionBody(ctx *schemas.BifrostContext) *fileLastGuard {
	contentType, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadContentType).(string)
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil
	}
	reader, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadReader).(io.Reader)
	if reader == nil {
		return nil
	}
	guard := newFileLastGuard(reader, params["boundary"])
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, io.Reader(guard))
	return guard
}

func newFileLastGuard(r io.Reader, boundary string) *fileLastGuard {
	delim := []byte("\r\n--" + boundary)
	buf := make([]byte, 0, 2*len(delim))
	// The first delimiter opens the body without a preceding CRLF.
	buf = append(buf, '\r', '\n')
	return &fileLastGuard{r: r, delim: delim, buf: buf}
}

func (g *fileLastGuard) Read(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	n, err := g.r.Read(p)
	if n > 0 {
		g.scan(p[:n])
		if g.err != nil {
			return 0, g.err
		}
	}
	return n, err
}

func (g *fileLastGuard) scan(data []byte) {
	for len(data) > 0 && g.state != guardDone {
		if g.state == guardSeekDelimiter {
			if len(g.buf) > 0 {
				// A delimiter may straddle the previous read and this one.
				head := data[:min(len(data), len(g.delim)-1)]
				joined := append(g.buf, head...)
				if i := bytes.Index(joined, g.delim); i >= 0 {
					data = data[i+len(g.delim)-len(g.buf):]
					g.buf, g.state = g.buf[:0], guardReadHeaders
					continue
				}
				if len(head) == len(data) {
					keep := min(len(joined), len(g.delim)-1)
					g.buf = append(g.buf[:0], joined[len(joined)-keep:]...)
					return
				}
				g.buf = g.buf[:0]
			}
			if i := bytes.Index(data, g.delim); i >= 0 {
				data = data[i+len(g.delim):]
				g.state = guardReadHeaders
				continue
			}
			keep := min(len(data), len(g.delim)-1)
			g.buf = append(g.buf[:0], data[len(data)-keep:]...)
			return
		}

		// guardReadHeaders: the bytes after a delimiter, up to the blank line ending the headers.
		prev := len(g.buf)
		g.buf = append(g.buf, data[:min(len(data), maxPartHeaderBytes-prev)]...)
		if len(g.buf) >= 2 && g.buf[0] == '-' && g.buf[1] == '-' {
			g.state = guardDone // closing delimiter
			return
		}
		end := bytes.Index(g.buf, []byte("\r\n\r\n"))
		if end < 0 {
			if len(g.buf) >= maxPartHeaderBytes {
				g.state = guardDone
			}
			return
		}
		g.checkPart(g.buf[:end])
		if g.err != nil {
			return
		}
		data = data[end+4-prev:]
		g.buf, g.state = g.buf[:0], guardSeekDelimiter
	}
}

// checkPart records the file part and fails on any part that follows it.
func (g *fileLastGuard) checkPart(headers []byte) {
	name := ""
	for _, line := range strings.Split(string(headers), "\r\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Content-Disposition") {
			continue
		}
		if _, params, err := mime.ParseMediaType(value); err == nil {
			name = params["name"]
		}
		break
	}
	if g.seenFile {
		g.err = fmt.Errorf("xAI transcription ignores multipart fields after the file: field %q follows it, send the file as the last part", name)
		return
	}
	if name == "file" {
		g.seenFile = true
	}
}
