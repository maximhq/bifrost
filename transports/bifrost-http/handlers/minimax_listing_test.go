package handlers

import (
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

func TestMinimaxManagementListing(t *testing.T) {
	models := []string{"MiniMax-M2.7", "speech-02-hd", "MiniMax-M3", "MiniMax-M3.1-Flash", "custom-model"}
	h := &ProviderHandler{
		inMemoryStore: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Minimax: {}}},
		modelsManager: &mockModelsManager{
			filtered:   map[schemas.ModelProvider][]string{schemas.Minimax: models},
			unfiltered: map[schemas.ModelProvider][]string{schemas.Minimax: models},
		},
	}
	for _, tc := range []struct {
		name  string
		query modelListQuery
		want  []string
		total int
	}{
		{"default", modelListQuery{Provider: schemas.Minimax, Limit: 100}, []string{"MiniMax-M3.1-Flash", "MiniMax-M3", "speech-02-hd", "custom-model"}, 4},
		{"pagination", modelListQuery{Provider: schemas.Minimax, Limit: 1}, []string{"MiniMax-M3.1-Flash"}, 4},
		{"offset", modelListQuery{Provider: schemas.Minimax, Limit: 1, Offset: 1}, []string{"MiniMax-M3"}, 4},
		{"legacy search", modelListQuery{Provider: schemas.Minimax, Limit: 100, Query: "M2.7"}, []string{"MiniMax-M2.7"}, 1},
		{"custom search", modelListQuery{Provider: schemas.Minimax, Limit: 100, Query: "custom-model"}, []string{"custom-model"}, 1},
		{"unfiltered", modelListQuery{Provider: schemas.Minimax, Limit: 100, Unfiltered: true}, models, 5},
		{"VK allowlist", modelListQuery{Provider: schemas.Minimax, Limit: 100, HasVKFilter: true, VKProviderConfigs: []tables.TableVirtualKeyProviderConfig{{Provider: "minimax", AllowedModels: schemas.WhiteList{"MiniMax-M3"}}}}, []string{"MiniMax-M3"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := h.listManagementModels(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, model := range got {
				names = append(names, model.Name)
			}
			if total != tc.total || !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("got %v (total %d), want %v (total %d)", names, total, tc.want, tc.total)
			}
		})
	}
}
