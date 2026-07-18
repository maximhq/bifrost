package deepgram

import (
	"fmt"
	"net/url"
)

// deepgramBlockedQueryParams are Deepgram query params Bifrost never forwards.
// callback/callback_method would turn the request into Deepgram's async
// webhook-delivery mode, which has no analog in Bifrost's synchronous
// request/response handler pipeline. `url` is consumed directly by
// Transcription() to choose the JSON-body request path and must never also be
// echoed onto the query string.
var deepgramBlockedQueryParams = map[string]bool{
	"callback":        true,
	"callback_method": true,
	"url":             true,
}

// appendExtraParamsAsQuery serializes provider-specific ExtraParams onto a
// Deepgram request's query string. Deepgram configures nearly all of its
// Listen/Speak behavior via query parameters rather than body fields, so
// anything not already first-classed by the caller lands here as-is.
//
// Array values are added as repeated keys (?key=a&key=b), not comma-joined:
// several Deepgram Listen params (keywords, replace, search) are documented as
// multi-valued via key repetition, and comma-joining would silently produce a
// malformed request for those.
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
			for _, item := range v {
				q.Add(key, item)
			}
		case []interface{}:
			for _, item := range v {
				q.Add(key, fmt.Sprintf("%v", item))
			}
		default:
			q.Set(key, fmt.Sprintf("%v", v))
		}
	}
}
