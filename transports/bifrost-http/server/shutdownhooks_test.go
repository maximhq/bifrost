package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestShutdownHooksRunFirstInTheDrain pins that work registered with AddShutdownHook runs in the part
// of the shutdown drain that finishes before the bifrost client and the stores close, in the order it
// was registered, so a hook's background jobs can still call the client and write their results.
func TestShutdownHooksRunFirstInTheDrain(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()
	s := &BifrostHTTPServer{}
	var ran []string
	s.AddShutdownHook(func() { ran = append(ran, "first") })
	s.AddShutdownHook(func() { ran = append(ran, "second") })
	s.drainClientUsers()
	assert.Equal(t, []string{"first", "second"}, ran)
}
