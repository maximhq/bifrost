package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestCheckURLAccessibility_FileExists(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "pricing-*.json")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := checkURLAccessibility("file://" + f.Name()); err != nil {
		t.Fatalf("expected no error for existing file, got: %v", err)
	}
}

func TestCheckURLAccessibility_FileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	if err := checkURLAccessibility("file://" + path); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestCheckURLAccessibility_HTTP200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := checkURLAccessibility(srv.URL); err != nil {
		t.Fatalf("expected no error for HTTP 200, got: %v", err)
	}
}

func TestCheckURLAccessibility_HTTPNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := checkURLAccessibility(srv.URL); err == nil {
		t.Fatal("expected error for HTTP 404, got nil")
	}
}
