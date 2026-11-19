package schemas

import (
	"context"
	"time"
)

// PhysicalSendRoute is core-owned route identity for the current provider
// attempt. It is written only while a physical-send guard is installed.
type PhysicalSendRoute struct {
	Provider ModelProvider
	Model    string
}

// PhysicalSendAttempt is the final provider-bound request data presented to an
// embedder's physical-send authorizer. Body is borrowed, read-only, and valid
// only for the duration of the Authorize call; implementations must not retain
// it. Destination contains the provider request URI with credentials removed.
type PhysicalSendAttempt struct {
	AttemptID   string
	Provider    ModelProvider
	Model       string
	Method      string
	Destination string
	Body        []byte
	BodySHA256  string
}

// PhysicalSendAuthorizer decides whether one exact physical provider attempt
// may be written. It is called synchronously at the transport boundary for
// every RoundTrip, including retries. Returning an error must prevent the
// staged request from being written.
type PhysicalSendAuthorizer func(ctx context.Context, attempt PhysicalSendAttempt) error

// PhysicalSendGuard scopes one authorizer to one Bifrost request. The transport
// rejects serialized requests larger than MaxSerializedRequestBytes rather
// than truncating them.
type PhysicalSendGuard struct {
	MaxSerializedRequestBytes int
	AuthorizationDeadline     time.Time
	Authorize                 PhysicalSendAuthorizer
}

type physicalSendTransportStateKey struct{}

type physicalSendTransportState struct {
	guard         *PhysicalSendGuard
	required      bool
	trustedValues map[any]any
}
