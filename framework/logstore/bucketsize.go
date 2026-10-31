package logstore

import "time"

// DefaultBucketSize picks a histogram bucket width for a time range, in seconds.
//
// The goal is a readable number of bars rather than a fixed resolution: a
// one-hour range wants minutes and a one-year range wants months, and using
// either width for the other produces a chart nobody can read. The thresholds
// are chosen so any range lands somewhere between roughly 12 and 60 buckets.
//
// A missing bound falls back to hourly rather than erroring. Callers reach here
// from query strings where an absent parameter is normal.
func DefaultBucketSize(start, end *time.Time) int64 {
	if start == nil || end == nil {
		return 3600 // Default 1 hour
	}
	duration := end.Sub(*start)
	switch {
	case duration >= 365*24*time.Hour: // >= 12 months
		return 30 * 24 * 3600 // Monthly (30 days)
	case duration >= 90*24*time.Hour: // >= 3 months
		return 7 * 24 * 3600 // Weekly (7 days)
	case duration > 31*24*time.Hour: // > ~1 month
		return 3 * 24 * 3600 // 3 days
	case duration >= 7*24*time.Hour: // >= 7 days, up to ~1 month
		return 24 * 3600 // Daily (one bar per day)
	case duration >= 3*24*time.Hour: // >= 3 days
		return 8 * 3600 // 8 hours
	case duration >= 24*time.Hour: // >= 24 hours
		return 3600 // Hourly
	case duration >= 2*time.Hour: // >= 2 hours
		return 600 // 10 minutes
	default:
		return 60 // 1 minute buckets for < 2 hours
	}
}
