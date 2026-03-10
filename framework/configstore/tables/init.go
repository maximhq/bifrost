package tables

import "github.com/maximhq/bifrost/core/schemas"

var logger schemas.Logger

// SetLogger sets the logger for the tables package.
func SetLogger(l schemas.Logger) {
	logger = l
}
