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
