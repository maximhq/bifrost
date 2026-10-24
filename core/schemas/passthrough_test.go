package schemas

import (
	"strings"
	"testing"
)

func TestPassthroughPolicyValidate(t *testing.T) {
	valid := []PassthroughRouteRule{
		{Path: "/**"},
		{Path: "/"},
		{Path: "/v1/files"},
		{Methods: []string{"GET", "DELETE"}, Path: "/v1/files/*"},
		{Path: "/v1/batches/**"},
		{Path: "/v1beta/models/*:generateContent"},
		{Path: "/v1/models/claude-sonnet-4-5@20250929/predict"},
	}
	for _, rule := range valid {
		policy := &PassthroughPolicy{AllowedRoutes: []PassthroughRouteRule{rule}}
		if err := policy.Validate(); err != nil {
			t.Errorf("%+v: unexpected error %v", rule, err)
		}
	}
	if err := (*PassthroughPolicy)(nil).Validate(); err != nil {
		t.Errorf("nil policy: %v", err)
	}
	if err := (&PassthroughPolicy{}).Validate(); err != nil {
		t.Errorf("empty policy: %v", err)
	}

	invalid := []struct {
		rule PassthroughRouteRule
		want string
	}{
		{PassthroughRouteRule{}, "path must start with /"},
		{PassthroughRouteRule{Methods: []string{"POST"}}, "path must start with /"},
		{PassthroughRouteRule{Path: "v1/files"}, "must start with /"},
		{PassthroughRouteRule{Path: "/v1/files?purpose=batch"}, "must not contain"},
		{PassthroughRouteRule{Path: "/v1/%66iles"}, "must not contain"},
		{PassthroughRouteRule{Path: "/v1/[fF]iles"}, "must not contain"},
		{PassthroughRouteRule{Path: "/v1\\files"}, "must not contain"},
		{PassthroughRouteRule{Path: "//v1/files"}, "empty, . or .. segments"},
		{PassthroughRouteRule{Path: "/v1/files/"}, "empty, . or .. segments"},
		{PassthroughRouteRule{Path: "/v1/../admin"}, "empty, . or .. segments"},
		{PassthroughRouteRule{Path: "/v1/**/content"}, "only allowed as the last"},
		{PassthroughRouteRule{Path: "/v1/files**"}, "only allowed as the last"},
		{PassthroughRouteRule{Methods: []string{"TRACE"}, Path: "/v1/files"}, "unsupported method"},
		{PassthroughRouteRule{Methods: []string{"get"}, Path: "/v1/files"}, "unsupported method"},
	}
	for _, tc := range invalid {
		policy := &PassthroughPolicy{AllowedRoutes: []PassthroughRouteRule{{Path: "/ok"}, tc.rule}}
		err := policy.Validate()
		if err == nil {
			t.Errorf("%+v: expected an error containing %q", tc.rule, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "allowed_routes[1]: ") {
			t.Errorf("%+v: error %q, want allowed_routes[1] and %q", tc.rule, err, tc.want)
		}
	}
}

func TestPassthroughRouteRuleMatches(t *testing.T) {
	for _, tc := range []struct {
		rule       PassthroughRouteRule
		method     string
		apiVersion string
		path       string
		want       bool
	}{
		{PassthroughRouteRule{Path: "/**"}, "DELETE", "", "/v1/organization/users/u1", true},
		{PassthroughRouteRule{Path: "/**"}, "POST", "v1beta", "/models/m:generateContent", true},
		{PassthroughRouteRule{Path: "/v1/files"}, "GET", "", "/v1/files", true},
		{PassthroughRouteRule{Path: "/v1/files"}, "GET", "", "/v1/files/f1", false},
		{PassthroughRouteRule{Path: "/v1/files"}, "GET", "", "/v1/files/", false},
		{PassthroughRouteRule{Path: "/v1/files"}, "GET", "", "/files", false},
		{PassthroughRouteRule{Path: "/v1/files"}, "GET", "", "/V1/FILES", false},
		{PassthroughRouteRule{Path: "/v1/files/*"}, "GET", "", "/v1/files/f1", true},
		{PassthroughRouteRule{Path: "/v1/files/*"}, "GET", "", "/v1/files/f1/content", false},
		{PassthroughRouteRule{Path: "/v1/files/*"}, "GET", "", "/v1/files/", false},
		{PassthroughRouteRule{Path: "/v1/files/*"}, "GET", "", "/v1/files", false},
		{PassthroughRouteRule{Path: "/v1/batches/**"}, "POST", "", "/v1/batches", true},
		{PassthroughRouteRule{Path: "/v1/batches/**"}, "POST", "", "/v1/batches/b1/cancel", true},
		{PassthroughRouteRule{Path: "/v1/batches/**"}, "POST", "", "/v1/batchesx", false},
		{PassthroughRouteRule{Path: "/v1/models/*/predict"}, "POST", "", "/v1/models/abc/predict", true},
		{PassthroughRouteRule{Path: "/v1/models/*/predict"}, "POST", "", "/v1/models/a/b/predict", false},
		{PassthroughRouteRule{Path: "/v1beta/models/*:generateContent"}, "POST", "v1beta", "/models/gemini-2.5-flash:generateContent", true},
		{PassthroughRouteRule{Path: "/v1beta/models/*:generateContent"}, "POST", "v1", "/models/gemini-2.5-flash:generateContent", false},
		{PassthroughRouteRule{Path: "/v1beta/models/*:generateContent"}, "POST", "", "/models/gemini-2.5-flash:generateContent", false},
		{PassthroughRouteRule{Path: "/models/*:generateContent"}, "POST", "v1beta", "/models/gemini-2.5-flash:generateContent", false},
		{PassthroughRouteRule{Path: "/*/models/**"}, "POST", "v1beta1", "/models/m:predict", true},
		{PassthroughRouteRule{Methods: []string{"GET", "delete"}, Path: "/v1/files/*"}, "DELETE", "", "/v1/files/f1", true},
		{PassthroughRouteRule{Methods: []string{"GET", "DELETE"}, Path: "/v1/files/*"}, "POST", "", "/v1/files/f1", false},
		{PassthroughRouteRule{}, "POST", "", "/v1/chat/completions", false},
	} {
		if got := tc.rule.Matches(tc.method, tc.apiVersion, tc.path); got != tc.want {
			t.Errorf("%+v %s %q %s: got %v, want %v", tc.rule, tc.method, tc.apiVersion, tc.path, got, tc.want)
		}
	}
}
