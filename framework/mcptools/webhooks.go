package mcptools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func listWebhooksTool() Tool {
	return Tool{
		name:        "list_webhooks",
		description: "Registered webhook endpoints (id, name, url, events, disabled). Never returns the signing secret.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  }
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			limit, err := listLimit(args)
			if err != nil {
				return nil, err
			}
			search, _, err := optionalStringArg(args, "search")
			if err != nil {
				return nil, err
			}
			rows, total, err := gov.GetWebhookEndpointsPaginated(ctx, configstore.WebhookEndpointsQueryParams{
				Limit: limit, Search: search,
			})
			if err != nil {
				return nil, fmt.Errorf("list webhooks failed: %w", err)
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				out = append(out, projectWebhook(&rows[i], false))
			}
			return map[string]any{"webhooks": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func describeWebhookTool() Tool {
	return Tool{
		name:        "describe_webhook",
		description: "One webhook endpoint. Never returns the signing secret.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "webhook_id": {"type": "string", "minLength": 1}
  },
  "required": ["webhook_id"]
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "webhook_id")
			if err != nil {
				return nil, err
			}
			row, err := gov.GetWebhookEndpointByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no webhook with id %q", id)
				}
				return nil, fmt.Errorf("webhook lookup failed: %w", err)
			}
			return projectWebhook(row, false), nil
		},
	}
}

func createWebhookTool() Tool {
	return Tool{
		name:        "create_webhook",
		description: "Register a webhook endpoint. events is a list such as async_job.completed, async_job.failed. Returns the signing secret once.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "url": {"type": "string", "minLength": 1},
    "events": {"type": "array", "minItems": 1, "items": {"type": "string"}},
    "include_response": {"type": "boolean"},
    "allow_private_network": {"type": "boolean"},
    "disabled": {"type": "boolean"}
  },
  "required": ["name", "url", "events"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "name")
			if err != nil {
				return nil, err
			}
			url, err := stringArg(args, "url")
			if err != nil {
				return nil, err
			}
			rawEvents, err := optionalStringSlice(args, "events")
			if err != nil {
				return nil, err
			}
			if len(rawEvents) == 0 {
				return nil, fmt.Errorf("events must list at least one event")
			}
			events := make([]tables.WebhookEvent, 0, len(rawEvents))
			for _, e := range rawEvents {
				event := tables.WebhookEvent(e)
				if !event.IsValid() {
					return nil, fmt.Errorf("unknown webhook event %q", e)
				}
				events = append(events, event)
			}
			includeResponse, err := boolArg(args, "include_response")
			if err != nil {
				return nil, err
			}
			allowPrivate, err := boolArg(args, "allow_private_network")
			if err != nil {
				return nil, err
			}
			disabled, err := boolArg(args, "disabled")
			if err != nil {
				return nil, err
			}
			endpoint := &tables.TableWebhookEndpoint{
				ID:                  uuid.NewString(),
				Name:                name,
				URL:                 url,
				Events:              events,
				IncludeResponse:     includeResponse,
				AllowPrivateNetwork: allowPrivate,
				Disabled:            disabled,
			}
			if err := endpoint.Validate(); err != nil {
				return nil, err
			}
			if err := gov.CreateWebhookEndpoint(ctx, endpoint); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					return nil, fmt.Errorf("a webhook named %q already exists", name)
				}
				return nil, fmt.Errorf("create webhook failed: %w", err)
			}
			out := projectWebhook(endpoint, true)
			out["note"] = "this is the only time the signing secret is returned; store it now"
			// The row is stored either way, so a reload failure is a warning on
			// a successful result: returned as an error, the handler would drop
			// the payload and the one-time secret with it.
			if reloader := requireReloader(deps); reloader != nil {
				if err := reloader.ReloadWebhookEndpoint(ctx, endpoint.ID); err != nil {
					out["warning"] = fmt.Sprintf("stored, but live reload failed so this process will not deliver to it until restart: %v", err)
				}
			}
			return out, nil
		},
	}
}

func updateWebhookTool() Tool {
	return Tool{
		name:        "update_webhook",
		description: "Change a webhook's name, url, events or flags. Does not rotate the signing secret.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "webhook_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "url": {"type": "string"},
    "events": {"type": "array", "minItems": 1, "items": {"type": "string"}},
    "include_response": {"type": "boolean"},
    "allow_private_network": {"type": "boolean"},
    "disabled": {"type": "boolean"}
  },
  "required": ["webhook_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "webhook_id")
			if err != nil {
				return nil, err
			}
			row, err := gov.GetWebhookEndpointByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no webhook with id %q", id)
				}
				return nil, fmt.Errorf("webhook lookup failed: %w", err)
			}
			if name, ok, err := optionalStringArg(args, "name"); err != nil {
				return nil, err
			} else if ok {
				row.Name = name
			}
			if url, ok, err := optionalStringArg(args, "url"); err != nil {
				return nil, err
			} else if ok {
				row.URL = url
			}
			if _, present := args["events"]; present {
				rawEvents, err := optionalStringSlice(args, "events")
				if err != nil {
					return nil, err
				}
				if len(rawEvents) == 0 {
					return nil, fmt.Errorf("events must list at least one event")
				}
				events := make([]tables.WebhookEvent, 0, len(rawEvents))
				for _, e := range rawEvents {
					event := tables.WebhookEvent(e)
					if !event.IsValid() {
						return nil, fmt.Errorf("unknown webhook event %q", e)
					}
					events = append(events, event)
				}
				row.Events = events
			}
			if flag, err := optionalBoolArg(args, "include_response"); err != nil {
				return nil, err
			} else if flag != nil {
				row.IncludeResponse = *flag
			}
			if flag, err := optionalBoolArg(args, "allow_private_network"); err != nil {
				return nil, err
			} else if flag != nil {
				row.AllowPrivateNetwork = *flag
			}
			if flag, err := optionalBoolArg(args, "disabled"); err != nil {
				return nil, err
			} else if flag != nil {
				row.Disabled = *flag
			}
			if err := row.Validate(); err != nil {
				return nil, err
			}
			if err := gov.UpdateWebhookEndpoint(ctx, row); err != nil {
				return nil, fmt.Errorf("update webhook failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				if err := reloader.ReloadWebhookEndpoint(ctx, row.ID); err != nil {
					return projectWebhook(row, false), fmt.Errorf("stored but live reload failed: %w", err)
				}
			}
			return projectWebhook(row, false), nil
		},
	}
}

func projectWebhook(row *tables.TableWebhookEndpoint, includeSecret bool) map[string]any {
	events := make([]string, 0, len(row.Events))
	for _, e := range row.Events {
		events = append(events, string(e))
	}
	out := map[string]any{
		"id":                    row.ID,
		"name":                  row.Name,
		"url":                   row.URL,
		"events":                events,
		"include_response":      row.IncludeResponse,
		"allow_private_network": row.AllowPrivateNetwork,
		"disabled":              row.Disabled,
	}
	if includeSecret && row.Secret != nil && row.Secret.GetValue() != "" {
		out["secret"] = row.Secret.GetValue()
	}
	return out
}
