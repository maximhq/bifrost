package postgresconn

import (
	"errors"
	"fmt"
	"testing"
)

// fakeSQLStateError is a driver error exposing a SQLSTATE the way *pgconn.PgError does.
type fakeSQLStateError struct{ state string }

// Error renders the fake without the "(SQLSTATE ...)" suffix, so tests exercise the typed path alone.
func (e fakeSQLStateError) Error() string { return "driver error" }

// SQLState returns the fake's SQLSTATE.
func (e fakeSQLStateError) SQLState() string { return e.state }

// TestIsTransientLockError covers the typed and rendered forms of every retried
// SQLSTATE, and checks that unrelated errors are not retried.
func TestIsTransientLockError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed deadlock", fakeSQLStateError{"40P01"}, true},
		{"wrapped typed serialization failure", fmt.Errorf("write: %w", fakeSQLStateError{"40001"}), true},
		{"typed lock timeout", fakeSQLStateError{"55P03"}, true},
		{"typed statement timeout", fakeSQLStateError{"57014"}, true},
		{"rendered deadlock", errors.New("failed to update budget b: ERROR: deadlock detected (SQLSTATE 40P01)"), true},
		{"rendered statement timeout", errors.New("ERROR: canceling statement due to statement timeout (SQLSTATE 57014)"), true},
		{"typed unique violation", fakeSQLStateError{"23505"}, false},
		{"rendered foreign key violation", errors.New("ERROR: insert violates foreign key (SQLSTATE 23503)"), false},
		{"plain error", errors.New("invalid reset duration"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientLockError(tc.err); got != tc.want {
				t.Fatalf("IsTransientLockError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
