package query

import (
	"context"
	"encoding/json"
	"time"
)

// ShareStatus is the live state of one share: its queued or running job with
// progress, and its latest scan, so a client can tell when to reload.
type ShareStatus struct {
	ShareID          int64      `json:"share_id"`
	Job              *ActiveJob `json:"job"`
	LatestScanID     *int64     `json:"latest_scan_id"`
	LatestScanStatus *string    `json:"latest_scan_status"`
	LatestFinishedAt *time.Time `json:"latest_scan_finished_at"`
}

// Statuses returns the live state of every share in one query.
func (q *DB) Statuses(ctx context.Context) ([]ShareStatus, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT s.id, j.id, j.kind, j.state, j.trigger, j.requested_by, j.requested_at, j.scan_id, sc.progress,
		       l.id, l.status, l.finished_at
		FROM shares s
		LEFT JOIN LATERAL (SELECT * FROM jobs WHERE share_id = s.id AND state IN ('queued', 'running') ORDER BY id DESC LIMIT 1) j ON true
		LEFT JOIN scans sc ON sc.id = j.scan_id AND sc.status = 'running'
		LEFT JOIN LATERAL (SELECT id, status, finished_at FROM scans WHERE share_id = s.id ORDER BY id DESC LIMIT 1) l ON true
		ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareStatus{}
	for rows.Next() {
		var st ShareStatus
		var jid *int64
		var kind, state, trigger, by *string
		var at *time.Time
		var scan *int64
		var prog []byte
		if err := rows.Scan(&st.ShareID, &jid, &kind, &state, &trigger, &by, &at, &scan, &prog,
			&st.LatestScanID, &st.LatestScanStatus, &st.LatestFinishedAt); err != nil {
			return nil, err
		}
		if jid != nil {
			st.Job = &ActiveJob{ID: *jid, Kind: *kind, State: *state, Trigger: *trigger, RequestedBy: *by, RequestedAt: *at, ScanID: scan}
			if len(prog) > 0 {
				st.Job.Progress = json.RawMessage(prog)
			}
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
