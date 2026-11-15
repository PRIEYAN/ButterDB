package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/ingest/put"
	"github.com/PRIEYAN/ButterDB/internal/ingest/remove"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/ingest/sweeper"
	projlog "github.com/PRIEYAN/ButterDB/internal/projection/log"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

var (
	_ put.JobStore           = (*uploadJobs)(nil)
	_ put.EncryptionKeys     = (*encryptionKeys)(nil)
	_ put.DEKUnwrapper       = (*dekUnwrapper)(nil)
	_ put.PeerResolver       = (*writePeers)(nil)
	_ put.OpProjector        = (*projlog.Projector)(nil)
	_ put.BucketAuthority    = (*buckets.Service)(nil)
	_ put.WriteGate          = (*buckets.WriteGates)(nil)
	_ put.Admission          = (*admission.Controller)(nil)
	_ put.ObjectReader       = (*objectrepo.Repository)(nil)
	_ put.BucketLookup       = (*bucketrepo.Repository)(nil)
	_ put.SpoolStore         = (*spool.Spool)(nil)
	_ put.MessageReader      = tgclient.ChannelRPC(nil)
	_ put.MessageDeleter     = tgclient.ChannelRPC(nil)
	_ put.BodySender         = tgclient.Sender(nil)
	_ put.ControlSender      = tgclient.Sender(nil)
	_ remove.JobStore        = (*deleteJobs)(nil)
	_ remove.PeerResolver    = (*writePeers)(nil)
	_ put.KeyLocks           = (*put.ObjectKeyLocks)(nil)
	_ remove.KeyLocks        = (*put.ObjectKeyLocks)(nil)
	_ remove.OperationLookup = (*operationLookup)(nil)
	_ remove.Principals      = principalContext{}
	_ remove.Authority       = (*buckets.Service)(nil)
	_ remove.WriteGates      = (*buckets.WriteGates)(nil)
	_ remove.ObjectReader    = (*objectrepo.Repository)(nil)
	_ remove.BucketLookup    = (*bucketrepo.Repository)(nil)
	_ remove.OpProjector     = (*projlog.Projector)(nil)
	_ remove.MessageReader   = tgclient.ChannelRPC(nil)
	_ remove.MessageDeleter  = tgclient.ChannelRPC(nil)
	_ remove.ControlSender   = tgclient.Sender(nil)
	_ buckets.ActiveJobs     = (*activeJobs)(nil)
	_ admission.Pacer        = (*flow.SendLimiter)(nil)
	_ sweeper.JobStore       = (*jobrepo.Repository)(nil)
	_ sweeper.UploadResumer  = (*put.Putter)(nil)
	_ sweeper.DeleteResumer  = (*remove.Deleter)(nil)
	_ sweeper.DueCleanup     = (*objectrepo.CleanupQueue)(nil)
	_ sweeper.OrphanBodies   = (*objectrepo.CleanupQueue)(nil)
	_ multipartstage.Index   = (*jobrepo.MultipartIndex)(nil)
	_ multipartstage.Putter  = (*put.Putter)(nil)
)

type uploadJobs struct {
	repo *jobrepo.Repository
}

func (j *uploadJobs) CreateJob(ctx context.Context, job jobrepo.UploadJob, idempotency *jobrepo.IdempotencyRecord) error {
	if idempotency == nil {
		return j.repo.CreateUpload(ctx, job)
	}
	return j.repo.CreateUploadWithIdempotency(ctx, job, *idempotency)
}

func (j *uploadJobs) GetJob(ctx context.Context, jobID string) (jobrepo.UploadJob, error) {
	return j.repo.GetUpload(ctx, jobID)
}

func (j *uploadJobs) TransitionJob(ctx context.Context, jobID string, from, to jobrepo.UploadState, patch jobrepo.UploadPatch) error {
	return j.repo.TransitionUpload(ctx, jobID, from, to, patch)
}

func (j *uploadJobs) LookupIdempotency(ctx context.Context, scope, idempotencyKey string) (jobrepo.IdempotencyRecord, bool, error) {
	record, err := j.repo.GetIdempotency(ctx, scope, idempotencyKey)
	if errors.Is(err, jobrepo.ErrIdempotencyNotFound) {
		return jobrepo.IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return jobrepo.IdempotencyRecord{}, false, err
	}
	return record, true, nil
}

type deleteJobs struct {
	repo *jobrepo.Repository
}

func (j *deleteJobs) CreateDeleteJob(ctx context.Context, job jobrepo.DeleteJob) error {
	return j.repo.CreateDelete(ctx, job)
}

func (j *deleteJobs) DeleteJob(ctx context.Context, jobID string) (jobrepo.DeleteJob, error) {
	return j.repo.GetDelete(ctx, jobID)
}

func (j *deleteJobs) TransitionDeleteJob(ctx context.Context, jobID string, from, to jobrepo.DeleteState, patch jobrepo.DeletePatch, _ int64) error {
	return j.repo.TransitionDelete(ctx, jobID, from, to, patch)
}

type encryptionKeys struct {
	repo *bucketrepo.Repository
}

func (k *encryptionKeys) ActiveKey(ctx context.Context, channelID int64, kekFingerprint string) (put.EncryptionKey, bool, error) {
	key, found, err := k.repo.ActiveKey(ctx, channelID, kekFingerprint)
	return putKey(key, found, err)
}

func (k *encryptionKeys) Key(ctx context.Context, channelID int64, keyID string) (put.EncryptionKey, bool, error) {
	key, found, err := k.repo.Key(ctx, channelID, keyID)
	return putKey(key, found, err)
}

func putKey(key bucketrepo.EncryptionKey, found bool, err error) (put.EncryptionKey, bool, error) {
	if err != nil || !found {
		return put.EncryptionKey{}, false, err
	}
	return put.EncryptionKey{KeyID: key.KeyID, WrappedDEK: key.WrappedDEK, KeyCheck: key.KeyCheck}, true, nil
}

type dekUnwrapper struct {
	ring *keyring.Ring
}

func (u *dekUnwrapper) UnwrapDEK(wrap []byte, check []byte, bucketChannelID int64, kid string) (put.DEK, error) {
	key, err := u.ring.UnwrapDEK(wrap, check, bucketChannelID, kid)
	if err != nil {
		return nil, err
	}
	return key, nil
}

type writePeers struct {
	peers *channelPeers
}

func (p *writePeers) ResolveChannel(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	return p.peers.ResolvePeer(ctx, channelID)
}

func (p *writePeers) PeerFor(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	return p.peers.ResolvePeer(ctx, channelID)
}

const selectProjectedOperation = `SELECT msg_id FROM projection_operations WHERE channel_id = ?1 AND op_id = ?2 AND op_type = ?3`

type operationLookup struct {
	db databaseReader
}

func (o *operationLookup) LookupOperation(ctx context.Context, channelID int64, opID string, opType wire.OpType) (int64, bool, error) {
	var msgID int64
	found := true
	err := o.db.Read(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, selectProjectedOperation, channelID, opID, string(opType)).Scan(&msgID)
		if errors.Is(err, sql.ErrNoRows) {
			found = false
			return nil
		}
		return err
	})
	if err != nil {
		return 0, false, fmt.Errorf("core: look up %s operation %s: %w", opType, opID, err)
	}
	return msgID, found, nil
}

const selectActiveJobs = `SELECT EXISTS (SELECT 1 FROM upload_jobs WHERE bucket_channel_id = ?1 AND state NOT IN ('committed','aborted'))
  OR EXISTS (SELECT 1 FROM delete_jobs WHERE bucket_channel_id = ?1 AND state NOT IN ('committed','aborted'))`

type activeJobs struct {
	db databaseReader
}

func (a *activeJobs) HasActiveJobs(ctx context.Context, bucketChannelID int64) (bool, error) {
	var active bool
	err := a.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, selectActiveJobs, bucketChannelID).Scan(&active)
	})
	if err != nil {
		return false, fmt.Errorf("core: check active jobs of bucket channel %d: %w", bucketChannelID, err)
	}
	return active, nil
}
