package lib

import (
	"errors"

	"github.com/valyala/fasthttp"
)

// NormalizeJSONErrorStatus maps statuses that forbid response content to 502.
func NormalizeJSONErrorStatus(code int) int {
	if code < 200 || code == fasthttp.StatusNoContent ||
		code == fasthttp.StatusResetContent || code == fasthttp.StatusNotModified {
		return fasthttp.StatusBadGateway
	}
	return code
}

var ErrNotFound = errors.New("not found")
var ErrAlreadyExists = errors.New("already exists")
