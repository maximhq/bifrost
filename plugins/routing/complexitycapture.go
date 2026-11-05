package routing

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// Request capture for phrase suggestions: the plugin owns one
// complexity.RequestCapture, hands it to the semantic classifier, turns it on
// and off with the analyzer config's suggestions block, and keeps what it
// stored bounded - an hourly upkeep sweeps vectors past their lookback (or the
// logs' retention, if that is shorter) and retries vector deletes that failed
// when their logs were deleted.

// captureUpkeepInterval is how often captured vectors are swept.
const captureUpkeepInterval = time.Hour

// captureUpkeepFirstDelay keeps the first sweep off the boot path.
const captureUpkeepFirstDelay = time.Minute

// captureUpkeepTimeout bounds one upkeep pass.
const captureUpkeepTimeout = 5 * time.Minute

// complexityCapture is the plugin's capture state.
type complexityCapture struct {
	capture *complexity.RequestCapture
	// logRetention reports the logs' retention in days; 0 or no function
	// means logs are kept forever, and only the lookback bounds vectors.
	logRetention atomic.Pointer[func() int]
	// lookbackDays is the configured lookback, 0 while suggestions are off.
	lookbackDays atomic.Int64

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// startComplexityCapture wires capture into the classifier and starts its
// upkeep. Capture stays off until a config enables suggestions.
func (p *RoutingPlugin) startComplexityCapture(ctx context.Context, interval, firstDelay time.Duration) {
	if p.semanticClassifier == nil {
		return
	}
	cc := &complexityCapture{capture: complexity.NewRequestCapture(p.logger)}
	p.semanticClassifier.SetRequestCapture(cc.capture)
	upkeepCtx, cancel := context.WithCancel(ctx)
	cc.cancel = cancel
	cc.wg.Go(func() { p.runCaptureUpkeep(upkeepCtx, interval, firstDelay) })
	p.capture = cc
}

// configureComplexityCapture applies the suggestions block of a resolved
// analyzer config.
func (p *RoutingPlugin) configureComplexityCapture(config *complexity.AnalyzerConfig) {
	cc := p.capture
	if cc == nil {
		return
	}
	if !config.SuggestionsEnabled() {
		cc.capture.Configure(nil)
		cc.lookbackDays.Store(0)
		return
	}
	cc.capture.Configure(&complexity.CaptureConfig{SampleRate: config.Suggestions.SampleRate})
	cc.lookbackDays.Store(int64(config.Suggestions.LookbackDays))
}

// SetLogRetention tells capture how long the deployment keeps logs, as a
// function so a retention changed at runtime is read at each sweep.
func (p *RoutingPlugin) SetLogRetention(days func() int) {
	if p.capture == nil {
		return
	}
	if days == nil {
		p.capture.logRetention.Store(nil)
		return
	}
	p.capture.logRetention.Store(&days)
}

// SetComplexityCaptureDeleteRecorder supplies where captured-vector deletes
// that fail with their log's delete are kept, so the upkeep retries them
// until they go. Without one a failed delete is only warned about, and the
// age sweep is what eventually removes the vector.
func (p *RoutingPlugin) SetComplexityCaptureDeleteRecorder(recorder complexity.CaptureDeleteRecorder) {
	if p.capture == nil {
		return
	}
	p.capture.capture.SetDeleteRecorder(recorder)
}

// ForgetComplexityCapturedRequests drops the captured vectors of deleted
// logs. It never fails the log delete; what it cannot remove is recorded for
// the upkeep to retry.
func (p *RoutingPlugin) ForgetComplexityCapturedRequests(ctx context.Context, ids []string) {
	if p.capture == nil || p.semanticClassifier == nil || len(ids) == 0 {
		return
	}
	p.semanticClassifier.ForgetCapturedRequests(ctx, ids)
}

// ComplexityCaptureStats reports what request capture has done since start.
func (p *RoutingPlugin) ComplexityCaptureStats() complexity.CaptureStats {
	if p.capture == nil {
		return complexity.CaptureStats{}
	}
	return p.capture.capture.Stats()
}

func (p *RoutingPlugin) runCaptureUpkeep(ctx context.Context, interval, firstDelay time.Duration) {
	timer := time.NewTimer(firstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.captureUpkeepOnce(ctx, time.Now())
			timer.Reset(interval)
		}
	}
}

// captureUpkeepOnce sweeps captured vectors past their keep window and
// retries failed deletes.
func (p *RoutingPlugin) captureUpkeepOnce(ctx context.Context, now time.Time) {
	cc := p.capture
	if cc == nil || p.semanticClassifier == nil {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, captureUpkeepTimeout)
	defer cancel()
	if keep := cc.keepDays(); keep > 0 {
		cutoff := now.Add(-time.Duration(keep) * 24 * time.Hour)
		if n, err := p.semanticClassifier.SweepCapturedRequests(passCtx, cutoff); err != nil {
			p.logger.Warn("[Routing] sweeping captured request vectors failed: %v", err)
		} else if n > 0 {
			p.logger.Debug("[Routing] swept %d captured request vectors older than %d days", n, keep)
		}
	}
	if n, err := p.semanticClassifier.RetryCapturedDeletes(passCtx); err != nil {
		p.logger.Warn("[Routing] retrying captured vector deletes failed after %d: %v", n, err)
	}
}

// keepDays is how long captured vectors are kept: the lookback, but never
// longer than the logs they belong to. With suggestions off the last
// lookback still applies to what was captured before, so nothing is kept
// forever; DefaultComplexitySuggestionsLookbackDays covers a restart.
func (cc *complexityCapture) keepDays() int {
	keep := int(cc.lookbackDays.Load())
	if keep <= 0 {
		keep = complexity.DefaultSuggestionsLookbackDays
	}
	if f := cc.logRetention.Load(); f != nil {
		if days := (*f)(); days > 0 && days < keep {
			keep = days
		}
	}
	return keep
}

// closeComplexityCapture stops the upkeep and drains the writer.
func (p *RoutingPlugin) closeComplexityCapture() {
	cc := p.capture
	if cc == nil {
		return
	}
	cc.cancel()
	cc.wg.Wait()
	cc.capture.Close()
}
