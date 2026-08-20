package configstore

import (
	"errors"
	"fmt"
	"strings"
)

var ErrNotFound = errors.New("not found")
var ErrAlreadyExists = errors.New("already exists")

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
