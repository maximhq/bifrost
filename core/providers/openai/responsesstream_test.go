package openai

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestHandleOpenAIResponsesStreaming_CustomHandlerSendsCompletedChunkWithRawFields(t *testing.T) {
	ln := fasthttputil.NewInmemoryListener()
	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.Response.Header.SetContentType("text/event-stream")
			ctx.SetBodyString("data: {\"provider\":\"chunk\"}\n\n")
		},
	}
	go func() { _ = server.Serve(ln) }()
	defer ln.Close()

	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return ln.Dial()
		},
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})

	customHandlerCalled := false
	customHandler := func(responseBody []byte, response *schemas.BifrostResponsesStreamResponse, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (interface{}, interface{}, *schemas.BifrostError) {
		customHandlerCalled = true
		if !sendBackRawRequest {
			t.Fatal("expected custom handler to receive sendBackRawRequest=true")
		}
		if !sendBackRawResponse {
			t.Fatal("expected custom handler to receive sendBackRawResponse=true")
		}
		if len(requestBody) == 0 {
			t.Fatal("expected serialized request body")
		}
		if string(responseBody) != `{"provider":"chunk"}` {
			t.Fatalf("unexpected SSE payload: %s", responseBody)
		}

		response.Type = schemas.ResponsesStreamResponseTypeCompleted
		response.SequenceNumber = 7
		return map[string]interface{}{"request": "raw"}, map[string]interface{}{"response": "raw"}, nil
	}

	stream, bifrostErr := HandleOpenAIResponsesStreaming(
		ctx,
		client,
		"http://test/v1/responses",
		&schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-test"},
		nil,
		nil,
		1,
		true,
		true,
		schemas.OpenAI,
		func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return result, err
		},
		customHandler,
		nil,
		nil,
		nil,
		nil,
		noopOpenAITestLogger{},
		nil,
	)
	if bifrostErr != nil {
		t.Fatalf("unexpected setup error: %v", bifrostErr.Error.Message)
	}

	chunk, ok := <-stream
	if !ok {
		t.Fatal("expected completed stream chunk")
	}
	if !customHandlerCalled {
		t.Fatal("expected custom response handler to be called")
	}
	if chunk.BifrostError != nil {
		t.Fatalf("unexpected stream error: %v", chunk.BifrostError.Error.Message)
	}
	if chunk.BifrostResponsesStreamResponse == nil {
		t.Fatal("expected responses stream response")
	}
	response := chunk.BifrostResponsesStreamResponse
	if response.Type != schemas.ResponsesStreamResponseTypeCompleted {
		t.Fatalf("expected completed chunk, got %q", response.Type)
	}
	if response.ExtraFields.ChunkIndex != 7 {
		t.Fatalf("expected chunk index 7, got %d", response.ExtraFields.ChunkIndex)
	}
	if got := response.ExtraFields.RawRequest.(map[string]interface{})["request"]; got != "raw" {
		t.Fatalf("expected custom raw request, got %#v", response.ExtraFields.RawRequest)
	}
	if got := response.ExtraFields.RawResponse.(map[string]interface{})["response"]; got != "raw" {
		t.Fatalf("expected custom raw response, got %#v", response.ExtraFields.RawResponse)
	}
	if _, ok := <-stream; ok {
		t.Fatal("expected stream to close after completed chunk")
	}
}

func TestHandleOpenAIResponsesStreaming_CustomHandlerPreservesRawFieldsOnErrorEvent(t *testing.T) {
	ln := fasthttputil.NewInmemoryListener()
	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.Response.Header.SetContentType("text/event-stream")
			ctx.SetBodyString("data: {\"provider\":\"error\"}\n\n")
		},
	}
	go func() { _ = server.Serve(ln) }()
	defer ln.Close()

	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return ln.Dial()
		},
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})

	customHandler := func(responseBody []byte, response *schemas.BifrostResponsesStreamResponse, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (interface{}, interface{}, *schemas.BifrostError) {
		if !sendBackRawRequest {
			t.Fatal("expected custom handler to receive sendBackRawRequest=true")
		}
		if !sendBackRawResponse {
			t.Fatal("expected custom handler to receive sendBackRawResponse=true")
		}
		if len(requestBody) == 0 {
			t.Fatal("expected serialized request body")
		}
		if string(responseBody) != `{"provider":"error"}` {
			t.Fatalf("unexpected SSE payload: %s", responseBody)
		}

		response.Type = schemas.ResponsesStreamResponseTypeError
		response.Message = schemas.Ptr("custom stream error")
		response.Code = schemas.Ptr("custom_error")
		return map[string]interface{}{"request": "raw"}, map[string]interface{}{"response": "raw"}, nil
	}

	stream, bifrostErr := HandleOpenAIResponsesStreaming(
		ctx,
		client,
		"http://test/v1/responses",
		&schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-test"},
		nil,
		nil,
		1,
		true,
		true,
		schemas.OpenAI,
		func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return result, err
		},
		customHandler,
		nil,
		nil,
		nil,
		nil,
		noopOpenAITestLogger{},
		nil,
	)
	if bifrostErr != nil {
		t.Fatalf("unexpected setup error: %v", bifrostErr.Error.Message)
	}

	chunk, ok := <-stream
	if !ok {
		t.Fatal("expected error stream chunk")
	}
	if chunk.BifrostError == nil {
		t.Fatal("expected bifrost error")
	}
	if chunk.BifrostError.Error.Message != "custom stream error" {
		t.Fatalf("expected custom stream error, got %q", chunk.BifrostError.Error.Message)
	}
	if chunk.BifrostError.Error.Code == nil || *chunk.BifrostError.Error.Code != "custom_error" {
		t.Fatalf("expected custom_error code, got %#v", chunk.BifrostError.Error.Code)
	}
	if got := chunk.BifrostError.ExtraFields.RawRequest.(map[string]interface{})["request"]; got != "raw" {
		t.Fatalf("expected custom raw request, got %#v", chunk.BifrostError.ExtraFields.RawRequest)
	}
	if got := chunk.BifrostError.ExtraFields.RawResponse.(map[string]interface{})["response"]; got != "raw" {
		t.Fatalf("expected custom raw response, got %#v", chunk.BifrostError.ExtraFields.RawResponse)
	}
	if _, ok := <-stream; ok {
		t.Fatal("expected stream to close after error chunk")
	}
}

type noopOpenAITestLogger struct{}

func (noopOpenAITestLogger) Debug(string, ...any)                   {}
func (noopOpenAITestLogger) Info(string, ...any)                    {}
func (noopOpenAITestLogger) Warn(string, ...any)                    {}
func (noopOpenAITestLogger) Error(string, ...any)                   {}
func (noopOpenAITestLogger) Fatal(string, ...any)                   {}
func (noopOpenAITestLogger) SetLevel(schemas.LogLevel)              {}
func (noopOpenAITestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopOpenAITestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}
