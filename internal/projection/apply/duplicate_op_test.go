package apply

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func (e *env) tableCounts() map[string]int {
	e.t.Helper()
	var tables []string
	err := e.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			tables = append(tables, name)
		}
		return rows.Err()
	})
	if err != nil {
		e.t.Fatalf("list tables: %v", err)
	}
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		counts[table] = e.count(`SELECT COUNT(*) FROM ` + table)
	}
	return counts
}

func (e *env) recordOperation(channelID int64, opType wire.OpType, job string, msgID int64) {
	e.t.Helper()
	e.exec(`INSERT INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome) VALUES (?1, ?2, ?3, ?4, 'applied')`, channelID, job, string(opType), msgID)
}

func changedTables(before, after map[string]int) []string {
	var changed []string
	for table, n := range after {
		if before[table] != n {
			changed = append(changed, fmt.Sprintf("%s %d->%d", table, before[table], n))
		}
	}
	sort.Strings(changed)
	return changed
}

func TestDuplicateOpRejectsWriteNothingElse(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(e *env)
		obs       projection.Observed
		op        wire.Op
		wantAdded []string
	}{
		{
			name: "del",
			setup: func(e *env) {
				e.recordOperation(bucketChannel, wire.OpDel, jobID(10), 5)
			},
			obs: bucketObs(bucketChannel, 10),
			op:  delOp("k", jobID(10)),
		},
		{
			name: "bcreate",
			setup: func(e *env) {
				e.recordOperation(registryChannel, wire.OpBucketCreate, jobID(10), 5)
			},
			obs: registryObs(10),
			op:  bucketOp(wire.OpBucketCreate, "videos", otherChannel, jobID(10)),
		},
		{
			name: "bcfg",
			setup: func(e *env) {
				e.recordOperation(registryChannel, wire.OpBucketConfig, jobID(10), 5)
			},
			obs: registryObs(10),
			op:  bucketOp(wire.OpBucketConfig, "photos", bucketChannel, jobID(10)),
		},
		{
			name: "bdel",
			setup: func(e *env) {
				e.recordOperation(registryChannel, wire.OpBucketDelete, jobID(10), 5)
			},
			obs: registryObs(10),
			op:  bucketOp(wire.OpBucketDelete, "photos", bucketChannel, jobID(10)),
		},
		{
			name: "encfg job seen at another message",
			setup: func(e *env) {
				e.mustApply(registryObs(1), markerOp(deployment, 1, 1, fingerprintA, 777, "sys.setup"), projection.OutcomeApplied)
				e.recordOperation(registryChannel, wire.OpEncConfig, jobID(10), 5)
			},
			obs: registryObs(10),
			op:  encfgOp(bucketChannel, keyIDA, 1, fingerprintA, jobID(10)),
		},
		{
			name: "encfg version not above the stored key",
			setup: func(e *env) {
				e.mustApply(registryObs(1), markerOp(deployment, 1, 1, fingerprintA, 777, "sys.setup"), projection.OutcomeApplied)
				e.mustApply(registryObs(4), encfgOp(bucketChannel, keyIDA, 1, fingerprintA, jobID(4)), projection.OutcomeApplied)
			},
			obs: registryObs(10),
			op:  encfgOp(bucketChannel, keyIDA, 1, fingerprintA, jobID(10)),
		},
		{
			name: "put",
			setup: func(e *env) {
				e.recordOperation(bucketChannel, wire.OpPut, jobID(10), 5)
			},
			obs:       bucketObs(bucketChannel, 10),
			op:        putOp("k", 9, jobID(10)),
			wantAdded: []string{"pending_message_cleanup 0->1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBucketEnv(t)
			tc.setup(e)
			before := e.tableCounts()
			e.mustReject(tc.obs, tc.op, projection.RejectDuplicateOp)
			changed := changedTables(before, e.tableCounts())
			if strings.Join(changed, ",") != strings.Join(tc.wantAdded, ",") {
				t.Fatalf("changed tables %v, want %v", changed, tc.wantAdded)
			}
			if tc.op.Type != wire.OpPut {
				return
			}
			if got := e.cleanupReason(tc.obs.MsgID); got != cleanupDuplicate {
				t.Fatalf("put cleanup reason %q, want %q", got, cleanupDuplicate)
			}
		})
	}
}

type writingApplier struct {
	err error
}

func (a writingApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?1, ?2, 'external', 0)`, in.Observed.ChannelID, in.Observed.MsgID)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	return projection.ApplyResult{}, a.err
}

func TestHarnessSavepointMirrorsProjector(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantRows int
	}{
		{"applied keeps writes", nil, 1},
		{"duplicate op keeps writes", &projection.Reject{Code: projection.RejectDuplicateOp}, 1},
		{"other reject rolls back", &projection.Reject{Code: projection.RejectBadBodyRef}, 0},
		{"plain error rolls back", fmt.Errorf("apply: failure"), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBucketEnv(t)
			var result projection.ApplyResult
			var applyErr error
			err := e.db.Write(context.Background(), func(tx *sql.Tx) error {
				return e.applyInSavepoint(tx, writingApplier{err: tc.err}, projection.ApplyInput{Observed: bucketObs(bucketChannel, 10)}, &result, &applyErr)
			})
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if n := e.count(`SELECT COUNT(*) FROM deleted_messages`); n != tc.wantRows {
				t.Fatalf("rows %d, want %d", n, tc.wantRows)
			}
		})
	}
}
