package schemas

// Clone returns an owned snapshot of cache metadata.
func (d *BifrostCacheMetadata) Clone() *BifrostCacheMetadata {
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

// CacheMetadataFromContext returns typed cache metadata stored on ctx.
func CacheMetadataFromContext(ctx *BifrostContext) (*BifrostCacheMetadata, bool) {
	if ctx == nil {
		return nil, false
	}
	metadata, ok := ctx.Value(BifrostContextKeyCacheMetadata).(*BifrostCacheMetadata)
	if !ok || metadata == nil || metadata.ProviderUsed == nil || metadata.ModelUsed == nil || metadata.InputTokens == nil {
		return nil, false
	}
	return metadata.Clone(), true
}

// SetCacheMetadataOnContext stores semantic cache metadata on ctx.
func SetCacheMetadataOnContext(ctx *BifrostContext, metadata *BifrostCacheMetadata) bool {
	if ctx == nil || metadata == nil || metadata.ProviderUsed == nil || metadata.ModelUsed == nil || metadata.InputTokens == nil {
		return false
	}
	ctx.SetValue(BifrostContextKeyCacheMetadata, metadata.Clone())
	return true
}

// Deprecated: use CacheMetadataFromContext.
func CacheDebugFromContext(ctx *BifrostContext) (*BifrostCacheMetadata, bool) {
	return CacheMetadataFromContext(ctx)
}

// Deprecated: use SetCacheMetadataOnContext.
func SetCacheDebugOnContext(ctx *BifrostContext, metadata *BifrostCacheMetadata) bool {
	return SetCacheMetadataOnContext(ctx, metadata)
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
