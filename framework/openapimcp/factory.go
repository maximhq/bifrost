package openapimcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
)

// FactoryOption configures Factory.
type FactoryOption func(*factoryOptions)

type factoryOptions struct {
	configDir string
}

// WithConfigDir sets the directory openapi_config.spec_file paths resolve against.
func WithConfigDir(dir string) FactoryOption {
	return func(o *factoryOptions) { o.configDir = dir }
}

// Factory returns the schemas.InProcessServerFactory core calls to synthesize an
// openapi client's server: resolve the spec (inline, file or URL fetched with the
// client core supplies), parse, synthesize, pick the base URL and build the
// server with core's HTTP client and header resolver.
//
// Errors that will not go away by retrying (unparseable spec, no usable
// operation, bad base URL, missing config) are wrapped with
// schemas.ErrMCPOpenAPIConfigInvalid so the connect path fails fast; a failed
// spec_url fetch is returned as-is and keeps core's transient retry behavior.
func Factory(opts ...FactoryOption) schemas.InProcessServerFactory {
	fo := factoryOptions{}
	for _, opt := range opts {
		opt(&fo)
	}
	return func(ctx context.Context, config *schemas.MCPClientConfig, deps schemas.InProcessServerDeps) (*server.MCPServer, error) {
		if config == nil || config.OpenAPIConfig == nil {
			return nil, permanent(errors.New("openapi_config is required"))
		}
		cfg := config.OpenAPIConfig
		data, source, err := ResolveSpec(ctx, cfg, deps.HTTPClient, fo.configDir)
		if err != nil {
			if source == "url" && !errors.Is(err, ErrSpecTooLarge) {
				return nil, fmt.Errorf("loading spec_url: %w", err)
			}
			return nil, permanent(err)
		}
		specURL := ""
		if cfg.SpecURL != nil {
			specURL = *cfg.SpecURL
		}
		doc, err := Parse(data, ParseOptions{SpecURL: specURL})
		if err != nil {
			return nil, permanent(err)
		}
		syn, err := Synthesize(doc, SynthesizeOptions{ClientName: config.Name, IncludeDeprecated: cfg.IncludeDeprecated})
		if err != nil {
			return nil, permanent(err)
		}
		if len(syn.Tools) == 0 {
			return nil, permanent(fmt.Errorf("no operation in the spec can be exposed as a tool (%d unsupported)", len(syn.Unsupported)))
		}
		base, explicit, err := ResolveBaseURL(doc, cfg.BaseURL, specURL)
		if err != nil {
			return nil, permanent(err)
		}
		srv, err := BuildServer(ServerOptions{
			ClientName:       config.Name,
			Synthesis:        syn,
			BaseURL:          base,
			BaseURLExplicit:  explicit,
			Credentials:      cfg.SecurityCredentials,
			HTTPClient:       deps.HTTPClient,
			Headers:          deps.Headers,
			MaxResponseBytes: cfg.MaxResponseBytes,
			Logger:           deps.Logger,
		})
		if err != nil {
			return nil, permanent(err)
		}
		return srv, nil
	}
}

func permanent(err error) error {
	return fmt.Errorf("%w: %w", schemas.ErrMCPOpenAPIConfigInvalid, err)
}
