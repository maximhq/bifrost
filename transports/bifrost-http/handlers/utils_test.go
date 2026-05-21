package handlers

import (
	"errors"
	"testing"
)

// TestIsUniqueConstraintError recognizes common database unique-constraint messages.
func TestIsUniqueConstraintError(t *testing.T) {
	cases := []string{
		"UNIQUE constraint failed: enterprise_access_profiles.name",
		`pq: duplicate key value violates unique constraint "idx_access_profiles_name"`,
		"Error 1062: Duplicate entry 'profile-a' for key 'enterprise_access_profiles.name'",
	}
	for _, tc := range cases {
		if !IsUniqueConstraintError(errors.New(tc)) {
			t.Fatalf("IsUniqueConstraintError(%q)=false, want true", tc)
		}
	}
	if IsUniqueConstraintError(errors.New("connection refused")) {
		t.Fatalf("non-unique error should not match")
	}
}

// TestIsUniqueConstraintError_Identifiers narrows matches to requested fields or indexes.
func TestIsUniqueConstraintError_Identifiers(t *testing.T) {
	err := errors.New(`pq: duplicate key value violates unique constraint "idx_access_profiles_name"`)
	if !IsUniqueConstraintError(err, "idx_access_profiles_name") {
		t.Fatalf("identifier match returned false")
	}
	if IsUniqueConstraintError(err, "enterprise_users.email") {
		t.Fatalf("unrelated identifier matched")
	}
}
