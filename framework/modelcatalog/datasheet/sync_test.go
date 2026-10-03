package datasheet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestLoadFromURLRefusesRedirectToLinkLocal: the entry URL passes
// ValidateExternalURL (it is a reachable loopback server), but its 302 points
// at the cloud metadata address. The redirect hop must be refused and the
// error must say so; the datasheet must never be read from there.
func TestLoadFromURLRefusesRedirectToLinkLocal(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redirect.Close()
	store := New(nil, bifrost.NewDefaultLogger(schemas.LogLevelError), Config{URL: redirect.URL, ModelParametersURL: redirect.URL})

	_, err := store.loadPricingFromURL(context.Background())
	if err == nil || !strings.Contains(err.Error(), "link-local") {
		t.Fatalf("pricing: expected link-local refusal, got %v", err)
	}
	_, err = store.loadModelParametersFromURL(context.Background())
	if err == nil || !strings.Contains(err.Error(), "link-local") {
		t.Fatalf("model parameters: expected link-local refusal, got %v", err)
	}
}
