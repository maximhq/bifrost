package relay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compiledOverrides(t testing.TB, overrides map[string]string) []bodyOverride {
	t.Helper()
	raw := make(map[string]json.RawMessage, len(overrides))
	for path, value := range overrides {
		raw[path] = json.RawMessage(value)
	}
	compiled := compileBodyOverrides(raw)
	require.Len(t, compiled, len(overrides))
	return compiled
}

func TestApplyBodyOverrides(t *testing.T) {
	usage := map[string]string{"include_usage": "true"}
	cases := []struct {
		name        string
		overrides   map[string]string
		method      string
		body        string
		want        string
		wantCreated bool
		wantErr     string
	}{
		{name: "added when missing", overrides: usage, body: `{"query":"x"}`, want: `{"query":"x","include_usage":true}`},
		{name: "replaced when the caller disagrees", overrides: usage, body: `{"query":"x","include_usage":false}`, want: `{"query":"x","include_usage":true}`},
		{name: "kept when already set", overrides: usage, body: `{"include_usage":true,"query":"x"}`, want: `{"include_usage":true,"query":"x"}`},
		{name: "a value of another type is replaced", overrides: usage, body: `{"include_usage":"yes"}`, want: `{"include_usage":true}`},
		{name: "created for an empty POST", overrides: usage, body: ``, want: `{"include_usage":true}`, wantCreated: true},
		{name: "created for a whitespace PUT", overrides: usage, method: "PUT", body: " \n", want: `{"include_usage":true}`, wantCreated: true},
		{name: "an empty GET stays empty", overrides: usage, method: "GET", body: ``, want: ``},
		{name: "an empty DELETE stays empty", overrides: usage, method: "DELETE", body: ``, want: ``},
		{name: "a DELETE with a JSON body is set", overrides: usage, method: "DELETE", body: `{"id":1}`, want: `{"id":1,"include_usage":true}`},
		{name: "nested parent created", overrides: map[string]string{"options.depth": `"advanced"`}, body: `{"query":"x"}`, want: `{"query":"x","options":{"depth":"advanced"}}`},
		{name: "nested parent kept", overrides: map[string]string{"options.depth": `"advanced"`}, body: `{"options":{"depth":"basic","n":2}}`, want: `{"options":{"depth":"advanced","n":2}}`},
		{name: "object value written compact", overrides: map[string]string{"limits": "{ \"max\" : 3 }"}, body: `{}`, want: `{"limits":{"max":3}}`},
		{name: "several fields in path order", overrides: map[string]string{"b": "2", "a": "1"}, body: `{}`, want: `{"a":1,"b":2}`},
		{name: "a duplicate deep elsewhere is not our concern", overrides: usage, body: `{"doc":{"k":1,"k":2}}`, want: `{"doc":{"k":1,"k":2},"include_usage":true}`},
		{name: "exact duplicate refused", overrides: usage, body: `{"include_usage":false,"include_usage":false}`, wantErr: "more than once"},
		{name: "case variant duplicate refused", overrides: usage, body: `{"include_usage":true,"Include_Usage":false}`, wantErr: "more than once"},
		{name: "lone case variant refused", overrides: usage, body: `{"INCLUDE_USAGE":false}`, wantErr: "different letter case"},
		{name: "duplicate parent refused", overrides: map[string]string{"options.depth": `"advanced"`}, body: `{"options":{},"options":{"depth":"basic"}}`, wantErr: "names options more than once"},
		{name: "duplicate inside the parent refused", overrides: map[string]string{"options.depth": `"advanced"`}, body: `{"options":{"depth":"a","Depth":"b"}}`, wantErr: "names options.depth more than once"},
		{name: "non-object parent refused", overrides: map[string]string{"options.depth": `"advanced"`}, body: `{"options":5}`, wantErr: "non-object at options"},
		{name: "array body refused", overrides: usage, body: `[{"query":"x"}]`, wantErr: "must be a JSON object"},
		{name: "invalid JSON refused", overrides: usage, body: `{"query":`, wantErr: "must be a JSON object"},
		{name: "form body refused", overrides: usage, body: `query=x`, wantErr: "must be a JSON object"},
		{name: "no overrides leaves any body alone", body: `not json`, want: `not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = "POST"
			}
			var overrides []bodyOverride
			if tc.overrides != nil {
				overrides = compiledOverrides(t, tc.overrides)
			}
			in := []byte(tc.body)
			original := string(in)
			got, created, err := applyBodyOverrides(overrides, method, in)
			assert.Equal(t, original, string(in), "the caller's buffer is never modified in place")
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
			assert.Equal(t, tc.wantCreated, created)
		})
	}
}

func TestApplyBodyOverridesKeepsACorrectBodyWithoutACopy(t *testing.T) {
	body := []byte(`{"query":"x","include_usage":true}`)
	got, _, err := applyBodyOverrides(compiledOverrides(t, map[string]string{"include_usage": "true"}), "POST", body)
	require.NoError(t, err)
	assert.Same(t, &body[0], &got[0], "a body that already carries the value is forwarded as is")
}

func TestWithJSONContentTypeReplacesAnySpelling(t *testing.T) {
	in := map[string]string{"Content-Type": "text/plain", "x-trace": "1"}
	out := withJSONContentType(in)
	assert.Equal(t, map[string]string{"content-type": "application/json", "x-trace": "1"}, out)
	assert.Equal(t, "text/plain", in["Content-Type"], "the caller's map is not modified")
}

func BenchmarkApplyBodyOverrides(b *testing.B) {
	usage := compiledOverrides(b, map[string]string{"include_usage": "true"})
	small := []byte(`{"query":"who won the 2026 world cup","search_depth":"advanced","max_results":5,"include_answer":true}`)
	smallSet := []byte(`{"query":"who won the 2026 world cup","search_depth":"advanced","max_results":5,"include_answer":true,"include_usage":true}`)
	large := []byte(`{"document":"` + strings.Repeat("a", 1<<20) + `","query":"x"}`)
	cases := []struct {
		name      string
		overrides []bodyOverride
		body      []byte
	}{
		{"no overrides", nil, small},
		{"small already set", usage, smallSet},
		{"small rewrite", usage, small},
		{"1MiB rewrite", usage, large},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.body)))
			for b.Loop() {
				_, _, _ = applyBodyOverrides(tc.overrides, "POST", tc.body)
			}
		})
	}
}
