package bedrock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

var _ schemas.RealtimeProvider = (*BedrockProvider)(nil)
var _ schemas.RealtimeConnector = (*BedrockProvider)(nil)
var _ schemas.RealtimeUsageExtractor = (*BedrockProvider)(nil)

const (
	// realtimeTextMessage is the WebSocket text frame type carried through RealtimeUpstream.
	realtimeTextMessage = 1
	// sonicStreamingPayloadHash marks a SigV4 request whose body is a signed event stream.
	sonicStreamingPayloadHash = "STREAMING-AWS4-HMAC-SHA256-EVENTS"
	sonicBidirectionalAction  = "invoke-with-bidirectional-stream"
	sonicOutboxSize           = 256
	sonicCloseGrace           = 2 * time.Second
	sonicErrorBodyLimit       = 64 << 10
)

// SupportsRealtimeAPI advertises Nova Sonic speech-to-speech over the Realtime surface.
func (provider *BedrockProvider) SupportsRealtimeAPI() bool { return true }

// RealtimeWebSocketURL is unused: Bedrock realtime is not a WebSocket, see ConnectRealtime.
func (provider *BedrockProvider) RealtimeWebSocketURL(_ schemas.Key, _, _ string) (string, *schemas.BifrostError) {
	return "", bedrockRealtimeError(400, "bedrock realtime runs over a bidirectional event stream, not a websocket url", nil)
}

// RealtimeHeaders is unused: Bedrock realtime signs its own stream, see ConnectRealtime.
func (provider *BedrockProvider) RealtimeHeaders(_ *schemas.BifrostContext, _ schemas.Key) (map[string]string, *schemas.BifrostError) {
	return nil, bedrockRealtimeError(400, "bedrock realtime runs over a bidirectional event stream, not websocket headers", nil)
}

// SupportsRealtimeWebRTC is false; Nova Sonic has no SDP endpoint.
func (provider *BedrockProvider) SupportsRealtimeWebRTC() bool { return false }

// ExchangeRealtimeWebRTCSDP reports the unsupported transport.
func (provider *BedrockProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", bedrockRealtimeError(400, "bedrock realtime does not support WebRTC SDP exchange", nil)
}

// ToBifrostRealtimeEvent decodes the OpenAI-shaped events the connection emits.
func (provider *BedrockProvider) ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	return realtimeCodec.ToBifrostRealtimeEvent(providerEvent)
}

// ToProviderRealtimeEvent encodes client events as OpenAI JSON for the connection to translate.
func (provider *BedrockProvider) ToProviderRealtimeEvent(bifrostEvent *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	return realtimeCodec.ToProviderRealtimeEvent(bifrostEvent)
}

// ShouldStartRealtimeTurn starts turns on Nova's own signals; there is no client-side trigger.
func (provider *BedrockProvider) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	return event != nil && (event.Type == schemas.RTEventInputAudioBufferCommitted || event.Type == schemas.RTEventResponseCreated)
}

// RealtimeTurnFinalEvent is the synthesized response.done that closes each assistant response.
func (provider *BedrockProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventResponseDone
}

func (provider *BedrockProvider) RealtimeWebRTCDataChannelLabel() string { return "" }

// RealtimeWebSocketSubprotocol matches OpenAI so browser clients negotiate identically.
func (provider *BedrockProvider) RealtimeWebSocketSubprotocol() string { return "realtime" }

func (provider *BedrockProvider) ShouldForwardRealtimeEvent(_ *schemas.BifrostRealtimeEvent) bool {
	return true
}

func (provider *BedrockProvider) ShouldAccumulateRealtimeOutput(eventType schemas.RealtimeEventType) bool {
	return realtimeCodec.ShouldAccumulateRealtimeOutput(eventType)
}

// ExtractRealtimeTurnUsage reads the usage the connection attaches to response.done.
func (provider *BedrockProvider) ExtractRealtimeTurnUsage(terminalEventRaw []byte) *schemas.BifrostLLMUsage {
	return realtimeCodec.ExtractRealtimeTurnUsage(terminalEventRaw)
}

// ExtractRealtimeTurnOutput reads the transcript and tool calls on response.done.
func (provider *BedrockProvider) ExtractRealtimeTurnOutput(terminalEventRaw []byte) *schemas.ChatMessage {
	return realtimeCodec.ExtractRealtimeTurnOutput(terminalEventRaw)
}

// ConnectRealtime checks credentials now; the HTTP/2 stream opens on the first event that needs the model.
func (provider *BedrockProvider) ConnectRealtime(ctx *schemas.BifrostContext, key schemas.Key, model, intent string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.RealtimeRequest); err != nil {
		return nil, err
	}
	if intent != "" {
		// Nova Sonic always generates a spoken reply; a transcription-only session would
		// silently bill for responses the client never sees.
		return nil, bedrockRealtimeError(400, "bedrock realtime does not support intent="+intent+"; user transcripts are streamed as conversation.item.input_audio_transcription events in a normal session", nil)
	}
	if key.Value.GetValue() != "" {
		return nil, bedrockRealtimeError(400, "bedrock realtime requires IAM credentials; Bedrock API keys cannot sign InvokeModelWithBidirectionalStream", nil)
	}
	if provider.realtimeClient == nil {
		return nil, bedrockRealtimeError(500, "bedrock realtime client is not configured", nil)
	}
	path, region := provider.getModelPathAndRegion(ctx, sonicBidirectionalAction, model, key)
	credentials := func() (aws.Credentials, *schemas.BifrostError) {
		return resolveAWSCredentials(ctx, key.BedrockKeyConfig, region)
	}
	if _, credErr := credentials(); credErr != nil {
		return nil, credErr
	}
	host := resolveBedrockHost(bedrockEndpoints(key.BedrockKeyConfig), bedrockServiceRuntime, region)
	return newBedrockRealtimeConn(ctx, provider, fmt.Sprintf("https://%s/model/%s", host, path), region, credentials, model), nil
}

// newBedrockRealtimeConn prepares a connection and queues session.created for the client.
func newBedrockRealtimeConn(ctx context.Context, provider *BedrockProvider, url, region string, credentials func() (aws.Credentials, *schemas.BifrostError), model string) *bedrockRealtimeConn {
	reqCtx, cancel := context.WithCancel(ctx)
	conn := &bedrockRealtimeConn{
		provider:    provider,
		reqCtx:      reqCtx,
		cancel:      cancel,
		url:         url,
		region:      region,
		credentials: credentials,
		session:     newSonicSession(model),
		encoder:     eventstream.NewEncoder(),
		outbox:      make(chan []byte, sonicOutboxSize),
		closed:      make(chan struct{}),
		ended:       make(chan struct{}),
	}
	conn.push(conn.session.sessionCreated())
	return conn
}

// bedrockRealtimeConn adapts a Nova Sonic bidirectional stream to RealtimeUpstream frames.
type bedrockRealtimeConn struct {
	provider    *BedrockProvider
	reqCtx      context.Context
	cancel      context.CancelFunc
	url         string
	region      string
	credentials func() (aws.Credentials, *schemas.BifrostError) // resolved again when the stream opens, so expiring credentials refresh
	session     *sonicSession

	writeMu sync.Mutex
	encoder *eventstream.Encoder
	signer  *v4.StreamSigner
	body    *io.PipeWriter
	frame   bytes.Buffer
	signed  bytes.Buffer

	outbox    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	ended     chan struct{} // closed when the upstream response is fully read
	failed    atomic.Bool   // an upstream failure was already reported to the client
	started   bool
}

// ReadMessage returns the next OpenAI-shaped server event for the client.
func (c *bedrockRealtimeConn) ReadMessage() (int, []byte, error) {
	select {
	case msg := <-c.outbox:
		return realtimeTextMessage, msg, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case <-c.ended:
		select {
		case msg := <-c.outbox:
			return realtimeTextMessage, msg, nil
		default:
			return 0, nil, io.EOF
		}
	}
}

// WriteMessage translates one client event and sends the resulting Nova Sonic events upstream.
func (c *bedrockRealtimeConn) WriteMessage(_ int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	upstream, replies := c.session.clientEvent(data)
	for _, reply := range replies {
		c.push(reply)
	}
	if len(upstream) == 0 {
		return nil
	}
	if !c.started {
		if bifrostErr := c.openStreamLocked(); bifrostErr != nil {
			// Ending the read side lets the handler close the session after the error.
			c.reportFailure(bifrostErr)
			close(c.ended)
			return nil
		}
	}
	if c.body == nil {
		return nil
	}
	for _, event := range upstream {
		if err := c.sendLocked(event); err != nil {
			// The body can close before the reader records why; prefer its in-band error.
			select {
			case <-c.ended:
			case <-time.After(sonicCloseGrace):
			}
			if c.failed.Load() {
				return nil
			}
			return err
		}
	}
	return nil
}

// Close ends the Nova Sonic session gracefully when possible, then tears the stream down.
func (c *bedrockRealtimeConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		graceful := make(chan struct{})
		go func() {
			defer close(graceful)
			c.writeMu.Lock()
			defer c.writeMu.Unlock()
			if c.body == nil {
				return
			}
			for _, event := range c.session.closeEvents() {
				if c.sendLocked(event) != nil {
					break
				}
			}
			// A signed empty frame tells Bedrock no more events follow.
			if frame, err := c.signLocked(nil); err == nil {
				_, _ = c.body.Write(frame)
			}
			_ = c.body.Close()
		}()
		timer := time.NewTimer(sonicCloseGrace)
		defer timer.Stop()
		select {
		case <-graceful:
			if c.started {
				select {
				case <-c.ended:
				case <-timer.C:
				}
			}
		case <-timer.C:
		}
		c.cancel()
	})
	return nil
}

// openStreamLocked signs and starts the HTTP/2 request whose body carries the input events.
func (c *bedrockRealtimeConn) openStreamLocked() *schemas.BifrostError {
	c.started = true
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(c.reqCtx, http.MethodPost, c.url, pr)
	if err != nil {
		return providerUtils.NewBifrostOperationError("error creating request", err)
	}
	req.ContentLength = -1
	providerUtils.SetExtraHeadersHTTP(c.reqCtx, req, c.provider.networkConfig.ExtraHeaders, nil)
	req.Header.Set("Content-Type", "application/vnd.amazon.eventstream")
	req.Header.Set("X-Amz-Content-Sha256", sonicStreamingPayloadHash)
	creds, credErr := c.credentials()
	if credErr != nil {
		return credErr
	}
	if signErr := signWithCredentials(c.reqCtx, req, creds, sonicStreamingPayloadHash, bedrockSigningService, c.region); signErr != nil {
		return signErr
	}
	seed, err := v4.GetSignedRequestSignature(req)
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to read request signature", err)
	}
	c.signer = v4.NewStreamSigner(creds, bedrockSigningService, c.region, seed)
	c.body = pw
	go c.run(req, pr)
	return nil
}

// sendLocked frames one Nova Sonic JSON event as a signed event-stream chunk.
func (c *bedrockRealtimeConn) sendLocked(event []byte) error {
	c.frame.Reset()
	payload := make([]byte, 0, base64.StdEncoding.EncodedLen(len(event))+12)
	payload = append(payload, `{"bytes":"`...)
	payload = base64.StdEncoding.AppendEncode(payload, event)
	payload = append(payload, `"}`...)
	var msg eventstream.Message
	msg.Headers.Set(":event-type", eventstream.StringValue("chunk"))
	msg.Headers.Set(":message-type", eventstream.StringValue("event"))
	msg.Headers.Set(":content-type", eventstream.StringValue("application/json"))
	msg.Payload = payload
	if err := c.encoder.Encode(&c.frame, msg); err != nil {
		return err
	}
	signed, err := c.signLocked(c.frame.Bytes())
	if err != nil {
		return err
	}
	_, err = c.body.Write(signed)
	return err
}

// signLocked wraps payload in the chained-signature envelope Bedrock verifies per event.
func (c *bedrockRealtimeConn) signLocked(payload []byte) ([]byte, error) {
	c.signed.Reset()
	date := time.Now().UTC()
	var msg eventstream.Message
	msg.Headers.Set(":date", eventstream.TimestampValue(date))
	msg.Payload = payload
	var headers bytes.Buffer
	if err := eventstream.EncodeHeaders(&headers, msg.Headers); err != nil {
		return nil, err
	}
	sig, err := c.signer.GetSignature(context.Background(), headers.Bytes(), payload, date)
	if err != nil {
		return nil, err
	}
	msg.Headers.Set(":chunk-signature", eventstream.BytesValue(sig))
	if err := c.encoder.Encode(&c.signed, msg); err != nil {
		return nil, err
	}
	return c.signed.Bytes(), nil
}

// run performs the HTTP/2 exchange and decodes model events until the stream ends.
func (c *bedrockRealtimeConn) run(req *http.Request, body *io.PipeReader) {
	defer close(c.ended)
	resp, err := c.provider.realtimeClient.Do(req)
	if err != nil {
		if c.reqCtx.Err() == nil {
			c.reportFailure(&schemas.BifrostError{Error: &schemas.ErrorField{Message: schemas.ErrProviderNetworkError, Error: err}})
		}
		_ = body.CloseWithError(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, sonicErrorBodyLimit))
		c.reportFailure(parseBedrockHTTPError(resp.StatusCode, resp.Header, raw))
		_ = body.CloseWithError(errors.New("bedrock realtime request rejected"))
		return
	}

	decoder := eventstream.NewDecoder()
	payloadBuf := make([]byte, 0, 64<<10)
	for {
		message, err := decoder.Decode(resp.Body, payloadBuf)
		if err != nil {
			if !errors.Is(err, io.EOF) && c.reqCtx.Err() == nil {
				c.reportFailure(&schemas.BifrostError{Error: &schemas.ErrorField{Message: schemas.ErrProviderNetworkError, Error: err}})
			}
			_ = body.CloseWithError(io.ErrClosedPipe)
			return
		}
		switch headerString(message, ":message-type") {
		case "event":
			var chunk struct {
				Bytes []byte `json:"bytes"`
			}
			if err := sonic.Unmarshal(message.Payload, &chunk); err != nil {
				c.reportFailure(bedrockRealtimeError(502, "failed to decode Nova Sonic event chunk", err))
				_ = body.CloseWithError(err)
				return
			}
			for _, out := range c.session.modelEvent(chunk.Bytes) {
				c.push(out)
			}
		case "exception":
			c.reportFailure(newBedrockStreamException("nova sonic", headerString(message, ":exception-type"), message.Payload))
			_ = body.CloseWithError(errors.New("bedrock realtime stream exception"))
			return
		case "error":
			c.reportFailure(bedrockRealtimeError(502, fmt.Sprintf("nova sonic stream %s: %s", headerString(message, ":error-code"), headerString(message, ":error-message")), nil))
			_ = body.CloseWithError(errors.New("bedrock realtime stream error"))
			return
		}
	}
}

// reportFailure sends an upstream failure to the client as an OpenAI Realtime error event.
func (c *bedrockRealtimeConn) reportFailure(bifrostErr *schemas.BifrostError) {
	if bifrostErr == nil || !c.failed.CompareAndSwap(false, true) {
		return
	}
	errorType, code, message := "server_error", "upstream_error", "bedrock realtime stream failed"
	if bifrostErr.Error != nil {
		if bifrostErr.Error.Message != "" {
			message = bifrostErr.Error.Message
		}
		if bifrostErr.Error.Code != nil && *bifrostErr.Error.Code != "" {
			code = *bifrostErr.Error.Code
		}
	}
	if bifrostErr.Type != nil && *bifrostErr.Type != "" {
		code = *bifrostErr.Type
	}
	if bifrostErr.StatusCode != nil {
		switch status := *bifrostErr.StatusCode; {
		case status == http.StatusTooManyRequests:
			errorType = "rate_limit_exceeded"
		case status >= 400 && status < 500:
			errorType = "invalid_request_error"
		}
	}
	c.push(c.session.errorReply(errorType, code, message)[0])
}

// push queues a client event unless the connection has been closed.
func (c *bedrockRealtimeConn) push(msg []byte) {
	select {
	case c.outbox <- msg:
	case <-c.closed:
	}
}

func headerString(message eventstream.Message, name string) string {
	if v := message.Headers.Get(name); v != nil {
		return v.String()
	}
	return ""
}

// bedrockRealtimeError attaches provider identity to a realtime setup failure.
func bedrockRealtimeError(status int, message string, err error) *schemas.BifrostError {
	result := providerUtils.NewBifrostOperationError(message, err)
	result.StatusCode = schemas.Ptr(status)
	result.ExtraFields.Provider = schemas.Bedrock
	result.ExtraFields.RequestType = schemas.RealtimeRequest
	if status >= 400 && status < 500 {
		result.Error.Type = schemas.Ptr("invalid_request_error")
	}
	return result
}
