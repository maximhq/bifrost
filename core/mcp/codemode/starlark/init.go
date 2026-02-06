//go:build !tinygo && !wasm

package starlark

import "github.com/maximhq/bifrost/core/schemas"

// noopLogger is a no-op implementation of schemas.Logger used as a fallback
// when no logger is provided.
type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                        {}
func (noopLogger) Info(string, ...any)                         {}
func (noopLogger) Warn(string, ...any)                         {}
func (noopLogger) Error(string, ...any)                        {}
func (noopLogger) Fatal(string, ...any)                        {}
func (noopLogger) SetLevel(schemas.LogLevel)                   {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType)      {}

// defaultLogger is used when nil is passed to NewStarlarkCodeMode.
var defaultLogger schemas.Logger = noopLogger{}
