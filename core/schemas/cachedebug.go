package schemas

// Clone returns an owned snapshot of cache debug data.
func (d *BifrostCacheDebug) Clone() *BifrostCacheDebug {
	if d == nil {
		return nil
	}
	clone := *d
	clone.CacheID = cloneString(d.CacheID)
	clone.HitType = cloneString(d.HitType)
	clone.RequestedProvider = cloneString(d.RequestedProvider)
	clone.RequestedModel = cloneString(d.RequestedModel)
	clone.ProviderUsed = cloneString(d.ProviderUsed)
	clone.ModelUsed = cloneString(d.ModelUsed)
	clone.InputTokens = cloneInt(d.InputTokens)
	clone.Threshold = cloneFloat64(d.Threshold)
	clone.Similarity = cloneFloat64(d.Similarity)
	clone.CacheHitLatency = cloneInt64(d.CacheHitLatency)
	return &clone
}

// CacheDebugFromContext returns typed cache debug data stored on ctx.
func CacheDebugFromContext(ctx *BifrostContext) (*BifrostCacheDebug, bool) {
	if ctx == nil {
		return nil, false
	}
	debug, ok := ctx.Value(BifrostContextKeyCacheDebug).(*BifrostCacheDebug)
	if !ok || debug == nil || debug.ProviderUsed == nil || debug.ModelUsed == nil || debug.InputTokens == nil {
		return nil, false
	}
	return debug.Clone(), true
}

// SetCacheDebugOnContext stores semantic cache debug data on ctx.
func SetCacheDebugOnContext(ctx *BifrostContext, debug *BifrostCacheDebug) bool {
	if ctx == nil || debug == nil || debug.ProviderUsed == nil || debug.ModelUsed == nil || debug.InputTokens == nil {
		return false
	}
	ctx.SetValue(BifrostContextKeyCacheDebug, debug.Clone())
	return true
}

// cloneString returns an owned copy of value.
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// cloneInt returns an owned copy of value.
func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// cloneFloat64 returns an owned copy of value.
func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// cloneInt64 returns an owned copy of value.
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
