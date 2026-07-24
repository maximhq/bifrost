package logstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// This file implements runtime resilience for the materialized-view read
// path. Startup already repairs shape drift (repairMatViewShapes diffs the
// live catalog against matviewRequiredColumns and drops stale views), but a
// query can still hit a missing or stale-shaped view at runtime: a replica
// that lost the ensureMatViews advisory-lock race during a rolling deploy
// reads while the lock holder is mid-rebuild, or an operator drops a view.
// Rather than gating reads on catalog checks (the approach of 8354ca76,
// reverted in 9c3e4c7f3), the failing query itself signals staleness: the
// dispatch site serves that request from the raw logs table, the matview
// read path is disabled process-wide, and a single-flight background repair
// recreates and refreshes the views before re-enabling it. A premature
// ready=true from any source is therefore harmless - the next shape error
// restarts the cycle, and the system converges once shapes are current.

// isMatViewShapeError reports whether err indicates a materialized view that
// is missing or has a stale shape, so the caller should fall back to the raw
// logs table. Deliberately excluded: 42883 (undefined_function - our readers
// use only built-ins, so that is a code bug we want loud) and 0A000
// (cached-plan drift is structurally prevented by the two-pool connection
// lifecycle; masking it would hide a regression there).
func isMatViewShapeError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42P01", // undefined_table: view dropped (mid-repair window, operator action)
		"42703", // undefined_column: old-shape view still present (the #5384 failure)
		"55000": // object_not_in_prerequisite_state: matview exists but is not populated
		return true
	}
	return false
}

// fallBackToRaw classifies err; on a matview shape error it disables the
// matview read path for subsequent requests, kicks the background self-heal,
// and returns true so the dispatch site falls through to its raw-table
// implementation for the current request. Any other error (including nil)
// returns false and the caller returns normally.
func (s *RDBLogStore) fallBackToRaw(err error) bool {
	if err == nil || !isMatViewShapeError(err) {
		return false
	}
	s.matViewsReady.Store(false)
	if s.logger != nil {
		s.logger.Warn(fmt.Sprintf("logstore: matview query failed with shape error, serving from raw tables until repaired: %s", err))
	}
	s.triggerMatViewSelfHeal()
	return true
}

// matViewHealCooldown bounds how often a process attempts a background
// repair. No dedicated retry loop exists here because recovery after a failed
// heal is owned by the periodic refresher: startMatViewRefresher re-arms
// matViewsReady on its next successful tick, and it is guaranteed to be
// running whenever self-heal can trigger (a shape error requires the matview
// path to have been enabled, which requires the boot-time ensureMatViews that
// also starts the refresher). Future shape errors additionally re-trigger the
// heal, cooldown-limited - while broken, every request keeps succeeding via
// the raw fallback.
const matViewHealCooldown = 30 * time.Second

// triggerMatViewSelfHeal starts a single-flight background repair: recreate
// any missing or stale matviews (ensureMatViews serializes cross-replica on
// the refresh advisory lock and handles shape diffing, drops, creates, and
// index builds), refresh them, and re-enable the matview read path. Repair
// failures are logged only; the raw fallback keeps serving in the meantime.
//
// A replica that loses the advisory-lock race re-enables the read path while
// the lock holder may still be mid-rebuild; that premature enable is accepted
// - the next query against a still-stale view falls back raw and re-triggers
// the heal, converging once the lock holder finishes.
func (s *RDBLogStore) triggerMatViewSelfHeal() {
	if !s.matViewHealInFlight.CompareAndSwap(false, true) {
		return // a heal is already running
	}
	go func() {
		defer s.matViewHealInFlight.Store(false)
		if time.Since(time.Unix(0, s.matViewHealLastAttempt.Load())) < matViewHealCooldown {
			return
		}
		s.matViewHealLastAttempt.Store(time.Now().UnixNano())

		ctx := context.Background()
		if err := ensureMatViews(ctx, s.db); err != nil {
			if s.logger != nil {
				s.logger.Warn(fmt.Sprintf("logstore: matview self-heal creation failed: %s (still serving from raw tables)", err))
			}
			return
		}
		if err := refreshMatViews(ctx, s.db); err != nil {
			if s.logger != nil {
				s.logger.Warn(fmt.Sprintf("logstore: matview self-heal refresh failed: %s (still serving from raw tables)", err))
			}
			return
		}
		s.matViewsReady.Store(true)
		if s.logger != nil {
			s.logger.Info("logstore: materialized views self-healed after shape error")
		}
	}()
}

// resetMatViewHeal clears the single-flight and cooldown state. Test helper.
func (s *RDBLogStore) resetMatViewHeal() {
	s.matViewHealInFlight.Store(false)
	s.matViewHealLastAttempt.Store(0)
}
