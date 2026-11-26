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

// ErrNoConfigStore is returned by a configuration write when the config store is disabled:
// configuration then comes only from config.json and changes only on a restart.
var ErrNoConfigStore = errors.New("config store not available")

// SavedNotAppliedError wraps a failure that came after the config store committed the change.
type SavedNotAppliedError struct {
	Err error
}

func (e *SavedNotAppliedError) Error() string { return e.Err.Error() }

func (e *SavedNotAppliedError) Unwrap() error { return e.Err }

// IsSavedNotApplied reports whether err says the store kept the change although a later step failed.
func IsSavedNotApplied(err error) bool {
	var saved *SavedNotAppliedError
	return errors.As(err, &saved)
}

// ErrInvalidPluginConfig wraps a plugin config refused before the plugin is loaded, so callers
// can tell a config the caller got wrong apart from a failure to load a valid one.
var ErrInvalidPluginConfig = errors.New("invalid plugin config")
