package modelcatalog

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// Sarvam's deprecated datasheet rows are legacy /v1 models that Token Factory
// v2 does not serve, so the live list must not be backfilled with them.
func TestComputeModelsForProvider_SarvamSkipsDeprecatedDatasheetModels(t *testing.T) {
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(nil),
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
	mc.initCaches()
	mc.datasheet.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "sarvam-m", Provider: string(schemas.Sarvam), Mode: "chat", IsDeprecated: true},
		{Model: "glm5.3-flash", Provider: string(schemas.Sarvam), Mode: "chat"},
	})
	mc.UpsertLive(schemas.Sarvam, "k1", false, []string{"glm5.3-flash", "sarvam-105b"})

	got := mc.GetModelsForProvider(schemas.Sarvam)
	if slices.Contains(got, "sarvam-m") {
		t.Fatalf("GetModelsForProvider(sarvam) = %v, want sarvam-m excluded", got)
	}
	for _, want := range []string{"glm5.3-flash", "sarvam-105b"} {
		if !slices.Contains(got, want) {
			t.Fatalf("GetModelsForProvider(sarvam) = %v, want %q retained", got, want)
		}
	}
}
