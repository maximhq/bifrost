package ollama

import (
	"bytes"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/typesafe"
)

// OllamaDecisionRequest is the body of POST /v1/systemone: Typesafe's systemone
// request plus Ollama's shared images and keep_alive. It is only ever encoded;
// decoding it would run Typesafe's UnmarshalJSON on the embedded pointer and fail.
type OllamaDecisionRequest struct {
	*typesafe.TypesafeDecisionRequest
	Images    []string         `json:"images,omitempty"` // raw base64, shared by every question
	KeepAlive *OllamaKeepAlive `json:"keep_alive,omitempty"`
}

// OllamaKeepAlive is how long Ollama keeps the model loaded after a request: a
// duration string such as "5m", or a number of seconds. 0 unloads the model
// right after the request; a negative value keeps it loaded. Exactly one
// member is set.
type OllamaKeepAlive struct {
	Duration *string
	Seconds  *float64
}

// MarshalJSON emits the duration string or the number of seconds.
func (k OllamaKeepAlive) MarshalJSON() ([]byte, error) {
	if k.Duration != nil && k.Seconds != nil {
		return nil, fmt.Errorf("both keep_alive duration and seconds are set; only one should be non-nil")
	}
	if k.Duration != nil {
		return sonic.Marshal(*k.Duration)
	}
	if k.Seconds != nil {
		return sonic.Marshal(*k.Seconds)
	}
	return []byte("null"), nil
}

// UnmarshalJSON reads a string as a duration and a number as seconds, and
// rejects any other JSON type.
func (k *OllamaKeepAlive) UnmarshalJSON(data []byte) error {
	*k = OllamaKeepAlive{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var duration string
		if err := sonic.Unmarshal(trimmed, &duration); err != nil {
			return err
		}
		k.Duration = &duration
		return nil
	}
	var seconds float64
	if err := sonic.Unmarshal(trimmed, &seconds); err != nil {
		return fmt.Errorf("keep_alive must be a duration string such as \"5m\" or a number of seconds")
	}
	k.Seconds = &seconds
	return nil
}
