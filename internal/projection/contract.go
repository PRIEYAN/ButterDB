package projection

import (
	"context"
	"database/sql"

	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Writer interface {
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type Observed struct {
	ChannelID   int64
	MsgID       int64
	MsgDate     int64
	IsDocument  bool
	ActorUserID int64
	RawText     string
}

type OutcomeKind string

const (
	OutcomeIgnored     OutcomeKind = "ignored"
	OutcomeAlreadySeen OutcomeKind = "already_seen"
	OutcomeTampered    OutcomeKind = "tampered"
	OutcomeApplied     OutcomeKind = "applied"
	OutcomeSuperseded  OutcomeKind = "superseded"
	OutcomeRejected    OutcomeKind = "rejected"
)

type RejectCode string

const (
	RejectMalformed         RejectCode = "malformed"
	RejectUnknownOp         RejectCode = "unknown_op"
	RejectWrongChannel      RejectCode = "wrong_channel"
	RejectWrongCarrier      RejectCode = "wrong_carrier"
	RejectBucketNotFound    RejectCode = "bucket_not_found"
	RejectBucketNameInvalid RejectCode = "bucket_name_invalid"
	RejectBucketExists      RejectCode = "bucket_exists"
	RejectBadBodyRef        RejectCode = "bad_body_ref"
	RejectSizeMismatch      RejectCode = "size_mismatch"
	RejectDuplicateOp       RejectCode = "duplicate_op"
	RejectMarkerConflict    RejectCode = "marker_conflict"
	RejectKEKMismatch       RejectCode = "kek_mismatch"
)

type Outcome struct {
	Kind       OutcomeKind
	OpType     wire.OpType
	Key        string
	RejectCode RejectCode
}

type Reject struct {
	Code   RejectCode
	Detail string
}

func (r *Reject) Error() string {
	return "projection: reject " + string(r.Code) + ": " + r.Detail
}

type ApplyInput struct {
	Observed Observed
	Op       wire.Op
}

type ApplyResult struct {
	Kind OutcomeKind
}

type Applier interface {
	Apply(ctx context.Context, tx Tx, in ApplyInput) (ApplyResult, error)
}

type Registry interface {
	Lookup(t wire.OpType) (Applier, bool)
}
