package tables

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateMetadata(t *testing.T) {
	tooMany := make(map[string]string, MaxMetadataEntries+1)
	for i := range MaxMetadataEntries + 1 {
		tooMany[fmt.Sprintf("k%d", i)] = "v"
	}
	reserved := func(key string) bool { return key == "system" }
	tests := []struct {
		name       string
		metadata   map[string]string
		isReserved func(string) bool
		wantErr    string
	}{
		{name: "nil", metadata: nil},
		{name: "valid", metadata: map[string]string{"owner": "team-a", "region.primary": "eu-west-1", "cost_center": ""}},
		{name: "reserved key allowed without a reserved func", metadata: map[string]string{"system": "x"}},
		{name: "reserved key refused", metadata: map[string]string{"system": "x"}, isReserved: reserved, wantErr: "reserved"},
		{name: "too many entries", metadata: tooMany, wantErr: "at most 50 entries"},
		{name: "key with space", metadata: map[string]string{"cost center": "x"}, wantErr: "invalid metadata key"},
		{name: "key too long", metadata: map[string]string{strings.Repeat("k", MaxMetadataKeyLength+1): "v"}, wantErr: "invalid metadata key"},
		{name: "value too long", metadata: map[string]string{"k": strings.Repeat("v", MaxMetadataValueLength+1)}, wantErr: "at most 512 characters"},
		{name: "first bad key in sorted order is reported", metadata: map[string]string{"z z": "x", "a a": "x"}, wantErr: `"a a"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMetadata(tt.metadata, tt.isReserved)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNormalizeTags(t *testing.T) {
	tooMany := make([]string, 0, MaxTags+1)
	for i := range MaxTags + 1 {
		tooMany = append(tooMany, fmt.Sprintf("t%d", i))
	}
	atLimitWithDuplicates := append(append([]string{}, tooMany[:MaxTags]...), "t0", " t1 ")
	tests := []struct {
		name    string
		tags    []string
		want    []string
		wantErr string
	}{
		{name: "nil", tags: nil, want: nil},
		{name: "empty", tags: []string{}, want: nil},
		{name: "trimmed, de-duplicated and sorted", tags: []string{" prod ", "eu", "prod", "approved-for-pii"}, want: []string{"approved-for-pii", "eu", "prod"}},
		{name: "case-sensitive", tags: []string{"Prod", "prod"}, want: []string{"Prod", "prod"}},
		{name: "dots and underscores", tags: []string{"team_payments", "env.prod"}, want: []string{"env.prod", "team_payments"}},
		{name: "max length", tags: []string{strings.Repeat("t", MaxTagLength)}, want: []string{strings.Repeat("t", MaxTagLength)}},
		{name: "duplicates do not count toward the limit", tags: atLimitWithDuplicates, want: slices.Sorted(slices.Values(tooMany[:MaxTags]))},
		{name: "too long", tags: []string{strings.Repeat("t", MaxTagLength+1)}, wantErr: "invalid tag"},
		{name: "comma", tags: []string{"a,b"}, wantErr: "invalid tag"},
		{name: "space inside", tags: []string{"a b"}, wantErr: "invalid tag"},
		{name: "colon", tags: []string{"env:prod"}, wantErr: "invalid tag"},
		{name: "blank", tags: []string{"  "}, wantErr: "invalid tag"},
		{name: "too many", tags: tooMany, wantErr: "at most 50 entries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeTags(tt.tags)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasAllTags(t *testing.T) {
	have := []string{"eu", "prod"}
	assert.True(t, HasAllTags(have, nil), "no wanted tags matches everything")
	assert.True(t, HasAllTags(have, []string{"prod"}))
	assert.True(t, HasAllTags(have, []string{"prod", "eu"}))
	assert.False(t, HasAllTags(have, []string{"prod", "us"}), "every wanted tag must be present")
	assert.False(t, HasAllTags(nil, []string{"prod"}))
}
