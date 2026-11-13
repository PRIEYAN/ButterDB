# Phase 05 — Buckets and sync
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement buckets and sync as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 04 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/storage-model.md`.
- `docs/spec/schema.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit the bucket service contract before fan-out. `bucket.Info` comes from
phase 02. `Create` publishes `bcreate` only after the private channel exists;
`Configure` publishes `bcfg`; `Delete` publishes `bdel` and follows
`docs/spec/storage-model.md` §5.4. A failed Telegram send is reconciled with
the same stable job ID before retrying.

```go
package buckets

import (
	"context"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
)

type Service interface {
	Create(ctx context.Context, spec bucket.Spec) (bucket.Info, error)
	Update(ctx context.Context, name bucket.BucketName, patch bucket.Patch) (bucket.Info, error)
	Delete(ctx context.Context, name bucket.BucketName) error
	Get(ctx context.Context, name bucket.BucketName) (bucket.Info, error)
	List(ctx context.Context) ([]bucket.Info, error)
}
```

Commit the sync consumer ports against the phase 03 projector and phase 04
transport types. Scan completion requires an empty Telegram history page, not
a short page. New out-of-order ops repair only their own key (E6). Difference
sync stores `pts` only after the corresponding projection transaction commits.
The exact authority fields and recovery order are in `docs/spec/storage-model.md`
§9. Commit as `define bucket and sync contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/05-buckets-and-sync-l1` | internal/buckets |
| L2 | `phase/05-buckets-and-sync-l2` | internal/syncer/scan |
| L3 | `phase/05-buckets-and-sync-l3` | internal/syncer/difference (with per-key repair) |
| L4 | `phase/05-buckets-and-sync-l4` | internal/syncer/livesync |
| L5 | `phase/05-buckets-and-sync-l5` | internal/syncer/reconcile (registry rediscovery, deletion reconciliation) |
| L6 | `phase/05-buckets-and-sync-l6` | internal/storage/channelrepo |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — internal/buckets

Implement internal/buckets. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/services/personaldrive/service.go` | 425 | PATTERN-ONLY | `internal/buckets` (L1), `internal/syncer/reconcile` (L5) | `Create`: create the broadcast channel, then sync and publish; `recover` / `dropProvisionalChannel`: a channel created but never committed is found and removed; `Discover` via owned channels. butterDB: a bucket channel without a committed `t=bcreate` is provisional; registry rediscovery finds the registry by its marker |
| `backend/services/lifecycle/service.go` | 267 | PATTERN-ONLY | `internal/buckets` (L1) | `SyncChannel` (incremental sync, then rebuild when repair is pending), `RebuildProjection` |
| `backend/auth/config.go` | 66 | PATTERN-ONLY | `internal/buckets` (L1) | Channel ID in a small JSON file written with `writePrivateFile`; a present-but-unparseable file is `ErrConfigInvalid`, distinct from absent. butterDB's `state.json` holds the registry channel ID the same way |
| `backend/core/peer_resolution.go` | 37 | PATTERN-ONLY | `internal/buckets` (L1) | `RefreshPeer`: on a stale access hash, resolve again, validate the channel ID and non-zero hash, persist the new hash |
| `backend/services/encryption/service.go` | 508 | PATTERN-ONLY | `internal/buckets` (L1) | `configOp` / `publishConfig`: encryption config travels as a TDX1 op and is projected like any other. butterDB publishes `t=encfg` with the wrapped DEK to the registry. Drop passwords, hints, biometrics |

Commit this lane as `implement phase 05 internal buckets` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/services/personaldrive/service.go`: `type Telegram interface {`; `type HistorySync interface {`; `type Candidate struct {`; `type State struct {`; `type Config struct {`; `type Service struct {`; `func NewService(config Config) *Service {`; `func (s *Service) Prepare(ctx context.Context) (State, error) {`
- `backend/services/lifecycle/service.go`: `type ActiveDrive struct {`; `func NewActiveDrive() *ActiveDrive {`; `func (a *ActiveDrive) ID() int64 {`; `func (a *ActiveDrive) Set(id int64) {`; `type Syncer interface {`; `type Backfiller interface {`; `type EventSink interface {`; `type Config struct {`
- `backend/auth/config.go`: `type ChannelS struct {`; `func SaveConfig(id int64) error {`; `func LoadConfig() (int64, error) {`
- `backend/core/peer_resolution.go`: `func (e *Engine) RefreshPeer(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {`
- `backend/services/encryption/service.go`: `type Status struct {`; `type Config struct {`; `type Service struct {`; `func NewService(cfg Config) *Service {`; `func (s *Service) Status() (Status, error) {`; `func (s *Service) StatusContext(ctx context.Context) (Status, error) {`; `func (s *Service) CreatePassword(password string, hint string) error {`; `func (s *Service) CreatePasswordContext(ctx context.Context, password string, hint string) error {`

### 5.2 Lane L2 — internal/syncer/scan

Implement internal/syncer/scan. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/sync/sync.go` | 874 | PORT | `internal/syncer/scan` (L2), `internal/syncer/reconcile` (L5) | `scan`: per-channel admission gate (`lockFor`, a weighted semaphore; queued callers cancel without waiting for another scan's network calls), `planHistory` (walk backwards for page bounds, only an empty page proves exhaustion, fail on no progress), `applyHistoryPlan` (oldest first), `applyInitialHistoryPlan` / `spoolHistoryPlan` / `commitInitialHistoryPlan` (fetch into the spool with no transaction open, then apply atomically), `EnsureAuthoritative`, `InitialSyncEmptyChannel`, `Incremental`, `retryFloodWait`, `getHistory` with four transport retries; constants `defaultPageSize = 100`, `maxFloodWaitRetries = 5`, `maxFloodWaitSleep = 60 * time.Second`. `reconcile`: `ReconcileDeletions` and `tombstoneMissing`, routed through the projector. Change: repair rule per E6; progress events become metrics. Drop: `adoptRecentCaptionlessMedia`, `PrepareHardDeleteProjection`, captionless media adoption options |
| `backend/sync/parse.go` | 105 | PORT-PARTIAL | `internal/syncer/scan` (L2) | `ParseHistoryPage`, `SortAscending`, `ParsedMessage`. Change: parse with `internal/wire`; keep unknown ops (E5). Drop `ParseOptions` adoption |
| `backend/sync/initial_scan_spool.go` | 136 | PORT | `internal/syncer/scan` (L2) | Spool writes and reads in batches of 256 (`initialScanSpoolBatch`), stale rows deleted at the start of a full scan. DDL moved to phase 02 |

Commit this lane as `implement phase 05 internal syncer scan` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/sync/sync.go`: `type Progress struct {`; `type Engine struct {`; `type PeerResolver interface {`; `func NewEngine(db *sql.DB, tg tgclient.Client, peers PeerResolver) *Engine {`; `func (e *Engine) Incremental(ctx context.Context, channelID int64) error {`; `func (e *Engine) PrepareHardDeleteProjection(ctx context.Context, channelID int64) error {`; `func (e *Engine) ReconcileDeletions(ctx context.Context, channelID int64) (int, error) {`; `func (e *Engine) EnsureAuthoritative(ctx context.Context, channelID int64) error {`
- `backend/sync/parse.go`: `type ParsedMessage struct {`; `type ParseOptions struct {`; `func ParseHistoryPage(msgs []tgclient.HistoryMessage) []ParsedMessage {`; `func ParseHistoryPageWithOptions(msgs []tgclient.HistoryMessage, opts ParseOptions) []ParsedMessage {`; `func SortAscending(msgs []ParsedMessage) {`

### 5.3 Lane L3 — internal/syncer/difference (with per-key repair)

Implement internal/syncer/difference (with per-key repair). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/sync/difference.go` | 169 | PORT | `internal/syncer/difference` (L3) | Bootstrap `pts` before a history scan, page through `getChannelDifference`, commit new ops, watermark and `pts` in one transaction per page, fall back to history on `TooLong`, apply deletion IDs |

Commit this lane as `implement phase 05 internal syncer difference with per key repai` after its package tests pass.

### 5.4 Lane L4 — internal/syncer/livesync

Implement internal/syncer/livesync. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/livesync/coordinator.go` | 268 | PORT | `internal/syncer/livesync` (L4) | Coalesce signal bursts per channel (debounce 750 ms), ignore unknown channels, backstop sync of known channels, per-sync timeout, failure reporting. Change: `Start` / `Close` per `docs/claude.md` §6.5; time from the clock |
| `backend/livesync/activity.go` (`TelegramActivity`, `Signals`, `Signal`, `Dropped`) | (in 127) | PORT-PARTIAL | `internal/syncer/livesync` (L4) | Bounded, never-blocking signal buffer that counts drops. The gotd part moved to `tgclient/conn` in phase 04 |

Commit this lane as `implement phase 05 internal syncer livesync` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/livesync/coordinator.go`: `type Syncer interface {`; `type EventSink interface {`; `type Config struct {`; `type Coordinator struct {`; `func NewCoordinator(cfg Config) *Coordinator {`; `func (c *Coordinator) Start(ctx context.Context) {`; `func (c *Coordinator) Stop() {`
- `backend/livesync/activity.go`: `type Activity interface {`; `type TelegramActivity struct {`; `func NewTelegramActivity(buffer int) *TelegramActivity {`; `func (a *TelegramActivity) Signals() <-chan int64 {`; `func (a *TelegramActivity) Dropped() int64 {`; `func (a *TelegramActivity) Signal(channelID int64) {`; `func (a *TelegramActivity) Handle(_ context.Context, updates tg.UpdatesClass) error {`; `func ChannelIDsFromUpdates(updates tg.UpdatesClass) []int64 {`

### 5.5 Lane L5 — internal/syncer/reconcile (registry rediscovery, deletion reconciliation)

Implement internal/syncer/reconcile (registry rediscovery, deletion reconciliation). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/sync/sync.go` | 874 | PORT | `internal/syncer/scan` (L2), `internal/syncer/reconcile` (L5) | `scan`: per-channel admission gate (`lockFor`, a weighted semaphore; queued callers cancel without waiting for another scan's network calls), `planHistory` (walk backwards for page bounds, only an empty page proves exhaustion, fail on no progress), `applyHistoryPlan` (oldest first), `applyInitialHistoryPlan` / `spoolHistoryPlan` / `commitInitialHistoryPlan` (fetch into the spool with no transaction open, then apply atomically), `EnsureAuthoritative`, `InitialSyncEmptyChannel`, `Incremental`, `retryFloodWait`, `getHistory` with four transport retries; constants `defaultPageSize = 100`, `maxFloodWaitRetries = 5`, `maxFloodWaitSleep = 60 * time.Second`. `reconcile`: `ReconcileDeletions` and `tombstoneMissing`, routed through the projector. Change: repair rule per E6; progress events become metrics. Drop: `adoptRecentCaptionlessMedia`, `PrepareHardDeleteProjection`, captionless media adoption options |
| `backend/services/personaldrive/service.go` | 425 | PATTERN-ONLY | `internal/buckets` (L1), `internal/syncer/reconcile` (L5) | `Create`: create the broadcast channel, then sync and publish; `recover` / `dropProvisionalChannel`: a channel created but never committed is found and removed; `Discover` via owned channels. butterDB: a bucket channel without a committed `t=bcreate` is provisional; registry rediscovery finds the registry by its marker |

Commit this lane as `implement phase 05 internal syncer reconcile registry rediscover` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/sync/sync.go`: `type Progress struct {`; `type Engine struct {`; `type PeerResolver interface {`; `func NewEngine(db *sql.DB, tg tgclient.Client, peers PeerResolver) *Engine {`; `func (e *Engine) Incremental(ctx context.Context, channelID int64) error {`; `func (e *Engine) PrepareHardDeleteProjection(ctx context.Context, channelID int64) error {`; `func (e *Engine) ReconcileDeletions(ctx context.Context, channelID int64) (int, error) {`; `func (e *Engine) EnsureAuthoritative(ctx context.Context, channelID int64) error {`
- `backend/services/personaldrive/service.go`: `type Telegram interface {`; `type HistorySync interface {`; `type Candidate struct {`; `type State struct {`; `type Config struct {`; `type Service struct {`; `func NewService(config Config) *Service {`; `func (s *Service) Prepare(ctx context.Context) (State, error) {`

### 5.6 Lane L6 — internal/storage/channelrepo

Implement internal/storage/channelrepo. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/projection/channels.go` | 271 | PORT-PARTIAL | `internal/storage/channelrepo` (L6) | `InsertChannel` (upsert with input validation), `GetChannel`, `ListChannels`, `ChannelExists`, `UpdateAccessHash`, `SetChannelPts`, `SetChannelPtsTx`, `DeleteChannel` cascading every channel-scoped table. Drop `UpdateInviteLink`, `MarkPersonalBackfillDone`, personal-first ordering |

Commit this lane as `implement phase 05 internal storage channelrepo` after its package tests pass.

## 6. Integration lane

core wiring, test/acceptance/phase05 (create bucket, external op ingest, delete db and rebuild from Telegram via faketg)
Own `internal/core`, `cmd/`, and `test/acceptance/phase05` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase05/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestBucketCreationIsReplayable`: bucket creation is replayable.
  - `TestExternalOperationIsIngested`: external operation is ingested.
  - `TestDeletedLocalDatabaseRebuildsFromTelegram`: deleted local database rebuilds from Telegram.
  - `TestPerKeyOrderingRepairPreservesLaterWrites`: per-key ordering repair preserves later writes.
### TDrive regression tests to adapt

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/sync/sync_test.go` | 717 | Partial | `TestIncrementalAppliesOpsAscending`, `TestIncrementalRetriesReadFloodWait`, `TestIncrementalFailsAfterMaxFloodRetries`, `TestIncrementalIsIdempotent`; rewrite `TestIncrementalRepairsLocallyAheadReplayBeforeReturning` for per-key repair. Drop adoption and hard-delete cases. Fixture `newSyncEnv` |
| `backend/sync/difference_test.go` | 152 | Full | All four |
| `backend/sync/truncated_history_test.go` | 221 | Partial | `TestEnsureAuthoritativeScansPastPagesThinnedByDeletions`, `TestFullScanRepairsOpsBankedAgainstUnreadObjects` |
| `backend/sync/history_retry_test.go` | 135 | Full | All four |
| `backend/sync/connection_hold_test.go` | 228 | Full | All four; they prove no transaction is held across a network wait (E7) |
| `backend/sync/admission_test.go` | 163 | Full | Both |
| `backend/sync/authoritative_test.go` | 202 | Partial | All except `TestEnsureAuthoritativeAdoptsCaptionlessMediaOnlyOnEmptyProjection` |
| `backend/sync/reconcile_test.go` | 167 | Partial | All except `TestReconcileDeletionsTombstonesMultipartFileMissingOnePart` |
| `backend/sync/progress_test.go` | 102 | CUT | UI progress events |
| `backend/livesync/coordinator_test.go` | 195 | Full | All four |
| `backend/projection/channels_test.go` | 236 | Partial | `TestInsertChannelInsertsAndUpdates`, `TestInsertChannelRejectsBadInput`, `TestDeleteChannelCascadesAllScopedTables`, `TestSetChannelPtsRoundTripsThroughGetAndListChannels` |
| `backend/auth/config_test.go` | 74 | Pattern | Missing versus corrupt state file |

## 8. Owner gates

None beyond the entry criteria and any explicit gates recorded in the applicable specification.

## 9. Exit criteria and handoff

All L1–L6 commits have passed monitoring and review; the integration build and phase 05 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 06.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding behavior for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/storage-model.md`

## 4. The registry channel

### 4.1 Creation

Created once by `internal/buckets` right after the primary account is linked
and bound (plan.md flow 6.5 step 10).

1. `channels.createChannel{broadcast: true, title: "butterDB registry", about: "butterDB registry <deployment id>. Do not post, edit or delete messages here."}`.
2. Insert the `channels` row (kind `registry`, access hash from the result) and mark it authoritative without a scan, because the channel is empty (`InitialSyncEmptyChannel`, legacy section 11, which refuses when a replay log for the channel exists).
3. Send the marker `t=reg` (section 4.2) with `StableRandomID(deploymentID, "reg:1")`.
4. Project the marker through `ProjectFromOp`.
5. Write `state.json` (section 4.3) by atomic rename.

A crash between 1 and 5 leaves a registry channel with no `state.json`. The next
start finds no `state.json` and a linked account, and enters operator-confirmed
rediscovery (section 4.5), which offers the stray channel as a candidate.

### 4.2 The registry marker

```text
TDX1|t=reg|dep=6pzyoxbfwcf6ubwcq5gpvjg5c4|fv=1|mr=1|kfp=bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6|uid=777000123|w=sys.setup|ts=1763000000
```

| Field | Meaning |
| --- | --- |
| `dep` | Deployment ID |
| `fv` | Format version the deployment writes (wire-format.md §7) |
| `mr` | Minimum reader format version needed to project this deployment correctly |
| `kfp` | Server key fingerprint, `bkf1_` + 32 hex (operations.md §5.2, section 8.1) |
| `uid` | Primary Telegram user ID |

The first `t=reg` in the registry channel is the deployment's identity. A later
`t=reg` is a **marker revision**, accepted only with the same `dep` and `uid`
and an `fv` and `mr` each greater than or equal to the current marker's. Its
`kfp` may differ only when written by `sys.rekey`: server key rotation
(security.md §5.6) and lost-key replacement (security.md §5.7) publish a
revision with the new fingerprint. A revision is how an upgraded binary raises
`fv` or `mr` (wire-format.md §7). Anything else is rejected with
`marker_conflict`. The newest accepted marker is the **current marker**; its
message ID is `registry_marker_msg_id` in `state.json`. Every accepted marker
is a `registry_markers` row (schema.md §4.6).

### 4.3 `state.json`

`<data_dir>/state.json`, mode `0600`, written by `internal/buckets` through
`internal/platform/secretfile` (temp file, fsync, rename, directory fsync).
operations.md §3.3 owns the file shape; this section owns what the registry
fields mean.

```json
{
  "version": 1,
  "registry_channel_id": 2208113456,
  "registry_access_hash": -6633117222220155911,
  "registry_marker_msg_id": 2,
  "primary_user_id": 777000123,
  "server_key_fingerprint": "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6",
  "ops_channel_id": 0,
  "ops_access_hash": 0,
  "probe_channel_id": 0,
  "probe_access_hash": 0
}
```

| Field | Rule |
| --- | --- |
| `version` | File format version, `1` |
| `registry_channel_id`, `registry_access_hash` | The registry channel peer |
| `registry_marker_msg_id` | Message ID of the current marker |
| `primary_user_id` | Equals the current marker's `uid` and `telegram_accounts.user_id` for role `primary` |
| `server_key_fingerprint` | Equals the current marker's `kfp` and the loaded key file's fingerprint |
| `ops_*`, `probe_*` | Owned by operations.md §9.4 and §13.5 |

The deployment ID is not in `state.json`; it is the current marker's `dep`,
projected into `registry_markers`. The first marker takes its `dep` from
`admin_meta.server_id` (admin-api.md §7.1). When rediscovery (section 4.5)
adopts an existing registry on a host whose `control.db` was not restored,
`admin_meta.server_id` is overwritten with the adopted `dep` in the same step
that writes `state.json`; no device can be paired on such a host yet, so no
GUI keychain entry is orphaned. Unknown fields are preserved on rewrite
(operations.md §3.3). A missing file is not an error until an account is
linked.

### 4.4 Startup verification

Two checkpoints of the startup order in operations.md §7.1 own these checks;
exit code 4 is operations.md §4.2's key and registry code, exit 5 its
incompatible-data-directory code. Stable reason codes go to the log and to
`GET /admin/v1/status`.

Local check, operations.md §7.1 step 7 (before any listener binds):

| Check | Reason code | Failure |
| --- | --- | --- |
| `state.json` loads; every field has its type | `state_invalid` | Exit 4 |
| No `state.json`, but `telegram_accounts` has a `primary` row | `registry_unknown` | Exit 4 with the hint to run `butterdbd setup` (rediscovery, section 4.5) |
| Loaded key fingerprint equals `server_key_fingerprint` (A6) | `kek_mismatch` | Exit 4 |
| The current `registry_markers` row, when present, has `msg_id = registry_marker_msg_id`, the same channel, `kfp = server_key_fingerprint` and `uid = primary_user_id` | `registry_marker_mismatch` | Exit 4 |
| That row's `mr` ≤ this binary's `wire.FormatVersion` | `format_too_new` | Exit 5; the operator upgrades the binary |
| The `channels` row of kind `registry`, when present, has `channel_id = registry_channel_id` | `projection_mismatch` | Exit 5; the operator runs `butterdbd rebuild --from-telegram` |

Remote check, operations.md §7.1 step 12 (once the primary run scope is
authorized, before the syncer starts):

| Check | Reason code | Failure |
| --- | --- | --- |
| Authorized user ID equals `primary_user_id` | `telegram_account_mismatch` | Graceful stop, exit 4 |
| `channels.getMessages(registry, [registry_marker_msg_id])` returns a text message whose header parses as `t=reg` | `registry_marker_missing` | Graceful stop, exit 4; rediscovery is the recovery |
| The SHA-256 of its trimmed header line equals `replay_log.first_seen_hash` for that message, when recorded; otherwise its `dep`, `uid` and `kfp` equal `state.json` | `registry_marker_mismatch` | Graceful stop, exit 4 |
| Its `mr` ≤ `wire.FormatVersion` | `format_too_new` | Graceful stop, exit 5 |

An unreachable Telegram leaves the remote check pending; the server stays
`syncing` and serves nothing Telegram-dependent (operations.md §7.2). Nothing
is repaired automatically.

### 4.5 Operator-confirmed rediscovery

Used when `state.json` is lost or its marker is missing. It runs inside
`butterdbd setup` on a TTY with the server stopped (process lock held), as
operations.md §10.5 step 2 describes, and is implemented by
`internal/syncer/reconcile` (phase 05). It is operator-confirmed: a wrong
choice would bind the deployment to another deployment's buckets.

1. `messages.getDialogs` over the primary and archived folders lists broadcast channels where the account is creator.
2. For each, `messages.getHistory` reads from the oldest message, at most 100 messages, to its first `t=reg`. A channel with one is a candidate; its marker revisions are then read through the whole channel to find the current marker (section 4.2).
3. Candidates whose current `kfp` differs from the loaded key's fingerprint, or whose `uid` differs from the authorized user, are listed as foreign and cannot be chosen. If every candidate is foreign, setup exits 4 (operations.md §10.5).
4. Each eligible candidate is printed with channel ID, title, creation date, `dep`, `fv`, `mr` and the count of live buckets from a registry scan.
5. The operator types the channel ID to adopt, even when there is one candidate. `--registry-channel <id>` supplies it non-interactively and still refuses a foreign channel. With no candidate at all, setup asks before creating a new registry (a fresh deployment, section 4.1).
6. Setup writes `admin_meta.server_id` (section 4.3), then `state.json` with the current marker's message ID. The next `serve` full-scans every channel from message 0 (section 9.2), or the operator runs `butterdbd rebuild --from-telegram` first (section 12.2).

---

### Contract source: `docs/spec/storage-model.md`

## 5. Buckets

### 5.1 Bucket = channel

A bucket's durable identity is its **bucket channel ID**. The name is an
attribute. A deleted name can be reused; the new bucket gets a new channel.

| Attribute | Values | Source op |
| --- | --- | --- |
| `name` | S3 bucket naming rules (`internal/domain/bucket.BucketName`): 3–63 characters of `a-z 0-9 . -`, starts and ends with a letter or digit, no `..`, not an IPv4 address, no `xn--` prefix, no `-s3alias` or `--ol-s3` suffix | `t=bcreate` |
| `policy` | `private` \| `public-read` (D7) | `t=bcreate`, `t=bcfg` |
| `encryption_default` | `none` \| `tde1` (D11, A5) | `t=bcreate`, `t=bcfg` |
| `cache_max_age_seconds` | 0–31536000, default 3600 (A8) | `t=bcreate`, `t=bcfg` |
| Active encryption key | Newest `t=encfg` for the bucket channel | `t=encfg` |

At most `limits.MaxBuckets` (100) live buckets per deployment.

### 5.2 Create

Requires registry authority (section 9.3). Steps, in `internal/buckets` under
the registry write gate:

1. Validate the name and the limit; check no live bucket has the name.
2. Mint job ID `j`.
3. `channels.createChannel{broadcast: true, title: "butterDB bucket <name>", about: "butterDB bucket <name> in deployment <deployment id>. Do not post, edit or delete messages here."}`.
4. Insert the `channels` row (kind `bucket`) and mark it authoritative as an empty channel.
5. Send `t=bcreate` with `StableRandomID(j, "bcreate")` and project it through `ProjectFromOp`. **Commit.**
6. When `encryption_default` is `tde1`, send `t=encfg` (section 8.3) with a second job ID and `StableRandomID(j2, "encfg")`, and project it, before the call returns. A failure here leaves a valid bucket with no key; the first encrypted PUT creates it (section 8.3).

`channels.createChannel` has no random ID. If its outcome is unknown, or the
process stops between steps 3 and 6, an unreferenced channel titled
`butterDB bucket <name>` may exist. `internal/syncer/reconcile` lists such
channels (creator, title prefix `butterDB bucket `, about text naming this
deployment, not referenced by any `buckets` row) as orphan channels in the
status API. The owner deletes them; they are never deleted automatically.
Default (owner may change).

### 5.3 Configure

`t=bcfg` carries the **full** configuration (`pol`, `de`, `cma`), never a
delta, so last-writer-wins by message ID is exact. Switching
`encryption_default` to `tde1` on a bucket with no key first sends `t=encfg`.
Changing `encryption_default` affects new objects only.

### 5.4 Delete

S3 `DeleteBucket` semantics. Requires registry authority and the bucket's
authority.

1. Take the bucket's exclusive write gate (puts and deletes hold it shared).
2. Refuse with `409 bucket_not_empty` when `buckets.object_count > 0`, or when `control.db` has a non-terminal `upload_jobs` or `delete_jobs` row for the bucket channel. This is S3 `DeleteBucket` semantics; the S3 listener itself never deletes a bucket and answers `403 AccessDenied` (s3-api.md §6.4), so the check lives in `internal/buckets` and is reached through the admin API.
3. Abort every open `multipart_uploads` row for the bucket and remove its staging directory. Default (owner may change).
4. Send `t=bdel` to the registry with `StableRandomID(j, "bdel")`. **Commit.**
5. Project: the bucket row gets `deleted_msg_id`; the channel row becomes `deleted`; the bucket's `objects`, `object_tombstones`, `object_bodies`, `encryption_keys` and `pending_message_cleanup` rows are removed (the channel deletion removes every message at once); the keyring generation advances (claude.md invariant 7); a `pending_channel_cleanup` row is queued.
6. `channels.deleteChannel` on the bucket channel. On failure the row stays queued for `ingest/sweeper`, which retries with backoff (1 min doubling to 1 h).

Tombstones do not count: a bucket whose keys were all deleted is empty.

`channels.deleteChannel` is not in the `tgclient/rpc` list of plan.md §5;
phase 04 adds it to that package.

---
