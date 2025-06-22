package main

import (
	"os"

	maximplugin "github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/sdk"
)

func main() {
	// Check if running as an RPC plugin
	if sdk.IsPluginBinary() {
		// Running as RPC plugin - serve via SDK
		// Get configuration from environment variables
		apiKey := os.Getenv("MAXIM_API_KEY")
		logRepoId := os.Getenv("MAXIM_LOG_REPO_ID")

		if apiKey == "" || logRepoId == "" {
			panic("MAXIM_API_KEY and MAXIM_LOG_REPO_ID environment variables must be set")
		}

		plugin, err := maximplugin.NewMaximLoggerPlugin(apiKey, logRepoId)
		if err != nil {
			panic(err)
		}

		sdk.ServePlugin(plugin)
	} else {
		// Running as direct binary - print usage
		println("Maxim Plugin for Bifrost")
		println("Usage:")
		println("  As RPC Plugin: Set MAXIM_API_KEY and MAXIM_LOG_REPO_ID environment variables")
		println("  As Direct Plugin: Use maximplugin.NewMaximLoggerPlugin() in your Go code")
	}
}
