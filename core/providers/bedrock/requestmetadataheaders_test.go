package bedrock

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestApplyHeaderRequestMetadata(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		existing map[string]string
		want     map[string]string
	}{
		{
			name: "copies prefixed headers only",
			headers: map[string]string{
				"x-bf-bedrock-metadata-tenant_slug": "acme",
				"x-bf-bedrock-metadata-trace-id":    "abc123",
				"x-bf-lh-user-id":                   "user-1",
			},
			want: map[string]string{"tenant_slug": "acme", "trace-id": "abc123"},
		},
		{
			name:     "extra_params value wins",
			headers:  map[string]string{"x-bf-bedrock-metadata-tenant_slug": "header"},
			existing: map[string]string{"tenant_slug": "body"},
			want:     map[string]string{"tenant_slug": "body"},
		},
		{
			name: "skips values bedrock would reject",
			headers: map[string]string{
				"x-bf-bedrock-metadata-user": "auth0|123",
				"x-bf-bedrock-metadata-ok":   "fine",
			},
			want: map[string]string{"ok": "fine"},
		},
		{
			name: "no headers leaves metadata unset",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, tt.headers)
			req := &BedrockConverseRequest{RequestMetadata: tt.existing}
			applyHeaderRequestMetadata(ctx, req)
			assert.Equal(t, tt.want, req.RequestMetadata)
		})
	}
}

func TestApplyHeaderRequestMetadataCapsEntries(t *testing.T) {
	headers := make(map[string]string)
	for i := range 20 {
		headers[fmt.Sprintf("x-bf-bedrock-metadata-k%02d", i)] = "v"
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	req := &BedrockConverseRequest{}
	applyHeaderRequestMetadata(ctx, req)
	assert.Len(t, req.RequestMetadata, maxRequestMetadataEntries)
	assert.Contains(t, req.RequestMetadata, "k00")
	assert.NotContains(t, req.RequestMetadata, "k19")
}
