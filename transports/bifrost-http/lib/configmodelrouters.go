package lib

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"gorm.io/gorm"
)

// fileModelRouters validates config.json's model_routers entries and returns
// the valid ones, keeping the first valid declaration of an id and skipping,
// and logging, every later one. An entry that fails validation is dropped
// before its id is counted, so it cannot shadow a valid declaration of the
// same id.
func fileModelRouters(configData *ConfigData) []declaredModelRouter {
	seen := make(map[string]bool, len(configData.ModelRouters))
	declared := make([]declaredModelRouter, 0, len(configData.ModelRouters))
	for _, entry := range configData.ModelRouters {
		router, asWritten, ok := fileModelRouter(entry)
		if !ok {
			continue
		}
		if seen[router.ID] {
			logger.Error("skipping model_routers entry: id %q is declared more than once", router.ID)
			continue
		}
		seen[router.ID] = true
		declared = append(declared, declaredModelRouter{router: router, asWritten: asWritten})
	}
	return declared
}

// declaredModelRouter is one valid config.json router, normalized, with the
// config as written for the split-mode merge.
type declaredModelRouter struct {
	router    configstore.ModelRouter
	asWritten *configstore.ComplexityAnalyzerConfig
}

// fileModelRouter validates one config.json router and returns it normalized,
// with its section hashes, the targets and routing it declares, and its
// identity hash, plus the config as written for the split-mode merge.
func fileModelRouter(entry configstore.ModelRouter) (router configstore.ModelRouter, asWritten *configstore.ComplexityAnalyzerConfig, ok bool) {
	if err := configstore.ValidateModelRouterIdentity(&entry); err != nil {
		logger.Error("invalid model_routers entry in config file: %v", err)
		return configstore.ModelRouter{}, nil, false
	}
	raw := entry.ComplexityAnalyzerConfig
	normalized, asWritten, _, ok := complexityAnalyzerConfigFromFile(fmt.Sprintf("model router %q", entry.ID), &raw)
	if !ok {
		return configstore.ModelRouter{}, nil, false
	}
	if entry.Targets != nil {
		if err := configstore.ValidateModelRouterTargets(entry.Targets); err != nil {
			logger.Error("invalid targets for model router %q in config file: %v", entry.ID, err)
			return configstore.ModelRouter{}, nil, false
		}
	}
	if entry.Routing != nil {
		routing := *entry.Routing
		if err := configstore.ValidateModelRouterRouting(&routing); err != nil {
			logger.Error("invalid routing for model router %q in config file: %v", entry.ID, err)
			return configstore.ModelRouter{}, nil, false
		}
		if err := rules.ValidateModelRouterCEL(routing.CelExpression); err != nil {
			logger.Error("invalid routing condition for model router %q in config file: %v", entry.ID, err)
			return configstore.ModelRouter{}, nil, false
		}
		entry.Routing = &routing
	}
	return configstore.ModelRouter{
		ID:                       entry.ID,
		Name:                     entry.Name,
		Description:              entry.Description,
		ComplexityAnalyzerConfig: *normalized,
		Targets:                  entry.Targets,
		Routing:                  entry.Routing,
		ConfigHash:               configstore.GenerateModelRouterHash(entry),
	}, asWritten, true
}

// resolveModelRouterFallbackKeyNames replaces each target fallback's
// provider_key_name with the key_id of that provider's key, as a routing
// rule's fallbacks are resolved at load, so only the key_id is stored.
func resolveModelRouterFallbackKeyNames(ctx context.Context, store configstore.ConfigStore, router *configstore.ModelRouter) error {
	named := false
	for _, target := range router.Targets {
		for _, fallback := range target.ParsedFallbacks {
			named = named || fallback.ProviderKeyName != nil
		}
	}
	if !named {
		return nil
	}
	return store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		for i := range router.Targets {
			for j := range router.Targets[i].ParsedFallbacks {
				fallback := &router.Targets[i].ParsedFallbacks[j]
				keyName := ""
				if fallback.ProviderKeyName != nil {
					keyName = strings.TrimSpace(*fallback.ProviderKeyName)
				}
				fallback.ProviderKeyName = nil
				if keyName == "" {
					continue
				}
				if strings.TrimSpace(fallback.KeyID) != "" {
					return fmt.Errorf("targets[%d].fallbacks[%d] cannot set key_id together with provider_key_name", i, j)
				}
				if strings.TrimSpace(string(fallback.Provider)) == "" {
					return fmt.Errorf("targets[%d].fallbacks[%d] provider_key_name requires provider to be set", i, j)
				}
				keyID, err := modelRouterFallbackKeyID(ctx, tx, string(fallback.Provider), keyName)
				if err != nil {
					return fmt.Errorf("targets[%d].fallbacks[%d] provider_key_name resolution failed: %w", i, j, err)
				}
				fallback.KeyID = keyID
			}
		}
		return nil
	})
}

// modelRouterFallbackKeyID looks up the key_id of the provider's key named
// keyName, as routing rule fallbacks resolve provider_key_name.
func modelRouterFallbackKeyID(ctx context.Context, tx *gorm.DB, provider string, keyName string) (string, error) {
	var key configstoreTables.TableKey
	err := tx.WithContext(ctx).
		Model(&configstoreTables.TableKey{}).
		Select("key_id").
		Where("LOWER(provider) = LOWER(?) AND name = ?", provider, keyName).
		First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("provider key not found for provider=%q name=%q", provider, keyName)
	}
	if err != nil {
		return "", err
	}
	return key.KeyID, nil
}

// syncModelRouterTargetsFromFile applies a config.json router's targets when
// the entry declares them and they should win: always in source-of-truth mode
// when they differ from the stored ones, and otherwise only when the entry
// changed since it was last applied. An entry without targets leaves the
// stored ones alone. It reports false when the write failed.
func syncModelRouterTargetsFromFile(ctx context.Context, config *Config, id string, declared, stored []configstoreTables.TableModelRouterTarget, entryChanged, sourceOfTruth bool) bool {
	if declared == nil || (!entryChanged && !sourceOfTruth) {
		return true
	}
	if configstore.ModelRouterTargetsHash(declared) == configstore.ModelRouterTargetsHash(stored) {
		return true
	}
	if err := config.ConfigStore.SetModelRouterTargets(ctx, id, declared); err != nil {
		logger.Error("failed to sync targets of model router %q from config file; a later boot retries them: %v", id, err)
		return false
	}
	return true
}

// modelRouterRoutingToApply reports whether a config.json router's routing is
// to be written, on the same terms as its targets: the entry declares it, it
// should win (always in source-of-truth mode, otherwise only when the entry
// changed since it was last applied), and it differs from the stored routing.
// The condition builder state is the dashboard's, so it is not compared.
func modelRouterRoutingToApply(declared, stored *configstore.ModelRouterRouting, entryChanged, sourceOfTruth bool) bool {
	if declared == nil || (!entryChanged && !sourceOfTruth) {
		return false
	}
	if stored == nil {
		return true
	}
	file, current := *declared, *stored
	file.Query, current.Query = nil, nil
	return !reflect.DeepEqual(file, current)
}

// modelRouterRoutingFromFile is the routing to write for a config.json
// router. A file routing without builder state keeps the stored one while the
// condition stays as it is: the builder state describes the condition, so it
// is cleared once the condition changes.
func modelRouterRoutingFromFile(declared, stored *configstore.ModelRouterRouting) configstore.ModelRouterRouting {
	routing := *declared
	if routing.Query == nil && stored != nil && stored.CelExpression == routing.CelExpression {
		routing.Query = stored.Query
	}
	return routing
}

// pendingModelRouter is a declared router that passed its own checks, with
// what the sync worked out for it.
type pendingModelRouter struct {
	declaredModelRouter
	current      configstore.ModelRouter
	exists       bool
	entryChanged bool
	writeRouting bool   // the entry's routing is to be written
	outcome      string // what skipping the entry means, for logs
}

// dropModelRouterPriorityConflicts decides which declared routers can have
// their routing written without two routers sharing a priority in a scope.
// As for routing rules (dropRoutingRulePriorityConflicts), the check runs on
// the state after the sync, so routers can swap priorities: a router whose
// routing the sync leaves as it is wins over one it would write, and among
// routers it would write the one declared first wins. A skipped router is
// skipped whole and keeps its stored routing, which can take a priority
// another write wants, so the check repeats until nothing more is skipped. It
// returns the ids it skipped. A new router is created after every global
// priority this check lets through (see nextGlobalModelRouterPriority), so it
// never takes one of them.
func dropModelRouterPriorityConflicts(stored []configstore.ModelRouter, deleted map[string]bool, candidates []pendingModelRouter) map[string]bool {
	skipped := map[string]bool{}
	writing := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if c.writeRouting {
			writing[c.router.ID] = true
		}
	}
	if len(writing) == 0 {
		return skipped
	}
	type holder struct {
		id      string
		routing *configstore.ModelRouterRouting
		pending bool
	}
	for {
		groups := make(map[string][]holder)
		var keys []string
		add := func(h holder) {
			key := modelRouterPriorityKey(h.routing)
			if _, ok := groups[key]; !ok {
				keys = append(keys, key)
			}
			groups[key] = append(groups[key], h)
		}
		for _, router := range stored {
			if deleted[router.ID] || router.Routing == nil || (writing[router.ID] && !skipped[router.ID]) {
				continue
			}
			add(holder{id: router.ID, routing: router.Routing})
		}
		for _, c := range candidates {
			if writing[c.router.ID] && !skipped[c.router.ID] {
				add(holder{id: c.router.ID, routing: c.router.Routing, pending: true})
			}
		}

		changed := false
		skip := func(h, kept holder, alsoDeclared bool) {
			skipped[h.id] = true
			changed = true
			outcome := "skipping the router"
			for _, c := range candidates {
				if c.router.ID == h.id {
					outcome = c.outcome
				}
			}
			where := "already held by model router " + kept.id
			if alsoDeclared {
				where = "also set on model router " + kept.id + " in config.json"
			}
			logger.Warn("model router %s: priority %d in scope %s is %s; %s. Give each router in a scope its own priority", h.id, h.routing.Priority, h.routing.Scope, where, outcome)
		}
		for _, key := range keys {
			group := groups[key]
			if len(group) < 2 {
				continue
			}
			var settled, pending []holder
			for _, h := range group {
				if h.pending {
					pending = append(pending, h)
				} else {
					settled = append(settled, h)
				}
			}
			if len(pending) == 0 {
				continue
			}
			if len(settled) > 0 {
				for _, h := range pending {
					skip(h, settled[0], false)
				}
				continue
			}
			// pending is already in file order.
			for _, h := range pending[1:] {
				skip(h, pending[0], true)
			}
		}
		if !changed {
			return skipped
		}
	}
}

// nextGlobalModelRouterPriority is where the sync creates a new router: off,
// global, and after every global priority it leaves taken, those stored and
// those it writes. A router declared with routing is moved there by the
// routing batch; one declared without stays, as the store would place it,
// after the last global router. The store's own default sees only what is
// stored, so it could hand out a priority a routing in the same file needs.
func nextGlobalModelRouterPriority(stored []configstore.ModelRouter, deleted, skipped map[string]bool, candidates []pendingModelRouter) int {
	next := 0
	take := func(routing *configstore.ModelRouterRouting) {
		if routing != nil && routing.Scope == "global" && routing.Priority >= next {
			next = routing.Priority + 1
		}
	}
	for _, router := range stored {
		if !deleted[router.ID] {
			take(router.Routing)
		}
	}
	for _, c := range candidates {
		if c.writeRouting && !skipped[c.router.ID] {
			take(c.router.Routing)
		}
	}
	return next
}

// writeModelRouterRoutingsFromFile writes the routing of every declared router
// that gets one in one batch, so routers can swap priorities, and returns the
// ids whose routing was written. If the batch fails, each routing is written
// on its own, so one that cannot be written holds back only its own router.
func writeModelRouterRoutingsFromFile(ctx context.Context, config *Config, changes []configstore.ModelRouterRoutingChange) map[string]bool {
	written := make(map[string]bool, len(changes))
	if len(changes) == 0 {
		return written
	}
	err := config.ConfigStore.SetModelRouterRoutings(ctx, changes)
	if err == nil {
		for _, change := range changes {
			written[change.RouterID] = true
		}
		return written
	}
	logger.Warn("failed to sync model router routing from config file in one batch, writing each router's routing on its own: %v", err)
	for _, change := range changes {
		if err := config.ConfigStore.SetModelRouterRouting(ctx, change.RouterID, change.Routing); err != nil {
			logger.Error("failed to sync routing of model router %q from config file; a later boot retries it: %v", change.RouterID, err)
			continue
		}
		written[change.RouterID] = true
	}
	return written
}

// modelRouterPriorityKey is the slot a router's routing takes: a priority is
// unique within its scope and scope id, as the store checks it.
func modelRouterPriorityKey(routing *configstore.ModelRouterRouting) string {
	scopeID := ""
	if routing.ScopeID != nil {
		scopeID = *routing.ScopeID
	}
	return routing.Scope + "\x00" + scopeID + "\x00" + strconv.Itoa(routing.Priority)
}

// modelRouterScopeResolves reports whether a config.json router's scope
// entity exists, once governance has synced. As with routing rules,
// config.json cannot scope a router to a user.
func modelRouterScopeResolves(ctx context.Context, store configstore.ConfigStore, routing *configstore.ModelRouterRouting) bool {
	if routing.Scope == "global" {
		return true
	}
	if routing.ScopeID == nil {
		return false
	}
	var err error
	switch routing.Scope {
	case "virtual_key":
		_, err = store.GetVirtualKey(ctx, *routing.ScopeID)
	case "team":
		_, err = store.GetTeam(ctx, *routing.ScopeID)
	case "customer":
		_, err = store.GetCustomer(ctx, *routing.ScopeID)
	default:
		return false
	}
	return err == nil
}

// syncModelRoutersFromFile reconciles config.json's model routers with the
// store when the file has a model_routers section. Routers are independent of
// governance.complexity_analyzer_config, which mergeGovernanceConfig applies.
//
// In source-of-truth mode the file's config is written whenever it differs
// from the stored one, and stored routers the file does not declare are
// deleted. In split mode only the sections whose hash changed are applied,
// merged into what the store holds, so dashboard edits to the rest survive a
// reboot, and a router new to the store is created from the file as written.
// Name, description, targets and routing follow their own hash the same way;
// targets and routing are written on their own, after the router, and only
// when the entry declares them. Each router is written on its own, so one bad
// entry is logged and skipped without holding back the others. Without a
// config store there is nowhere to keep them, so the section is skipped with
// a warning. A null section decodes to a nil list and is treated as omitted;
// only an explicit `[]` prunes every router.
//
// An entry is skipped whole, as a routing rule is, when its routing cannot be
// written: its scope entity does not exist yet, or another router in the
// scope holds its priority once the sync is done (see
// dropModelRouterPriorityConflicts). A new router is then not created and a
// stored one keeps its last persisted version; its file hash is not recorded,
// so a later boot applies the entry once the scope or priority allows it. So
// is an entry whose fallback provider_key_name does not resolve. The routing
// of every entry that gets one is written last, in one batch, so routers can
// swap priorities. An entry that declares targets or routing has its file hash
// recorded only once they are written, as other config.json loaders advance a
// hash only for what they applied, so a targets or routing write that fails
// is retried by the next boot.
func syncModelRoutersFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	if config == nil || configData == nil || !configData.sectionPresent("model_routers") || configData.ModelRouters == nil {
		return
	}
	if config.ConfigStore == nil {
		logger.Warn("config file declares model_routers, but there is no config store to keep them; skipping")
		return
	}
	declared := fileModelRouters(configData)

	stored, err := config.ConfigStore.GetModelRouters(ctx)
	if err != nil {
		logger.Error("failed to read model routers for config file sync: %v", err)
		return
	}
	storedByID := make(map[string]configstore.ModelRouter, len(stored))
	for _, router := range stored {
		storedByID[router.ID] = router
	}
	sourceOfTruth := configData.isConfigJSONSourceOfTruth()

	// Undeclared routers go first, so a declared router can take the name of
	// one being removed, as routing rules are pruned before their inserts.
	deleted := map[string]bool{}
	if sourceOfTruth {
		keep := map[string]bool{}
		for _, entry := range configData.ModelRouters {
			// Every declared id is kept, valid or not: a typo in an entry must
			// not delete the router it meant to configure.
			keep[strings.TrimSpace(entry.ID)] = true
		}
		for _, router := range stored {
			if keep[router.ID] {
				continue
			}
			if err := config.ConfigStore.DeleteModelRouter(ctx, router.ID); err != nil && !errors.Is(err, configstore.ErrNotFound) {
				logger.Error("failed to delete model router %q absent from config file: %v", router.ID, err)
				continue
			}
			deleted[router.ID] = true
		}
	}

	candidates := make([]pendingModelRouter, 0, len(declared))
	for _, d := range declared {
		current, exists := storedByID[d.router.ID]
		c := pendingModelRouter{declaredModelRouter: d, current: current, exists: exists, outcome: "skipping the router"}
		if exists {
			c.outcome = "keeping its last persisted version"
		}
		if err := resolveModelRouterFallbackKeyNames(ctx, config.ConfigStore, &c.router); err != nil {
			logger.Error("model router %s: %v; %s", c.router.ID, err, c.outcome)
			continue
		}
		// Hashed with the key ids its names resolved to, as routing rules are
		// hashed after resolving theirs, so a rotated key re-applies the entry.
		c.router.ConfigHash = configstore.GenerateModelRouterHash(c.router)
		c.entryChanged = !exists || current.ConfigHash != c.router.ConfigHash
		if routing := c.router.Routing; modelRouterRoutingToApply(routing, current.Routing, c.entryChanged, sourceOfTruth) {
			if !modelRouterScopeResolves(ctx, config.ConfigStore, routing) {
				scopeID := ""
				if routing.ScopeID != nil {
					scopeID = *routing.ScopeID
				}
				logger.Warn("model router %s: scope %s with scope_id %q does not resolve to an existing entity (use the entity's id, not its name; config.json cannot scope to a user); %s", c.router.ID, routing.Scope, scopeID, c.outcome)
				continue
			}
			c.writeRouting = true
		}
		candidates = append(candidates, c)
	}
	skipped := dropModelRouterPriorityConflicts(stored, deleted, candidates)
	nextGlobal := nextGlobalModelRouterPriority(stored, deleted, skipped, candidates)

	routings := make([]configstore.ModelRouterRoutingChange, 0, len(candidates))
	// The routers whose file hash waits for their targets and routing to be
	// written, in file order, with the router as it is recorded once they are.
	var awaiting []pendingModelRouter
	awaitingRouter := make(map[string]configstore.ModelRouter, len(candidates))
	targetsFailed := make(map[string]bool)
	for _, c := range candidates {
		if skipped[c.router.ID] {
			continue
		}
		current, exists := c.current, c.exists
		next := c.router
		if exists && !sourceOfTruth {
			identityChanged := current.ConfigHash != c.router.ConfigHash
			configChanged := !current.ConfigHashes.Equal(c.router.ConfigHashes)
			if !identityChanged && !configChanged {
				continue
			}
			next = current
			if configChanged {
				merged, err := configstore.MergeComplexityAnalyzerConfigByHashes(&current.ComplexityAnalyzerConfig, c.asWritten)
				if err != nil {
					logger.Error("failed to merge model router %q from config file: %v", c.router.ID, err)
					continue
				}
				next.ComplexityAnalyzerConfig = *merged
			}
			if identityChanged {
				next.Name, next.Description, next.ConfigHash = c.router.Name, c.router.Description, c.router.ConfigHash
			}
		}
		// Targets and routing are applied on their own below, so a router is
		// created with the default routing and its routing set after.
		next.Targets, next.Routing = current.Targets, current.Routing
		applied, waits := next, (c.writeRouting || c.router.Targets != nil) && next.ConfigHash != current.ConfigHash
		if waits {
			next.ConfigHash = current.ConfigHash
		}
		err = nil
		if exists {
			if !reflect.DeepEqual(current, next) {
				err = config.ConfigStore.UpdateModelRouter(ctx, &next)
			}
		} else {
			next.Routing = &configstore.ModelRouterRouting{Scope: "global", Priority: nextGlobal}
			err = config.ConfigStore.CreateModelRouter(ctx, &next)
			if errors.Is(err, configstore.ErrModelRouterPriorityTaken) {
				// A row this version cannot read is not in the stored list, so
				// it can hold that priority; the store's default goes after it.
				next.Routing = nil
				err = config.ConfigStore.CreateModelRouter(ctx, &next)
			}
			if err == nil && next.Routing != nil && next.Routing.Scope == "global" && next.Routing.Priority >= nextGlobal {
				nextGlobal = next.Routing.Priority + 1
			}
			if errors.Is(err, configstore.ErrAlreadyExists) {
				// A stored row this version cannot read is left out of the list
				// above, and the file's declaration replaces it. Any other clash
				// is the name, held by another router.
				if _, getErr := config.ConfigStore.GetModelRouter(ctx, next.ID); errors.Is(getErr, configstore.ErrConfigUnreadable) {
					err = config.ConfigStore.UpdateModelRouter(ctx, &next)
				} else {
					err = fmt.Errorf("the name %q is already used by another model router", next.Name)
				}
			}
		}
		if err != nil {
			logger.Error("failed to sync model router %q from config file: %v", c.router.ID, err)
			continue
		}
		if waits {
			awaiting = append(awaiting, c)
			awaitingRouter[c.router.ID] = applied
		}
		if !syncModelRouterTargetsFromFile(ctx, config, c.router.ID, c.router.Targets, current.Targets, c.entryChanged, sourceOfTruth) {
			targetsFailed[c.router.ID] = true
		}
		if c.writeRouting {
			routings = append(routings, configstore.ModelRouterRoutingChange{RouterID: c.router.ID, Routing: modelRouterRoutingFromFile(c.router.Routing, current.Routing)})
		}
	}
	written := writeModelRouterRoutingsFromFile(ctx, config, routings)
	for _, c := range awaiting {
		if targetsFailed[c.router.ID] || (c.writeRouting && !written[c.router.ID]) {
			continue
		}
		applied := awaitingRouter[c.router.ID]
		if err := config.ConfigStore.UpdateModelRouter(ctx, &applied); err != nil {
			logger.Error("failed to record config file hash of model router %q; a later boot applies its entry again: %v", c.router.ID, err)
		}
	}
}
