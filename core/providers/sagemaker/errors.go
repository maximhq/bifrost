package sagemaker

import (
	"regexp"
	"strconv"
	"strings"

	openai "github.com/maximhq/bifrost/core/providers/openai"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

const (
	modelErrorCode     = "model_error"
	modelErrorBodyFrom = ` with message "`
	modelErrorBodyTo   = `". See `
	expiredTokenText   = "has expired"
)

// lmiErrorInfoRegex reads LMI's stringified error, e.g.
// error=ErrorInfo(message='The model `x` does not exist.', type='NotFoundError', param='model', code=404).
var lmiErrorInfoRegex = regexp.MustCompile(`message='(.*)', type='([^']*)'.*code=(\d+)`)

// parseOpenAIPathError converts an /openai/v1 error response. SageMaker keeps the container's
// status but wraps its body as {"error":{"type":"server_error","code":"model_error","message":
// "Received ... error (N) from <container> with message \"<body>\". See <CloudWatch URL> ..."}},
// so the container's own error is unwrapped from that message.
func parseOpenAIPathError(resp *fasthttp.Response) *schemas.BifrostError {
	bErr := openai.ParseOpenAIError(resp)
	if bErr.Error != nil && bErr.Error.Code != nil && *bErr.Error.Code == modelErrorCode {
		unwrapModelError(bErr)
	}
	return bErr
}

// unwrapModelError replaces SageMaker's wrapper message with the container's error. It handles
// vLLM ({"error":{...}}), SGLang ({"object":"error",...}) and LMI ({"code","message"} or a
// stringified ErrorInfo). LMI reports client errors as 424/500, so a 4xx code found inside
// becomes the status. When the wrapper does not match, the message is left untouched.
func unwrapModelError(bErr *schemas.BifrostError) {
	msg := bErr.Error.Message
	from := strings.Index(msg, modelErrorBodyFrom)
	to := strings.LastIndex(msg, modelErrorBodyTo)
	if from < 0 || to <= from+len(modelErrorBodyFrom) {
		return
	}
	body := msg[from+len(modelErrorBodyFrom) : to]

	var message, errType string
	var code int64
	switch {
	case !gjson.Valid(body):
		message = body
	case gjson.Get(body, "error.message").Exists():
		message = gjson.Get(body, "error.message").String()
		errType = gjson.Get(body, "error.type").String()
		code = gjson.Get(body, "error.code").Int()
		if param := gjson.Get(body, "error.param"); param.Exists() && param.Type != gjson.Null {
			bErr.Error.Param = param.String()
		}
	case gjson.Get(body, "error").Type == gjson.String:
		inner := gjson.Get(body, "error").String()
		if m := lmiErrorInfoRegex.FindStringSubmatch(inner); m != nil {
			message, errType = m[1], m[2]
			code, _ = strconv.ParseInt(m[3], 10, 64)
		} else {
			message = inner
		}
	default:
		message = gjson.Get(body, "message").String()
		errType = gjson.Get(body, "type").String()
		code = gjson.Get(body, "code").Int()
	}
	if message == "" {
		return
	}
	bErr.Error.Message = message
	if errType != "" {
		bErr.Error.Type = schemas.Ptr(errType)
	}
	if code >= 400 && code < 500 && (bErr.StatusCode == nil || *bErr.StatusCode >= 500) {
		bErr.StatusCode = schemas.Ptr(int(code))
	}
}

// isExpiredTokenError reports whether SageMaker rejected the bearer token as expired, which
// means the cached token outlived its credentials and must be minted again.
func isExpiredTokenError(bErr *schemas.BifrostError) bool {
	return bErr != nil && bErr.StatusCode != nil && *bErr.StatusCode == fasthttp.StatusForbidden &&
		bErr.Error != nil && strings.Contains(bErr.Error.Message, expiredTokenText)
}
