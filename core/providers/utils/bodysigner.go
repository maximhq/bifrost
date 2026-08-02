package utils

import schemas "github.com/maximhq/bifrost/core/schemas"

// BodySigner signs the final request body bytes after the handler has marshaled them and
// returns auth headers to set on the outgoing request. Handlers invoke it immediately after
// building the body, only when non-nil. The caller decides whether signing applies (e.g. AWS
// SigV4 for Bedrock Mantle when no API key is present); the handler stays auth-scheme-agnostic.
type BodySigner func(jsonData []byte) (map[string]string, *schemas.BifrostError)
