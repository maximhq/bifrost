package postgresconn

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestWithApplicationName covers the opt-in default, the role suffix, quoting,
// and the 63-byte cap keeping the role intact.
func TestWithApplicationName(t *testing.T) {
	t.Cleanup(func() { SetApplicationName("") })
	const dsn = "host='h' port='5432'"

	SetApplicationName("")
	if got := WithApplicationName(dsn, "config"); got != dsn {
		t.Fatalf("unset base changed the DSN: %q", got)
	}

	SetApplicationName("bifrost-node-1")
	if got := WithApplicationName(dsn, "logs"); got != dsn+" application_name='bifrost-node-1-logs'" {
		t.Fatalf("got %q", got)
	}

	SetApplicationName("bifrost-" + strings.Repeat("x", 80))
	got := WithApplicationName(dsn, "config")
	name := strings.TrimSuffix(strings.TrimPrefix(got, dsn+" application_name='"), "'")
	if len(name) != maxApplicationNameLen || !strings.HasSuffix(name, "-config") {
		t.Fatalf("long base not capped with role kept: %q (%d bytes)", name, len(name))
	}

	// A multibyte rune at the cut point: 57 ASCII bytes leave room for only the
	// first byte of "é" before "-logs". Postgres hex-escapes any non-printable-
	// ASCII byte server-side, which would also push the name past 63 and drop
	// the role, so the name must come out as printable ASCII within the cap.
	SetApplicationName(strings.Repeat("a", 57) + "é")
	got = WithApplicationName(dsn, "logs")
	name = strings.TrimSuffix(strings.TrimPrefix(got, dsn+" application_name='"), "'")
	if !utf8.ValidString(name) || len(name) > maxApplicationNameLen || !strings.HasSuffix(name, "-logs") {
		t.Fatalf("multibyte base not cut cleanly: %q (%d bytes)", name, len(name))
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] > 0x7e {
			t.Fatalf("non-printable-ASCII byte %#x at %d would be escaped by Postgres: %q", name[i], i, name)
		}
	}

	SetApplicationName("bifrost-hôte")
	if got := WithApplicationName(dsn, "config"); got != dsn+" application_name='bifrost-h_te-config'" {
		t.Fatalf("non-ASCII base not sanitized: %q", got)
	}
}
