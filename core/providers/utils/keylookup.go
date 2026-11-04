package utils

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// IsNotFoundError reports whether the provider answered 404.
func IsNotFoundError(err *schemas.BifrostError) bool {
	return err != nil && err.StatusCode != nil && *err.StatusCode == 404
}

// ShouldTryNextKeyForLookup reports whether a call addressing an object by id should move to the next key:
// the object lives on one key's account, so a 404 or a refusal of this key says nothing about the others.
func ShouldTryNextKeyForLookup(err *schemas.BifrostError) bool {
	if err == nil || err.StatusCode == nil {
		return false
	}
	switch *err.StatusCode {
	case 401, 403, 404, 429:
		return true
	}
	return false
}

// LookupAcrossKeys calls fn with each key in order until one serves the object, returning that key's index.
// A 404 is returned only when every key answered 404; otherwise the last other error is, since the object may sit behind that key.
// An error that should not move on (a 5xx, a 400) is returned at once, so a mutating call is never replayed after an ambiguous failure.
func LookupAcrossKeys[T any](ctx *schemas.BifrostContext, keys []schemas.Key, fn func(schemas.Key) (T, *schemas.BifrostError)) (T, int, *schemas.BifrostError) {
	var zero T
	var notFoundErr, otherErr *schemas.BifrostError
	for i, key := range keys {
		result, err := fn(key)
		if err == nil {
			return result, i, nil
		}
		if !ShouldTryNextKeyForLookup(err) {
			return zero, i, err
		}
		if IsNotFoundError(err) {
			notFoundErr = err
		} else {
			otherErr = err
		}
		if ctx != nil && ctx.Err() != nil {
			break
		}
	}
	if otherErr != nil {
		return zero, -1, otherErr
	}
	return zero, -1, notFoundErr
}
