package evals

import (
	"context"
	"errors"
	"strings"

	"github.com/tidwall/gjson"
)

// JudgeRequest is one model call an evaluator wants made.
type JudgeRequest struct {
	Provider string
	Model    string
	System   string
	User     string
	Params   map[string]any // provider model parameters, passed through
	// ParamsJSON is Params already encoded. An evaluator encodes its parameters once
	// when it is built and sets this, so they are not encoded again for every call.
	ParamsJSON []byte
	Schema     map[string]any // JSON schema for structured output; nil asks for plain text
}

// JudgeResponse is the model's answer to a JudgeRequest.
type JudgeResponse struct {
	Text      string
	Truncated bool     // the reply stopped at the token limit
	Usage     *Usage   // tokens billed for the call, when known
	Cost      *float64 // cost billed for the call, when known
}

// Judge makes a model call on behalf of an evaluator. The engine stays independent of
// how the call is routed; NewBifrostJudge adapts the gateway client to this shape.
type Judge func(ctx context.Context, req JudgeRequest) (JudgeResponse, error)

// errNoJSONObject is returned when a reply holds no parseable JSON object.
var errNoJSONObject = errors.New("the judge did not return valid JSON")

// firstJSONObject returns the first well-formed JSON object found in text, as its raw
// text. Judges often wrap their answer in prose or a code fence, so every opening brace
// is tried in turn. The object is returned undecoded: callers read the fields they need
// with gjson, which avoids building a map for every reply.
func firstJSONObject(text string) (string, error) {
	for start := strings.IndexByte(text, '{'); start >= 0; {
		if end := matchingBrace(text, start); end > start {
			if candidate := text[start : end+1]; gjson.Valid(candidate) {
				return candidate, nil
			}
		}
		next := strings.IndexByte(text[start+1:], '{')
		if next < 0 {
			break
		}
		start += next + 1
	}
	return "", errNoJSONObject
}

// matchingBrace returns the index of the brace closing the object that opens at start,
// or -1. Braces inside string literals do not count.
func matchingBrace(text string, start int) int {
	depth := 0
	inString, escaped := false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// reasoningText renders a judge's reasoning field. Anything that is not a string is
// kept as JSON so nothing the judge said is lost.
func reasoningText(reasoning gjson.Result) string {
	switch reasoning.Type {
	case gjson.Null:
		return ""
	case gjson.String:
		return reasoning.String()
	}
	return reasoning.Raw
}

// addUsage sums two usage values, treating nil as absent.
func addUsage(base, add *Usage) *Usage {
	if add == nil {
		return base
	}
	if base == nil {
		return new(*add)
	}
	return &Usage{
		PromptTokens:     base.PromptTokens + add.PromptTokens,
		CompletionTokens: base.CompletionTokens + add.CompletionTokens,
		TotalTokens:      base.TotalTokens + add.TotalTokens,
	}
}

// addCost sums two costs, treating nil as absent.
func addCost(base, add *float64) *float64 {
	if add == nil {
		return base
	}
	if base == nil {
		return new(*add)
	}
	return new(*base + *add)
}

// isStructuredOutputRejection reports whether a judge error says the model does not
// accept a JSON schema, in which case asking again for plain text can still work.
func isStructuredOutputRejection(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"response_format", "json_schema", "structured output", "text.format"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
