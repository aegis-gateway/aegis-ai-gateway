// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package purge implements auditable deletion of audit_events
// rows that fall outside the configured retention window.
package purge

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Table selects which table(s) are targeted by a purge run.
type Table string

const (
	// TableAuditLogs is retained so that a stored config or a script naming it
	// gets a clear refusal rather than a confusing one. Migration 017 dropped
	// the table; nothing ever wrote it.
	TableAuditLogs   Table = "audit_logs"
	TableAuditEvents Table = "audit_events"
	TableBoth        Table = "both"
)

// Options configures a purge run.
type Options struct {
	Before  time.Time // delete rows with timestamp < Before
	DryRun  bool
	Table   Table
	BatchSz int // rows per DELETE statement; default 1000
}

// Result summarises a completed purge run.
type Result struct {
	WindowStart   time.Time
	WindowEnd     time.Time
	IDMin         int64
	IDMax         int64
	RowsDeleted   int
	CheckpointIDs []int64
	DryRun        bool
}

// Run executes a purge according to opts and returns a summary.
//
// Dry-run mode queries counts and ID ranges without deleting. Either way a
// row is written to audit_purges so every run is traceable.
//
// When unsealed events exist in the purge window a warning is printed to
// stdout but execution continues; run 'aegis-migrate seal' first for full
// attestation integrity.
func Run(ctx context.Context, pool *pgxpool.Pool, opts Options) (*Result, error) {
	if opts.BatchSz <= 0 {
		opts.BatchSz = 1000
	}
	if opts.Table == "" {
		opts.Table = TableBoth
	}

	res := &Result{
		WindowStart: time.Time{},
		WindowEnd:   opts.Before,
		DryRun:      opts.DryRun,
	}

	// Refuse the table migration 017 dropped, rather than reporting a purge of
	// zero rows. A caller asking to purge audit_logs believes it holds the
	// decision record; answering "0 deleted" confirms that belief and is the
	// same failure the retired endpoint had, where an empty result read as "no
	// activity" instead of "this does not exist".
	if opts.Table == TableAuditLogs {
		return nil, fmt.Errorf(
			"purge: audit_logs was dropped by migration 017 and nothing ever wrote it; " +
				"the decision record is in audit_events, so purge --table audit_events")
	}

	// Advisory warning for unsealed events.
	if msg := unsealedWarning(ctx, pool, opts.Before); msg != "" {
		fmt.Println(msg)
	}

	// audit_events is the only table left to purge.
	rangeTable := "audit_events"

	idMin, idMax, err := queryIDRange(ctx, pool, rangeTable, opts.Before)
	if err != nil {
		return nil, fmt.Errorf("range query on %s: %w", rangeTable, err)
	}
	res.IDMin = idMin
	res.IDMax = idMax

	// Checkpoints attest audit_events only, and their range_start/range_end are
	// audit_events ids. audit_logs has its own unrelated BIGSERIAL, so feeding a
	// log id range into this lookup attributes arbitrary checkpoints — or none —
	// to the purge, and that attribution is written permanently to audit_purges.
	// Only look up checkpoints when audit_events rows are actually in scope.
	cids, err := overlappingCheckpoints(ctx, pool, idMin, idMax)
	if err != nil {
		return nil, fmt.Errorf("checkpoint lookup: %w", err)
	}
	res.CheckpointIDs = cids

	if opts.DryRun {
		count, err := countRows(ctx, pool, opts.Table, opts.Before)
		if err != nil {
			return nil, fmt.Errorf("dry-run count: %w", err)
		}
		res.RowsDeleted = count

		if err := recordPurge(ctx, pool, res); err != nil {
			return nil, fmt.Errorf("write audit_purges: %w", err)
		}
		return res, nil
	}

	// Real purge inside a single transaction so the audit_purges record is
	// atomic with the deletes.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	total, err := execBatchDelete(ctx, tx, opts.Table, opts.Before, opts.BatchSz)
	if err != nil {
		return nil, err
	}
	res.RowsDeleted = total

	if err := recordPurgeTx(ctx, tx, res); err != nil {
		return nil, fmt.Errorf("write audit_purges: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

// OldestEventAgeDays returns the age in days of the oldest row in audit_events.
// Returns -1 when the table is empty or unreachable.
func OldestEventAgeDays(ctx context.Context, pool *pgxpool.Pool) float64 {
	var oldest time.Time
	err := pool.QueryRow(ctx, `SELECT MIN("timestamp") FROM audit_events`).Scan(&oldest)
	if err != nil || oldest.IsZero() {
		return -1
	}
	return time.Since(oldest).Hours() / 24
}

// --- helpers -----------------------------------------------------------------

func queryIDRange(ctx context.Context, pool *pgxpool.Pool, table string, before time.Time) (int64, int64, error) {
	q := fmt.Sprintf(`SELECT COALESCE(MIN(id),0), COALESCE(MAX(id),0) FROM %s WHERE "timestamp" < $1`, table)
	var idMin, idMax int64
	if err := pool.QueryRow(ctx, q, before).Scan(&idMin, &idMax); err != nil {
		return 0, 0, err
	}
	return idMin, idMax, nil
}

func countRows(ctx context.Context, pool *pgxpool.Pool, tbl Table, before time.Time) (int, error) {
	tables := tablesFor(tbl)
	total := 0
	for _, t := range tables {
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE "timestamp" < $1`, t)
		var n int
		if err := pool.QueryRow(ctx, q, before).Scan(&n); err != nil {
			return 0, fmt.Errorf("count %s: %w", t, err)
		}
		total += n
	}
	return total, nil
}

func execBatchDelete(ctx context.Context, tx pgx.Tx, tbl Table, before time.Time, batchSz int) (int, error) {
	tables := tablesFor(tbl)
	total := 0
	for _, t := range tables {
		q := fmt.Sprintf(
			`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE "timestamp" < $1 LIMIT $2)`,
			t, t,
		)
		for {
			tag, err := tx.Exec(ctx, q, before, batchSz)
			if err != nil {
				return total, fmt.Errorf("delete %s: %w", t, err)
			}
			n := int(tag.RowsAffected())
			total += n
			if n < batchSz {
				break
			}
		}
	}
	return total, nil
}

// tablesFor names the tables a purge touches.
//
// audit_events only, since migration 017 dropped audit_logs. TableBoth is kept
// as the default so an existing invocation continues to work and means what it
// now can mean.
func tablesFor(tbl Table) []string {
	return []string{"audit_events"}
}

func overlappingCheckpoints(ctx context.Context, pool *pgxpool.Pool, idMin, idMax int64) ([]int64, error) {
	rows, err := pool.Query(ctx,
		`SELECT id FROM audit_checkpoints WHERE range_start <= $2 AND range_end >= $1 ORDER BY id`,
		idMin, idMax,
	)
	if err != nil {
		// Only a genuinely absent table is benign — that is a deployment that
		// has not run migration 008 yet. Any other failure (connection lost,
		// permission denied) must surface: returning an empty list would write
		// "no checkpoints affected" permanently into audit_purges, which is the
		// record someone later relies on to explain a gap in the chain.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
			return []int64{}, nil
		}
		return nil, fmt.Errorf("query overlapping checkpoints: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}

func unsealedWarning(ctx context.Context, pool *pgxpool.Pool, before time.Time) string {
	var lastEnd int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(range_end),0) FROM audit_checkpoints`).Scan(&lastEnd); err != nil {
		return "" // table absent
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE "timestamp" < $1 AND id > $2`,
		before, lastEnd,
	).Scan(&count); err != nil || count == 0 {
		return ""
	}
	return fmt.Sprintf(
		"Warning: unsealed events in purge window (%d rows beyond last checkpoint). Run 'aegis-migrate seal' first for full attestation.",
		count,
	)
}

const insertPurge = `
INSERT INTO audit_purges
    (window_start, window_end, event_id_min, event_id_max, rows_deleted, affected_checkpoint_ids, dry_run)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

func recordPurge(ctx context.Context, pool *pgxpool.Pool, res *Result) error {
	_, err := pool.Exec(ctx, insertPurge,
		res.WindowStart, res.WindowEnd,
		res.IDMin, res.IDMax,
		res.RowsDeleted,
		safeSlice(res.CheckpointIDs),
		res.DryRun,
	)
	return err
}

func recordPurgeTx(ctx context.Context, tx pgx.Tx, res *Result) error {
	_, err := tx.Exec(ctx, insertPurge,
		res.WindowStart, res.WindowEnd,
		res.IDMin, res.IDMax,
		res.RowsDeleted,
		safeSlice(res.CheckpointIDs),
		res.DryRun,
	)
	return err
}

func safeSlice(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}
