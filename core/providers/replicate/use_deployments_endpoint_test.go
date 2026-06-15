package replicate

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestUseDeploymentsEndpoint_AliasOverride verifies the per-alias
// ReplicateAliasCfg.UseDeploymentsEndpoint override resolves correctly:
// alias value wins when set, else falls through to key-level config.
func TestUseDeploymentsEndpoint_AliasOverride(t *testing.T) {
	keyDeployments := schemas.Key{
		ReplicateKeyConfig: &schemas.ReplicateKeyConfig{UseDeploymentsEndpoint: true},
	}
	keyPredictions := schemas.Key{
		ReplicateKeyConfig: &schemas.ReplicateKeyConfig{UseDeploymentsEndpoint: false},
	}

	// No alias in ctx — falls back to key-level setting.
	if got := useDeploymentsEndpoint(nil, keyDeployments); !got {
		t.Errorf("nil ctx + key=deployments: want true, got false")
	}
	if got := useDeploymentsEndpoint(nil, keyPredictions); got {
		t.Errorf("nil ctx + key=predictions: want false, got true")
	}
	if got := useDeploymentsEndpoint(nil, schemas.Key{}); got {
		t.Errorf("nil ctx + nil ReplicateKeyConfig: want false, got true")
	}

	// Alias override true wins over key=false.
	ctxOverrideTrue := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctxOverrideTrue.Cancel()
	trueVal := true
	ctxOverrideTrue.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "prod-llm",
		Config: &schemas.AliasConfig{
			ModelID: "owner/name:version",
			ReplicateAliasCfg: &schemas.ReplicateAliasCfg{
				UseDeploymentsEndpoint: &trueVal,
			},
		},
	})
	if got := useDeploymentsEndpoint(ctxOverrideTrue, keyPredictions); !got {
		t.Errorf("alias=true should override key=false: got false")
	}

	// Alias override false wins over key=true.
	ctxOverrideFalse := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctxOverrideFalse.Cancel()
	falseVal := false
	ctxOverrideFalse.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "experimental-llm",
		Config: &schemas.AliasConfig{
			ModelID: "owner/name:version",
			ReplicateAliasCfg: &schemas.ReplicateAliasCfg{
				UseDeploymentsEndpoint: &falseVal,
			},
		},
	})
	if got := useDeploymentsEndpoint(ctxOverrideFalse, keyDeployments); got {
		t.Errorf("alias=false should override key=true: got true")
	}

	// Alias present but ReplicateAliasCfg unset — falls through to key.
	ctxNoCfg := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctxNoCfg.Cancel()
	ctxNoCfg.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "x",
		Config: &schemas.AliasConfig{ModelID: "x"},
	})
	if got := useDeploymentsEndpoint(ctxNoCfg, keyDeployments); !got {
		t.Errorf("no alias cfg + key=deployments: want true, got false")
	}
}
