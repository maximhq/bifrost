package lib

import (
	"context"
	"fmt"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// benchKeyCounts are the provider key counts the request-path key benchmarks run at: a single
// key, a typical pool and a large one.
var benchKeyCounts = []int{1, 10, 50}

// benchProviderKeys returns n enabled keys that serve every model at weight 1, in creation order.
func benchProviderKeys(n int) []schemas.Key {
	keys := make([]schemas.Key, n)
	for i := range keys {
		keys[i] = schemas.Key{
			ID:     fmt.Sprintf("bench-key-%02d", i),
			Name:   fmt.Sprintf("bench key %02d", i),
			Value:  *schemas.NewSecretVar(fmt.Sprintf("sk-bench-%02d", i)),
			Models: schemas.WhiteList{"*"},
			Weight: 1,
		}
	}
	return keys
}

// benchKeyContext returns a request context and the number of keys it admits. A restricted
// context carries a governance key restriction naming every other key (the first, third and so
// on), as a virtual key that allows only some of a provider's keys sets it.
func benchKeyContext(keys []schemas.Key, restricted bool) (*schemas.BifrostContext, int) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if !restricted {
		return ctx, len(keys)
	}
	ids := make([]string, 0, (len(keys)+1)/2)
	for i := 0; i < len(keys); i += 2 {
		ids = append(ids, keys[i].ID)
	}
	ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, ids)
	return ctx, len(ids)
}

// runProviderKeyBenchmarks runs body as keys=N/unrestricted and keys=N/restricted for each key
// count, over a Config whose live store holds one OpenAI provider with N keys, loaded the way
// file-only boot loads providers.
func runProviderKeyBenchmarks(b *testing.B, body func(b *testing.B, c *Config, ctx *schemas.BifrostContext, admitted int)) {
	for _, n := range benchKeyCounts {
		for _, restricted := range []bool{false, true} {
			mode := "unrestricted"
			if restricted {
				mode = "restricted"
			}
			b.Run(fmt.Sprintf("keys=%d/%s", n, mode), func(b *testing.B) {
				keys := benchProviderKeys(n)
				c := withProviders(b, &Config{}, map[schemas.ModelProvider]configstore.ProviderConfig{
					schemas.OpenAI: {},
				}, map[schemas.ModelProvider][]schemas.Key{
					schemas.OpenAI: keys,
				})
				ctx, admitted := benchKeyContext(keys, restricted)
				body(b, c, ctx, admitted)
			})
		}
	}
}

// BenchmarkGetKeysForProvider measures the account's key read that core makes for every request:
// the provider's keys from the live store, filtered to a governance key restriction when the
// context carries one.
func BenchmarkGetKeysForProvider(b *testing.B) {
	runProviderKeyBenchmarks(b, func(b *testing.B, c *Config, ctx *schemas.BifrostContext, admitted int) {
		account := NewBaseAccount(c)
		keys, err := account.GetKeysForProvider(ctx, schemas.OpenAI)
		if err != nil || len(keys) != admitted {
			b.Fatalf("GetKeysForProvider = %d keys, %v; want %d keys", len(keys), err, admitted)
		}
		b.ReportAllocs()
		for b.Loop() {
			_, _ = account.GetKeysForProvider(ctx, schemas.OpenAI)
		}
	})
}

// BenchmarkSelectKeyForProviderRequestType measures core's key pool build over the live store's
// keys: the account read, the enabled, value and model filters, and the weighted random pick,
// through the exported entry point that reaches them. Its baseProviderType lookup adds a
// GetConfigForProvider call whose cost does not depend on the keys.
func BenchmarkSelectKeyForProviderRequestType(b *testing.B) {
	runProviderKeyBenchmarks(b, func(b *testing.B, c *Config, ctx *schemas.BifrostContext, admitted int) {
		client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
			Account: NewBaseAccount(c),
			Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			b.Fatalf("bifrost.Init: %v", err)
		}
		b.Cleanup(client.Shutdown)
		key, err := client.SelectKeyForProviderRequestType(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
		if err != nil || key.ID == "" {
			b.Fatalf("SelectKeyForProviderRequestType = %q, %v; want a key from %d admitted", key.ID, err, admitted)
		}
		b.ReportAllocs()
		for b.Loop() {
			_, _ = client.SelectKeyForProviderRequestType(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
		}
	})
}
