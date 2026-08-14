// Session identity resolution: who a conversation is, resolved from harness
// metadata or an explicit header. Lives beside the extractor because identity
// comes from the same request surfaces harness detection reads.

package complexity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

const (
	claudeCodeSessionIDHeader = "x-claude-code-session-id"
	// SessionKeyPrefix namespaces session records in the shared KV store.
	SessionKeyPrefix            = "complexity-session:v1:"
	maxComplexitySessionIDBytes = 255
)

// ResolveSessionID walks the enabled identity sources in their fixed precedence
// order. identitySources must contain normalized ComplexitySessionIdentity*
// values; configuration normalization owns defaults and validation.
func ResolveSessionID(ctx *schemas.BifrostContext, identitySources []string) (id, source string, ok bool) {
	if sessionIdentitySourceEnabled(identitySources, configstore.ComplexitySessionIdentityHeader) {
		if id := normalizeComplexitySessionID(sessionIDFromContext(ctx)); id != "" {
			return id, configstore.ComplexitySessionIdentityHeader, true
		}
	}
	if sessionIdentitySourceEnabled(identitySources, configstore.ComplexitySessionIdentityHarness) {
		if id := normalizeComplexitySessionID(harnessSessionID(ctx)); id != "" {
			return id, configstore.ComplexitySessionIdentityHarness, true
		}
	}
	return "", "", false
}

func sessionIdentitySourceEnabled(identitySources []string, wanted string) bool {
	for _, source := range identitySources {
		if source == wanted {
			return true
		}
	}
	return false
}

// normalizeComplexitySessionID validates the opaque identity shared by the
// session store and request logs. IDs are never truncated because doing so can
// merge otherwise distinct conversations.
func normalizeComplexitySessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxComplexitySessionIDBytes || !utf8.ValidString(id) || strings.IndexByte(id, 0) >= 0 {
		return ""
	}
	return id
}

func sessionIDFromContext(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	return strings.TrimSpace(id)
}

func harnessSessionID(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	switch detectComplexityHarness(ctx) {
	case complexityHarnessClaudeCode:
		return strings.TrimSpace(headers[claudeCodeSessionIDHeader])
	case complexityHarnessCodex:
		metadata, ok := parseCodexTurnMetadata(ctx)
		if !ok {
			return ""
		}
		return strings.TrimSpace(metadata.SessionID)
	default:
		return ""
	}
}

// BuildSessionKey isolates session state by scope while keeping raw
// scope and session identifiers out of storage keys.
func BuildSessionKey(scopeID, sessionID string) (string, bool) {
	scopeID = strings.TrimSpace(scopeID)
	sessionID = strings.TrimSpace(sessionID)
	if scopeID == "" || sessionID == "" {
		return "", false
	}
	return SessionKeyPrefix + SessionHash(scopeID+"\x00"+sessionID), true
}

func SessionHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
