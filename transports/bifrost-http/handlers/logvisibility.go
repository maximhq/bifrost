package handlers

import (
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/valyala/fasthttp"
)

// withHiddenRequestTypes applies configured visibility only to log read routes.
// The separate context key preserves any enterprise access-control query scope.
func (h *LoggingHandler) withHiddenRequestTypes(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if h.config == nil || h.config.LogsStoreConfig == nil || len(h.config.LogsStoreConfig.HiddenRequestTypes) == 0 {
			next(ctx)
			return
		}
		previous := ctx.UserValue(logstore.HiddenRequestTypesContextKey)
		ctx.SetUserValue(logstore.HiddenRequestTypesContextKey, h.config.LogsStoreConfig.HiddenRequestTypes)
		defer ctx.SetUserValue(logstore.HiddenRequestTypesContextKey, previous)
		next(ctx)
	}
}
