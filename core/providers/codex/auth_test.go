package codex

import "testing"

func TestExtractAccountID(t *testing.T) {
	token := &TokenResponse{
		AccessToken: "eyJhbGciOiJub25lIn0.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoib3JnXzEyMyJ9fQ.",
	}
	if accountID := ExtractAccountID(token); accountID != "org_123" {
		t.Fatalf("expected account id org_123, got %q", accountID)
	}
}

func TestExpiresAtFromNow(t *testing.T) {
	value := ExpiresAtFromNow(60)
	if value == "" {
		t.Fatal("expected non-empty RFC3339 expiry")
	}
}
