package schemas

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestNewBifrostContext_NoGoroutineLeakWithBackgroundAndNoDeadline(t *testing.T) {
	// Get baseline goroutine count
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Create multiple contexts with context.Background() and no deadline
	// Previously this would leak goroutines
	contexts := make([]*BifrostContext, 100)
	for i := 0; i < 100; i++ {
		contexts[i] = NewBifrostContext(context.Background(), NoDeadline)
	}

	// Give time for any goroutines to start
	runtime.Gosched()
	time.Sleep(10 * time.Millisecond)

	// Check goroutine count - should not have increased significantly
	// (allow some slack for runtime/test goroutines)
	afterCreate := runtime.NumGoroutine()

	// With the fix, no goroutines should be spawned since there's nothing to watch
	// Allow a small margin for test framework goroutines
	if afterCreate > baseline+10 {
		t.Errorf("Goroutine leak detected: baseline=%d, after creating 100 contexts=%d", baseline, afterCreate)
	}

	// Verify the contexts still work correctly
	for i, ctx := range contexts {
		// Should not be cancelled
		select {
		case <-ctx.Done():
			t.Errorf("Context %d should not be done", i)
		default:
			// Expected
		}

		// Should return nil error
		if ctx.Err() != nil {
			t.Errorf("Context %d Err() should be nil, got %v", i, ctx.Err())
		}

		// Should have no deadline
		if _, ok := ctx.Deadline(); ok {
			t.Errorf("Context %d should not have deadline", i)
		}
	}

	// Explicitly cancel all contexts
	for _, ctx := range contexts {
		ctx.Cancel()
	}

	// Verify all are cancelled
	for i, ctx := range contexts {
		select {
		case <-ctx.Done():
			// Expected
		default:
			t.Errorf("Context %d should be done after Cancel()", i)
		}

		if ctx.Err() != context.Canceled {
			t.Errorf("Context %d Err() should be context.Canceled, got %v", i, ctx.Err())
		}
	}
}

func TestNewBifrostContext_GoroutineStartsWithDeadline(t *testing.T) {
	// Get baseline goroutine count
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Create context with a deadline - should spawn goroutine
	deadline := time.Now().Add(1 * time.Hour)
	ctx := NewBifrostContext(context.Background(), deadline)

	// Give time for goroutine to start
	runtime.Gosched()
	time.Sleep(10 * time.Millisecond)

	afterCreate := runtime.NumGoroutine()

	// Should have at least one more goroutine for the deadline watcher
	if afterCreate <= baseline {
		t.Errorf("Expected goroutine to be spawned for deadline context: baseline=%d, after=%d", baseline, afterCreate)
	}

	// Clean up
	ctx.Cancel()
}

func TestNewBifrostContext_GoroutineStartsWithCancellableParent(t *testing.T) {
	// Get baseline goroutine count
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Create a cancellable parent
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	// Create BifrostContext with cancellable parent but no deadline
	// Should spawn goroutine to watch parent
	ctx := NewBifrostContext(parent, NoDeadline)

	// Give time for goroutine to start
	runtime.Gosched()
	time.Sleep(10 * time.Millisecond)

	afterCreate := runtime.NumGoroutine()

	// Should have goroutine for watching parent cancellation
	if afterCreate <= baseline {
		t.Errorf("Expected goroutine to be spawned for cancellable parent: baseline=%d, after=%d", baseline, afterCreate)
	}

	// Verify parent cancellation propagates
	parentCancel()
	time.Sleep(10 * time.Millisecond)

	select {
	case <-ctx.Done():
		// Expected
	default:
		t.Error("Context should be cancelled when parent is cancelled")
	}

	if ctx.Err() != context.Canceled {
		t.Errorf("Context Err() should be context.Canceled, got %v", ctx.Err())
	}
}

func TestNewBifrostContext_DeadlineExpires(t *testing.T) {
	// Create context with short deadline
	deadline := time.Now().Add(50 * time.Millisecond)
	ctx := NewBifrostContext(context.Background(), deadline)

	// Should not be done yet
	select {
	case <-ctx.Done():
		t.Error("Context should not be done before deadline")
	default:
		// Expected
	}

	// Wait for deadline
	time.Sleep(100 * time.Millisecond)

	// Should be done now
	select {
	case <-ctx.Done():
		// Expected
	default:
		t.Error("Context should be done after deadline")
	}

	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("Context Err() should be context.DeadlineExceeded, got %v", ctx.Err())
	}
}

func TestNewBifrostContext_SetAndGetValue(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)

	// Set a value
	ctx.SetValue("key1", "value1")

	// Get the value
	if v := ctx.Value("key1"); v != "value1" {
		t.Errorf("Expected value1, got %v", v)
	}

	// Get non-existent key
	if v := ctx.Value("nonexistent"); v != nil {
		t.Errorf("Expected nil for non-existent key, got %v", v)
	}

	// Clean up
	ctx.Cancel()
}

func TestNewBifrostContext_NilParent(t *testing.T) {
	// Should not panic with nil parent
	// Note: passing nil is allowed by NewBifrostContext which converts it to context.Background()
	var nilCtx context.Context //nolint:staticcheck // testing nil parent handling
	ctx := NewBifrostContext(nilCtx, NoDeadline)

	// Should work normally
	if ctx.Err() != nil {
		t.Errorf("New context should have nil error, got %v", ctx.Err())
	}

	ctx.Cancel()

	if ctx.Err() != context.Canceled {
		t.Errorf("Cancelled context should have Canceled error, got %v", ctx.Err())
	}
}
