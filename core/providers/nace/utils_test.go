package nace

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The API accepts "<name>/<version>" only when the version matches this set.
var declaredPart = regexp.MustCompile(`^[A-Za-z0-9 ._@+-]+$`)

func TestClientIdentity(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    string
	}{
		{"2.5.0", "bifrost/2.5.0"},
		{" v2.5.0-rc.1+build ", "bifrost/v2.5.0-rc.1+build"},
		{"2.5.0/build", "bifrost/2.5.0build"},
		{"2.5.0 (darwin)", "bifrost/2.5.0darwin"},
		{"", "bifrost/dev"},
	} {
		got := clientIdentity(tc.version)
		require.Equal(t, tc.want, got, "version %q", tc.version)
		require.True(t, declaredPart.MatchString(strings.TrimPrefix(got, "bifrost/")), "version part of %q", got)
	}

	long := clientIdentity(strings.Repeat("9", 200))
	require.Len(t, long, 64)
	require.True(t, declaredPart.MatchString(strings.TrimPrefix(long, "bifrost/")))
}
