package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/tidwall/gjson"
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
				out = append(out, projectWebhook(&rows[i]))
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
			return projectWebhook(row), nil
		},
	}
}

func createWebhookTool() Tool {
	return Tool{
		name: "create_webhook",
		description: "Register a webhook endpoint. events is a list such as async_job.completed, async_job.failed. Returns the signing secret once. " +
			webhookPrivateNetworkNote,
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "url": {"type": "string", "minLength": 1},
    "events": {"type": "array", "minItems": 1, "items": {"type": "string"}},
    "include_response": {"type": "boolean"},
    "disabled": {"type": "boolean"}
  },
  "required": ["name", "url", "events"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			for _, required := range []string{"name", "url"} {
				if _, err := stringArg(args, required); err != nil {
					return nil, err
				}
			}
			if _, present := args["events"]; !present {
				return nil, fmt.Errorf("events must list at least one event")
			}
			if err := refusePrivateNetworkArg(args); err != nil {
				return nil, err
			}
			body := adminBody{}.copySet(args, "name", "url", "events", "include_response", "allow_private_network", "disabled")
			response, err := callAdmin(ctx, deps, http.MethodPost, "/api/webhooks", body)
			if err != nil {
				return nil, err
			}
			var endpoint tables.TableWebhookEndpoint
			if err := decodeAdminField(response, "endpoint", &endpoint); err != nil {
				return nil, err
			}
			out := projectWebhook(&endpoint)
			if secret := gjson.GetBytes(response, "secret").String(); secret != "" {
				out["secret"] = secret
				out["note"] = "this is the only time the signing secret is returned; store it now"
			}
			return out, nil
		},
	}
}

func updateWebhookTool() Tool {
	return Tool{
		name: "update_webhook",
		description: "Change a webhook's name, url, events or flags. Does not rotate the signing secret. " +
			webhookPrivateNetworkNote + " For an endpoint that already allows it, every other field can still be changed here, but not its url.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "webhook_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "url": {"type": "string"},
    "events": {"type": "array", "minItems": 1, "items": {"type": "string"}},
    "include_response": {"type": "boolean"},
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
			path, err := adminPath("/api/webhooks/{id}", id)
			if err != nil {
				return nil, err
			}
			if err := refusePrivateNetworkArg(args); err != nil {
				return nil, err
			}
			row, err := gov.GetWebhookEndpointByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no webhook with id %q", id)
				}
				return nil, fmt.Errorf("webhook lookup failed: %w", err)
			}
			// The route takes the endpoint's whole desired state and clears
			// what is left out, so the body is the stored row with this patch
			// applied. That includes what this tool does not expose: delivery
			// tuning, and the custom headers, sent as the placeholders a read
			// returns - which the route swaps back for the stored values.
			body := adminBody{
				"name":                          row.Name,
				"url":                           row.URL,
				"events":                        row.Events,
				"include_response":              row.IncludeResponse,
				"allow_private_network":         row.AllowPrivateNetwork,
				"disabled":                      row.Disabled,
				"max_retries":                   row.MaxRetries,
				"retry_backoff_initial_seconds": row.RetryBackoffInitialSeconds,
				"retry_backoff_max_seconds":     row.RetryBackoffMaxSeconds,
				"attempt_timeout_seconds":       row.AttemptTimeoutSeconds,
				"max_response_payload_kbs":      row.MaxResponsePayloadKBs,
				"max_concurrent_deliveries":     row.MaxConcurrentDeliveries,
			}
			if len(row.Headers) > 0 {
				headers := make(map[string]*schemas.SecretVar, len(row.Headers))
				for name, value := range row.Headers {
					headers[name] = value.FullyRedacted()
				}
				body["headers"] = headers
			}
			// The route accepts a private-network endpoint without a genuine
			// admin session only while it keeps its stored URL.
			if url, ok, err := optionalStringArg(args, "url"); err != nil {
				return nil, err
			} else if ok && row.AllowPrivateNetwork && args["allow_private_network"] != false && url != row.URL {
				return nil, fmt.Errorf("webhook %q allows private-network delivery, and re-pointing such an endpoint needs an authenticated admin session, which a tool call is not; change its url in the dashboard with authentication enabled", id)
			}
			body.copySet(args, "name", "url", "events", "include_response", "allow_private_network", "disabled")
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			var endpoint tables.TableWebhookEndpoint
			if err := decodeAdmin(response, &endpoint); err != nil {
				return nil, err
			}
			return projectWebhook(&endpoint), nil
		},
	}
}

// webhookPrivateNetworkNote is what both webhook write tools say about
// private-network delivery, which the routes leave to an admin session.
const webhookPrivateNetworkNote = "An endpoint on a loopback or private-network address needs allow_private_network, which only an authenticated admin session can set (the webhook routes refuse it otherwise, and a tool call is not one): set it in the dashboard with authentication enabled."

// refusePrivateNetworkArg refuses allow_private_network, which the schema no
// longer offers, with the reason rather than letting the route 403 it. false
// turns private delivery off, which the route accepts from anyone, so it
// still goes through.
func refusePrivateNetworkArg(args map[string]any) error {
	if value, present := args["allow_private_network"]; present && value != false && value != nil {
		return fmt.Errorf("allow_private_network cannot be set from a tool call: the webhook routes only accept it from an authenticated admin session; set it in the dashboard with authentication enabled")
	}
	return nil
}

// projectWebhook never carries the signing secret: the row's JSON leaves it
// out, and the one response that shows it is create's.
func projectWebhook(row *tables.TableWebhookEndpoint) map[string]any {
	events := make([]string, 0, len(row.Events))
	for _, e := range row.Events {
		events = append(events, string(e))
	}
	return map[string]any{
		"id":                    row.ID,
		"name":                  row.Name,
		"url":                   row.URL,
		"events":                events,
		"include_response":      row.IncludeResponse,
		"allow_private_network": row.AllowPrivateNetwork,
		"disabled":              row.Disabled,
	}
}
