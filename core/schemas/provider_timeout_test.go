package schemas

import (
	"context"
	"strings"
	"testing"
)

func TestProviderTimeoutMessageDoesNotClaimHardcodedDefault(t *testing.T) {
	if strings.Contains(ErrProviderRequestTimedOut, "default is 300 seconds") {
		t.Fatalf("provider timeout message must not claim a hardcoded default: %q", ErrProviderRequestTimedOut)
	}
}

func TestConfiguredRequestTimeoutContextValueIsReservedFromPlugins(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.BlockRestrictedWrites()
	ctx.SetValue(BifrostContextKeyConfiguredRequestTimeoutSeconds, 1)
	if got := ctx.Value(BifrostContextKeyConfiguredRequestTimeoutSeconds); got != nil {
		t.Fatalf("reserved configured timeout was overwritten: %v", got)
	}
}
