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

Implement internal/replica/account. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 12 internal replica account` after its package tests pass.

### 5.2 Lane L2 — internal/replica/worker + internal/storage/replicarepo

Implement internal/replica/worker + internal/storage/replicarepo. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/tgclient/download.go` | 173 | PORT-PARTIAL (phase 04) | `internal/replica/worker` (L2) consumes `tgclient/rangeio` | Copy a primary body by streaming it, then send it to the replica with the same job-derived `random_id` |

Commit this lane as `implement phase 12 internal replica worker internal storage repl` after its package tests pass.

### 5.3 Lane L3 — internal/replica/backfill

Implement internal/replica/backfill. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/backfill/backfill.go` | 368 | PATTERN-ONLY | `internal/replica/backfill` (L3) | Resumable checkpoint cursor (`backfill_progress`), `alreadyDone` / `markDone` gate, `loadAlreadyPublished` so a restart never republishes, throttle `OpsPerSec = 5` via a ticker, progress events |

Commit this lane as `implement phase 12 internal replica backfill` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/backfill/backfill.go`: `type Runner struct {`; `type PeerResolver interface {`; `type ProgressEvent struct {`; `func NewRunner(db *sql.DB, tg tgclient.Client, peers PeerResolver) *Runner {`; `func (r *Runner) RunPersonal(ctx context.Context, channelID int64, onProgress func(ProgressEvent)) error {`

### 5.4 Lane L4 — internal/readpath/replicasource (fallback read source)

Implement internal/readpath/replicasource (fallback read source). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/media/external.go` (`externalRangeClient`) | (in 274) | PATTERN-ONLY | `internal/readpath/replicasource` (L4) | A second range client behind the same read port, checked periodically for access (`externalAccessCheckInterval = 30 * time.Second`) |

Commit this lane as `implement phase 12 internal readpath replicasource fallback read` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/media/external.go`: `type ExternalMedia struct {`; `func ExternalName(message tgclient.HistoryMessage) string {`; `func ExternalKind(message tgclient.HistoryMessage) StreamKind {`; `func (s *Service) OpenExternal(ctx context.Context, source ExternalMedia, publish func(func() error) error) (OpenResult, error) {`; `func (c *externalRangeClient) ResolveDocument(ctx context.Context, _ tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {`; `func (c *externalRangeClient) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {`; `func (s *Service) CloseExternalSessions(accountID, channelID int64) []string {`; `func (s *Service) CloseSourceSessions(accountID int64, kind string, peerID int64) []string {`

### 5.5 Lane L5 — internal/replica/deletes

Implement internal/replica/deletes. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 12 internal replica deletes` after its package tests pass.

### 5.6 Lane L6 — internal/adminapi/handlers/replica.go + api/adminv1 replica DTO file + apps/admin/internal/ui/replica

Implement internal/adminapi/handlers/replica.go + api/adminv1 replica DTO file + apps/admin/internal/ui/replica. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 12 internal adminapi handlers replica go api adm` after its package tests pass.

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
