# Phase 07 status — Read path and origin cache

Result: accepted
Date: 2026-11-16
Phase branch: phase/07-read-path @ d738895 (before this note)
Main before: 907f800
Main after: the commit that records this note
Prelude commit: 046ba13
Lanes head: 30888a5
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 internal/readpath/rangereader | phase/07-read-path-and-origin-cache-l1 | 1 | PASS | ACCEPT | 1 |
| L2 internal/readpath/blockcache | phase/07-read-path-and-origin-cache-l2 | 3 | PASS | ACCEPT | 3 |
| L3 internal/readpath/objectreader | phase/07-read-path-and-origin-cache-l3 | 1 | PASS | ACCEPT | 1 |
| L4 internal/readpath/httprange | phase/07-read-path-and-origin-cache-l4 | 1 | PASS | ACCEPT | 1 |
| INT integration | phase/07-read-path-and-origin-cache-int | 1 | PASS | ACCEPT | 3 |

- **L2:** L2 was re-dispatched after verification V5 found `TestBudgetUnderConcurrency` failing 6 times in 100 runs (§11 fix-forward).
  - Attempt 2 showed the failure was a sampling artifact in the test: a non-atomic directory walk across shards. The store was not overrunning its budget. The attempt replaced the walk with an instrumented fsx wrapper.
  - Attempt 3 fixed that wrapper so it stops subtracting directory sizes when empty shards are pruned. It also added a check that the tracked total equals the bytes on disk.
  - The store's budget logic is unchanged. Only the error prefixes changed. L2's two new commits were picked after integration.
- **Briefs:** as in phase 06, lane briefs were passed by line reference into this document, and each lane was monitored and reviewed as soon as it returned.

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| 046ba13 | prieyan | define read path contracts |
| 6a2ef30 | prieyan | port range reader with coalescing and read ahead |
| e1edf6a | prieyan | add disk origin block cache |
| 7173877 | prieyan | add object reader with version pinning and decryption |
| 30888a5 | prieyan | add range and conditional request helpers |
| 11e7b9e | prieyan | wire read path in core |
| 5a40c7c | prieyan | add phase 07 acceptance tests |
| 3b391e1 | prieyan | record phase 07 ported files in notices |
| 5ee88f3 | prieyan | measure block cache budget with an instrumented fs wrapper |
| d738895 | prieyan | ignore directories in block cache byte tracker and check its total |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | Includes the acceptance suites of phases 01–06. The first run failed on the flaky blockcache test; fixed through L2 attempts 2 and 3. |
| V6 race tests | pass | `./internal/...` |
| V7 module integrity | pass | Tidy changed nothing. |
| V8 layering | pass | |
| V9 comment policy | pass | |
| V10 phase acceptance | pass | TestEncryptedRangeCrossingChunksReturnsExactBytes, TestCacheHitMakesZeroTelegramCalls and TestConditionalGETAndInvalidRangeFollowHTTPRules all reported `--- PASS`. |
| V11 apps modules | n/a | |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 10 commits. Author and committer are prieyan on all. No bodies, no merges, no bad subjects. |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | All four packages. |
| faketg acceptance | yes | Real put through phase 06 `core.NewWrite`, then reads through `core.NewRead`. |
| SQLite and real crypto | yes | TDE1 encrypted ranges crossing chunk boundaries. |
| Real disk block cache | yes | t.TempDir(). The index reloads across a restarted `Read`. |
| Race detector | yes | `./internal/...`. |
| Real Telegram | no | |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| none in section 8 | n/a | n/a | n/a |

The phase ran under the owner's standing instruction: "continue everything i going to sleep dont ask me anything do the best".

## Module changes

None.

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |
| backend/media/range_reader.go and cache.go (with tests) | internal/readpath/rangereader | L1 |
| backend/thumbnail/cache.go (pattern) and cache_test.go (pattern cases) | internal/readpath/blockcache | L2 |
| backend/mountcontent/reader.go, document_cache.go, document_resolution.go and peer_cache.go; backend/mountcache/lru.go and coalescer.go (with tests) | internal/readpath/objectreader | L3 |
| backend/media/server.go (range parts); backend/mountdav/conditions.go and serializeEntityTag; backend/mountadapter/conditions.go (pattern) | internal/readpath/httprange | L4 |

All rows are in `docs/legal/THIRD_PARTY_NOTICES.md`.

## Deviations and minor findings

- **Prelude:** `internal/readpath/contract.go` adds two ports:
  - `DocumentRangeReader`: the block port that rangereader consumes and that blockcache both decorates and implements.
  - `StoredReader`: rangereader's `ReadStoredAt`, which objectreader consumes.

  The stack is objectreader → rangereader → blockcache → tgclient rangeio.
- **L1 (rangereader):**
  - Lifecycle is `Start`/`Close(ctx)`, with Sleeper and DocumentResolver ports.
  - The global getFile slot pool is dropped. rangeio takes the foreground slot, and the reader keeps a local background lane.
  - `Throughput` is dropped; it is phase 11 metrics work.
  - Minor: `ErrInvalidRetryPolicy` is a local sentinel.
  - Minor: the package-level `transientTransportPatterns` var should become a function (claude.md §6.4).
- **L2 (blockcache):**
  - Cache keys use the byte offset, not a block index.
  - It has no clock.
  - `cache.min_free_bytes` is not implemented, because fsx has no statfs.
  - `Warm` exists, but nothing calls it on put yet.
  - Minor: some sentinels are local, now prefixed.
- **L3 (objectreader):**
  - Nil `Keys` is rejected.
  - The DEK is cleared immediately after the decryption subkey is derived.
  - Minor: the invariant 7 generation gate is not implemented. No decrypting reader is cached across requests, and core must never cache an `Opened`.
  - The coalescer and LRU are exported for reuse in phase 13.
- **L4 (httprange):**
  - Only one comment remains, a single-line external-constraint note.
  - A malformed If-Match or If-None-Match is ignored, as AWS does.
  - The package returns typed errors and outcomes only.
- **INT:**
  - `core.NewRead` builds `Read`, a lifecycle.Component.
  - `serve` does not register `Read`, consistent with phases 05 and 06.
  - Warm-on-put and `cache.min_free_bytes` are not wired.
  - After a restart, the cache-hit path makes zero range reads but one ResolveDocument call, because the document-reference cache is in memory only.

## Phase document defects

- §4 names `Source` but gives no owner. L3 exports the implementation.
- The lane briefs do not say how blockcache stacks relative to rangereader.

## Open questions for the owner

Carried from phase 06, still open:

- Serve-time startup of sync, write and read is not wired.
- Put retry pacing.
- The `upload_jobs` CHECK gap.
- Sentinels belong in `internal/domain/errs`.
- The clock timer port.
- The deleteChannel RPC.

New in this phase:

- Should warm-on-put and the invariant 7 generation gate be wired in phase 08?

## Worktrees and branches left in place

None.
