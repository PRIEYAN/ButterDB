# Phase 04 status — Telegram transport and identity

Result: accepted
Date: 2026-11-15
Phase branch: phase/04-telegram-transport @ aa8d570 (before this note)
Main before: 668b534
Main after: the commit that records this note
Prelude commit: d62c211
Lanes head: f8a3fef
Stopped at: none
Resume point: none

## Lanes

Lane branches follow the phase document's lanes table (`phase/04-telegram-transport-and-identity-<lane>`), not the phase slug. L1 to L6 were dispatched, reviewed, accepted and picked in an earlier orchestrator session. That session ended before it wrote a status note, and its monitor and reviewer reports were not preserved. The attempt counts below come from the lane branches: a lane with a follow-up fix commit after its brief's commits is counted as two attempts in the same worktree, and no `-r<k>` retry branch exists. Per §9, a lane is picked only after a monitor PASS and a reviewer ACCEPT.

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 internal/tgclient/conn | phase/04-telegram-transport-and-identity-l1 | 2 (inferred) | PASS (earlier session) | ACCEPT (earlier session) | 3 |
| L2 internal/tgclient/flow | phase/04-telegram-transport-and-identity-l2 | 2 (inferred) | PASS (earlier session) | ACCEPT (earlier session) | 4 |
| L3 internal/tgclient/{send,rangeio} | phase/04-telegram-transport-and-identity-l3 | 1 (inferred) | PASS (earlier session) | ACCEPT (earlier session) | 2 |
| L4 internal/tgclient/{rpc,authrpc} | phase/04-telegram-transport-and-identity-l4 | 2 (inferred) | PASS (earlier session) | ACCEPT (earlier session) | 4 |
| L5 internal/tgclient/faketg | phase/04-telegram-transport-and-identity-l5 | 2 (inferred) | PASS (earlier session) | ACCEPT (earlier session) | 2 |
| L6 internal/identity, internal/storage/accountrepo | phase/04-telegram-transport-and-identity-l6 | 2 (inferred) | PASS (earlier session) | ACCEPT (earlier session) | 4 |
| INT integration | phase/04-telegram-transport-and-identity-int | 2 | PASS | ACCEPT | 5 |

INT attempt 1 was cut off by a session shutdown after it had written six uncommitted files in `internal/core`. It never reported. Attempt 2 ran in the same worktree, reviewed those files, kept them and finished the brief. A relaunch of attempt 2 had also been interrupted by a session restart before it wrote anything, and it is not counted.

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| d62c211 | prieyan | define telegram transport contracts |
| 262a9e6 | prieyan | port telegram connection scope and media pools |
| ed7d766 | prieyan | add document reference resolution and refresh |
| 462005b | prieyan | export closed scope sentinel from conn |
| c505673 | prieyan | port flood wait retry policies |
| 2433425 | prieyan | port write coordinator and getfile limiter |
| 49d1861 | prieyan | add default on send rate limiter |
| 4fe79ec | prieyan | test typed flood wait recording in write coordinator |
| 292a62b | prieyan | port idempotent document and control sends |
| 09263ec | prieyan | port bounded range reads with cdn verification |
| 53b52e2 | prieyan | add channel history and message rpcs |
| 7b3357c | prieyan | add login rpcs |
| 1a1e186 | prieyan | classify uncertain channel creation as unknown outcome |
| f727e5d | prieyan | tighten rpc test and package state |
| 0922506 | prieyan | add scriptable fake telegram client |
| 2f7c6ea | prieyan | align fake telegram errors and edge cases with adapters |
| 4dd305a | prieyan | add telegram login state machine |
| de7e45b | prieyan | add sealed session storage and account repository |
| df0c148 | prieyan | lease session keys through a plain byte port |
| f8a3fef | prieyan | assert credentials file mode is 0600 |
| e81f430 | prieyan | wire telegram transport and identity in core |
| 5195a3d | prieyan | add setup driver and tgprobe runner to core |
| ddf1797 | prieyan | add setup and tgprobe subcommands |
| 1d4b86f | prieyan | add phase 04 acceptance tests |
| 2fb5447 | prieyan | record phase 04 ported files in notices |
| aa8d570 | prieyan | tidy module requirements for phase 04 |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | includes test/acceptance/phase01 to phase03 |
| V6 race tests | pass | `./internal/...` (section 7 lists no packages) |
| V7 module integrity | pass | |
| V8 layering | pass | |
| V9 comment policy | pass | |
| V10 phase acceptance | pass | TestUncertainSendRetriesWithStableRandomID, TestFloodWaitAndTransportRetryBudgetsAreSeparate, TestRangeReadsVerifyCDNHashes, TestLoginStateSurvivesRestart all `--- PASS` |
| V11 apps modules | n/a | no `apps/` module yet |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 26 commits, author and committer prieyan, no bodies, no merges, no bad subjects |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | every tgclient subpackage, identity, accountrepo, core, cmd |
| faketg acceptance | yes | the four section 7 cases, plus probe and setup tests in core |
| SQLite in t.TempDir() | yes | control.db with accountrepo in core, cmd and acceptance |
| Race detector | yes | `./internal/...`; the INT worker also ran it over `./cmd/...` and `./test/acceptance/phase04` |
| Telegram test DC smoke | no | gated: `TestTelegramTestDCSmoke` (build tag `tgtest`) skips because BUTTERDB_TGTEST is unset. It compiles and vets under the tag, but it has never run against Telegram |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| Test-DC credentials (section 8, optional) | Supply BUTTERDB_TGTEST_API_ID and BUTTERDB_TGTEST_API_HASH for the gated smoke test? | Not supplied. The owner instructed: "continue, dont ask me anything,, even it's important dont ask me" and "if anything requried my suggestion i give permision to yourself to make a effeicney option" | Closed as a gated skip, which section 8 allows ("Record a gated skip when they are absent") |

## Module changes

| Module | Package paths | Version | Added by |
| --- | --- | --- | --- |
| github.com/gotd/td | github.com/gotd/td/telegram and the gotd subpackages the adapters import | v0.145.1 | prelude |
| golang.org/x/time | golang.org/x/time/rate | v0.16.0 | prelude |
| golang.org/x/sync | promoted from indirect to direct by tidy | v0.23.0 | tidy |

Tidy moved gotd/td, x/sync and x/time to direct requirements and added test-only go.sum entries. The `go` line stayed at 1.26.0 and no `toolchain` line appeared.

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |
| backend/tgclient/client.go, range.go | internal/tgclient/types.go | prelude |
| backend/tgclient/session.go, gotd_pool.go | internal/tgclient/conn | L1 |
| backend/tgclient/gotd.go | internal/tgclient/conn, send, rpc | L1, L3, L4 |
| backend/tgclient/gotd_range.go | internal/tgclient/rangeio, conn | L3, L1 |
| backend/tgclient/gotd_media.go, download.go | internal/tgclient/rangeio | L3 |
| backend/tgclient/floodwait_retry.go, write_coordinator.go, getfile_limiter.go, transient.go | internal/tgclient/flow | L2 |
| backend/tgclient/idempotency.go, upload_part_retry.go, upload_media_retry.go | internal/tgclient/send | L3 |
| backend/tgclient/personal_drive.go, backend/auth/peer_resolution.go | internal/tgclient/rpc | L4 |
| backend/tgclient/fake.go | internal/tgclient/faketg | L5 |
| backend/auth/session_storage.go, backend/livesync/activity.go | internal/tgclient/conn | L1 |
| backend/auth/auth.go | internal/tgclient/authrpc, internal/identity, internal/tgclient/conn | L4, L6, L1 |
| backend/services/auth/service.go | internal/identity | L6 |

All rows are recorded in `docs/legal/THIRD_PARTY_NOTICES.md` §2.1. `cmd/tdrive/auth_drive_commands.go` is listed there as a design reference for `setup`. The INT worker confirmed that the file exists but did not read it.

## Deviations and minor findings

- INT reviewer minor 1 (R6): core and cmd add package-local sentinels instead of using `internal/domain/errs`:
  - `errSessionKeyUnavailable` (identityports.go)
  - `errTelegramClosed`
  - `ErrAppNotStarted`
  - `ErrSetupAborted`
  - `ErrProbe*`
  - `errProbeDeclined`

  The reviewer suggests at least wrapping `errs.ErrKeyUnavailable` for the session-key lease failure. Not fixed; carried forward.
- INT reviewer minor 2 (R3): `ProbeOptions.UploadFloodAbort` (internal/core/probe.go) is set but never read. The 60 s ceiling comes from `probeUploadFloodCeiling` in probewiring.go. Not fixed; carried forward.
- core reads secret files through a private `osFileReader` (`os.ReadFile`), because `fsx.FS` has no read method.
- TestRangeReadsVerifyCDNHashes drives the real `rangeio.Stream` and core retry policy against faketg's `FaultCDNRedirect`. The real `verifyFileHashes` and AES-CTR need gotd types, which `test/` may not import (L2). The rangeio unit tests (`TestVerifyFileHashes*`, cdn_test.go) are the only cover for them.
- TestLoginStateSurvivesRestart builds `identity.Service` on faketg, a real control.db and a real keyring, because core's lazy auth is bound to the gotd transport. An unfinished login attempt is not persisted, by design: after a restart a stale login id gets `ErrLoginNotActive`.
- tgprobe differs from operations.md §13.5:
  - With no `ops_channels` table before phase 11, it finds the probe channel by the title "butterDB probe".
  - The control step uses a core-private pass-through gate with zero retries, so it bypasses the E10 limiter, as the probe requires.
  - `home_dc` is read from the uploaded document's DC.
  - A 2 MiB average object size, derived from D4, feeds the documents-per-minute recommendation.
  - `mb_per_s` is decimal.
  - The report is always written, with mode 0600.
- `setup` accepts `--role primary|replica` and needs the process lock (the server must be stopped). It hides terminal echo only on Linux.
- Telegram failures exit with code 6 (operations.md §4.2).
- The smoke test assumes test-DC phone `+9996610001` and code `11111` (the 99966XYYYY convention). It is unverified.
- Lane monitor and reviewer reports for L1 to L6 were lost with the earlier session. Their minor findings, if any, are not recorded here.
- The orchestrator did not stop to ask the owner at any point, on the owner's explicit instruction. Every self-made choice is recorded in this note.

## Phase document defects

- The lanes table names branches with slug `telegram-transport-and-identity`, while the file slug is `telegram-transport`. The table wins (§1.2), so the phase branch is `phase/04-telegram-transport` and the lane branches carry the longer slug.
- Section 6 tells the integration lane to cherry-pick and to run `go mod tidy`. Under §9 and §10.3 both belong to the orchestrator, and they were done that way.
- Section 7 lists no race package set, so V6 used `./internal/...`.

## Open questions for the owner

- Should `fsx.FS` gain a `ReadFile`, so core can drop `osFileReader`?
- `core.App.OpenTelegram` (TelegramSession) opens its own control.db handle. When phase 05 lands the serve wiring, it should share the single writer instead.
- Should the package-local sentinels in core and cmd (deviation 1), and those carried from phase 03, move into `internal/domain/errs` with stable codes?
- `GOOS=windows go build ./cmd/...` fails in `internal/platform/processlock` (`tryLockFile` and `processRunning` are undefined). This predates phase 04 and lies outside its paths. Is Windows a server target?
- Test-DC smoke credentials are still outstanding if you want that evidence class run.

## Worktrees and branches left in place

none
