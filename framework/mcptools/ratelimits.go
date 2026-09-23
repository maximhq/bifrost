package mcptools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
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

func createRateLimitTool() Tool {
	return Tool{
		name:        "create_rate_limit",
		description: "Create a rate limit and attach it to a team, customer or virtual key. A max limit requires its matching reset_duration (forms like 1h, 1d, 1M).",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "owner_type": {"type": "string", "enum": ["team", "customer", "virtual_key"]},
    "owner_id": {"type": "string", "minLength": 1},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
  },
  "required": ["owner_type", "owner_id"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			ownerType, err := stringArg(args, "owner_type")
			if err != nil {
				return nil, err
			}
			ownerID, err := stringArg(args, "owner_id")
			if err != nil {
				return nil, err
			}
			if err := checkTenant(ctx, func(t tenant) error {
				switch ownerType {
				case "team":
					owned, err := t.ownsTeam(ctx, gov, ownerID)
					return requireOwned(owned, err, t, fmt.Sprintf("team %q", ownerID))
				case "customer":
					return requireOwned(t.ownsCustomer(ownerID), nil, t, fmt.Sprintf("customer %q", ownerID))
				case "virtual_key":
					owned, err := t.ownsVirtualKeyID(ctx, gov, ownerID)
					return requireOwned(owned, err, t, fmt.Sprintf("virtual key %q", ownerID))
				}
				return nil // attachRateLimit rejects an unknown owner_type
			}); err != nil {
				return nil, err
			}
			rl, err := rateLimitFromArgs(args)
			if err != nil {
				return nil, err
			}
			if rl.TokenMaxLimit == nil && rl.RequestMaxLimit == nil {
				return nil, fmt.Errorf("set token_max_limit and/or request_max_limit")
			}
			rl.ID = uuid.NewString()
			now := time.Now()
			rl.TokenLastReset = now
			rl.RequestLastReset = now
			if err := attachRateLimit(ctx, gov, ownerType, ownerID, rl); err != nil {
				return nil, err
			}
			reloadRateLimitOwner(ctx, deps, ownerType, ownerID)
			out := rateLimitSummary(*rl)
			out["id"] = rl.ID
			out["owner_type"] = ownerType
			out["owner_id"] = ownerID
			return out, nil
		},
	}
}

func updateRateLimitTool() Tool {
	return Tool{
		name:        "update_rate_limit",
		description: "Change token or request caps on an existing rate limit. Current usage is left alone.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "rate_limit_id": {"type": "string", "minLength": 1},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
  },
  "required": ["rate_limit_id"]
}`,
		noLogs:       true,
		mutating:     true,
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
			row, err := gov.GetRateLimit(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no rate limit with id %q", id)
				}
				return nil, fmt.Errorf("rate limit lookup failed: %w", err)
			}
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsRateLimit(ctx, gov, id)
				return requireOwned(owned, err, t, fmt.Sprintf("rate limit %q", id))
			}); err != nil {
				return nil, err
			}
			patch, err := rateLimitFromArgs(args)
			if err != nil {
				return nil, err
			}
			if patch.TokenMaxLimit != nil {
				row.TokenMaxLimit = patch.TokenMaxLimit
			}
			if patch.TokenResetDuration != nil {
				row.TokenResetDuration = patch.TokenResetDuration
			}
			if patch.RequestMaxLimit != nil {
				row.RequestMaxLimit = patch.RequestMaxLimit
			}
			if patch.RequestResetDuration != nil {
				row.RequestResetDuration = patch.RequestResetDuration
			}
			if err := gov.UpdateRateLimit(ctx, row); err != nil {
				return nil, fmt.Errorf("update rate limit failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				_ = reloader.ReloadRateLimit(ctx, row.ID)
			}
			out := rateLimitSummary(*row)
			out["id"] = row.ID
			return out, nil
		},
	}
}

func rateLimitFromArgs(args map[string]any) (*tables.TableRateLimit, error) {
	rl := &tables.TableRateLimit{}
	tokenMax, err := optionalInt64Arg(args, "token_max_limit")
	if err != nil {
		return nil, err
	}
	if tokenMax != nil {
		if *tokenMax <= 0 {
			return nil, fmt.Errorf("token_max_limit must be greater than 0")
		}
		rl.TokenMaxLimit = tokenMax
	}
	tokenReset, ok, err := optionalStringArg(args, "token_reset_duration")
	if err != nil {
		return nil, err
	}
	if ok {
		if _, err := tables.ParseDuration(tokenReset); err != nil {
			return nil, fmt.Errorf("invalid token_reset_duration %q (use forms like 1h, 1d, 1M)", tokenReset)
		}
		rl.TokenResetDuration = &tokenReset
	}
	reqMax, err := optionalInt64Arg(args, "request_max_limit")
	if err != nil {
		return nil, err
	}
	if reqMax != nil {
		if *reqMax <= 0 {
			return nil, fmt.Errorf("request_max_limit must be greater than 0")
		}
		rl.RequestMaxLimit = reqMax
	}
	reqReset, ok, err := optionalStringArg(args, "request_reset_duration")
	if err != nil {
		return nil, err
	}
	if ok {
		if _, err := tables.ParseDuration(reqReset); err != nil {
			return nil, fmt.Errorf("invalid request_reset_duration %q (use forms like 1h, 1d, 1M)", reqReset)
		}
		rl.RequestResetDuration = &reqReset
	}
	if rl.TokenMaxLimit != nil && rl.TokenResetDuration == nil {
		return nil, fmt.Errorf("token_reset_duration is required when token_max_limit is set")
	}
	if rl.RequestMaxLimit != nil && rl.RequestResetDuration == nil {
		return nil, fmt.Errorf("request_reset_duration is required when request_max_limit is set")
	}
	return rl, nil
}

// attachRateLimit validates and loads the owner first, then creates the rate
// limit and points the owner at it in one transaction, so a failed owner update
// cannot leave an orphaned rate limit row behind.
func attachRateLimit(ctx context.Context, gov GovernanceReader, ownerType, ownerID string, rl *tables.TableRateLimit) error {
	var attach func(tx *gorm.DB) error
	switch ownerType {
	case "team":
		team, err := gov.GetTeam(ctx, ownerID)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return fmt.Errorf("no team with id %q", ownerID)
			}
			return fmt.Errorf("team lookup failed: %w", err)
		}
		if err := refuseExistingRateLimit("team", ownerID, team.RateLimitID); err != nil {
			return err
		}
		attach = func(tx *gorm.DB) error {
			team.RateLimitID = &rl.ID
			if err := gov.UpdateTeam(ctx, team, tx); err != nil {
				return fmt.Errorf("attach rate limit to team failed: %w", err)
			}
			return nil
		}
	case "customer":
		customer, err := gov.GetCustomer(ctx, ownerID)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return fmt.Errorf("no customer with id %q", ownerID)
			}
			return fmt.Errorf("customer lookup failed: %w", err)
		}
		if err := refuseExistingRateLimit("customer", ownerID, customer.RateLimitID); err != nil {
			return err
		}
		attach = func(tx *gorm.DB) error {
			customer.RateLimitID = &rl.ID
			if err := gov.UpdateCustomer(ctx, customer, tx); err != nil {
				return fmt.Errorf("attach rate limit to customer failed: %w", err)
			}
			return nil
		}
	case "virtual_key":
		vk, err := gov.GetVirtualKey(ctx, ownerID)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return fmt.Errorf("no virtual key with id %q", ownerID)
			}
			return fmt.Errorf("virtual key lookup failed: %w", err)
		}
		if err := refuseExistingRateLimit("virtual key", ownerID, vk.RateLimitID); err != nil {
			return err
		}
		attach = func(tx *gorm.DB) error {
			vk.RateLimitID = &rl.ID
			if err := gov.UpdateVirtualKey(ctx, vk, tx); err != nil {
				return fmt.Errorf("attach rate limit to virtual key failed: %w", err)
			}
			return nil
		}
	default:
		return fmt.Errorf("owner_type must be team, customer or virtual_key")
	}
	return gov.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		if err := gov.CreateRateLimit(ctx, rl, tx); err != nil {
			return fmt.Errorf("create rate limit failed: %w", err)
		}
		return attach(tx)
	})
}

// refuseExistingRateLimit stops create_rate_limit from re-pointing an owner
// that already has a limit: the old row would be orphaned, and changing the
// limit is what update_rate_limit is for.
func refuseExistingRateLimit(ownerKind, ownerID string, existing *string) error {
	if existing == nil || *existing == "" {
		return nil
	}
	return fmt.Errorf("%s %q already has rate limit %q; change it with update_rate_limit", ownerKind, ownerID, *existing)
}

func reloadRateLimitOwner(ctx context.Context, deps *Deps, ownerType, ownerID string) {
	reloader := requireReloader(deps)
	if reloader == nil {
		return
	}
	switch ownerType {
	case "team":
		_, _ = reloader.ReloadTeam(ctx, ownerID)
	case "customer":
		_, _ = reloader.ReloadCustomer(ctx, ownerID)
	case "virtual_key":
		_, _ = reloader.ReloadVirtualKey(ctx, ownerID)
	}
}
