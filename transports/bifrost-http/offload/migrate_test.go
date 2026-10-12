package offload

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunMigrateOffloadRejectsPositionalArguments(t *testing.T) {
	assert.Equal(t, 2, RunMigrateOffload([]string{"/unexpected/path", "--dry-run"}))
}

func TestRunMigrateOffloadHelp(t *testing.T) {
	assert.Equal(t, 0, RunMigrateOffload([]string{"--help"}))
}
