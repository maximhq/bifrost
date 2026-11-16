package grant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// uniqueEntries is the accumulator every tool-pattern list is built through. It owns both the list
// and the record of what is already in it, so the two cannot be paired wrongly, which is the
// mistake it exists to make unrepresentable.
func TestUniqueEntries(t *testing.T) {
	t.Run("keeps insertion order and drops repeats", func(t *testing.T) {
		entries := newUniqueEntries(4)
		for _, entry := range []string{"github-read_file", "slack-post", "github-read_file", "jira-create"} {
			entries.add(entry)
		}
		assert.Equal(t, []string{"github-read_file", "slack-post", "jira-create"}, entries.list())
	})

	t.Run("a repeat does not move an entry", func(t *testing.T) {
		// Order is what a consumer sees as precedence, so re-adding must not reshuffle.
		entries := newUniqueEntries(2)
		entries.add("first")
		entries.add("second")
		entries.add("first")
		assert.Equal(t, []string{"first", "second"}, entries.list())
	})

	// Empty means no tool may be executed, which a consumer must be able to tell from nothing
	// having been resolved, so the list is never nil.
	t.Run("nothing added is empty, not nil", func(t *testing.T) {
		entries := newUniqueEntries(0)
		assert.NotNil(t, entries.list())
		assert.Empty(t, entries.list())
	})

	t.Run("the empty string is an entry like any other", func(t *testing.T) {
		// Skipping unnamed tools is the caller's rule, not this accumulator's.
		entries := newUniqueEntries(2)
		entries.add("")
		entries.add("")
		assert.Equal(t, []string{""}, entries.list())
	})

	t.Run("capacity is a hint, not a limit", func(t *testing.T) {
		entries := newUniqueEntries(1)
		for _, entry := range []string{"a", "b", "c"} {
			entries.add(entry)
		}
		assert.Equal(t, []string{"a", "b", "c"}, entries.list())
	})
}
