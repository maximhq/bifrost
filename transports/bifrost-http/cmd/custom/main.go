// Package main is the custom production entry point for Bifrost. It wraps the
// standard bifrost-http server with the embedded admin UI.
//
// This binary is built by Dockerfile.custom using a go.work workspace that
// resolves local module dependencies (core, framework, plugins, transports).
package main

import (
	"context"
	"embed"
	"flag"
	"os"
	"strings"
	"time"

	_ "go.uber.org/automaxprocs"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bifrostServer "github.com/maximhq/bifrost/transports/bifrost-http/server"
)

//go:embed all:ui
var uiContent embed.FS

var Version string

var logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
var server *bifrostServer.BifrostHTTPServer

func init() {
	if Version == "" {
		Version = "v1.0.0-custom"
	}
	defaultHost := os.Getenv("BIFROST_HOST")
	if defaultHost == "" {
		defaultHost = bifrostServer.DefaultHost
	}
	defaultLogLevel := strings.ToLower(os.Getenv("LOG_LEVEL"))
	if defaultLogLevel == "" {
		defaultLogLevel = bifrostServer.DefaultLogLevel
	}
	server = bifrostServer.NewBifrostHTTPServer(Version, uiContent)
	flag.StringVar(&server.Port, "port", bifrostServer.DefaultPort, "Port to run the server on")
	flag.StringVar(&server.Host, "host", defaultHost, "Host to bind the server to")
	flag.StringVar(&server.AppDir, "app-dir", bifrostServer.DefaultAppDir, "Application data directory")
	flag.StringVar(&server.LogLevel, "log-level", defaultLogLevel, "Logger level (debug, info, warn, error)")
	flag.StringVar(&server.LogOutputStyle, "log-style", bifrostServer.DefaultLogOutputStyle, "Logger output type (json or pretty)")
}

func main() {
	flag.Parse()

	logger.SetOutputType(schemas.LoggerOutputType(server.LogOutputStyle))
	logger.SetLevel(schemas.LogLevel(server.LogLevel))
	lib.SetLogger(logger)
	bifrostServer.SetLogger(logger)
	handlers.SetLogger(logger)

	ctx := context.Background()
	t := time.Now()
	if err := server.Bootstrap(ctx); err != nil {
		logger.Error("failed to bootstrap server: %v", err)
		os.Exit(1)
	}
	logger.Info("Time spent in Bifrost server bootstrap %d ms", time.Since(t).Milliseconds())

	if err := server.Start(); err != nil {
		logger.Error("failed to start server: %v", err)
		os.Exit(1)
	}
}
