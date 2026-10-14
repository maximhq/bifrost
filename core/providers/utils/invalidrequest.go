package utils

import (
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// invalidRequestError marks a converter failure as caller input rather than an
// internal Bifrost fault, so it can be reported as a 400 instead of a 500.
type invalidRequestError struct{ msg string }

func (e *invalidRequestError) Error() string { return e.msg }

// InvalidRequestErrorf builds the error a request converter returns when the
// caller's own input is the problem.
func InvalidRequestErrorf(format string, args ...any) error {
	return &invalidRequestError{msg: fmt.Sprintf(format, args...)}
}

// AsBifrostBadRequestError converts a converter error into a 400 when it (or
// anything it wraps) was built by InvalidRequestErrorf.
func AsBifrostBadRequestError(err error) (*schemas.BifrostError, bool) {
	var invalid *invalidRequestError
	if errors.As(err, &invalid) {
		return NewBifrostBadRequestError(invalid.msg), true
	}
	return nil, false
}

// IsInvalidRequestError reports whether err, or anything it wraps, was built
// by InvalidRequestErrorf.
func IsInvalidRequestError(err error) bool {
	var invalid *invalidRequestError
	return errors.As(err, &invalid)
}
