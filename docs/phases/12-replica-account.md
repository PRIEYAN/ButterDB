# Phase 12 — Replica account
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement replica account as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 11 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/storage-model.md`.
- `docs/spec/admin-api.md`.
- `docs/spec/operations.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit `internal/replica/contracts.go` before fan-out. The types mirror the
`replica_jobs` and `replica_objects` tables in `docs/spec/schema.md` §5.9; L2
implements the repository, L3 backfill enqueues gaps, and L4 reads only an
exact-version mapping. The primary remains the write authority.

```go
package replica

import "context"

type Job struct {
	Seq int64
	Kind string
	PrimaryChannelID int64
	PrimaryMsgID int64
	ObjectKey string
	ReplicaJobID string
	State string
	ReplicaBodyMsgID int64
	ReplicaOpMsgID int64
	Attempts int64
	NextAttemptAt int64
}

type Mapping struct {
	PrimaryChannelID int64
	ObjectKey string
	PrimaryPutMsgID int64
	ReplicaChannelID int64
	ReplicaBodyMsgID int64
	ReplicaPutMsgID int64
}

type Repository interface {
	Enqueue(ctx context.Context, job Job) error
	Next(ctx context.Context) (Job, bool, error)
	Transition(ctx context.Context, seq int64, from, to string, bodyMsgID, opMsgID int64) error
	Complete(ctx context.Context, seq int64, from string, mapping Mapping) error
	MappingForVersion(ctx context.Context, channelID int64, key string, putMsgID int64) (Mapping, bool, error)
}
```

`Transition` must compare the old state and update the new state atomically in
`control.db`; a stale worker cannot regress a job. Use the same phase 04
transport interface with a second account and separate rate limiters. Commit
as `define replica contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/12-replica-account-l1` | internal/replica/account |
| L2 | `phase/12-replica-account-l2` | internal/replica/worker + internal/storage/replicarepo |
| L3 | `phase/12-replica-account-l3` | internal/replica/backfill |
| L4 | `phase/12-replica-account-l4` | internal/readpath/replicasource (fallback read source) |
| L5 | `phase/12-replica-account-l5` | internal/replica/deletes |
| L6 | `phase/12-replica-account-l6` | internal/adminapi/handlers/replica.go + api/adminv1 replica DTO file + apps/admin/internal/ui/replica |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — internal/replica/account

**Objective.** Link, hold and unlink the replica Telegram account, and create its mirror channels: one replica registry channel plus one replica channel per primary bucket. Mirror the bucket ops and wrapped DEKs so the replica describes itself.

**Owned paths.** `internal/replica/account/`.

**Read.** `docs/spec/admin-api.md` §8.1 and §13 (replica login states, `credentials_source`, `replica_same_account`, unlink); `docs/spec/storage-model.md` rows for the replica account and registry; `docs/spec/schema.md` `replica_channels`; `docs/plan.md` §7.7 steps 1–2; `docs/spec/operations.md` §3.1 (`secrets/session_replica.json`, `secrets/telegram_app_replica.json`). Reuse phase 04 `internal/identity` as a **second instance**. Do not copy its code.

**Contracts.**

```go
package account

type Login interface {
	Status(ctx context.Context) (identity.Snapshot, error)
	Start(ctx context.Context, phone string) (identity.Snapshot, error)
	SubmitCode(ctx context.Context, loginID, code string) (identity.Snapshot, error)
	SubmitPassword(ctx context.Context, loginID, password string) (identity.Snapshot, error)
	Cancel(ctx context.Context) (identity.Snapshot, error)
	Unlink(ctx context.Context) (identity.Snapshot, error)
}

type ChannelCreator interface {
	CreateChannel(ctx context.Context, title, about string) (channelID, accessHash int64, err error)
	SendControl(ctx context.Context, channelID, accessHash int64, text string, randomID int64) (msgID int64, err error)
}

type Channels interface {
	Upsert(ctx context.Context, c ChannelMapping) error
	All(ctx context.Context) ([]ChannelMapping, error)
	Clear(ctx context.Context) error
}

type ChannelMapping struct {
	PrimaryChannelID  int64
	Kind              string
	ReplicaChannelID  int64
	ReplicaAccessHash int64
	State             string
}

type Dependencies struct {
	Login         Login
	Creator       ChannelCreator
	Channels      Channels
	Buckets       BucketSource
	PrimaryUserID func(ctx context.Context) (int64, error)
	Clock         clock.Clock
}

type BucketSource interface {
	Registry(ctx context.Context) (channelID int64, ops []string, err error)
	Buckets(ctx context.Context) ([]bucket.Info, error)
}

func New(deps Dependencies) (*Service, error)
func (s *Service) Login() Login
func (s *Service) EnsureChannels(ctx context.Context) error
func (s *Service) Unlink(ctx context.Context) error
func (s *Service) Linked(ctx context.Context) (bool, error)
```

**Rules.**
- An authorization whose user ID equals the primary's is logged out immediately and answered with `replica_same_account`.
- `api_id` 0 with an empty `api_hash` reuses the primary credentials. Otherwise, store separate credentials as `telegram_app_replica.json` through `secretfile`.
- `EnsureChannels` is idempotent. It creates only channels missing from `replica_channels`, with stable random IDs per primary channel, so a crash between creation and the mapping row never yields two replica channels. Before creating, query the replica account's dialogs for a channel whose `t=reg` marker names the primary channel.
- The replica uses its own `flow` limiters and its own `tgclient/conn` scope, never the primary's.
- Unlink stops workers (through a callback from integration), logs out best-effort, deletes `session_replica.json` and clears the journal tables. It never deletes replica channels.

**Tests (binding names).** `TestReplicaRejectsPrimaryAccount`, `TestReuseOfPrimaryCredentials`, `TestSeparateCredentialsStoredPrivately`, `TestEnsureChannelsIdempotent`, `TestEnsureChannelsRecoversAfterCrash`, `TestMirrorsEncfgAndBucketOps`, `TestUnlinkKeepsChannels`, `TestSeparateLimiters`. Use two `faketg` accounts.

**Verify.**

```bash
go test -mod=readonly -race ./internal/replica/account/...
gofmt -l internal/replica/account
```

**Commits.** `add replica account login and unlink`, `add replica channel creation and bucket mirroring`.

### 5.2 Lane L2 — internal/replica/worker + internal/storage/replicarepo

**Objective.** Copy every primary commit to the replica account, in journal order, through the durable state machine in this document's "Replica journal contract and recovery" section. Implement the `replica.Repository` from the prelude over `control.db`.

**Owned paths.** `internal/replica/worker/`, `internal/storage/replicarepo/`.

**Read.** This document's §4 (prelude contract) and its "Replica journal contract and recovery" section (binding states and rules); `docs/plan.md` §7.7 steps 3–5 and 7; `docs/spec/schema.md` `replica_jobs`, `replica_objects`; `docs/spec/operations.md` §15.2 (`butterdb_replica_lag_ops`). TDrive reference: `/home/prieyan/weeb/TDrive/backend/tgclient/download.go`, for streaming a primary document. Here, read through `readpath` instead.

**Contracts.** `replicarepo.Repo` implements `replica.Repository` exactly as in §4, plus:

```go
package replicarepo

func New(db *sqlitedb.DB) *Repo
func (r *Repo) Lag(ctx context.Context) (pending int64, oldest time.Time, err error)
func (r *Repo) FirstFailing(ctx context.Context) (replica.Job, bool, error)
```

```go
package worker

type StoredReader interface {
	OpenStored(ctx context.Context, channelID int64, putMsgID int64) (io.ReadSeekCloser, int64, [32]byte, error)
}

type ReplicaSender interface {
	SendBody(ctx context.Context, replicaChannelID int64, name string, size int64, body io.ReadSeeker, caption string, randomID int64) (msgID int64, err error)
	SendControl(ctx context.Context, replicaChannelID int64, text string, randomID int64) (msgID int64, err error)
	FindByRandomID(ctx context.Context, replicaChannelID int64, randomID int64) (msgID int64, found bool, err error)
	VerifyDocument(ctx context.Context, replicaChannelID int64, msgID int64, size int64, sha256 [32]byte) error
}

type Dependencies struct {
	Repo    replica.Repository
	Reader  StoredReader
	Sender  ReplicaSender
	Mapper  ChannelMapper
	Clock   clock.Clock
	Metrics Metrics
}

func New(deps Dependencies) (*Worker, error)
func (w *Worker) Start(ctx context.Context) error
func (w *Worker) Close(ctx context.Context) error
```

**Rules.**
- Persist each state **before** the network step it guards. Retries reuse `replica_job_id` and `StableRandomID(replicaJobID, "body")` or `"put"`. Never mint a new random ID.
- On `body_sending` or `op_attempted` after a restart, call `FindByRandomID` before resending.
- Copy stored bytes unchanged: encrypted objects stay ciphertext, and the replica never sees plaintext. Verify the replica document's size and SHA-256 before sending the put op.
- The move to `done` and the `replica_objects` upsert share one `control.db` transaction. A job superseded by a later primary op for the same key becomes `skipped`.
- No SQLite transaction stays open across a send. Exponential backoff uses `next_attempt_at`, and the worker never blocks a primary acknowledgement.
- Export `butterdb_replica_lag_ops` from `Lag`.

**Tests (binding names).** `TestReplicaPutResumesAfterUnknownBodySend`, `TestReplicaPutResumesAfterUnknownControlSend`, `TestReplicaCopiesCiphertextUnchanged`, `TestReplicaVerifiesDocumentBeforePut`, `TestDoneAndMappingAtomic`, `TestSupersededJobSkipped`, `TestTransitionRejectsStaleState`, `TestPrimaryAcknowledgementDoesNotWaitForReplica`, `TestBackoffNextAttempt`; `replicarepo`: `TestEnqueueUniquePerPrimaryOp`, `TestNextOrderedBySeq`, `TestMappingForVersionExact`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/replica/worker/... ./internal/storage/replicarepo/...
gofmt -l internal/replica/worker internal/storage/replicarepo
```

**Commits.** `add replica journal repository`, `add replica worker state machine`.

### 5.3 Lane L3 — internal/replica/backfill

**Objective.** Close every gap between the primary projection and the replica journal: at link time (mirroring existing objects) and at every start (crashes between a primary commit and its journal row).

**Owned paths.** `internal/replica/backfill/`.

**Read.** This document's "Replica journal contract and recovery" section (scan order, uniqueness, op kinds); `docs/plan.md` §7.7 step 3; `docs/spec/admin-api.md` §13 (`backfill` progress fields); `docs/spec/schema.md` `replica_jobs` uniqueness. TDrive pattern: `/home/prieyan/weeb/TDrive/backend/backfill/backfill.go` (checkpoint cursor, already-done gate, `OpsPerSec = 5` throttle, progress events).

**Contracts.**

```go
package backfill

type ReplayCursor interface {
	After(ctx context.Context, channelID int64, afterMsgID int64, limit int) ([]Op, error)
	Channels(ctx context.Context) ([]int64, error)
}

type Op struct {
	ChannelID int64
	MsgID     int64
	Kind      string
	ObjectKey string
}

type Progress struct {
	ObjectsTotal int64
	ObjectsDone  int64
	BytesDone    int64
	StartedAt    time.Time
}

type Dependencies struct {
	Replay ReplayCursor
	Repo   replica.Repository
	Live   LiveObjects
	Clock  clock.Clock
	Rate   int
}

type LiveObjects interface {
	IsLiveVersion(ctx context.Context, channelID int64, key string, putMsgID int64) (bool, error)
	Count(ctx context.Context) (int64, error)
}

func New(deps Dependencies) (*Runner, error)
func (r *Runner) Run(ctx context.Context, onProgress func(Progress)) error
```

**Rules.**
- Scan `replay_log` in `(channel_id, msg_id)` order, in keyset batches of 256. Include `bcreate`, `bcfg`, `bdel` and `encfg` ops, not only `put` and `del`.
- Enqueue relies on the `UNIQUE (primary_channel_id, primary_msg_id, kind)` constraint, so a re-run inserts nothing new (idempotent).
- A `put` that is no longer the live version of its key is enqueued as `skipped`, never copied.
- Throttle enqueue to `Rate` operations per second (default 5), honour cancellation, and report progress at most once per second.
- No transaction is held across batches.

**Tests (binding names).** `TestReplicaBackfillIsIdempotent`, `TestBackfillIncludesBucketAndKeyOps`, `TestBackfillSkipsSupersededVersions`, `TestBackfillResumesAfterCancel`, `TestBackfillRateLimited`, `TestBackfillProgressFields`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/replica/backfill/...
gofmt -l internal/replica/backfill
```

**Commit.** `add replica backfill`.

### 5.4 Lane L4 — internal/readpath/replicasource (fallback read source)

**Objective.** Serve an object's stored bytes from the replica account when the primary fails non-transiently, but only for the exact primary version the replica mirrors.

**Owned paths.** `internal/readpath/replicasource/`.

**Read.** This document's "Replica journal contract and recovery" section (version-exact fallback rule); `docs/plan.md` §7.2 step 12 and §7.7 step 8; `docs/phases/07-read-path.md` §5 (the `rangereader` read port this decorates); `docs/spec/storage-model.md` failure-modes table. TDrive pattern: `/home/prieyan/weeb/TDrive/backend/media/external.go` (`externalRangeClient`, a second range client behind the same port, with a periodic access check every 30 s).

**Contracts.**

```go
package replicasource

type Mappings interface {
	MappingForVersion(ctx context.Context, channelID int64, key string, putMsgID int64) (replica.Mapping, bool, error)
}

type ReplicaRange interface {
	ResolveDocument(ctx context.Context, channelID int64, msgID int64) (tgclient.DocumentRef, error)
	ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error)
}

type Dependencies struct {
	Primary  Source
	Mappings Mappings
	Replica  ReplicaRange
	Clock    clock.Clock
}

func New(deps Dependencies) (*Fallback, error)
```

`Source` is the stored-bytes read port that phase 07 `objectreader` consumes. `*Fallback` implements the same interface, so the integration lane wraps the primary source without changing any phase 07 package. Copy the port's method set exactly from `docs/phases/07-read-path.md`. If that port is not exported for decoration, stop and report.

**Rules.**
- Fall back only on non-transient primary failures: channel or message gone, `CHANNEL_PRIVATE`, an account restriction, or the primary marked unavailable. Never fall back on flood waits, timeouts or context cancellation.
- The replica must serve the identical primary put message ID. An older replica version must never satisfy a newer primary version: return the primary error instead.
- Stored bytes are identical on both sides, so decryption and ETags upstream are unaffected.
- Recheck replica access at most every 30 s. Fallback reads count in `butterdb_replica_fallback_reads_total`.

**Tests (binding names).** `TestReplicaMappingIsVersionExact`, `TestFallbackOnChannelGone`, `TestNoFallbackOnFloodWait`, `TestNoFallbackOnCancel`, `TestFallbackBytesIdentical`, `TestAccessRecheckInterval`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/readpath/replicasource/...
gofmt -l internal/readpath/replicasource
```

**Commit.** `add replica fallback read source`.

### 5.5 Lane L5 — internal/replica/deletes

**Objective.** Mirror primary deletions on the replica: object tombstones, superseded-version cleanup, bucket deletion and purges, using the same tombstone-first, batch-of-100 rules as the primary.

**Owned paths.** `internal/replica/deletes/`.

**Read.** This document's "Replica journal contract and recovery" section (L5 rule); `docs/spec/storage-model.md` hard-delete and overwrite sections (E3, E4); `docs/phases/06-write-path.md` §5.3 (the primary `remove` flow to mirror); `docs/spec/schema.md` `replica_jobs` kinds `del`, `purge`, `bdel`.

**Contracts.**

```go
package deletes

type ReplicaDeleter interface {
	SendControl(ctx context.Context, replicaChannelID int64, text string, randomID int64) (msgID int64, err error)
	DeleteMessages(ctx context.Context, replicaChannelID int64, msgIDs []int64) error
	FindByRandomID(ctx context.Context, replicaChannelID int64, randomID int64) (msgID int64, found bool, err error)
}

type Dependencies struct {
	Repo     replica.Repository
	Deleter  ReplicaDeleter
	Mappings Mappings
	Clock    clock.Clock
}

type Mappings interface {
	Current(ctx context.Context, channelID int64, key string) (replica.Mapping, bool, error)
	Remove(ctx context.Context, channelID int64, key string) error
}

func New(deps Dependencies) (*Handler, error)
func (h *Handler) Handle(ctx context.Context, job replica.Job) error
```

The L2 worker calls `Handle` for jobs of kind `del`, `purge` and `bdel`. Integration wires it through a small dispatch interface the worker declares.

**Rules.**
- Send the replica `t=del` tombstone first, with `StableRandomID(replicaJobID, "del")`, and only then delete the replica body and put messages in batches of at most 100.
- A partial batch failure retries only the remaining IDs. Deleting an already-deleted message counts as success.
- A put for a key that arrives after its delete in journal order keeps the newer mapping. A delete never removes a mapping whose primary put message ID is newer than the delete.
- Never delete replica channels on `bdel`. Mark `replica_channels.state = 'deleted'`.

**Tests (binding names).** `TestReplicaDeleteTombstonePreventsResurrection`, `TestTombstoneBeforeDelete`, `TestBatchesOfHundred`, `TestPartialBatchRetriesRemainder`, `TestDeleteKeepsNewerMapping`, `TestBucketDeleteKeepsChannel`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/replica/deletes/...
gofmt -l internal/replica/deletes
```

**Commit.** `add replica delete mirroring`.

### 5.6 Lane L6 — internal/adminapi/handlers/replica.go + api/adminv1 replica DTO file + apps/admin/internal/ui/replica

**Objective.** Expose the replica account in the admin API and the Fyne app: status, login, cancel and unlink.

**Owned paths.** `internal/adminapi/handlers/replica.go`, `internal/adminapi/handlers/replica_test.go`, `apps/admin/internal/ui/replica/` (including `vm/`). If `api/adminv1` lacks any replica DTO named in `docs/spec/admin-api.md` §16.6, add exactly one new file, `api/adminv1/replica_extra.go`. Never edit the existing DTO files.

**Read.** `docs/spec/admin-api.md` §13, §16.6 (replica DTOs), §16.7 (replica client methods, already implemented in phase 09 L5); `docs/spec/admin-gui.md` §11.8 and §16.2 (`ui/replica/vm` row); `docs/phases/10-admin-gui.md` L2 (the reusable `telegramlink.LoginWizard`).

**Server contract.** In `handlers`, add the routes from admin-api §13 (`GET /admin/v1/replica`, `POST /admin/v1/replica/telegram/phone`, `/code`, `/password`, `/cancel`, `/unlink`). They replace the phase 09 `501` placeholders and call a port declared in `replica.go`:

```go
type ReplicaService interface {
	Status(ctx context.Context) (adminv1.ReplicaStatus, error)
	Start(ctx context.Context, phone string, apiID int, apiHash string) (adminv1.ReplicaStatus, error)
	SubmitCode(ctx context.Context, loginID, code string) (adminv1.ReplicaStatus, error)
	SubmitPassword(ctx context.Context, loginID, password string) (adminv1.ReplicaStatus, error)
	Cancel(ctx context.Context) (adminv1.ReplicaStatus, error)
	Unlink(ctx context.Context) (adminv1.ReplicaStatus, error)
}
```

If the phase 09 router registers routes centrally, add a `RegisterReplica(mux, ReplicaService)` function in `replica.go`, and let the integration lane call it instead of editing the router file.

**GUI contract.** `replica/vm` declares the client port from admin-gui §3.3 and adapts it into a `LoginFlow` for the reused `telegramlink.LoginWizard`. `replica` exports `NewView(vm, nav) fyne.CanvasObject`. Show `state`, lag, backfill progress and `last_error` without credentials.

**Rules.**
- Responses never contain session bytes, api_hash, codes or passwords.
- Unlink requires `{"confirm": "unlink"}`, and the GUI makes the user type `unlink`.
- Every replica mutation writes an audit record.

**Tests (binding names).** Server: `TestReplicaRoutesReplacePlaceholders`, `TestReplicaSameAccountMapped`, `TestReplicaUnlinkRequiresConfirm`, `TestReplicaStatusHasNoSecrets`. GUI: `TestNotImplementedPlaceholder`, `TestReplicaWizardReusesFlow`, `TestBackfillProgress`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/adminapi/handlers/...
(cd apps/admin && CGO_ENABLED=1 go test -mod=readonly -race ./internal/ui/replica/...)
gofmt -l internal/adminapi/handlers apps/admin/internal/ui/replica
```

**Commits.** `add replica admin api routes`, `add replica screen to admin app`.

## 6. Integration lane

core wiring, test/acceptance/phase12
Own `internal/core`, `cmd/`, and `test/acceptance/phase12` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase12/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestReplicaBackfillReachesParity`: replica backfill reaches parity.
  - `TestReadsFallBackAfterPrimaryFailure`: reads fall back after primary failure.
  - `TestDeletesPropagateWithoutResurrection`: deletes propagate without resurrection.
  - `TestReplicaLagIsVisible`: replica lag is visible.
### TDrive regression tests to adapt

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/backfill/backfill_test.go` | 201 | Pattern | Resume after interruption without duplicate ops; runs once per channel |

## 8. Owner gates

The owner supplies a second Telegram account for the real-account replication check. Fake-based acceptance is mandatory even when this check cannot run.

## 9. Exit criteria and handoff

All L1–L6 commits have passed monitoring and review; the integration build and phase 12 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 13.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding behavior for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/admin-api.md`

## 13. Replica account (phase 12)

The replica login reuses the §8 state machine, a second instance in
`internal/replica/account`, and the same request DTOs.

`ReplicaStatus`:

```json
{
  "state": "backfilling",
  "next": "none",
  "credentials_source": "primary",
  "login": null,
  "account": {"user_id": 777000999, "username": "…", "first_name": "…", "phone_masked": "…", "linked_at": "…", "connected": true},
  "lag": {"pending_ops": 412, "oldest_pending_at": "…"},
  "backfill": {"objects_total": 18234, "objects_done": 9120, "bytes_done": 26000000000, "started_at": "…"},
  "last_error": null
}
```

| `state` | Meaning |
| --- | --- |
| `not_linked` | No replica |
| `sending_code`, `awaiting_code`, `awaiting_password` | Login in progress, as §8 |
| `backfilling` | Linked; mirroring existing objects (A9) |
| `linked` | Linked and caught up |
| `degraded` | Linked; replica journal stalled or connection down; see `last_error` |

`next` follows §8.2: `phone` in `not_linked`, `code`, `password` or `wait`
during a login, `none` once linked. `credentials_source` is `primary`,
`replica` or empty when `not_linked`.

`POST /admin/v1/replica/telegram/phone`:

```json
{"phone": "+447700900999", "api_id": 0, "api_hash": ""}
```

`api_id` `0` and empty `api_hash` reuse the primary credentials
(`credentials_source: "primary"`); otherwise both are required and stored in
`<data_dir>/secrets/telegram_app_replica.json` (`credentials_source:
"replica"`). Default (owner may change): reusing the primary `api_id` is
allowed, because Telegram ties an `api_id` to the developer, not to one
account. Errors as §8.4 plus `replica_already_linked` and `telegram_not_linked`
(primary missing).

`POST /admin/v1/replica/telegram/code`, `/password` and `/cancel` behave as
§8.5, §8.6 and §8.7 against the replica attempt. An authorization whose
`user_id` equals the primary's is logged out and answered with
`replica_same_account`. On success the session is stored in
`<data_dir>/secrets/session_replica.json` and `state` becomes `backfilling`.

`POST /admin/v1/replica/unlink` with `{"confirm": "unlink"}` stops the replica
workers, logs the replica session out (best effort), deletes
`session_replica.json` and clears the replica journal. Replica channels are not
deleted from Telegram. Errors: `replica_not_linked`.

---

### Contract source: `docs/spec/storage-model.md`

## 12. Rebuild paths

### 12.1 From the replay log

`butterdbd rebuild`: no network. Re-derives every table from `replay_log`
(section 10). Used after an applier fix or a schema change.

### 12.2 From Telegram

`butterdbd rebuild --from-telegram`: requires `state.json` (or rediscovery,
section 4.5) and the key file.

Server stopped (process lock). operations.md §10.3 owns the command; the
procedure is:

1. Create `tmp/projection.rebuild.db` and apply the projection migrations.
2. Insert the registry `channels` row from `state.json`; full-scan it to authority (section 9.2).
3. For each live bucket, resolve the access hash from dialogs and full-scan the bucket channel to authority.
4. Copy `deleted_messages` and both cleanup queues from the old file when it is readable, so no deleted object revives and no queued deletion is forgotten.
5. Verify every active `t=encfg` unwraps with the key file. Failures are listed, not fatal.
6. Rename the live file to `projection.db.pre-rebuild-<UTC timestamp>` and the new file into place.

`--bucket <name>` rescans one bucket channel into the live `projection.db`,
replacing that bucket's rows in one transaction.

`control.db` is untouched. Jobs that referenced message IDs now absent are
resolved by the sweeper's normal rules.

---

## Additional binding contracts and checks

### plan.md: 7.7 Replica mirroring

### 7.7 Replica mirroring (phase 12)

1. The owner links a second account from the GUI replica screen through
   `/admin/v1/replica/login`, `/admin/v1/replica/login/code` and
   `/admin/v1/replica/login/password`, the same state machine as 7.5.
   `replica/account` refuses an account whose user ID equals the primary's.
2. `replica/account` creates a replica registry channel and one replica channel
   per bucket in the second account, records the mapping through `replicarepo`
   in `control.db`, and mirrors the bucket ops and wrapped DEKs (`t=encfg`) so
   the replica is self-describing.
3. Every primary commit (put, delete, bucket op) appends a `replica_journal`
   row after the projection commit (A9). A crash between the two is closed by
   `replica/backfill`, which compares projected objects with mirrored ones at
   start and journals every gap.
4. `replica/worker` takes journal rows in order. For a put it reads the stored
   bytes through `readpath` (cache first, then the primary), so ciphertext is
   copied unchanged and the replica never holds plaintext of an encrypted
   object. It sends the body and a `t=put` to the replica bucket channel with
   `StableRandomID(replicaJobID, "body")` and `"put"`, through the replica
   account's own `flow` limiters.
5. The worker records `(bucket, key, primary put msg ID, replica body msg ID,
   replica put msg ID)` and marks the journal row done.
6. `replica/deletes` mirrors tombstones and superseded-message deletions in the
   replica channels with the same batch-of-100 rule.
7. Lag is exported as `butterdb_replica_lag_ops`. Replication never delays a
   primary acknowledgement; D8 covers the primary only.
8. On read, `readpath/replicasource` serves the replica document when the
   primary fails non-transiently, as in 7.2 step 12.

---

### Replica journal contract and recovery

`docs/spec/schema.md` §5.9 names the physical tables `replica_channels`,
`replica_jobs`, and `replica_objects`. The `replica_journal` name in the plan
means `replica_jobs`; do not add a fourth table. The phase 02 migration already
contains these tables. L2 implements a repository over that schema; L3 uses it
to append missing work after a restart.

For a `put`, use these durable transitions in `seq` order:

| State | Durable fact | Next action after restart |
| --- | --- | --- |
| `pending` | Primary op and job ID recorded | Read primary stored bytes and start the body send |
| `body_sending` | Body send attempted with the stable body random ID | Query the replica channel for the receipt before retrying |
| `body_sent` | `replica_body_msg_id` recorded | Send the put op with the stable put random ID |
| `op_attempted` | Put send attempted | Query for its receipt before retrying |
| `done` | Replica put ID and `replica_objects` row committed | No work |
| `skipped` | A later primary op superseded this one before copying | No work; record the reason |

The worker must persist each state before the network step it guards. A retry
reuses the same `replica_job_id` and operation-specific `StableRandomID`, never
a new random ID. It verifies the replica document's stored length and SHA-256
before publishing the put control message. `replica_objects` is updated in the
same `control.db` transaction as the transition to `done`. A replayed journal
entry therefore cannot create two visible mappings for one key.

L3 backfill scans primary `replay_log` in `(channel_id, msg_id)` order and
inserts absent jobs using the schema's uniqueness constraint. It includes
bucket configuration and encryption-key ops, not only objects. L4 may fall
back only to a replica mapping for the same primary put message ID; an older
replica object must not satisfy a newer primary version. L5 mirrors a `del`
tombstone before deleting the replica's old body and put messages, and retries
partial delete batches. The admin API reports lag and the first failing job
without exposing Telegram credentials.

Named phase acceptance tests: `TestReplicaRejectsPrimaryAccount`,
`TestReplicaBackfillIsIdempotent`, `TestReplicaPutResumesAfterUnknownBodySend`,
`TestReplicaPutResumesAfterUnknownControlSend`, `TestReplicaMappingIsVersionExact`,
`TestReplicaDeleteTombstonePreventsResurrection`, and
`TestPrimaryAcknowledgementDoesNotWaitForReplica`.
