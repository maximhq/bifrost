package bifrost

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// sharedKeysAccount is a MockAccount whose GetKeysForProvider returns one shared slice without
// copying or counting, as an account serving keys from memory does, so a benchmark over it
// measures only core's own work on the keys.
type sharedKeysAccount struct {
	*MockAccount
	keys []schemas.Key
}

// GetKeysForProvider returns the shared key slice for every provider.
func (a *sharedKeysAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return a.keys, nil
}

// BenchmarkKeyPoolRotatingPick measures the request worker's key work before a request's first
// attempt: selectKeyFromProviderForModelWithPool builds the eligible pool, and a rotating pool
// goes through pickFromRotatingPool and the default weighted random selector, as the worker's
// keyProvider closure does. A single key is a fixed pool, served without a pick. The account
// returns a shared slice, so these numbers are core's alone.
func BenchmarkKeyPoolRotatingPick(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("keys=%d", n), func(b *testing.B) {
			mock := NewMockAccount()
			mock.AddProvider(schemas.OpenAI, 5, 1000)
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
			client, err := Init(context.Background(), schemas.BifrostConfig{
				Account: &sharedKeysAccount{MockAccount: mock, keys: keys},
				Logger:  NewDefaultLogger(schemas.LogLevelError),
			})
			if err != nil {
				b.Fatalf("Init: %v", err)
			}
			b.Cleanup(client.Shutdown)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			usedKeyIDs, deadKeyIDs := map[string]bool{}, map[string]bool{}
			pick := func() (schemas.Key, error) {
				pool, canRotate, err := client.selectKeyFromProviderForModelWithPool(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", schemas.OpenAI)
				if err != nil {
					return schemas.Key{}, err
				}
				if !canRotate {
					return pool[0], nil
				}
				return client.pickFromRotatingPool(ctx, pool, schemas.OpenAI, "gpt-4o", usedKeyIDs, deadKeyIDs)
			}
			if key, err := pick(); err != nil || key.ID == "" {
				b.Fatalf("pick = %q, %v; want a key", key.ID, err)
			}
			b.ReportAllocs()
			for b.Loop() {
				_, _ = pick()
			}
		})
	}
}
