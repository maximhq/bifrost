package replicate

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// The per-model input image field resolves from the datasheet where it carries a record, falling
// back to the built-in table so a catalog-less deployment is unchanged.
func TestGetInputImageFieldName_CatalogThenTable(t *testing.T) {
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities {
		return &schemas.ModelCapabilities{
			FieldNames: map[string]string{schemas.LogicalFieldInputImage: "image_prompt"},
		}
	})
	// flux-kontext-pro is "input_image" in the table; the datasheet overrides it.
	if got := getInputImageFieldName(schemas.ResolveModelCaps(schemas.Replicate, "black-forest-labs/flux-kontext-pro")); got != "image_prompt" {
		t.Fatalf("datasheet should win, got %q", got)
	}
	schemas.SetCapabilityResolver(nil)

	for model, want := range map[string]string{
		"black-forest-labs/flux-kontext-pro":      "input_image",
		"black-forest-labs/flux-1.1-pro":          "image_prompt",
		"black-forest-labs/flux-dev":              "image",
		"black-forest-labs/flux-kontext-pro:abc1": "input_image",
		"some-owner/unlisted-model":               "input_images",
	} {
		if got := getInputImageFieldName(schemas.ResolveModelCaps(schemas.Replicate, model)); got != want {
			t.Errorf("%s: got %q, want %q", model, got, want)
		}
	}
}
