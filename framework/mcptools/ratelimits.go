package mcptools

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func listRateLimitsTool() Tool {
	return Tool{
		name:        "list_rate_limits",
		description: "Configured rate limits (id and token/request caps). Does not say which team or key owns them — use describe_team / describe_virtual_key for that.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  }
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			limit, err := listLimit(args)
			if err != nil {
				return nil, err
			}
			rows, err := gov.GetRateLimits(ctx)
			if err != nil {
				return nil, fmt.Errorf("list rate limits failed: %w", err)
			}
			// No row filter in the store; a scoped caller sees its tenant's.
			if t, known, err := readTenant(ctx, false); err != nil {
				return nil, err
			} else if known {
				index, err := t.index(ctx, gov)
				if err != nil {
					return nil, err
				}
				rows = slices.DeleteFunc(rows, func(rl tables.TableRateLimit) bool { return !index.rateLimits[rl.ID] })
			}
			if len(rows) > limit {
				rows = rows[:limit]
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				item := rateLimitSummary(rows[i])
				item["id"] = rows[i].ID
				out = append(out, item)
			}
			return map[string]any{"rate_limits": out, "returned": len(out)}, nil
		},
	}
}

func describeRateLimitTool() Tool {
	return Tool{
		name:        "describe_rate_limit",
		description: "One rate limit: token and request caps, reset durations and current usage.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "rate_limit_id": {"type": "string", "minLength": 1}
  },
  "required": ["rate_limit_id"]
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "rate_limit_id")
			if err != nil {
				return nil, err
			}
			notFound := fmt.Errorf("no rate limit with id %q", id)
			row, err := gov.GetRateLimit(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, notFound
				}
				return nil, fmt.Errorf("rate limit lookup failed: %w", err)
			}
			if err := hiddenOutsideTenant(ctx, false, func(t tenant) (bool, error) {
				return t.ownsRateLimit(ctx, gov, id)
			}, notFound); err != nil {
				return nil, err
			}
			out := rateLimitSummary(*row)
			out["id"] = row.ID
			return out, nil
		},
	}
}
