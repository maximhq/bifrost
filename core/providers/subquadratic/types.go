package subquadratic

import "github.com/maximhq/bifrost/core/providers/typesafe"

// SubquadraticImage is one image in a Decisions API request: plain base64 bytes
// with an explicit media type. SubQ rejects URLs and data: URI prefixes.
type SubquadraticImage struct {
	MediaType string `json:"mediaType"`
	Base64    string `json:"base64"`
}

// SubquadraticDecisionRequest is the body of POST /v1/systemone. It reuses the
// Typesafe System One question shape and adds SubQ's images array.
type SubquadraticDecisionRequest struct {
	*typesafe.TypesafeDecisionRequest
	Images []SubquadraticImage `json:"images,omitempty"`
}
