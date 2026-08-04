package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/scripts/bifrost-migration-cli/litellm"
)

// Secrets are redacted by the LiteLLM management API (/credentials masks the
// api_key, /model/info omits it entirely), so the model migration reads the
// config file directly. It carries the unredacted credential references like
// `os.environ/FOO` env refs and literal keys that Bifrost needs.

// ProviderPlan is one Bifrost provider to ensure, with the keys to add under
// it. Name is the management-API provider key (a standard provider name, or a
// generated name for a custom provider).
type ProviderPlan struct {
	Name         string
	IsCustom     bool   // true => POST /api/providers with CustomProviderConfig
	BaseProvider string // for custom providers: base_provider_type
	BaseURL      string // network_config.base_url (LiteLLM api_base)
	Keys         []KeyPlan
}

// KeyPlan is one Bifrost key under a provider. Value is the wire string Bifrost
// resolves: "env.FOO" => from environment, anything else => literal value.
type KeyPlan struct {
	Name   string
	Value  string
	Models []string // allowlist; ["*"] when the deployment serves all models
	// URL and VLLMModelName carry per-key routing for the keyless self-hosted
	// providers (vllm/ollama): these are standard Bifrost providers whose
	// server URL lives on each key, so distinct base URLs become distinct keys.
	// URL set => emit the provider-matching *_key_config. VLLMModelName sets
	// vllm_key_config.model_name (vllm selects a key by the exact served model).
	URL           string
	VLLMModelName string
	// Provider-specific structured credentials (Azure/Bedrock/Vertex).
	// Exactly one of these is set when the provider requires it.
	AzureKeyConfig   *BifrostAzureKeyConfig
	BedrockKeyConfig *BifrostBedrockKeyConfig
	VertexKeyConfig  *BifrostVertexKeyConfig
}

// ModelConfigPlan is one global Bifrost model config created from LiteLLM
// deployment-level budgets and rate limits.
type ModelConfigPlan struct {
	SourceName string
	ModelName  string
	Provider   *string
	Budgets    []BifrostCreateBudgetRequest
	RateLimit  *BifrostCreateRateLimitRequest
}

type SkippedProvider struct {
	Provider string
	Reason   string
}

type modelMigrationInput struct {
	Params       *litellm.LiteLLMModelInfoLiteLLMParams
	Deployment   *litellm.Deployment
	RawModel     string
	APIKey       string
	APIBase      string
	CredName     string
	BaseProvider string
	// Azure structured credentials
	AzureClientID     string
	AzureClientSecret string
	AzureTenantID     string
	AzureADToken      string
	// Bedrock (AWS) structured credentials
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSRegionName      string
	AWSRoleName        string // IAM role ARN for STS AssumeRole
	AWSSessionName     string
	AWSSessionToken    string
	// Vertex (GCP) structured credentials
	VertexProject     string
	VertexLocation    string
	VertexCredentials string // service-account JSON or ADC ref; empty = ADC
}

// liteLLMProviderAliases maps LiteLLM provider prefix strings that differ from
// Bifrost standard provider names. Applied in deriveProvider after lowercasing.
var liteLLMProviderAliases = map[string]string{
	"vertex_ai":              "vertex",
	"vertex_ai_beta":         "vertex",
	"hosted_vllm":            "vllm",
	"ollama_chat":            "ollama",
	"cohere_chat":            "cohere",
	"text-completion-openai": "openai",
	"azure_ai":               "azure",
	"fireworks_ai":           "fireworks",
}

// specialCredProviders are Bifrost standard providers that use structured
// credentials instead of a plain api_key: Azure (per-key endpoint + optional
// Entra ID), Bedrock (AWS IAM credentials), Vertex (GCP credentials).
// These are intercepted before the standard api_key path in
// LiteLLMModelsToBifrostProviders.
var specialCredProviders = map[string]bool{
	"azure":   true,
	"bedrock": true,
	"vertex":  true,
}

// standardProviders is the set of Bifrost standard provider names (from schemas.StandardProviders).
// A LiteLLM provider absent from this is either added as a custom provider or is skipped and reported.
var standardProviders = map[string]bool{
	"anthropic":   true,
	"azure":       true,
	"bedrock":     true,
	"cerebras":    true,
	"cohere":      true,
	"deepseek":    true,
	"elevenlabs":  true,
	"fireworks":   true,
	"gemini":      true,
	"groq":        true,
	"huggingface": true,
	"minimax":     true,
	"mistral":     true,
	"nebius":      true,
	"ollama":      true,
	"openai":      true,
	"openrouter":  true,
	"parasail":    true,
	"perplexity":  true,
	"replicate":   true,
	"runway":      true,
	"vertex":      true,
	"vllm":        true,
	"xai":         true,
}

var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

// maxProviderNameLen is the Bifrost provider-name column limit (varchar(50)).
const maxProviderNameLen = 50

// customProviders is the subset of Bifrost base providers we allow to back a
// custom provider (a deployment whose credential sets a non-standard api_base).
// A deployment whose base provider is outside this set is skipped.
var customProviders = map[string]bool{
	"openai":    true,
	"anthropic": true,
	"gemini":    true,
	"bedrock":   true,
}

// selfHostedURLProviders are keyless standard Bifrost providers that route per
// key: each carries the server URL in its *_key_config rather than at the
// provider level, so a deployment's api_base becomes a per-key URL instead of a
// custom provider. vllm additionally selects a key by the exact served model.
var selfHostedURLProviders = map[string]bool{
	"vllm":   true,
	"ollama": true,
}

// LiteLLMModelsToBifrostProviders transforms LiteLLM model deployments into
// Bifrost providers, keys and global model configs.
//
// credByName holds the resolved (decrypted) named credentials keyed by CredentialValues
//
// deployments holds the resolved per-deployment litellm_params (inline credential + budget)
// keyed by model_id (database) and, as a fallback, model_name (config).
func LiteLLMModelsToBifrostProviders(models []litellm.LiteLLMModelInfo, credByName map[string]*litellm.LiteLLMModelCredential, deployments map[string]*litellm.Deployment, cfg MigrationRunConfig) ([]ProviderPlan, []ModelConfigPlan, []SkippedProvider) {
	// keyAgg accumulates one Bifrost key's allowlist across the deployments that
	// share its credential. wildcard records a "*" deployment, which collapses
	// the allowlist to ["*"].
	type keyAgg struct {
		plan     KeyPlan
		modelSet map[string]bool
		wildcard bool
	}
	// provAgg accumulates one Bifrost provider and its keys, keyed by credential
	// signature so deployments sharing a credential fold into one key.
	type provAgg struct {
		plan     ProviderPlan
		keys     map[string]*keyAgg
		keyOrder []string
	}

	provByName := map[string]*provAgg{}
	var provOrder []string
	var skippedProviders []SkippedProvider
	skippedProviderSet := map[string]bool{}

	var modelConfigs []ModelConfigPlan
	mcIndex := map[string]int{} // modelConfigSignature -> index into modelConfigs

	// addModelConfig folds one deployment's tpm/rpm overrides into the global
	// model config for its (provider, model), keeping the strictest limits.
	addModelConfig := func(sourceName, rawModel string, b litellm.LiteLLMBudget, base, provider string) {
		mc, _, err := modelConfigPlan(sourceName, rawModel, b, base, provider, cfg.MaxBudgetPeriod)
		if err != nil || mc == nil {
			return
		}
		sig := modelConfigSignature(*mc)
		if i, ok := mcIndex[sig]; ok {
			mergeModelConfig(&modelConfigs[i], *mc)
			return
		}
		mcIndex[sig] = len(modelConfigs)
		modelConfigs = append(modelConfigs, *mc)
	}

	addSkippedProvider := func(provider, reason string) {
		provider = strings.TrimSpace(provider)
		if provider == "" {
			provider = "unknown"
		}
		sig := provider + "\x00" + reason
		if skippedProviderSet[sig] {
			return
		}
		skippedProviderSet[sig] = true
		skippedProviders = append(skippedProviders, SkippedProvider{Provider: provider, Reason: reason})
	}

	// ensureProv returns the accumulator for a provider, creating it on first
	// use. Custom providers carry their base provider + URL; standard providers
	// (including the self-hosted keyless ones) carry neither.
	ensureProv := func(name string, isCustom bool, baseProvider, baseURL string) *provAgg {
		pa := provByName[name]
		if pa == nil {
			plan := ProviderPlan{Name: name, IsCustom: isCustom}
			if isCustom {
				plan.BaseProvider = baseProvider
				plan.BaseURL = baseURL
			}
			pa = &provAgg{plan: plan, keys: map[string]*keyAgg{}}
			provByName[name] = pa
			provOrder = append(provOrder, name)
		}
		return pa
	}

	for _, m := range models {
		in, ok := resolveModelMigrationInput(m, credByName, deployments, cfg)
		if !ok {
			continue
		}
		p := in.Params
		rawModel := in.RawModel
		dep := in.Deployment
		apiKey := in.APIKey
		apiBase := in.APIBase
		credName := in.CredName
		base := in.BaseProvider

		tpm, rpm := modelInfoRateLimits(m)
		// Rate limits come from /model/info (unencrypted); the spend budget comes
		// from the deployment's litellm_params (config plaintext / DB decrypted).
		budget := litellm.LiteLLMBudget{TPMLimit: tpm, RPMLimit: rpm}
		if dep != nil {
			budget.MaxBudget = dep.MaxBudget
			budget.BudgetDuration = dep.BudgetDuration
		}

		// Full wildcard "*" routes to every provider: emit only a global,
		// all-providers model config, never a concrete provider/key.
		if rawModel == "*" {
			addModelConfig(m.ModelName, rawModel, budget, base, base)
			continue
		}

		// Unsupported / unresolvable provider (incl. "*/..." provider globs):
		// nothing to create.
		if base == "" || !standardProviders[base] {
			addSkippedProvider(base, fmt.Sprintf("unsupported or unresolvable provider for model %q", rawModel))
			continue
		}

		// Self-hosted keyless providers (vllm/ollama): a standard provider
		// whose server URL lives per key, so distinct base URLs become distinct
		// keys. The model string is the full upstream id; only a leading
		// "provider/" segment is stripped.
		if selfHostedURLProviders[base] {
			if apiBase == "" {
				addSkippedProvider(base, fmt.Sprintf("missing api_base for self-hosted model %q", rawModel))
				continue // no server URL to route to
			}
			served := stripProviderPrefix(rawModel, base)
			if served != "*" && strings.ContainsAny(served, "*?") {
				addSkippedProvider(base, fmt.Sprintf("partial wildcard model %q is not supported", rawModel))
				continue
			}
			pa := ensureProv(base, false, "", "")
			// vllm selects a key by exact served model, so each (URL, model) is
			// its own key; ollama groups by URL and unions its models.
			keySig := apiBase
			if base == "vllm" {
				keySig = apiBase + "\x00" + served
			}
			ka := pa.keys[keySig]
			if ka == nil {
				value, _ := keyValue(apiKey)
				kp := KeyPlan{Name: base + "/" + selfHostedKeyName(base, apiBase, len(pa.keyOrder)), Value: value, URL: apiBase}
				if base == "vllm" && served != "*" {
					kp.VLLMModelName = served
				}
				ka = &keyAgg{plan: kp, modelSet: map[string]bool{}}
				pa.keys[keySig] = ka
				pa.keyOrder = append(pa.keyOrder, keySig)
			}
			if served == "*" {
				ka.wildcard = true
			} else {
				ka.modelSet[served] = true
			}
			addModelConfig(m.ModelName, rawModel, budget, base, base)
			continue
		}

		// Azure, Bedrock, Vertex: per-key structured credentials.
		// Azure always intercepted here (api_base is per-key endpoint, not a
		// custom-provider URL). Bedrock/Vertex intercepted when no api_key is
		// set (IAM / GCP auth); Bedrock API-key auth falls through to the
		// standard path below.
		if base == "azure" || ((base == "bedrock" || base == "vertex") && apiKey == "") {
			model := trimModelPrefix(rawModel)
			if model == "" || (model != "*" && strings.ContainsAny(model, "*?")) {
				addSkippedProvider(base, fmt.Sprintf("partial wildcard model %q is not supported", rawModel))
				continue
			}

			pa := ensureProv(base, false, "", "")

			var credSig string
			var kp KeyPlan

			switch base {
			case "azure":
				endpoint, _ := keyValue(in.APIBase)
				if endpoint == "" {
					addSkippedProvider("azure", fmt.Sprintf("no api_base for azure model %q; endpoint required", rawModel))
					continue
				}
				apiKeyVal, _ := keyValue(apiKey)
				if apiKeyVal == "" && in.AzureClientID == "" {
					addSkippedProvider("azure", fmt.Sprintf("no api_key or azure_client_id for azure model %q", rawModel))
					continue
				}
				credSig = "azure:" + apiKey + ":" + in.APIBase + ":" + in.AzureClientID
				azCfg := &BifrostAzureKeyConfig{Endpoint: endpoint}
				if in.AzureClientID != "" {
					azCfg.ClientID = ptr(in.AzureClientID)
					if in.AzureClientSecret != "" {
						azCfg.ClientSecret = ptr(in.AzureClientSecret)
					}
					if in.AzureTenantID != "" {
						azCfg.TenantID = ptr(in.AzureTenantID)
					}
				}
				kp = KeyPlan{Name: "azure/" + azureKeyName(in.APIBase, len(pa.keyOrder)), Value: apiKeyVal, AzureKeyConfig: azCfg}

			case "bedrock":
				credSig = "bedrock:" + in.AWSAccessKeyID + ":" + in.AWSRoleName + ":" + in.AWSRegionName
				bCfg := &BifrostBedrockKeyConfig{}
				if in.AWSAccessKeyID != "" {
					bCfg.AccessKey = in.AWSAccessKeyID
					bCfg.SecretKey = in.AWSSecretAccessKey
				}
				if in.AWSSessionToken != "" {
					bCfg.SessionToken = ptr(in.AWSSessionToken)
				}
				if in.AWSRegionName != "" {
					bCfg.Region = ptr(in.AWSRegionName)
				}
				if in.AWSRoleName != "" {
					bCfg.RoleARN = ptr(in.AWSRoleName)
					if in.AWSSessionName != "" {
						bCfg.RoleSessionName = ptr(in.AWSSessionName)
					}
				}
				kp = KeyPlan{Name: "bedrock/" + bedrockKeyName(in, len(pa.keyOrder)), Value: "", BedrockKeyConfig: bCfg}

			case "vertex":
				if in.VertexProject == "" {
					addSkippedProvider("vertex", fmt.Sprintf("no vertex_project for vertex model %q", rawModel))
					continue
				}
				credSig = "vertex:" + in.VertexProject + ":" + in.VertexLocation + ":" + in.VertexCredentials
				vCfg := &BifrostVertexKeyConfig{
					ProjectID:       in.VertexProject,
					Region:          in.VertexLocation,
					AuthCredentials: in.VertexCredentials,
				}
				kp = KeyPlan{Name: "vertex/" + vertexKeyName(in, len(pa.keyOrder)), Value: "", VertexKeyConfig: vCfg}
			}

			ka := pa.keys[credSig]
			if ka == nil {
				ka = &keyAgg{plan: kp, modelSet: map[string]bool{}}
				pa.keys[credSig] = ka
				pa.keyOrder = append(pa.keyOrder, credSig)
			}
			if model == "*" {
				ka.wildcard = true
			} else {
				ka.modelSet[model] = true
			}
			addModelConfig(m.ModelName, rawModel, budget, base, base)
			continue
		}

		model := trimModelPrefix(p.Model)
		// Partial wildcard like openai/gpt-5*: not representable in Bifrost; ignore.
		if model == "" || (model != "*" && strings.ContainsAny(model, "*?")) {
			addSkippedProvider(base, fmt.Sprintf("partial wildcard model %q is not supported", rawModel))
			continue
		}

		// A credential with a custom base URL becomes a Bifrost custom provider,
		// but only for base providers Bifrost can wrap; others are ignored.
		isCustom := apiBase != ""
		provName := base
		if isCustom {
			if !customProviders[base] {
				addSkippedProvider(base, fmt.Sprintf("custom api_base is not supported for model %q", rawModel))
				continue
			}
			provName = customProviderName(base, apiBase)
		}

		pa := ensureProv(provName, isCustom, base, apiBase)

		// Deployments sharing a credential collapse into one key whose allowlist
		// is the union of their models. Keyless inline deployments group by value.
		credSig := credName
		if credSig == "" {
			credSig = "inline:" + apiKey
		}
		ka := pa.keys[credSig]
		if ka == nil {
			value, _ := keyValue(apiKey)
			if value == "" {
				// No credential resolved — Bifrost rejects keys with empty values.
				// Still emit a model config (rate limits) but skip key creation.
				addSkippedProvider(provName, fmt.Sprintf("no credential for model %q; skipping key", rawModel))
				addModelConfig(m.ModelName, rawModel, budget, base, provName)
				continue
			}
			name := credName
			if name == "" {
				name = keyName(base, apiKey, len(pa.keyOrder))
			}
			ka = &keyAgg{plan: KeyPlan{Name: provName + "/" + name, Value: value}, modelSet: map[string]bool{}}
			pa.keys[credSig] = ka
			pa.keyOrder = append(pa.keyOrder, credSig)
		}
		if model == "*" {
			ka.wildcard = true
		} else {
			ka.modelSet[model] = true
		}

		addModelConfig(m.ModelName, rawModel, budget, base, provName)
	}

	plans := make([]ProviderPlan, 0, len(provOrder))
	for _, name := range provOrder {
		pa := provByName[name]
		plan := pa.plan
		for _, sig := range pa.keyOrder {
			ka := pa.keys[sig]
			if ka.wildcard {
				ka.plan.Models = []string{"*"}
			} else {
				allowed := make([]string, 0, len(ka.modelSet))
				for mdl := range ka.modelSet {
					allowed = append(allowed, mdl)
				}
				sort.Strings(allowed)
				ka.plan.Models = allowed
			}
			plan.Keys = append(plan.Keys, ka.plan)
		}
		plans = append(plans, plan)
	}

	return plans, modelConfigs, skippedProviders
}

// modelInfoRateLimits returns the deployment's tpm/rpm overrides. The model_info
// block is the base; the litellm_params block carries the deployment-level
// limits and overrides it.
func modelInfoRateLimits(m litellm.LiteLLMModelInfo) (tpm, rpm *int64) {
	if m.ModelInfo != nil {
		tpm = m.ModelInfo.Tpm
		rpm = m.ModelInfo.Rpm
	}
	if m.LiteLLMParams != nil {
		if m.LiteLLMParams.Tpm != nil {
			tpm = m.LiteLLMParams.Tpm
		}
		if m.LiteLLMParams.Rpm != nil {
			rpm = m.LiteLLMParams.Rpm
		}
	}
	return tpm, rpm
}

// ProviderKeyRef is a (provider, key-UUID) pair used to attach specific Bifrost
// provider keys to a virtual key.
type ProviderKeyRef struct {
	Provider string
	KeyID    string
}

// BuildKeyModelIndex builds a model-name → []ProviderKeyRef index from the
// provider/key plans that were migrated and the UUIDs returned by Bifrost.
// A key whose Models list is ["*"] (wildcard) is reachable by every model name;
// such keys are collected in wildcardKeys and merged into every lookup result.
// keyIDByName maps full key names (e.g. "openai/OPENAI_KEY") to Bifrost UUIDs.
func BuildKeyModelIndex(plans []ProviderPlan, keyIDByName map[string]string) (map[string][]ProviderKeyRef, []ProviderKeyRef) {
	specific := map[string][]ProviderKeyRef{} // model → refs for keys with explicit model lists
	var wildcardKeys []ProviderKeyRef         // keys that serve all models

	addRef := func(model, provider, keyID string) {
		ref := ProviderKeyRef{Provider: provider, KeyID: keyID}
		specific[model] = append(specific[model], ref)
	}

	for _, p := range plans {
		for _, k := range p.Keys {
			keyID, ok := keyIDByName[k.Name]
			if !ok {
				continue // key was not successfully created in Bifrost; skip
			}
			isWildcard := len(k.Models) == 1 && k.Models[0] == "*"
			if isWildcard {
				wildcardKeys = append(wildcardKeys, ProviderKeyRef{Provider: p.Name, KeyID: keyID})
			} else {
				for _, m := range k.Models {
					addRef(m, p.Name, keyID)
				}
			}
		}
	}
	return specific, wildcardKeys
}

// ModelRef is a Bifrost (provider, model) pair that a LiteLLM public model_name
// resolves to.
type ModelRef struct {
	Provider string
	Model    string
}

// BuildModelIndex maps each LiteLLM public model_name to the Bifrost
// (provider, model) pairs it resolves to, and returns the sorted set of target
// providers. It also indexes by the upstream model name (trimmed from its
// provider prefix), so a VK with models:["gpt-4o"] resolves even when the
// deployment model_name is "openai-gpt-4o". model_group_alias entries from the
// config (e.g. "fast-cheap" -> "openai-gpt-4o") are followed transitively.
func BuildModelIndex(models []litellm.LiteLLMModelInfo, credByName map[string]*litellm.LiteLLMModelCredential, deployments map[string]*litellm.Deployment, cfg MigrationRunConfig, litellmCfg *litellm.LiteLLMConfig) (map[string][]ModelRef, []string) {
	index := map[string][]ModelRef{}
	seen := map[string]map[string]bool{} // source_name -> "provider|model" set
	providerSet := map[string]bool{}

	addRef := func(sourceName, provider, model string) {
		if seen[sourceName] == nil {
			seen[sourceName] = map[string]bool{}
		}
		sig := provider + "|" + model
		if seen[sourceName][sig] {
			return
		}
		seen[sourceName][sig] = true
		index[sourceName] = append(index[sourceName], ModelRef{Provider: provider, Model: model})
		if provider != "*" {
			providerSet[provider] = true
		}
	}

	for _, m := range models {
		in, ok := resolveModelMigrationInput(m, credByName, deployments, cfg)
		if !ok {
			continue
		}
		base := in.BaseProvider
		rawModel := in.RawModel

		if rawModel == "*" {
			addRef(m.ModelName, "*", "*")
			continue
		}
		if base == "" || !standardProviders[base] {
			continue
		}

		if selfHostedURLProviders[base] {
			if in.APIBase == "" {
				continue
			}
			model := stripProviderPrefix(rawModel, base)
			if model != "*" && strings.ContainsAny(model, "*?") {
				continue
			}
			addRef(m.ModelName, base, model)
			continue
		}

		model := trimModelPrefix(rawModel)
		if model == "" || (model != "*" && strings.ContainsAny(model, "*?")) {
			continue
		}

		provider := base
		if in.APIBase != "" {
			if !customProviders[base] {
				continue
			}
			provider = customProviderName(base, in.APIBase)
		}
		addRef(m.ModelName, provider, model)

		// Also index by the upstream model name (e.g. "gpt-4o") so VK
		// allowed_model lists that reference the actual model name — rather than
		// the LiteLLM deployment model_name — resolve to the right provider.
		if model != "*" && model != m.ModelName {
			addRef(model, provider, model)
		}
	}

	// Follow model_group_alias entries: alias -> deployment model_name.
	// e.g. "fast-cheap" -> "openai-gpt-4o" -> refs already in index.
	if litellmCfg != nil {
		for alias, target := range litellmCfg.RouterSettings.ModelGroupAlias {
			if refs, ok := index[target]; ok {
				for _, r := range refs {
					addRef(alias, r.Provider, r.Model)
				}
			}
		}
	}

	var providers []string
	for p := range providerSet {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	return index, providers
}

func resolveModelMigrationInput(m litellm.LiteLLMModelInfo, credByName map[string]*litellm.LiteLLMModelCredential, deployments map[string]*litellm.Deployment, cfg MigrationRunConfig) (modelMigrationInput, bool) {
	if m.LiteLLMParams == nil {
		return modelMigrationInput{}, false
	}
	p := m.LiteLLMParams
	rawModel := strings.TrimSpace(p.Model)

	var dep *litellm.Deployment
	if m.ModelInfo != nil {
		dep = deployments[m.ModelInfo.Id]
	}
	if dep == nil {
		dep = deployments[m.ModelName]
	}

	var apiKey, apiBase, credProvider, credName string
	var cred *litellm.LiteLLMModelCredential
	if p.LiteLLMCredentialName != nil {
		credName = strings.TrimSpace(*p.LiteLLMCredentialName)
	}
	if credName != "" {
		cred = credByName[credName]
	} else {
		if dep != nil {
			cred = dep.InlineCredential()
		}
		// LiteLLM resolves os.environ/ references before storing to DB, so if the
		// env var was unset at LiteLLM startup the DB entry has an empty api_key.
		// Fall back to the config-keyed deployment (by model_name) which still
		// holds the raw env ref string we can pass to Bifrost as "env.VAR".
		if cred == nil {
			if cfgDep := deployments[m.ModelName]; cfgDep != nil && cfgDep != dep {
				cred = cfgDep.InlineCredential()
			}
		}
	}
	if cred != nil {
		if cred.CredentialValues != nil {
			if cred.CredentialValues.ApiKey != nil {
				apiKey = strings.TrimSpace(*cred.CredentialValues.ApiKey)
			}
			if cred.CredentialValues.ApiBase != nil {
				apiBase = strings.TrimSpace(*cred.CredentialValues.ApiBase)
			}
		}
		if cred.CredentialInfo != nil {
			credProvider = strings.ToLower(strings.TrimSpace(cred.CredentialInfo.CustomLLMProvider))
		}
	}

	base := deriveProvider(p.CustomLLMProvider, p.Model, cfg.DefaultProvider)
	if (base == "" || !standardProviders[base]) && standardProviders[credProvider] {
		base = credProvider
	}

	// Resolve Azure/Bedrock/Vertex structured credential fields.
	// Try the DB deployment value first (resolved literal); fall back to the
	// config deployment which still carries "os.environ/VAR" env refs that
	// keyValue converts to the "env.VAR" format Bifrost resolves at runtime.
	cfgDep := deployments[m.ModelName]
	sf := func(f func(*litellm.Deployment) string) string {
		var dbVal, cfgVal string
		if dep != nil {
			dbVal = f(dep)
		}
		if cfgDep != nil && cfgDep != dep {
			cfgVal = f(cfgDep)
		}
		if v, _ := keyValue(dbVal); v != "" {
			return v
		}
		v, _ := keyValue(cfgVal)
		return v
	}

	in := modelMigrationInput{
		Params:       p,
		Deployment:   dep,
		RawModel:     rawModel,
		APIKey:       apiKey,
		APIBase:      apiBase,
		CredName:     credName,
		BaseProvider: base,
		// Azure
		AzureClientID:     sf(func(d *litellm.Deployment) string { return d.AzureClientID }),
		AzureClientSecret: sf(func(d *litellm.Deployment) string { return d.AzureClientSecret }),
		AzureTenantID:     sf(func(d *litellm.Deployment) string { return d.AzureTenantID }),
		AzureADToken:      sf(func(d *litellm.Deployment) string { return d.AzureADToken }),
		// Bedrock
		AWSAccessKeyID:     sf(func(d *litellm.Deployment) string { return d.AWSAccessKeyID }),
		AWSSecretAccessKey: sf(func(d *litellm.Deployment) string { return d.AWSSecretAccessKey }),
		AWSRegionName:      sf(func(d *litellm.Deployment) string { return d.AWSRegionName }),
		AWSRoleName:        sf(func(d *litellm.Deployment) string { return d.AWSRoleName }),
		AWSSessionName:     sf(func(d *litellm.Deployment) string { return d.AWSSessionName }),
		AWSSessionToken:    sf(func(d *litellm.Deployment) string { return d.AWSSessionToken }),
		// Vertex
		VertexProject:     sf(func(d *litellm.Deployment) string { return d.VertexProject }),
		VertexLocation:    sf(func(d *litellm.Deployment) string { return d.VertexLocation }),
		VertexCredentials: sf(func(d *litellm.Deployment) string { return d.VertexCredentials }),
	}
	return in, true
}

// modelConfigPlan maps a LiteLLM deployment's governance fields (carried in b)
// to one global Bifrost model config keyed by the actual upstream model name
// because Bifrost model configs are unique by model_name. sourceName is the
// public model_name and rawModel is litellm_params.model.
func modelConfigPlan(sourceName, rawModel string, b litellm.LiteLLMBudget, base, provider, maxBudgetPeriod string) (*ModelConfigPlan, string, error) {
	budget, err := toBudget(b, maxBudgetPeriod)
	if err != nil {
		return nil, "", err
	}
	rateLimit := toRateLimit(b)
	if budget == nil && rateLimit == nil {
		return nil, "", nil
	}

	modelName := trimModelPrefix(rawModel)
	if modelName == "" {
		return nil, fmt.Sprintf("%s (%s): empty model_name is not migrated", sourceName, strings.TrimSpace(rawModel)), nil
	}
	if modelName != "*" && strings.ContainsAny(modelName, "*?") {
		return nil, fmt.Sprintf("%s (%s): partial wildcard model limits are not migrated", sourceName, strings.TrimSpace(rawModel)), nil
	}

	var providerPtr *string
	if strings.TrimSpace(rawModel) == "*" {
		provider = ""
	}
	if provider != "" {
		p := provider
		providerPtr = &p
	}
	if base == "*" {
		return nil, fmt.Sprintf("%s (%s): wildcard provider patterns are not migrated", sourceName, strings.TrimSpace(rawModel)), nil
	}

	plan := &ModelConfigPlan{SourceName: strings.TrimSpace(sourceName), ModelName: modelName, Provider: providerPtr, RateLimit: rateLimit}
	if budget != nil {
		plan.Budgets = []BifrostCreateBudgetRequest{*budget}
	}
	return plan, "", nil
}

// mergeModelConfig folds duplicate Bifrost model configs into the stricter
// limit because Bifrost accepts only one config per model_name.
func mergeModelConfig(existing *ModelConfigPlan, next ModelConfigPlan) {
	existing.SourceName = appendSource(existing.SourceName, next.SourceName)
	existing.Provider = mergeModelConfigProvider(existing.Provider, next.Provider)
	existing.RateLimit = mergeRateLimit(existing.RateLimit, next.RateLimit)
	existing.Budgets = mergeBudgets(existing.Budgets, next.Budgets)
}

// appendSource records every LiteLLM deployment that contributed to a merged
// model config for dry-run visibility.
func appendSource(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" || strings.Contains(","+existing+",", ","+next+",") {
		return existing
	}
	return existing + "," + next
}

// mergeModelConfigProvider preserves the scoped provider for duplicate configs;
// duplicates only merge when the provider already matches.
func mergeModelConfigProvider(existing, next *string) *string {
	if existing == nil || next == nil {
		return nil
	}
	provider := *existing
	return &provider
}

// mergeRateLimit keeps the lowest non-nil RPM and TPM from duplicate LiteLLM
// deployments for the same actual model.
func mergeRateLimit(existing, next *BifrostCreateRateLimitRequest) *BifrostCreateRateLimitRequest {
	if existing == nil {
		return next
	}
	if next == nil {
		return existing
	}
	requestLimit, requestReset := minRateLimitDimension(existing.RequestMaxLimit, existing.RequestResetDuration, next.RequestMaxLimit, next.RequestResetDuration)
	tokenLimit, tokenReset := minRateLimitDimension(existing.TokenMaxLimit, existing.TokenResetDuration, next.TokenMaxLimit, next.TokenResetDuration)
	return &BifrostCreateRateLimitRequest{
		RequestMaxLimit:      requestLimit,
		RequestResetDuration: requestReset,
		TokenMaxLimit:        tokenLimit,
		TokenResetDuration:   tokenReset,
	}
}

// minRateLimitDimension returns the stricter limit and its required reset
// duration for one rate-limit dimension.
func minRateLimitDimension(a *int64, aReset *string, b *int64, bReset *string) (*int64, *string) {
	if a == nil {
		return b, bReset
	}
	if b == nil || *a <= *b {
		return a, aReset
	}
	return b, bReset
}

// mergeBudgets keeps a single lowest max budget when duplicate model configs
// contain budget limits, preserving the reset duration attached to that budget.
func mergeBudgets(existing, next []BifrostCreateBudgetRequest) []BifrostCreateBudgetRequest {
	if len(existing) == 0 {
		return next
	}
	if len(next) == 0 {
		return existing
	}
	if next[0].MaxLimit < existing[0].MaxLimit {
		return next
	}
	return existing
}

// modelConfigSignature returns the Bifrost uniqueness key for a global model
// config, intentionally ignoring the governance payload.
func modelConfigSignature(mc ModelConfigPlan) string {
	provider := ""
	if mc.Provider != nil {
		provider = *mc.Provider
	}
	return provider + "|" + mc.ModelName
}

// deriveProvider resolves the Bifrost base provider for a deployment:
// custom_llm_provider, else the prefix before the first "/" in model, else the
// default. The result is lowercased and normalised through liteLLMProviderAliases
// so LiteLLM names like "vertex_ai" map to Bifrost names like "vertex".
func deriveProvider(customLLMProvider, model, def string) string {
	var raw string
	if customLLMProvider != "" {
		raw = strings.ToLower(customLLMProvider)
	} else if model == "" {
		return ""
	} else if i := strings.Index(model, "/"); i > 0 {
		raw = strings.ToLower(model[:i])
	} else {
		raw = strings.ToLower(def)
	}
	if canonical, ok := liteLLMProviderAliases[raw]; ok {
		return canonical
	}
	return raw
}

// customProviderName synthesizes a unique, stable provider name for a (base, apiBase) pair
// by hashing the full URL with SHA-256 and using the first 8 bytes as a 16-char hex suffix.
// Format: "<base>-<16 hex chars>", e.g. "openai-3f1a9b2c4d5e6f7a".
// Max length: len("anthropic") + 1 + 16 = 26, well within the varchar(50) column limit.
// The name is deterministic: same inputs always produce the same name, so the migration
// is idempotent across runs.
func customProviderName(base, apiBase string) string {
	h := sha256.Sum256([]byte(base + "\x00" + apiBase))
	return base + "-" + hex.EncodeToString(h[:8])
}

// keyValue maps a LiteLLM api_key onto a Bifrost key value string and reports
// whether it is a literal (plaintext) secret. "os.environ/FOO" and "env.FOO"
// become "env.FOO"; an empty key stays empty (keyless providers are valid);
// anything else is carried as a literal.
func keyValue(apiKey string) (value string, literal bool) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", false
	}
	if name, ok := strings.CutPrefix(apiKey, "os.environ/"); ok {
		return "env." + name, false
	}
	if strings.HasPrefix(apiKey, "env.") {
		return apiKey, false
	}
	return apiKey, true
}

// stripProviderPrefix removes only a leading "provider/" segment from a
// self-hosted model string, preserving HF-style repo ids
// ("meta-llama/Llama-3.1-8B-Instruct" stays intact; "vllm/llama3" -> "llama3").
func stripProviderPrefix(model, provider string) string {
	if rest, ok := strings.CutPrefix(model, provider+"/"); ok {
		return rest
	}
	return model
}

// selfHostedKeyName names a per-URL key for a self-hosted provider from its
// server host, falling back to a stable per-provider index.
func selfHostedKeyName(base, apiBase string, idx int) string {
	host := apiBase
	if u, err := url.Parse(apiBase); err == nil && u.Host != "" {
		host = u.Host
	}
	slug := strings.Trim(nonAlphanumRe.ReplaceAllString(strings.ToLower(host), "-"), "-")
	if slug == "" {
		return fmt.Sprintf("%s-key-%d", base, idx)
	}
	name := base + "-" + slug
	if len(name) > maxProviderNameLen {
		name = name[:maxProviderNameLen]
	}
	return name
}

// keyName produces a readable key name from the credential: the env var name
// for env refs, else a stable per-provider fallback.
func keyName(base, apiKey string, idx int) string {
	apiKey = strings.TrimSpace(apiKey)
	if name, ok := strings.CutPrefix(apiKey, "os.environ/"); ok {
		return name
	}
	if name, ok := strings.CutPrefix(apiKey, "env."); ok {
		return name
	}
	if apiKey == "" {
		return base + "-default"
	}
	return fmt.Sprintf("%s-key-%d", base, idx)
}

// azureKeyName derives a stable key name for an Azure deployment from its
// endpoint URL hostname, e.g. "https://myazure.openai.azure.com/" → "myazure-openai-azure-com".
func azureKeyName(apiBase string, idx int) string {
	return selfHostedKeyName("azure", apiBase, idx)
}

// bedrockKeyName derives a stable key name from the Bedrock auth fields.
// Prefers role ARN (last path segment) > access key env var name > fallback index.
func bedrockKeyName(in modelMigrationInput, idx int) string {
	if in.AWSRoleName != "" {
		name := in.AWSRoleName
		if n, ok := strings.CutPrefix(name, "env."); ok {
			return n
		}
		// ARN: arn:aws:iam::123456789012:role/MyRole → "MyRole"
		if i := strings.LastIndex(name, "/"); i >= 0 {
			if seg := name[i+1:]; seg != "" {
				return seg
			}
		}
		return name
	}
	if in.AWSAccessKeyID != "" {
		name := in.AWSAccessKeyID
		if n, ok := strings.CutPrefix(name, "env."); ok {
			return n
		}
		return fmt.Sprintf("bedrock-key-%d", idx)
	}
	return fmt.Sprintf("bedrock-default-%d", idx)
}

// vertexKeyName derives a stable key name from the Vertex project and location.
// e.g. project="my-project", location="us-central1" → "my-project-us-central1".
func vertexKeyName(in modelMigrationInput, idx int) string {
	project := in.VertexProject
	if n, ok := strings.CutPrefix(project, "env."); ok {
		project = n
	}
	location := in.VertexLocation
	if n, ok := strings.CutPrefix(location, "env."); ok {
		location = n
	}
	slug := strings.Trim(nonAlphanumRe.ReplaceAllString(strings.ToLower(project+"-"+location), "-"), "-")
	if slug == "" {
		return fmt.Sprintf("vertex-key-%d", idx)
	}
	if len(slug) > maxProviderNameLen-7 { // room for "vertex/" prefix in full key name
		slug = slug[:maxProviderNameLen-7]
	}
	return slug
}
