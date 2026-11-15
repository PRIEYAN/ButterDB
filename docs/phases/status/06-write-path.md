# Phase 06 status — Write path

Result: accepted
Date: 2026-11-16
Phase branch: phase/06-write-path @ 07438ec (before this note)
Main before: 1ac7a7a
Main after: the commit that records this note
Prelude commit: f09daa3
Lanes head: 1697828 (L1–L6 at 2da024f, then fix lanes L9–L11)
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 internal/ingest/spool | phase/06-write-path-l1 | 1 | PASS | ACCEPT | 1 |
| L2 internal/ingest/put | phase/06-write-path-l2 | 2 | PASS | ACCEPT | 5 |
| L3 internal/ingest/remove | phase/06-write-path-l3 | 2 | PASS | ACCEPT | 2 |
| L4 internal/ingest/admission | phase/06-write-path-l4 | 2 | PASS | ACCEPT | 3 |
| L5 internal/storage/jobrepo + internal/ingest/sweeper | phase/06-write-path-l5 | 3 | PASS | ACCEPT (by owner decision) | 5 |
| L6 internal/ingest/multipartstage | phase/06-write-path-l6 | 2 | PASS | ACCEPT | 4 |
| L7 internal/platform/fsx (fix lane) | phase/06-write-path-l7 | 1 | PASS | ACCEPT | 1 |
| L8 internal/syncer/{reconcile,difference} (fix lane) | phase/06-write-path-l8 | 1 | PASS | ACCEPT | 1 |
| L9 multipartstage test fixture (fix lane) | phase/06-write-path-l9 | 1 | PASS | ACCEPT | 1 |
| L10 put shared key lock (fix lane) | phase/06-write-path-l10 | 1 | PASS | ACCEPT | 1 |
| L11 flow pre-paced coordinator view (fix lane) | phase/06-write-path-l11 | 1 | PASS | ACCEPT | 1 |
| INT integration | phase/06-write-path-int-r1 | 2 | PASS | ACCEPT | 3 |

Notes on attempts and interruptions:

- **L5:** attempt 2 (`ecbf4c9`) was rejected because its raw-INSERT test did not cover a NULL salt or a NULL nonce prefix. Attempt 3 found that those cases cannot pass: the frozen `upload_jobs` CHECK accepts them (see Owner gates). The owner chose "Accept as-is, log it", so L5 is accepted at `ecbf4c9`. The attempt-2 worker had stopped responding, so attempt 3 ran as a fresh worker in the same worktree.
- **API limit:** around 15:00 IST on 2026-11-15 every agent hit the 5-hour API limit. All were resumed at 16:17 in their own contexts. These interruptions are not counted as attempts.
- **Briefs by reference:** lane briefs were handed to workers by line reference into this document, not pasted verbatim (§7.2). This saved prompt size on long briefs.
- **Early reviews:** monitors and reviewers were started per lane as each worker returned, as in phase 05.
- **Fix lanes:** L7–L11 were opened by the orchestrator. Each fixes a defect found while reviewing or integrating another lane. Each had its own worker, monitor and reviewer.
- **INT attempt 1:** reported PARTIAL, which surfaced L9, L10 and L11. Attempt 2 ran in a fresh `-r1` worktree from the new head and reused attempt 1's files.

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| f09daa3 | prieyan | define write path contracts |
| 63c5665 | prieyan | add upload spool with hashing and fsync |
| dd7c636 | prieyan | add put job journal and body send |
| a8df612 | prieyan | add put commit with projection and superseded cleanup |
| 1af26b7 | prieyan | add encrypted put with persisted parameters |
| bee8c9f | prieyan | release the key lock before superseded cleanup |
| 9628c1d | prieyan | store job times in milliseconds and tighten put tests |
| 3c0a999 | prieyan | add hard delete with tombstone and batched message deletion |
| 30e2ad9 | prieyan | use millisecond times and telegram message date in hard delete |
| 8ce0972 | prieyan | add write admission slots and request budget |
| e3d610d | prieyan | add budget aware send pacing |
| 52e8938 | prieyan | key put slot limit by caller scope instead of object key |
| e86e985 | prieyan | add job repository with compare and swap transitions |
| bf1d9d7 | prieyan | add pending message cleanup queue over projection database |
| b49af86 | prieyan | add orphan body queries to the cleanup queue |
| 74c4f17 | prieyan | add write path sweeper |
| 85ec720 | prieyan | test encrypted job table check with raw inserts |
| 04c78e2 | prieyan | add multipart upload index repository |
| 89e215a | prieyan | add multipart part staging |
| 8afa344 | prieyan | add multipart complete assembly and idle sweep |
| 046cf5d | prieyan | pass multipart etag to the put service |
| 14b8608 | prieyan | add read open to fsx os and rooted filesystems |
| 2da024f | prieyan | store syncer timestamps in milliseconds |
| 8f65b51 | prieyan | fix stale fs fixture in multipart dependency test |
| 13380b1 | prieyan | inject the per key write lock into put |
| 1697828 | prieyan | add pre paced write coordinator view |
| de73b9d | prieyan | wire write path in core with one shared key lock and pre paced sender |
| 6614b90 | prieyan | add phase 06 acceptance tests and shared key lock test |
| 07438ec | prieyan | record phase 06 ported files in notices |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | includes test/acceptance/phase01 to phase05 |
| V6 race tests | pass | `./internal/...` (section 7 lists no packages) |
| V7 module integrity | pass | tidy after integration changed nothing |
| V8 layering | pass | |
| V9 comment policy | pass | |
| V10 phase acceptance | pass | TestPUTAcknowledgesOnlyCommittedTelegramState, TestOverwriteCleansSupersededMessages, TestDeleteTombstonePreventsResurrection, TestUncertainSendIsReconciled, TestMultipartPartsAssembleToOneDocument all `--- PASS`, plus TestPutAndDeleteShareOneKeyLock |
| V11 apps modules | n/a | no `apps/` module yet |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 29 commits, author and committer prieyan, no bodies, no merges, no bad subjects |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | every new package |
| faketg acceptance | yes | the five section 7 cases plus the shared-lock case, with the real lane packages wired through `core.NewWrite` |
| SQLite in t.TempDir() | yes | real projection.db and control.db; real projector, appliers, rebuilder, jobrepo and cleanup queue |
| Race detector | yes | `./internal/...` |
| Telegram test DC / real transport | no | the acceptance suite passes faketg as both the limited and the pre-paced sender. Single-token pacing is proven at the coordinator level (`internal/core/writesend_test.go`), not end to end |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| none in section 8 | n/a | n/a | n/a |
| upload_jobs CHECK defect (raised by L5 attempt 3) | The upload_jobs CHECK does not refuse an encrypted row with a NULL salt or nonce prefix (length(NULL) is NULL, so the CHECK passes), and migration 0001 is frozen. Add a migration-0002 trigger, accept and log, or stop the phase? | "Accept as-is, log it" | yes |

The owner also said, while away: "continue everything i going to sleep dont ask me anything do the best". Every decision after that point was made by the orchestrator and is recorded below.

## Module changes

| Module | Package paths | Version | Added by |
| --- | --- | --- | --- |
| none | | | The prelude added no module, and tidy changed nothing |

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |
| backend/mountwrite/staging.go (+ staging_test.go) | internal/ingest/spool | L1 |
| backend/mountwrite/locks.go (+ locks_test.go) | internal/ingest/put | L2 |
| backend/services/file/hidden_upload.go, backend/core/engine.go, backend/projection/errors.go (patterns; tests adapted) | internal/ingest/put | L2 |
| backend/services/file/upload.go `deleteMessagesChunked` (pattern) | internal/ingest/remove | L3 |
| mountwrite journal, types, recovery and hard-delete files; projection hard-delete and multipart files; services/file mutations.go and service.go | jobrepo, sweeper, remove, admission, spool | design references |

Every row is in `docs/legal/THIRD_PARTY_NOTICES.md`.

## Deviations and minor findings

- **Prelude design:**
  - Shared value types were committed as prelude files: `spool.File`, the admission `Budget`, `SlowDownError` and `Config`, the jobrepo record types, and the `ingest.CleanupQueue` and `ingest.CleanupDrainer` ports. Without them, the parallel lanes could not compile against each other.
  - Ingest packages may import `jobrepo`, but only for its value types. The phase document's own multipart Index already names them. This is a recorded exception to claude.md L10.
  - Admission and put import `tgclient/flow` only for `flow.SendClass`. This is an exception to claude.md §5.4.
- **Admission per-key limit:** `AcquirePut(ctx, b, scope string)` is keyed per access key or device, not per object key. operations.md §2.4 and s3-api.md (rank 2) win over the phase document's contract (rank 4). Phase document defect.
- **Cross-lane contracts set by the orchestrator:**
  - put exports `PutMultipart(ctx, req, body, uploadID, jobID, etag)`, so a multipart upload and its upload job share one durable job ID, and the stored object carries the multipart ETag.
  - put and remove export `Resume(ctx, jobID)` for the sweeper.
  - remove owns the batch-of-100 cleanup drainer, which put and the sweeper reuse.
  - L5 owns `objectrepo/cleanupqueue.go`, which holds the backoff.
  - L2 carries `locks_test.go`, not L1, because KeyedLocker lives in put.
- **Timestamp audit:** schema §1.1 puts every `*_at` column in Unix milliseconds. Put, remove and phase 05's `syncer/{reconcile,difference}` wrote seconds. Fixed in L2 attempt 2, L3 attempt 2, and fix lane L8 (phase 05 code).
- **Message dates:** put and remove read the sent message back with GetMessages and project Telegram's date and text, never the local clock (storage-model §6.1, invariant 10). A failed read-back leaves the job for Resume or sync.
- **Fix lanes:**
  - L7 added `Open` to `fsx.OS` and `fsx.Rooted`. The FS interface is unchanged.
  - L9 fixed the multipart test fixture that L7 made stale.
  - L10 made put take a required `KeyLocks` dependency. One `put.NewKeyLocks()` is shared by put and remove (storage-model §6.6).
  - L11 added `WriteCoordinator.Prepaced()`. Put's sender uses it, so each send takes exactly one limiter token: the one admission reserves. Remove keeps the limited sender because it does not pace through admission.
- **Put retry pacing (INT reviewer minor):** after the first attempt, put's re-sends take no new limiter token. That covers the unknown-outcome retries (at most 3), resumed `put_attempted` and `uncertain_put` jobs, and flood-wait retries. The flood-wait cooldown and the per-class slot still apply. Accepted for this phase; carried forward.
- **upload_jobs CHECK gap (owner accepted):** an encrypted row with a NULL salt or nonce prefix passes the table CHECK. jobrepo's Go validation refuses it. A raw SQL insert would not.
- **L1:**
  - Spool `Open` now has `fsx.Rooted.Open` available, but it still calls `os.Open` directly.
  - Core uses `NewWithBudget(spool.max_bytes)`.
  - `spool.min_free_bytes` is not enforced, because fsx has no free-space query.
- **L5:**
  - TestNoTxAcrossNetwork uses 4 concurrent writes.
  - Multipart retention and spool start-up cleanup are not in the sweeper. The spool constructor does its own start-up cleanup.
  - The orphan-body pass uses dedicated SQL, not `ManagedMsgIDsIn`.
- **L6:**
  - There is a small window between writing a part's index row and the rename. A same-size stale part could be assembled after a crash there.
  - `checksum_value` stays empty until the S3 handler phase.
- **L3:**
  - DeleteObjects merging is not implemented. Callers loop over Delete.
  - An invalid key wraps `object.ErrInvalidKey`.
- **INT:**
  - `serve` does not build Write or Sync yet, as in phase 05.
  - `core.WithPrincipal` and `core.PrincipalForCaller` carry the caller identity for remove. Phases 08 and 09 set it.
  - A real ActiveJobs adapter (a small control.db query) replaces the phase 05 stub. It fails closed when the Control DB is absent.
  - Idempotent replay of an in-flight job returns an outcome-unknown error instead of waiting within the budget.
- **Package-local sentinels** remain in put, remove, jobrepo and spool.

## Phase document defects

- §5.4 keys `AcquirePut` by object key. The rank-2 specs say per access key.
- §5.1 assigns `locks_test.go` to L1. Its subject, `locks.go`, ports into put (L2).
- §5.6's Putter `Put` cannot share the job ID or carry the multipart ETag. It was changed to `PutMultipart`.
- §4 does not say who owns `pending_message_cleanup` claiming, the batch drainer, or the cleanup queue.
- `fsx` had no read method, which §6.4 of claude.md requires for spool and multipart.
- The frozen `upload_jobs` CHECK does not refuse NULL salt or nonce, although schema.md §5.5 says it does.

## Open questions for the owner

- Should put pace its retry and resume sends through admission (one token per re-send)?
- Should a migration-0002 trigger close the upload_jobs NULL salt and nonce CHECK gap?
- Serve-time startup of the syncer, sweeper and write path is still unwired. Which phase should own it?
- Should the package-local sentinels of phases 03–06 move into `internal/domain/errs` with codes, so the s3err and admin layers can map `ErrIdempotencyConflict`, `ErrInvalidRequest` and others?
- Carried from phase 05:
  - The clock timer port.
  - The missing deleteChannel RPC; the sweeper's channel cleanup still has no RPC to call.
  - The Windows processlock build.

## Worktrees and branches left in place

- `phase/06-write-path-int` (INT attempt 1, superseded by `-int-r1`), kept as evidence.
