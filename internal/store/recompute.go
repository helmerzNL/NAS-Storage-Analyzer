package store

import (
	"context"
	"fmt"
)

// RecomputeAll rebuilds every derived number of a share from its files table:
// hardlink dedup, direct and recursive directory sums, category and extension
// totals. It does not touch the file system. Use it to repair or to seed.
func (s *Store) RecomputeAll(ctx context.Context, shareID int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := aggregate(ctx, tx, shareID, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Recategorize applies the current extension_map to the stored files of a
// share and rebuilds the aggregates, without rescanning. It returns the number
// of files whose category changed. Snapshots of earlier scans keep the
// category layout they had when they were taken.
func (s *Store) Recategorize(ctx context.Context, shareID int64) (changed int64, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(ctx, `
		UPDATE files f SET category_id = c.cat
		FROM (SELECT e.ext, coalesce(m.category_id, 8::smallint) AS cat
		      FROM (SELECT DISTINCT ext FROM files WHERE share_id = $1) e
		      LEFT JOIN extension_map m ON m.ext = e.ext) c
		WHERE f.share_id = $1 AND f.ext = c.ext AND f.category_id <> c.cat`, shareID)
	if err != nil {
		return 0, fmt.Errorf("recategorize files: %w", err)
	}
	if err := aggregate(ctx, tx, shareID, true); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}
