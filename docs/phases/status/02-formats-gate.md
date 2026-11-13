# Phase 02 status — Formats gate (TDX1 object ops, TDE1 container, schema v1)

Result: accepted
Date: 2026-11-13
Phase branch: phase/02-formats-gate @ 3fc84ef (before this note)
Main before: 5736991
Main after: the commit that records this note
Prelude commit: 9ed56bd
Lanes head: 030e62c
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 wire core and op catalogue (L1 and L2 merged by the owner) | phase/02-formats-gate-l1 | 2 | PASS | ACCEPT | 5 |
| L3 TDE1 format | phase/02-formats-gate-l3 | 1 | PASS | ACCEPT | 1 |
| L4 schema and sqlite | phase/02-formats-gate-l4 | 1 | PASS | ACCEPT | 2 |
| L5 domain model | phase/02-formats-gate-l5 | 2 | PASS | ACCEPT | 2 |
| INT integration | phase/02-formats-gate-int | 1 | PASS | ACCEPT | 2 |

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| 9ed56bd | prieyan | define phase 02 format contracts |
| eeb198b | prieyan | port tdx1 grammar parser and formatter |
| 88e3b3a | prieyan | add tdx1 object and registry ops |
| bfaf9cc | prieyan | add tdx1 round trip and malformed case tables |
| 8bce1e2 | prieyan | reject empty op type as malformed |
| 9ea5ecf | prieyan | require canonical base64 in binary fields |
| 1b2385f | prieyan | port tde1 header nonce and size arithmetic |
| 9d868f4 | prieyan | add sqlite writer and read pool adapter |
| ce3f243 | prieyan | add schema version one migrations |
| a4cfa1d | prieyan | add object bucket and access domain validation |
| 030e62c | prieyan | add access key info domain type |
| 5f8ed7b | prieyan | add phase 02 acceptance tests |
| 5aea152 | prieyan | record phase 02 ported files in notices |
| c1096bc | prieyan | tidy module requirements for phase 02 |
| 3fc84ef | prieyan | record phase 02 format clarifications |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | includes test/acceptance/phase01 |
| V6 race tests | pass | `./internal/... ./cmd/...` |
| V7 module integrity | pass | |
| V8 layering | pass | |
| V9 comment policy | pass | |
| V10 phase acceptance | pass | TestEveryOpParsesAfterFormatting, TestMalformedWireDataReturnsTheSpecifiedError, TestUnknownOpRemainsAReplayableReject, TestSchemaMigrationsBuildAndIntegrityCheck, TestTDE1SizeVectorsMatch all `--- PASS`, plus nine extra acceptance tests |
| V11 apps modules | n/a | no `apps/` module yet |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 15 commits, author and committer prieyan, no bodies, no merges, no bad subjects |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | |
| Fuzz targets | yes | FuzzParseNoPanic and FuzzRoundTrip, short runs, no failures |
| Byte-exact spec vectors | yes | all 12 wire-format §10 examples, verified independently by the reviewer against the spec text |
| SQLite in t.TempDir() | yes | real schema v1 for both databases; DDL byte-identical to schema.md §4.1 and §5.1 |
| faketg acceptance | no | no Telegram code yet |
| Projector-level unknown-op handling | no | no projector until phase 03; proven at the wire and schema level |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| Key fingerprint format, asked before the prelude | bkf1_ + 32 hex or base64url 22 chars? | bkf1_ + 32 hex (Recommended) | yes, recorded as E15 |
| Wire lane split, asked before the prelude | How should the L1/L2 cross-dependency be resolved? | Merge L1+L2 into one lane (Recommended) | yes |
| Format freeze sign-off (section 8) | Sign off TDX1, TDE1 and schema v1, with the empty-`t` and canonical-base64 rules? | Sign off, freeze formats | yes, clarifications recorded as E16 |

## Module changes

| Module | Package paths | Version | Added by |
| --- | --- | --- | --- |
| modernc.org/sqlite | modernc.org/sqlite | v1.45.0 | prelude |

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |
| backend/projection/wire.go (+ wire_test.go cases) | internal/wire | L1 |
| backend/crypto/stream.go (constants, size arithmetic, nonce, header) | internal/crypto/format | L3 |
| backend/crypto/random_access.go (StoredOffset, ciphertextChunkOffset) | internal/crypto/format | L3 |
| backend/crypto/ciphertext_size_test.go, stored_offset_test.go (cases) | internal/crypto/format | L3 |
| backend/storage_sqlite.go (pragma values only) | internal/storage/sqlitedb | L4 |

## Deviations and minor findings

- The owner merged lanes L1 and L2 into one wire lane. Dispatch is two switch functions in `internal/wire/ops_catalog.go`; there is no prelude registration hook.
- The prelude could not hold bodyless Go functions or an empty `sqlitedb.DB`. `internal/wire/types.go` and `internal/storage/sqlitedb/contract.go` hold only types, constants and sentinels. The lanes implemented every spec function with the exact signature.
- `schema.Apply` follows schema.md §3.3, `Apply(ctx, db *sqlitedb.DB, set Set) ([]int, error)`, not the phase brief's `Apply(ctx, db, name string) error`.
- `ErrWrongDatabase` lives in `sqlitedb` because of an import cycle, and `schema.ErrWrongDatabase` aliases it.
- `sqlitedb.OpenWith` takes an injected clock and fsx. `Close` checkpoints under a background context bounded by the busy timeout, because the contract gives `Close` no context.
- Pragmas are applied through the DSN `_pragma` parameters, so modernc runs them on every connection. They are not applied through a custom connector hook.
- The wire, crypto/format and domain packages duplicate limits constants by design (layering L1). Acceptance asserts every exported pair equal. format's 1900 MiB cap and object's multipart ceiling are unexported and are checked only indirectly.
- L5 implemented the bucket naming rules from storage-model.md §5.1, because the brief's pointer, s3-api §6.4, covers CreateBucket and DeleteBucket. It added `access.KeyInfo` and `ValidateKeyName` after review, since admin-api §16.8 and phase 08 require them.
- The wire lane's first commit carried a placeholder `ops_catalog.go` so the commit would build. The next commit replaced it.
- Shared keys `sz`, `psz`, `enc`, `ev` and `ts` are range-checked on every op type and stored only on ops that use them.
- In docs/decisions.md, E15 recorded the fingerprint format. operations.md, security.md and phase documents 01 and 11 were corrected in the prelude commit.

## Phase document defects

- The §4 prelude contract lists bodyless functions and `type DB struct{}`. Neither compiles, and the empty struct could not hold state.
- The §5 split between L1 and L2 makes two halves of one Go package depend on each other, and the prelude has no registration hook that L2's brief requires.
- The §5.4 `schema.Apply` signature differs from schema.md §3.3.
- §5.5 points to s3-api §6.4 for bucket names; the rules are in storage-model.md §5.1.
- wire-format.md §12 places the phase 02 round trip "through the projector", which does not exist until phase 03.

## Open questions for the owner

- schema.md §2.1 and §3.3 should say that `ErrWrongDatabase` lives in `sqlitedb` and is aliased from `schema`.
- Carried from phase 01: `s3.domains` versus `s3.base_domains` and the skew key (resolve before phase 08); the home of `RangeReadAlignment` and `MaxMessageTextChars`; four error codes no spec names; `setup --key-only` timing.
- For phase 03: L3 noted that the TDE1 HKDF info string must be the butterDB one (`butterDB/file/v1`), not TDrive's.

## Worktrees and branches left in place

none
