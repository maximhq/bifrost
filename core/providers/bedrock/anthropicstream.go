package bedrock

import (
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// bedrockAnthropicEventReader adapts Bedrock's AWS EventStream framing to the
// providerUtils.SSEEventReader contract so that Claude-on-Bedrock streams can be
// driven by the shared anthropic.StreamAnthropicChatEvents /
// StreamAnthropicResponsesEvents loops.
//
// Bedrock's invoke-with-response-stream returns binary EventStream frames, each
// "event" frame carrying one native Anthropic SSE chunk inside a {"bytes": ...}
// payload. This reader decodes a frame, unwraps the inner chunk, and derives the
// SSE-style event type from the chunk's own "type" field (the same value the
// native `event:` line carries). AWS exception frames and transport failures are
// surfaced as *anthropic.StreamReaderError so the shared loop can forward a
// BifrostError with the right retry status code.
type bedrockAnthropicEventReader struct {
	decoder      *eventstream.Decoder
	src          io.Reader
	payloadBuf   []byte
	providerName schemas.ModelProvider
}

// newBedrockAnthropicEventReader builds a reader over an already-wrapped body
// stream (idle-timeout + cancellation wrapping is the caller's responsibility).
func newBedrockAnthropicEventReader(src io.Reader, providerName schemas.ModelProvider) *bedrockAnthropicEventReader {
	return &bedrockAnthropicEventReader{
		decoder:      eventstream.NewDecoder(),
		src:          src,
		payloadBuf:   make([]byte, 0, 1024*1024), // 1MB payload buffer
		providerName: providerName,
	}
}

// ReadEvent implements providerUtils.SSEEventReader. It returns the next decoded
// Anthropic chunk as (eventType, rawChunkJSON), io.EOF at end of stream, or an
// error. Retryable AWS exceptions and transport errors are wrapped in
// *anthropic.StreamReaderError carrying a classified *schemas.BifrostError.
func (r *bedrockAnthropicEventReader) ReadEvent() (string, []byte, error) {
	for {
		message, err := r.decoder.Decode(r.src, r.payloadBuf)
		if err != nil {
			if err == io.EOF {
				return "", nil, io.EOF
			}
			// Transport-level errors (stale/closed connection, unexpected EOF,
			// checksum) are retryable — emit a non-Bifrost network error so the
			// retry gate in executeRequestWithRetries can retry transparently.
			if isStreamTransportError(err) {
				return "", nil, &anthropic.StreamReaderError{BifrostError: &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Message: schemas.ErrProviderNetworkError,
						Error:   err,
					},
				}}
			}
			return "", nil, err
		}

		if len(message.Payload) == 0 {
			continue
		}

		// Non-"event" message types carry AWS exception details in their headers.
		if msgTypeHeader := message.Headers.Get(":message-type"); msgTypeHeader != nil {
			if msgType := msgTypeHeader.String(); msgType != "event" {
				excType := msgType
				if excHeader := message.Headers.Get(":exception-type"); excHeader != nil {
					if v := excHeader.String(); v != "" {
						excType = v
					}
				}
				errMsg := string(message.Payload)
				var bedrockErr BedrockError
				if e := sonic.Unmarshal(message.Payload, &bedrockErr); e == nil && bedrockErr.Message != "" {
					errMsg = bedrockErr.Message
				}
				// Retryable AWS exceptions must not set IsBifrostError:true — that would
				// bypass the retry gate. Emit IsBifrostError:false with the equivalent
				// HTTP status code so transientServerStatusCodes / perKeyFailureStatusCodes
				// drive the retry.
				if statusCode, ok := retryableBedrockExceptions[excType]; ok {
					return "", nil, &anthropic.StreamReaderError{BifrostError: &schemas.BifrostError{
						IsBifrostError: false,
						StatusCode:     &statusCode,
						Error: &schemas.ErrorField{
							Message: fmt.Sprintf("%s stream %s: %s", r.providerName, excType, errMsg),
						},
					}}
				}
				// Non-retryable exceptions (e.g. validationException, accessDeniedException)
				// are terminal — return a plain error so the shared loop forwards them via
				// ProcessAndSendError (IsBifrostError:true) and they are NOT retried.
				return "", nil, fmt.Errorf("%s stream %s: %s", r.providerName, excType, errMsg)
			}
		}

		// Normal event: payload wraps a native Anthropic SSE chunk in its bytes field.
		var chunkPayload struct {
			Bytes []byte `json:"bytes"`
		}
		if e := sonic.Unmarshal(message.Payload, &chunkPayload); e != nil {
			return "", nil, e
		}

		// Derive the SSE-style event type from the embedded chunk's "type" field,
		// mirroring the native Anthropic `event:` line that the shared loop expects.
		var typed struct {
			Type string `json:"type"`
		}
		_ = sonic.Unmarshal(chunkPayload.Bytes, &typed)
		return typed.Type, chunkPayload.Bytes, nil
	}
}
