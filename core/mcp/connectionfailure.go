package mcp

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

// errCredentialRotated is the recorded reason when CloseAndMarkNeedsReauth
// parks a client: the stored credential was replaced by an admin and the
// connection it authorized cannot be reused until the new one is authorized.
var errCredentialRotated = errors.New("credential was rotated and must be reauthorized")

// connectionFailureMessageMaxLen bounds MCPConnectionFailure.Message. The
// record rides every client-list response and, in a distributed deployment,
// every node-state heartbeat, so an upstream error that embeds a whole HTML
// error page must not travel at full length.
const connectionFailureMessageMaxLen = 512

// newConnectionFailure builds the record for a failure that just happened at
// stage. previous is the client's current record, if any: a failure that
// follows another keeps the earlier Since so the record spans the whole
// unhealthy run, while At always reflects this most recent attempt. The
// caller replaces the client's pointer with the result rather than mutating
// the old record in place (see MCPConnectionFailure's doc).
func newConnectionFailure(stage schemas.MCPConnectionFailureStage, err error, previous *schemas.MCPConnectionFailure) *schemas.MCPConnectionFailure {
	now := time.Now()
	since := now
	if previous != nil && !previous.Since.IsZero() {
		since = previous.Since
	}
	return &schemas.MCPConnectionFailure{
		Stage:   stage,
		Message: connectionFailureMessage(err),
		At:      now,
		Since:   since,
	}
}

// connectionFailureMessage normalizes an error for the record: internal
// whitespace (including the newlines a multi-line upstream body carries)
// collapses to single spaces, and the result is cut at
// connectionFailureMessageMaxLen on a rune boundary.
func connectionFailureMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if utf8.RuneCountInString(msg) <= connectionFailureMessageMaxLen {
		return msg
	}
	runes := []rune(msg)
	return string(runes[:connectionFailureMessageMaxLen-1]) + "…"
}

// markClientHealthy is the one way State becomes Healthy: the failure record
// is cleared in the same write, so a Healthy client never carries a stale
// explanation. Caller holds m.mu.
func markClientHealthy(cs *schemas.MCPClientState) {
	cs.State = schemas.MCPConnectionStateHealthy
	cs.LastFailure = nil
}

// recordClientFailure replaces the client's failure record with one for the
// failure that just happened, preserving the start of the current unhealthy
// run. It does not touch State: every caller decides that separately, since
// the same failure lands in Unstable or NeedsReauth depending on its
// classification. Caller holds m.mu.
func recordClientFailure(cs *schemas.MCPClientState, stage schemas.MCPConnectionFailureStage, err error) {
	cs.LastFailure = newConnectionFailure(stage, err, cs.LastFailure)
}
