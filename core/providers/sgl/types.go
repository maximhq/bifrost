package sgl

import "github.com/maximhq/bifrost/core/providers/typesafe"

type SGLDecisionRequest struct {
	*typesafe.TypesafeDecisionRequest
	Images []string `json:"images,omitempty"` // data or http(s) URLs, shared by every question
}
