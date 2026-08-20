package vertex

import (
	"context"
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
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

func TestVertexGeminiImageURLSchemesAllowGCS(t *testing.T) {
	result, err := gemini.ToGeminiChatCompletionRequestWithImageURLSchemes(nil, &schemas.BifrostChatRequest{
		Model: "gemini-3-flash-preview",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL: "gs://my-bucket/xxx.png",
							},
						},
					},
				},
			},
		},
	}, geminiImageURLSchemes...)
	if err != nil {
		t.Fatalf("expected Vertex Gemini schemes to allow gs:// image URLs, got: %v", err)
	}
	if len(result.Contents) != 1 || len(result.Contents[0].Parts) != 1 || result.Contents[0].Parts[0].FileData == nil {
		t.Fatalf("expected one fileData part, got %#v", result.Contents)
	}
	if result.Contents[0].Parts[0].FileData.FileURI != "gs://my-bucket/xxx.png" {
		t.Fatalf("expected gs:// fileUri, got %q", result.Contents[0].Parts[0].FileData.FileURI)
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
		name              string
		region            string
		model             string
		forceSingleRegion bool
		expected          string
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
		{
			name:              "force single region skips us pool promotion for flagged model",
			region:            "us-east5",
			model:             "claude-opus-4-7",
			forceSingleRegion: true,
			expected:          "us-east5-aiplatform.googleapis.com",
		},
		{
			name:              "force single region skips eu pool promotion for flagged model",
			region:            "europe-west1",
			model:             "claude-opus-4-7",
			forceSingleRegion: true,
			expected:          "europe-west1-aiplatform.googleapis.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := getVertexModelAwareAPIHost(tt.region, tt.model, tt.forceSingleRegion, nil)
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
		name              string
		region            string
		model             string
		forceSingleRegion bool
		expected          string
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
		{
			name:              "force single region keeps flagged model on requested region host and path",
			region:            "us-east5",
			model:             "claude-opus-4-7",
			forceSingleRegion: true,
			expected:          "https://us-east5-aiplatform.googleapis.com/v1/projects/project-123/locations/us-east5/publishers/anthropic/models/claude-opus-4-7:rawPredict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := getVertexModelAwarePublisherModelURL(tt.region, "v1", "project-123", "anthropic", tt.model, ":rawPredict", tt.forceSingleRegion, nil)
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

// TestResolveVertexForceSingleRegion_AliasOverride verifies the per-alias
// VertexAliasCfg.ForceSingleRegion (*bool) override: an explicit alias-level
// value wins over the key-level bool — including an alias false overriding a key
// true — while a nil alias value falls through to the key-level bool.
func TestResolveVertexForceSingleRegion_AliasOverride(t *testing.T) {
	keyForceTrue := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{ForceSingleRegion: true},
	}
	keyForceFalse := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{ForceSingleRegion: false},
	}

	// nil ctx uses the key-level value.
	if got := resolveVertexForceSingleRegion(nil, keyForceTrue); !got {
		t.Errorf("nil ctx: got %v, want key-level true", got)
	}

	// empty ctx (no resolved alias) uses the key-level value.
	ctx0 := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if got := resolveVertexForceSingleRegion(ctx0, keyForceFalse); got {
		t.Errorf("empty ctx: got %v, want key-level false", got)
	}

	// Alias-level true overrides key-level false.
	ctxTrue := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctxTrue.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID:        "claude-opus-4-7",
			VertexAliasCfg: &schemas.VertexAliasCfg{ForceSingleRegion: schemas.Ptr(true)},
		},
	})
	if got := resolveVertexForceSingleRegion(ctxTrue, keyForceFalse); !got {
		t.Errorf("alias true should win over key false: got %v, want true", got)
	}

	// Alias-level false overrides key-level true (explicit set beats nil).
	ctxFalse := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctxFalse.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID:        "claude-opus-4-7",
			VertexAliasCfg: &schemas.VertexAliasCfg{ForceSingleRegion: schemas.Ptr(false)},
		},
	})
	if got := resolveVertexForceSingleRegion(ctxFalse, keyForceTrue); got {
		t.Errorf("alias false should win over key true: got %v, want false", got)
	}

	// Alias present but ForceSingleRegion nil falls through to the key-level value.
	ctxNil := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctxNil.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID:        "claude-opus-4-7",
			VertexAliasCfg: &schemas.VertexAliasCfg{ForceSingleRegion: nil},
		},
	})
	if got := resolveVertexForceSingleRegion(ctxNil, keyForceTrue); !got {
		t.Errorf("nil alias value should fall through to key true: got %v, want true", got)
	}
}

// TestGeminiImageURLSchemesContract pins the exact allowlist that Vertex passes
// into the Gemini converter. vertex.go reuses geminiImageURLSchemes across
// ChatCompletion / ChatCompletionStream / Responses / ResponsesStream / CountTokens,
// so a regression that drops "gs" (or accidentally adds e.g. "file") is caught
// here without having to drive each provider entrypoint end-to-end.
func TestGeminiImageURLSchemesContract(t *testing.T) {
	want := []string{"http", "https", "gs"}
	if !reflect.DeepEqual(geminiImageURLSchemes, want) {
		t.Fatalf("geminiImageURLSchemes = %v, want %v", geminiImageURLSchemes, want)
	}
}

func TestVertexServiceTierHeaderValue_Gemini35FlashLiteFlex(t *testing.T) {
	t.Parallel()

	flex := schemas.BifrostServiceTierFlex
	if got := vertexServiceTierHeaderValue("global", "gemini-3.5-flash-lite", flex); got != "flex" {
		t.Fatalf("expected flex header for gemini-3.5-flash-lite on global, got %q", got)
	}
	if got := vertexServiceTierHeaderValue("us-central1", "gemini-3.5-flash-lite", flex); got != "" {
		t.Fatalf("expected no flex header outside global region, got %q", got)
	}
}

// TestClassifyURLSource pins the capability matrix this provider implements. Vertex
// serves two model families with different source support -- Claude-on-Vertex takes
// base64 only, Gemini-on-Vertex takes fileData.fileUri -- so the same URL is downloaded
// for one and forwarded for the other. Getting this wrong is invisible until a live
// request fails, which is how the gs:// regression shipped.
func TestClassifyURLSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		url       string
		anthropic bool
		want      urlSourceDisposition
	}{
		{name: "https to gemini is fetched", url: "https://example.com/a.pdf", want: urlSourceFetchHTTP},
		{name: "http to gemini is fetched", url: "http://example.com/a.pdf", want: urlSourceFetchHTTP},
		{name: "gcs to gemini is forwarded", url: "gs://bucket/clip.mp4", want: urlSourceForward},
		{name: "data uri to gemini is forwarded", url: "data:image/png;base64,iVBORw0KGgo=", want: urlSourceForward},

		{name: "https to claude is fetched", url: "https://example.com/a.pdf", anthropic: true, want: urlSourceFetchHTTP},
		{name: "http to claude is fetched", url: "http://example.com/a.pdf", anthropic: true, want: urlSourceFetchHTTP},
		{name: "gcs to claude is fetched from storage", url: "gs://bucket/clip.mp4", anthropic: true, want: urlSourceFetchGCS},
		{name: "data uri to claude is forwarded", url: "data:image/png;base64,iVBORw0KGgo=", anthropic: true, want: urlSourceForward},

		// Schemes bifrost cannot fetch are still handed to the provider rather than
		// rejected here: bifrost does not decide what the provider accepts, and the
		// provider's own answer is the authoritative one.
		{name: "s3 is forwarded for gemini", url: "s3://bucket/a.pdf", want: urlSourceForward},
		{name: "s3 is forwarded for claude", url: "s3://bucket/a.pdf", anthropic: true, want: urlSourceForward},
		{name: "file scheme is forwarded", url: "file:///etc/passwd", want: urlSourceForward},
		{name: "scheme-less value is forwarded", url: "bucket/a.pdf", want: urlSourceForward},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := classifyURLSource(tt.url, tt.anthropic); got != tt.want {
				t.Fatalf("classifyURLSource(%q, anthropic=%v) = %v, want %v", tt.url, tt.anthropic, got, tt.want)
			}
		})
	}
}

// forwardedGeminiURLs are the URL forms a Gemini-family request must reach Vertex with
// intact. gs:// is the load-bearing case: downloading it fails outright today (the
// reported bug) and forwarding keeps large media out of the 25 MiB inline path.
// https is deliberately absent -- Vertex rejects a forwarded https fileUri, measured
// against harness 47.10, so it stays on the fetch path. See classifyURLSource.
var forwardedGeminiURLs = []struct {
	name string
	url  string
}{
	{name: "gcs", url: "gs://my-bucket/video-eval/clip.mp4"},
	{name: "data uri", url: "data:image/png;base64,iVBORw0KGgo="},
}

func TestInlineRemoteURLSourcesForwardsGeminiURLs(t *testing.T) {
	t.Parallel()

	for _, tc := range forwardedGeminiURLs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &VertexProvider{}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			fileURL := tc.url
			request := &schemas.BifrostChatRequest{
				Model: "gemini-3.1-pro-preview",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentBlocks: []schemas.ChatContentBlock{
								{
									Type: schemas.ChatContentBlockTypeFile,
									File: &schemas.ChatInputFile{FileURL: &fileURL},
								},
								{
									Type:           schemas.ChatContentBlockTypeImage,
									ImageURLStruct: &schemas.ChatInputImage{URL: tc.url},
								},
							},
						},
					},
				},
			}

			if err := provider.inlineRemoteURLSources(ctx, schemas.Key{}, request); err != nil {
				t.Fatalf("inlineRemoteURLSources(%q) returned error, want forward: %v", tc.url, err)
			}

			blocks := request.Input[0].Content.ContentBlocks
			if got := blocks[0].File.FileURL; got == nil || *got != tc.url {
				t.Errorf("file url = %v, want %q untouched", got, tc.url)
			}
			if blocks[0].File.FileData != nil {
				t.Errorf("file data = %q, want nil (nothing should have been fetched)", *blocks[0].File.FileData)
			}
			if got := blocks[1].ImageURLStruct.URL; got != tc.url {
				t.Errorf("image url = %q, want %q untouched", got, tc.url)
			}
		})
	}
}

func TestInlineDocumentURLsResponsesForwardsGeminiURLs(t *testing.T) {
	t.Parallel()

	for _, tc := range forwardedGeminiURLs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &VertexProvider{}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			fileURL := tc.url
			imageURL := tc.url
			role := schemas.ResponsesInputMessageRoleUser
			request := &schemas.BifrostResponsesRequest{
				Model: "gemini-3.1-pro-preview",
				Input: []schemas.ResponsesMessage{
					{
						Role: &role,
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type:                                  schemas.ResponsesInputMessageContentBlockTypeFile,
									ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{FileURL: &fileURL},
								},
								{
									Type:                                   schemas.ResponsesInputMessageContentBlockTypeImage,
									ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: &imageURL},
								},
							},
						},
					},
				},
			}

			if err := provider.inlineDocumentURLsResponses(ctx, schemas.Key{}, request); err != nil {
				t.Fatalf("inlineDocumentURLsResponses(%q) returned error, want forward: %v", tc.url, err)
			}

			blocks := request.Input[0].Content.ContentBlocks
			if got := blocks[0].ResponsesInputMessageContentBlockFile.FileURL; got == nil || *got != tc.url {
				t.Errorf("file url = %v, want %q untouched", got, tc.url)
			}
			if got := blocks[1].ResponsesInputMessageContentBlockImage.ImageURL; got == nil || *got != tc.url {
				t.Errorf("image url = %v, want %q untouched", got, tc.url)
			}
			// An unchanged URL alone does not prove the object was left alone: a
			// regression that fetched the bytes AND kept the URL would satisfy the two
			// checks above. FileData is what says nothing was downloaded, which is the
			// actual claim of this test. The chat twin already asserts it, so without
			// this the invariant is only half covered - and the uncovered half is the
			// Responses path.
			if got := blocks[0].ResponsesInputMessageContentBlockFile.FileData; got != nil {
				t.Errorf("file data = %v, want nil (the reference must be forwarded, not fetched)", *got)
			}
		})
	}
}

// TestInlineRemoteURLSourcesForwardsUnfetchableSchemes: a scheme bifrost cannot download
// is passed through untouched rather than rejected. Bifrost does not model which sources
// a provider accepts - the provider answers for itself, and its capabilities can change
// without a bifrost release.
func TestInlineRemoteURLSourcesForwardsUnfetchableSchemes(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{"s3://my-bucket/doc.pdf", "bucket/doc.pdf", "file:///etc/passwd"} {
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()

			provider := &VertexProvider{}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			fileURL := rawURL
			request := &schemas.BifrostChatRequest{
				Model: "gemini-3.1-pro-preview",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentBlocks: []schemas.ChatContentBlock{
								{
									Type: schemas.ChatContentBlockTypeFile,
									File: &schemas.ChatInputFile{FileURL: &fileURL},
								},
							},
						},
					},
				},
			}

			if err := provider.inlineRemoteURLSources(ctx, schemas.Key{}, request); err != nil {
				t.Fatalf("inlineRemoteURLSources(%q) errored, want pass-through: %v", rawURL, err)
			}

			block := request.Input[0].Content.ContentBlocks[0]
			if got := block.File.FileURL; got == nil || *got != rawURL {
				t.Errorf("file url = %v, want %q forwarded untouched", got, rawURL)
			}
			if block.File.FileData != nil {
				t.Errorf("file data = %q, want nil: bifrost must not have fetched anything", *block.File.FileData)
			}
		})
	}
}

// TestVertexGeminiGCSFileURLSurvivesInlining is the end-to-end regression for the
// reported failure: a video file block carrying a gs:// URI must reach the Gemini
// converter intact and come out as a fileData part. TestVertexGeminiImageURLSchemesAllowGCS
// only exercises the converter in isolation, which is why the inliner running ahead of it
// went unnoticed.
func TestVertexGeminiGCSFileURLSurvivesInlining(t *testing.T) {
	t.Parallel()

	const gcsURI = "gs://dropbox-ml-dev-uploads/video-eval/clip.mp4"
	provider := &VertexProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	fileURL := gcsURI
	fileType := "video/mp4"
	request := &schemas.BifrostChatRequest{
		Model: "gemini-3.1-pro-preview",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeFile,
							File: &schemas.ChatInputFile{FileURL: &fileURL, FileType: &fileType},
						},
					},
				},
			},
		},
	}

	if err := provider.inlineRemoteURLSources(ctx, schemas.Key{}, request); err != nil {
		t.Fatalf("inlineRemoteURLSources returned error for gs:// file source: %v", err)
	}

	result, err := gemini.ToGeminiChatCompletionRequestWithImageURLSchemes(ctx, request, geminiImageURLSchemes...)
	if err != nil {
		t.Fatalf("converting gs:// file source failed: %v", err)
	}
	if len(result.Contents) != 1 || len(result.Contents[0].Parts) != 1 {
		t.Fatalf("expected one part, got %#v", result.Contents)
	}
	part := result.Contents[0].Parts[0]
	if part.FileData == nil {
		t.Fatalf("expected a fileData part, got %#v", part)
	}
	if part.FileData.FileURI != gcsURI {
		t.Errorf("fileUri = %q, want %q", part.FileData.FileURI, gcsURI)
	}
	if part.FileData.MIMEType != fileType {
		t.Errorf("mimeType = %q, want %q", part.FileData.MIMEType, fileType)
	}
}
