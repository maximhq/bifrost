package lib

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePromptCacheTTL(t *testing.T) {
	assert.NoError(t, ValidatePromptCache(nil), "no prompt_cache block is not a misconfiguration")
	assert.NoError(t, ValidatePromptCache(&schemas.PromptCacheConfig{AutoInject: true}),
		"an absent TTL means the provider default")
	assert.NoError(t, ValidatePromptCache(&schemas.PromptCacheConfig{TTL: schemas.Ptr("1h")}))

	for _, bad := range []string{"42m", "5 minutes", "1H", "3600", ""} {
		assert.Error(t, ValidatePromptCache(&schemas.PromptCacheConfig{TTL: schemas.Ptr(bad)}),
			"expected TTL %q to be rejected before it reaches the provider", bad)
	}
}

// The config file and the management API are two doors onto the same field, and only
// the file had a contract in front of it. This pins them together: whatever
// config.schema.json declares for prompt_cache.ttl is exactly what the API accepts, so
// the two cannot drift apart silently.
func TestValidatePromptCacheMatchesConfigSchemaEnum(t *testing.T) {
	var schema struct {
		Defs struct {
			PromptCache struct {
				Properties struct {
					TTL struct {
						Enum []string `json:"enum"`
					} `json:"ttl"`
				} `json:"properties"`
			} `json:"prompt_cache"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(loadLocalSchema(t), &schema))

	allowed := schema.Defs.PromptCache.Properties.TTL.Enum
	require.NotEmpty(t, allowed, "config.schema.json must declare an enum for prompt_cache.ttl")

	for _, ttl := range allowed {
		assert.NoError(t, ValidatePromptCache(&schemas.PromptCacheConfig{TTL: schemas.Ptr(ttl)}),
			"schema declares %q valid, so the API must accept it", ttl)
	}
	assert.Error(t, ValidatePromptCache(&schemas.PromptCacheConfig{TTL: schemas.Ptr("not-in-the-enum")}),
		"a value the schema rejects must not pass the API")
}

// An injection point whose location or role falls outside the schema's enums is not
// rejected at runtime, it silently matches nothing (matchMessageIndices in
// core/providers/utils/promptcache.go). A typo like "messages" or "sytem" is therefore
// stored, reported back as saved, and quietly never fires. The config-file path already
// rejects both; the API path must agree.
func TestValidatePromptCacheInjectionPoints(t *testing.T) {
	point := func(location string, role *string) *schemas.PromptCacheConfig {
		return &schemas.PromptCacheConfig{
			InjectionPoints: []schemas.CacheControlInjectionPoint{
				{Location: location, Role: role, Index: schemas.Ptr(0)},
			},
		}
	}

	assert.NoError(t, ValidatePromptCache(point("message", nil)))
	assert.NoError(t, ValidatePromptCache(point("", nil)),
		"location is optional in the schema, so an absent one is not a violation")
	for _, role := range []string{"system", "developer", "user", "assistant"} {
		assert.NoError(t, ValidatePromptCache(point("message", schemas.Ptr(role))), "role %q is in the schema enum", role)
	}

	assert.Error(t, ValidatePromptCache(point("messages", nil)), "a near-miss location must not be stored silently")
	assert.Error(t, ValidatePromptCache(point("tools", nil)), "only message targets exist today")
	assert.Error(t, ValidatePromptCache(point("message", schemas.Ptr("sytem"))), "a misspelled role must not be stored silently")
	assert.Error(t, ValidatePromptCache(point("message", schemas.Ptr("tool"))), "tool is not one of the four roles")

	// Deliberately NOT rejected: the schema declares no maxItems, and the injector caps
	// emitted markers at four on its own. Nor is a point with neither role nor index,
	// which matchMessageIndices documents as matching nothing on purpose.
	many := &schemas.PromptCacheConfig{}
	for i := 0; i < 6; i++ {
		many.InjectionPoints = append(many.InjectionPoints, schemas.CacheControlInjectionPoint{Location: "message", Index: schemas.Ptr(i)})
	}
	assert.NoError(t, ValidatePromptCache(many), "the schema sets no maxItems; the injector clamps instead")
	assert.NoError(t, ValidatePromptCache(&schemas.PromptCacheConfig{
		InjectionPoints: []schemas.CacheControlInjectionPoint{{Location: "message"}},
	}), "a point with neither role nor index matches nothing by design, it is not invalid")
}

// The companion to TestValidatePromptCacheMatchesConfigSchemaEnum, for the injection
// point enums. Both doors onto these fields must accept the same values.
func TestValidatePromptCachePointEnumsMatchConfigSchema(t *testing.T) {
	var schema struct {
		Defs struct {
			Point struct {
				Properties struct {
					Location struct {
						Enum []string `json:"enum"`
					} `json:"location"`
					Role struct {
						Enum []string `json:"enum"`
					} `json:"role"`
				} `json:"properties"`
			} `json:"cache_control_injection_point"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(loadLocalSchema(t), &schema))

	locations := schema.Defs.Point.Properties.Location.Enum
	roles := schema.Defs.Point.Properties.Role.Enum
	require.NotEmpty(t, locations, "config.schema.json must declare a location enum")
	require.NotEmpty(t, roles, "config.schema.json must declare a role enum")

	for _, loc := range locations {
		assert.NoError(t, ValidatePromptCache(&schemas.PromptCacheConfig{
			InjectionPoints: []schemas.CacheControlInjectionPoint{{Location: loc, Index: schemas.Ptr(0)}},
		}), "schema declares location %q valid, so the API must accept it", loc)
	}
	for _, role := range roles {
		assert.NoError(t, ValidatePromptCache(&schemas.PromptCacheConfig{
			InjectionPoints: []schemas.CacheControlInjectionPoint{{Location: "message", Role: schemas.Ptr(role)}},
		}), "schema declares role %q valid, so the API must accept it", role)
	}
}
