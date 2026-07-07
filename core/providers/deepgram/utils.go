package deepgram

import (
	"fmt"
	"net/url"
	"strings"
)

// deepgramBlockedQueryParams are Deepgram query params Bifrost never forwards.
// callback/callback_method would turn the request into Deepgram's async
// webhook-delivery mode, which has no analog in Bifrost's synchronous
// request/response handler pipeline.
var deepgramBlockedQueryParams = map[string]bool{
	"callback":        true,
	"callback_method": true,
}

// appendExtraParamsAsQuery serializes provider-specific ExtraParams onto a
// Deepgram request's query string. Deepgram configures nearly all of its
// Listen/Speak behavior via query parameters rather than body fields, so
// anything not already first-classed by the caller lands here as-is.
func appendExtraParamsAsQuery(q url.Values, extraParams map[string]interface{}) {
	for key, value := range extraParams {
		if deepgramBlockedQueryParams[key] {
			continue
		}
		if value == nil {
			continue
		}
		switch v := value.(type) {
		case []string:
			if len(v) > 0 {
				q.Set(key, strings.Join(v, ","))
			}
		case []interface{}:
			if len(v) == 0 {
				continue
			}
			parts := make([]string, 0, len(v))
			for _, item := range v {
				parts = append(parts, fmt.Sprintf("%v", item))
			}
			q.Set(key, strings.Join(parts, ","))
		default:
			q.Set(key, fmt.Sprintf("%v", v))
		}
	}
}
