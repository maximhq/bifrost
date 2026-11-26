package chaosmock

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// MCP protocol versions a virtual server can speak. 2025-11-25 is the last revision negotiated
// through initialize; 2026-07-28 replaced that handshake with server/discover and dropped sessions.
const (
	ProtocolVersion20260728 = "2026-07-28"
	ProtocolVersion20251125 = "2025-11-25"
	ProtocolVersion20250618 = "2025-06-18"
	ProtocolVersion20250326 = "2025-03-26"
	ProtocolVersion20241105 = "2024-11-05"
)

// KnownProtocolVersions lists every version a server can be set to speak, newest first.
var KnownProtocolVersions = []string{ProtocolVersion20260728, ProtocolVersion20251125, ProtocolVersion20250618, ProtocolVersion20250326, ProtocolVersion20241105}

// DefaultProtocolVersions is what a fresh server speaks: the session-era revisions it has always
// spoken, so a scenario that never sets versions keeps the handshakes and session counts it had.
var DefaultProtocolVersions = []string{ProtocolVersion20250618, ProtocolVersion20250326, ProtocolVersion20241105}

// CodeUnsupportedProtocolVersion answers a request for a version the server does not speak; its
// data lists the versions it does, so a client can negotiate down.
const CodeUnsupportedProtocolVersion = -32022

// isModernVersion reports whether v uses the sessionless protocol core.
func isModernVersion(v string) bool { return v >= ProtocolVersion20260728 }

// Handshake records one negotiation a server answered: the method that carried it (initialize or
// server/discover), the version the client asked for, and the one it was answered with.
type Handshake struct {
	Method     string `json:"method"`
	Requested  string `json:"requested"`
	Negotiated string `json:"negotiated"`
}

// versionState holds the versions a server speaks, newest first.
type versionState struct {
	mu       sync.Mutex
	versions []string
}

// newVersionState starts a server on DefaultProtocolVersions.
func newVersionState() *versionState {
	return &versionState{versions: slices.Clone(DefaultProtocolVersions)}
}

// set replaces the versions, refusing an empty list or an unknown version, and keeps them newest
// first without duplicates.
func (v *versionState) set(versions []string) error {
	if len(versions) == 0 {
		return errors.New("versions must name at least one protocol version")
	}
	for _, version := range versions {
		if !slices.Contains(KnownProtocolVersions, version) {
			return fmt.Errorf("unknown protocol version %q: must be one of %s", version, strings.Join(KnownProtocolVersions, ", "))
		}
	}
	var ordered []string
	for _, known := range KnownProtocolVersions {
		if slices.Contains(versions, known) {
			ordered = append(ordered, known)
		}
	}
	v.mu.Lock()
	v.versions = ordered
	v.mu.Unlock()
	return nil
}

// list returns the versions, newest first.
func (v *versionState) list() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.versions)
}

// supports reports whether the server speaks version.
func (v *versionState) supports(version string) bool {
	return slices.Contains(v.list(), version)
}

// negotiateLegacy answers an initialize: the requested version when the server speaks it through
// initialize, otherwise the newest version it does, or "" when it speaks none.
func (v *versionState) negotiateLegacy(requested string) string {
	versions := v.list()
	if !isModernVersion(requested) && slices.Contains(versions, requested) {
		return requested
	}
	for _, version := range versions {
		if !isModernVersion(version) {
			return version
		}
	}
	return ""
}

// unsupportedVersion is the error answering a request for a version the server does not speak.
func unsupportedVersion(id json.RawMessage, requested string, supported []string) *response {
	data, _ := json.Marshal(map[string]any{"supported": supported, "requested": requested})
	resp := newError(id, CodeUnsupportedProtocolVersion, fmt.Sprintf("unsupported protocol version: %q (supported: %v)", requested, supported))
	resp.Error.Data = data
	return resp
}

// SetProtocolVersions replaces the versions the server speaks. Clients negotiate afresh on their
// next handshake; connections already open keep the version they negotiated.
func (s *Server) SetProtocolVersions(versions []string) error { return s.versions.set(versions) }

// ProtocolVersions returns the versions the server speaks, newest first.
func (s *Server) ProtocolVersions() []string { return s.versions.list() }
