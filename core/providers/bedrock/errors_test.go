package bedrock

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecuteBedrockRequest_SurfacesExceptionType is an end-to-end guard for the
// non-streaming executor path (Converse / chat_completion). It must route upstream
// HTTP errors through parseBedrockHTTPError so the AWS exception type — delivered
// here only via the X-Amzn-Errortype header with a ":<url>" qualifier, as real EOL
// models return — is surfaced on both the top-level type and the nested error.type.
// Regression for a bespoke inline error path that dropped the type entirely.
func TestExecuteBedrockRequest_SurfacesExceptionType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amzn-Errortype", "ResourceNotFoundException:http://internal.amazon.com/coral/com.amazon.bedrock/")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"This model version has reached the end of its life. Please refer to the AWS documentation for more details."}`))
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)

	provider := &BedrockProvider{client: server.Client()}
	_, _, _, bifrostErr := provider.executeBedrockRequest(req)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode)
	assert.False(t, bifrostErr.IsBifrostError, "non-streaming path delegates retryability to the retry gate via status code")
	require.NotNil(t, bifrostErr.Type, "top-level type must be recovered from the header")
	assert.Equal(t, "ResourceNotFoundException", *bifrostErr.Type)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Type, "nested error.type must be populated for OpenAI-shaped consumers")
	assert.Equal(t, "ResourceNotFoundException", *bifrostErr.Error.Type)
	assert.Contains(t, bifrostErr.Error.Message, "reached the end of its life")
}

// TestParseBedrockHTTPError_PreservesExceptionType verifies that the upstream
// AWS exception type (the JSON "__type" field) is preserved on the resulting
// BifrostError. Without this, a retired/unsupported model error surfaces as a
// generic "InternalServerError" once it is converted back for the streaming
// EventStream path.
func TestParseBedrockHTTPError_PreservesExceptionType(t *testing.T) {
	// Shape AWS Bedrock returns for an EOL/unsupported model.
	body := []byte(`{"__type":"ValidationException","message":"Invocation of model ID anthropic.claude-v2 with on-demand throughput isn't supported."}`)

	bifrostErr := parseBedrockHTTPError(http.StatusBadRequest, http.Header{}, body)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Type, "exception type must be preserved from __type")
	assert.Equal(t, "ValidationException", *bifrostErr.Type)
	require.NotNil(t, bifrostErr.Error)
	assert.Equal(t, "Invocation of model ID anthropic.claude-v2 with on-demand throughput isn't supported.", bifrostErr.Error.Message)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
}

// TestParseBedrockHTTPError_TypeFromHeader covers the real end-of-life model
// case: AWS reports the exception type only in the X-Amzn-Errortype response
// header while the body carries just {"message": ...}. The type must still be
// recovered from the header.
func TestParseBedrockHTTPError_TypeFromHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Amzn-Errortype", "ValidationException")
	body := []byte(`{"message":"This model version has reached the end of its life. Please refer to the AWS documentation for more details."}`)

	bifrostErr := parseBedrockHTTPError(http.StatusBadRequest, headers, body)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Type, "type must be recovered from X-Amzn-Errortype header")
	assert.Equal(t, "ValidationException", *bifrostErr.Type)
	require.NotNil(t, bifrostErr.Error)
	assert.Contains(t, bifrostErr.Error.Message, "reached the end of its life")
}

// TestParseBedrockHTTPError_PopulatesNestedErrorType verifies the AWS exception
// type is surfaced on the nested error object (error.type), not only at the
// top level. OpenAI-shaped consumers read error.type/error.code, so a
// Bedrock passthrough error must carry the type there — matching every other
// provider. This is the customer-reported gap: "only error.message, no type".
func TestParseBedrockHTTPError_PopulatesNestedErrorType(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Amzn-Errortype", "ValidationException")
	body := []byte(`{"message":"Invocation of model ID anthropic.claude-v2 with on-demand throughput isn't supported."}`)

	bifrostErr := parseBedrockHTTPError(http.StatusBadRequest, headers, body)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, "ValidationException", *bifrostErr.Type, "top-level type must still be set")
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Type, "nested error.type must be populated for OpenAI-shaped consumers")
	assert.Equal(t, "ValidationException", *bifrostErr.Error.Type)
}

// TestParseBedrockHTTPError_NestedTypeFromBodyType verifies the nested
// error.type is also populated when the type comes from the body "__type"
// rather than the header.
func TestParseBedrockHTTPError_NestedTypeFromBodyType(t *testing.T) {
	body := []byte(`{"__type":"ThrottlingException","message":"rate exceeded"}`)

	bifrostErr := parseBedrockHTTPError(http.StatusTooManyRequests, http.Header{}, body)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Type)
	assert.Equal(t, "ThrottlingException", *bifrostErr.Error.Type)
}

// TestParseBedrockHTTPError_HeaderTypeQualifierStripped ensures the trailing
// ":<url>" / "#<shape>" qualifier AWS sometimes appends to X-Amzn-Errortype is
// removed.
func TestParseBedrockHTTPError_HeaderTypeQualifierStripped(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Amzn-Errortype", "ValidationException:http://internal.amazon.com/coral/com.amazon.bedrock/")
	body := []byte(`{"message":"bad model"}`)

	bifrostErr := parseBedrockHTTPError(http.StatusBadRequest, headers, body)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, "ValidationException", *bifrostErr.Type)
}

// TestParseBedrockHTTPError_BodyTypePreferredOverHeader ensures the body's
// "__type" wins when both the body and header carry a type.
func TestParseBedrockHTTPError_BodyTypePreferredOverHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Amzn-Errortype", "SomethingElse")
	body := []byte(`{"__type":"ValidationException","message":"bad model"}`)

	bifrostErr := parseBedrockHTTPError(http.StatusBadRequest, headers, body)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, "ValidationException", *bifrostErr.Type)
}

// TestParseBedrockHTTPError_RoundTripToBedrockError is the regression test for
// the actual bug: the exception type must survive the parse -> ToBedrockError
// round trip used by the streaming error converter, rather than falling back to
// "InternalServerError". Modeled on the EOL case where the type is header-only.
func TestParseBedrockHTTPError_RoundTripToBedrockError(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Amzn-Errortype", "ValidationException")
	body := []byte(`{"message":"This model version has reached the end of its life. Please refer to the AWS documentation for more details."}`)

	bifrostErr := parseBedrockHTTPError(http.StatusBadRequest, headers, body)
	bedrockErr := ToBedrockError(bifrostErr)

	require.NotNil(t, bedrockErr)
	assert.Equal(t, "ValidationException", bedrockErr.Type, "must forward AWS exception type, not fall back to InternalServerError")
	assert.Contains(t, bedrockErr.Message, "reached the end of its life")
}

// TestNewBedrockStreamException_TypeFromBody verifies an in-stream exception
// event whose payload carries a "__type" forwards that type and surfaces the
// clean message.
func TestNewBedrockStreamException_TypeFromBody(t *testing.T) {
	payload := []byte(`{"__type":"ValidationException","message":"This model version has reached the end of its life."}`)

	streamErr := newBedrockStreamException("bedrock", "validationException", payload)

	require.NotNil(t, streamErr)
	require.NotNil(t, streamErr.Type)
	assert.Equal(t, "ValidationException", *streamErr.Type, "payload __type is preferred over the header excType")
	assert.True(t, streamErr.IsBifrostError, "non-retryable exceptions are terminal")
	assert.Nil(t, streamErr.StatusCode)
	require.NotNil(t, streamErr.Error)
	assert.Contains(t, streamErr.Error.Message, "reached the end of its life")

	// Must survive conversion to the streaming EventStream exception payload.
	bedrockErr := ToBedrockError(streamErr)
	assert.Equal(t, "ValidationException", bedrockErr.Type)
}

// TestNewBedrockStreamException_TypeFromHeader verifies the type falls back to
// the :exception-type header value (excType) when the payload has no "__type".
func TestNewBedrockStreamException_TypeFromHeader(t *testing.T) {
	payload := []byte(`{"message":"bad input"}`)

	streamErr := newBedrockStreamException("", "validationException", payload)

	require.NotNil(t, streamErr)
	require.NotNil(t, streamErr.Type)
	assert.Equal(t, "validationException", *streamErr.Type)

	bedrockErr := ToBedrockError(streamErr)
	assert.Equal(t, "validationException", bedrockErr.Type, "must not fall back to InternalServerError")
}

// TestNewBedrockStreamException_Retryable verifies each retryable AWS in-stream
// exception is marked with IsBifrostError:false and a gate-recognized transient
// status so the retry fires, while still forwarding the original type. Covers the
// two exceptions whose native status (424 / 408) the retry gate does not
// recognize and which must be mapped to a transient code.
func TestNewBedrockStreamException_Retryable(t *testing.T) {
	// status codes the retry gate honors (transientServerStatusCodes ∪ perKeyFailureStatusCodes).
	gateRetryable := map[int]bool{500: true, 502: true, 503: true, 504: true, 429: true}

	cases := []struct {
		excType string
		status  int
	}{
		{"internalServerException", 500},
		{"throttlingException", 429},
		{"serviceUnavailableException", 503},
		{"modelNotReadyException", 503},
		{"modelStreamErrorException", 503}, // native 424 -> mapped
		{"modelTimeoutException", 504},     // native 408 -> mapped
	}

	for _, c := range cases {
		t.Run(c.excType, func(t *testing.T) {
			streamErr := newBedrockStreamException("", c.excType, []byte(`{"message":"transient"}`))

			require.NotNil(t, streamErr)
			assert.False(t, streamErr.IsBifrostError, "retryable exceptions must keep IsBifrostError:false for the retry gate")
			require.NotNil(t, streamErr.StatusCode)
			assert.Equal(t, c.status, *streamErr.StatusCode)
			assert.True(t, gateRetryable[*streamErr.StatusCode], "mapped status must be one the retry gate honors")
			require.NotNil(t, streamErr.Type)
			assert.Equal(t, c.excType, *streamErr.Type, "original exception type must still be forwarded")
		})
	}
}

// TestNewBedrockStreamException_Terminal verifies non-retryable AWS in-stream
// exceptions are terminal (IsBifrostError:true, no retry status) yet still
// forward the type so the client sees the real exception rather than
// InternalServerError.
func TestNewBedrockStreamException_Terminal(t *testing.T) {
	for _, excType := range []string{"validationException", "accessDeniedException", "resourceNotFoundException"} {
		t.Run(excType, func(t *testing.T) {
			streamErr := newBedrockStreamException("", excType, []byte(`{"message":"bad request"}`))

			require.NotNil(t, streamErr)
			assert.True(t, streamErr.IsBifrostError, "non-retryable exceptions must be terminal")
			assert.Nil(t, streamErr.StatusCode)
			require.NotNil(t, streamErr.Type)
			assert.Equal(t, excType, *streamErr.Type)

			assert.Equal(t, excType, ToBedrockError(streamErr).Type)
		})
	}
}

// TestParseBedrockHTTPError_NoTypeFallsBack ensures that when AWS provides no
// "__type" we don't fabricate one; ToBedrockError still applies its
// InternalServerError fallback for a typeless error.
func TestParseBedrockHTTPError_NoTypeFallsBack(t *testing.T) {
	body := []byte(`{"message":"something went wrong"}`)

	bifrostErr := parseBedrockHTTPError(http.StatusInternalServerError, http.Header{}, body)
	require.NotNil(t, bifrostErr)
	assert.Nil(t, bifrostErr.Type, "no __type present, so none should be set")

	bedrockErr := ToBedrockError(bifrostErr)
	require.NotNil(t, bedrockErr)
	assert.Equal(t, "InternalServerError", bedrockErr.Type)
	assert.Equal(t, "something went wrong", bedrockErr.Message)
}
