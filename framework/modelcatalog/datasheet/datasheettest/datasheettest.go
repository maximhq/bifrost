// Package datasheettest builds model catalog datasheets for tests.
package datasheettest

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore/livestoretest"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// New returns a datasheet from datasheet.New over a new live store closed when the test ends.
func New(t testing.TB, configStore configstore.ConfigStore, logger schemas.Logger, cfg datasheet.Config) *datasheet.Store {
	t.Helper()
	ds, err := datasheet.New(configStore, livestoretest.New(t), logger, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}
