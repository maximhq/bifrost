package main

import "strings"

// trimModelPrefix strips the "provider/" prefix from a LiteLLM model string.
// wildcards like "*", "openai/*", "bedrock/*" collapse to "*".
func trimModelPrefix(model string) string {
	if i := strings.Index(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	if model == "*" {
		return "*"
	}
	return model
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }
