//go:build !tinygo && !wasm

package starlark

import "github.com/maximhq/bifrost/core/schemas"

var logger schemas.Logger

// SetLogger sets the logger for the starlark package.
func SetLogger(l schemas.Logger) {
	logger = l
}
