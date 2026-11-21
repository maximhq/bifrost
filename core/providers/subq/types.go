package subq

import "github.com/maximhq/bifrost/core/providers/typesafe"

// SubqImage is one image in a Decisions API request: plain base64 bytes
// with an explicit media type. Subq rejects URLs and data: URI prefixes.
type SubqImage struct {
	MediaType string `json:"mediaType"`
	Base64    string `json:"base64"`
}

// SubqDecisionRequest is the body of POST /v1/systemone. It reuses the
// Typesafe System One question shape and adds Subq's images array.
type SubqDecisionRequest struct {
	*typesafe.TypesafeDecisionRequest
	Images []SubqImage `json:"images,omitempty"`
}
