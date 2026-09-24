package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/client"
	"github.com/maximhq/bifrost/cli/internal/output"
)

// resourceAction describes one ergonomic command backed by a gateway operation.
type resourceAction struct {
	Method      string
	Path        string
	Arguments   []string
	Auth        client.AuthMode
	AcceptsBody bool
	NeedsBody   bool
	RawBody     bool
	ContentType string
	Destructive bool
}

// resourceDescriptor groups the actions exposed for one CLI resource.
type resourceDescriptor struct {
	Description string
	Actions     map[string]resourceAction
}

// resourceRegistry maps first-class CLI names to canonical Bifrost endpoints.
var resourceRegistry = map[string]resourceDescriptor{
	"models": {
		Description: "discover gateway and catalog models",
		Actions: map[string]resourceAction{
			"list":           {Method: http.MethodGet, Path: "/v1/models", Auth: client.AuthInference},
			"info":           {Method: http.MethodGet, Path: "/api/models/details", Auth: client.AuthManagement},
			"details":        {Method: http.MethodGet, Path: "/api/models/details", Auth: client.AuthManagement},
			"parameters":     {Method: http.MethodGet, Path: "/api/models/parameters", Auth: client.AuthManagement},
			"base":           {Method: http.MethodGet, Path: "/api/models/base", Auth: client.AuthManagement},
			"catalog-update": {Method: http.MethodPut, Path: "/api/models/catalog", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		},
	},
	"model-groups": {
		Description: "list routable Bifrost model IDs",
		Actions:     readOnlyListActionWithAuth("/v1/models", client.AuthInference),
	},
	"responses": {
		Description: "create and manage stored Responses API results",
		Actions: map[string]resourceAction{
			"create":       {Method: http.MethodPost, Path: "/v1/responses", Auth: client.AuthInference, AcceptsBody: true, NeedsBody: true},
			"get":          {Method: http.MethodGet, Path: "/v1/responses/{response_id}", Arguments: []string{"response-id"}, Auth: client.AuthInference},
			"delete":       {Method: http.MethodDelete, Path: "/v1/responses/{response_id}", Arguments: []string{"response-id"}, Auth: client.AuthInference, Destructive: true},
			"cancel":       {Method: http.MethodPost, Path: "/v1/responses/{response_id}/cancel", Arguments: []string{"response-id"}, Auth: client.AuthInference, Destructive: true},
			"input-items":  {Method: http.MethodGet, Path: "/v1/responses/{response_id}/input_items", Arguments: []string{"response-id"}, Auth: client.AuthInference},
			"count-tokens": {Method: http.MethodPost, Path: "/v1/responses/input_tokens", Auth: client.AuthInference, AcceptsBody: true, NeedsBody: true},
			"compact":      {Method: http.MethodPost, Path: "/v1/responses/compact", Auth: client.AuthInference, AcceptsBody: true, NeedsBody: true},
		},
	},
	"videos": {
		Description: "create and manage video generations",
		Actions: map[string]resourceAction{
			"list":     {Method: http.MethodGet, Path: "/v1/videos", Auth: client.AuthInference},
			"create":   {Method: http.MethodPost, Path: "/v1/videos", Auth: client.AuthInference, AcceptsBody: true, NeedsBody: true},
			"edit":     {Method: http.MethodPost, Path: "/v1/videos/edits", Auth: client.AuthInference, AcceptsBody: true, NeedsBody: true},
			"get":      {Method: http.MethodGet, Path: "/v1/videos/{video_id}", Arguments: []string{"video-id"}, Auth: client.AuthInference},
			"download": {Method: http.MethodGet, Path: "/v1/videos/{video_id}/content", Arguments: []string{"video-id"}, Auth: client.AuthInference},
			"delete":   {Method: http.MethodDelete, Path: "/v1/videos/{video_id}", Arguments: []string{"video-id"}, Auth: client.AuthInference, Destructive: true},
			"remix":    {Method: http.MethodPost, Path: "/v1/videos/{video_id}/remix", Arguments: []string{"video-id"}, Auth: client.AuthInference, AcceptsBody: true, NeedsBody: true},
		},
	},
	"providers": {
		Description: "manage configured providers",
		Actions: withActions(crudActions("/api/providers", "provider", client.AuthManagement), map[string]resourceAction{
			"refresh-models": {Method: http.MethodPost, Path: "/api/providers/{provider}/refresh-models", Arguments: []string{"provider"}, Auth: client.AuthManagement},
		}),
	},
	"credentials": {
		Description: "manage provider credentials",
		Actions: map[string]resourceAction{
			"list":           {Method: http.MethodGet, Path: "/api/providers/{provider}/keys", Arguments: []string{"provider"}, Auth: client.AuthManagement},
			"get":            {Method: http.MethodGet, Path: "/api/providers/{provider}/keys/{key_id}", Arguments: []string{"provider", "key-id"}, Auth: client.AuthManagement},
			"create":         {Method: http.MethodPost, Path: "/api/providers/{provider}/keys", Arguments: []string{"provider"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
			"update":         {Method: http.MethodPut, Path: "/api/providers/{provider}/keys/{key_id}", Arguments: []string{"provider", "key-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
			"delete":         {Method: http.MethodDelete, Path: "/api/providers/{provider}/keys/{key_id}", Arguments: []string{"provider", "key-id"}, Auth: client.AuthManagement, Destructive: true},
			"refresh-models": {Method: http.MethodPost, Path: "/api/providers/{provider}/keys/{key_id}/refresh-models", Arguments: []string{"provider", "key-id"}, Auth: client.AuthManagement},
		},
	},
	"virtual-keys": {
		Description: "manage inference virtual keys",
		Actions: withActions(crudActions("/api/governance/virtual-keys", "vk_id", client.AuthManagement), map[string]resourceAction{
			"assigned":               {Method: http.MethodGet, Path: "/api/agent/virtual-keys", Auth: client.AuthAgent},
			"generate":               {Method: http.MethodPost, Path: "/api/governance/virtual-keys", Auth: client.AuthManagement, AcceptsBody: true},
			"info":                   {Method: http.MethodGet, Path: "/api/governance/virtual-keys/{vk_id}", Arguments: []string{"vk-id"}, Auth: client.AuthManagement},
			"rotate":                 {Method: http.MethodPost, Path: "/api/governance/virtual-keys/{vk_id}/rotate", Arguments: []string{"vk-id"}, Auth: client.AuthManagement, Destructive: true},
			"rotate-all":             {Method: http.MethodPost, Path: "/api/governance/virtual-keys/rotate", Auth: client.AuthManagement, AcceptsBody: true, Destructive: true},
			"quota":                  {Method: http.MethodGet, Path: "/api/governance/virtual-keys/quota", Auth: client.AuthInference},
			"users":                  {Method: http.MethodGet, Path: "/api/governance/virtual-keys/{vk_id}/users", Arguments: []string{"vk-id"}, Auth: client.AuthManagement},
			"attach-users":           {Method: http.MethodPost, Path: "/api/governance/virtual-keys/{vk_id}/users", Arguments: []string{"vk-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
			"detach-user":            {Method: http.MethodDelete, Path: "/api/governance/virtual-keys/{vk_id}/users/{user_id}", Arguments: []string{"vk-id", "user-id"}, Auth: client.AuthManagement, Destructive: true},
			"reveal":                 {Method: http.MethodGet, Path: "/api/governance/virtual-keys/{vk_id}/reveal", Arguments: []string{"vk-id"}, Auth: client.AuthManagement},
			"copy":                   {Method: http.MethodGet, Path: "/api/governance/virtual-keys/{vk_id}/copy", Arguments: []string{"vk-id"}, Auth: client.AuthManagement},
			"set-budget-override":    {Method: http.MethodPut, Path: "/api/governance/virtual-keys/{vk_id}/budgets/{budget_id}/override", Arguments: []string{"vk-id", "budget-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
			"delete-budget-override": {Method: http.MethodDelete, Path: "/api/governance/virtual-keys/{vk_id}/budgets/{budget_id}/override", Arguments: []string{"vk-id", "budget-id"}, Auth: client.AuthManagement, Destructive: true},
		}),
	},
	"teams": {Description: "manage governance teams", Actions: withActions(crudActions("/api/governance/teams", "team_id", client.AuthManagement), map[string]resourceAction{
		"members":         {Method: http.MethodGet, Path: "/api/governance/teams/{team_id}/members", Arguments: []string{"team-id"}, Auth: client.AuthManagement},
		"add-member":      {Method: http.MethodPost, Path: "/api/governance/teams/{team_id}/members", Arguments: []string{"team-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"remove-member":   {Method: http.MethodDelete, Path: "/api/governance/teams/{team_id}/members/{user_id}", Arguments: []string{"team-id", "user-id"}, Auth: client.AuthManagement, Destructive: true},
		"customers":       {Method: http.MethodGet, Path: "/api/governance/teams/{team_id}/customers", Arguments: []string{"team-id"}, Auth: client.AuthManagement},
		"attach-customer": {Method: http.MethodPost, Path: "/api/governance/teams/{team_id}/customers", Arguments: []string{"team-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"detach-customer": {Method: http.MethodDelete, Path: "/api/governance/teams/{team_id}/customers/{customer_id}", Arguments: []string{"team-id", "customer-id"}, Auth: client.AuthManagement, Destructive: true},
	})},
	"customers": {Description: "manage governance customers", Actions: withActions(crudActions("/api/governance/customers", "customer_id", client.AuthManagement), map[string]resourceAction{
		"teams":          {Method: http.MethodGet, Path: "/api/governance/customers/{customer_id}/teams", Arguments: []string{"customer-id"}, Auth: client.AuthManagement},
		"business-units": {Method: http.MethodGet, Path: "/api/governance/customers/{customer_id}/business-units", Arguments: []string{"customer-id"}, Auth: client.AuthManagement},
	})},
	"users": {Description: "manage Enterprise users", Actions: map[string]resourceAction{
		"list":           {Method: http.MethodGet, Path: "/api/governance/users", Auth: client.AuthManagement},
		"get":            {Method: http.MethodGet, Path: "/api/governance/users/{user_id}", Arguments: []string{"user-id"}, Auth: client.AuthManagement},
		"get-by-email":   {Method: http.MethodGet, Path: "/api/governance/users/email/{email}", Arguments: []string{"email"}, Auth: client.AuthManagement},
		"create":         {Method: http.MethodPost, Path: "/api/governance/users", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete":         {Method: http.MethodDelete, Path: "/api/governance/users/{user_id}", Arguments: []string{"user-id"}, Auth: client.AuthManagement, Destructive: true},
		"teams":          {Method: http.MethodGet, Path: "/api/governance/users/{user_id}/teams", Arguments: []string{"user-id"}, Auth: client.AuthManagement},
		"set-teams":      {Method: http.MethodPut, Path: "/api/governance/users/{user_id}/teams", Arguments: []string{"user-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"business-units": {Method: http.MethodGet, Path: "/api/governance/users/{user_id}/business-units", Arguments: []string{"user-id"}, Auth: client.AuthManagement},
		"set-role":       {Method: http.MethodPut, Path: "/api/governance/users/{user_id}/role", Arguments: []string{"user-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"permissions":    {Method: http.MethodGet, Path: "/api/governance/users/me/permissions", Auth: client.AuthManagement},
		"vk-policy":      {Method: http.MethodGet, Path: "/api/governance/users/me/vk-creation-policy", Auth: client.AuthManagement},
	}},
	"management-keys": {Description: "manage Enterprise administrative API keys", Actions: crudActions("/api/api-keys", "id", client.AuthManagement)},
	"model-configs":   {Description: "manage governance model configurations", Actions: crudActions("/api/governance/model-configs", "mc_id", client.AuthManagement)},
	"pricing-overrides": {Description: "manage governance pricing overrides", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/governance/pricing-overrides", Auth: client.AuthManagement},
		"create": {Method: http.MethodPost, Path: "/api/governance/pricing-overrides", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"update": {Method: http.MethodPut, Path: "/api/governance/pricing-overrides/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete": {Method: http.MethodDelete, Path: "/api/governance/pricing-overrides/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
	}},
	"routing-rules": {Description: "manage canonical model routing rules", Actions: crudActions("/api/routing/rules", "rule_id", client.AuthManagement)},
	"governance-providers": {Description: "manage provider-level governance", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/governance/providers", Auth: client.AuthManagement},
		"update": {Method: http.MethodPut, Path: "/api/governance/providers/{provider_name}", Arguments: []string{"provider-name"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete": {Method: http.MethodDelete, Path: "/api/governance/providers/{provider_name}", Arguments: []string{"provider-name"}, Auth: client.AuthManagement, Destructive: true},
	}},
	"budgets":     {Description: "inspect effective governance budgets", Actions: readOnlyListAction("/api/governance/budgets")},
	"rate-limits": {Description: "inspect effective governance rate limits", Actions: readOnlyListAction("/api/governance/rate-limits")},
	"business-units": {Description: "manage Enterprise business units", Actions: withActions(crudActions("/api/governance/business-units", "business_unit_id", client.AuthManagement), map[string]resourceAction{
		"users":             {Method: http.MethodGet, Path: "/api/governance/business-units/{business_unit_id}/users", Arguments: []string{"business-unit-id"}, Auth: client.AuthManagement},
		"add-user":          {Method: http.MethodPost, Path: "/api/governance/business-units/{business_unit_id}/users", Arguments: []string{"business-unit-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"remove-user":       {Method: http.MethodDelete, Path: "/api/governance/business-units/{business_unit_id}/users/{user_id}", Arguments: []string{"business-unit-id", "user-id"}, Auth: client.AuthManagement, Destructive: true},
		"customers":         {Method: http.MethodGet, Path: "/api/governance/business-units/{business_unit_id}/customers", Arguments: []string{"business-unit-id"}, Auth: client.AuthManagement},
		"attach-customer":   {Method: http.MethodPost, Path: "/api/governance/business-units/{business_unit_id}/customers", Arguments: []string{"business-unit-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"detach-customer":   {Method: http.MethodDelete, Path: "/api/governance/business-units/{business_unit_id}/customers/{customer_id}", Arguments: []string{"business-unit-id", "customer-id"}, Auth: client.AuthManagement, Destructive: true},
		"create-governance": {Method: http.MethodPost, Path: "/api/governance/business-units/{business_unit_id}/governance", Arguments: []string{"business-unit-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"update-governance": {Method: http.MethodPut, Path: "/api/governance/business-units/{business_unit_id}/governance", Arguments: []string{"business-unit-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete-governance": {Method: http.MethodDelete, Path: "/api/governance/business-units/{business_unit_id}/governance", Arguments: []string{"business-unit-id"}, Auth: client.AuthManagement, Destructive: true},
	})},
	"projects": {Description: "manage Enterprise governance projects", Actions: withActions(crudActions("/api/governance/projects", "project_id", client.AuthManagement), map[string]resourceAction{
		"members":       {Method: http.MethodGet, Path: "/api/governance/projects/{project_id}/members", Arguments: []string{"project-id"}, Auth: client.AuthManagement},
		"add-members":   {Method: http.MethodPost, Path: "/api/governance/projects/{project_id}/members", Arguments: []string{"project-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"update-member": {Method: http.MethodPut, Path: "/api/governance/projects/{project_id}/members/{member_id}", Arguments: []string{"project-id", "member-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"remove-member": {Method: http.MethodDelete, Path: "/api/governance/projects/{project_id}/members/{member_id}", Arguments: []string{"project-id", "member-id"}, Auth: client.AuthManagement, Destructive: true},
	})},
	"roles": {Description: "manage Enterprise RBAC roles", Actions: withActions(crudActions("/api/governance/rbac/roles", "role_id", client.AuthManagement), map[string]resourceAction{
		"permissions":     {Method: http.MethodGet, Path: "/api/governance/rbac/roles/{role_id}/permissions", Arguments: []string{"role-id"}, Auth: client.AuthManagement},
		"set-permissions": {Method: http.MethodPut, Path: "/api/governance/rbac/roles/{role_id}/permissions", Arguments: []string{"role-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	})},
	"rbac": {Description: "inspect Enterprise RBAC metadata", Actions: map[string]resourceAction{
		"resources":   {Method: http.MethodGet, Path: "/api/governance/rbac/resources", Auth: client.AuthManagement},
		"operations":  {Method: http.MethodGet, Path: "/api/governance/rbac/operations", Auth: client.AuthManagement},
		"permissions": {Method: http.MethodGet, Path: "/api/governance/rbac/permissions", Auth: client.AuthManagement},
	}},
	"access-profiles": {Description: "manage Enterprise access profiles", Actions: withActions(crudActions("/api/governance/access-profiles", "profile_id", client.AuthManagement), map[string]resourceAction{
		"attach-roles": {Method: http.MethodPost, Path: "/api/governance/access-profiles/{profile_id}/roles", Arguments: []string{"profile-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"detach-role":  {Method: http.MethodDelete, Path: "/api/governance/access-profiles/{profile_id}/roles/{role_id}", Arguments: []string{"profile-id", "role-id"}, Auth: client.AuthManagement, Destructive: true},
		"propagate":    {Method: http.MethodPost, Path: "/api/governance/access-profiles/{profile_id}/propagate", Arguments: []string{"profile-id"}, Auth: client.AuthManagement, AcceptsBody: true},
		"clone":        {Method: http.MethodPost, Path: "/api/governance/access-profiles/{profile_id}/clone", Arguments: []string{"profile-id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"activate":     {Method: http.MethodPut, Path: "/api/governance/access-profiles/{profile_id}/activate", Arguments: []string{"profile-id"}, Auth: client.AuthManagement},
		"deactivate":   {Method: http.MethodPut, Path: "/api/governance/access-profiles/{profile_id}/deactivate", Arguments: []string{"profile-id"}, Auth: client.AuthManagement},
		"versions":     {Method: http.MethodGet, Path: "/api/governance/access-profiles/{profile_id}/versions", Arguments: []string{"profile-id"}, Auth: client.AuthManagement},
		"version":      {Method: http.MethodGet, Path: "/api/governance/access-profiles/{profile_id}/versions/{version}", Arguments: []string{"profile-id", "version"}, Auth: client.AuthManagement},
		"audit-logs":   {Method: http.MethodGet, Path: "/api/governance/access-profiles/{profile_id}/audit-logs", Arguments: []string{"profile-id"}, Auth: client.AuthManagement},
	})},
	"audit-logs": {Description: "inspect Enterprise administrative audit logs", Actions: map[string]resourceAction{
		"list":        {Method: http.MethodGet, Path: "/api/governance/audit-logs", Auth: client.AuthManagement},
		"get":         {Method: http.MethodGet, Path: "/api/governance/audit-logs/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"filter-data": {Method: http.MethodGet, Path: "/api/governance/audit-logs/filterdata", Auth: client.AuthManagement},
		"export":      {Method: http.MethodGet, Path: "/api/governance/audit-logs/export", Auth: client.AuthManagement},
		"verify":      {Method: http.MethodGet, Path: "/api/governance/audit-logs/{id}/verify", Arguments: []string{"id"}, Auth: client.AuthManagement},
	}},
	"plugins": {Description: "manage gateway plugins", Actions: withActions(crudActions("/api/plugins", "name", client.AuthManagement), map[string]resourceAction{
		"builtins": {Method: http.MethodGet, Path: "/api/plugins/builtins", Auth: client.AuthManagement},
		"loaded":   {Method: http.MethodGet, Path: "/api/plugins/loaded", Auth: client.AuthManagement},
	})},
	"skills": {Description: "manage gateway skills", Actions: withActions(crudActions("/api/skills", "id", client.AuthManagement), map[string]resourceAction{
		"versions":         {Method: http.MethodGet, Path: "/api/skills/{id}/versions", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"shift-version":    {Method: http.MethodPost, Path: "/api/skills/{id}/shift-version", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"all-version":      {Method: http.MethodGet, Path: "/api/skills/all/version", Auth: client.AuthManagement},
		"bump-all-version": {Method: http.MethodPut, Path: "/api/skills/all/version", Auth: client.AuthManagement},
		"cleanup-files":    {Method: http.MethodDelete, Path: "/api/skills/files/orphans", Auth: client.AuthManagement, Destructive: true},
	})},
	"webhooks": {Description: "manage outbound webhooks", Actions: withActions(crudActions("/api/webhooks", "id", client.AuthManagement), map[string]resourceAction{
		"rotate-secret":     {Method: http.MethodPost, Path: "/api/webhooks/{id}/rotate-secret", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
		"test":              {Method: http.MethodPost, Path: "/api/webhooks/{id}/test", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"deliveries":        {Method: http.MethodGet, Path: "/api/webhooks/{id}/deliveries", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"search-deliveries": {Method: http.MethodGet, Path: "/api/webhooks/deliveries", Auth: client.AuthManagement},
		"redeliver":         {Method: http.MethodPost, Path: "/api/webhooks/deliveries/{id}/redeliver", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
	})},
	"prompts": {Description: "manage prompt repository prompts", Actions: withActions(crudActions("/api/prompt-repo/prompts", "id", client.AuthManagement), map[string]resourceAction{
		"versions":       {Method: http.MethodGet, Path: "/api/prompt-repo/prompts/{id}/versions", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"create-version": {Method: http.MethodPost, Path: "/api/prompt-repo/prompts/{id}/versions", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"sessions":       {Method: http.MethodGet, Path: "/api/prompt-repo/prompts/{id}/sessions", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"create-session": {Method: http.MethodPost, Path: "/api/prompt-repo/prompts/{id}/sessions", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	})},
	"prompt-folders": {Description: "manage prompt repository folders", Actions: crudActions("/api/prompt-repo/folders", "id", client.AuthManagement)},
	"prompt-versions": {Description: "inspect prompt repository versions", Actions: map[string]resourceAction{
		"get":    {Method: http.MethodGet, Path: "/api/prompt-repo/versions/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"delete": {Method: http.MethodDelete, Path: "/api/prompt-repo/versions/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
	}},
	"prompt-sessions": {Description: "manage prompt editing sessions", Actions: map[string]resourceAction{
		"get":    {Method: http.MethodGet, Path: "/api/prompt-repo/sessions/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"update": {Method: http.MethodPut, Path: "/api/prompt-repo/sessions/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete": {Method: http.MethodDelete, Path: "/api/prompt-repo/sessions/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
		"rename": {Method: http.MethodPut, Path: "/api/prompt-repo/sessions/{id}/rename", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"commit": {Method: http.MethodPost, Path: "/api/prompt-repo/sessions/{id}/commit", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true},
	}},
	"prompt-deployments": {Description: "manage Enterprise prompt deployment strategies", Actions: crudActions("/api/prompt-repo/deployments", "id", client.AuthManagement)},
	"logs": {Description: "query and administer inference logs", Actions: map[string]resourceAction{
		"list":               {Method: http.MethodGet, Path: "/api/logs", Auth: client.AuthManagement},
		"get":                {Method: http.MethodGet, Path: "/api/logs/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"stats":              {Method: http.MethodGet, Path: "/api/logs/stats", Auth: client.AuthManagement},
		"histogram":          {Method: http.MethodGet, Path: "/api/logs/histogram", Auth: client.AuthManagement},
		"rankings":           {Method: http.MethodGet, Path: "/api/logs/rankings", Auth: client.AuthManagement},
		"dashboard":          {Method: http.MethodGet, Path: "/api/logs/dashboard", Auth: client.AuthManagement},
		"filter-data":        {Method: http.MethodGet, Path: "/api/logs/filterdata", Auth: client.AuthManagement},
		"dropped":            {Method: http.MethodGet, Path: "/api/logs/dropped", Auth: client.AuthManagement},
		"session":            {Method: http.MethodGet, Path: "/api/logs/sessions/{session_id}", Arguments: []string{"session-id"}, Auth: client.AuthManagement},
		"session-summary":    {Method: http.MethodGet, Path: "/api/logs/sessions/{session_id}/summary", Arguments: []string{"session-id"}, Auth: client.AuthManagement},
		"recalculate-cost":   {Method: http.MethodPost, Path: "/api/logs/recalculate-cost", Auth: client.AuthManagement, AcceptsBody: true},
		"recalculate-status": {Method: http.MethodGet, Path: "/api/logs/recalculate-cost/status", Auth: client.AuthManagement},
		"recalculate-cancel": {Method: http.MethodPost, Path: "/api/logs/recalculate-cost/cancel", Auth: client.AuthManagement, Destructive: true},
		"delete":             {Method: http.MethodDelete, Path: "/api/logs", Auth: client.AuthManagement, AcceptsBody: true, Destructive: true},
	}},
	"mcp-logs": {Description: "query and administer MCP logs", Actions: map[string]resourceAction{
		"list":        {Method: http.MethodGet, Path: "/api/mcp-logs", Auth: client.AuthManagement},
		"get":         {Method: http.MethodGet, Path: "/api/mcp-logs/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"stats":       {Method: http.MethodGet, Path: "/api/mcp-logs/stats", Auth: client.AuthManagement},
		"histogram":   {Method: http.MethodGet, Path: "/api/mcp-logs/histogram", Auth: client.AuthManagement},
		"filter-data": {Method: http.MethodGet, Path: "/api/mcp-logs/filterdata", Auth: client.AuthManagement},
		"delete":      {Method: http.MethodDelete, Path: "/api/mcp-logs", Auth: client.AuthManagement, AcceptsBody: true, Destructive: true},
	}},
	"mcp-clients": {Description: "manage MCP client connections", Actions: map[string]resourceAction{
		"list":                  {Method: http.MethodGet, Path: "/api/mcp/clients", Auth: client.AuthManagement},
		"create":                {Method: http.MethodPost, Path: "/api/mcp/client", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"update":                {Method: http.MethodPut, Path: "/api/mcp/client/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete":                {Method: http.MethodDelete, Path: "/api/mcp/client/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
		"reconnect":             {Method: http.MethodPost, Path: "/api/mcp/client/{id}/reconnect", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"reauthorize":           {Method: http.MethodPost, Path: "/api/mcp/client/{id}/reauthorize", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"complete-oauth":        {Method: http.MethodPost, Path: "/api/mcp/client/{id}/complete-oauth", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"initiate-verification": {Method: http.MethodPost, Path: "/api/mcp/client/{id}/initiate-verification", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true},
		"verify-headers":        {Method: http.MethodPost, Path: "/api/mcp/client/{id}/verify-headers", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"verify-exchange":       {Method: http.MethodPost, Path: "/api/mcp/client/{id}/verify-exchange", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	}},
	"mcp-library": {Description: "manage the MCP server library", Actions: map[string]resourceAction{
		"list":        {Method: http.MethodGet, Path: "/api/mcp/library", Auth: client.AuthManagement},
		"filter-data": {Method: http.MethodGet, Path: "/api/mcp/library/filterdata", Auth: client.AuthManagement},
		"create":      {Method: http.MethodPost, Path: "/api/mcp/library", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete":      {Method: http.MethodDelete, Path: "/api/mcp/library/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
		"force-sync":  {Method: http.MethodPost, Path: "/api/mcp/library/force-sync", Auth: client.AuthManagement},
	}},
	"virtual-mcps": {Description: "manage virtual MCP servers", Actions: withActions(crudActions("/api/mcp/virtual-mcps", "id", client.AuthManagement), map[string]resourceAction{
		"attach-key": {Method: http.MethodPost, Path: "/api/mcp/virtual-mcps/{id}/virtual-keys/{vk_id}", Arguments: []string{"id", "vk-id"}, Auth: client.AuthManagement},
		"detach-key": {Method: http.MethodDelete, Path: "/api/mcp/virtual-mcps/{id}/virtual-keys/{vk_id}", Arguments: []string{"id", "vk-id"}, Auth: client.AuthManagement, Destructive: true},
	})},
	"mcp-sessions": {Description: "manage per-user MCP sessions", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/mcp/sessions", Auth: client.AuthManagement},
		"reauth": {Method: http.MethodPost, Path: "/api/mcp/sessions/{id}/reauth", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"revoke": {Method: http.MethodDelete, Path: "/api/mcp/sessions/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
	}},
	"oauth2-sessions": {Description: "manage gateway OAuth sessions", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/oauth2/sessions", Auth: client.AuthManagement},
		"revoke": {Method: http.MethodDelete, Path: "/api/oauth2/sessions/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
	}},
	"notifications": {Description: "manage gateway notifications", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/notifications", Auth: client.AuthManagement},
		"create": {Method: http.MethodPost, Path: "/api/notifications", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	}},
	"feature-flags": {Description: "inspect or update gateway feature flags", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/feature-flags", Auth: client.AuthManagement},
		"update": {Method: http.MethodPut, Path: "/api/feature-flags/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	}},
	"gateway-config": {Description: "inspect or update gateway configuration", Actions: map[string]resourceAction{
		"get":          {Method: http.MethodGet, Path: "/api/config", Auth: client.AuthManagement},
		"update":       {Method: http.MethodPut, Path: "/api/config", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"metadata":     {Method: http.MethodPost, Path: "/api/config/metadata", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"sync-pricing": {Method: http.MethodPost, Path: "/api/pricing/force-sync", Auth: client.AuthManagement},
	}},
	"proxy-config": {Description: "inspect or update proxy configuration", Actions: map[string]resourceAction{
		"get":    {Method: http.MethodGet, Path: "/api/proxy-config", Auth: client.AuthManagement},
		"update": {Method: http.MethodPut, Path: "/api/proxy-config", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	}},
	"complexity-analyzer": {Description: "manage semantic complexity routing", Actions: map[string]resourceAction{
		"get":               {Method: http.MethodGet, Path: "/api/routing/complexity-analyzer-config", Auth: client.AuthManagement},
		"update":            {Method: http.MethodPut, Path: "/api/routing/complexity-analyzer-config", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"reset":             {Method: http.MethodPost, Path: "/api/routing/complexity-analyzer-config/reset", Auth: client.AuthManagement, Destructive: true},
		"status":            {Method: http.MethodGet, Path: "/api/routing/complexity-analyzer-status", Auth: client.AuthManagement},
		"retry":             {Method: http.MethodPost, Path: "/api/routing/complexity-analyzer-status/retry", Auth: client.AuthManagement},
		"generations":       {Method: http.MethodGet, Path: "/api/routing/complexity-analyzer-generations", Auth: client.AuthManagement},
		"delete-generation": {Method: http.MethodDelete, Path: "/api/routing/complexity-analyzer-generations/{namespace}", Arguments: []string{"namespace"}, Auth: client.AuthManagement, Destructive: true},
	}},
	"circuit-breakers": {Description: "manage Enterprise circuit-breaker policies", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/circuit-breaker/policies", Auth: client.AuthManagement},
		"create": {Method: http.MethodPost, Path: "/api/circuit-breaker/policies", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"update": {Method: http.MethodPut, Path: "/api/circuit-breaker/policies/{name}", Arguments: []string{"name"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete": {Method: http.MethodDelete, Path: "/api/circuit-breaker/policies/{name}", Arguments: []string{"name"}, Auth: client.AuthManagement, Destructive: true},
		"state":  {Method: http.MethodGet, Path: "/api/circuit-breaker/state", Auth: client.AuthManagement},
	}},
	"load-balancer-config": {Description: "manage Enterprise load-balancer configuration", Actions: singletonConfigActions("/api/load-balancer-config")},
	"large-payload-config": {Description: "manage Enterprise large-payload configuration", Actions: singletonConfigActions("/api/large-payload-config")},
	"network-trust": {Description: "manage Enterprise trusted networks", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/network-trust", Auth: client.AuthManagement},
		"add":    {Method: http.MethodPost, Path: "/api/network-trust", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"remove": {Method: http.MethodDelete, Path: "/api/network-trust", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true, Destructive: true},
	}},
	"alert-channels": {Description: "manage Enterprise alert channels", Actions: crudActions("/api/alerting/channels", "id", client.AuthManagement)},
	"alert-rules":    {Description: "manage Enterprise alert rules", Actions: crudActions("/api/alerting/rules", "id", client.AuthManagement)},
	"alert-history":  {Description: "inspect Enterprise alert history", Actions: readOnlyListAction("/api/alerting/history")},
	"guardrails": {Description: "manage Enterprise guardrail provider configurations", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/guardrails", Auth: client.AuthManagement},
		"get":    {Method: http.MethodGet, Path: "/api/guardrails/{name}", Arguments: []string{"name"}, Auth: client.AuthManagement},
		"create": {Method: http.MethodPost, Path: "/api/guardrails/{name}", Arguments: []string{"name"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"update": {Method: http.MethodPut, Path: "/api/guardrails/{name}", Arguments: []string{"name"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete": {Method: http.MethodDelete, Path: "/api/guardrails/{name}", Arguments: []string{"name"}, Auth: client.AuthManagement, Destructive: true},
		"verify": {Method: http.MethodPost, Path: "/api/guardrails/{name}/verify", Arguments: []string{"name"}, Auth: client.AuthManagement},
	}},
	"guardrail-rules": {Description: "manage Enterprise guardrail routing rules", Actions: map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: "/api/guardrails/rules", Auth: client.AuthManagement},
		"create": {Method: http.MethodPost, Path: "/api/guardrails/rules", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"update": {Method: http.MethodPut, Path: "/api/guardrails/rules/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"delete": {Method: http.MethodDelete, Path: "/api/guardrails/rules/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
		"toggle": {Method: http.MethodPatch, Path: "/api/guardrails/rules/{id}/toggle", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true},
	}},
	"devices": {Description: "inspect and remove Enterprise managed devices", Actions: map[string]resourceAction{
		"list":        {Method: http.MethodGet, Path: "/api/devices", Auth: client.AuthManagement},
		"get":         {Method: http.MethodGet, Path: "/api/devices/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"apps":        {Method: http.MethodGet, Path: "/api/devices/{id}/apps", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"mcp-servers": {Method: http.MethodGet, Path: "/api/devices/{id}/mcp-servers", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"delete":      {Method: http.MethodDelete, Path: "/api/devices/{id}", Arguments: []string{"id"}, Auth: client.AuthManagement, Destructive: true},
	}},
	"managed-apps": {Description: "manage Enterprise application approvals", Actions: map[string]resourceAction{
		"list":            {Method: http.MethodGet, Path: "/api/apps", Auth: client.AuthManagement},
		"approval-scopes": {Method: http.MethodGet, Path: "/api/apps/{id}/approval-scopes", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"set-approval":    {Method: http.MethodPut, Path: "/api/apps/{id}/approval", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"batch-approval":  {Method: http.MethodPut, Path: "/api/apps/approval/batch", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	}},
	"managed-mcp-servers": {Description: "manage Enterprise MCP server approvals", Actions: map[string]resourceAction{
		"list":            {Method: http.MethodGet, Path: "/api/mcp-servers", Auth: client.AuthManagement},
		"approval-scopes": {Method: http.MethodGet, Path: "/api/mcp-servers/{id}/approval-scopes", Arguments: []string{"id"}, Auth: client.AuthManagement},
		"set-approval":    {Method: http.MethodPut, Path: "/api/mcp-servers/{id}/approval", Arguments: []string{"id"}, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"batch-approval":  {Method: http.MethodPut, Path: "/api/mcp-servers/approval/batch", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	}},
	"edge": {Description: "manage Enterprise edge-agent policy", Actions: map[string]resourceAction{
		"stats":                {Method: http.MethodGet, Path: "/api/edge/stats", Auth: client.AuthManagement},
		"config":               {Method: http.MethodGet, Path: "/api/edge/config", Auth: client.AuthManagement},
		"update-config":        {Method: http.MethodPut, Path: "/api/edge/config", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"domains":              {Method: http.MethodGet, Path: "/api/edge/config/domains", Auth: client.AuthManagement},
		"add-domain":           {Method: http.MethodPost, Path: "/api/edge/config/domains", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"remove-domain":        {Method: http.MethodDelete, Path: "/api/edge/config/domains/{domain}", Arguments: []string{"domain"}, Auth: client.AuthManagement, Destructive: true},
		"interception-scopes":  {Method: http.MethodGet, Path: "/api/edge/interception/scopes", Auth: client.AuthManagement},
		"set-interception":     {Method: http.MethodPut, Path: "/api/edge/interception", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"set-virtual-key-auth": {Method: http.MethodPut, Path: "/api/edge/virtual-key-auth", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"generate-ca":          {Method: http.MethodPost, Path: "/api/edge/ca/generate", Auth: client.AuthManagement, Destructive: true},
		"rotate-ca":            {Method: http.MethodPost, Path: "/api/edge/ca/rotate", Auth: client.AuthManagement, Destructive: true},
		"import-ca":            {Method: http.MethodPost, Path: "/api/edge/ca/import", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true, Destructive: true},
	}},
	"cluster": {Description: "inspect and administer an Enterprise cluster", Actions: map[string]resourceAction{
		"nodes":                    {Method: http.MethodGet, Path: "/api/cluster/nodes", Auth: client.AuthManagement},
		"health":                   {Method: http.MethodGet, Path: "/api/cluster/health", Auth: client.AuthManagement},
		"diagnostic":               {Method: http.MethodPost, Path: "/api/cluster/diagnostic", Auth: client.AuthManagement, AcceptsBody: true},
		"governance-introspection": {Method: http.MethodGet, Path: "/api/cluster/governance-introspection", Auth: client.AuthManagement},
		"remove-ghost-nodes":       {Method: http.MethodDelete, Path: "/api/cluster/ghost-nodes", Auth: client.AuthManagement, AcceptsBody: true, Destructive: true},
	}},
	"branding": {Description: "manage Enterprise dashboard branding", Actions: map[string]resourceAction{
		"get":    {Method: http.MethodGet, Path: "/api/branding", Auth: client.AuthNone},
		"asset":  {Method: http.MethodGet, Path: "/api/branding/asset/{kind}", Arguments: []string{"kind"}, Auth: client.AuthNone},
		"update": {Method: http.MethodPut, Path: "/api/branding", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
		"reset":  {Method: http.MethodDelete, Path: "/api/branding", Auth: client.AuthManagement, Destructive: true},
	}},
	"license": {Description: "inspect or upload an Enterprise license", Actions: map[string]resourceAction{
		"status": {Method: http.MethodGet, Path: "/api/license/status", Auth: client.AuthNone},
		"upload": {Method: http.MethodPost, Path: "/api/license", Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true, RawBody: true, ContentType: "application/octet-stream"},
	}},
}

// crudActions constructs the standard list/get/create/update/delete action set.
func crudActions(basePath, identifier string, auth client.AuthMode) map[string]resourceAction {
	itemPath := strings.TrimSuffix(basePath, "/") + "/{" + identifier + "}"
	argument := strings.ReplaceAll(identifier, "_", "-")
	return map[string]resourceAction{
		"list":   {Method: http.MethodGet, Path: basePath, Auth: auth},
		"get":    {Method: http.MethodGet, Path: itemPath, Arguments: []string{argument}, Auth: auth},
		"create": {Method: http.MethodPost, Path: basePath, Auth: auth, AcceptsBody: true, NeedsBody: true},
		"update": {Method: http.MethodPut, Path: itemPath, Arguments: []string{argument}, Auth: auth, AcceptsBody: true, NeedsBody: true},
		"delete": {Method: http.MethodDelete, Path: itemPath, Arguments: []string{argument}, Auth: auth, Destructive: true},
	}
}

// readOnlyListAction constructs a list-only resource action set.
func readOnlyListAction(path string) map[string]resourceAction {
	return readOnlyListActionWithAuth(path, client.AuthManagement)
}

// readOnlyListActionWithAuth constructs a list-only resource with explicit auth.
func readOnlyListActionWithAuth(path string, auth client.AuthMode) map[string]resourceAction {
	return map[string]resourceAction{
		"list": {Method: http.MethodGet, Path: path, Auth: auth},
	}
}

// singletonConfigActions constructs get and update operations for one configuration object.
func singletonConfigActions(path string) map[string]resourceAction {
	return map[string]resourceAction{
		"get":    {Method: http.MethodGet, Path: path, Auth: client.AuthManagement},
		"update": {Method: http.MethodPut, Path: path, Auth: client.AuthManagement, AcceptsBody: true, NeedsBody: true},
	}
}

// withActions adds specialized operations to a standard resource action set.
func withActions(base, additions map[string]resourceAction) map[string]resourceAction {
	for name, action := range additions {
		base[name] = action
	}
	return base
}

// runResource executes a declarative resource action.
func (r *Runner) runResource(ctx context.Context, env *environment, resourceName string, args []string) error {
	descriptor := resourceRegistry[resourceName]
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return r.printResourceHelp(resourceName, descriptor)
	}
	actionName := args[0]
	if resourceName == "virtual-keys" && actionName == "generate" {
		return r.generateVirtualKey(ctx, env, args[1:])
	}
	if resourceName == "credentials" && actionName == "create" && !containsBodyFlag(args[1:]) {
		return r.createProviderCredential(ctx, env, args[1:])
	}
	action, ok := descriptor.Actions[actionName]
	if !ok {
		return fmt.Errorf("unknown %s action %q", resourceName, actionName)
	}
	// Browser SSO intentionally cannot enumerate every gateway key. When it is
	// the only available credential, make the familiar list command show just
	// the virtual keys assigned to the signed-in user.
	credentials := env.Client.CredentialsSnapshot()
	if resourceName == "virtual-keys" && actionName == "list" &&
		strings.TrimSpace(credentials.ManagementKey) == "" &&
		strings.TrimSpace(credentials.SessionToken) == "" &&
		strings.TrimSpace(credentials.AgentToken) != "" {
		action = resourceAction{Method: http.MethodGet, Path: "/api/agent/virtual-keys", Auth: client.AuthAgent}
	}
	provided := args[1:]
	if len(provided) < len(action.Arguments) {
		return fmt.Errorf("usage: bifrost %s %s %s", resourceName, actionName, formatArguments(action.Arguments))
	}
	path := action.Path
	for index, argument := range action.Arguments {
		placeholder := "{" + strings.ReplaceAll(argument, "-", "_") + "}"
		path = strings.Replace(path, placeholder, url.PathEscape(provided[index]), 1)
	}

	fs := flag.NewFlagSet("bifrost "+resourceName+" "+actionName, flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var bodyValue string
	var bodyFile string
	var queryFlags repeatedValue
	var dryRun bool
	var yes bool
	var limit int
	var offset int
	var search string
	var sortBy string
	var order string
	fs.StringVar(&bodyValue, "body", "", "inline JSON request body")
	fs.StringVar(&bodyFile, "file", "", "JSON request body file, or - for stdin")
	fs.Var(&queryFlags, "query", "query parameter as key=value; repeatable")
	fs.BoolVar(&dryRun, "dry-run", false, "print the request plan without applying it")
	fs.BoolVar(&yes, "yes", false, "confirm a destructive operation")
	if actionName == "list" {
		fs.IntVar(&limit, "limit", -1, "maximum list results")
		fs.IntVar(&offset, "offset", -1, "list result offset")
		fs.StringVar(&search, "search", "", "server-side list search")
		fs.StringVar(&sortBy, "sort-by", "", "server-side sort field")
		fs.StringVar(&order, "order", "", "server-side sort order")
	}
	if err := fs.Parse(provided[len(action.Arguments):]); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	body, err := r.readBody(bodyValue, bodyFile)
	if err != nil {
		return err
	}
	if len(body) > 0 && !action.AcceptsBody {
		return fmt.Errorf("%s %s does not accept a request body", resourceName, actionName)
	}
	if action.NeedsBody && len(body) == 0 {
		return fmt.Errorf("%s %s requires --body or --file", resourceName, actionName)
	}
	if len(body) > 0 && !action.RawBody && !json.Valid(body) {
		return fmt.Errorf("request body must be valid JSON")
	}
	query, err := parseQuery(queryFlags)
	if err != nil {
		return err
	}
	if actionName == "list" {
		if limit >= 0 {
			query.Set("limit", fmt.Sprint(limit))
		}
		if offset >= 0 {
			query.Set("offset", fmt.Sprint(offset))
		}
		if strings.TrimSpace(search) != "" {
			query.Set("search", strings.TrimSpace(search))
		}
		if strings.TrimSpace(sortBy) != "" {
			query.Set("sort_by", strings.TrimSpace(sortBy))
		}
		if strings.TrimSpace(order) != "" {
			query.Set("order", strings.TrimSpace(order))
		}
	}
	if action.Destructive && !yes && !dryRun {
		return fmt.Errorf("refusing destructive operation without --yes; inspect it first with --dry-run")
	}
	headers := http.Header{}
	if action.ContentType != "" {
		headers.Set("Content-Type", action.ContentType)
	}
	request := client.Request{Method: action.Method, Path: path, Query: query, Headers: headers, Body: body, Auth: action.Auth}
	if dryRun {
		return r.printRequestPlan(env, request)
	}
	return r.executeAndPrint(ctx, env, request)
}

// containsBodyFlag reports whether a caller requested the complete JSON workflow.
func containsBodyFlag(args []string) bool {
	for _, argument := range args {
		if argument == "--body" || argument == "--file" || strings.HasPrefix(argument, "--body=") || strings.HasPrefix(argument, "--file=") {
			return true
		}
	}
	return false
}

// createProviderCredential provides safe flags for common token-based providers.
func (r *Runner) createProviderCredential(ctx context.Context, env *environment, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bifrost credentials create <provider> --name NAME --value-stdin [--model MODEL]")
	}
	provider := strings.TrimSpace(args[0])
	fs := flag.NewFlagSet("bifrost credentials create", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var name string
	var description string
	var models repeatedValue
	var blacklistedModels repeatedValue
	var weight float64
	var disabled bool
	var keyless bool
	var valueStdin bool
	var dryRun bool
	fs.StringVar(&name, "name", "", "credential name")
	fs.StringVar(&description, "description", "", "credential description")
	fs.Var(&models, "model", "allowed model; repeatable, defaults to *")
	fs.Var(&blacklistedModels, "blacklisted-model", "blocked model; repeatable")
	fs.Float64Var(&weight, "weight", 1, "load-balancing weight")
	fs.BoolVar(&disabled, "disabled", false, "create the credential disabled")
	fs.BoolVar(&keyless, "keyless", false, "create an empty credential for a keyless provider")
	fs.BoolVar(&valueStdin, "value-stdin", false, "read the provider secret from stdin")
	fs.BoolVar(&dryRun, "dry-run", false, "print the request plan without creating the credential")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected credentials create arguments: %s", strings.Join(fs.Args(), " "))
	}
	if provider == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("provider and --name are required")
	}
	value := ""
	var err error
	if !keyless && !dryRun {
		value, err = r.readSecret("Provider credential: ", valueStdin)
		if err != nil {
			return err
		}
	} else if !keyless {
		value = "<provided-at-apply-time>"
	}
	if len(models) == 0 {
		models = repeatedValue{"*"}
	}
	payload := map[string]any{
		"name": strings.TrimSpace(name), "value": value, "models": []string(models),
		"blacklisted_models": []string(blacklistedModels), "weight": weight,
		"enabled": !disabled, "description": description,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request := client.Request{
		Method: http.MethodPost, Path: "/api/providers/" + url.PathEscape(provider) + "/keys",
		Body: body, Auth: client.AuthManagement,
	}
	if dryRun {
		// The dry-run plan deliberately reports only body presence, never the secret.
		return r.printRequestPlan(env, request)
	}
	return r.executeAndPrint(ctx, env, request)
}

// generateVirtualKey provides a flag-based alternative to the complete JSON create request.
func (r *Runner) generateVirtualKey(ctx context.Context, env *environment, args []string) error {
	fs := flag.NewFlagSet("bifrost virtual-keys generate", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var name string
	var description string
	var teamID string
	var customerID string
	var expiresAt string
	var providers repeatedValue
	var allowedModels repeatedValue
	var allowAllProviders bool
	var inactive bool
	var dryRun bool
	fs.StringVar(&name, "name", "", "virtual-key name")
	fs.StringVar(&description, "description", "", "virtual-key description")
	fs.StringVar(&teamID, "team-id", "", "owning team ID")
	fs.StringVar(&customerID, "customer-id", "", "owning customer ID")
	fs.StringVar(&expiresAt, "expires-at", "", "RFC3339 expiration time")
	fs.Var(&providers, "provider", "allowed provider; repeatable")
	fs.Var(&allowedModels, "allowed-model", "allowed model for every supplied provider; repeatable")
	fs.BoolVar(&allowAllProviders, "allow-all-providers", false, "allow every configured provider")
	fs.BoolVar(&inactive, "inactive", false, "create the key disabled")
	fs.BoolVar(&dryRun, "dry-run", false, "print the request plan without creating the key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected virtual key generation arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("--name is required")
	}
	if strings.TrimSpace(teamID) != "" && strings.TrimSpace(customerID) != "" {
		return fmt.Errorf("--team-id and --customer-id are mutually exclusive")
	}
	if len(allowedModels) > 0 && len(providers) == 0 {
		return fmt.Errorf("--allowed-model requires --provider; the restriction is only applied per provider")
	}
	payload := map[string]any{
		"name": strings.TrimSpace(name), "description": description,
		"allow_all_providers": allowAllProviders, "is_active": !inactive,
	}
	if strings.TrimSpace(teamID) != "" {
		payload["team_id"] = strings.TrimSpace(teamID)
	}
	if strings.TrimSpace(customerID) != "" {
		payload["customer_id"] = strings.TrimSpace(customerID)
	}
	if strings.TrimSpace(expiresAt) != "" {
		payload["expires_at"] = strings.TrimSpace(expiresAt)
	}
	if len(providers) > 0 {
		providerConfigs := make([]map[string]any, 0, len(providers))
		for _, provider := range providers {
			config := map[string]any{"provider": strings.TrimSpace(provider)}
			if len(allowedModels) > 0 {
				config["allowed_models"] = []string(allowedModels)
			}
			providerConfigs = append(providerConfigs, config)
		}
		payload["provider_configs"] = providerConfigs
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request := client.Request{Method: http.MethodPost, Path: "/api/governance/virtual-keys", Body: body, Auth: client.AuthManagement}
	if dryRun {
		return r.printRequestPlan(env, request)
	}
	return r.executeAndPrint(ctx, env, request)
}

// printRequestPlan renders a credential-free dry-run description.
func (r *Runner) printRequestPlan(env *environment, request client.Request) error {
	endpoint, err := client.BuildEndpoint(env.Client.BaseURL, request.Path, request.Query)
	if err != nil {
		return err
	}
	plan := map[string]any{
		"dry_run": true, "method": request.Method, "url": endpoint,
		"has_body": len(request.Body) > 0, "auth_mode": request.Auth,
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return output.Print(r.Out, body, env.Output)
}

// printResourceHelp prints stable action syntax for one resource.
func (r *Runner) printResourceHelp(name string, descriptor resourceDescriptor) error {
	if _, err := fmt.Fprintf(r.Out, "%s: %s\n\n", name, descriptor.Description); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.Out, "Usage: bifrost %s <action> [identifiers] [flags]\n\nActions:\n", name); err != nil {
		return err
	}
	actions := make([]string, 0, len(descriptor.Actions))
	width := 15
	for actionName := range descriptor.Actions {
		actions = append(actions, actionName)
		if len(actionName) > width {
			width = len(actionName)
		}
	}
	sort.Strings(actions)
	for _, actionName := range actions {
		action, ok := descriptor.Actions[actionName]
		if !ok {
			continue
		}
		suffix := formatArguments(action.Arguments)
		if suffix != "" {
			suffix = " " + suffix
		}
		if _, err := fmt.Fprintf(r.Out, "  %-*s %s%s\n", width, actionName, action.Method, suffix); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(r.Out, "\nMutation flags: --body JSON | --file PATH | --dry-run | --yes\nList flags: --limit N | --offset N | --search TERM | --sort-by FIELD | --order ORDER | --query key=value\n")
	return err
}

// formatArguments renders required positional identifiers for help text.
func formatArguments(arguments []string) string {
	formatted := make([]string, len(arguments))
	for index, argument := range arguments {
		formatted[index] = "<" + argument + ">"
	}
	return strings.Join(formatted, " ")
}
