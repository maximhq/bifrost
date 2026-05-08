package handlers

import "github.com/maximhq/bifrost/core/schemas"

var version string
var logger schemas.Logger

// SetLogger sets the logger for the application.
func SetLogger(l schemas.Logger) {
	logger = l
}

// GetLogger returns the current logger for the application.
func GetLogger() schemas.Logger {
	return logger
}

// SetVersion sets the version for the application.
func SetVersion(v string) {
	version = v
}

func GetVersion() string {
	return version
}
