package log

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	selectRecorded = `SELECT first_seen_hash, op_type, object_key FROM replay_log WHERE channel_id = ? AND msg_id = ?`

	insertReplayLog = `INSERT INTO replay_log
  (channel_id, msg_id, op_type, op_payload_json, raw_header, first_seen_hash, object_key, job_id, is_document, actor_user_id, msg_date)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	upsertTamper = `INSERT INTO replay_log_tamper (channel_id, msg_id, old_hash, new_hash, new_raw_header)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (channel_id, msg_id, new_hash) DO UPDATE SET observations = observations + 1`

	upsertReject = `INSERT INTO replay_log_rejects (channel_id, msg_id, code, op_type, job_id, raw_header, detail, msg_date)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (channel_id, msg_id) DO UPDATE SET
  code = excluded.code,
  op_type = excluded.op_type,
  job_id = excluded.job_id,
  raw_header = excluded.raw_header,
  detail = excluded.detail,
  msg_date = excluded.msg_date`

	insertOperationOutcome = `INSERT INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome, error)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (channel_id, op_id, op_type) DO NOTHING`
)

type recorded struct {
	hash    string
	opType  string
	keyText sql.NullString
}

type logRow struct {
	obs    projection.Observed
	op     wire.Op
	header string
	hash   string
}

func readRecorded(ctx context.Context, tx projection.Tx, channelID, msgID int64) (recorded, bool, error) {
	var row recorded
	err := tx.QueryRowContext(ctx, selectRecorded, channelID, msgID).Scan(&row.hash, &row.opType, &row.keyText)
	if errors.Is(err, sql.ErrNoRows) {
		return recorded{}, false, nil
	}
	if err != nil {
		return recorded{}, false, fmt.Errorf("projection log: read recorded op: %w", err)
	}
	return row, true, nil
}

func recordTamper(ctx context.Context, tx projection.Tx, obs projection.Observed, oldHash, newHash, header string) error {
	if _, err := tx.ExecContext(ctx, upsertTamper, obs.ChannelID, obs.MsgID, oldHash, newHash, header); err != nil {
		return fmt.Errorf("projection log: record tamper: %w", err)
	}
	return nil
}

func insertLogRow(ctx context.Context, tx projection.Tx, row logRow) error {
	payload, err := payloadJSON(row.op)
	if err != nil {
		return err
	}
	var objectKey any
	if key, ok := wire.KeyOf(row.op); ok {
		objectKey = key
	}
	_, err = tx.ExecContext(ctx, insertReplayLog,
		row.obs.ChannelID, row.obs.MsgID, string(row.op.Type), string(payload), row.header, row.hash,
		objectKey, row.op.JobID, boolToInt(row.obs.IsDocument), row.obs.ActorUserID, row.obs.MsgDate)
	if err != nil {
		return fmt.Errorf("projection log: insert replay_log: %w", err)
	}
	return nil
}

func payloadJSON(op wire.Op) ([]byte, error) {
	var payload []byte
	var err error
	if wire.Known(op.Type) {
		payload, err = json.Marshal(op)
	} else {
		payload, err = json.Marshal(struct {
			T string `json:"t"`
		}{T: string(op.Type)})
	}
	if err != nil {
		return nil, fmt.Errorf("projection log: marshal op: %w", err)
	}
	return payload, nil
}

func recordReject(ctx context.Context, tx projection.Tx, obs projection.Observed, header string, op wire.Op, reject *projection.Reject) error {
	_, err := tx.ExecContext(ctx, upsertReject,
		obs.ChannelID, obs.MsgID, string(reject.Code), string(op.Type), op.JobID, header, reject.Detail, obs.MsgDate)
	if err != nil {
		return fmt.Errorf("projection log: record reject: %w", err)
	}
	return nil
}

func recordOperationOutcome(ctx context.Context, tx projection.Tx, obs projection.Observed, op wire.Op, outcome projection.OutcomeKind, detail string) error {
	if op.JobID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, insertOperationOutcome,
		obs.ChannelID, op.JobID, string(op.Type), obs.MsgID, string(outcome), detail)
	if err != nil {
		return fmt.Errorf("projection log: record operation outcome: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
