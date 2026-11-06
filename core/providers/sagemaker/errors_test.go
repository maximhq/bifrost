package sagemaker

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Bodies captured from live /openai/v1 endpoints on 2026-10-24 (account id replaced).
const (
	vllmModelErrorBody = `{"error":{"message":"Received client error (400) from primary with message \"{\"error\":{\"message\":\"max_tokens=100000 cannot be greater than max_model_len=32768. Please request fewer output tokens. (parameter=max_tokens, value=100000)\",\"type\":\"BadRequestError\",\"param\":\"max_tokens\",\"code\":400}}\". See https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#logEventViewer:group=/aws/sagemaker/Endpoints/bifrost-vllm-qwen3-4b-d8590b in account 123456789012 for more information.","type":"server_error","code":"model_error"}}`

	sglangModelErrorBody = `{"error":{"message":"Received client error (400) from primary with message \"{\"object\":\"error\",\"message\":\"max_completion_tokens is too large: 1000000.This model supports at most 32768 completion tokens.\",\"type\":\"BadRequestError\",\"param\":null,\"code\":400}\". See https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#logEventViewer:group=/aws/sagemaker/Endpoints/bifrost-sglang-qwen3-4b-9556ce in account 123456789012 for more information.","type":"server_error","code":"model_error"}}`

	lmiAsyncFailureBody = `{"error":{"message":"Received client error (424) from primary with message \"{\"code\":424,\"message\":\"Async Inference Failure service.invoke_handler_async() failure\",\"properties\":{},\"content\":{\"keys\":[],\"values\":[]},\"cancelled\":false}\". See https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#logEventViewer:group=/aws/sagemaker/Endpoints/bifrost-lmi-qwen3-4b-a5f221 in account 123456789012 for more information.","type":"server_error","code":"model_error"}}`

	lmiErrorInfoBody = `{"error":{"message":"Received server error (500) from primary with message \"{\"error\":\"error\\u003dErrorInfo(message\\u003d\\u0027The model ` + "`does-not-exist`" + ` does not exist.\\u0027, type\\u003d\\u0027NotFoundError\\u0027, param\\u003d\\u0027model\\u0027, code\\u003d404)\",\"code\":500}\". See https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#logEventViewer:group=/aws/sagemaker/Endpoints/bifrost-lmi-qwen3-4b-a5f221 in account 123456789012 for more information.","type":"server_error","code":"model_error"}}`

	// On an inference component the wrapper names a container id instead of "primary".
	vllmICModelErrorBody = `{"error":{"message":"Received client error (404) from mRLgmCB1SCyheljhJ0SDvFqWQKlF9tLytfqI with message \"{\"error\":{\"message\":\"The model ` + "`does-not-exist`" + ` does not exist. Valid aliases: qwen3-0.6b.\",\"type\":\"NotFoundError\",\"param\":\"model\",\"code\":404}}\". See https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#logEventViewer:group=/aws/sagemaker/Endpoints/bifrost-vllm-qwen3-0-6b-611803 in account 123456789012 for more information.","type":"server_error","code":"model_error"}}`

	expiredTokenBody      = `{"error":{"message":"[REDACTED] has expired","type":"permission_error","code":"access_denied"}}`
	unknownEndpointBody   = `{"error":{"message":"Endpoint bifrost-probe-missing-endpoint of account 123456789012 not found.","type":"invalid_request_error","code":"validation_error"}}`
	unwrappedModelErrBody = `{"error":{"message":"model failed without the usual wrapper","type":"server_error","code":"model_error"}}`
)

func errorResponse(status int, body string) *fasthttp.Response {
	resp := &fasthttp.Response{}
	resp.SetStatusCode(status)
	resp.Header.SetContentType("application/json")
	resp.SetBodyString(body)
	return resp
}

func TestParseOpenAIPathErrorUnwrapsContainerError(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantMsg    string
		wantType   string
	}{
		{"vllm", 400, vllmModelErrorBody, 400, "max_tokens=100000 cannot be greater than max_model_len=32768. Please request fewer output tokens. (parameter=max_tokens, value=100000)", "BadRequestError"},
		{"sglang top-level error object", 400, sglangModelErrorBody, 400, "max_completion_tokens is too large: 1000000.This model supports at most 32768 completion tokens.", "BadRequestError"},
		{"lmi 424", 424, lmiAsyncFailureBody, 424, "Async Inference Failure service.invoke_handler_async() failure", ""},
		{"lmi ErrorInfo string reports the inner 404", 500, lmiErrorInfoBody, 404, "The model `does-not-exist` does not exist.", "NotFoundError"},
		{"inference component names a container id", 404, vllmICModelErrorBody, 404, "The model `does-not-exist` does not exist. Valid aliases: qwen3-0.6b.", "NotFoundError"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bErr := parseOpenAIPathError(errorResponse(tc.status, tc.body))
			require.NotNil(t, bErr.StatusCode)
			require.Equal(t, tc.wantStatus, *bErr.StatusCode)
			require.Equal(t, tc.wantMsg, bErr.Error.Message)
			require.NotContains(t, bErr.Error.Message, "CloudWatch")
			if tc.wantType != "" {
				require.NotNil(t, bErr.Error.Type)
				require.Equal(t, tc.wantType, *bErr.Error.Type)
			}
		})
	}
}

func TestParseOpenAIPathErrorKeepsSageMakerErrors(t *testing.T) {
	bErr := parseOpenAIPathError(errorResponse(400, unknownEndpointBody))
	require.Equal(t, 400, *bErr.StatusCode)
	require.Equal(t, "Endpoint bifrost-probe-missing-endpoint of account 123456789012 not found.", bErr.Error.Message)
	require.Equal(t, "validation_error", *bErr.Error.Code)

	bErr = parseOpenAIPathError(errorResponse(500, unwrappedModelErrBody))
	require.Equal(t, "model failed without the usual wrapper", bErr.Error.Message, "an unrecognised wrapper keeps SageMaker's message")
	require.Equal(t, 500, *bErr.StatusCode)
}

func TestIsExpiredTokenError(t *testing.T) {
	require.True(t, isExpiredTokenError(parseOpenAIPathError(errorResponse(403, expiredTokenBody))))
	require.False(t, isExpiredTokenError(parseOpenAIPathError(errorResponse(400, unknownEndpointBody))))
	require.False(t, isExpiredTokenError(&schemas.BifrostError{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "Authentication failed: Please make sure your API Key is valid."}}),
		"a wrong-region or malformed token is a configuration error, not a stale cache entry")
	require.False(t, isExpiredTokenError(nil))
}
