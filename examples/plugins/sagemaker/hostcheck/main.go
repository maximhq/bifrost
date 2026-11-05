package main

import (
	"fmt"
	"os"
	"plugin"

	_ "github.com/aws/aws-sdk-go-v2/aws"
	_ "github.com/aws/aws-sdk-go-v2/config"
	_ "github.com/maximhq/bifrost/core/providers/openai"
	_ "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const expectedPluginName = "sagemaker"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: hostcheck <plugin.so>")
		os.Exit(2)
	}

	loaded, err := plugin.Open(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "open plugin: %v\n", err)
		os.Exit(1)
	}

	getName, err := lookup[func() string](loaded, "GetName")
	if err != nil {
		fail(err)
	}
	if name := getName(); name != expectedPluginName {
		fail(fmt.Errorf("plugin name = %q, want %q", name, expectedPluginName))
	}

	if _, err := lookup[func(any) error](loaded, "Init"); err != nil {
		fail(err)
	}
	if _, err := lookup[func(*schemas.BifrostContext, *schemas.BifrostRequest) error](loaded, "PreRequestHook"); err != nil {
		fail(err)
	}
	if _, err := lookup[func(*schemas.BifrostContext, *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error)](loaded, "PreLLMHook"); err != nil {
		fail(err)
	}
	if _, err := lookup[func(*schemas.BifrostContext, *schemas.BifrostResponse, *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)](loaded, "PostLLMHook"); err != nil {
		fail(err)
	}
	cleanup, err := lookup[func() error](loaded, "Cleanup")
	if err != nil {
		fail(err)
	}
	if err := cleanup(); err != nil {
		fail(fmt.Errorf("cleanup plugin: %w", err))
	}

	fmt.Printf("loaded %s with compatible Bifrost and AWS package hashes\n", getName())
}

func lookup[T any](loaded *plugin.Plugin, name string) (T, error) {
	var zero T
	symbol, err := loaded.Lookup(name)
	if err != nil {
		return zero, fmt.Errorf("lookup %s: %w", name, err)
	}
	typed, ok := symbol.(T)
	if !ok {
		return zero, fmt.Errorf("symbol %s has an incompatible type", name)
	}
	return typed, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
