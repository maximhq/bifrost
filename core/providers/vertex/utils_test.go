package vertex

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestGetVertexAPIHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		region   string
		expected string
	}{
		{
			name:     "global endpoint",
			region:   "global",
			expected: "aiplatform.googleapis.com",
		},
		{
			name:     "us multi-region pool endpoint",
			region:   "us",
			expected: "aiplatform.us.rep.googleapis.com",
		},
		{
			name:     "eu multi-region pool endpoint",
			region:   "eu",
			expected: "aiplatform.eu.rep.googleapis.com",
		},
		{
			name:     "single region endpoint",
			region:   "us-central1",
			expected: "us-central1-aiplatform.googleapis.com",
		},
		{
			name:     "single european region endpoint",
			region:   "europe-west1",
			expected: "europe-west1-aiplatform.googleapis.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := getVertexAPIHost(tt.region)
			if actual != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

func TestIsVertexMultiRegionEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		region   string
		expected bool
	}{
		{region: "us", expected: true},
		{region: "eu", expected: true},
		{region: "global", expected: false},
		{region: "us-central1", expected: false},
		{region: "europe-west1", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			t.Parallel()

			actual := isVertexMultiRegionEndpoint(tt.region)
			if actual != tt.expected {
				t.Fatalf("expected %v, got %v", tt.expected, actual)
			}
		})
	}
}

func TestGetVertexModelListingAPIHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		region   string
		expected string
	}{
		{
			name:     "global listing endpoint",
			region:   "global",
			expected: "aiplatform.googleapis.com",
		},
		{
			name:     "us multi-region uses standard listing endpoint",
			region:   "us",
			expected: "aiplatform.googleapis.com",
		},
		{
			name:     "eu multi-region uses standard listing endpoint",
			region:   "eu",
			expected: "aiplatform.googleapis.com",
		},
		{
			name:     "single region listing endpoint",
			region:   "us-central1",
			expected: "us-central1-aiplatform.googleapis.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := getVertexModelListingAPIHost(tt.region)
			if actual != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

func TestGetVertexPublisherModelURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		region   string
		expected string
	}{
		{
			name:     "global raw predict",
			region:   "global",
			expected: "https://aiplatform.googleapis.com/v1/projects/project-123/locations/global/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
		{
			name:     "us multi-region raw predict",
			region:   "us",
			expected: "https://aiplatform.us.rep.googleapis.com/v1/projects/project-123/locations/us/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
		{
			name:     "eu multi-region raw predict",
			region:   "eu",
			expected: "https://aiplatform.eu.rep.googleapis.com/v1/projects/project-123/locations/eu/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
		{
			name:     "single region raw predict",
			region:   "us-central1",
			expected: "https://us-central1-aiplatform.googleapis.com/v1/projects/project-123/locations/us-central1/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := getVertexPublisherModelURL(tt.region, "v1", "project-123", "anthropic", "claude-opus-4-7", ":rawPredict")
			if actual != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

func TestGetVertexModelAwareAPIHost(t *testing.T) {
	// Seed the model params cache with vertex_ai/ prefix (matches how model-parameters are stored)
	providerUtils.SetModelParams("vertex_ai/claude-opus-4-7", providerUtils.ModelParams{
		IsVertexMultiRegionOnly: schemas.Ptr(true),
	})
	providerUtils.SetModelParams("vertex_ai/claude-sonnet-4-5", providerUtils.ModelParams{
		IsVertexMultiRegionOnly: schemas.Ptr(false),
	})
	t.Cleanup(func() {
		providerUtils.DeleteModelParams("vertex_ai/claude-opus-4-7")
		providerUtils.DeleteModelParams("vertex_ai/claude-sonnet-4-5")
	})

	tests := []struct {
		name     string
		region   string
		model    string
		expected string
	}{
		{
			name:     "global endpoint ignores model flag",
			region:   "global",
			model:    "claude-opus-4-7",
			expected: "aiplatform.googleapis.com",
		},
		{
			name:     "us multi-region always uses rep host (flagged model)",
			region:   "us",
			model:    "claude-opus-4-7",
			expected: "aiplatform.us.rep.googleapis.com",
		},
		{
			name:     "eu multi-region always uses rep host (flagged model)",
			region:   "eu",
			model:    "claude-opus-4-7",
			expected: "aiplatform.eu.rep.googleapis.com",
		},
		{
			name:     "us multi-region always uses rep host (unflagged model)",
			region:   "us",
			model:    "claude-sonnet-4-5",
			expected: "aiplatform.us.rep.googleapis.com",
		},
		{
			name:     "eu multi-region always uses rep host (unknown model)",
			region:   "eu",
			model:    "some-unknown-model",
			expected: "aiplatform.eu.rep.googleapis.com",
		},
		{
			name:     "single region promotes flagged model to us multi-region pool",
			region:   "us-central1",
			model:    "claude-opus-4-7",
			expected: "aiplatform.us.rep.googleapis.com",
		},
		{
			name:     "single region promotes flagged model to eu multi-region pool",
			region:   "europe-west1",
			model:    "claude-opus-4-7",
			expected: "aiplatform.eu.rep.googleapis.com",
		},
		{
			name:     "asia region does NOT promote flagged model (no pool)",
			region:   "asia-southeast1",
			model:    "claude-opus-4-7",
			expected: "asia-southeast1-aiplatform.googleapis.com",
		},
		{
			name:     "me region does NOT promote flagged model (no pool)",
			region:   "me-west1",
			model:    "claude-opus-4-7",
			expected: "me-west1-aiplatform.googleapis.com",
		},
		{
			name:     "single region keeps standard host for unflagged model",
			region:   "us-central1",
			model:    "claude-sonnet-4-5",
			expected: "us-central1-aiplatform.googleapis.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := getVertexModelAwareAPIHost(tt.region, tt.model)
			if actual != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

func TestGetVertexModelAwarePublisherModelURL(t *testing.T) {
	// Seed the model params cache with vertex_ai/ prefix (matches how model-parameters are stored)
	providerUtils.SetModelParams("vertex_ai/claude-opus-4-7", providerUtils.ModelParams{
		IsVertexMultiRegionOnly: schemas.Ptr(true),
	})
	t.Cleanup(func() {
		providerUtils.DeleteModelParams("vertex_ai/claude-opus-4-7")
	})

	tests := []struct {
		name     string
		region   string
		model    string
		expected string
	}{
		{
			name:     "us multi-region flagged model gets rep host URL",
			region:   "us",
			model:    "claude-opus-4-7",
			expected: "https://aiplatform.us.rep.googleapis.com/v1/projects/project-123/locations/us/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
		{
			name:     "eu multi-region flagged model gets rep host URL",
			region:   "eu",
			model:    "claude-opus-4-7",
			expected: "https://aiplatform.eu.rep.googleapis.com/v1/projects/project-123/locations/eu/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
		{
			name:     "us multi-region unflagged model still gets rep host URL",
			region:   "us",
			model:    "claude-3-5-sonnet",
			expected: "https://aiplatform.us.rep.googleapis.com/v1/projects/project-123/locations/us/publishers/anthropic/models/claude-3-5-sonnet:rawPredict",
		},
		{
			name:     "single region flagged model gets promoted to us pool",
			region:   "us-central1",
			model:    "claude-opus-4-7",
			expected: "https://aiplatform.us.rep.googleapis.com/v1/projects/project-123/locations/us/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
		{
			name:     "single region europe flagged model gets promoted to eu pool",
			region:   "europe-west1",
			model:    "claude-opus-4-7",
			expected: "https://aiplatform.eu.rep.googleapis.com/v1/projects/project-123/locations/eu/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
		{
			name:     "single region unflagged model keeps standard host",
			region:   "us-central1",
			model:    "claude-3-5-sonnet",
			expected: "https://us-central1-aiplatform.googleapis.com/v1/projects/project-123/locations/us-central1/publishers/anthropic/models/claude-3-5-sonnet:rawPredict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := getVertexModelAwarePublisherModelURL(tt.region, "v1", "project-123", "anthropic", tt.model, ":rawPredict")
			if actual != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

func TestVertexRegionToPool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		region       string
		expectedPool string
		expectedOK   bool
	}{
		{region: "us-central1", expectedPool: "us", expectedOK: true},
		{region: "us-east1", expectedPool: "us", expectedOK: true},
		{region: "us-east5", expectedPool: "us", expectedOK: true},
		{region: "europe-west1", expectedPool: "eu", expectedOK: true},
		{region: "europe-west4", expectedPool: "eu", expectedOK: true},
		{region: "asia-southeast1", expectedPool: "", expectedOK: false},
		{region: "me-west1", expectedPool: "", expectedOK: false},
		{region: "southamerica-east1", expectedPool: "", expectedOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			t.Parallel()

			pool, ok := vertexRegionToPool(tt.region)
			if ok != tt.expectedOK {
				t.Fatalf("expected ok=%v, got ok=%v", tt.expectedOK, ok)
			}
			if pool != tt.expectedPool {
				t.Fatalf("expected %q, got %q", tt.expectedPool, pool)
			}
		})
	}
}

// TestResolveVertexProjectID_AliasOverride verifies the per-alias ProjectID
// override lets one Vertex credential serve deployments across distinct GCP
// projects.
func TestResolveVertexProjectID_AliasOverride(t *testing.T) {
	keyProject := "key-level-project"
	aliasProject := "alias-level-project"
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID: *schemas.NewSecretVar(keyProject),
		},
	}

	if got := resolveVertexProjectID(nil, key); got != keyProject {
		t.Errorf("nil ctx: got %q, want key-level %q", got, keyProject)
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if got := resolveVertexProjectID(ctx, key); got != keyProject {
		t.Errorf("empty ctx: got %q, want key-level %q", got, keyProject)
	}

	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID: "claude-sonnet-4-5",
			VertexAliasCfg: &schemas.VertexAliasCfg{
				ProjectID: schemas.NewSecretVar(aliasProject),
			},
		},
	})
	if got := resolveVertexProjectID(ctx, key); got != aliasProject {
		t.Errorf("alias override should win: got %q, want %q", got, aliasProject)
	}

	// Empty alias ProjectID falls through to key-level.
	ctx2 := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx2.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "x",
		Config: &schemas.AliasConfig{
			ModelID: "x",
			VertexAliasCfg: &schemas.VertexAliasCfg{
				ProjectID: schemas.NewSecretVar(""),
			},
		},
	})
	if got := resolveVertexProjectID(ctx2, key); got != keyProject {
		t.Errorf("empty alias ProjectID should fall through: got %q, want %q", got, keyProject)
	}
}

// TestResolveVertexRegion_AliasOverride verifies the top-level
// AliasConfig.Region override for Vertex.
func TestResolveVertexRegion_AliasOverride(t *testing.T) {
	keyRegion := "us-central1"
	aliasRegion := "us-east5"
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			Region: *schemas.NewSecretVar(keyRegion),
		},
	}

	if got := resolveVertexRegion(nil, key); got != keyRegion {
		t.Errorf("nil ctx: got %q, want %q", got, keyRegion)
	}

	ctx0 := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if got := resolveVertexRegion(ctx0, key); got != keyRegion {
		t.Errorf("empty ctx: got %q, want key-level %q", got, keyRegion)
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID: "claude-sonnet-4-5",
			Region:  schemas.NewSecretVar(aliasRegion),
		},
	})
	if got := resolveVertexRegion(ctx, key); got != aliasRegion {
		t.Errorf("alias Region should win: got %q, want %q", got, aliasRegion)
	}

	ctx2 := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx2.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "x",
		Config: &schemas.AliasConfig{
			ModelID: "x",
			Region:  schemas.NewSecretVar(""),
		},
	})
	if got := resolveVertexRegion(ctx2, key); got != keyRegion {
		t.Errorf("empty alias Region should fall through: got %q, want %q", got, keyRegion)
	}
}

// TestResolveVertexProjectNumber_AliasOverride mirrors the ProjectID test.
func TestResolveVertexProjectNumber_AliasOverride(t *testing.T) {
	keyNumber := "111111"
	aliasNumber := "222222"
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectNumber: *schemas.NewSecretVar(keyNumber),
		},
	}

	if got := resolveVertexProjectNumber(nil, key); got != keyNumber {
		t.Errorf("nil ctx: got %q, want %q", got, keyNumber)
	}

	ctx0 := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if got := resolveVertexProjectNumber(ctx0, key); got != keyNumber {
		t.Errorf("empty ctx: got %q, want key-level %q", got, keyNumber)
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "x",
		Config: &schemas.AliasConfig{
			ModelID: "x",
			VertexAliasCfg: &schemas.VertexAliasCfg{
				ProjectNumber: schemas.NewSecretVar(aliasNumber),
			},
		},
	})
	if got := resolveVertexProjectNumber(ctx, key); got != aliasNumber {
		t.Errorf("alias ProjectNumber should win: got %q, want %q", got, aliasNumber)
	}

	ctx2 := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx2.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "x",
		Config: &schemas.AliasConfig{
			ModelID: "x",
			VertexAliasCfg: &schemas.VertexAliasCfg{
				ProjectNumber: schemas.NewSecretVar(""),
			},
		},
	})
	if got := resolveVertexProjectNumber(ctx2, key); got != keyNumber {
		t.Errorf("empty alias ProjectNumber should fall through: got %q, want %q", got, keyNumber)
	}
}
