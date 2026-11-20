package vectorstore

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The vector budget bounds the space log-derived vectors take: Warp's log
// index and every complexity router's captured requests, together, under one
// size. Vector stores report no size per namespace, so the size is estimated
// from counts: each vector is its embedding (four bytes a dimension) plus a
// fixed allowance for its metadata and its share of the store's index. The
// pass acts before the budget is reached: past BudgetHighWatermark of it, the
// oldest vectors go first across every index, down to BudgetLowWatermark. One
// cutoff time is found for all of them, and each deletes what is older, so a
// quiet index is not emptied to make room for a busy one's recent vectors.

// BudgetBytesPerVectorOverhead is the allowance, beyond the embedding, for one
// vector's metadata and its share of the store's index structures.
const BudgetBytesPerVectorOverhead = 1024

// BudgetHighWatermark is the share of the budget past which a pass trims. The
// pass runs hourly, so trimming only once the budget was crossed would let an
// hour's growth go over it; at 80%, a fifth of the budget is headroom for that.
const BudgetHighWatermark = 0.8

// BudgetLowWatermark is the share of the budget a trim goes down to. The gap to
// the high watermark spaces trims out: under it, a pass is only the counts.
const BudgetLowWatermark = 0.65

// budgetSearchSpan is how far back a cutoff is searched for: older vectors are
// taken as the oldest there are, and go first.
const budgetSearchSpan = 10 * 365 * 24 * time.Hour

// BudgetIndex is one log-derived index the budget covers.
type BudgetIndex struct {
	Name      string      // for logs: "warp", or the capture namespace
	Store     VectorStore // where it lives
	Namespace string
	// Dimension is the embedding width; 0 reads it off one stored vector.
	Dimension int
	// Filter selects the index's own vectors in the namespace, so nothing else
	// stored there is counted or deleted.
	Filter []Query
	// TimestampField holds each vector's time in unix seconds.
	TimestampField string
}

// BudgetResult reports one pass.
type BudgetResult struct {
	EstimatedBytes int64     // the estimate before trimming
	Cutoff         time.Time // zero when nothing was trimmed
	Deleted        int       // vectors the stores confirmed deleted
	// Skipped is the indexes that could not be counted, and why. They are left
	// out of the estimate and are not trimmed.
	Skipped []string
}

// EnforceBudget estimates the size of the indexes and, when it is past
// BudgetHighWatermark of budgetBytes, deletes their oldest vectors, across all
// of them, until the estimate is BudgetLowWatermark of the budget.
//
// An index that cannot be counted (a store that cannot count, one that is down,
// or a Chromem namespace not yet opened with CreateNamespace since a restart)
// is left out and listed in Skipped. Leaving it out only makes the estimate
// smaller, so the others are trimmed less, never more, than the budget asks.
func EnforceBudget(ctx context.Context, budgetBytes int64, indexes []BudgetIndex, now time.Time) (BudgetResult, error) {
	if budgetBytes <= 0 || len(indexes) == 0 {
		return BudgetResult{}, nil
	}
	sized := make([]sizedIndex, 0, len(indexes))
	var total int64
	var skipped []string
	for _, index := range indexes {
		count, err := Count(ctx, index.Store, index.Namespace, index.Filter)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", index.Name, err))
			continue
		}
		if count == 0 {
			continue
		}
		dimension := index.Dimension
		if dimension <= 0 {
			dimension = storedDimension(ctx, index)
		}
		entry := sizedIndex{BudgetIndex: index, count: count, bytesPerVector: int64(dimension)*4 + BudgetBytesPerVectorOverhead}
		total += entry.count * entry.bytesPerVector
		sized = append(sized, entry)
	}
	result := BudgetResult{EstimatedBytes: total, Skipped: skipped}
	if total <= int64(float64(budgetBytes)*BudgetHighWatermark) {
		return result, nil
	}
	target := int64(float64(budgetBytes) * BudgetLowWatermark)
	cutoff, err := budgetCutoff(ctx, sized, target, now)
	if err != nil {
		return result, err
	}
	result.Cutoff = cutoff
	// Every index is tried, so one store's failure does not leave the others
	// over budget; the failures are reported together.
	var failures []error
	for _, index := range sized {
		deleted, err := index.Store.DeleteAll(ctx, index.Namespace, index.olderThan(cutoff))
		// A store can report a delete that did not happen per entry, with no
		// error for the call: only confirmed deletes count.
		var failed int
		firstFailure := ""
		for _, entry := range deleted {
			if entry.Status == DeleteStatusSuccess {
				result.Deleted++
				continue
			}
			if failed == 0 {
				firstFailure = entry.Error
			}
			failed++
		}
		switch {
		case err != nil:
			failures = append(failures, fmt.Errorf("trim %s: %w", index.Name, err))
		case failed > 0:
			failures = append(failures, fmt.Errorf("trim %s: %d of %d deletes failed: %s", index.Name, failed, len(deleted), firstFailure))
		}
	}
	return result, errors.Join(failures...)
}

type sizedIndex struct {
	BudgetIndex
	count          int64
	bytesPerVector int64
}

func (i sizedIndex) olderThan(cutoff time.Time) []Query {
	return i.withTime(QueryOperatorLessThan, cutoff)
}

func (i sizedIndex) atOrAfter(cutoff time.Time) []Query {
	return i.withTime(QueryOperatorGreaterThanOrEqual, cutoff)
}

func (i sizedIndex) withTime(operator QueryOperator, cutoff time.Time) []Query {
	queries := make([]Query, 0, len(i.Filter)+1)
	queries = append(queries, i.Filter...)
	return append(queries, Query{Field: i.TimestampField, Operator: operator, Value: cutoff.Unix()})
}

// budgetCutoff finds the oldest second such that what is at or after it,
// across every index, fits in target. Keeping fewer vectors as the cutoff
// moves later makes the search a bisection over seconds, the resolution of the
// stored timestamps: about thirty filtered counts of each index over the ten
// years it spans, and only when a trim is due. Vectors written in the same
// second are kept or deleted together. The second after now always fits,
// keeping nothing older than now.
//
// What is kept is counted, not worked out from the first count: vectors
// deleted since then, by retention or another node's pass, would otherwise
// still count as kept and push the cutoff later than the budget needs.
func budgetCutoff(ctx context.Context, indexes []sizedIndex, target int64, now time.Time) (time.Time, error) {
	hi := now.Truncate(time.Second).Add(time.Second)
	lo := hi.Add(-budgetSearchSpan)
	kept := func(cutoff time.Time) (int64, error) {
		var bytes int64
		for _, index := range indexes {
			kept, err := Count(ctx, index.Store, index.Namespace, index.atOrAfter(cutoff))
			if err != nil {
				return 0, fmt.Errorf("count %s from %s: %w", index.Name, cutoff.Format(time.RFC3339), err)
			}
			bytes += kept * index.bytesPerVector
		}
		return bytes, nil
	}
	if fits, err := kept(lo); err != nil {
		return time.Time{}, err
	} else if fits <= target {
		return lo, nil
	}
	// Invariant: lo does not fit, hi does.
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / time.Second / 2 * time.Second)
		bytes, err := kept(mid)
		if err != nil {
			return time.Time{}, err
		}
		if bytes <= target {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi, nil
}

// storedDimension reads one vector of the index to learn its width, for an
// index whose width is not known (a capture namespace's is hashed into its
// name). A store that will not return one counts the commonest width,
// OpenAI's 1536, which keeps the estimate in the right range.
func storedDimension(ctx context.Context, index BudgetIndex) int {
	const fallback = 1536
	results, _, err := index.Store.GetAll(WithIncludeVectors(ctx), index.Namespace, index.Filter, nil, nil, 1)
	if err != nil || len(results) == 0 || len(results[0].Vector) == 0 {
		return fallback
	}
	return len(results[0].Vector)
}
