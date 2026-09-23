package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// GovernanceReaderStub embeds the GovernanceReader interface without implementing
// it, so a fake that only overrides the methods it needs panics on anything new.
type GovernanceReaderStub struct {
	GovernanceReader
}

// fakeGovernanceReader is describe_virtual_key's one dependency. lookup is
// keyed on id, mirroring GetVirtualKey's own "not found" contract: a missing
// key returns configstore.ErrNotFound, not a nil, nil pair.
type fakeGovernanceReader struct {
	GovernanceReaderStub
	byID         map[string]*tables.TableVirtualKey
	teams        map[string]*tables.TableTeam
	customers    map[string]*tables.TableCustomer
	budgets      map[string]*tables.TableBudget
	providers    []tables.TableProvider
	providerKeys map[schemas.ModelProvider][]schemas.Key
	providerCfgs map[schemas.ModelProvider]configstore.ProviderConfig
	mcpClients   map[string]*tables.TableMCPClient
	virtualMCPs  map[uint]*tables.TableVirtualMCP
	vmcpVKs      map[uint][]string
	routing      map[string]*tables.TableRoutingRule
	rateLimits   map[string]*tables.TableRateLimit
	modelConfigs map[string]*tables.TableModelConfig
	plugins      map[string]*tables.TablePlugin
	pricing      map[string]*tables.TablePricingOverride
	webhooks     map[string]*tables.TableWebhookEndpoint
	flags        map[string]tables.TableFeatureFlag
	oauthTokens  []tables.TableMCPOauthToken
	headerCreds  []tables.TableMCPPerUserHeaderCredential
	clientConfig *configstore.ClientConfig
	err          error
	sawContext   context.Context
	sawID        string
	createdVK    *tables.TableVirtualKey
	addedMCP     *schemas.MCPClientConfig
	// createModelConfigErr makes CreateModelConfig fail, to prove the rows
	// written before it in the same transaction are rolled back.
	createModelConfigErr error
	updateModelConfigErr error
	clientConfigErr      error
}

func (f *fakeGovernanceReader) GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error) {
	f.sawContext = ctx
	f.sawID = id
	if f.err != nil {
		return nil, f.err
	}
	vk, ok := f.byID[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return vk, nil
}

func TestDescribeVirtualKeyReportsUnavailableWithoutAGovernanceReader(t *testing.T) {
	_, err := runTool(t, "describe_virtual_key", &Deps{}, map[string]any{"virtual_key_id": "vk-1"})
	require.ErrorContains(t, err, "not available")
}

func TestDescribeVirtualKeyRequiresAnID(t *testing.T) {
	deps := &Deps{Governance: &fakeGovernanceReader{}}
	_, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "  "})
	require.ErrorContains(t, err, "virtual_key_id")
}

func TestDescribeVirtualKeyReportsUnknownID(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{}}
	deps := &Deps{Governance: fake}
	_, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-missing"})
	require.ErrorContains(t, err, "vk-missing")
	require.ErrorContains(t, err, "describe_filter_space")
}

// The caller's context is what carries queryscope's row-level filter into the
// store - GetVirtualKey narrows to rows the caller may see the same way every
// LogReader method does. Losing it here would return any key to anyone who
// asked, the same failure mode LogReader's own tools guard against.
func TestDescribeVirtualKeyPassesCallerContextToStore(t *testing.T) {
	type scopeKey struct{}
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{
		"vk-1": {ID: "vk-1", Name: "prod"},
	}}
	deps := &Deps{Governance: fake}
	ctx := context.WithValue(context.Background(), scopeKey{}, "caller-scope")
	_, err := runToolCtx(t, ctx, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)
	require.Equal(t, "caller-scope", fake.sawContext.Value(scopeKey{}))
	require.Equal(t, "vk-1", fake.sawID)
}

// The result must never carry the key's own secret value, its rotation
// history, or any provider credential beneath it - only the budget/limit/
// provider shape describeVirtualKey hand-picks. This is the regression test
// for that: a row deliberately carrying secret-shaped data in every field
// describeVirtualKey does not touch, asserting none of it survives.
func TestDescribeVirtualKeyNeverLeaksSecretFields(t *testing.T) {
	teamID := "team-1"
	expires := time.Now().Add(24 * time.Hour)
	vk := &tables.TableVirtualKey{
		ID:                "vk-1",
		Name:              "prod",
		Description:       "production traffic",
		TeamID:            &teamID,
		ExpiresAt:         &expires,
		Value:             schemas.SecretVar{Val: "sk-super-secret-value"},
		PreviousValueHash: "leftover-hash",
		Budgets: []tables.TableBudget{
			{ID: "budget-1", MaxLimit: 100, CurrentUsage: 42, ResetDuration: "1M", LastReset: time.Now()},
		},
		RateLimit: &tables.TableRateLimit{ID: "rl-1", TokenMaxLimit: int64Ptr(1000), TokenCurrentUsage: 250},
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				AllowedModels: []string{"gpt-4o"},
				Keys: []tables.TableKey{
					{ID: 1, Name: "prod-openai-key", Value: schemas.SecretVar{Val: "sk-should-never-appear"}},
				},
			},
		},
	}
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}}
	deps := &Deps{Governance: fake}

	result, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)

	out, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "vk-1", out["id"])
	require.Equal(t, "prod", out["name"])
	require.Equal(t, "team-1", out["team_id"])

	budgets, ok := out["budgets"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, budgets, 1)
	require.InDelta(t, 100.0, budgets[0]["max_limit"], 0.001)
	require.InDelta(t, 42.0, budgets[0]["current_usage"], 0.001)

	rateLimit, ok := out["rate_limit"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, int64(1000), rateLimit["token_max_limit"])
	require.NotContains(t, rateLimit, "request_max_limit", "an unset limit family must not read as a limit of zero")

	providers, ok := out["providers"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, providers, 1)
	require.Equal(t, "openai", providers[0]["provider"])
	require.Equal(t, []string{"gpt-4o"}, providers[0]["allowed_models"])
	require.NotContains(t, providers[0], "keys", "no provider key detail, secret or otherwise, may reach the model")

	// The bounded-result serialization is the last line of defense; walking
	// the returned value directly is what proves the secret was never placed
	// there in the first place, not merely stripped afterward.
	serialized := boundToolResult(result)
	require.NotContains(t, serialized, "sk-super-secret-value")
	require.NotContains(t, serialized, "sk-should-never-appear")
	require.NotContains(t, serialized, "leftover-hash")
	require.NotContains(t, serialized, "prod-openai-key", "not even a key's name belongs in a chat tool result")
}

// A budget under an active override must report the effective cap, not the
// raw one the override has already changed - the same distinction the
// dashboard itself makes (see TableBudget.EffectiveMaxLimit).
func TestDescribeVirtualKeyBudgetReportsEffectiveLimitUnderOverride(t *testing.T) {
	vk := &tables.TableVirtualKey{
		ID:   "vk-1",
		Name: "prod",
		Budgets: []tables.TableBudget{
			{
				ID: "budget-1", MaxLimit: 100, CurrentUsage: 10, ResetDuration: "1M", LastReset: time.Now(),
				OverrideAmount: 50, OverrideMode: tables.BudgetOverrideModeForever,
			},
		},
	}
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}}
	deps := &Deps{Governance: fake}

	result, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)

	budgets := result.(map[string]any)["budgets"].([]map[string]any)
	require.InDelta(t, 150.0, budgets[0]["max_limit"], 0.001, "override amount must be folded into the reported cap")
	require.Equal(t, true, budgets[0]["override_active"])
}

func int64Ptr(v int64) *int64 { return &v }

func (f *fakeGovernanceReader) GetVirtualKeysPaginated(ctx context.Context, params configstore.VirtualKeyQueryParams) ([]tables.TableVirtualKey, int64, error) {
	f.sawContext = ctx
	out := make([]tables.TableVirtualKey, 0, len(f.byID))
	for _, vk := range f.byID {
		// Filtered like the store, so tenant walks see only the keys asked for.
		if params.TeamID != "" && (vk.TeamID == nil || *vk.TeamID != params.TeamID) {
			continue
		}
		if params.CustomerID != "" && (vk.CustomerID == nil || *vk.CustomerID != params.CustomerID) {
			continue
		}
		out = append(out, *vk)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error {
	f.sawContext = ctx
	if f.byID == nil {
		f.byID = map[string]*tables.TableVirtualKey{}
	}
	f.byID[virtualKey.ID] = virtualKey
	f.createdVK = virtualKey
	return nil
}

func (f *fakeGovernanceReader) UpdateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error {
	f.sawContext = ctx
	if f.byID == nil {
		f.byID = map[string]*tables.TableVirtualKey{}
	}
	f.byID[virtualKey.ID] = virtualKey
	return nil
}

func (f *fakeGovernanceReader) GetTeam(ctx context.Context, id string, _ ...*gorm.DB) (*tables.TableTeam, error) {
	if f.teams == nil {
		return nil, configstore.ErrNotFound
	}
	team, ok := f.teams[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return team, nil
}

func (f *fakeGovernanceReader) GetTeamsPaginated(ctx context.Context, params configstore.TeamsQueryParams) ([]tables.TableTeam, int64, error) {
	out := make([]tables.TableTeam, 0, len(f.teams))
	for _, team := range f.teams {
		if params.CustomerID != "" && (team.CustomerID == nil || *team.CustomerID != params.CustomerID) {
			continue
		}
		out = append(out, *team)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error {
	if f.teams == nil {
		f.teams = map[string]*tables.TableTeam{}
	}
	f.teams[team.ID] = team
	return nil
}

func (f *fakeGovernanceReader) UpdateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error {
	if f.teams == nil {
		f.teams = map[string]*tables.TableTeam{}
	}
	f.teams[team.ID] = team
	return nil
}

func (f *fakeGovernanceReader) GetCustomer(ctx context.Context, id string, _ ...*gorm.DB) (*tables.TableCustomer, error) {
	if f.customers == nil {
		return nil, configstore.ErrNotFound
	}
	customer, ok := f.customers[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return customer, nil
}

func (f *fakeGovernanceReader) GetCustomersPaginated(ctx context.Context, params configstore.CustomersQueryParams) ([]tables.TableCustomer, int64, error) {
	out := make([]tables.TableCustomer, 0, len(f.customers))
	for _, customer := range f.customers {
		out = append(out, *customer)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error {
	if f.customers == nil {
		f.customers = map[string]*tables.TableCustomer{}
	}
	f.customers[customer.ID] = customer
	return nil
}

func (f *fakeGovernanceReader) UpdateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error {
	if f.customers == nil {
		f.customers = map[string]*tables.TableCustomer{}
	}
	f.customers[customer.ID] = customer
	return nil
}

func (f *fakeGovernanceReader) GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableBudget, error) {
	if f.budgets == nil {
		return nil, configstore.ErrNotFound
	}
	budget, ok := f.budgets[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return budget, nil
}

func (f *fakeGovernanceReader) GetBudgets(ctx context.Context) ([]tables.TableBudget, error) {
	out := make([]tables.TableBudget, 0, len(f.budgets))
	for _, budget := range f.budgets {
		out = append(out, *budget)
	}
	return out, nil
}

func (f *fakeGovernanceReader) CreateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error {
	if f.budgets == nil {
		f.budgets = map[string]*tables.TableBudget{}
	}
	f.budgets[budget.ID] = budget
	return nil
}

func (f *fakeGovernanceReader) UpdateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error {
	if f.budgets == nil {
		f.budgets = map[string]*tables.TableBudget{}
	}
	f.budgets[budget.ID] = budget
	return nil
}

func (f *fakeGovernanceReader) GetProviders(ctx context.Context) ([]tables.TableProvider, error) {
	return f.providers, nil
}

func (f *fakeGovernanceReader) GetProviderKeys(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
	if f.providerKeys == nil {
		return nil, configstore.ErrNotFound
	}
	keys, ok := f.providerKeys[provider]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return keys, nil
}

func (f *fakeGovernanceReader) AddProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig, tx ...*gorm.DB) error {
	f.providers = append(f.providers, tables.TableProvider{Name: string(provider)})
	return nil
}

func (f *fakeGovernanceReader) CreateProviderKey(ctx context.Context, provider schemas.ModelProvider, key schemas.Key, tx ...*gorm.DB) error {
	if f.providerKeys == nil {
		f.providerKeys = map[schemas.ModelProvider][]schemas.Key{}
	}
	f.providerKeys[provider] = append(f.providerKeys[provider], key)
	return nil
}

func (f *fakeGovernanceReader) UpdateProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, key schemas.Key, tx ...*gorm.DB) error {
	keys := f.providerKeys[provider]
	for i := range keys {
		if keys[i].ID == keyID {
			keys[i] = key
			f.providerKeys[provider] = keys
			return nil
		}
	}
	return configstore.ErrNotFound
}

func (f *fakeGovernanceReader) GetProviderConfig(ctx context.Context, provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	if f.providerCfgs == nil {
		return nil, configstore.ErrNotFound
	}
	cfg, ok := f.providerCfgs[provider]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return &cfg, nil
}

func (f *fakeGovernanceReader) UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig, tx ...*gorm.DB) error {
	if f.providerCfgs == nil {
		f.providerCfgs = map[schemas.ModelProvider]configstore.ProviderConfig{}
	}
	f.providerCfgs[provider] = config
	return nil
}

func (f *fakeGovernanceReader) GetMCPClientByID(ctx context.Context, id string) (*tables.TableMCPClient, error) {
	if f.mcpClients == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.mcpClients[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetMCPClientsPaginated(ctx context.Context, params configstore.MCPClientsQueryParams) ([]tables.TableMCPClient, int64, error) {
	out := make([]tables.TableMCPClient, 0, len(f.mcpClients))
	for _, row := range f.mcpClients {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreateMCPClientConfig(ctx context.Context, clientConfig *schemas.MCPClientConfig) error {
	f.addedMCP = clientConfig
	if f.mcpClients == nil {
		f.mcpClients = map[string]*tables.TableMCPClient{}
	}
	f.mcpClients[clientConfig.ID] = &tables.TableMCPClient{
		ClientID:       clientConfig.ID,
		Name:           clientConfig.Name,
		ConnectionType: string(clientConfig.ConnectionType),
	}
	return nil
}

func (f *fakeGovernanceReader) UpdateMCPClientConfig(ctx context.Context, id string, clientConfig *tables.TableMCPClient) error {
	if f.mcpClients == nil {
		f.mcpClients = map[string]*tables.TableMCPClient{}
	}
	f.mcpClients[id] = clientConfig
	return nil
}

func (f *fakeGovernanceReader) GetVirtualMCPByID(ctx context.Context, id uint) (*tables.TableVirtualMCP, error) {
	if f.virtualMCPs == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.virtualMCPs[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetVirtualMCPsPaginated(ctx context.Context, params configstore.VirtualMCPsQueryParams) ([]tables.TableVirtualMCP, int64, error) {
	out := make([]tables.TableVirtualMCP, 0, len(f.virtualMCPs))
	for _, row := range f.virtualMCPs {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreateVirtualMCP(ctx context.Context, def *tables.TableVirtualMCP) error {
	if f.virtualMCPs == nil {
		f.virtualMCPs = map[uint]*tables.TableVirtualMCP{}
	}
	if def.ID == 0 {
		def.ID = uint(len(f.virtualMCPs) + 1)
	}
	f.virtualMCPs[def.ID] = def
	return nil
}

func (f *fakeGovernanceReader) UpdateVirtualMCP(ctx context.Context, def *tables.TableVirtualMCP) error {
	if f.virtualMCPs == nil {
		f.virtualMCPs = map[uint]*tables.TableVirtualMCP{}
	}
	f.virtualMCPs[def.ID] = def
	return nil
}

func (f *fakeGovernanceReader) AttachVirtualMCPToVirtualKey(ctx context.Context, vmcpID uint, virtualKeyID string) error {
	if f.vmcpVKs == nil {
		f.vmcpVKs = map[uint][]string{}
	}
	f.vmcpVKs[vmcpID] = append(f.vmcpVKs[vmcpID], virtualKeyID)
	return nil
}

func (f *fakeGovernanceReader) DetachVirtualMCPFromVirtualKey(ctx context.Context, vmcpID uint, virtualKeyID string) error {
	kept := f.vmcpVKs[vmcpID][:0]
	for _, id := range f.vmcpVKs[vmcpID] {
		if id != virtualKeyID {
			kept = append(kept, id)
		}
	}
	f.vmcpVKs[vmcpID] = kept
	return nil
}

func (f *fakeGovernanceReader) GetVirtualKeyIDsForVirtualMCP(ctx context.Context, vmcpID uint) ([]string, error) {
	return f.vmcpVKs[vmcpID], nil
}

// ExecuteTransaction snapshots the rows that transactional tools write to and
// restores them when fn fails, standing in for a database rollback. The rows
// themselves are copied, not just the maps: the getters hand out the stored
// pointers, so a tool that edits a loaded row in place (setting RateLimitID on
// the owner) must see that edit undone too.
func (f *fakeGovernanceReader) ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	rateLimits := cloneRows(f.rateLimits)
	modelConfigs := cloneRows(f.modelConfigs)
	budgets := cloneRows(f.budgets)
	teams := cloneRows(f.teams)
	customers := cloneRows(f.customers)
	byID := cloneRows(f.byID)
	if err := fn(nil); err != nil {
		f.rateLimits, f.modelConfigs, f.budgets = rateLimits, modelConfigs, budgets
		f.teams, f.customers, f.byID = teams, customers, byID
		return err
	}
	return nil
}

// cloneRows copies each row a map points at, so the snapshot does not share
// structs with the live map.
func cloneRows[K comparable, V any](rows map[K]*V) map[K]*V {
	if rows == nil {
		return nil
	}
	out := make(map[K]*V, len(rows))
	for k, v := range rows {
		if v == nil {
			out[k] = nil
			continue
		}
		c := *v
		out[k] = &c
	}
	return out
}

func (f *fakeGovernanceReader) GetClientConfig(ctx context.Context) (*configstore.ClientConfig, error) {
	if f.clientConfigErr != nil {
		return nil, f.clientConfigErr
	}
	if f.clientConfig == nil {
		return &configstore.ClientConfig{}, nil
	}
	return f.clientConfig, nil
}

func (f *fakeGovernanceReader) GetRoutingRule(ctx context.Context, id string) (*tables.TableRoutingRule, error) {
	if f.routing == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.routing[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetRoutingRulesPaginated(ctx context.Context, params configstore.RoutingRulesQueryParams) ([]tables.TableRoutingRule, int64, error) {
	out := make([]tables.TableRoutingRule, 0, len(f.routing))
	for _, row := range f.routing {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreateRoutingRule(ctx context.Context, rule *tables.TableRoutingRule, tx ...*gorm.DB) error {
	if f.routing == nil {
		f.routing = map[string]*tables.TableRoutingRule{}
	}
	f.routing[rule.ID] = rule
	return nil
}

func (f *fakeGovernanceReader) UpdateRoutingRule(ctx context.Context, rule *tables.TableRoutingRule, tx ...*gorm.DB) error {
	if f.routing == nil {
		f.routing = map[string]*tables.TableRoutingRule{}
	}
	f.routing[rule.ID] = rule
	return nil
}

func (f *fakeGovernanceReader) GetRateLimit(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableRateLimit, error) {
	if f.rateLimits == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.rateLimits[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetRateLimits(ctx context.Context) ([]tables.TableRateLimit, error) {
	out := make([]tables.TableRateLimit, 0, len(f.rateLimits))
	for _, row := range f.rateLimits {
		out = append(out, *row)
	}
	return out, nil
}

func (f *fakeGovernanceReader) CreateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error {
	if f.rateLimits == nil {
		f.rateLimits = map[string]*tables.TableRateLimit{}
	}
	f.rateLimits[rateLimit.ID] = rateLimit
	return nil
}

func (f *fakeGovernanceReader) UpdateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error {
	if f.rateLimits == nil {
		f.rateLimits = map[string]*tables.TableRateLimit{}
	}
	f.rateLimits[rateLimit.ID] = rateLimit
	return nil
}

func (f *fakeGovernanceReader) GetModelConfigByID(ctx context.Context, id string) (*tables.TableModelConfig, error) {
	if f.modelConfigs == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.modelConfigs[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetModelConfigsPaginated(ctx context.Context, params configstore.ModelConfigsQueryParams) ([]tables.TableModelConfig, int64, error) {
	out := make([]tables.TableModelConfig, 0, len(f.modelConfigs))
	for _, row := range f.modelConfigs {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error {
	if f.createModelConfigErr != nil {
		return f.createModelConfigErr
	}
	if f.modelConfigs == nil {
		f.modelConfigs = map[string]*tables.TableModelConfig{}
	}
	f.modelConfigs[modelConfig.ID] = modelConfig
	return nil
}

func (f *fakeGovernanceReader) UpdateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error {
	if f.updateModelConfigErr != nil {
		return f.updateModelConfigErr
	}
	if f.modelConfigs == nil {
		f.modelConfigs = map[string]*tables.TableModelConfig{}
	}
	f.modelConfigs[modelConfig.ID] = modelConfig
	return nil
}

func (f *fakeGovernanceReader) GetPlugin(ctx context.Context, name string) (*tables.TablePlugin, error) {
	if f.plugins == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.plugins[name]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetPlugins(ctx context.Context) ([]*tables.TablePlugin, error) {
	out := make([]*tables.TablePlugin, 0, len(f.plugins))
	for _, row := range f.plugins {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeGovernanceReader) CreatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error {
	if f.plugins == nil {
		f.plugins = map[string]*tables.TablePlugin{}
	}
	f.plugins[plugin.Name] = plugin
	return nil
}

func (f *fakeGovernanceReader) UpdatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error {
	if f.plugins == nil {
		f.plugins = map[string]*tables.TablePlugin{}
	}
	f.plugins[plugin.Name] = plugin
	return nil
}

func (f *fakeGovernanceReader) GetPricingOverrideByID(ctx context.Context, id string) (*tables.TablePricingOverride, error) {
	if f.pricing == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.pricing[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetPricingOverridesPaginated(ctx context.Context, params configstore.PricingOverridesQueryParams) ([]tables.TablePricingOverride, int64, error) {
	out := make([]tables.TablePricingOverride, 0, len(f.pricing))
	for _, row := range f.pricing {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreatePricingOverride(ctx context.Context, override *tables.TablePricingOverride, tx ...*gorm.DB) error {
	if f.pricing == nil {
		f.pricing = map[string]*tables.TablePricingOverride{}
	}
	f.pricing[override.ID] = override
	return nil
}

func (f *fakeGovernanceReader) UpdatePricingOverride(ctx context.Context, override *tables.TablePricingOverride, tx ...*gorm.DB) error {
	if f.pricing == nil {
		f.pricing = map[string]*tables.TablePricingOverride{}
	}
	f.pricing[override.ID] = override
	return nil
}

func (f *fakeGovernanceReader) GetWebhookEndpointByID(ctx context.Context, id string) (*tables.TableWebhookEndpoint, error) {
	if f.webhooks == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.webhooks[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetWebhookEndpointsPaginated(ctx context.Context, params configstore.WebhookEndpointsQueryParams) ([]tables.TableWebhookEndpoint, int64, error) {
	out := make([]tables.TableWebhookEndpoint, 0, len(f.webhooks))
	for _, row := range f.webhooks {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) CreateWebhookEndpoint(ctx context.Context, endpoint *tables.TableWebhookEndpoint) error {
	if f.webhooks == nil {
		f.webhooks = map[string]*tables.TableWebhookEndpoint{}
	}
	if endpoint.Secret == nil || endpoint.Secret.GetValue() == "" {
		endpoint.Secret = schemas.NewSecretVar("whsec_test")
	}
	f.webhooks[endpoint.ID] = endpoint
	return nil
}

func (f *fakeGovernanceReader) UpdateWebhookEndpoint(ctx context.Context, endpoint *tables.TableWebhookEndpoint) error {
	if f.webhooks == nil {
		f.webhooks = map[string]*tables.TableWebhookEndpoint{}
	}
	f.webhooks[endpoint.ID] = endpoint
	return nil
}

func (f *fakeGovernanceReader) ListFeatureFlags(ctx context.Context) ([]tables.TableFeatureFlag, error) {
	out := make([]tables.TableFeatureFlag, 0, len(f.flags))
	for _, row := range f.flags {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeGovernanceReader) UpsertFeatureFlag(ctx context.Context, id string, enabled bool, updatedAt int64) error {
	if f.flags == nil {
		f.flags = map[string]tables.TableFeatureFlag{}
	}
	f.flags[id] = tables.TableFeatureFlag{ID: id, Enabled: enabled, UpdatedAt: updatedAt}
	return nil
}

func (f *fakeGovernanceReader) ListOauthUserTokens(ctx context.Context, params configstore.MCPSessionsFilterParams) ([]tables.TableMCPOauthToken, error) {
	return f.oauthTokens, nil
}

func (f *fakeGovernanceReader) ListMCPPerUserHeaderCredentials(ctx context.Context, params configstore.MCPSessionsFilterParams) ([]tables.TableMCPPerUserHeaderCredential, error) {
	return f.headerCreds, nil
}

func TestCreateVirtualKeyReturnsSecretOnceAndListDoesNot(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{}}
	deps := &Deps{Governance: fake}

	created, err := runTool(t, "create_virtual_key", deps, map[string]any{"name": "ops"})
	require.NoError(t, err)
	out := created.(map[string]any)
	secret, _ := out["value"].(string)
	require.True(t, strings.HasPrefix(secret, VirtualKeyPrefix))
	require.Contains(t, boundToolResult(created), secret)

	listed, err := runTool(t, "list_virtual_keys", deps, map[string]any{})
	require.NoError(t, err)
	require.NotContains(t, boundToolResult(listed), secret)
}

func TestRotateVirtualKeyReturnsNewSecret(t *testing.T) {
	active := true
	vk := &tables.TableVirtualKey{
		ID:       "vk-1",
		Name:     "ops",
		Value:    *schemas.NewSecretVar("sk-bf-old"),
		IsActive: &active,
	}
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}}
	deps := &Deps{Governance: fake}

	result, err := runTool(t, "rotate_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)
	out := result.(map[string]any)
	secret, _ := out["value"].(string)
	require.True(t, strings.HasPrefix(secret, VirtualKeyPrefix))
	require.NotEqual(t, "sk-bf-old", secret)
}

func TestCreateBudgetRequiresExactlyOneOwner(t *testing.T) {
	fake := &fakeGovernanceReader{}
	deps := &Deps{Governance: fake}
	_, err := runTool(t, "create_budget", deps, map[string]any{"max_limit": 10.0, "reset_duration": "1d"})
	require.ErrorContains(t, err, "team_id or customer_id")
}

func TestStdioMCPClientIsRefused(t *testing.T) {
	fake := &fakeGovernanceReader{}
	deps := &Deps{Governance: fake}
	_, err := runTool(t, "add_mcp_client", deps, map[string]any{
		"name": "shell", "connection_type": "stdio", "connection_string": "npx foo",
	})
	require.ErrorContains(t, err, "stdio")
}

func TestAddMCPClientRefusesReservedName(t *testing.T) {
	fake := &fakeGovernanceReader{}
	deps := &Deps{Governance: fake}
	_, err := runTool(t, "add_mcp_client", deps, map[string]any{
		"name": "bifrostmcp", "connection_type": "http", "connection_string": "https://example.com",
	})
	require.ErrorContains(t, err, "reserved")
}

func TestListProviderKeysNeverReturnsSecret(t *testing.T) {
	fake := &fakeGovernanceReader{providerKeys: map[schemas.ModelProvider][]schemas.Key{
		"openai": {{ID: "k1", Name: "prod", Value: *schemas.NewSecretVar("sk-never-leak")}},
	}}
	deps := &Deps{Governance: fake}
	result, err := runTool(t, "list_provider_keys", deps, map[string]any{"provider": "openai"})
	require.NoError(t, err)
	require.NotContains(t, boundToolResult(result), "sk-never-leak")
}

func TestGetHealthSkipsPingsWhenDisabled(t *testing.T) {
	result, err := runTool(t, "get_health", &Deps{DisableDBPings: true}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "ok", out["status"])
	require.Equal(t, "disabled", out["components"].(map[string]string)["config_store"])
}

func TestGetVersionReportsUnknownWhenUnset(t *testing.T) {
	result, err := runTool(t, "get_version", &Deps{}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "unknown", result.(map[string]any)["version"])
}

func TestGetSessionReportsUnknownID(t *testing.T) {
	_, err := runTool(t, "get_session", &Deps{LogManager: &fakeLogReader{}}, map[string]any{"session_id": "missing"})
	require.ErrorContains(t, err, "missing")
}

func TestQueryMCPLogsReturnsProjectedRows(t *testing.T) {
	fake := &fakeLogReader{mcpSearchResult: &logstore.MCPToolLogSearchResult{
		Logs: []logstore.MCPToolLog{{ID: "m1", ToolName: "echo", Status: "success"}},
	}}
	result, err := runTool(t, "query_mcp_logs", &Deps{LogManager: fake}, map[string]any{"filters": map[string]any{}})
	require.NoError(t, err)
	rows := result.(map[string]any)["rows"].([]map[string]any)
	require.Equal(t, "m1", rows[0]["id"])
	require.Equal(t, "echo", rows[0]["tool_name"])
}

func TestCreateVirtualMCPAndAttach(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": {ID: "vk-1"}}}
	deps := &Deps{Governance: fake}
	created, err := runTool(t, "create_virtual_mcp", deps, map[string]any{"name": "ops-bundle"})
	require.NoError(t, err)
	id := created.(map[string]any)["id"].(uint)
	_, err = runTool(t, "attach_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(id), "virtual_key_id": "vk-1"})
	require.NoError(t, err)
	require.Equal(t, []string{"vk-1"}, fake.vmcpVKs[id])
}

// A virtual MCP the gateway has not reloaded does not serve its new tools, so
// a failed reload is reported, not swallowed; a successful one returns the
// reloaded definition.
func TestVirtualMCPWritesReportReloadFailure(t *testing.T) {
	fake := &fakeGovernanceReader{}
	reloader := &recordingReloader{fail: errors.New("cache boom")}
	deps := &Deps{Governance: fake, Reloader: reloader}
	_, err := runTool(t, "create_virtual_mcp", deps, map[string]any{"name": "ops-bundle"})
	require.ErrorContains(t, err, "stored but live reload failed")
	require.ErrorContains(t, err, "cache boom")
	require.Len(t, fake.virtualMCPs, 1, "the definition is stored even though the reload failed")

	_, err = runTool(t, "update_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(1), "name": "renamed"})
	require.ErrorContains(t, err, "stored but live reload failed")

	reloader.fail = nil
	reloader.reloadedVMCP = &tables.TableVirtualMCP{ID: 1, Name: "from-reload"}
	result, err := runTool(t, "update_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(1), "name": "renamed-again"})
	require.NoError(t, err)
	require.Equal(t, "from-reload", result.(map[string]any)["name"])
}

// recordingReloader records the live-state calls a write makes and can fail
// them, standing in for the HTTP server.
type recordingReloader struct {
	GovernanceReloader
	calls        []string
	fail         error
	reloadedVMCP *tables.TableVirtualMCP
}

func (r *recordingReloader) AttachVirtualMCPToVirtualKeyInMemory(ctx context.Context, vkID string, id uint) error {
	r.calls = append(r.calls, fmt.Sprintf("attach:%s:%d", vkID, id))
	return r.fail
}

func (r *recordingReloader) DetachVirtualMCPFromVirtualKeyInMemory(ctx context.Context, vkID string, id uint) error {
	r.calls = append(r.calls, fmt.Sprintf("detach:%s:%d", vkID, id))
	return r.fail
}

func (r *recordingReloader) ReloadVirtualMCP(ctx context.Context, id uint) (*tables.TableVirtualMCP, error) {
	r.calls = append(r.calls, fmt.Sprintf("reload-vmcp:%d", id))
	if r.fail != nil {
		return nil, r.fail
	}
	return r.reloadedVMCP, nil
}

func (r *recordingReloader) ReloadVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error) {
	r.calls = append(r.calls, "reload-vk:"+id)
	return nil, r.fail
}

func (r *recordingReloader) ReloadModelConfig(ctx context.Context, id string) (*tables.TableModelConfig, error) {
	r.calls = append(r.calls, "reload-mc:"+id)
	return nil, r.fail
}

func (r *recordingReloader) ReloadTeam(ctx context.Context, id string) (*tables.TableTeam, error) {
	r.calls = append(r.calls, "reload-team:"+id)
	return nil, r.fail
}

func (r *recordingReloader) ReloadCustomer(ctx context.Context, id string) (*tables.TableCustomer, error) {
	r.calls = append(r.calls, "reload-customer:"+id)
	return nil, r.fail
}

// The gateway enforces virtual-MCP assignments from an in-memory map that a
// key or definition reload does not touch, so attach and detach update it -
// and a detach whose live update fails is not reported as done.
func TestVirtualMCPAssignmentUpdatesLiveState(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": {ID: "vk-1"}}}
	reloader := &recordingReloader{}
	deps := &Deps{Governance: fake, Reloader: reloader}
	_, err := runTool(t, "attach_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(7), "virtual_key_id": "vk-1"})
	require.NoError(t, err)
	_, err = runTool(t, "detach_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(7), "virtual_key_id": "vk-1"})
	require.NoError(t, err)
	require.Contains(t, reloader.calls, "attach:vk-1:7")
	require.Contains(t, reloader.calls, "detach:vk-1:7")

	reloader.fail = errors.New("cache boom")
	_, err = runTool(t, "detach_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(7), "virtual_key_id": "vk-1"})
	require.ErrorContains(t, err, "takes effect only after a restart")

	_, err = runTool(t, "attach_virtual_mcp", &Deps{Governance: fake}, map[string]any{"virtual_mcp_id": float64(7), "virtual_key_id": "vk-typo"})
	require.ErrorContains(t, err, `no virtual key with id "vk-typo"`)
}

// Deactivating a key only stops it authenticating once the in-memory copy is
// reloaded; a failed reload is an error, not a success over a stale cache.
// Rotate and create still return their one-time secret, with a warning.
func TestVirtualKeyReloadFailuresAreNotSwallowed(t *testing.T) {
	newFake := func() *fakeGovernanceReader {
		return &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": {ID: "vk-1", Name: "k"}}}
	}
	reloader := &recordingReloader{fail: errors.New("cache boom")}
	_, err := runTool(t, "deactivate_virtual_key", &Deps{Governance: newFake(), Reloader: reloader}, map[string]any{"virtual_key_id": "vk-1"})
	require.ErrorContains(t, err, "was not reloaded")

	result, err := runTool(t, "rotate_virtual_key", &Deps{Governance: newFake(), Reloader: reloader}, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.NotEmpty(t, out["value"])
	require.Contains(t, out["warning"], "OLD secret keeps authenticating")

	result, err = runTool(t, "create_virtual_key", &Deps{Governance: newFake(), Reloader: reloader}, map[string]any{"name": "new"})
	require.NoError(t, err)
	require.NotEmpty(t, result.(map[string]any)["value"])
	require.Contains(t, result.(map[string]any)["warning"], "will not authenticate")
}

// A failed cooldown read must not become a zero cooldown that cuts the old
// secret off immediately.
func TestRotateVirtualKeyRefusesWithoutCooldown(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": {ID: "vk-1"}}, clientConfigErr: errors.New("db down")}
	before := fake.byID["vk-1"].Value.GetValue()
	_, err := runTool(t, "rotate_virtual_key", &Deps{Governance: fake}, map[string]any{"virtual_key_id": "vk-1"})
	require.ErrorContains(t, err, "was not rotated")
	require.Equal(t, before, fake.byID["vk-1"].Value.GetValue())
}

// Virtual-key budgets hang off model configs; updating one reloads that
// config so the new cap is enforced now, not after a restart.
func TestUpdateBudgetReloadsOwningModelConfig(t *testing.T) {
	fake := &fakeGovernanceReader{budgets: map[string]*tables.TableBudget{
		"b-1": {ID: "b-1", MaxLimit: 10, ResetDuration: "1d", ModelConfigID: ptr("mc-1")},
	}}
	reloader := &recordingReloader{}
	_, err := runTool(t, "update_budget", &Deps{Governance: fake, Reloader: reloader}, map[string]any{"budget_id": "b-1", "max_limit": 20.0})
	require.NoError(t, err)
	require.Contains(t, reloader.calls, "reload-mc:mc-1")
}

// A budget is stored before its owner is reloaded, so a failed reload is a
// warning on the stored budget rather than an error that hides the write.
func TestBudgetOwnerReloadFailureWarns(t *testing.T) {
	fake := &fakeGovernanceReader{
		teams:     map[string]*tables.TableTeam{"team-1": {ID: "team-1"}},
		customers: map[string]*tables.TableCustomer{"cust-1": {ID: "cust-1"}},
		budgets: map[string]*tables.TableBudget{
			"b-vk": {ID: "b-vk", MaxLimit: 10, ResetDuration: "1d", VirtualKeyID: ptr("vk-1")},
		},
	}
	reloader := &recordingReloader{fail: errors.New("cache boom")}
	deps := &Deps{Governance: fake, Reloader: reloader}

	for _, owner := range []map[string]any{{"team_id": "team-1"}, {"customer_id": "cust-1"}} {
		args := map[string]any{"max_limit": 5.0, "reset_duration": "1d"}
		maps.Copy(args, owner)
		result, err := runTool(t, "create_budget", deps, args)
		require.NoError(t, err)
		out := result.(map[string]any)
		require.NotEmpty(t, out["id"])
		require.Contains(t, out["warning"], "cache boom")
	}

	result, err := runTool(t, "update_budget", deps, map[string]any{"budget_id": "b-vk", "max_limit": 20.0})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.InDelta(t, 20.0, out["max_limit"], 0.001)
	require.Contains(t, out["warning"], "cache boom")
	require.Contains(t, reloader.calls, "reload-vk:vk-1")

	reloader.fail = nil
	result, err = runTool(t, "create_budget", deps, map[string]any{"max_limit": 5.0, "reset_duration": "1d", "team_id": "team-1"})
	require.NoError(t, err)
	require.NotContains(t, result.(map[string]any), "warning")
}

type recordingMCPClientEditor struct {
	live    map[string]*schemas.MCPClientConfig
	applied []*schemas.MCPClientConfig
	fail    error
}

func (e *recordingMCPClientEditor) GetMCPClientConfig(id string) (*schemas.MCPClientConfig, error) {
	cfg, ok := e.live[id]
	if !ok {
		return nil, fmt.Errorf("MCP client %q not found", id)
	}
	copied := *cfg
	return &copied, nil
}

func (e *recordingMCPClientEditor) UpdateMCPClient(ctx context.Context, id string, cfg *schemas.MCPClientConfig) error {
	e.applied = append(e.applied, cfg)
	return e.fail
}

// update_mcp_client persists tools_to_execute on the field the store actually
// serializes, and applies the edit to the live client - a disable that only
// reached the database left the client serving tools until restart.
func TestUpdateMCPClientPersistsAndAppliesLive(t *testing.T) {
	fake := &fakeGovernanceReader{mcpClients: map[string]*tables.TableMCPClient{
		"c-1": {ClientID: "c-1", Name: "github", ToolsToExecute: schemas.WhiteList{"*"}},
	}}
	editor := &recordingMCPClientEditor{live: map[string]*schemas.MCPClientConfig{
		"c-1": {ID: "c-1", Name: "github", ToolsToExecute: schemas.WhiteList{"*"}},
	}}
	deps := &Deps{Governance: fake, MCPClients: editor}
	_, err := runTool(t, "update_mcp_client", deps, map[string]any{"client_id": "c-1", "disabled": true, "tools_to_execute": []any{"search"}})
	require.NoError(t, err)
	require.Equal(t, schemas.WhiteList{"search"}, fake.mcpClients["c-1"].ToolsToExecute)
	require.Len(t, editor.applied, 1)
	require.True(t, editor.applied[0].Disabled)
	require.Equal(t, schemas.WhiteList{"search"}, editor.applied[0].ToolsToExecute)
	require.Equal(t, schemas.WhiteList{"*"}, editor.live["c-1"].ToolsToExecute, "the live config is edited through a copy")

	_, err = runTool(t, "update_mcp_client", deps, map[string]any{"client_id": "c-1", "tools_to_execute": nil})
	require.ErrorContains(t, err, "tools_to_execute must be an array")

	editor.fail = errors.New("dial boom")
	_, err = runTool(t, "update_mcp_client", deps, map[string]any{"client_id": "c-1", "disabled": false})
	require.ErrorContains(t, err, "takes effect after the next restart")
}

// Routing writes get the checks the HTTP handler makes; each of these was
// stored before and failed, or matched nothing, only at request time.
func TestRoutingRuleWritesAreValidated(t *testing.T) {
	target := []any{map[string]any{"provider": "openai", "model": "gpt-4o", "weight": 1.0}}
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"name": "r", "targets": target, "cel_expression": "model !! x"}, "invalid CEL expression"},
		{map[string]any{"name": "r", "targets": target, "fallbacks": []any{"not-a-provider"}}, "known provider"},
		{map[string]any{"name": "r", "targets": target, "scope": "team", "scope_id": "team-typo"}, `no team with id "team-typo"`},
		{map[string]any{"name": "r", "targets": []any{map[string]any{"key_id": "k-1", "weight": 1.0}}}, "key_id requires provider"},
		{map[string]any{"name": "r", "targets": []any{
			map[string]any{"provider": "OpenAI", "model": "gpt-4o", "weight": 0.5},
			map[string]any{"provider": "openai", "model": "GPT-4o", "weight": 0.5},
		}}, "duplicates an earlier target"},
	}
	for _, tc := range cases {
		fake := &fakeGovernanceReader{}
		_, err := runTool(t, "create_routing_rule", &Deps{Governance: fake, RoutingCEL: fakeCEL{}}, tc.args)
		require.ErrorContains(t, err, tc.want)
		require.Empty(t, fake.routing, "nothing is stored when validation fails")
	}

	// A zero-weight target is allowed, as it is on the HTTP API.
	_, err := runTool(t, "create_routing_rule", &Deps{Governance: &fakeGovernanceReader{}, RoutingCEL: fakeCEL{}}, map[string]any{"name": "r", "targets": []any{
		map[string]any{"provider": "openai", "weight": 1.0},
		map[string]any{"provider": "anthropic", "weight": 0.0},
	}})
	require.NoError(t, err)

	_, err = runTool(t, "create_routing_rule", &Deps{Governance: &fakeGovernanceReader{}}, map[string]any{"name": "r", "targets": target})
	require.ErrorContains(t, err, "cannot be validated")
}

type recordingKeyChecks struct {
	reject error
	calls  []string
}

func (c *recordingKeyChecks) ValidateProviderKey(provider schemas.ModelProvider, key schemas.Key, creating bool) error {
	c.calls = append(c.calls, fmt.Sprintf("validate:%s:%v", key.Name, creating))
	return c.reject
}

func (c *recordingKeyChecks) OnKeyAdded(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error {
	c.calls = append(c.calls, "added:"+key.Name)
	return nil
}

func (c *recordingKeyChecks) OnKeyUpdated(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error {
	c.calls = append(c.calls, "updated:"+key.Name)
	return nil
}

// Provider-key writes get the HTTP handler's checks before anything is stored
// (an Ollama key with no URL was stored unusable), and its catalog refresh
// after - without which a disabled key's models stayed listed.
func TestProviderKeyWritesAreValidatedAndRefreshCatalog(t *testing.T) {
	fake := &fakeGovernanceReader{}
	checks := &recordingKeyChecks{reject: errors.New("ollama_key_config.url is required for Ollama keys")}
	_, err := runTool(t, "create_provider_key", &Deps{Governance: fake, ProviderKeys: checks}, map[string]any{"provider": "ollama", "name": "k", "value": "x"})
	require.ErrorContains(t, err, "url is required")
	require.Empty(t, fake.providerKeys, "a rejected key is not stored")

	checks.reject = nil
	_, err = runTool(t, "create_provider_key", &Deps{Governance: fake, ProviderKeys: checks}, map[string]any{"provider": "openai", "name": "k", "value": "sk-x"})
	require.NoError(t, err)
	keyID := fake.providerKeys["openai"][0].ID
	_, err = runTool(t, "update_provider_key", &Deps{Governance: fake, ProviderKeys: checks}, map[string]any{"provider": "openai", "key_id": keyID, "enabled": false})
	require.NoError(t, err)
	require.Equal(t, []string{"validate:k:true", "validate:k:true", "added:k", "validate:k:false", "updated:k"}, checks.calls, "the rejected call validated and stopped; the next two validated, stored and refreshed")

	_, err = runTool(t, "create_provider_key", &Deps{Governance: fake}, map[string]any{"provider": "openai", "name": "k2", "value": "sk-y"})
	require.ErrorContains(t, err, "cannot be validated")
}

// update_plugin merges the incoming config over the stored one, as the HTTP
// handler does, so a key the caller did not send survives; and the config is
// checked against the plugin's typed config before it is stored.
func TestUpdatePluginMergesAndNormalizesConfig(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
		"otel": {Name: "otel", Enabled: true, Config: map[string]any{"url": "https://old", "plugin_span_filter": "llm"}},
	}}
	runtime := &recordingPluginRuntime{}
	_, err := runTool(t, "update_plugin", &Deps{Governance: fake, PluginRuntime: runtime}, map[string]any{"name": "otel", "config": map[string]any{"url": "https://new"}})
	require.NoError(t, err)
	config := fake.plugins["otel"].Config.(map[string]any)
	require.Equal(t, "https://new", config["url"])
	require.Equal(t, "llm", config["plugin_span_filter"], "a key the caller did not send is kept")

	runtime.normalizeErr = errors.New("url: invalid")
	_, err = runTool(t, "update_plugin", &Deps{Governance: fake, PluginRuntime: runtime}, map[string]any{"name": "otel", "config": map[string]any{"url": "::"}})
	require.ErrorContains(t, err, "invalid plugin configuration")
	require.Equal(t, "https://new", fake.plugins["otel"].Config.(map[string]any)["url"], "a config that fails the typed check is not stored")
}

func TestDeleteToolsAreNotDeclared(t *testing.T) {
	for _, name := range []string{
		"delete_provider_key",
		"delete_mcp_client",
		"delete_virtual_mcp",
		"delete_routing_rule",
		"delete_rate_limit",
		"delete_model_config",
		"delete_plugin",
		"delete_pricing_override",
		"delete_webhook",
		"delete_virtual_key",
		"delete_team",
		"delete_customer",
	} {
		_, ok := toolByName(buildTools(), name)
		require.False(t, ok, "%s must not be hosted", name)
	}
}

func TestCreateRoutingRuleRequiresWeightsSumToOne(t *testing.T) {
	fake := &fakeGovernanceReader{}
	deps := &Deps{Governance: fake}
	_, err := runTool(t, "create_routing_rule", deps, map[string]any{
		"name": "split",
		"targets": []any{
			map[string]any{"provider": "openai", "weight": 0.5},
			map[string]any{"provider": "anthropic", "weight": 0.3},
		},
	})
	require.ErrorContains(t, err, "sum to 1")
}

// fakeCEL accepts any expression except one containing "!!", standing in for
// the routing plugin's compiler.
type fakeCEL struct{}

func (fakeCEL) ValidateCELExpression(expression string) error {
	if strings.Contains(expression, "!!") {
		return errors.New("syntax error")
	}
	return nil
}

func TestCreateRoutingRulePersistsTargets(t *testing.T) {
	fake := &fakeGovernanceReader{}
	deps := &Deps{Governance: fake, RoutingCEL: fakeCEL{}}
	result, err := runTool(t, "create_routing_rule", deps, map[string]any{
		"name":           "prod-split",
		"cel_expression": `provider == "openai"`,
		"targets": []any{
			map[string]any{"provider": "openai", "model": "gpt-4o", "weight": 1.0},
		},
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "prod-split", out["name"])
	require.Equal(t, "global", out["scope"])
	require.Len(t, fake.routing, 1)
}

// Rule fallbacks are RoutingFallback entries that can pin a provider key. The
// tools take and show the "provider/model" strings, and describe must not drop
// a pin set elsewhere (the UI or config.json) by flattening it to a string.
func TestRoutingRuleFallbacksRoundTrip(t *testing.T) {
	target := []any{map[string]any{"provider": "openai", "model": "gpt-4o", "weight": 1.0}}
	fake := &fakeGovernanceReader{}
	deps := &Deps{Governance: fake, RoutingCEL: fakeCEL{}}

	_, err := runTool(t, "create_routing_rule", deps, map[string]any{
		"name": "r", "targets": target, "fallbacks": []any{"anthropic/claude-sonnet-4", "azure/"},
	})
	require.NoError(t, err)
	require.Len(t, fake.routing, 1)
	var created *tables.TableRoutingRule
	for _, rule := range fake.routing {
		created = rule
	}
	require.Equal(t, []string{"anthropic/claude-sonnet-4", "azure/"}, tables.RoutingFallbackStrings(created.ParsedFallbacks))
	require.Equal(t, schemas.ModelProvider("azure"), created.ParsedFallbacks[1].Provider)
	require.Empty(t, created.ParsedFallbacks[1].Model, "\"azure/\" keeps the incoming model")

	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"rule_id": created.ID, "fallbacks": []any{"openai/gpt-4o-mini"}})
	require.NoError(t, err)
	require.Equal(t, []string{"openai/gpt-4o-mini"}, tables.RoutingFallbackStrings(created.ParsedFallbacks))

	created.ParsedFallbacks = append(created.ParsedFallbacks, tables.RoutingFallback{
		Fallback: schemas.Fallback{Provider: "openai", Model: "gpt-4o", KeyID: "key-1"},
	})
	result, err := runTool(t, "describe_routing_rule", deps, map[string]any{"rule_id": created.ID})
	require.NoError(t, err)
	body, err := json.Marshal(result.(map[string]any)["fallbacks"])
	require.NoError(t, err)
	require.JSONEq(t, `["openai/gpt-4o-mini", {"provider": "openai", "model": "gpt-4o", "key_id": "key-1"}]`, string(body))
}

// An empty cel_expression means "match everything in scope", so it is a value,
// not a missing one: create accepts it and update uses it to clear the match.
func TestRoutingRuleCELExpressionAcceptsEmpty(t *testing.T) {
	target := []any{map[string]any{"provider": "openai", "model": "gpt-4o", "weight": 1.0}}
	fake := &fakeGovernanceReader{}
	deps := &Deps{Governance: fake, RoutingCEL: fakeCEL{}}

	_, err := runTool(t, "create_routing_rule", deps, map[string]any{"name": "r", "targets": target, "cel_expression": ""})
	require.NoError(t, err)
	_, err = runTool(t, "create_routing_rule", deps, map[string]any{"name": "r", "targets": target, "cel_expression": 42.0})
	require.ErrorContains(t, err, "cel_expression must be a string")

	fake.routing = map[string]*tables.TableRoutingRule{
		"rr-1": {ID: "rr-1", Name: "r", CelExpression: `model == "gpt-4o"`, Targets: []tables.TableRoutingTarget{{Provider: ptr("openai"), Weight: 1}}},
	}
	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"rule_id": "rr-1", "name": "renamed"})
	require.NoError(t, err)
	require.Equal(t, `model == "gpt-4o"`, fake.routing["rr-1"].CelExpression, "an absent key keeps the stored expression")

	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"rule_id": "rr-1", "cel_expression": 42.0})
	require.ErrorContains(t, err, "cel_expression must be a string")

	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"rule_id": "rr-1", "cel_expression": ""})
	require.NoError(t, err)
	require.Empty(t, fake.routing["rr-1"].CelExpression, "an explicit empty string clears the expression")
}

func TestCreateRateLimitAttachesToTeam(t *testing.T) {
	fake := &fakeGovernanceReader{teams: map[string]*tables.TableTeam{
		"team-1": {ID: "team-1", Name: "ops"},
	}}
	deps := &Deps{Governance: fake}
	result, err := runTool(t, "create_rate_limit", deps, map[string]any{
		"owner_type":             "team",
		"owner_id":               "team-1",
		"request_max_limit":      float64(100),
		"request_reset_duration": "1h",
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "team", out["owner_type"])
	require.Equal(t, int64(100), out["request_max_limit"])
	require.NotEmpty(t, fake.teams["team-1"].RateLimitID)
}

// An owner that already has a rate limit is not silently re-pointed at a new
// one: the old row would be orphaned and the caller meant update_rate_limit.
func TestCreateRateLimitRefusesOwnerWithExistingLimit(t *testing.T) {
	fake := &fakeGovernanceReader{
		teams:     map[string]*tables.TableTeam{"team-1": {ID: "team-1", RateLimitID: ptr("rl-old")}},
		customers: map[string]*tables.TableCustomer{"cust-1": {ID: "cust-1", RateLimitID: ptr("rl-old")}},
		byID:      map[string]*tables.TableVirtualKey{"vk-1": {ID: "vk-1", RateLimitID: ptr("rl-old")}},
	}
	for ownerType, ownerID := range map[string]string{"team": "team-1", "customer": "cust-1", "virtual_key": "vk-1"} {
		_, err := runTool(t, "create_rate_limit", &Deps{Governance: fake}, map[string]any{
			"owner_type":             ownerType,
			"owner_id":               ownerID,
			"request_max_limit":      float64(100),
			"request_reset_duration": "1h",
		})
		require.ErrorContains(t, err, `already has rate limit "rl-old"`, ownerType)
		require.ErrorContains(t, err, "update_rate_limit", ownerType)
	}
	require.Empty(t, fake.rateLimits)
	require.Equal(t, "rl-old", *fake.teams["team-1"].RateLimitID)
	require.Equal(t, "rl-old", *fake.customers["cust-1"].RateLimitID)
	require.Equal(t, "rl-old", *fake.byID["vk-1"].RateLimitID)
}

func TestCreateModelConfigGlobal(t *testing.T) {
	fake := &fakeGovernanceReader{}
	deps := &Deps{Governance: fake}
	result, err := runTool(t, "create_model_config", deps, map[string]any{
		"model_name":           "gpt-4o",
		"provider":             "openai",
		"max_limit":            50.0,
		"reset_duration":       "1d",
		"token_max_limit":      float64(1000),
		"token_reset_duration": "1h",
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "gpt-4o", out["model_name"])
	require.Equal(t, "global", out["scope"])
	require.Len(t, fake.modelConfigs, 1)
}

// A duplicate model config must not leave the nested rate limit it created
// first behind as an orphan row.
func TestCreateModelConfigRollsBackRateLimitOnFailure(t *testing.T) {
	fake := &fakeGovernanceReader{createModelConfigErr: configstore.ErrAlreadyExists}
	_, err := runTool(t, "create_model_config", &Deps{Governance: fake}, map[string]any{
		"model_name":           "gpt-4o",
		"token_max_limit":      float64(1000),
		"token_reset_duration": "1h",
	})
	require.ErrorContains(t, err, "already exists")
	require.Empty(t, fake.rateLimits)
	require.Empty(t, fake.modelConfigs)
}

// A bad budget is rejected before anything is written.
func TestCreateModelConfigValidatesBudgetBeforeWriting(t *testing.T) {
	fake := &fakeGovernanceReader{}
	_, err := runTool(t, "create_model_config", &Deps{Governance: fake}, map[string]any{
		"model_name":           "gpt-4o",
		"max_limit":            50.0,
		"reset_duration":       "soon",
		"token_max_limit":      float64(1000),
		"token_reset_duration": "1h",
	})
	require.ErrorContains(t, err, "invalid reset_duration")
	require.Empty(t, fake.rateLimits)
	require.Empty(t, fake.modelConfigs)
}

// An unknown owner is caught before the rate limit row is created.
func TestCreateRateLimitUnknownOwnerWritesNothing(t *testing.T) {
	fake := &fakeGovernanceReader{}
	_, err := runTool(t, "create_rate_limit", &Deps{Governance: fake}, map[string]any{
		"owner_type":             "team",
		"owner_id":               "missing",
		"request_max_limit":      float64(100),
		"request_reset_duration": "1h",
	})
	require.ErrorContains(t, err, `no team with id "missing"`)
	require.Empty(t, fake.rateLimits)
}

type fakeRateLimitReloader struct {
	GovernanceReloader
	reloaded []string
}

func (r *fakeRateLimitReloader) ReloadRateLimit(ctx context.Context, id string) error {
	r.reloaded = append(r.reloaded, id)
	return nil
}

// Enforcement reads rate limits from the in-memory store, so an update that
// only reached the database would not take effect until restart.
func TestUpdateRateLimitRefreshesInMemoryStore(t *testing.T) {
	limit := int64(10)
	reset := "1h"
	fake := &fakeGovernanceReader{rateLimits: map[string]*tables.TableRateLimit{
		"rl-1": {ID: "rl-1", RequestMaxLimit: &limit, RequestResetDuration: &reset},
	}}
	reloader := &fakeRateLimitReloader{}
	_, err := runTool(t, "update_rate_limit", &Deps{Governance: fake, Reloader: reloader}, map[string]any{
		"rate_limit_id":          "rl-1",
		"request_max_limit":      float64(20),
		"request_reset_duration": "1h",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"rl-1"}, reloader.reloaded)
}

// Plugin config is free-form and routinely carries provider or backend
// credentials; describe_plugin must not hand them to an MCP caller.
func TestDescribePluginRedactsCredentialConfig(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
		"otel": {Name: "otel", Enabled: true, Config: map[string]any{
			"collector_url": "https://otel.example.com",
			"api_key":       "sk-must-not-leak",
			"max_tokens":    float64(512),
			"headers":       map[string]any{"Authorization": "Bearer also-secret", "x-team": "ops"},
			"password":      "env.OTEL_PASSWORD",
		}},
	}}
	result, err := runTool(t, "describe_plugin", &Deps{Governance: fake}, map[string]any{"name": "otel"})
	require.NoError(t, err)
	serialized := boundToolResult(result)
	require.NotContains(t, serialized, "sk-must-not-leak")
	require.NotContains(t, serialized, "also-secret")
	config := result.(map[string]any)["config"].(map[string]any)
	require.Equal(t, "https://otel.example.com", config["collector_url"])
	require.Equal(t, float64(512), config["max_tokens"])
	require.Equal(t, "ops", config["headers"].(map[string]any)["x-team"])
	require.Equal(t, "env.OTEL_PASSWORD", config["password"])
}

// The plugin loader executes whatever file path it is given, so MCP callers
// must not be able to choose one.
func TestPluginToolsRejectPath(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{"custom": {Name: "custom"}}}
	deps := &Deps{Governance: fake}
	_, err := runTool(t, "create_plugin", deps, map[string]any{"name": "evil", "path": "/tmp/evil.so"})
	require.ErrorContains(t, err, "path cannot be set over MCP")
	_, err = runTool(t, "update_plugin", deps, map[string]any{"name": "custom", "path": "../../evil.so"})
	require.ErrorContains(t, err, "path cannot be set over MCP")
	require.Nil(t, fake.plugins["custom"].Path)
	require.NotContains(t, fake.plugins, "evil")
}

type recordingProviderRuntime struct {
	ProviderRuntime
	updatedKeys []schemas.Key
}

func (r *recordingProviderRuntime) UpdateProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, key schemas.Key) error {
	r.updatedKeys = append(r.updatedKeys, key)
	return nil
}

// Without the config store there is no stored key to patch, and replacing the
// live key with {ID} alone would drop its value and models.
// update_provider validates each supplied value and the merged pair, keeps the
// unsupplied half as stored, and never touches Description, which is reserved
// for discovery errors.
func TestUpdateProviderValidatesConcurrencyAndBuffer(t *testing.T) {
	newFake := func() *fakeGovernanceReader {
		return &fakeGovernanceReader{providerCfgs: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {
				ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 4, BufferSize: 10},
				Description:              "discovery failed: 401",
			},
		}}
	}
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"concurrency": float64(0)}, "concurrency must be at least 1"},
		{map[string]any{"buffer_size": float64(-1)}, "buffer_size must be at least 1"},
		{map[string]any{"concurrency": float64(20)}, "concurrency (20) must not exceed buffer_size (10)"},
		{map[string]any{"buffer_size": float64(2)}, "concurrency (4) must not exceed buffer_size (2)"},
	} {
		fake := newFake()
		tc.args["provider"] = "openai"
		_, err := runTool(t, "update_provider", &Deps{Governance: fake}, tc.args)
		require.ErrorContains(t, err, tc.want)
		require.Equal(t, 4, fake.providerCfgs["openai"].ConcurrencyAndBufferSize.Concurrency, "a rejected update writes nothing")
	}

	fake := newFake()
	result, err := runTool(t, "update_provider", &Deps{Governance: fake}, map[string]any{
		"provider": "openai", "concurrency": float64(8), "description": "caller text",
	})
	require.NoError(t, err)
	stored := fake.providerCfgs["openai"]
	require.Equal(t, schemas.ConcurrencyAndBufferSize{Concurrency: 8, BufferSize: 10}, *stored.ConcurrencyAndBufferSize)
	require.Equal(t, "discovery failed: 401", stored.Description)
	require.NotContains(t, result.(map[string]any), "description")
}

func TestUpdateProviderKeyRequiresConfigStore(t *testing.T) {
	runtime := &recordingProviderRuntime{}
	_, err := runTool(t, "update_provider_key", &Deps{ProviderRuntime: runtime}, map[string]any{
		"provider": "openai",
		"key_id":   "key-1",
		"weight":   0.5,
	})
	require.Error(t, err)
	require.Empty(t, runtime.updatedKeys)
}

type failingWebhookReloader struct {
	GovernanceReloader
}

func (failingWebhookReloader) ReloadWebhookEndpoint(ctx context.Context, id string) error {
	return errors.New("reload boom")
}

// The signing secret is only ever returned by create_webhook. A reload failure
// after the row is stored must not turn into an error result that drops it.
func TestCreateWebhookKeepsSecretWhenReloadFails(t *testing.T) {
	fake := &fakeGovernanceReader{}
	result, err := runTool(t, "create_webhook", &Deps{Governance: fake, Reloader: failingWebhookReloader{}}, map[string]any{
		"name":   "alerts",
		"url":    "https://example.com/hook",
		"events": []any{string(tables.WebhookEventAsyncJobCompleted)},
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "whsec_test", out["secret"])
	require.Contains(t, out["warning"], "reload boom")
	require.Contains(t, out["note"], "only time the signing secret is returned")
}

// Without the in-memory flag store nothing checks the flag is registered or
// file-locked, so the write is refused instead of persisted unvalidated.
func TestUpdateFeatureFlagRequiresFlagStore(t *testing.T) {
	fake := &fakeGovernanceReader{}
	_, err := runTool(t, "update_feature_flag", &Deps{Governance: fake}, map[string]any{"id": "not-a-flag", "enabled": true})
	require.ErrorContains(t, err, "feature flag store is not available")
	require.Empty(t, fake.flags)
}

// A failed model-config update must roll back the rate limit it created for it.
func TestUpdateModelConfigRollsBackRateLimitOnFailure(t *testing.T) {
	fake := &fakeGovernanceReader{
		modelConfigs:         map[string]*tables.TableModelConfig{"mc-1": {ID: "mc-1", ModelName: "gpt-4o", Scope: tables.ModelConfigScopeGlobal}},
		updateModelConfigErr: errors.New("write boom"),
	}
	_, err := runTool(t, "update_model_config", &Deps{Governance: fake}, map[string]any{
		"model_config_id":        "mc-1",
		"request_max_limit":      float64(10),
		"request_reset_duration": "1h",
	})
	require.ErrorContains(t, err, "write boom")
	require.Empty(t, fake.rateLimits)
	require.Nil(t, fake.modelConfigs["mc-1"].RateLimitID, "the owning row must not keep a link to the rolled-back rate limit")
}

// describe_plugin redacts secrets, so a caller that edits the described config
// and sends it back must not overwrite the stored secrets with the placeholder.
func TestUpdatePluginKeepsSecretsBehindRedactionPlaceholder(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
		"otel": {Name: "otel", Config: map[string]any{
			"api_key": "sk-stored",
			"headers": map[string]any{"Authorization": "Bearer stored"},
			"url":     "https://old.example.com",
		}},
	}}
	deps := &Deps{Governance: fake}
	_, err := runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "config": map[string]any{
		"api_key": schemas.RedactedAttrValue,
		"headers": map[string]any{"Authorization": schemas.RedactedAttrValue},
		"url":     "https://new.example.com",
	}})
	require.NoError(t, err)
	config := fake.plugins["otel"].Config.(map[string]any)
	require.Equal(t, "sk-stored", config["api_key"])
	require.Equal(t, "Bearer stored", config["headers"].(map[string]any)["Authorization"])
	require.Equal(t, "https://new.example.com", config["url"])

	_, err = runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "config": map[string]any{
		"password": schemas.RedactedAttrValue,
	}})
	require.ErrorContains(t, err, "config.password is the redaction placeholder")
}

// Index-based restoration is only safe when the array lines up with the
// stored one; a shortened array would shift secrets onto the wrong entries.
func TestUpdatePluginRejectsPlaceholderInResizedArray(t *testing.T) {
	stored := func() *fakeGovernanceReader {
		return &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
			"otel": {Name: "otel", Config: map[string]any{"backends": []any{
				map[string]any{"name": "a", "token": "tok-a"},
				map[string]any{"name": "b", "token": "tok-b"},
			}}},
		}}
	}
	fake := stored()
	_, err := runTool(t, "update_plugin", &Deps{Governance: fake}, map[string]any{"name": "otel", "config": map[string]any{
		"backends": []any{map[string]any{"name": "b", "token": schemas.RedactedAttrValue}},
	}})
	require.ErrorContains(t, err, "config.backends has 1 items but the stored value has 2")
	require.Equal(t, "tok-a", fake.plugins["otel"].Config.(map[string]any)["backends"].([]any)[0].(map[string]any)["token"])

	// A resized array with real values only is an ordinary replacement.
	fake = stored()
	_, err = runTool(t, "update_plugin", &Deps{Governance: fake}, map[string]any{"name": "otel", "config": map[string]any{
		"backends": []any{map[string]any{"name": "c", "token": "tok-c"}},
	}})
	require.NoError(t, err)

	// Same length still restores by index.
	fake = stored()
	_, err = runTool(t, "update_plugin", &Deps{Governance: fake}, map[string]any{"name": "otel", "config": map[string]any{
		"backends": []any{
			map[string]any{"name": "a", "token": schemas.RedactedAttrValue},
			map[string]any{"name": "b2", "token": schemas.RedactedAttrValue},
		},
	}})
	require.NoError(t, err)
	backends := fake.plugins["otel"].Config.(map[string]any)["backends"].([]any)
	require.Equal(t, "tok-b", backends[1].(map[string]any)["token"])
}

type recordingPluginRuntime struct {
	calls        []string
	normalizeErr error
}

func (r *recordingPluginRuntime) NormalizePluginConfig(name string, config map[string]any) (map[string]any, error) {
	return nil, r.normalizeErr
}

func (r *recordingPluginRuntime) ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any, placement *schemas.PluginPlacement, order *int) error {
	r.calls = append(r.calls, "reload:"+name)
	return nil
}

func (r *recordingPluginRuntime) DisablePlugin(ctx context.Context, name string) error {
	r.calls = append(r.calls, "disable:"+name)
	return nil
}

// The live process follows the stored enabled flag: switching a plugin off
// stops it rather than reloading it, and a plugin created disabled is not
// started.
func TestPluginToolsRespectEnabledFlag(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{"otel": {Name: "otel", Enabled: true}}}
	runtime := &recordingPluginRuntime{}
	deps := &Deps{Governance: fake, PluginRuntime: runtime}

	_, err := runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "enabled": false})
	require.NoError(t, err)
	_, err = runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "enabled": true})
	require.NoError(t, err)
	_, err = runTool(t, "create_plugin", deps, map[string]any{"name": "cache", "enabled": false})
	require.NoError(t, err)
	_, err = runTool(t, "create_plugin", deps, map[string]any{"name": "jsonparser"})
	require.NoError(t, err)

	require.Equal(t, []string{"disable:otel", "reload:otel", "disable:cache", "reload:jsonparser"}, runtime.calls)
}

func TestListMCPAuthSessionsOmitsTokens(t *testing.T) {
	fake := &fakeGovernanceReader{
		oauthTokens: []tables.TableMCPOauthToken{{
			SessionID:    "sess-replayable",
			ID:           "tok-1",
			AuthMode:     "user",
			MCPClientID:  "client-1",
			Status:       "active",
			AccessToken:  "secret-access",
			RefreshToken: "secret-refresh",
		}},
	}
	deps := &Deps{Governance: fake}
	result, err := runTool(t, "list_mcp_auth_sessions", deps, map[string]any{})
	require.NoError(t, err)
	serialized := boundToolResult(result)
	require.NotContains(t, serialized, "secret-access")
	require.NotContains(t, serialized, "secret-refresh")
	// The session id selects the credential, so it is a secret too.
	require.NotContains(t, serialized, "sess-replayable")
	require.Equal(t, true, result.(map[string]any)["sessions"].([]map[string]any)[0]["session_bound"])
	rows := result.(map[string]any)["sessions"].([]map[string]any)
	require.Equal(t, "oauth", rows[0]["kind"])
	require.Equal(t, true, rows[0]["has_refresh"])
}

func TestGetWarpConfigUnavailableWithoutStore(t *testing.T) {
	_, err := runTool(t, "get_warp_config", &Deps{}, map[string]any{})
	require.ErrorContains(t, err, "not available")
}

type recordingWarpSaver struct {
	store  *fakeWarpStore
	bodies []map[string]any
	fail   error
}

func (w *recordingWarpSaver) SaveConfigJSON(ctx context.Context, body []byte) error {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	w.bodies = append(w.bodies, decoded)
	if w.fail != nil {
		return w.fail
	}
	row := &tables.TableWarpConfig{}
	if w.store.row != nil {
		copied := *w.store.row
		row = &copied
	}
	row.Enabled, _ = decoded["enabled"].(bool)
	row.Provider, _ = decoded["provider"].(string)
	row.Model, _ = decoded["model"].(string)
	if dimension, ok := decoded["embedding_dimension"].(float64); ok {
		row.EmbeddingDimension = int(dimension)
	}
	if namespace, ok := decoded["log_vector_store_namespace"].(string); ok {
		row.LogVectorStoreNamespace = namespace
	}
	w.store.row = row
	return nil
}

// update_warp_config saves through Warp's own validation, sending the stored
// settings with the patch applied and naming only the embedding fields the
// caller set, so the rest keep their stored values instead of resetting.
func TestUpdateWarpConfigPersists(t *testing.T) {
	suffix := "be brief"
	store := &fakeWarpStore{row: &tables.TableWarpConfig{
		Provider: "anthropic", Model: "old", HistoryRetentionDays: 30, SystemPromptSuffix: &suffix,
		EmbeddingModel: "text-embedding-3-small",
	}}
	saver := &recordingWarpSaver{store: store}
	deps := &Deps{Warp: store, WarpConfig: saver}
	result, err := runTool(t, "update_warp_config", deps, map[string]any{
		"enabled":  true,
		"provider": "openai",
		"model":    "gpt-4o",
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, true, out["enabled"])
	require.Equal(t, "openai", store.row.Provider)
	body := saver.bodies[0]
	require.Equal(t, float64(30), body["history_retention_days"], "settings the tool does not expose are carried, not wiped")
	require.Equal(t, "be brief", body["system_prompt_suffix"])
	require.NotContains(t, body, "embedding_model", "unset embedding fields are left to the stored row")

	_, err = runTool(t, "update_warp_config", deps, map[string]any{"embedding_model": "text-embedding-3-large"})
	require.NoError(t, err)
	require.Equal(t, "text-embedding-3-large", saver.bodies[1]["embedding_model"])

	saver.fail = errors.New("invalid warp config: base_url must be an absolute http or https URL")
	_, err = runTool(t, "update_warp_config", deps, map[string]any{"base_url": "ftp://x"})
	require.ErrorContains(t, err, "base_url must be")

	_, err = runTool(t, "update_warp_config", &Deps{Warp: store}, map[string]any{"enabled": false})
	require.ErrorContains(t, err, "cannot be validated")
}

// Changing the embedding model can also change the vector dimension, and Warp
// then requires a new namespace; both are settable here, validated and sent
// only when named, and reported back.
func TestUpdateWarpConfigEmbeddingSpace(t *testing.T) {
	newDeps := func() (*Deps, *fakeWarpStore, *recordingWarpSaver) {
		store := &fakeWarpStore{row: &tables.TableWarpConfig{
			EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
			EmbeddingDimension: 1536, LogVectorStoreNamespace: "warp_logs",
		}}
		saver := &recordingWarpSaver{store: store}
		return &Deps{Warp: store, WarpConfig: saver}, store, saver
	}

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"embedding_dimension": float64(0)}, "embedding_dimension must be at least 1"},
		{map[string]any{"embedding_dimension": "3072"}, "embedding_dimension must be"},
		{map[string]any{"log_vector_store_namespace": "  "}, "log_vector_store_namespace must not be empty"},
	} {
		deps, _, saver := newDeps()
		_, err := runTool(t, "update_warp_config", deps, tc.args)
		require.ErrorContains(t, err, tc.want)
		require.Empty(t, saver.bodies, "a rejected value is never sent")
	}

	deps, store, saver := newDeps()
	result, err := runTool(t, "update_warp_config", deps, map[string]any{
		"embedding_model":            "text-embedding-3-large",
		"embedding_dimension":        float64(3072),
		"log_vector_store_namespace": "warp_logs_large",
	})
	require.NoError(t, err)
	require.Equal(t, float64(3072), saver.bodies[0]["embedding_dimension"])
	require.Equal(t, "warp_logs_large", saver.bodies[0]["log_vector_store_namespace"])
	out := result.(map[string]any)
	require.Equal(t, 3072, out["embedding_dimension"])
	require.Equal(t, "warp_logs_large", out["log_vector_store_namespace"])
	require.Equal(t, "warp_logs_large", store.row.LogVectorStoreNamespace)

	_, err = runTool(t, "update_warp_config", deps, map[string]any{"enabled": false})
	require.NoError(t, err)
	require.NotContains(t, saver.bodies[1], "embedding_dimension", "unnamed, the stored dimension is left to the stored row")
	require.NotContains(t, saver.bodies[1], "log_vector_store_namespace")
}

// A null semantic_search_threshold is "not set", like an absent one.
func TestUpdateWarpConfigIgnoresNullThreshold(t *testing.T) {
	store := &fakeWarpStore{row: &tables.TableWarpConfig{SemanticSearchThreshold: 0.8}}
	saver := &recordingWarpSaver{store: store}
	_, err := runTool(t, "update_warp_config", &Deps{Warp: store, WarpConfig: saver}, map[string]any{
		"semantic_search_threshold": nil, "enabled": false,
	})
	require.NoError(t, err)
	require.NotContains(t, saver.bodies[0], "semantic_search_threshold")

	_, err = runTool(t, "update_warp_config", &Deps{Warp: store, WarpConfig: saver}, map[string]any{"semantic_search_threshold": 0.5})
	require.NoError(t, err)
	require.Equal(t, 0.5, saver.bodies[1]["semantic_search_threshold"])
}

func TestListWebhooksNeverReturnsSecret(t *testing.T) {
	secret := schemas.NewSecretVar("whsec_must_not_leak")
	fake := &fakeGovernanceReader{webhooks: map[string]*tables.TableWebhookEndpoint{
		"wh-1": {ID: "wh-1", Name: "alerts", URL: "https://example.com/hook", Secret: secret, Events: []tables.WebhookEvent{tables.WebhookEventAsyncJobCompleted}},
	}}
	result, err := runTool(t, "list_webhooks", &Deps{Governance: fake}, map[string]any{})
	require.NoError(t, err)
	require.NotContains(t, boundToolResult(result), "whsec_must_not_leak")
}

type fakeWarpStore struct {
	row *tables.TableWarpConfig
}

func (f *fakeWarpStore) GetWarpConfig(context.Context) (*tables.TableWarpConfig, error) {
	return f.row, nil
}

func (f *fakeWarpStore) UpsertWarpConfig(_ context.Context, config *tables.TableWarpConfig) error {
	copied := *config
	f.row = &copied
	return nil
}
