package logstore

import "fmt"

var (
	ErrNotFound    = fmt.Errorf("log not found")
	ErrJobInternal = fmt.Errorf("internal job store error")
	// ErrInvalidWebhookReference marks a submit whose webhook endpoint
	// reference is unusable — a caller mistake, not a server failure.
	ErrInvalidWebhookReference = fmt.Errorf("invalid webhook endpoint reference")
)
