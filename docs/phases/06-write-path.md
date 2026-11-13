# Phase 06 — Write path
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement write path as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 05 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/storage-model.md`.
- `docs/spec/s3-api.md`.
- `docs/spec/security.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit the shared write service contract in `internal/ingest/put/contracts.go`
before fan-out. `object.PutRequest` is committed in phase 02 to match the
admin API's `ObjectWriter` port. The `io.Reader` is consumed into a private
spool file before Telegram I/O. `ContentLength` is plaintext length; admission checks the
stored length after TDE1 expansion. `IdempotencyKey` is scoped to the caller's
access key (E9). A successful `Put` returns only after the Telegram put control
message has been projected (D8).

```go
package put

import (
	"context"
	"io"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

type Service interface {
	Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error)
}
```

`internal/ingest/remove` exposes `Delete(ctx context.Context,
name bucket.BucketName, key object.Key) error`. `internal/ingest/multipartstage`
exposes create, add-part, complete, abort, and list operations with the
request and response fields in `docs/spec/s3-api.md` §9. The control database
schema in `docs/spec/schema.md` is the contract for spool, job, and multipart
state; no lane may add a separate journal. Share the durable server-minted
job ID across spool, put, sweeper, and multipart lanes. A retry must reuse the
same body and put random IDs. Commit as `define write path contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/06-write-path-l1` | internal/ingest/spool |
| L2 | `phase/06-write-path-l2` | internal/ingest/put |
| L3 | `phase/06-write-path-l3` | internal/ingest/remove |
| L4 | `phase/06-write-path-l4` | internal/ingest/admission (send-rate limiter, flood-wait budget → SlowDown mapping, process-wide slots) |
| L5 | `phase/06-write-path-l5` | internal/storage/jobrepo + internal/ingest/sweeper (uncertain sends, pending cleanup, control migrations) |
| L6 | `phase/06-write-path-l6` | internal/ingest/multipartstage (S3 multipart part storage and assembly into the spool) |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — internal/ingest/spool

**Objective.** Stream an upload body to a private spool file, fsync it, and compute MD5, SHA-256 and the size while enforcing the size cap, so the put service sends from a durable, seekable local copy.

**Owned paths.** `internal/ingest/spool/`.

**Read.** `docs/spec/storage-model.md` §6.2–§6.3 (commit protocol, uncertain outcomes), §7 (limits), §8.5 (E8); `docs/spec/operations.md` §3.1 (`spool/` layout and modes); `docs/decisions.md` A4 (bytes stored exactly as uploaded).

```go
package spool

type File struct {
	Path   string
	Size   int64
	MD5    [16]byte
	SHA256 [32]byte
}

func New(dir string, fs fsx.FS, clock clock.Clock) (*Spool, error)
func (s *Spool) Write(ctx context.Context, jobID string, body io.Reader, declared int64, maxStored int64) (File, error)
func (s *Spool) Open(f File) (io.ReadSeekCloser, error)
func (s *Spool) Remove(jobID string) error
```

**Rules.**
- Write to `spool/<job id>.plain` (mode `0600`) through a temp file and a rename after `fsync`. Reject before writing when the declared length exceeds the cap. Stop reading the moment the actual length exceeds the declared one.
- Store bytes exactly as received (A4). Never normalise or strip metadata here.
- On error the temp file is removed. Space reservation and accounting use the configured spool budget.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/mountwrite/staging.go` | 555 | PORT-PARTIAL | `internal/ingest/spool` (L1) | Keep: `DiskStagingStore` streaming a body to a private file while hashing, `exactLengthReader` (length mismatch and trailing data rejected), per-object and aggregate byte quotas, slot semaphore with cancellable wait, startup accounting of existing files, partial-file cleanup on every failure, safe keys from operation IDs, private modes. Change: hash MD5 (S3 ETag) and SHA-256 together; spool plaintext and encrypt during send through `crypto/stream.EncryptingReaderAt`. Drop `copyEncryptedToStage` |
| `backend/services/file/service.go` (`acquireUploadSlot`, `uploadConcurrency`, `retryVisibleSend`, `stageUploadPart`, `createTempWithFallback`) | 797 | PATTERN-ONLY | `internal/ingest/admission` (L4), `internal/ingest/spool` (L1) | Process-wide upload slots; `retryVisibleSend` stops retrying once the outcome is unknown. SlowDown mapping from the flood-wait budget (A7) is new |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/mountwrite/staging.go`: `type DiskStagingConfig struct {`; `type DiskStagingStore struct {`; `func NewDiskStagingStore(config DiskStagingConfig) (*DiskStagingStore, error) {`; `func (s *DiskStagingStore) Root() string {`; `func (s *DiskStagingStore) UsedBytes() int64 {`; `func (s *DiskStagingStore) Stage(ctx context.Context, request StageRequest, source io.Reader) (StagedObject, error) {`; `func (s *DiskStagingStore) Open(staged StagedObject) (ReadSeekCloser, error) {`; `func (s *DiskStagingStore) Remove(ctx context.Context, staged StagedObject) error {`
- `backend/services/file/service.go`: `type PeerResolver interface {`; `type EventSink interface {`; `type Service struct {`; `type Metadata struct {`; `type DownloadResult struct {`; `type PreviewPayload struct {`; `func (r *contextReader) Read(p []byte) (int, error) {`; `func WithDownloadProgressID(ctx context.Context, requestID string) context.Context {`

**Tests (binding names).** the carried `staging_test.go` and `locks_test.go` cases, plus `TestSpoolHashesMatch`, `TestDeclaredLengthEnforced`, `TestFsyncBeforeRename`, `TestTempRemovedOnError`, `TestBytesUnchanged`.

**Verify.**

```bash
go vet -mod=readonly ./internal/ingest/spool/...
go test -mod=readonly -race ./internal/ingest/spool/...
gofmt -l internal/ingest/spool
```

**Commits.** `add upload spool with hashing and fsync`.

### 5.2 Lane L2 — internal/ingest/put

**Objective.** Run the object write path behind `put.Service`: admission, spool, optional encryption with persisted salt and nonce prefix, the body document send, the `t=put` commit, projection, and cleanup of superseded messages. Answer only after the commit is projected (D8).

**Owned paths.** `internal/ingest/put/` (implementation; `contracts.go` is the prelude's).

**Read.** `docs/spec/storage-model.md` §6.1–§6.6, §8.4–§8.5; `docs/spec/schema.md` §5.5 (`upload_jobs` state machine), §5.7 (`idempotency_keys`); `docs/spec/s3-api.md` §8.1 (PutObject), §8.10 (encryption headers), §12.1 (budget); `docs/decisions.md` D8, A5, A7, E8, E9.

**Rules.**
- Persist the job row (including, when encrypted, the file salt and nonce prefix) **before** the first send. Retries reuse the job ID-derived random IDs and the same salt and prefix. Re-verify the spool SHA-256 before each retry.
- Set `put_attempted` before the `t=put` send. An unknown outcome is reconciled through receipts and history, never resent with new identity.
- Conditional puts (`IfMatch`, `IfNoneMatchAny`) are checked authoritatively at commit.
- Telegram flood waits beyond the request budget return `SlowDown` through `admission`. No SQLite transaction is held across a send.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/services/file/hidden_upload.go` | 585 | PORT-PARTIAL | `internal/ingest/put` (L2) | Keep: send the hidden body with `random_id = StableRandomID(jobID, step)`; validate metadata and source size before any remote call; on `ErrSendOutcomeUnknown` resend with the same `random_id` so Telegram returns the original message; persist a recovered receipt before cleanup; `discardHiddenBody` when the commit never happens; refuse a receipt whose message ID is already owned. Drop: the part loop, `partAttachmentName` part suffixes, `hiddenUploadUUID`, `PartsForUUIDContext` |
| `backend/core/engine.go` (`EmitAndProjectContext`, `sendControlWithRetry`, `controlPeer`) | 932 | PORT-PARTIAL | `internal/ingest/put` (L2) | Send the control message, then `ProjectFromOp` its receipt; a projection error after acceptance returns the positive message ID wrapped in `ErrControlProjection` and is never resent. Change: `random_id` from the job ID (E9) |
| `backend/mountwrite/locks.go` | 112 | PORT | `internal/ingest/put` (L2) | `KeyedLocker`: serialize writers of one `(bucket, key)`, sorted and deduplicated multi-key acquisition, entries cleaned up, cancellable wait |
| `backend/mountadapter/remote.go` | 759 | PATTERN-ONLY | `internal/ingest/put` (L2) | Upload hidden, then commit; an unknown outcome or a hidden receipt needing recovery is surfaced as "recovery required", never as failure |
| `backend/mountwrite/coordinator.go` | 452 | PATTERN-ONLY | `internal/ingest/put` (L2) | `createOrLoad` keyed by operation ID so a retried request resumes, `withOperation`, `transition`, `markAborted`, `deferCleanup` |
| `backend/services/file/upload_failures.go` | 126 | PATTERN-ONLY | `internal/ingest/put` (L2) | `classifyUploadFailure`: not sent, uncertain and canceled are distinct outcomes; phase 08 maps them to S3 errors |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/services/file/hidden_upload.go`: `type HiddenUploadRequest struct {`; `type HiddenBody struct {`; `func (s *Service) UploadHidden(ctx context.Context, channelID int64, request HiddenUploadRequest, source io.ReadSeeker) (HiddenBody, error) {`; `func (s *Service) RecoverHiddenUpload(`; `func (s *Service) RecoverAndDiscardHiddenUpload(`; `func (s *Service) DiscardHiddenReceipt(`; `func (s *Service) DiscardHiddenOperation(ctx context.Context, channelID int64, operationID string) error {`
- `backend/core/engine.go`: `type EventSink interface {`; `type Config struct {`; `type Engine struct {`; `func New(ctx context.Context, cfg Config) (*Engine, error) {`; `type DriveScanProgressEvent struct {`; `func (e *Engine) PauseLiveSync() {`; `func (e *Engine) ResumeLiveSync() {`; `func (e *Engine) Close() {`
- `backend/mountwrite/locks.go`: `type KeyedLocker struct {`; `func NewKeyedLocker() *KeyedLocker {`; `func (l *KeyedLocker) Lock(ctx context.Context, keys ...string) (func(), error) {`; `func (l *KeyedLocker) Len() int {`
- `backend/mountadapter/remote.go`: `type HiddenStore interface {`; `type TelegramRemoteConfig struct {`; `type TelegramRemote struct {`; `func NewTelegramRemote(config TelegramRemoteConfig) (*TelegramRemote, error) {`; `func (remote *TelegramRemote) UploadHidden(ctx context.Context, request mountwrite.HiddenUpload, source io.ReadSeeker) (mountwrite.RemoteBody, error) {`; `func (remote *TelegramRemote) RecoverHidden(ctx context.Context, request mountwrite.HiddenUpload, source io.ReadSeeker) (mountwrite.RemoteBody, error) {`; `func (remote *TelegramRemote) DiscardHidden(ctx context.Context, operationID string, body *mountwrite.RemoteBody) error {`; `func (remote *TelegramRemote) Commit(ctx context.Context, request mountwrite.CommitRequest) (mountwrite.MutationResult, error) {`
- `backend/mountwrite/coordinator.go`: `type CoordinatorConfig struct {`; `type Coordinator struct {`; `func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {`; `type UUIDGenerator struct{}`; `func (UUIDGenerator) NewID() string {`; `func (c *Coordinator) Status() Status {`; `func (c *Coordinator) Drain(ctx context.Context) error {`; `func (c *Coordinator) Close(ctx context.Context) error {`
- `backend/services/file/upload_failures.go`: `type UploadFailure struct {`; `func NotSentFailures(filePaths []string, err error) []UploadFailure {`; `func (s *Service) UploadWithFailures(ctx context.Context, channelID int64, filePaths, parentIDs []string, encrypt bool) ([]Metadata, []UploadFailure, error) {`; `func (o *failureUploadObserver) Started(id int, name string, size int64, parentID string) {`; `func (o *failureUploadObserver) Accepted(id int) {`; `func (o *failureUploadObserver) Completed(id int, name string) {`; `func (o *failureUploadObserver) Failed(id int, name string, err error) {`

**Tests (binding names).** the carried `hidden_upload_test.go`, `hidden_upload_recovery_test.go`, `upload_retry_test.go` and `engine_write_test.go` cases, plus `TestPutAcknowledgedAfterProjection`, `TestEncryptedRetryReusesSaltAndNonce`, `TestSpoolHashMismatchFailsJob`, `TestOverwriteQueuesSupersededCleanup`, `TestIfNoneMatchAtCommit`, `TestFloodWaitPastBudgetSlowDown`, `TestIdempotentReplayReturnsSameObject`.

**Verify.**

```bash
go vet -mod=readonly ./internal/ingest/put/...
go test -mod=readonly -race ./internal/ingest/put/...
gofmt -l internal/ingest/put
```

**Commits.** `add put job journal and body send`, `add put commit with projection and superseded cleanup`, `add encrypted put with persisted parameters`.

### 5.3 Lane L3 — internal/ingest/remove

**Objective.** Hard-delete objects: publish the `t=del` tombstone first, project it, then delete the object's Telegram messages in batches of 100, recovering partial batches through the delete job journal.

**Owned paths.** `internal/ingest/remove/`.

**Read.** `docs/spec/storage-model.md` §6.5 (tombstone then batches), §13; `docs/spec/schema.md` §5.6 (`delete_jobs`); `docs/spec/s3-api.md` §8.4–§8.5 (DeleteObject, DeleteObjects); `docs/decisions.md` D10, E4.

**Rules.**
- The tombstone is projected before any message is deleted. An unknown tombstone outcome sets `uncertain_del` and answers `SlowDown`.
- Deleting a missing key succeeds (`204` semantics). Batches are at most 100 IDs, and a failed batch resumes from its checkpoint.
- The Telegram deletion is irreversible, so nothing is deleted without a persisted plan.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/services/file/upload.go` `deleteMessagesChunked` | (in 777) | PORT-PARTIAL | `internal/ingest/remove` (L3) | Chunks of 100, each under the write retry policy, continue after a failed chunk, return the first error |
| `backend/mountwrite/hard_delete.go`, `journal_hard_delete.go` | 184, 303 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/storage/jobrepo` (L5) | Durable plan of message IDs appended once, then `NextHardDeleteBatch` / `MarkHardDeleteBatchDone` so a crash resumes mid-delete. butterDB writes the `t=del` tombstone before the first batch (E4) |
| `backend/projection/hard_delete.go`, `hard_delete_intent.go` | 660, 179 | PATTERN-ONLY | `internal/ingest/remove` (L3) | Plan pages (`maxHardDeletePlanPageSize = 1000`), plan validation that refuses control messages and bodies referenced elsewhere |
| `backend/services/file/mutations.go` (`Delete`, `purgeObject`, `deletePlannedBodies`, `SweepOrphanParts`) | 599 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/ingest/sweeper` (L5) | Purge claims a durable plan, deletes bodies, then completes. `SweepOrphanParts` becomes the sweeper's orphan-body pass using `objectrepo.ManagedMsgIDsIn` |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/services/file/upload.go`: `func (s *Service) Upload(ctx context.Context, channelID int64, filePaths []string, parentIDs []string, encrypt bool) ([]Metadata, error) {`; `func (s *Service) CancelUpload(uploadID int) bool {`
- `backend/mountwrite/hard_delete.go`: `func (c *Coordinator) HardDelete(ctx context.Context, request HardDeleteRequest) (MutationResult, error) {`
- `backend/projection/hard_delete.go`: `func ValidateHardDeletePlanItems(`; `func HardDeletePlanPage(ctx context.Context, db *sql.DB, channelID int64, opID string, afterMsgID int64, limit int) (ids []int64, total int64, done bool, err error) {`; `func CompleteHardDeletePlan(ctx context.Context, db *sql.DB, channelID int64, opID string) (err error) {`
- `backend/services/file/mutations.go`: `func (s *Service) Meta(channelID int64, msgID int, name string, size int64, parentID string) error {`; `func (s *Service) MetaContext(ctx context.Context, channelID int64, msgID int, name string, size int64, parentID string) error {`; `func (s *Service) Rename(ctx context.Context, channelID int64, msgID int, newName string) (err error) {`; `func (s *Service) Move(ctx context.Context, channelID int64, msgID int, newParentID string) (err error) {`; `func (s *Service) Delete(ctx context.Context, channelID int64, msgID int) (err error) {`; `func (s *Service) TrashObject(ctx context.Context, channelID int64, objectID string) error {`; `func (s *Service) RestoreObject(ctx context.Context, channelID int64, objectID string) error {`; `type TrashListing struct {`

**Tests (binding names).** the carried hard-delete pattern cases (batch resume after a failed batch), plus `TestTombstoneBeforeDelete`, `TestMissingKeySucceeds`, `TestBatchesOfHundred`, `TestUncertainTombstoneSlowDown`, `TestResumeAfterCrashMidBatch`.

**Verify.**

```bash
go vet -mod=readonly ./internal/ingest/remove/...
go test -mod=readonly -race ./internal/ingest/remove/...
gofmt -l internal/ingest/remove
```

**Commits.** `add hard delete with tombstone and batched message deletion`.

### 5.4 Lane L4 — internal/ingest/admission (send-rate limiter, flood-wait budget → SlowDown mapping, process-wide slots)

**Objective.** Gate every write before it touches disk or Telegram: process-wide put slots, budget-aware waiting on the E10 send-rate limiter from `tgclient/flow`, and the A7 request budget that turns long waits into `503 SlowDown`.

**Owned paths.** `internal/ingest/admission/`.

**Read.** `docs/decisions.md` A7 and E10; `docs/spec/operations.md` §2 keys `ingest.max_concurrent_puts`, `ingest.max_concurrent_puts_per_key`, `telegram.send_rate.*`, `s3.request_budget`, and §13.3; `docs/spec/s3-api.md` §12.1. TDrive pattern only: `/home/prieyan/weeb/TDrive/backend/services/file/service.go` (`acquireUploadSlot`, `uploadConcurrency`, `retryVisibleSend`).

**Contracts.**

```go
package admission

type Budget struct {
	Deadline time.Time
}

func NewBudget(now time.Time, total time.Duration) Budget
func (b Budget) Remaining(now time.Time) time.Duration

type SlowDownError struct {
	RetryAfter time.Duration
	Reason     string
}

func (e *SlowDownError) Error() string
func (e *SlowDownError) Unwrap() error

type Config struct {
	MaxConcurrentPuts       int
	MaxConcurrentPutsPerKey int
}

type Pacer interface {
	Reserve(class flow.SendClass) (delay time.Duration, cancel func())
}

type Dependencies struct {
	Config Config
	Pacer  Pacer
	Clock  clock.Clock
}

func New(deps Dependencies) (*Controller, error)
func (c *Controller) AcquirePut(ctx context.Context, b Budget, bucketChannelID int64, key object.Key) (release func(), err error)
func (c *Controller) WaitSend(ctx context.Context, b Budget, class flow.SendClass) error
func (c *Controller) WaitFlood(ctx context.Context, b Budget, wait time.Duration) error
```

`Pacer` is satisfied by the E10 send-rate limiter in `internal/tgclient/flow` (phase 04 L2), which owns the token buckets. This lane owns no rate limiter of its own, so every send is limited exactly once.

`(*SlowDownError).Unwrap` returns `errs.ErrSlowDown`, so `s3err.Map` and the admin error table map it without importing this package.

**Rules.**
- Slots are FIFO (a fair semaphore), shared by S3 and admin uploads. The per-key limit stops one hot key from taking every slot.
- `WaitSend` calls `Pacer.Reserve`. If the delay ends after `Budget.Deadline`, it calls `cancel` (returning the token) and returns `*SlowDownError`. Otherwise it waits out the delay. Server-directed flood waits are an additional floor applied through `WaitFlood`.
- If a wait would end after `Budget.Deadline`, return `*SlowDownError` immediately, with `RetryAfter` equal to the wait rounded up to whole seconds (minimum 1). Never sleep past the budget.
- Every waiter honours `ctx` cancellation and returns `ctx.Err()`, not `SlowDown`.
- Transfer time is outside the budget. Only waiting counts (A7).
- No goroutine outlives a call. All time comes from the injected clock.

**Tests (binding names).** `TestSlotsFIFO`, `TestPerKeyLimit`, `TestSlotReleaseOnPanicPath`, `TestBudgetExhaustedReturnsSlowDown`, `TestRetryAfterRoundedUpMinOne`, `TestSendReservationCancelledOnSlowDown`, `TestFloodWaitBeyondBudget`, `TestFloodWaitWithinBudgetSleeps`, `TestCancellationNotSlowDown`, `TestSlowDownUnwrapsToSentinel`. Use a fake clock; no real sleeps.

**Verify.**

```bash
go test -mod=readonly -race ./internal/ingest/admission/...
gofmt -l internal/ingest/admission
```

**Commits.** `add write admission slots and request budget`, `add budget aware send pacing`.

### 5.5 Lane L5 — internal/storage/jobrepo + internal/ingest/sweeper (uncertain sends, pending cleanup, control migrations)

**Objective.** Write the job repository over `upload_jobs`, `delete_jobs` and `idempotency_keys`, and the sweeper that finishes uncertain sends, retries pending message cleanup, and expires idempotency rows.

**Owned paths.** `internal/storage/jobrepo/` (except `multipart.go` and `multipart_test.go`, which belong to L6), `internal/ingest/sweeper/`.

**Read.** `docs/spec/schema.md` §5.5–§5.7 (state machines and checks), §8 (retention), §9 (`storage/jobrepo` rows); `docs/spec/storage-model.md` §6.3; `docs/spec/security.md` §16 (`TestIdempotencyScopedPerAccessKey`).

**Rules.**
- Transitions are compare-and-swap on the prior state. A stale transition affects no row and returns a typed error.
- Idempotency is scoped per access key or per device (E9).
- The sweeper claims rows with backoff (1 min doubling to 1 h), never holds a transaction across a Telegram call, and only deletes messages whose ownership is recorded.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/mountwrite/hard_delete.go`, `journal_hard_delete.go` | 184, 303 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/storage/jobrepo` (L5) | Durable plan of message IDs appended once, then `NextHardDeleteBatch` / `MarkHardDeleteBatchDone` so a crash resumes mid-delete. butterDB writes the `t=del` tombstone before the first batch (E4) |
| `backend/services/file/mutations.go` (`Delete`, `purgeObject`, `deletePlannedBodies`, `SweepOrphanParts`) | 599 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/ingest/sweeper` (L5) | Purge claims a durable plan, deletes bodies, then completes. `SweepOrphanParts` becomes the sweeper's orphan-body pass using `objectrepo.ManagedMsgIDsIn` |
| `backend/mountwrite/journal.go` | 482 | PATTERN-ONLY | `internal/storage/jobrepo` (L5) | `SQLiteJournal`: `Create`, `Get`, compare-and-swap `Transition`, `ListRecoverable` in stable order, typed uniqueness-violation detection, corrupt-row rejection. butterDB's job rows also persist file salt and nonce prefix before the first send (E8) |
| `backend/mountwrite/types.go` | 618 | PATTERN-ONLY | `internal/storage/jobrepo` (L5) | `JournalState` with an explicit `allowedTransitions` table and `ValidateTransition`. TDrive states: `receiving`, `staged`, `uploading`, `uploaded`, `committing`, `reconciling`, `remote_committed`, `projection_pending`, `cleanup_pending`, `delete_plan_pending`, `deleting_bodies`, `delete_finalizing`, `done`, `aborted`. butterDB's job states are defined by the phase 06 document |
| `backend/mountwrite/recovery.go` | 188 | PATTERN-ONLY | `internal/ingest/sweeper` (L5) | `Recover` walks recoverable records: reconcile an uploading receipt before removing staged bytes, retry a committing operation idempotently, finish cleanup |
| `backend/projection/multipart.go` (`QueuePartCleanup`, `PendingPartCleanup`, `ClearPartCleanup`) | (in 332) | PATTERN-ONLY | `internal/ingest/sweeper` (L5) | A durable cleanup queue of message IDs. The rest of the file is cut |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/mountwrite/hard_delete.go`: `func (c *Coordinator) HardDelete(ctx context.Context, request HardDeleteRequest) (MutationResult, error) {`
- `backend/services/file/mutations.go`: `func (s *Service) Meta(channelID int64, msgID int, name string, size int64, parentID string) error {`; `func (s *Service) MetaContext(ctx context.Context, channelID int64, msgID int, name string, size int64, parentID string) error {`; `func (s *Service) Rename(ctx context.Context, channelID int64, msgID int, newName string) (err error) {`; `func (s *Service) Move(ctx context.Context, channelID int64, msgID int, newParentID string) (err error) {`; `func (s *Service) Delete(ctx context.Context, channelID int64, msgID int) (err error) {`; `func (s *Service) TrashObject(ctx context.Context, channelID int64, objectID string) error {`; `func (s *Service) RestoreObject(ctx context.Context, channelID int64, objectID string) error {`; `type TrashListing struct {`
- `backend/mountwrite/journal.go`: `type JournalRecord struct {`; `type JournalPatch struct {`; `type Journal interface {`; `type HardDeletePlanStatus struct {`; `type HardDeleteJournal interface {`; `type SQLiteJournal struct {`; `func EnsureJournalSchema(ctx context.Context, db *sql.DB) error {`; `func NewSQLiteJournal(db *sql.DB) (*SQLiteJournal, error) {`
- `backend/mountwrite/types.go`: `type Mutation struct {`; `func (m Mutation) AffectedParents() []string {`; `func (m Mutation) AffectedObjects(result MutationResult) []string {`; `type PutRequest struct {`; `func (r PutRequest) Validate() error {`; `type MkdirRequest struct {`; `func (r MkdirRequest) Validate() error {`; `type MoveRequest struct {`
- `backend/mountwrite/recovery.go`: `type RecoveryReport struct {`; `func (c *Coordinator) Recover(ctx context.Context) (RecoveryReport, error) {`
- `backend/projection/multipart.go`: `type FilePart struct {`; `func MultipartParts(db *sql.DB, channelID int64, fileMsgID int64) ([]FilePart, error) {`; `func MultipartPartsContext(ctx context.Context, db *sql.DB, channelID int64, fileMsgID int64) ([]FilePart, error) {`; `func PartsForUUID(db *sql.DB, channelID int64, uuid string) ([]FilePart, error) {`; `func PartsForUUIDContext(ctx context.Context, db *sql.DB, channelID int64, uuid string) ([]FilePart, error) {`; `func OrphanPartMessages(db *sql.DB, channelID int64) ([]int64, error) {`; `func DeleteFileParts(db *sql.DB, channelID int64, uuid string) error {`; `func DeleteFilePartsByMsgIDs(db *sql.DB, channelID int64, msgIDs []int64) error {`

**Tests (binding names).** every schema §9 `storage/jobrepo` row (`TestTransitionCAS`, `TestEncryptedJobNeedsSaltAndNonce`, `TestIdempotencyScoped`, `TestNoTxAcrossNetwork`), plus `TestIdempotencyScopedPerAccessKey` (security §16) and the carried journal and recovery pattern cases, plus `TestSweeperFinishesUncertainPut`, `TestSweeperRetriesPendingCleanupWithBackoff`, `TestIdempotencyRowsExpire`.

**Verify.**

```bash
go vet -mod=readonly ./internal/storage/jobrepo/... ./internal/ingest/sweeper/...
go test -mod=readonly -race ./internal/storage/jobrepo/... ./internal/ingest/sweeper/...
gofmt -l internal/storage/jobrepo internal/ingest/sweeper
```

**Commits.** `add job repository with compare and swap transitions`, `add write path sweeper`.

### 5.6 Lane L6 — internal/ingest/multipartstage (S3 multipart part storage and assembly into the spool)

**Objective.** Stage S3 multipart parts on local disk, keep the multipart index in `control.db`, and on Complete present the listed parts as one body to `internal/ingest/put`. Nothing reaches Telegram before Complete (D9).

**Owned paths.** `internal/ingest/multipartstage/`, `internal/storage/jobrepo/multipart.go` and `internal/storage/jobrepo/multipart_test.go` (new files in the L5 package; L5 owns every other file in `jobrepo`). The `multipart_uploads` and `multipart_parts` tables already exist in `control/0001_init.sql` (phase 02 L4, `docs/spec/schema.md` §5.8); add no migration.

**Read.** `docs/spec/s3-api.md` §9 (limits, states, §9.4 validation and outcome tables, §9.5–§9.7); `docs/spec/storage-model.md` §7.2; `docs/spec/schema.md` `multipart_uploads` and `multipart_parts`; `docs/spec/operations.md` data-directory row `multipart/<upload id>/`; `docs/spec/security.md` test `TestUploadIDValidatedBeforePath`.

**Contracts.**

```go
package multipartstage

type CreateRequest struct {
	Bucket            bucket.BucketName
	BucketChannelID   int64
	Key               object.Key
	ContentType       string
	UserMeta          map[string]string
	SystemMeta        map[string]string
	Encrypted         bool
	ChecksumAlgorithm string
	AccessKeyID       string
}

type Upload struct {
	ID          string
	Bucket      bucket.BucketName
	Key         object.Key
	InitiatedAt time.Time
	State       string
}

type Part struct {
	Number   int
	Size     int64
	ETag     string
	Checksum string
	StagedAt time.Time
}

type CompletedPart struct {
	Number   int
	ETag     string
	Checksum string
}

type Conditions struct {
	IfMatch        string
	IfNoneMatchAny bool
}

type PartPage struct {
	Parts      []Part
	NextMarker int
	Truncated  bool
}

type UploadQuery struct {
	Prefix         string
	Delimiter      string
	KeyMarker      string
	UploadIDMarker string
	Max            int
}

type UploadPage struct {
	Uploads            []Upload
	CommonPrefixes     []string
	NextKeyMarker      string
	NextUploadIDMarker string
	Truncated          bool
}

type Index interface {
	CreateUpload(ctx context.Context, u jobrepo.MultipartUpload) error
	GetUpload(ctx context.Context, id string) (jobrepo.MultipartUpload, error)
	PutPart(ctx context.Context, p jobrepo.MultipartPart) error
	ListParts(ctx context.Context, id string, marker int, max int) ([]jobrepo.MultipartPart, bool, error)
	ListUploads(ctx context.Context, bucketChannelID int64, q UploadQuery) ([]jobrepo.MultipartUpload, bool, error)
	Transition(ctx context.Context, id string, from, to string, patch jobrepo.MultipartPatch) error
	Delete(ctx context.Context, id string) error
	IdleBefore(ctx context.Context, t time.Time, limit int) ([]string, error)
}

type Putter interface {
	Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error)
}

type Dependencies struct {
	Index   Index
	Putter  Putter
	Dir     string
	Clock   clock.Clock
	Random  io.Reader
	FS      fsx.FS
}

func New(deps Dependencies) (*Stage, error)
func (s *Stage) Create(ctx context.Context, req CreateRequest) (Upload, error)
func (s *Stage) PutPart(ctx context.Context, uploadID string, partNumber int, size int64, body io.Reader) (Part, error)
func (s *Stage) Complete(ctx context.Context, uploadID string, parts []CompletedPart, cond Conditions) (object.ObjectMeta, error)
func (s *Stage) Abort(ctx context.Context, uploadID string) error
func (s *Stage) ListParts(ctx context.Context, uploadID string, marker int, max int) (PartPage, error)
func (s *Stage) ListUploads(ctx context.Context, bucketChannelID int64, q UploadQuery) (UploadPage, error)
func (s *Stage) SweepIdle(ctx context.Context, idle time.Duration) (int, error)
```

**Rules.**
- The upload ID is 32 lowercase hex characters from `crypto/rand`. Validate the format **before** building any path, so a crafted ID cannot escape the directory: return `errs.ErrNoSuchUpload`.
- Part files are `multipart/<upload id>/part-<NNNNN>` (mode `0600`, directory `0700`), written to a temp file and renamed after `fsync`. Compute the MD5 ETag while streaming. A re-uploaded part number replaces the earlier file atomically.
- Check the running stored-size total (through `format.CiphertextSize` when encrypted) against `limits.MaxStoredObjectBytes` before reading a part body. Every non-final listed part must be at least 5 MiB at Complete.
- Complete: move `open` to `committing` with the job ID, concatenate the listed parts into one `io.Reader` without copying them into memory, and call `Putter.Put` with the conditions and metadata from `Create`. The multipart ETag is the hex MD5 of the concatenated binary part MD5s plus `-N`. On success the state is `completed` (storing the ETag, put message ID and part-list SHA-256) and part files are removed. On failure follow the s3-api §9.4 outcome table.
- No SQLite transaction may stay open while a part is streamed or while `Put` runs.
- `SweepIdle` aborts `open` uploads idle longer than `s3.multipart_idle_expiry` and removes their files. It never touches `committing`.

**Tests (binding names).** `TestUploadIDValidatedBeforePath`, `TestPartReplaceAtomic`, `TestPartMD5ETag`, `TestRunningTotalCheckedBeforeBody`, `TestEncryptedTotalUsesCiphertextSize`, `TestCompleteConcatenatesListedPartsOnly`, `TestCompleteMultipartETag`, `TestCompleteStateTransitions`, `TestCompleteReplayIdenticalPartList`, `TestAbortRemovesFiles`, `TestAbortCommittingRejected`, `TestSweepIdleSkipsCommitting`, `TestNoTransactionDuringPut` (the fake `Index` fails if `Put` runs inside a transaction).

**Verify.**

```bash
go test -mod=readonly -race ./internal/ingest/multipartstage/... ./internal/storage/jobrepo/...
gofmt -l internal/ingest/multipartstage internal/storage/jobrepo
```

**Commits.** `add multipart upload index repository`, `add multipart part staging`, `add multipart complete assembly and idle sweep`.

## 6. Integration lane

core wiring, test/acceptance/phase06 (put, overwrite cleans superseded, delete hard-deletes, encrypted put, uncertain send reconciliation)
Own `internal/core`, `cmd/`, and `test/acceptance/phase06` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase06/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestPUTAcknowledgesOnlyCommittedTelegramState`: PUT acknowledges only committed Telegram state.
  - `TestOverwriteCleansSupersededMessages`: overwrite cleans superseded messages.
  - `TestDeleteTombstonePreventsResurrection`: delete tombstone prevents resurrection.
  - `TestUncertainSendIsReconciled`: uncertain send is reconciled.
  - `TestMultipartPartsAssembleToOneDocument`: multipart parts assemble to one document.
### TDrive regression tests to adapt

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/mountwrite/staging_test.go` | 357 | Full | All eight |
| `backend/mountwrite/locks_test.go` | 111 | Full | All three |
| `backend/services/file/hidden_upload_test.go` | 1,021 | Partial | `TestUploadHiddenSingleIsNotVisibleBeforeCommit`, `TestUploadHiddenRetryUsesStableTelegramMessage`, `TestUploadHiddenRetriesBoundedFloodWaitAndRewinds`, `TestUploadHiddenValidatesMetadataAndSourceBeforeRemoteAccess`, `TestUploadHiddenRejectsEncryptedPlaintextAboveTDE1Capacity`, the zero and exact-chunk size cases. Drop multipart cases |
| `backend/services/file/hidden_upload_recovery_test.go` | 606 | Partial | `TestRecoverAndDiscardHiddenUploadClosesSendReceiptCrashGap`, `TestRecoverAndDiscardHiddenUploadPersistsRecoveredReceiptBeforeDeleteRetry`, `TestUploadHiddenUnknownSendOutcomeReconcilesCurrentPartBeforeCleanup`, `TestUploadHiddenAcceptedSendWithoutReceiptIsRecoveredBeforeCleanup`, `TestDiscardHiddenReceiptRejectsMessageIDsWithoutProjectedOwnership` |
| `backend/services/file/upload_retry_test.go` | 731 | Partial | `TestSingleUploadRecoversAfterTransientFailure`, `TestUploadFailsWhenTransientBudgetExhausted`, `TestSingleUploadRetriesLostAcceptedReceiptIdempotently`. Drop multipart and manifest cases |
| `backend/core/engine_write_test.go` | 248 | Partial | `TestEmitAndProjectContextRetriesControlWithStableRandomID`, `TestEmitAndProjectBatchMarksPostSendProjectionFailure` |
| `backend/mountwrite/journal_test.go` | 488 | Pattern | Compare-and-swap transition, invalid transition without mutation, recoverable ordering, corrupt row detection, duplicate create |
| `backend/mountwrite/recovery_test.go`, `hidden_receipt_recovery_test.go` | 598, 463 | Pattern | Receipt reconciled before staged bytes are removed; persisted receipt reused; stage retained until cleanup succeeds |
| `backend/mountwrite/hard_delete_test.go`, `journal_hard_delete_test.go` | 256, 141 | Pattern | Batch resume after a failed batch |

## 8. Owner gates

None beyond the entry criteria and any explicit gates recorded in the applicable specification.

## 9. Exit criteria and handoff

All L1–L6 commits have passed monitoring and review; the integration build and phase 06 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 07.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding behavior for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/storage-model.md`

## 6. Objects

### 6.1 Identity

| Concept | Value |
| --- | --- |
| Object address | `(bucket channel ID, key)`. Keys are UTF-8, 1–1024 bytes (`limits.MaxKeyBytes`), compared and ordered as raw bytes |
| Version | The put message ID. One live version per key |
| Body reference | The put's `bm`. `bm = 0` only for an unencrypted zero-byte object, which has no body |
| `LastModified` | The put message's Telegram date (`replay_log.msg_date`), never a local clock (claude.md invariant 10) |
| `ETag` | The put's `et`, quoted on the wire |

A body never creates an object. An object exists only through an applied put
(claude.md invariant 9).

### 6.2 Commit protocol: body then put

The put message is the commit boundary (E2, D8). Steps and job states, owned by
`internal/ingest/put`; `control.db` `upload_jobs` holds the state:

| Step | Action | Job state after |
| --- | --- | --- |
| 1 | Validate key and metadata; `wire.FitsPut` with the classification of wire-format.md §6.3; resolve encryption (section 8.4); compute stored size from the declared length; refuse over `limits.MaxStoredObjectBytes` | none |
| 2 | Spool the body to `spool/<job id>.plain`, computing MD5 and SHA-256 in one pass; fsync | none |
| 3 | Insert the job row with sizes, hashes, metadata, `base_put_msg_id` (the key's current version, 0 if absent), and for encrypted jobs a fresh 16-byte file salt and 20-byte nonce prefix (E8). Commit | `prepared` |
| 4 | Encrypted: seal into `spool/<job id>.sealed` with the persisted salt and prefix | `prepared` |
| 5 | Set state, commit, then `SendFileIdempotent` with caption `TDX1\|t=body\|j=<j>\|sz=<stored>[\|enc=1]` and `StableRandomID(j, "body")` | `body_sending` |
| 6 | Record `body_msg_id`; project the body observation (`object_bodies` row) | `body_sent` |
| 7 | Format the `t=put` text, store it in `put_header`, set state, commit, **then** send with `SendControlIdempotent` and `StableRandomID(j, "put")` | `put_attempted` |
| 8 | Record `put_msg_id`; project through `ProjectFromOp`. **Commit is now durable in Telegram** | `committed` |
| 9 | Respond (D8). Delete superseded messages (section 6.4). Remove spool files | `committed` |

Zero-byte unencrypted objects skip steps 4–6: the put carries `bm=0`, `sz=0`.

Ported rules from legacy section 14:

| Rule | Detail |
| --- | --- |
| An error does not prove a send failed | A send can return a message ID and an error. A non-zero ID means the message exists; it is recorded and never resent |
| Plan by stored size | Every size check uses `format.CiphertextSize` for encrypted objects (claude.md invariant 3) |
| Rewind, never resume | Retries rewind the `io.ReadSeeker` body to offset 0 and reuse the same random ID |
| Two chunkings | One object is one Telegram message of up to 1900 MiB; gotd uploads it in 512 KiB parts (`uploader.MaximumPartSize`), 8 in flight (`UploadThreads`). A transport retry resends one 512 KiB part, not the object |
| Flood waits | Every server-directed `FLOOD_WAIT` is honoured while the request budget lasts; past it the request answers `503 SlowDown` (A7) |
| Process-wide upload slots | `UploadThreads` slots shared FIFO by all jobs |
| Put attempted before send | State `put_attempted` is committed before the put send, never after (claude.md invariant 4) |

### 6.3 Uncertain outcomes and resumption

| Job state | Meaning | Sweeper action |
| --- | --- | --- |
| `prepared` | Nothing sent | After a crash: `aborted`, spool removed |
| `body_sending` | Body may exist | Wait for one completed sync pass of the bucket channel that **started after** `updated_at`; then look up `object_bodies` by `job_id`. Found: `body_sent`. Not found: `aborted` |
| `uncertain_body` | Body send outcome unknown after the budget | Same as `body_sending` |
| `body_sent` | Body exists, no commit | Resume at step 7 only under the version guard below; otherwise `aborted` and the body is queued `orphan_body` |
| `put_attempted`, `uncertain_put` | Commit may exist | Wait for a completed sync pass started after `updated_at`, then check `projection_operations` for `(channel, j, 'put')`. Found: `committed` with that message ID. Not found: resend the stored `put_header` with the same random ID only under the version guard; otherwise `aborted` and the body is queued `orphan_body` |
| `committed`, `aborted` | Terminal | Pruned after 7 days |

**Version guard.** A job resumed after its request has returned (crash, budget
exhaustion, client gone) may send its put, or a delete job its tombstone, only
if the key's projected version still equals the job's `base_put_msg_id` (for
delete jobs, `target_put_msg_id`). Otherwise a newer write committed in between,
and resuming would let stale content win last-writer-wins. The guard is checked
in the same short `projection.db` read that precedes the send; the per-key
write lock (section 6.6) closes the remaining race inside the process.

Orphan bodies: an `object_bodies` row older than 24 hours
(`limits.OrphanBodyGrace`, Default (owner may change)) that no live object
references and no non-terminal job owns is queued for deletion with reason
`orphan_body`. Bodies sent by other writers are covered the same way.

### 6.4 Overwrite: last writer wins

For one key, the put with the highest message ID is the object (E3, claude.md
invariant 13). When a put applies:

| Situation at apply time | Result | Cleanup queued |
| --- | --- | --- |
| No current version, no tombstone above the put | Put becomes the object | none |
| Current version `V` < put `M` | Put replaces `V` | `V`'s put and body, reason `superseded` |
| Current version `V` > put `M` | Put is superseded on arrival; outcome `superseded` | `M`'s put and body, reason `superseded` |
| Tombstone with message ID > `M` | Put is blocked; outcome `superseded` | `M`'s put and body, reason `blocked` |
| A put with the same `j` already applied at another message ID | `duplicate_op` reject | `M`'s put message only, reason `duplicate` (the body is shared with the first put) |
| `M` or its `bm` is in `deleted_messages` | Put is gone: it supersedes older versions but creates no row | older version's messages, reason `superseded` |

Cleanup rows are inserted in the projection transaction that applies the
put. Superseded messages are deleted only after the new put is committed and
projected. No cleanup row is ever queued for a message ID that is the
`put_msg_id` or `body_msg_id` of a live object, and the cleanup worker checks
this again before each batch.

The rules are order-independent: applying the same set of puts and
tombstones in any order yields the same `objects`, `object_tombstones` and
cleanup set. Per-key repair (section 9.4) relies on this and the rebuild
equivalence test proves it.

### 6.5 Delete: tombstone, then batches of 100

`internal/ingest/remove`, with `control.db` `delete_jobs` holding state:

| Step | Action | Job state after |
| --- | --- | --- |
| 1 | Load the key's current version. Missing: `204 No Content`, nothing sent | none |
| 2 | Insert the job with `target_put_msg_id`. Commit | `prepared` |
| 3 | Format `t=del` (key, `pv` = target version, `w`, `ts`), store it in `del_header`, set state, commit, send with `StableRandomID(j, "del")` | `del_attempted` |
| 4 | Project: the tombstone row keeps the highest `del_msg_id`; a current version below it is removed and its put and body are queued with reason `deleted`. **Commit** | `committed` |
| 5 | `channels.deleteMessages` in batches of 100 (`limits.DeleteBatchSize`). Confirmed IDs leave `pending_message_cleanup` and enter `deleted_messages` with source `cleanup` | `committed` |

An unknown outcome at step 3 sets `uncertain_del` and answers `503 SlowDown`;
the sweeper resolves it like `uncertain_put` (section 6.3): found in
`projection_operations` means `committed`, otherwise the stored `del_header` is
resent with the same random ID only under the version guard, else `aborted`.

A failed batch stays queued for `ingest/sweeper`; the response is unchanged.
The tombstone blocks resurrection: any put with a lower message ID for the key,
arriving late or replayed in a rebuild, is blocked (E4, claude.md invariant
14). A put with a higher message ID is unaffected. The tombstone message itself
is kept (plan.md risk R11).

`DeleteObjects` runs the steps per key with one tombstone per key and merges
the message deletions across keys into shared batches of 100.

### 6.6 Concurrency inside the process

| Lock | Scope | Held by |
| --- | --- | --- |
| Bucket write gate | Per bucket channel, read-write | Puts and deletes shared; bucket delete exclusive |
| Key lock | Per `(bucket channel, key)`, exclusive | From the conditional-write check (`If-Match`, `If-None-Match`) through step 8 of 6.2 or step 4 of 6.5. Only covers that window, never the body upload |
| Registry write gate | Registry channel | Bucket create, configure, delete and `t=encfg` |

Two puts to one key from different requests still race in Telegram; the higher
message ID wins. The key lock only makes conditional writes and the version
guard exact within one process.

---

### Contract source: `docs/spec/storage-model.md`

## 7. Size limits

All ceilings live in `internal/domain/limits`.

| Constant | Value | Meaning |
| --- | --- | --- |
| `MaxStoredObjectBytes` | `1900 * 1024 * 1024` = 1,992,294,400 | Largest stored body (D9). Ciphertext counts |
| `MaxEncryptedPlaintextBytes` | 1,991,808,062 | Largest plaintext that fits once sealed |
| `MaxKeyBytes` | 1024 | Key length in UTF-8 bytes |
| `MaxUserMetadataBytes` | 2048 | Sum of `x-amz-meta-*` names and values, S3's limit |
| `MaxSystemHeaderBytes` | 1024 | Each stored system header value (s3-api.md §8.1, wire-format.md §5.2.2) |
| `MaxContentTypeBytes` | 1024 | s3-api.md §8.1 |
| `MaxPutFixedChars` | 512 | Ceiling on the put op without key, content type and metadata; s3-api.md §8.7 relies on it (wire-format.md §6.2) |
| `MaxPutOpChars` | 4096 | Telegram text message limit (wire-format.md §6) |
| `MaxCaptionChars` | 1024 | Telegram caption limit |
| `DeleteBatchSize` | 100 | IDs per `channels.deleteMessages` |
| `MaxBuckets` | 100 | Live buckets per deployment |
| `MaxDeleteObjectsKeys` | 1000 | Keys per `DeleteObjects` |
| `MinMultipartPartBytes` | `5 * 1024 * 1024` | Every S3 part but the last |
| `MaxMultipartParts` | 10000 | S3 part numbers 1–10000 |
| `MaxCacheMaxAgeSeconds` | 31536000 | Bucket `cache_max_age_seconds` ceiling |
| `OrphanBodyGrace` | 24 h | Section 6.3 |
| `IdempotencyTTL` | 24 h | Idempotency record lifetime |

### 7.1 Arithmetic

TDE1 (legacy section 12) for plaintext length `L`, chunk `C = 65536`:

```
CiphertextSize(L) = 50 + L + 16 * (floor(L / 65536) + 1)
```

| Plaintext | Stored |
| ---: | ---: |
| 0 | 66 |
| 65,536 | 65,618 |
| 734,003 | 734,245 |
| 1,991,808,062 | 1,992,294,400 (exactly the ceiling: 30,393 chunks, overhead 486,338 bytes) |
| 1,991,808,063 | 1,992,294,401 (rejected) |

An encrypted upload just under 1900 MiB of plaintext is rejected with
`EntityTooLarge` (S3, 400) / `object_too_large` (admin API, 413) before any
byte is spooled, from the declared length. Unencrypted objects may use the full
1,992,294,400 bytes. Telegram's own non-premium document limit is 2000 MiB, so
the ceiling leaves 100 MiB of margin.

Legacy `MinResumableUploadBytes`, `LargeFileMaxBytes` and `MaxParts` are not
used: there is no TDX1 multipart (D9).

### 7.2 S3 multipart upload

S3 multipart parts are staged on local disk by `internal/ingest/multipartstage`
and assembled into one body (D9). The running sum of part sizes, converted to
stored size when the upload is encrypted, is checked against
`MaxStoredObjectBytes` at every `UploadPart`; the excess part fails with
`EntityTooLarge`. Nothing reaches Telegram before `CompleteMultipartUpload`.

---
