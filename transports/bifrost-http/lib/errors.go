package lib

import (
	"errors"

	"github.com/maximhq/bifrost/core/schemas"
)

// Thin wrapper, keeping it here since it is exported
func NormalizeJSONErrorStatus(code int) int {
	return schemas.NormalizeJSONErrorStatus(code)
}

var ErrNotFound = errors.New("not found")
var ErrAlreadyExists = errors.New("already exists")

// ErrInvalidPluginConfig wraps a plugin config refused before the plugin is loaded, so callers
// can tell a config the caller got wrong apart from a failure to load a valid one.
var ErrInvalidPluginConfig = errors.New("invalid plugin config")
