package handlers

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var version string
var logger schemas.Logger

// SetLogger sets the logger for the application.
func SetLogger(l schemas.Logger) {
	logger = l
}

// SetVersion sets the version for the application, and the version reported in
// the User-Agent sent to providers.
func SetVersion(v string) {
	version = v
	providerUtils.BifrostVersion = v
}

func GetVersion() string {
	return version
}
