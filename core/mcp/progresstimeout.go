package mcp

// SBS fork: MetaMCP's "reset timeout on progress" and "max total timeout" for upstream tool calls.
//
// With SBS_MCP_RESET_TIMEOUT_ON_PROGRESS=true, a tool call asks the upstream server for progress
// notifications (a progress token in _meta), and its execution timeout becomes an idle timeout:
// every progress notification for the call restarts it. SBS_MCP_MAX_TOTAL_TIMEOUT (seconds,
// default 1800) caps the whole call however much progress it reports. Off by default, which
// keeps upstream behaviour: one fixed timeout per call.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// DefaultMaxTotalToolTimeout caps a call whose timeout progress keeps resetting.
const DefaultMaxTotalToolTimeout = 30 * time.Minute

var (
	progressSettingsOnce sync.Once
	progressResetEnabled bool
	progressMaxTotal     time.Duration

	// progressWaiters maps a progress token to the reset function of the call that sent it.
	progressWaiters sync.Map // string -> func()
)

func init() {
	client.SetGlobalNotificationHook(routeProgressNotification)
}

// progressSettings reads the environment once.
func progressSettings() (enabled bool, maxTotal time.Duration) {
	progressSettingsOnce.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("SBS_MCP_RESET_TIMEOUT_ON_PROGRESS"))) {
		case "1", "true", "yes", "on":
			progressResetEnabled = true
		}
		progressMaxTotal = DefaultMaxTotalToolTimeout
		if v := strings.TrimSpace(os.Getenv("SBS_MCP_MAX_TOTAL_TIMEOUT")); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				progressMaxTotal = time.Duration(secs) * time.Second
			}
		}
	})
	return progressResetEnabled, progressMaxTotal
}

// routeProgressNotification restarts the idle timer of the call a progress notification belongs to.
func routeProgressNotification(n mcp.JSONRPCNotification) {
	if n.Method != string(mcp.MethodNotificationProgress) {
		return
	}
	token, ok := n.Params.AdditionalFields["progressToken"]
	if !ok {
		return
	}
	if reset, ok := progressWaiters.Load(fmt.Sprint(token)); ok {
		reset.(func())()
	}
}

// idleTimeoutCtx is a context that ends when its idle timer fires (reported as DeadlineExceeded,
// like a plain timeout, so callers classify it the same), when the total cap passes, or with its parent.
type idleTimeoutCtx struct {
	context.Context
	cause context.CancelCauseFunc
}

func (c *idleTimeoutCtx) Err() error {
	if err := c.Context.Err(); err != nil {
		if cause := context.Cause(c.Context); cause == context.DeadlineExceeded {
			return context.DeadlineExceeded
		}
		return err
	}
	return nil
}

// withToolTimeout returns the context a tool call runs under, the request carrying a progress
// token when reset-on-progress is on, and a cancel func that must always be called.
func withToolTimeout(parent context.Context, idle time.Duration, req mcp.CallToolRequest) (context.Context, mcp.CallToolRequest, context.CancelFunc) {
	enabled, maxTotal := progressSettings()
	if !enabled || idle <= 0 {
		ctx, cancel := context.WithTimeout(parent, idle)
		return ctx, req, cancel
	}
	if maxTotal < idle {
		maxTotal = idle
	}
	capped, capCancel := context.WithTimeout(parent, maxTotal)
	inner, cause := context.WithCancelCause(capped)
	timer := time.AfterFunc(idle, func() { cause(context.DeadlineExceeded) })

	token := "sbs-" + uuid.NewString()
	progressWaiters.Store(token, func() {
		if inner.Err() == nil {
			timer.Reset(idle)
		}
	})

	meta := &mcp.Meta{}
	if req.Params.Meta != nil {
		copied := *req.Params.Meta
		meta = &copied
	}
	if meta.ProgressToken == nil { // keep a token the caller asked for
		meta.ProgressToken = token
	} else {
		progressWaiters.Store(fmt.Sprint(meta.ProgressToken), func() {
			if inner.Err() == nil {
				timer.Reset(idle)
			}
		})
		progressWaiters.Delete(token)
		token = fmt.Sprint(meta.ProgressToken)
	}
	req.Params.Meta = meta

	ctx := &idleTimeoutCtx{Context: inner, cause: cause}
	return ctx, req, func() {
		timer.Stop()
		progressWaiters.Delete(token)
		cause(context.Canceled)
		capCancel()
	}
}
