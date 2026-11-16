package integrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterCodexModelList(t *testing.T) {
	allow := func(models ...string) func(string) bool {
		return func(m string) bool {
			for _, a := range models {
				if a == m {
					return true
				}
			}
			return false
		}
	}
	body := []byte(`{"models":[{"slug":"gpt-6.1-sol","priority":1,"visibility":"list"},{"slug":"gpt-6-astra"},{"slug":"gpt-5.6-terra","context_window":272000}],"etag":"x"}`)

	t.Run("keeps allowed entries in order with every field", func(t *testing.T) {
		out, err := filterCodexModelList(body, allow("gpt-5.6-terra", "gpt-6.1-sol"))
		require.NoError(t, err)
		require.JSONEq(t, `{"models":[{"slug":"gpt-6.1-sol","priority":1,"visibility":"list"},{"slug":"gpt-5.6-terra","context_window":272000}],"etag":"x"}`, string(out))
	})

	t.Run("nothing allowed leaves an empty array", func(t *testing.T) {
		out, err := filterCodexModelList(body, allow())
		require.NoError(t, err)
		require.JSONEq(t, `{"models":[],"etag":"x"}`, string(out))
	})

	t.Run("a body without models is unchanged", func(t *testing.T) {
		in := []byte(`{"detail":"nope"}`)
		out, err := filterCodexModelList(in, allow())
		require.NoError(t, err)
		require.Equal(t, string(in), string(out))
	})

	t.Run("invalid JSON is an error", func(t *testing.T) {
		_, err := filterCodexModelList([]byte(`{"models":[`), allow())
		require.Error(t, err)
	})
}
