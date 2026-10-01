package handlers

import "testing"

// TestEndpointSlugDerivable pins the pre-dial gate used by the MCP create handlers: an
// endpoint_slug (or, failing that, a name) that slugifies to empty is rejected, so an invalid
// request fails with a 400 before any upstream verification runs.
func TestEndpointSlugDerivable(t *testing.T) {
	cases := []struct {
		name         string
		endpointSlug string
		clientName   string
		want         bool
	}{
		{"derivable from name", "", "My Client", true},
		{"explicit slug rescues an underivable name", "custom-slug", "***", true},
		{"underivable name, no slug", "", "***", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		if got := endpointSlugDerivable(tc.endpointSlug, tc.clientName); got != tc.want {
			t.Errorf("%s: endpointSlugDerivable(%q, %q) = %v, want %v", tc.name, tc.endpointSlug, tc.clientName, got, tc.want)
		}
	}
}
