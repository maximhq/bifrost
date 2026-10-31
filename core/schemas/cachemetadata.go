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

// CacheLookupFromContext returns the outcome of the semantic cache lookup for this request: hit
// or miss, the entry's id, and on a semantic search its similarity and embedding usage. It is known
// before the response starts, so a stream can report it in its headers, which are sent before any
// chunk carries cache metadata. False when no lookup ran.
func CacheLookupFromContext(ctx *BifrostContext) (*BifrostCacheMetadata, bool) {
	if ctx == nil {
		return nil, false
	}
	metadata, ok := ctx.Value(BifrostContextKeyCacheLookup).(*BifrostCacheMetadata)
	if !ok || metadata == nil {
		return nil, false
	}
	return metadata.Clone(), true
}

// SetCacheLookupOnContext records the outcome of the semantic cache lookup on ctx, replacing any
// earlier attempt's. Reports false when there is nothing to record.
func SetCacheLookupOnContext(ctx *BifrostContext, metadata *BifrostCacheMetadata) bool {
	if ctx == nil || metadata == nil {
		return false
	}
	ctx.SetValue(BifrostContextKeyCacheLookup, metadata.Clone())
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
