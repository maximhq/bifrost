package mcptools

import (
	"context"
	"slices"
	"sync"
	"time"
)

func getHealthTool() Tool {
	return Tool{
		name:        "get_health",
		description: "Whether this deployment can reach its config, log and vector stores, per store: ok, error, not_configured or disabled. Same checks as GET /health. This is process health, not LLM-provider uptime.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "skip_pings": {"type": "boolean", "description": "Skip store pings even if they are enabled on the deployment."}
  }
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			skip, err := boolArg(args, "skip_pings")
			if err != nil {
				return nil, err
			}
			stores := []struct {
				name   string
				label  string
				pinger Pinger
			}{
				{"config_store", "config store", deps.ConfigPing},
				{"log_store", "log store", deps.LogsPing},
				{"vector_store", "vector store", deps.VectorPing},
			}
			// Per store, not one flag: "degraded" alone cannot say whether it
			// is the log store (no answers about traffic) or the vector store
			// (no semantic search) that is down.
			components := make(map[string]string, len(stores))
			if skip || deps.DisableDBPings {
				for _, store := range stores {
					components[store.name] = "disabled"
				}
				return map[string]any{"status": "ok", "components": components}, nil
			}
			reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			var (
				mu     sync.Mutex
				failed []string
				wg     sync.WaitGroup
			)
			for _, store := range stores {
				if store.pinger == nil {
					// Goroutines for earlier stores may already be writing.
					mu.Lock()
					components[store.name] = "not_configured"
					mu.Unlock()
					continue
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					state := "ok"
					if err := store.pinger.Ping(reqCtx); err != nil {
						state = "error"
					}
					mu.Lock()
					defer mu.Unlock()
					components[store.name] = state
					if state == "error" {
						failed = append(failed, store.label+" not available")
					}
				}()
			}
			wg.Wait()
			// status and errors read the same as GET /health.
			if len(failed) > 0 {
				slices.Sort(failed)
				return map[string]any{"status": "unavailable", "components": components, "errors": failed}, nil
			}
			return map[string]any{"status": "ok", "components": components}, nil
		},
	}
}

func getVersionTool() Tool {
	return Tool{
		name:        "get_version",
		description: "The Bifrost build version this process is running.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "include_unknown": {"type": "boolean", "description": "Return \"unknown\" when the process was not stamped with a version. Always returned anyway."}
  }
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			version := deps.Version
			if version == "" {
				version = "unknown"
			}
			return map[string]any{"version": version}, nil
		},
	}
}
