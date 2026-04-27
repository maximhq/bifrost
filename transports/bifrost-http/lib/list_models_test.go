package lib

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

type virtualKeyByValueStore struct {
	*MockConfigStore
	virtualKey *tables.TableVirtualKey
	gotValue   string
	err        error
}

func (m *virtualKeyByValueStore) GetVirtualKeyByValue(ctx context.Context, value string) (*tables.TableVirtualKey, error) {
	m.gotValue = value
	return m.virtualKey, m.err
}

func TestApplyVirtualKeyProviderFilter_SetsAvailableProviders(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")

	store := &virtualKeyByValueStore{
		MockConfigStore: NewMockConfigStore(),
		virtualKey: &tables.TableVirtualKey{
			IsActive: true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{Provider: "azure"},
				{Provider: "azure"},
				{Provider: "packyapi"},
				{Provider: ""},
			},
		},
	}

	if err := ApplyVirtualKeyProviderFilter(ctx, store); err != nil {
		t.Fatalf("ApplyVirtualKeyProviderFilter returned error: %v", err)
	}

	if store.gotValue != "sk-bf-test" {
		t.Fatalf("expected store lookup to receive sk-bf-test, got %q", store.gotValue)
	}

	got, ok := ctx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	if !ok {
		t.Fatal("expected available providers to be set on context")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 unique providers, got %d (%v)", len(got), got)
	}
	if got[0] != schemas.ModelProvider("azure") || got[1] != schemas.ModelProvider("packyapi") {
		t.Fatalf("unexpected providers order/content: %v", got)
	}
}

func TestApplyVirtualKeyProviderFilter_NoVirtualKeyNoop(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	store := &virtualKeyByValueStore{MockConfigStore: NewMockConfigStore()}

	if err := ApplyVirtualKeyProviderFilter(ctx, store); err != nil {
		t.Fatalf("ApplyVirtualKeyProviderFilter returned error: %v", err)
	}
	if got := ctx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected no available providers on context, got %v", got)
	}
}

func TestApplyVirtualKeyProviderFilter_DoesNotOverwriteExistingProviders(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	ctx.SetValue(schemas.BifrostContextKeyAvailableProviders, []schemas.ModelProvider{"governance"})

	store := &virtualKeyByValueStore{
		MockConfigStore: NewMockConfigStore(),
		virtualKey: &tables.TableVirtualKey{
			IsActive: true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{Provider: "azure"},
			},
		},
	}

	if err := ApplyVirtualKeyProviderFilter(ctx, store); err != nil {
		t.Fatalf("ApplyVirtualKeyProviderFilter returned error: %v", err)
	}

	got, ok := ctx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	if !ok || len(got) != 1 || got[0] != "governance" {
		t.Fatalf("expected existing providers to be preserved, got %v", got)
	}
	if store.gotValue != "" {
		t.Fatalf("expected store lookup to be skipped when providers already exist, got %q", store.gotValue)
	}
}

func TestApplyVirtualKeyProviderFilter_InactiveVirtualKeyNoop(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-inactive")

	store := &virtualKeyByValueStore{
		MockConfigStore: NewMockConfigStore(),
		virtualKey: &tables.TableVirtualKey{
			IsActive: false,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{Provider: "azure"},
			},
		},
	}

	if err := ApplyVirtualKeyProviderFilter(ctx, store); err != nil {
		t.Fatalf("ApplyVirtualKeyProviderFilter returned error: %v", err)
	}
	if got := ctx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected inactive virtual key to leave providers unset, got %v", got)
	}
}

func TestApplyVirtualKeyProviderFilter_NilVirtualKeyNoop(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-missing")

	store := &virtualKeyByValueStore{
		MockConfigStore: NewMockConfigStore(),
		virtualKey:      nil,
	}

	if err := ApplyVirtualKeyProviderFilter(ctx, store); err != nil {
		t.Fatalf("ApplyVirtualKeyProviderFilter returned error: %v", err)
	}
	if got := ctx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected missing virtual key to leave providers unset, got %v", got)
	}
}

func TestApplyVirtualKeyProviderFilter_EmptyProviderConfigsNoop(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-empty")

	store := &virtualKeyByValueStore{
		MockConfigStore: NewMockConfigStore(),
		virtualKey: &tables.TableVirtualKey{
			IsActive:        true,
			ProviderConfigs: nil,
		},
	}

	if err := ApplyVirtualKeyProviderFilter(ctx, store); err != nil {
		t.Fatalf("ApplyVirtualKeyProviderFilter returned error: %v", err)
	}
	if got := ctx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected empty provider configs to leave providers unset, got %v", got)
	}
}

func TestApplyVirtualKeyProviderFilter_StoreError(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-error")

	wantErr := errors.New("store unavailable")
	store := &virtualKeyByValueStore{
		MockConfigStore: NewMockConfigStore(),
		err:             wantErr,
	}

	if err := ApplyVirtualKeyProviderFilter(ctx, store); !errors.Is(err, wantErr) {
		t.Fatalf("expected store error %v, got %v", wantErr, err)
	}
	if got := ctx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected store error to leave providers unset, got %v", got)
	}
}
