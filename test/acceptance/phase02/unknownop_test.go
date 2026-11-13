package phase02

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	unknownOpChannel = int64(2045123456)
	unknownOpMsgID   = int64(9001)
	unknownOpDate    = int64(1763000800)
)

func firstSeenHash(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(wire.ExtractHeaderLine(raw))))
	return hex.EncodeToString(sum[:])
}

func recordUnknownOp(t *testing.T, db *sqlitedb.DB, msgID int64, raw string, op wire.Op) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"t": string(op.Type)})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO replay_log (channel_id, msg_id, op_type, op_payload_json, raw_header, first_seen_hash, is_document, msg_date)
			 VALUES (?, ?, ?, ?, ?, ?, 0, ?)`,
			unknownOpChannel, msgID, string(op.Type), string(payload), raw, firstSeenHash(raw), unknownOpDate); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO replay_log_rejects (channel_id, msg_id, code, op_type, raw_header, msg_date)
			 VALUES (?, ?, 'unknown_op', ?, ?, ?)`,
			unknownOpChannel, msgID, string(op.Type), raw, unknownOpDate)
		return err
	})
	if err != nil {
		t.Fatalf("record unknown op %q: %v", op.Type, err)
	}
}

func TestUnknownOpRemainsAReplayableReject(t *testing.T) {
	db := openMigrated(t, sqlitedb.Projection, schema.Projection)
	raws := make([]string, 0, len(legacyOpTypes)+1)
	for _, name := range append([]string{"newfutureop"}, legacyOpTypes...) {
		raws = append(raws, "TDX1|t="+name+"|j="+putJob+"|zz=1\nremainder ignored")
	}
	for i, raw := range raws {
		name := strings.TrimPrefix(strings.SplitN(raw, "|", 3)[1], "t=")
		t.Run(name, func(t *testing.T) {
			op, err := wire.Parse(raw)
			if !errors.Is(err, wire.ErrWireUnknownOp) {
				t.Fatalf("Parse error = %v, want ErrWireUnknownOp", err)
			}
			if string(op.Type) != name {
				t.Fatalf("Type = %q, want %q", op.Type, name)
			}
			if wire.Known(op.Type) {
				t.Fatalf("%q is known", op.Type)
			}
			if !wire.HasPrefix(raw) {
				t.Fatalf("HasPrefix is false for a TDX1 header")
			}

			msgID := unknownOpMsgID + int64(i)
			recordUnknownOp(t, db, msgID, raw, op)

			var storedRaw, opType, payload, code, rejectType string
			err = db.Reader().QueryRow(
				`SELECT l.raw_header, l.op_type, l.op_payload_json, r.code, r.op_type
				 FROM replay_log l JOIN replay_log_rejects r USING (channel_id, msg_id)
				 WHERE l.channel_id = ? AND l.msg_id = ?`, unknownOpChannel, msgID).
				Scan(&storedRaw, &opType, &payload, &code, &rejectType)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if storedRaw != raw || opType != name || rejectType != name || code != "unknown_op" {
				t.Fatalf("stored row = %q %q %q %q", storedRaw, opType, rejectType, code)
			}
			if payload != `{"t":"`+name+`"}` {
				t.Fatalf("payload = %s", payload)
			}

			replayed, err := wire.Parse(storedRaw)
			if !errors.Is(err, wire.ErrWireUnknownOp) || replayed.Type != op.Type {
				t.Fatalf("replay parse = %+v, %v; want the same unknown op", replayed, err)
			}
		})
	}

	var logRows, rejectRows int
	if err := db.Reader().QueryRow("SELECT (SELECT count(*) FROM replay_log), (SELECT count(*) FROM replay_log_rejects WHERE code = 'unknown_op')").Scan(&logRows, &rejectRows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if logRows != len(raws) || rejectRows != len(raws) {
		t.Fatalf("rows = %d replay_log, %d rejects; want %d each", logRows, rejectRows, len(raws))
	}
}

func TestUnknownOpWithoutTypeIsMalformedNotUnknown(t *testing.T) {
	for _, raw := range []string{"TDX1|t=", "TDX1|t=|j=" + putJob} {
		op, err := wire.Parse(raw)
		if !errors.Is(err, wire.ErrWireMalformed) {
			t.Fatalf("Parse(%q) error = %v, want ErrWireMalformed", raw, err)
		}
		if errors.Is(err, wire.ErrWireUnknownOp) || op.Type != "" {
			t.Fatalf("Parse(%q) = %+v, %v; an empty type must not be an unknown op", raw, op, err)
		}
	}
}

func TestUnknownOpClassification(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		candidate  bool
		wantErr    error
		wantRecord bool
	}{
		{"plain text is not a candidate", "hello", false, wire.ErrWireMalformed, false},
		{"foreign header is not a candidate", "hello|t=put", false, wire.ErrWireBadVersion, false},
		{"empty caption is not a candidate", "", false, wire.ErrWireMissingHeader, false},
		{"bare grammar prefix is a candidate and malformed", "TDX1", true, wire.ErrWireMalformed, false},
		{"bad version is a candidate and malformed", "TDX1x|t=put", true, wire.ErrWireBadVersion, false},
		{"unknown type is a candidate and recorded", "TDX1|t=zz", true, wire.ErrWireUnknownOp, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if wire.HasPrefix(tc.raw) != tc.candidate {
				t.Fatalf("HasPrefix = %v, want %v", wire.HasPrefix(tc.raw), tc.candidate)
			}
			_, err := wire.Parse(tc.raw)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Parse error = %v, want %v", err, tc.wantErr)
			}
			if recorded := errors.Is(err, wire.ErrWireUnknownOp); recorded != tc.wantRecord {
				t.Fatalf("recorded as unknown op = %v, want %v", recorded, tc.wantRecord)
			}
		})
	}
}
