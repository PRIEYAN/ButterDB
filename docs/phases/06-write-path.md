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

Implement internal/ingest/spool. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/mountwrite/staging.go` | 555 | PORT-PARTIAL | `internal/ingest/spool` (L1) | Keep: `DiskStagingStore` streaming a body to a private file while hashing, `exactLengthReader` (length mismatch and trailing data rejected), per-object and aggregate byte quotas, slot semaphore with cancellable wait, startup accounting of existing files, partial-file cleanup on every failure, safe keys from operation IDs, private modes. Change: hash MD5 (S3 ETag) and SHA-256 together; spool plaintext and encrypt during send through `crypto/stream.EncryptingReaderAt`. Drop `copyEncryptedToStage` |
| `backend/services/file/service.go` (`acquireUploadSlot`, `uploadConcurrency`, `retryVisibleSend`, `stageUploadPart`, `createTempWithFallback`) | 797 | PATTERN-ONLY | `internal/ingest/admission` (L4), `internal/ingest/spool` (L1) | Process-wide upload slots; `retryVisibleSend` stops retrying once the outcome is unknown. SlowDown mapping from the flood-wait budget (A7) is new |

Commit this lane as `implement phase 06 internal ingest spool` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/mountwrite/staging.go`: `type DiskStagingConfig struct {`; `type DiskStagingStore struct {`; `func NewDiskStagingStore(config DiskStagingConfig) (*DiskStagingStore, error) {`; `func (s *DiskStagingStore) Root() string {`; `func (s *DiskStagingStore) UsedBytes() int64 {`; `func (s *DiskStagingStore) Stage(ctx context.Context, request StageRequest, source io.Reader) (StagedObject, error) {`; `func (s *DiskStagingStore) Open(staged StagedObject) (ReadSeekCloser, error) {`; `func (s *DiskStagingStore) Remove(ctx context.Context, staged StagedObject) error {`
- `backend/services/file/service.go`: `type PeerResolver interface {`; `type EventSink interface {`; `type Service struct {`; `type Metadata struct {`; `type DownloadResult struct {`; `type PreviewPayload struct {`; `func (r *contextReader) Read(p []byte) (int, error) {`; `func WithDownloadProgressID(ctx context.Context, requestID string) context.Context {`

### 5.2 Lane L2 — internal/ingest/put

Implement internal/ingest/put. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/services/file/hidden_upload.go` | 585 | PORT-PARTIAL | `internal/ingest/put` (L2) | Keep: send the hidden body with `random_id = StableRandomID(jobID, step)`; validate metadata and source size before any remote call; on `ErrSendOutcomeUnknown` resend with the same `random_id` so Telegram returns the original message; persist a recovered receipt before cleanup; `discardHiddenBody` when the commit never happens; refuse a receipt whose message ID is already owned. Drop: the part loop, `partAttachmentName` part suffixes, `hiddenUploadUUID`, `PartsForUUIDContext` |
| `backend/core/engine.go` (`EmitAndProjectContext`, `sendControlWithRetry`, `controlPeer`) | 932 | PORT-PARTIAL | `internal/ingest/put` (L2) | Send the control message, then `ProjectFromOp` its receipt; a projection error after acceptance returns the positive message ID wrapped in `ErrControlProjection` and is never resent. Change: `random_id` from the job ID (E9) |
| `backend/mountwrite/locks.go` | 112 | PORT | `internal/ingest/put` (L2) | `KeyedLocker`: serialize writers of one `(bucket, key)`, sorted and deduplicated multi-key acquisition, entries cleaned up, cancellable wait |
| `backend/mountadapter/remote.go` | 759 | PATTERN-ONLY | `internal/ingest/put` (L2) | Upload hidden, then commit; an unknown outcome or a hidden receipt needing recovery is surfaced as "recovery required", never as failure |
| `backend/mountwrite/coordinator.go` | 452 | PATTERN-ONLY | `internal/ingest/put` (L2) | `createOrLoad` keyed by operation ID so a retried request resumes, `withOperation`, `transition`, `markAborted`, `deferCleanup` |
| `backend/services/file/upload_failures.go` | 126 | PATTERN-ONLY | `internal/ingest/put` (L2) | `classifyUploadFailure`: not sent, uncertain and canceled are distinct outcomes; phase 08 maps them to S3 errors |

Commit this lane as `implement phase 06 internal ingest put` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/services/file/hidden_upload.go`: `type HiddenUploadRequest struct {`; `type HiddenBody struct {`; `func (s *Service) UploadHidden(ctx context.Context, channelID int64, request HiddenUploadRequest, source io.ReadSeeker) (HiddenBody, error) {`; `func (s *Service) RecoverHiddenUpload(`; `func (s *Service) RecoverAndDiscardHiddenUpload(`; `func (s *Service) DiscardHiddenReceipt(`; `func (s *Service) DiscardHiddenOperation(ctx context.Context, channelID int64, operationID string) error {`
- `backend/core/engine.go`: `type EventSink interface {`; `type Config struct {`; `type Engine struct {`; `func New(ctx context.Context, cfg Config) (*Engine, error) {`; `type DriveScanProgressEvent struct {`; `func (e *Engine) PauseLiveSync() {`; `func (e *Engine) ResumeLiveSync() {`; `func (e *Engine) Close() {`
- `backend/mountwrite/locks.go`: `type KeyedLocker struct {`; `func NewKeyedLocker() *KeyedLocker {`; `func (l *KeyedLocker) Lock(ctx context.Context, keys ...string) (func(), error) {`; `func (l *KeyedLocker) Len() int {`
- `backend/mountadapter/remote.go`: `type HiddenStore interface {`; `type TelegramRemoteConfig struct {`; `type TelegramRemote struct {`; `func NewTelegramRemote(config TelegramRemoteConfig) (*TelegramRemote, error) {`; `func (remote *TelegramRemote) UploadHidden(ctx context.Context, request mountwrite.HiddenUpload, source io.ReadSeeker) (mountwrite.RemoteBody, error) {`; `func (remote *TelegramRemote) RecoverHidden(ctx context.Context, request mountwrite.HiddenUpload, source io.ReadSeeker) (mountwrite.RemoteBody, error) {`; `func (remote *TelegramRemote) DiscardHidden(ctx context.Context, operationID string, body *mountwrite.RemoteBody) error {`; `func (remote *TelegramRemote) Commit(ctx context.Context, request mountwrite.CommitRequest) (mountwrite.MutationResult, error) {`
- `backend/mountwrite/coordinator.go`: `type CoordinatorConfig struct {`; `type Coordinator struct {`; `func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {`; `type UUIDGenerator struct{}`; `func (UUIDGenerator) NewID() string {`; `func (c *Coordinator) Status() Status {`; `func (c *Coordinator) Drain(ctx context.Context) error {`; `func (c *Coordinator) Close(ctx context.Context) error {`
- `backend/services/file/upload_failures.go`: `type UploadFailure struct {`; `func NotSentFailures(filePaths []string, err error) []UploadFailure {`; `func (s *Service) UploadWithFailures(ctx context.Context, channelID int64, filePaths, parentIDs []string, encrypt bool) ([]Metadata, []UploadFailure, error) {`; `func (o *failureUploadObserver) Started(id int, name string, size int64, parentID string) {`; `func (o *failureUploadObserver) Accepted(id int) {`; `func (o *failureUploadObserver) Completed(id int, name string) {`; `func (o *failureUploadObserver) Failed(id int, name string, err error) {`

### 5.3 Lane L3 — internal/ingest/remove

Implement internal/ingest/remove. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/services/file/upload.go` `deleteMessagesChunked` | (in 777) | PORT-PARTIAL | `internal/ingest/remove` (L3) | Chunks of 100, each under the write retry policy, continue after a failed chunk, return the first error |
| `backend/mountwrite/hard_delete.go`, `journal_hard_delete.go` | 184, 303 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/storage/jobrepo` (L5) | Durable plan of message IDs appended once, then `NextHardDeleteBatch` / `MarkHardDeleteBatchDone` so a crash resumes mid-delete. butterDB writes the `t=del` tombstone before the first batch (E4) |
| `backend/projection/hard_delete.go`, `hard_delete_intent.go` | 660, 179 | PATTERN-ONLY | `internal/ingest/remove` (L3) | Plan pages (`maxHardDeletePlanPageSize = 1000`), plan validation that refuses control messages and bodies referenced elsewhere |
| `backend/services/file/mutations.go` (`Delete`, `purgeObject`, `deletePlannedBodies`, `SweepOrphanParts`) | 599 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/ingest/sweeper` (L5) | Purge claims a durable plan, deletes bodies, then completes. `SweepOrphanParts` becomes the sweeper's orphan-body pass using `objectrepo.ManagedMsgIDsIn` |

Commit this lane as `implement phase 06 internal ingest remove` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/services/file/upload.go`: `func (s *Service) Upload(ctx context.Context, channelID int64, filePaths []string, parentIDs []string, encrypt bool) ([]Metadata, error) {`; `func (s *Service) CancelUpload(uploadID int) bool {`
- `backend/mountwrite/hard_delete.go`: `func (c *Coordinator) HardDelete(ctx context.Context, request HardDeleteRequest) (MutationResult, error) {`
- `backend/projection/hard_delete.go`: `func ValidateHardDeletePlanItems(`; `func HardDeletePlanPage(ctx context.Context, db *sql.DB, channelID int64, opID string, afterMsgID int64, limit int) (ids []int64, total int64, done bool, err error) {`; `func CompleteHardDeletePlan(ctx context.Context, db *sql.DB, channelID int64, opID string) (err error) {`
- `backend/services/file/mutations.go`: `func (s *Service) Meta(channelID int64, msgID int, name string, size int64, parentID string) error {`; `func (s *Service) MetaContext(ctx context.Context, channelID int64, msgID int, name string, size int64, parentID string) error {`; `func (s *Service) Rename(ctx context.Context, channelID int64, msgID int, newName string) (err error) {`; `func (s *Service) Move(ctx context.Context, channelID int64, msgID int, newParentID string) (err error) {`; `func (s *Service) Delete(ctx context.Context, channelID int64, msgID int) (err error) {`; `func (s *Service) TrashObject(ctx context.Context, channelID int64, objectID string) error {`; `func (s *Service) RestoreObject(ctx context.Context, channelID int64, objectID string) error {`; `type TrashListing struct {`

### 5.4 Lane L4 — internal/ingest/admission (send-rate limiter, flood-wait budget → SlowDown mapping, process-wide slots)

Implement internal/ingest/admission (send-rate limiter, flood-wait budget → SlowDown mapping, process-wide slots). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/services/file/service.go` (`acquireUploadSlot`, `uploadConcurrency`, `retryVisibleSend`, `stageUploadPart`, `createTempWithFallback`) | 797 | PATTERN-ONLY | `internal/ingest/admission` (L4), `internal/ingest/spool` (L1) | Process-wide upload slots; `retryVisibleSend` stops retrying once the outcome is unknown. SlowDown mapping from the flood-wait budget (A7) is new |

Commit this lane as `implement phase 06 internal ingest admission send rate limiter f` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/services/file/service.go`: `type PeerResolver interface {`; `type EventSink interface {`; `type Service struct {`; `type Metadata struct {`; `type DownloadResult struct {`; `type PreviewPayload struct {`; `func (r *contextReader) Read(p []byte) (int, error) {`; `func WithDownloadProgressID(ctx context.Context, requestID string) context.Context {`

### 5.5 Lane L5 — internal/storage/jobrepo + internal/ingest/sweeper (uncertain sends, pending cleanup, control migrations)

Implement internal/storage/jobrepo + internal/ingest/sweeper (uncertain sends, pending cleanup, control migrations). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/mountwrite/hard_delete.go`, `journal_hard_delete.go` | 184, 303 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/storage/jobrepo` (L5) | Durable plan of message IDs appended once, then `NextHardDeleteBatch` / `MarkHardDeleteBatchDone` so a crash resumes mid-delete. butterDB writes the `t=del` tombstone before the first batch (E4) |
| `backend/services/file/mutations.go` (`Delete`, `purgeObject`, `deletePlannedBodies`, `SweepOrphanParts`) | 599 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/ingest/sweeper` (L5) | Purge claims a durable plan, deletes bodies, then completes. `SweepOrphanParts` becomes the sweeper's orphan-body pass using `objectrepo.ManagedMsgIDsIn` |
| `backend/mountwrite/journal.go` | 482 | PATTERN-ONLY | `internal/storage/jobrepo` (L5) | `SQLiteJournal`: `Create`, `Get`, compare-and-swap `Transition`, `ListRecoverable` in stable order, typed uniqueness-violation detection, corrupt-row rejection. butterDB's job rows also persist file salt and nonce prefix before the first send (E8) |
| `backend/mountwrite/types.go` | 618 | PATTERN-ONLY | `internal/storage/jobrepo` (L5) | `JournalState` with an explicit `allowedTransitions` table and `ValidateTransition`. TDrive states: `receiving`, `staged`, `uploading`, `uploaded`, `committing`, `reconciling`, `remote_committed`, `projection_pending`, `cleanup_pending`, `delete_plan_pending`, `deleting_bodies`, `delete_finalizing`, `done`, `aborted`. butterDB's job states are defined by the phase 06 document |
| `backend/mountwrite/recovery.go` | 188 | PATTERN-ONLY | `internal/ingest/sweeper` (L5) | `Recover` walks recoverable records: reconcile an uploading receipt before removing staged bytes, retry a committing operation idempotently, finish cleanup |
| `backend/projection/multipart.go` (`QueuePartCleanup`, `PendingPartCleanup`, `ClearPartCleanup`) | (in 332) | PATTERN-ONLY | `internal/ingest/sweeper` (L5) | A durable cleanup queue of message IDs. The rest of the file is cut |

Commit this lane as `implement phase 06 internal storage jobrepo internal ingest swee` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/mountwrite/hard_delete.go`: `func (c *Coordinator) HardDelete(ctx context.Context, request HardDeleteRequest) (MutationResult, error) {`
- `backend/services/file/mutations.go`: `func (s *Service) Meta(channelID int64, msgID int, name string, size int64, parentID string) error {`; `func (s *Service) MetaContext(ctx context.Context, channelID int64, msgID int, name string, size int64, parentID string) error {`; `func (s *Service) Rename(ctx context.Context, channelID int64, msgID int, newName string) (err error) {`; `func (s *Service) Move(ctx context.Context, channelID int64, msgID int, newParentID string) (err error) {`; `func (s *Service) Delete(ctx context.Context, channelID int64, msgID int) (err error) {`; `func (s *Service) TrashObject(ctx context.Context, channelID int64, objectID string) error {`; `func (s *Service) RestoreObject(ctx context.Context, channelID int64, objectID string) error {`; `type TrashListing struct {`
- `backend/mountwrite/journal.go`: `type JournalRecord struct {`; `type JournalPatch struct {`; `type Journal interface {`; `type HardDeletePlanStatus struct {`; `type HardDeleteJournal interface {`; `type SQLiteJournal struct {`; `func EnsureJournalSchema(ctx context.Context, db *sql.DB) error {`; `func NewSQLiteJournal(db *sql.DB) (*SQLiteJournal, error) {`
- `backend/mountwrite/types.go`: `type Mutation struct {`; `func (m Mutation) AffectedParents() []string {`; `func (m Mutation) AffectedObjects(result MutationResult) []string {`; `type PutRequest struct {`; `func (r PutRequest) Validate() error {`; `type MkdirRequest struct {`; `func (r MkdirRequest) Validate() error {`; `type MoveRequest struct {`
- `backend/mountwrite/recovery.go`: `type RecoveryReport struct {`; `func (c *Coordinator) Recover(ctx context.Context) (RecoveryReport, error) {`
- `backend/projection/multipart.go`: `type FilePart struct {`; `func MultipartParts(db *sql.DB, channelID int64, fileMsgID int64) ([]FilePart, error) {`; `func MultipartPartsContext(ctx context.Context, db *sql.DB, channelID int64, fileMsgID int64) ([]FilePart, error) {`; `func PartsForUUID(db *sql.DB, channelID int64, uuid string) ([]FilePart, error) {`; `func PartsForUUIDContext(ctx context.Context, db *sql.DB, channelID int64, uuid string) ([]FilePart, error) {`; `func OrphanPartMessages(db *sql.DB, channelID int64) ([]int64, error) {`; `func DeleteFileParts(db *sql.DB, channelID int64, uuid string) error {`; `func DeleteFilePartsByMsgIDs(db *sql.DB, channelID int64, msgIDs []int64) error {`

### 5.6 Lane L6 — internal/ingest/multipartstage (S3 multipart part storage and assembly into the spool)

Implement internal/ingest/multipartstage (S3 multipart part storage and assembly into the spool). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 06 internal ingest multipartstage s3 multipart p` after its package tests pass.

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
