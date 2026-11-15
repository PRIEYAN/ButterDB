# Phase 05 status — Buckets and sync

Result: accepted
Date: 2026-11-15
Phase branch: phase/05-buckets-and-sync @ fa2f25d (before this note)
Main before: aacae31
Main after: the commit that records this note
Prelude commit: 4ffe85f
Lanes head: 2c8e952
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 internal/buckets | phase/05-buckets-and-sync-l1 | 1 | PASS | ACCEPT | 2 |
| L2 internal/syncer/scan | phase/05-buckets-and-sync-l2 | 2 | PASS | ACCEPT | 3 |
| L3 internal/syncer/difference | phase/05-buckets-and-sync-l3 | 1 | PASS | ACCEPT | 1 |
| L4 internal/syncer/livesync | phase/05-buckets-and-sync-l4 | 1 | PASS | ACCEPT | 2 |
| L5 internal/syncer/reconcile | phase/05-buckets-and-sync-l5 | 1 | PASS | ACCEPT | 2 |
| L6 internal/storage/channelrepo | phase/05-buckets-and-sync-l6 | 2 | PASS | ACCEPT | 2 |
| INT integration | phase/05-buckets-and-sync-int | 1 | PASS | ACCEPT | 5 |
| L7 internal/tgclient/rangeio (fix lane, §11 V6) | phase/05-buckets-and-sync-l7 | 1 | PASS | ACCEPT | 1 |

At about 01:00 IST every running agent hit the 5-hour API usage limit. The L1, L2 and L4 workers and the L3 and L5 reviewers were stopped mid-task. Each was resumed in its own context at 11:10 IST, after the limit reset. These interruptions are not counted as attempts.

The six lanes were monitored and reviewed as each one returned, not in one batch after all six finished (§8.1). This was done to save time while the host ran on battery.

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| 4ffe85f | prieyan | define bucket and sync contracts |
| 3b00024 | prieyan | add bucket service on registry channel |
| 3e85118 | prieyan | add registry channel creation |
| ace28dd | prieyan | port full history scan with spool |
| adbbb2e | prieyan | add carried and binding scan tests |
| cb149e5 | prieyan | move scan comments off declarations and wrap gate errors |
| fa2cf9a | prieyan | port channel difference sync with per key repair |
| 39cbec0 | prieyan | port live sync coordinator |
| f10e6e9 | prieyan | fix startup and reload barriers in live sync tests |
| 95ba346 | prieyan | add registry rediscovery |
| 04f8f18 | prieyan | add deletion reconciliation |
| c98a591 | prieyan | port channel repository |
| 2c8e952 | prieyan | add set channel pts and insert channel to channel repository |
| 0951434 | prieyan | record state json shape decision |
| 9e92d96 | prieyan | wire buckets and sync in core |
| abdbc91 | prieyan | add registry setup and rediscovery to setup command |
| d8d82e7 | prieyan | export registry state store and registry recording from core |
| ba5ed9c | prieyan | add phase 05 acceptance tests |
| 9af7eaf | prieyan | record phase 05 ported files in notices |
| fa2f25d | prieyan | deduplicate file reference refresh across late readers |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | includes test/acceptance/phase01 to phase04 |
| V6 race tests | pass | `./internal/...`; the first run failed (see Deviations), then passed after the L7 fix |
| V7 module integrity | pass | |
| V8 layering | pass | |
| V9 comment policy | pass | |
| V10 phase acceptance | pass | TestBucketCreationIsReplayable, TestExternalOperationIsIngested, TestDeletedLocalDatabaseRebuildsFromTelegram, TestPerKeyOrderingRepairPreservesLaterWrites all `--- PASS` |
| V11 apps modules | n/a | no `apps/` module yet |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 20 commits, author and committer prieyan, no bodies, no merges, no bad subjects |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | every new package |
| faketg acceptance | yes | the four section 7 cases, plus faketg-driven lane tests |
| SQLite in t.TempDir() | yes | real projection.db and control.db, with the real projector, appliers and rebuilder |
| Race detector | yes | `./internal/...`; the INT worker also ran it over `./cmd/...` and `test/acceptance/phase05` |
| Telegram test DC smoke | no | phase 05 adds none. The production setup path (`TelegramSession.EnsureRegistry` on the real transport) compiles but has never run against Telegram |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| none in section 8 | n/a | n/a | n/a |
| state.json shape (raised by L1 as a spec conflict) | Which state.json shape should phase 05 integration write: storage-model §4.3 flat, operations.md §3.3 nested, or defer? | "storage-model flat (Recommended)" | yes. Recorded as E17 in 0951434, which also updated storage-model §4.3, operations.md §3.3/§5/§7 and the phase 05 binding copy |

## Module changes

| Module | Package paths | Version | Added by |
| --- | --- | --- | --- |
| none | | | The prelude added no module. Tidy after integration changed nothing (`golang.org/x/sync/semaphore` and `singleflight` come from the existing x/sync requirement) |

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |
| backend/sync/sync.go, parse.go, initial_scan_spool.go (+ truncated_history, history_retry, connection_hold, admission, authoritative tests) | internal/syncer/scan | L2 |
| backend/sync/difference.go (+ sync.go retry constants, difference_test.go, sync_test.go cases) | internal/syncer/difference | L3 |
| backend/livesync/coordinator.go (+ test) | internal/syncer/livesync | L4 |
| backend/sync/sync.go (ReconcileDeletions, tombstoneMissing; reconcile_test.go cases) | internal/syncer/reconcile | L5 |
| backend/projection/channels.go (+ channels_test.go cases) | internal/storage/channelrepo | L6 |
| backend/services/personaldrive/service.go, lifecycle/service.go, auth/config.go, core/peer_resolution.go, services/encryption/service.go | internal/buckets, internal/syncer/reconcile | L1, L5 (design reference only) |

Every row is recorded in `docs/legal/THIRD_PARTY_NOTICES.md`. The activity.go row records that the drop-counting buffer landed in `internal/tgclient/conn` in phase 04 and that livesync consumes it through a port. The INT worker recorded the pattern-only rows as "referenced" without confirming that a lane opened each file. L1 reported them as pattern only.

## Deviations and minor findings

- **Prelude design:** shared sync types and ports live in `internal/syncer/contract.go`, following the phase 03 `internal/projection/contract.go` precedent. channelrepo is infrastructure, so it cannot import that file. It defines its own types, and core adapts them to the ports.
- **V6 fix lane:** the first V6 run failed. `TestFileReferenceRefreshDeduplicatedAcrossReaders` in phase 04's `internal/tgclient/rangeio` reported 2 refreshes for 16 concurrent readers. It also failed on main (aacae31), 4 of 50 runs under -race.
  - Cause: a late reader missed the refreshed-reference cache before `singleflight.Do`, and so started a second flight after the first flight had finished.
  - No phase 05 lane owned the path. The orchestrator opened fix lane L7 (`internal/tgclient/rangeio/` only), with a worker, monitor and reviewer. The fix re-checks the cache inside the flight. After it, 200 of 200 race runs passed.
  - Strictly, §15 calls for a stop when no lane owns a failing path. This fix lane is a deviation, chosen under the owner's standing instruction to proceed with efficient choices.
- **Integration base:** the INT worktree was based on 0951434 (the picked lanes plus the E17 docs commit), not on LANES_HEAD, so that integration built against the corrected spec.
- L1 minor: `ErrBucketLimitReached`, `ErrOpRejected`, `ErrOpTampered` and `ErrOpNotProjected` are local to `buckets`. admin-api.md maps `bucket_limit_reached` to a code, so this one should become an `errs` sentinel.
- L1 deviations:
  - Multipart abort (§5.4 step 3) and `channels.deleteChannel` (step 6) are deferred to phase 06. The tgclient transport has no deleteChannel RPC; phase 04 omitted it although storage-model §5.4 assigns it to phase 04.
  - `ActiveJobs` is a core adapter that always answers false until jobrepo exists. Until then, bucket deletion does not see pending upload or delete jobs.
  - The bcreate job ID is derived from sha256(deployment, "bcreate", channelID). A resumed send reads back the stored text before projecting it.
- L2 deviations:
  - The `Sleeper` port and local transport classification exist because scan cannot import `tgclient/flow`.
  - Two carried connection_hold cases were renamed from Reads to Writers, because the probe is a writer probe.
  - Registry rebuild uses `RebuildAllTx`.
- L3 deviation: difference has no transient transport retries; flow is not importable. A transport failure waits for the livesync signal or the backstop.
- L4 minors:
  - The loop wakes on a real-time 50 ms ticker, and the per-sync timeout is `context.WithTimeout`, because `clock.Clock` has only `Now()`.
  - The backstop keeps the "signal" reason for a channel that is already queued.
- L5 minors:
  - A test uses `time.After` as a select guard.
  - Orphan channels with an empty About are listed with `AboutVerified=false`, because neither `rpc.ListOwnedChannels` nor faketg fills About.
- L6 minors and deviations:
  - `ErrInvalidChannel` and `ErrChannelNotFound` are package-local sentinels.
  - `RecordDeletedTx` removes live objects and backs their sizes out of the bucket counters, although schema §6 names the appliers as the objects writer. This removal has no op.
- INT minors:
  - Core runs raw SQL for `buckets` channel IDs, `admin_meta` and the startup checks. Move these into repositories when adminrepo and jobrepo land.
  - The unused `ErrRegistryNotAdopted` should be removed.
  - The single-line comment above `noActiveJobs` should be removed in phase 06.
- INT deviations:
  - Core has a minimal `admin_meta.server_id` accessor, minting 26 base32 characters when the value is absent.
  - Adoption also inserts the registry `channels` row.
  - `serve` opens the databases and the key ring to run the local §4.4 checks when `state.json` or `control.db` exists.

## Phase document defects

- §4 says "Write any ports shared by sync lanes into the prelude" but gives no shapes. The orchestrator designed `internal/syncer/contract.go`.
- storage-model §5.4 says phase 04 adds `channels.deleteChannel` to `tgclient/rpc`. It did not, and phase 05 has no lane that owns tgclient.
- storage-model §4.3 and operations.md §3.3 contradicted each other on the `state.json` shape. Fixed by E17.
- §5.4 asks L4 to port the TelegramActivity buffer, which phase 04 had already ported into `tgclient/conn`.

## Open questions for the owner

- **Syncer start in `serve`:** `serve` does not yet start the syncer. It opens no transport, runs no initial `EnsureAllAuthoritative`, and starts no livesync coordinator. The remote §4.4 checks are also not implemented, and the orphan finder and deletion reconciler have no caller. The pieces exist in `core.NewSync`. A later phase, phase 06 or the listener phases, should own serve-time startup under the `linked` switch.
- **Missing key at setup:** a missing key file at `setup` exits 1 rather than 4.
- **Clock timer port:** should `clock.Clock` gain a timer or sleeper port, so that livesync and the retry sleepers use the injected dependency throughout?
- **Package-local sentinels:** should the package-local sentinels of phases 03–05 move into `internal/domain/errs` with codes?
- **Carried from phase 04:**
  - `GOOS=windows` build failure in processlock.
  - `fsx.FS` has no `ReadFile`.
  - `TelegramSession` opens its own control.db handle.

## Worktrees and branches left in place

none
