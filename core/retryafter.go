package bifrost

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryAfterTime returns the earliest retry time advertised by the current response.
// A missing or malformed hint leaves the existing exponential backoff in control.
func retryAfterTime(headers map[string]string, now time.Time) time.Time {
	for name, value := range headers {
		if !strings.EqualFold(name, "Retry-After") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return time.Time{}
		}
		isSeconds := true
		for _, c := range value {
			if c < '0' || c > '9' {
				isSeconds = false
				break
			}
		}
		if isSeconds {
			seconds, err := strconv.ParseUint(value, 10, 64)
			// Saturate valid but enormous hints rather than overflowing or retrying early.
			if err != nil || seconds > uint64(math.MaxInt64/int64(time.Second)) {
				return now.Add(time.Duration(math.MaxInt64))
			}
			return now.Add(time.Duration(seconds) * time.Second)
		}
		if date, err := http.ParseTime(value); err == nil {
			return date
		}
		return time.Time{}
	}
	return time.Time{}
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
