package tests

import (
	"bifrost/interfaces"
	"fmt"
	"time"

	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

type Plugin struct {
	logger *logging.Logger
}

func (plugin *Plugin) PreHook(req *interfaces.BifrostRequest) (*interfaces.BifrostRequest, error) {
	traceID := time.Now().Format("20060102_150405000")

	trace := plugin.logger.Trace(&logging.TraceConfig{
		Id:   traceID,
		Name: maxim.StrPtr("bifrost"),
	})

	trace.SetInput(fmt.Sprintf("New Request Incoming: %v", req))

	req.PluginParams["traceID"] = traceID

	return req, nil
}

func (plugin *Plugin) PostHook(res *interfaces.CompletionResult) (*interfaces.CompletionResult, error) {
	fmt.Println(res.PluginParams)

	traceID := res.PluginParams["traceID"].(string)

	plugin.logger.SetTraceOutput(traceID, fmt.Sprintf("Response: %v", res))
	return res, nil
}
