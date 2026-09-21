package mcptools

import (
	"context"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/framework/featureflags"
)

func listFeatureFlagsTool() Tool {
	return Tool{
		name:        "list_feature_flags",
		description: "Feature flags and whether they are on, plus which layer set them (default, db, file). File-locked flags cannot be toggled.",
		schemaJSON: `{
  "type": "object",
  "properties": {}
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.FeatureFlags != nil {
				flags := deps.FeatureFlags.List()
				out := make([]map[string]any, 0, len(flags))
				for _, flag := range flags {
					out = append(out, projectFeatureFlag(flag))
				}
				return map[string]any{"flags": out, "returned": len(out)}, nil
			}
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			rows, err := gov.ListFeatureFlags(ctx)
			if err != nil {
				return nil, fmt.Errorf("list feature flags failed: %w", err)
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				out = append(out, map[string]any{
					"id":      row.ID,
					"enabled": row.Enabled,
					"source":  "db",
				})
			}
			return map[string]any{
				"flags":    out,
				"returned": len(out),
				"note":     "in-memory flag store is unavailable; these are persisted overrides only",
			}, nil
		},
	}
}

func updateFeatureFlagTool() Tool {
	return Tool{
		name:        "update_feature_flag",
		description: "Turn a registered feature flag on or off. File-locked flags (config.json / Helm) cannot be changed.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "id": {"type": "string", "minLength": 1},
    "enabled": {"type": "boolean"}
  },
  "required": ["id", "enabled"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			id, err := stringArg(args, "id")
			if err != nil {
				return nil, err
			}
			enabled, err := boolArg(args, "enabled")
			if err != nil {
				return nil, err
			}
			if _, present := args["enabled"]; !present {
				return nil, fmt.Errorf("enabled is required")
			}
			path, err := adminPath("/api/feature-flags/{id}", id)
			if err != nil {
				return nil, err
			}
			response, err := callAdmin(ctx, deps, http.MethodPut, path, adminBody{"enabled": enabled})
			if err != nil {
				return nil, err
			}
			var status featureflags.FlagStatus
			if err := decodeAdmin(response, &status); err != nil {
				return nil, err
			}
			return projectFeatureFlag(status), nil
		},
	}
}

func projectFeatureFlag(flag featureflags.FlagStatus) map[string]any {
	return map[string]any{
		"id":              flag.ID,
		"display_name":    flag.DisplayName,
		"description":     flag.Description,
		"enabled":         flag.Enabled,
		"default":         flag.Default,
		"source":          string(flag.Source),
		"locked":          flag.Locked,
		"registered":      flag.Registered,
		"enterprise_only": flag.EnterpriseOnly,
	}
}
