package configstore

import (
	"errors"
	"fmt"
	"strings"
)

var ErrNotFound = errors.New("not found")
var ErrAlreadyExists = errors.New("already exists")

// ErrMCPEndpointSlugExists is returned when a create resolves an endpoint slug already used by a
// Virtual MCP or an MCP client. Both serve at /mcp/<slug>, so the slug namespace is shared; callers
// answer with a clear message and a 409. (Name uniqueness is left to each table's own unique index
// and surfaces as a generic unique-constraint error.)
var ErrMCPEndpointSlugExists = errors.New("an MCP endpoint with this slug already exists")

// ErrMCPEndpointSlugInvalid is returned when a create cannot derive an endpoint slug (the name
// slugifies to empty and no endpoint_slug was supplied). It is caller input, so handlers map it
// to a 400 rather than a 500.
var ErrMCPEndpointSlugInvalid = errors.New("could not derive an MCP endpoint slug; provide an endpoint_slug")

// ErrVirtualKeyAccessProfileManaged is returned by AttachVirtualMCPToVirtualKey (enterprise) when the
// target virtual key is managed by an access profile: its MCP access is governed by the profile, so it
// cannot be assigned a Virtual MCP directly. OSS never returns it (no access profiles).
var ErrVirtualKeyAccessProfileManaged = errors.New("access-profile-managed virtual keys cannot be assigned a virtual MCP directly")

// ErrConfigUnreadable marks a stored configuration value that could not be
// decoded or that failed validation — the value is present but this version
// cannot make sense of it.
//
// It exists so callers can tell that apart from the store itself being
// unreachable. A caller with usable defaults should degrade on this and only
// this: degrading on every error would report an unreachable database as a
// working installation running on defaults, which is a worse lie than an error.
var ErrConfigUnreadable = errors.New("stored configuration is unreadable")

// ErrUnresolvedKeys is returned when one or more keys could not be resolved
type ErrUnresolvedKeys struct {
	Identifiers []string
}

func (e *ErrUnresolvedKeys) Error() string {
	return fmt.Sprintf("could not resolve keys: %s", strings.Join(e.Identifiers, ", "))
}
