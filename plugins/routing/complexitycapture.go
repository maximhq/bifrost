package routing

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// Request capture for phrase suggestions. Each model router captures its own
// classified requests: its semantic classifier gets a complexity.RequestCapture
// of its own, sampling at the router's suggestions rate into namespaces keyed
// by the router (complexity.CaptureNamespace), so a router's suggestion runs
// read only the requests it classified. The Complexity Router page's config
// captures nothing.
//
// The plugin keeps what was stored bounded with an hourly upkeep: each router's
// vectors are swept past its lookback (or the logs' retention, if that is
// shorter), whether this node serves the router or only the config store has
// it, vectors of deleted routers age out on the default lookback, namespaces
// from before captures were per router are dropped, and
// vector deletes that failed when their logs were deleted are retried.

// captureUpkeepInterval is how often captured vectors are swept.
const captureUpkeepInterval = time.Hour

// captureUpkeepFirstDelay keeps the first sweep off the boot path.
const captureUpkeepFirstDelay = time.Minute

// captureUpkeepTimeout bounds one upkeep pass.
const captureUpkeepTimeout = 5 * time.Minute

// captureDropTimeout bounds dropping a deleted router's captures.
const captureDropTimeout = time.Minute

// complexityCapture is the plugin's capture state: what every router's
// capture shares. The cleaner deletes and sweeps across every router's capture
// namespaces and is never offered a request; the recorder, which keeps failed
// deletes for the upkeep to retry, is given to every router's capture; and the
// logs' retention bounds how long any captured vector is kept (0, or no
// function, means logs are kept forever and only the lookback bounds vectors).
type complexityCapture struct {
	cleaner      *complexity.RequestCapture                       // sweeps and deletes; never captures
	recorder     atomic.Pointer[complexity.CaptureDeleteRecorder] // failed deletes, retried by the upkeep
	logRetention atomic.Pointer[func() int]                       // the logs' retention in days

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// startComplexityCapture turns on per-router capture and starts its upkeep.
// A router samples nothing until its config enables suggestions.
func (p *RoutingPlugin) startComplexityCapture(ctx context.Context, interval, firstDelay time.Duration) {
	cc := &complexityCapture{cleaner: complexity.NewRequestCapture(p.logger)}
	upkeepCtx, cancel := context.WithCancel(ctx)
	cc.cancel = cancel
	cc.wg.Go(func() { p.runCaptureUpkeep(upkeepCtx, interval, firstDelay) })
	p.capture = cc
}

// newRouterCapture gives a router's semantic classifier a capture of its own,
// keyed by the router. Nil when capture was not started.
func (p *RoutingPlugin) newRouterCapture(id string, semantic *complexity.SemanticClassifier) *complexity.RequestCapture {
	cc := p.capture
	if cc == nil || semantic == nil {
		return nil
	}
	capture := complexity.NewRequestCapture(p.logger)
	if recorder := cc.recorder.Load(); recorder != nil {
		capture.SetDeleteRecorder(*recorder)
	}
	semantic.SetCaptureRouter(id)
	semantic.SetRequestCapture(capture)
	return capture
}

// configureRouterCapture applies the suggestions block of a router's resolved
// config to its capture: it samples at the suggestions rate while they are on
// and the router classifies semantically, and nothing otherwise.
func configureRouterCapture(rt *modelRouterRuntime, resolved *complexity.AnalyzerConfig) {
	if rt.capture == nil {
		return
	}
	if resolved != nil && resolved.Classifier == complexity.ClassifierSemantic && resolved.SuggestionsEnabled() {
		rt.capture.Configure(&complexity.CaptureConfig{SampleRate: resolved.Suggestions.SampleRate})
		return
	}
	rt.capture.Configure(nil)
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
	cc := p.capture
	if cc == nil {
		return
	}
	cc.recorder.Store(&recorder)
	cc.cleaner.SetDeleteRecorder(recorder)
	p.forEachModelRouter(func(rt *modelRouterRuntime) {
		if rt.capture != nil {
			rt.capture.SetDeleteRecorder(recorder)
		}
	})
}

// captureStore is where captured requests are kept: Bifrost's configured
// vector store, or nil when there is none or it is Chromem.
func (p *RoutingPlugin) captureStore() vectorstore.VectorStore {
	ptr := p.complexityVectorStore.Load()
	if ptr == nil {
		return nil
	}
	return complexity.CaptureStore(*ptr)
}

// ForgetComplexityCapturedRequests drops the captured vectors of deleted
// logs, from every router's namespaces. It never fails the log delete; what
// it cannot remove is recorded for the upkeep to retry.
func (p *RoutingPlugin) ForgetComplexityCapturedRequests(ctx context.Context, ids []string) {
	cc := p.capture
	if cc == nil || len(ids) == 0 {
		return
	}
	// A sample of a deleted log may still be queued in any router's writer.
	p.forEachModelRouter(func(rt *modelRouterRuntime) { rt.capture.MarkForgotten(ids) })
	cc.cleaner.Forget(ctx, p.captureStore(), "", ids)
}

// ComplexityCaptureStats reports what request capture has done since start,
// summed over the routers served now.
func (p *RoutingPlugin) ComplexityCaptureStats() complexity.CaptureStats {
	var total complexity.CaptureStats
	p.forEachModelRouter(func(rt *modelRouterRuntime) {
		if rt.capture == nil {
			return
		}
		s := rt.capture.Stats()
		total.Enabled = total.Enabled || s.Enabled
		total.Queued += s.Queued
		total.Dropped += s.Dropped
		total.Written += s.Written
		total.Failed += s.Failed
		total.Forgotten += s.Forgotten
	})
	return total
}

// dropRouterCaptures deletes a deleted router's captured vectors, off the
// caller's path.
func (p *RoutingPlugin) dropRouterCaptures(id string) {
	cc := p.capture
	store := p.captureStore()
	if cc == nil || store == nil {
		return
	}
	cc.wg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), captureDropTimeout)
		defer cancel()
		if err := cc.cleaner.DropRouter(ctx, store, id); err != nil {
			p.logger.Warn("[Routing] failed to drop the captured requests of deleted model router %s; they age out with the default lookback: %v", id, err)
		}
	})
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

// captureUpkeepOnce sweeps every capture namespace past its keep window and
// retries failed deletes. A served router's namespaces keep its lookback. A
// router this node does not serve may still be served by another node sharing
// the store (or be saved but left out here, as session routing without a KV
// store is), so its namespaces keep the lookback the config store has for it;
// only a router the store no longer holds gets the default, and when the store
// cannot be read, those namespaces are left for the next pass rather than swept
// on a guess. A namespace from before captures were per router is dropped.
func (p *RoutingPlugin) captureUpkeepOnce(ctx context.Context, now time.Time) {
	cc := p.capture
	store := p.captureStore()
	if cc == nil || store == nil {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, captureUpkeepTimeout)
	defer cancel()
	namespaces, err := complexity.ListCaptureNamespaces(passCtx, store)
	if err != nil {
		p.logger.Warn("[Routing] listing captured request namespaces failed: %v", err)
	}
	keepByRouter := map[string]int{}
	p.forEachModelRouter(func(rt *modelRouterRuntime) {
		keepByRouter[complexity.CaptureRouterKey(rt.id)] = cc.keepDays(routerLookbackDays(rt))
	})
	// Read once, and only when a namespace this node does not serve turns up.
	var stored map[string]int
	storedRead, storedOK := false, false
	byKeep := map[int][]string{}
	for _, ns := range namespaces {
		key, ok := complexity.CaptureNamespaceRouterKey(ns)
		if !ok {
			if err := store.DeleteNamespace(passCtx, ns); err != nil {
				p.logger.Warn("[Routing] dropping captured request namespace %s from before captures were per router failed: %v", ns, err)
			}
			continue
		}
		keep, served := keepByRouter[key]
		if !served {
			if !storedRead {
				stored, storedOK = p.storedRouterKeepDays(passCtx, cc)
				storedRead = true
			}
			if !storedOK {
				continue
			}
			if keep, served = stored[key]; !served {
				keep = cc.keepDays(0) // no router holds it: deleted
			}
		}
		byKeep[keep] = append(byKeep[keep], ns)
	}
	for keep, group := range byKeep {
		cutoff := now.Add(-time.Duration(keep) * 24 * time.Hour)
		if n, err := complexity.SweepCaptureNamespaces(passCtx, store, group, cutoff); err != nil {
			p.logger.Warn("[Routing] sweeping captured request vectors failed: %v", err)
		} else if n > 0 {
			p.logger.Debug("[Routing] swept %d captured request vectors older than %d days", n, keep)
		}
	}
	if n, err := cc.cleaner.RetryDeletes(passCtx, store, ""); err != nil {
		p.logger.Warn("[Routing] retrying captured vector deletes failed after %d: %v", n, err)
	}
}

// storedRouterKeepDays is the keep window of every router the config store
// holds, by capture router key, and false when the store could not be read.
// Without a config store there is no one else's router to keep: every
// unserved namespace takes the default.
func (p *RoutingPlugin) storedRouterKeepDays(ctx context.Context, cc *complexityCapture) (map[string]int, bool) {
	keep := map[string]int{}
	if p.configStore == nil {
		return keep, true
	}
	routers, err := p.configStore.GetModelRouters(ctx)
	if err != nil {
		p.logger.Warn("[Routing] reading model routers for captured request upkeep failed; routers not served here are left unswept this pass: %v", err)
		return nil, false
	}
	for _, router := range routers {
		lookback := 0
		if router.Suggestions != nil {
			lookback = router.Suggestions.LookbackDays
		}
		keep[complexity.CaptureRouterKey(router.ID)] = cc.keepDays(lookback)
	}
	return keep, true
}

// routerLookbackDays is a router's configured lookback, kept while its
// suggestions are off so what it captured ages out on the lookback the
// operator set; 0 when it has none.
func routerLookbackDays(rt *modelRouterRuntime) int {
	if rt == nil || rt.config == nil {
		return 0
	}
	config := rt.config.Load()
	if config == nil || config.Suggestions == nil {
		return 0
	}
	return config.Suggestions.LookbackDays
}

// keepDays is how long captured vectors are kept: the lookback (the default
// when unset), but never longer than the logs they belong to.
func (cc *complexityCapture) keepDays(lookbackDays int) int {
	keep := lookbackDays
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

// closeComplexityCapture stops the upkeep and drains the cleaner. Each
// router's capture is closed with its router.
func (p *RoutingPlugin) closeComplexityCapture() {
	cc := p.capture
	if cc == nil {
		return
	}
	cc.cancel()
	cc.wg.Wait()
	cc.cleaner.Close()
}
