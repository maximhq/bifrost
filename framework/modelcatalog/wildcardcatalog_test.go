package modelcatalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// fireworksCatalog builds the catalog shape a Fireworks deployment actually has
// (issue #6657):
//
//   - the datasheet carries Fireworks rows, because the public pricing sheet
//     ships ~315 "fireworks_ai/..." entries;
//   - the live store is empty for Fireworks, because Fireworks has no enumerable
//     OpenAI-style list-models endpoint (see core/providers/fireworks: the
//     provider builds its list from the key's configured models) and an operator
//     who only pasted an API key configured none;
//   - the provider key itself is unrestricted, i.e. an ordinary "just add the
//     key" setup.
//
// The datasheet fixture mirrors the real feed: keys are "<litellm provider>/<model
// id>" and the row's own "provider" field is "fireworks_ai", which normalizeProvider
// folds onto schemas.Fireworks.
func fireworksCatalog(t *testing.T) *ModelCatalog {
	t.Helper()

	dir := t.TempDir()
	pricingPath := filepath.Join(dir, "pricing.json")
	pricingJSON := []byte(`{
		"fireworks_ai/accounts/fireworks/models/glm-5p2": {
			"provider": "fireworks_ai",
			"mode": "chat",
			"base_model": "glm-5.2",
			"input_cost_per_token": 0.0000014,
			"output_cost_per_token": 0.0000044
		},
		"fireworks_ai/accounts/fireworks/models/kimi-k2p6": {
			"provider": "fireworks_ai",
			"mode": "chat",
			"base_model": "kimi-k2.6",
			"input_cost_per_token": 0.0000006,
			"output_cost_per_token": 0.0000025
		}
	}`)
	if err := os.WriteFile(pricingPath, pricingJSON, 0o600); err != nil {
		t.Fatalf("write pricing fixture: %v", err)
	}

	// Point the parameters feed at a local file too, so nothing in this test can
	// reach the network.
	paramsPath := filepath.Join(dir, "params.json")
	if err := os.WriteFile(paramsPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write params fixture: %v", err)
	}

	ds := datasheet.New(nil, nil, datasheet.Config{
		URL:                "file://" + pricingPath,
		ModelParametersURL: "file://" + paramsPath,
	})
	if err := ds.LoadFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load pricing fixture: %v", err)
	}

	kc := keyconfig.New(nil)
	kc.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.Fireworks: {{
			ID:      "fw-key-1",
			Enabled: ptrBool(true),
			Models:  schemas.WhiteList{"*"},
		}},
	})

	mc := &ModelCatalog{
		datasheet: ds,
		live:      live.New(nil),
		keyconf:   kc,
		done:      make(chan struct{}),
	}
	mc.initCaches()

	// Sanity: the fixture really did land, so a failure below is about the
	// wildcard rule and not about an empty datasheet.
	if got := mc.datasheet.DatasheetModelsForProvider(schemas.Fireworks); len(got) != 2 {
		t.Fatalf("fixture did not load: DatasheetModelsForProvider(fireworks) = %v, want 2 entries", got)
	}
	if got := mc.live.UnfilteredModelsForProvider(schemas.Fireworks); len(got) != 0 {
		t.Fatalf("live store should be empty for fireworks, got %v", got)
	}
	return mc
}

// TestIsModelAllowedForProvider_WildcardAllowsModelMissingFromCatalog is the
// regression test for issue #6657.
//
// A virtual key scoped to Fireworks with allowed_models ["*"] must permit every
// Fireworks model. schemas.WhiteList documents "*" as "all values are allowed"
// (core/schemas/account.go) and framework/configstore/tables/virtualkey.go
// documents allowed_models ["*"] as "allows all models", but the unrestricted
// branch of IsModelAllowedForProvider resolves the wildcard against the
// discovered catalog instead. Fireworks' catalog is the pricing datasheet alone,
// which lags new releases, so any model newer than the sheet is refused with
// 403 model_blocked even though the wildcard should not narrow anything.
func TestIsModelAllowedForProvider_WildcardAllowsModelMissingFromCatalog(t *testing.T) {
	mc := fireworksCatalog(t)
	wildcard := schemas.WhiteList{"*"}

	// Control: a model the datasheet knows is permitted today. If this ever
	// fails the fixture is wrong, not the rule under test.
	if !mc.IsModelAllowedForProvider(schemas.Fireworks, "accounts/fireworks/models/glm-5p2", nil, wildcard) {
		t.Fatalf("control failed: a datasheet-known Fireworks model must be allowed under a wildcard")
	}

	// The defect: glm-5p3 is a real Fireworks model that postdates the pricing
	// datasheet, so the catalog has never heard of it.
	const model = "accounts/fireworks/models/glm-5p3"
	if got := mc.IsModelAllowedForProvider(schemas.Fireworks, model, nil, wildcard); !got {
		t.Errorf("IsModelAllowedForProvider(fireworks, %q, allowed=[\"*\"]) = false, want true: "+
			"a wildcard allowlist must permit every model on the provider, not only the ones "+
			"the discovered catalog happens to list (issue #6657)", model)
	}

	// The short form the issue also reports failing.
	const shortForm = "glm-5p3"
	if got := mc.IsModelAllowedForProvider(schemas.Fireworks, shortForm, nil, wildcard); !got {
		t.Errorf("IsModelAllowedForProvider(fireworks, %q, allowed=[\"*\"]) = false, want true (issue #6657)", shortForm)
	}
}
