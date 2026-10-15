package tables

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProviderJobIDBatchFormIsFrozen is a regression guard on a primary key, not a
// style preference. Every batch row already in batch_jobs is keyed by this exact
// string. If the batch form ever changes, running code stops finding those rows,
// treats each settled batch as unseen, and bills it a second time.
func TestProviderJobIDBatchFormIsFrozen(t *testing.T) {
	assert.Equal(t, "batch-job:openai:batch_abc",
		ProviderJobID(ProviderJobKindBatch, "openai", "batch_abc"))

	// An absent kind is a pre-kind caller, and must resolve to the same row.
	assert.Equal(t, ProviderJobID(ProviderJobKindBatch, "openai", "batch_abc"),
		ProviderJobID("", "openai", "batch_abc"))
}

// TestProviderJobIDSeparatesKinds pins the other half: a video job must never
// collide with a batch that happens to carry the same provider-side id.
func TestProviderJobIDSeparatesKinds(t *testing.T) {
	batch := ProviderJobID(ProviderJobKindBatch, "openai", "id_123")
	video := ProviderJobID(ProviderJobKindVideo, "openai", "id_123")
	assert.NotEqual(t, batch, video)
	assert.Equal(t, "video-job:openai:id_123", video)
}
