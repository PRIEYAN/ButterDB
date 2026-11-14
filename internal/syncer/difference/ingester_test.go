package difference

import (
	"context"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/projection/rebuild"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type scriptedProjector struct {
	outcome projection.Outcome
	err     error
}

func (p scriptedProjector) ProjectFromOpTx(context.Context, projection.Tx, projection.Observed) (projection.Outcome, error) {
	return p.outcome, p.err
}

type scriptedRepairs struct {
	needed    bool
	needsErr  error
	repairErr error
	checks    int
	repairs   int
}

func (r *scriptedRepairs) NeedsKeyRepair(context.Context, projection.Tx, int64, string, int64) (bool, error) {
	r.checks++
	return r.needed, r.needsErr
}

func (r *scriptedRepairs) RepairKeyTx(context.Context, projection.Tx, int64, string) (rebuild.Result, error) {
	r.repairs++
	return rebuild.Result{}, r.repairErr
}

func TestIngesterRepairsOnlyNewKeyOpsBehindARecordedOne(t *testing.T) {
	errBoom := errors.New("boom")
	cases := []struct {
		name        string
		outcome     projection.Outcome
		needed      bool
		projectErr  error
		needsErr    error
		repairErr   error
		wantChecks  int
		wantRepairs int
		wantErr     error
	}{
		{"applied put behind a recorded op", projection.Outcome{Kind: projection.OutcomeApplied, OpType: wire.OpPut, Key: "k"}, true, nil, nil, nil, 1, 1, nil},
		{"applied put with nothing above it", projection.Outcome{Kind: projection.OutcomeApplied, OpType: wire.OpPut, Key: "k"}, false, nil, nil, nil, 1, 0, nil},
		{"superseded del behind a recorded op", projection.Outcome{Kind: projection.OutcomeSuperseded, OpType: wire.OpDel, Key: "k"}, true, nil, nil, nil, 1, 1, nil},
		{"duplicate op reject behind a recorded op", projection.Outcome{Kind: projection.OutcomeRejected, OpType: wire.OpPut, Key: "k", RejectCode: projection.RejectDuplicateOp}, true, nil, nil, nil, 1, 1, nil},
		{"size mismatch reject is not a trigger", projection.Outcome{Kind: projection.OutcomeRejected, OpType: wire.OpPut, Key: "k", RejectCode: projection.RejectSizeMismatch}, true, nil, nil, nil, 0, 0, nil},
		{"already seen op is an own write", projection.Outcome{Kind: projection.OutcomeAlreadySeen, OpType: wire.OpPut, Key: "k"}, true, nil, nil, nil, 0, 0, nil},
		{"tampered op changes nothing", projection.Outcome{Kind: projection.OutcomeTampered, OpType: wire.OpPut, Key: "k"}, true, nil, nil, nil, 0, 0, nil},
		{"ignored message", projection.Outcome{Kind: projection.OutcomeIgnored}, true, nil, nil, nil, 0, 0, nil},
		{"bucket op has no key", projection.Outcome{Kind: projection.OutcomeApplied, OpType: wire.OpBucketCreate}, true, nil, nil, nil, 0, 0, nil},
		{"body op has no key", projection.Outcome{Kind: projection.OutcomeApplied, OpType: wire.OpBody}, true, nil, nil, nil, 0, 0, nil},
		{"projection failure", projection.Outcome{}, true, errBoom, nil, nil, 0, 0, errBoom},
		{"order check failure", projection.Outcome{Kind: projection.OutcomeApplied, OpType: wire.OpPut, Key: "k"}, true, nil, errBoom, nil, 1, 0, errBoom},
		{"repair failure", projection.Outcome{Kind: projection.OutcomeApplied, OpType: wire.OpPut, Key: "k"}, true, nil, nil, errBoom, 1, 1, errBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repairs := &scriptedRepairs{needed: tc.needed, needsErr: tc.needsErr, repairErr: tc.repairErr}
			ingester, err := NewIngester(IngesterDependencies{
				Projector: scriptedProjector{outcome: tc.outcome, err: tc.projectErr}, Repairs: repairs, Logger: discardLogger(),
			})
			if err != nil {
				t.Fatalf("new ingester: %v", err)
			}
			outcome, err := ingester.IngestTx(context.Background(), nil, projection.Observed{ChannelID: 5, MsgID: 9})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && outcome != tc.outcome {
				t.Fatalf("outcome = %+v, want %+v", outcome, tc.outcome)
			}
			if repairs.checks != tc.wantChecks || repairs.repairs != tc.wantRepairs {
				t.Fatalf("checks=%d repairs=%d, want %d/%d", repairs.checks, repairs.repairs, tc.wantChecks, tc.wantRepairs)
			}
		})
	}
}

func TestNewIngesterRejectsMissingDependencies(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(d *IngesterDependencies)
	}{
		{"projector", func(d *IngesterDependencies) { d.Projector = nil }},
		{"repairs", func(d *IngesterDependencies) { d.Repairs = nil }},
		{"logger", func(d *IngesterDependencies) { d.Logger = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := IngesterDependencies{Projector: scriptedProjector{}, Repairs: &scriptedRepairs{}, Logger: discardLogger()}
			tc.mutate(&deps)
			if _, err := NewIngester(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewIngester error = %v, want missing dependency", err)
			}
		})
	}
}
