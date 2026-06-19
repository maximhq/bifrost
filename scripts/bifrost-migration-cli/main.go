package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/scripts/bifrost-migration-cli/litellm"
)

var (
	version = "dev"
	commit  = "none"
)

// main parses CLI flags and runs all LiteLLM entity migrations in order.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("bifrost-migration-cli %s (%s)\n", version, commit)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg := NewMigrationRunConfig()
	var migrateErrors []error
	if err := runModels(ctx, cfg); err != nil {
		log.Printf("ERROR model migration: %v", err)
		migrateErrors = append(migrateErrors, err)
	}
	if err := runOrganizations(ctx, cfg); err != nil {
		log.Printf("ERROR organization migration: %v", err)
		migrateErrors = append(migrateErrors, err)
	}
	if err := runTeams(ctx, cfg); err != nil {
		log.Printf("ERROR team migration: %v", err)
		migrateErrors = append(migrateErrors, err)
	}
	if err := runUsers(ctx, cfg); err != nil {
		log.Printf("ERROR user migration: %v", err)
		migrateErrors = append(migrateErrors, err)
	}
	if err := runVKs(ctx, cfg); err != nil {
		log.Printf("ERROR virtual key migration: %v", err)
		migrateErrors = append(migrateErrors, err)
	}
	if len(migrateErrors) > 0 {
		log.Fatalf("migration completed with %d error(s); see above", len(migrateErrors))
	}
}

// MigrationRunConfig carries all CLI/runtime state shared needed for migration.
type MigrationRunConfig struct {
	LiteLLMClient     *litellm.LiteLLMClient
	BifrostClient     *BifrostClient
	LiteLLMConfigPath string
	LiteLLMDBURL      string
	LiteLLMSaltKey    string
	DefaultProvider   string
	MaxBudgetPeriod   string
	DryRun            bool
}

func requireEnv(key string) string {
	val, ok := os.LookupEnv(key)
	val = strings.TrimSpace(val)
	if ok && val != "" {
		return val
	}
	log.Fatalf("%s is required.", key)
	return ""
}

func NewMigrationRunConfig() MigrationRunConfig {
	litellmMasterKey := requireEnv("LITELLM_MASTER_KEY")

	litellmSaltKey, ok := os.LookupEnv("LITELLM_SALT_KEY")
	if !ok {
		litellmSaltKey = litellmMasterKey
	}

	defaultProvider, ok := os.LookupEnv("DEFAULT_PROVIDER")
	if !ok {
		defaultProvider = litellm.DefaultModelProvider
	}
	maxBudgetPeriod, ok := os.LookupEnv("MAX_BUDGET_PERIOD")
	if !ok {
		maxBudgetPeriod = defaultMaxBudgetPeriod
	}

	return MigrationRunConfig{
		LiteLLMClient: &litellm.LiteLLMClient{
			BaseURL: requireEnv("LITELLM_URL"),
			APIKey:  litellmMasterKey,
		},
		BifrostClient: &BifrostClient{
			BaseURL: requireEnv("BIFROST_URL"),
			APIKey:  requireEnv("BIFROST_API_KEY"),
		},
		LiteLLMConfigPath: requireEnv("LITELLM_CONFIG"),
		LiteLLMDBURL:      os.Getenv("LITELLM_DB_URL"),
		LiteLLMSaltKey:    litellmSaltKey,
		DefaultProvider:   defaultProvider,
		MaxBudgetPeriod:   maxBudgetPeriod,
		DryRun:            os.Getenv("DRY_RUN") == "1",
	}
}

// runOrganizations migrates every LiteLLM organization into a Bifrost customer. Per-org
// failures are logged and counted but do not abort the whole run, so one bad
// record never blocks the rest of the migration.
func runOrganizations(ctx context.Context, cfg MigrationRunConfig) error {
	orgs, err := cfg.LiteLLMClient.ListOrganizations(ctx)
	if err != nil {
		return err
	}
	log.Printf("fetched %d organization(s) from LiteLLM", len(orgs))

	var migrated, skipped, failed int
	for _, org := range orgs {
		customer, err := LiteLLMOrganizationToBifrostCustomer(org, cfg)
		if err != nil {
			log.Printf("SKIP org %q: %v", org.OrganizationID, err)
			skipped++
			continue
		}

		if cfg.DryRun {
			log.Printf("DRY-RUN org %q -> customer %q (budgets=%d, rateLimit=%v)",
				org.OrganizationID, customer.Name, len(customer.Budgets), customer.RateLimit != nil)
			migrated++
			continue
		}

		if err := cfg.BifrostClient.CreateCustomer(ctx, customer); err != nil {
			log.Printf("FAIL org %q: %v", org.OrganizationID, err)
			failed++
			continue
		}
		log.Printf("OK   org %q -> customer %q", org.OrganizationID, customer.Name)
		migrated++
	}

	log.Printf("done: %d migrated, %d skipped, %d failed", migrated, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d organization(s) failed to migrate", failed)
	}
	return nil
}

// runTeams migrates every LiteLLM team into a Bifrost team. A team inside an
// organization is linked to the migrated customer: team.organization_id -> the
// org's alias -> the Bifrost customer of the same name. When that customer
// cannot be resolved (org has no alias, or was not migrated yet), the team is
// created unlinked and a warning is logged rather than failing the run.
// Per-team failures are logged and counted but do not abort the whole run.
func runTeams(ctx context.Context, cfg MigrationRunConfig) error {
	teams, err := cfg.LiteLLMClient.ListTeams(ctx)
	if err != nil {
		return err
	}
	orgs, err := cfg.LiteLLMClient.ListOrganizations(ctx)
	if err != nil {
		return err
	}
	log.Printf("fetched %d team(s) and %d organization(s) from LiteLLM", len(teams), len(orgs))

	// org id -> alias, to map a team's organization onto its Bifrost customer.
	aliasByOrgID := make(map[string]string, len(orgs))
	for _, o := range orgs {
		aliasByOrgID[o.OrganizationID] = strings.TrimSpace(o.OrganizationAlias)
	}

	var migrated, skipped, failed, unlinked int
	for _, team := range teams {
		// Resolve the customer link (read-only; safe in dry-run too).
		var customerID *string
		if team.OrganizationID != nil && *team.OrganizationID != "" {
			alias := aliasByOrgID[*team.OrganizationID]
			if alias == "" {
				log.Printf("WARN team %q: organization %q not found / has no alias; creating unlinked", team.TeamAlias, *team.OrganizationID)
				unlinked++
			} else if id, ok, err := cfg.BifrostClient.FindCustomerByName(ctx, alias); err != nil {
				log.Printf("WARN team %q: resolving customer %q: %v; creating unlinked", team.TeamAlias, alias, err)
				unlinked++
			} else if !ok {
				log.Printf("WARN team %q: customer %q not migrated yet; creating unlinked", team.TeamAlias, alias)
				unlinked++
			} else {
				customerID = &id
			}
		}

		teamReq, err := LiteLLMTeamToBifrostTeam(team, customerID, cfg)
		if err != nil {
			log.Printf("SKIP team %q: %v", team.TeamID, err)
			skipped++
			continue
		}

		if cfg.DryRun {
			link := "none"
			if customerID != nil {
				link = *customerID
			}
			log.Printf("DRY-RUN team %q -> team %q (customer=%s, budgets=%d, rateLimit=%v)",
				team.TeamID, teamReq.Name, link, len(teamReq.Budgets), teamReq.RateLimit != nil)
			migrated++
			continue
		}

		if err := cfg.BifrostClient.CreateTeam(ctx, teamReq); err != nil {
			log.Printf("FAIL team %q: %v", team.TeamID, err)
			failed++
			continue
		}
		log.Printf("OK   team %q -> team %q (customer=%v)", team.TeamID, teamReq.Name, customerID != nil)
		migrated++
	}

	log.Printf("done: %d migrated, %d skipped, %d failed, %d unlinked", migrated, skipped, failed, unlinked)
	if failed > 0 {
		return fmt.Errorf("%d team(s) failed to migrate", failed)
	}
	return nil
}

// runUsers migrates every LiteLLM internal user into a Bifrost user, then links
// each user to the Bifrost teams matching its LiteLLM team memberships:
// LiteLLM user.teams (team_ids) -> team alias -> Bifrost team of the same name.
// This assumes teams were migrated first. Users without an email are skipped
// (Bifrost requires one); unresolvable team links are warned and skipped. A
// user that already exists (duplicate email) is reused so memberships still
// link. Per-user failures are logged and counted but do not abort the run.
func runUsers(ctx context.Context, cfg MigrationRunConfig) error {
	users, err := cfg.LiteLLMClient.ListUsers(ctx)
	if err != nil {
		return err
	}
	teams, err := cfg.LiteLLMClient.ListTeams(ctx)
	if err != nil {
		return err
	}
	log.Printf("fetched %d user(s) and %d team(s) from LiteLLM", len(users), len(teams))

	// LiteLLM team_id -> alias, to map a user's memberships onto Bifrost teams.
	aliasByTeamID := make(map[string]string, len(teams))
	for _, tm := range teams {
		aliasByTeamID[tm.TeamID] = strings.TrimSpace(tm.TeamAlias)
	}

	plans, report := LiteLLMUsersToBifrostUsers(users)
	printUserReport(plans, report)

	if cfg.DryRun {
		log.Printf("dry-run: %d user(s) planned, no writes performed", len(plans))
		return nil
	}

	var migrated, failed, links, linkSkipped int
	for _, p := range plans {
		userID, err := cfg.BifrostClient.CreateUser(ctx, &BifrostCreateUserRequest{Name: p.Name, Email: p.Email})
		if err != nil {
			log.Printf("FAIL user %q: %v", maskEmail(p.Email), err)
			failed++
			continue
		}
		migrated++
		log.Printf("OK   user %q -> %q", maskEmail(p.Email), userID)

		for _, srcTeamID := range p.SourceTeamIDs {
			alias := aliasByTeamID[srcTeamID]
			if alias == "" {
				log.Printf("WARN user %q: team %q not found / has no alias; skipping link", maskEmail(p.Email), srcTeamID)
				linkSkipped++
				continue
			}
			teamID, ok, err := cfg.BifrostClient.FindTeamByName(ctx, alias)
			if err != nil {
				log.Printf("WARN user %q: resolving team %q: %v; skipping link", maskEmail(p.Email), alias, err)
				linkSkipped++
				continue
			}
			if !ok {
				log.Printf("WARN user %q: team %q not migrated yet; skipping link", maskEmail(p.Email), alias)
				linkSkipped++
				continue
			}
			if err := cfg.BifrostClient.AddTeamMember(ctx, teamID, userID); err != nil {
				log.Printf("FAIL link user %q -> team %q: %v", maskEmail(p.Email), alias, err)
				failed++
				continue
			}
			links++
			log.Printf("OK   link user %q -> team %q", maskEmail(p.Email), alias)
		}
	}

	log.Printf("done: %d user(s) migrated, %d link(s), %d failed, %d link(s) skipped", migrated, links, failed, linkSkipped)
	if failed > 0 {
		return fmt.Errorf("%d user/link write(s) failed", failed)
	}
	return nil
}

func maskEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 1 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}

// printUserReport logs the planned users (with their team links) and everything
// the transform could not carry.
func printUserReport(plans []UserPlan, r UserMigrationReport) {
	for _, p := range plans {
		log.Printf("PLAN user %q (%s) -> teams=%v", p.Name, maskEmail(p.Email), p.SourceTeamIDs)
	}
	logReportSection("skipped (no email; Bifrost requires one)", r.SkippedNoEmail)
	logReportSection("dropped roles (no LiteLLM->Bifrost role mapping)", r.DroppedRoles)
	logReportSection("dropped user budgets (no field on create-user; use access profiles)", r.DroppedBudgets)
	logReportSection("dropped user rate limits (no field on create-user; use access profiles)", r.DroppedRateLimits)
}

// runVKs migrates every LiteLLM virtual key into a Bifrost virtual key. It
// reads the LiteLLM config to build a model_name -> (provider, model) index, so
// each VK's allow-list (the union of the key's, its team's and its
// organization's allowed models — Bifrost gates models only on the VK) maps to
// Bifrost provider configs. Ownership resolves to a Bifrost team (preferred) or
// customer (mutually exclusive). LiteLLM user_id, expiry and the key value
// itself have no VK-create field and are reported. Per-key failures are logged
// and counted but do not abort the run.
func runVKs(ctx context.Context, cfg MigrationRunConfig) error {
	models, err := cfg.LiteLLMClient.ListModelInfo(ctx)
	if err != nil {
		return err
	}
	litellmCfg, err := litellm.ReadLiteLLMConfig(cfg.LiteLLMConfigPath)
	if err != nil {
		return err
	}
	store, err := litellm.NewCredentialStore(ctx, cfg.LiteLLMDBURL, litellmCfg, cfg.LiteLLMSaltKey)
	if err != nil {
		return err
	}
	defer store.Close()
	deployments, err := store.Deployments(ctx)
	if err != nil {
		return err
	}
	// Build the provider/key plans to derive which keys serve which models.
	plans, _, _ := LiteLLMModelsToBifrostProviders(models, store.Named(), deployments, cfg)

	// Fetch key UUIDs from Bifrost so VKs can attach specific keys by ID.
	bifrostKeys, err := cfg.BifrostClient.ListAllKeys(ctx)
	if err != nil {
		return err
	}
	keyIDByName := make(map[string]string, len(bifrostKeys))
	for _, k := range bifrostKeys {
		keyIDByName[k.Name] = k.KeyID
	}
	keyModelIdx, wildcardKeys := BuildKeyModelIndex(plans, keyIDByName)

	// Only providers that actually exist in Bifrost can be referenced by a VK
	// (a create rejects unknown providers). For "all proxy models" VKs, expand
	// to ALL providers in Bifrost — not just those found in the model index —
	// so new providers added between migration runs are also covered.
	bifrostProviders, err := cfg.BifrostClient.ListProviders(ctx)
	if err != nil {
		return err
	}
	allProviders := make([]string, 0, len(bifrostProviders))
	for p := range bifrostProviders {
		allProviders = append(allProviders, p)
	}
	sort.Strings(allProviders)

	keys, err := cfg.LiteLLMClient.ListVirtualKeys(ctx)
	if err != nil {
		return err
	}
	teams, err := cfg.LiteLLMClient.ListTeams(ctx)
	if err != nil {
		return err
	}
	orgs, err := cfg.LiteLLMClient.ListOrganizations(ctx)
	if err != nil {
		return err
	}
	log.Printf("fetched %d virtual key(s), %d team(s), %d org(s), %d model deployment(s); key index has %d name(s), %d wildcard key(s), %d provider(s)",
		len(keys), len(teams), len(orgs), len(models), len(keyModelIdx), len(wildcardKeys), len(allProviders))

	// LiteLLM id -> (alias, allowed models, org id) for owner + model folding.
	type entity struct {
		alias  string
		models []string
		orgID  string // for teams: the owning org, so VK inherits org model restrictions
	}
	teamByID := make(map[string]entity, len(teams))
	for _, t := range teams {
		orgID := ""
		if t.OrganizationID != nil {
			orgID = *t.OrganizationID
		}
		teamByID[t.TeamID] = entity{alias: strings.TrimSpace(t.TeamAlias), models: t.Models, orgID: orgID}
	}
	orgByID := make(map[string]entity, len(orgs))
	for _, o := range orgs {
		orgByID[o.OrganizationID] = entity{alias: strings.TrimSpace(o.OrganizationAlias), models: o.Models}
	}

	var migrated, failed, skipped, unlinked int
	var unmappedModels, droppedUserLinks, droppedExpiries, droppedProviders []string

	for _, key := range keys {
		var teamModels, orgModels []string
		if key.TeamID != nil {
			te := teamByID[*key.TeamID]
			teamModels = te.models
			// Fold in the team's org model restrictions: Bifrost has no org-level
			// model gates, so any restriction the org imposes must live on the VK.
			if te.orgID != "" {
				orgModels = orgByID[te.orgID].models
			}
		}
		if key.OrgID != nil {
			orgModels = orgByID[*key.OrgID].models
		}

		plan, err := LiteLLMVirtualKeyToBifrost(key, teamModels, orgModels, keyModelIdx, wildcardKeys, allProviders, cfg.MaxBudgetPeriod)
		if err != nil {
			log.Printf("SKIP virtual key: %v", err)
			skipped++
			continue
		}

		req := &BifrostCreateVirtualKeyRequest{Name: plan.Name, IsActive: plan.IsActive, RateLimit: plan.RateLimit}
		for _, pc := range plan.ProviderConfigs {
			// A VK create rejects providers that do not exist in Bifrost (e.g.
			// one whose migration failed). Drop and report them.
			if !bifrostProviders[pc.Provider] {
				droppedProviders = append(droppedProviders, fmt.Sprintf("%s: %s", plan.Name, pc.Provider))
				continue
			}
			req.ProviderConfigs = append(req.ProviderConfigs, BifrostVKProviderConfigRequest{
				Provider:      pc.Provider,
				KeyIDs:        pc.KeyIDs,
				AllowedModels: []string{"*"},
			})
		}
		if plan.Budget != nil {
			req.Budgets = []BifrostCreateBudgetRequest{*plan.Budget}
		}

		// Resolve owner -> Bifrost team (preferred) or customer.
		owner := "none"
		switch {
		case plan.OwnerTeamID != nil:
			alias := teamByID[*plan.OwnerTeamID].alias
			if id, ok := resolveOwner(ctx, cfg.BifrostClient.FindTeamByName, alias); ok {
				req.TeamID = &id
				owner = "team:" + alias
			} else {
				log.Printf("WARN vkey %q: team %q not resolved; creating unlinked", plan.Name, alias)
				unlinked++
			}
		case plan.OwnerOrgID != nil:
			alias := orgByID[*plan.OwnerOrgID].alias
			if id, ok := resolveOwner(ctx, cfg.BifrostClient.FindCustomerByName, alias); ok {
				req.CustomerID = &id
				owner = "customer:" + alias
			} else {
				log.Printf("WARN vkey %q: customer %q not resolved; creating unlinked", plan.Name, alias)
				unlinked++
			}
		}

		if plan.SourceUserID != nil && *plan.SourceUserID != "" {
			droppedUserLinks = append(droppedUserLinks, fmt.Sprintf("%s (user %s)", plan.Name, *plan.SourceUserID))
		}
		if plan.Expiry != nil {
			droppedExpiries = append(droppedExpiries, fmt.Sprintf("%s (%s)", plan.Name, *plan.Expiry))
		}
		for _, m := range plan.UnmappedModels {
			unmappedModels = append(unmappedModels, fmt.Sprintf("%s: %s", plan.Name, m))
		}

		if cfg.DryRun {
			log.Printf("DRY-RUN vkey %q owner=%s providers=%d budgets=%d rateLimit=%v active=%v",
				plan.Name, owner, len(req.ProviderConfigs), len(req.Budgets), req.RateLimit != nil, req.IsActive)
			migrated++
			continue
		}

		if err := cfg.BifrostClient.CreateVirtualKey(ctx, req); err != nil {
			log.Printf("FAIL vkey %q: %v", plan.Name, err)
			failed++
			continue
		}
		migrated++
		log.Printf("OK   vkey %q (owner=%s, providers=%d)", plan.Name, owner, len(req.ProviderConfigs))
	}

	logReportSection("unmapped models (not in config; not granted)", unmappedModels)
	logReportSection("dropped providers (not migrated to Bifrost)", droppedProviders)
	logReportSection("dropped user links (no VK-create field; user gets VKs via access profiles)", droppedUserLinks)
	logReportSection("dropped expiries (no VK-create field)", droppedExpiries)
	if len(keys) > 0 {
		log.Printf("NOTE virtual key values are server-generated; original LiteLLM tokens are NOT carried over")
	}
	log.Printf("done: %d migrated, %d skipped, %d failed, %d unlinked", migrated, skipped, failed, unlinked)
	if failed > 0 {
		return fmt.Errorf("%d virtual key(s) failed to migrate", failed)
	}
	return nil
}

// resolveOwner looks up a Bifrost owner id by alias using the given finder,
// returning ok=false when the alias is blank or no match is found.
func resolveOwner(ctx context.Context, find func(context.Context, string) (string, bool, error), alias string) (string, bool) {
	if alias == "" {
		return "", false
	}
	id, ok, err := find(ctx, alias)
	if err != nil || !ok {
		return "", false
	}
	return id, true
}

// runModels reads a LiteLLM proxy config, transforms its model deployments into
// Bifrost providers, keys and global model configs
func runModels(ctx context.Context, cfg MigrationRunConfig) error {
	models, err := cfg.LiteLLMClient.ListModelInfo(ctx)
	if err != nil {
		return err
	}

	// The management API masks secrets, so resolve real credential values from
	// the config (plaintext) and the LiteLLM database (decrypted with the salt
	// key): named credentials by name, inline ones per deployment.
	litellmCfg, err := litellm.ReadLiteLLMConfig(cfg.LiteLLMConfigPath)
	if err != nil {
		return err
	}
	store, err := litellm.NewCredentialStore(ctx, cfg.LiteLLMDBURL, litellmCfg, cfg.LiteLLMSaltKey)
	if err != nil {
		return err
	}
	defer store.Close()
	deployments, err := store.Deployments(ctx)
	if err != nil {
		return err
	}
	log.Printf("fetched %d model deployment(s) from LiteLLM; resolved %d named credential(s) and %d deployment param set(s)", len(models), len(store.Named()), len(deployments))

	plans, modelConfigs, skippedProviders := LiteLLMModelsToBifrostProviders(models, store.Named(), deployments, cfg)
	printModelReport(plans, modelConfigs, skippedProviders)

	if cfg.DryRun {
		log.Printf("dry-run: %d provider(s), %d model config(s) planned, no writes performed", len(plans), len(modelConfigs))
		return nil
	}

	var providersOK, keysOK, modelConfigsOK, failed int
	for _, p := range plans {
		if err := cfg.BifrostClient.EnsureProvider(ctx, p); err != nil {
			log.Printf("FAIL provider %q: %v", p.Name, err)
			failed++
			continue
		}
		providersOK++
		log.Printf("OK   provider %q (custom=%v, base_url=%q)", p.Name, p.IsCustom, p.BaseURL)
		for _, k := range p.Keys {
			if err := cfg.BifrostClient.CreateProviderKey(ctx, p.Name, k); err != nil {
				log.Printf("FAIL key %q/%q: %v", p.Name, k.Name, err)
				failed++
				continue
			}
			keysOK++
			log.Printf("OK   key %q/%q (models=%v)", p.Name, k.Name, k.Models)
		}
	}
	for _, mc := range modelConfigs {
		if err := cfg.BifrostClient.CreateModelConfig(ctx, mc); err != nil {
			log.Printf("FAIL model config %q: %v", modelConfigSignature(mc), err)
			failed++
			continue
		}
		modelConfigsOK++
		log.Printf("OK   model config %q (budgets=%d, rateLimit=%v)", modelConfigSignature(mc), len(mc.Budgets), mc.RateLimit != nil)
	}

	log.Printf("done: %d provider(s), %d key(s), %d model config(s) written, %d failed", providersOK, keysOK, modelConfigsOK, failed)
	if failed > 0 {
		return fmt.Errorf("%d model migration write(s) failed", failed)
	}
	return nil
}

// printModelReport logs the planned providers/keys and everything the transform
// could not faithfully carry, so the operator can act on it.
func printModelReport(plans []ProviderPlan, modelConfigs []ModelConfigPlan, skippedProviders []SkippedProvider) {
	for _, p := range plans {
		log.Printf("PLAN provider %q (custom=%v, base_url=%q): %d key(s)", p.Name, p.IsCustom, p.BaseURL, len(p.Keys))
		for _, k := range p.Keys {
			log.Printf("       key %q models=%v", k.Name, k.Models)
		}
	}
	for _, mc := range modelConfigs {
		provider := "*"
		if mc.Provider != nil {
			provider = *mc.Provider
		}
		log.Printf("PLAN model config source=%q provider=%q model=%q budgets=%d rateLimit=%v", mc.SourceName, provider, mc.ModelName, len(mc.Budgets), mc.RateLimit != nil)
	}
	var skippedProvidersStr []string
	for _, sp := range skippedProviders {
		if sp.Reason != "" {
			skippedProvidersStr = append(skippedProvidersStr, fmt.Sprintf("%s (%s)", sp.Provider, sp.Reason))
		} else {
			skippedProvidersStr = append(skippedProvidersStr, sp.Provider)
		}
	}
	logReportSection("skipped providers", skippedProvidersStr)
}

// logReportSection prints a titled migration report section when it has items.
func logReportSection(title string, items []string) {
	if len(items) == 0 {
		return
	}
	log.Printf("REPORT %s (%d):", title, len(items))
	for _, it := range items {
		log.Printf("  - %s", it)
	}
}
