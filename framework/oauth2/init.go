package oauth2

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// logger defaults to the standard logger so discovery that runs before
// NewOAuth2Provider (config.json load) can log.
var logger schemas.Logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)

func SetLogger(l schemas.Logger) {
	logger = l
}
