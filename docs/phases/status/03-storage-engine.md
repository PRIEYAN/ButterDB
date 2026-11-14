# Phase 03 status — Storage engine

Result: accepted
Date: 2026-11-14
Phase branch: phase/03-storage-engine @ b4b7ace (before this note)
Main before: c03f256
Main after: the commit that records this note
Prelude commit: 7141cb6
Lanes head: 8b8b620
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 internal/projection/log | phase/03-storage-engine-l1 | 2 | PASS | ACCEPT | 2 |
| L2 internal/projection/apply | phase/03-storage-engine-l2 | 2 | PASS | ACCEPT | 3 |
| L3 internal/projection/rebuild | phase/03-storage-engine-l3 | 2 | PASS | ACCEPT | 3 |
| L4 internal/crypto/{stream,randomaccess} | phase/03-storage-engine-l4 | 1 | PASS | ACCEPT | 3 |
| L5 internal/crypto/keyring | phase/03-storage-engine-l5 | 1 | PASS | ACCEPT | 2 |
| L6 internal/storage/{objectrepo,bucketrepo} | phase/03-storage-engine-l6 | 1 | PASS | ACCEPT | 2 |
| INT integration | phase/03-storage-engine-int | 1 | PASS | ACCEPT | 3 |

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| 7141cb6 | prieyan | define projector and crypto contracts |
| eff7c7b | prieyan | port projection log with tamper and reject recording |
| 8e5a431 | prieyan | keep applier writes on duplicate op rejects |
| 1000038 | prieyan | add object and bucket op appliers |
| 4c2a659 | prieyan | add encryption config and registry marker appliers |
| b14bc43 | prieyan | keep duplicate put cleanup under projector savepoints |
| d442493 | prieyan | port projection rebuild |
| 6639973 | prieyan | add per key ordering repair |
| a810f94 | prieyan | record applied outcomes and match projector rejects in rebuild |
| ce83399 | prieyan | port tde1 stream encryption |
| 8be6535 | prieyan | add encrypting reader at for resumable sends |
| bb20600 | prieyan | port authenticated random access decryptor |
| 9bcd5d3 | prieyan | add server keyring with subkey derivation |
| d447325 | prieyan | add dek wrapping and secret sealing |
| 9be17ab | prieyan | add object repository with keyset listing |
| 8b8b620 | prieyan | add bucket repository |
| eb72a73 | prieyan | wire projector and key generation in core |
| 147dbf7 | prieyan | add phase 03 acceptance tests |
| 445c1d8 | prieyan | record phase 03 ported files in notices |
| b4b7ace | prieyan | tidy module requirements for phase 03 |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | includes test/acceptance/phase01 and phase02 |
| V6 race tests | pass | `./internal/... ./cmd/...` |
| V7 module integrity | pass | |
| V8 layering | pass | |
| V9 comment policy | pass | |
| V10 phase acceptance | pass | TestDuplicateProjectionIsIdempotent, TestTamperedReplayIsFatal, TestRebuildMatchesTheLiveProjection, TestCiphertextTamperAndTruncationFailAuthentication all `--- PASS`, plus TestPerKeyRepairEqualsInOrderProjection |
| V11 apps modules | n/a | no `apps/` module yet |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 20 commits, author and committer prieyan, no bodies, no merges, no bad subjects |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | |
| SQLite in t.TempDir() | yes | real schema v1; acceptance uses the real projector, appliers, rebuilder, keyring, stream and randomaccess together |
| Fuzz targets | yes | FuzzRandomAccessDecryptorChunkBoundaries, short runs |
| Independent known-answer vectors | yes | TDE1 stream golden vector cross-checked against PyNaCl; keyring HKDF, XChaCha20 and HMAC against hand-rolled references |
| faketg acceptance | no | faketg arrives in phase 04; TestNoTxAcrossNetwork uses a scripted 2 s delay instead |
| Telegram test DC smoke | no | phase 04 |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| none | Phase 03 section 8 lists no owner gate | n/a | n/a |

## Module changes

| Module | Package paths | Version | Added by |
| --- | --- | --- | --- |
| golang.org/x/crypto | golang.org/x/crypto/chacha20poly1305 | v0.57.0 | prelude |

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |
| backend/projection/log.go (+ log_test.go) | internal/projection/log | L1 |
| backend/projection/rebuild.go (+ test) | internal/projection/rebuild | L3 |
| backend/projection/encryption_rebuild.go (+ test) | internal/projection/rebuild | L3 |
| backend/crypto/stream.go (+ stream_test.go, ciphertext_size_test.go stream cases) | internal/crypto/stream | L4 |
| backend/crypto/random_access.go (+ test) | internal/crypto/randomaccess | L4 |
| backend/crypto/vault.go (wrap, key check, key generator; + vault_test.go cases) | internal/crypto/keyring | L5 |
| backend/projection/managed_messages.go (+ test) | internal/storage/objectrepo | L6 |
| backend/projection/reconcile.go (partial, + test) | internal/storage/objectrepo | L6 |

## Deviations and minor findings

- The projector contract (`internal/projection/contract.go`) was designed by the orchestrator, because storage-model.md §10 leaves the exact Go shape to the phase 03 prelude. Entry points are methods on constructed values (`log.NewProjector`, `apply.NewRegistry`, `rebuild.New`), as constructor injection requires.
- The projector has no clock. Schema v1 has no `seen_at` or `detected_at` column, and claude.md §6.4 and invariant 10 forbid the projector from reading the clock. The orchestrator's suggestion of a clock was wrong.
- A `duplicate_op` reject keeps the Applier's writes in both the projector and rebuild: the savepoint is released, not rolled back. A duplicate put therefore leaves its message queued in `pending_message_cleanup` (reason `duplicate`), as storage-model.md §6.4 requires. Every other reject rolls the Applier's writes back. This came out of the cross-lane review of L1, L2 and L3.
- `wrong_carrier` is enforced in the L2 Appliers, so rebuild also enforces it.
- L2 declares a `KeyGeneration` port (`Advance(bucketChannelID)`), which `bdel` and `encfg` call for invariant 7. No spec defines the keyring generation API. `internal/core.KeyGenerations` is a minimal per-bucket counter placeholder.
- Registry ops require a `channels` row of kind `registry` (storage-model.md §4.1 step 2); integration and rebuild insert it first.
- The encfg `kfp` check follows security.md §5.4 and §5.6. It compares against the newest marker below the encfg's message ID and rejects `kek_mismatch`. A `kfp` change on `t=reg` is accepted only when `w == "sys.rekey"`, because `pkfp` is not in the frozen grammar.
- Rebuild runs as one transaction across all channels (storage-model.md §10). operations.md §10.3's "one transaction per keyset batch" wording should be reconciled in the docs.
- After per-key repair (E6), `pending_message_cleanup` holds the same message IDs as in-order projection but can differ in `reason` and `queued_by_msg_id`. This is by design: rows are preserved and re-derived with INSERT OR IGNORE (storage-model.md §9.4, schema.md §7). The acceptance test compares that table by message-ID set.
- Live projection equals RebuildAll only when the caller runs the E6 repair trigger. Without it, a put that arrives below a recorded tombstone records `superseded` live but `applied` after rebuild.
- Package-local sentinels remain in several lanes, because `internal/domain/errs` is outside lane paths:
  - keyring: `ErrKeyFile*`, `ErrRingClosed` and others
  - stream and randomaccess: `ErrAuthFailed`, `ErrCiphertextSize` and others
  - rebuild: `ErrRegistryChannelAlone`, `ErrEncryptionConfigReplayInvalid`, `ErrUnexpectedOutcome`
  - objectrepo: `ErrInvalidContinuationToken`
  - bucketrepo: `ErrCorruptKeyRow`
  - log: unused `ErrChannelNotEmpty`

  keyring's `ErrDEKUnwrap`, `ErrDEKCheck` and `ErrSecretUnseal` wrap `errs.ErrKeyUnavailable`.
- L6 minor findings that were not fixed:
  - `managedSources` in objectrepo is a package-level variable; it should be a function.
  - One carried TDrive test was renamed (`TestManagedMessageLookupsUseIndexes`).
  - The listing query uses `sqlite_autoindex_objects_1` with SEARCH, not COVERING; schema.md §4.8/§9's "COVERING" claim is wrong.
- L5 minor findings:
  - `Load` follows a chain of symlinks, while the spec says to follow once; the final target is still checked.
  - `Generate` checks for an existing file and then writes, which is not atomic.
  - operations.md §2.5's root-owned group-read warning has no channel out of `Load`.
- L4: `EncryptingReaderAt` cannot detect a plaintext source that is longer than declared.
- TestNoTxAcrossNetwork uses a scripted delay instead of faketg. Point it at faketg once phase 04 lands.
- Worker commits omit the session attribution trailer: claude.md §12.2 and the commit-msg hook forbid trailers.
- During the run, a stray untracked `go.work.sum` appeared in the main checkout. It came from an orchestrator `go doc` call in workspace mode, and was removed before any lane was picked.

## Phase document defects

- §4 names the prelude ports but gives no Go shape for `Tx`, `Writer`, `Outcome`, `Applier` or `Registry`.
- §5.1 names `seen_at`/`detected_at`, which do not exist in schema v1. It also assigns the managed_messages and reconcile tests to L1, although they belong to L6.
- §5.5's fingerprint rule was corrected to E15 in the prelude.
- No lane brief assigns `wrong_carrier`.
- No document defines the keyring generation API that invariant 7 needs.

## Open questions for the owner

- **encfg `kfp` check:**
  - security.md §5.4 and §5.6 reject a mismatching `kfp` (implemented).
  - storage-model.md §8.3 says the projector does not reject it, and orders rotation differently.

  Reconcile before phase 11.
- **Key rotation `pkfp`:** security.md §5.6 needs an optional `pkfp` on `t=reg`, which the frozen phase 02 grammar lacks. It can be added later with an `fv` bump and a new decision entry. Until then, rotation is recognised by `w=sys.rekey` (storage-model.md §4.2).
- **storage-model.md §8.1 is stale against security.md:**
  - It describes the key file as 64 hex characters.
  - It omits `kid` from the DEK aad.
  - It names the subkey `K_dek` instead of `K_wrap`.

  security.md was followed.
- **Projection repair trigger (carry-forward to phases 05 and 06):** the caller of `ProjectFromOpTx` owns the E6 trigger and must run `rebuild.NeedsKeyRepair` plus `RepairKeyTx` in the same transaction for every put and del.
- **Error sentinels:** should the package-local sentinels listed above get stable codes in `internal/domain/errs`? The S3 layer must map `ErrInvalidContinuationToken` to `400 InvalidArgument`.
- **Docs to correct:** schema.md §4.8/§9 "COVERING", operations.md §10.3 rebuild transaction wording, and schema.md §2.1/§3.3 on where `ErrWrongDatabase` lives (carried from phase 02).

## Worktrees and branches left in place

none
