package main

import (
	mockerplugin "github.com/maximhq/bifrost/plugins/mocker"
	"github.com/maximhq/bifrost/sdk"
)

func main() {
	// Check if running as an RPC plugin
	if sdk.IsPluginBinary() {
		// Serve the plugin factory - configuration will be passed via RPC
		sdk.ServePluginFactory(mockerplugin.NewPlugin)
	} else {
		// Running as direct binary - print usage
		println("Mocker Plugin for Bifrost")
		println("Usage:")
		println("  As RPC Plugin: Configuration is passed via RPC from the host")
		println("  As Direct Plugin: Use mockerplugin.NewPlugin() in your Go code")
		println("")
		println("Example configuration JSON:")
		println(`{"enabled":true,"default_behavior":"passthrough","rules":[{"name":"test","enabled":true,"priority":1,"conditions":{},"probability":1.0,"responses":[{"type":"success","weight":1.0,"content":{"message":"Mock response"}}]}]}`)
	}
}
