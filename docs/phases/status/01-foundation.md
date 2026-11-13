# Phase 01 status — Foundation and guardrails

Result: accepted
Date: 2026-11-13
Phase branch: phase/01-foundation @ e79ed78 (before this note)
Main before: 8d482ac
Main after: the commit that records this note
Prelude commit: 27c182f
Lanes head: 1400e71
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 platform | phase/01-foundation-and-guardrails-l1 | 2 | PASS | ACCEPT | 6 |
| L2 config and logging | phase/01-foundation-and-guardrails-l2 | 2 | PASS | ACCEPT | 5 |
| L3 lifecycle and errors | phase/01-foundation-and-guardrails-l3 | 1 | PASS | ACCEPT | 2 |
| L4 tooling | phase/01-foundation-and-guardrails-l4 | 2 | PASS | ACCEPT | 6 |
| INT integration | phase/01-foundation-int | 1 | PASS | ACCEPT | 4 |

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| 27c182f | prieyan | set up butterdb foundation contracts |
| 2da5f29 | prieyan | add clock and filesystem ports |
| 0afdef4 | prieyan | add data directory resolution and mode checks |
| 5e14d5f | prieyan | add atomic private secret file writer |
| bea07c5 | prieyan | add process lock with guard file |
| 409c875 | prieyan | confine filesystem access with os root |
| e51d9f0 | prieyan | remove package level temp prefixes |
| 792b559 | prieyan | add configuration loading with environment overrides |
| 782c827 | prieyan | remove package level values from config |
| 4c75fb3 | prieyan | add structured logging with redaction |
| eee4e9d | prieyan | fix configuration error wording |
| 9d6b998 | prieyan | reject empty required configuration paths |
| b301829 | prieyan | add domain error sentinels and limits |
| e4a8448 | prieyan | add lifecycle group with reverse close |
| 009b647 | prieyan | add layering and comment policy checks |
| b4418af | prieyan | add verify script and worktree helpers |
| 6efa0ce | prieyan | add task runner file |
| 93d5c24 | prieyan | store script fixtures as inert files |
| 42a66af | prieyan | restrict domain packages to the standard library |
| 1400e71 | prieyan | reject doc comments on exported declarations |
| bc16165 | prieyan | add core skeleton |
| 21a40db | prieyan | add butterdbd version and check-config commands |
| 463329e | prieyan | add phase 01 acceptance tests |
| ad4e1fc | prieyan | record phase 01 ported files in notices |
| e79ed78 | prieyan | tidy module requirements for phase 01 |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | |
| V6 race tests | pass | `./internal/... ./cmd/...`; section 7 names no race list |
| V7 module integrity | pass | |
| V8 layering | pass | |
| V9 comment policy | pass | |
| V10 phase acceptance | pass | TestConfigurationRejectsUnknownAndUnsafeSecretValues, TestProcessLockPreventsASecondServer, TestLayeringAndNoCommentChecksEnforceRepositoryRules all `--- PASS` |
| V11 apps modules | n/a | no `apps/` module yet |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 25 commits, author and committer prieyan, no bodies, no merges, no bad subjects |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | |
| Real filesystem in t.TempDir() | yes | data directory, secret files, process lock, os.Root confinement |
| Built binary as subprocess | yes | check-config exit 2/0; second serve exits 3; SIGTERM exits 0 |
| Script fixtures | yes | scripts/test/run.sh, 16 cases |
| faketg acceptance | no | no Telegram code in phase 01 |
| Telegram test DC smoke | no | not in phase 01 |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| A10 project license, asked before the prelude | Owner gate A10: which license should butterDB use? | Apache-2.0 | yes |
| A10 project license, confirmed after verification | Confirm Apache-2.0 so I can write the status note and fast-forward main (local only)? | Confirm Apache-2.0 | yes |

## Module changes

| Module | Package paths | Version | Added by |
| --- | --- | --- | --- |
| golang.org/x/sys | golang.org/x/sys/unix | v0.48.0 | prelude |
| github.com/BurntSushi/toml | github.com/BurntSushi/toml | v1.6.0 | prelude |

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |
| backend/datadir/datadir.go (+ test) | internal/platform/datadir | L1 |
| backend/processlock/lock.go (+ lock_test.go, owner_process_test.go) | internal/platform/processlock | L1 |
| backend/processlock/lock_unix.go | internal/platform/processlock | L1 |
| backend/processlock/process_unix.go | internal/platform/processlock | L1 |
| backend/auth/private_file.go (+ test) | internal/platform/secretfile | L1 |

## Deviations and minor findings

- Before the phase, the owner approved fast-forwarding `main` from e65a8b3 to the documentation branch `docs/implementation-plan` (8d482ac). The untracked MIT draft `LICENSE` was moved out of the tree, and the prelude committed the canonical Apache-2.0 text instead.
- `go.mod` declares `go 1.26.0`, not `go 1.26`. `golang.org/x/sys v0.48.0` declares `go 1.26.0`, and the read-only build fails with `go 1.26`. It is the same release, so no newer toolchain is required. `go.work` uses `go 1.26.0` to match.
- The prelude wrote no Go contract files. Each lane's packages were independent, so lanes chose their own signatures within the spec.
- `logging.New(w io.Writer, cfg config.LogConfig)` follows operations.md §14.1. Phase 01 §5.2 says `New(w, level slog.Level)`, but the spec outranks the phase document.
- L1: lane-local sentinels in datadir, secretfile and processlock. `fsx.FS` has 8 methods. processlock calls `os` directly because flock needs an `*os.File`. `fsx.NewRoot` gives os.Root confinement, and `internal/core` must pass `Rooted` wherever confinement is required.
- L2: local `config.ErrInvalid`. The §2.8 cross-key checks live in `cmd/butterdbd`. `media.presets` and `media.buckets` are rejected as unknown keys until phase 13 extends `MediaConfig`.
- L3: extra sentinels whose codes are not named by any spec: `access.key_not_found`, `access.key_revoked`, `encryption.key_unavailable`, `object.empty`. `limits.DefaultCloseTimeout = 30s` is not in storage-model.md §7. The worker ran a bare `git reset -q` once to unstage; it changed only the index.
- L4: commits 009b647 to 6efa0ce briefly held real `go.mod` and `go.work` fixture files under `scripts/test/fixtures/`. Commit 93d5c24 renamed them to `.fixture`, and no module file of the repository was touched. The doc-comment rule in `nocomments.sh` flags a lone comment above an exported top-level declaration only when the comment starts with the identifier. `shellcheck` and `task` were not installed, so neither was run.
- INT: `butterdbd serve` is a skeleton that only takes the lock, so the exit-3 mapping can be proven end to end. The `version` and `commit` package-level vars exist for `-ldflags -X`. `errUsage` is local to cmd. check-config omits the `kek_fingerprint` comparison and output (phase 03) and the media preset checks.
- INT: `scripts/test/run.sh` case `layering_rejects_app_importing_internal` fails when `GOWORK=off` is exported. The acceptance test strips `GOWORK`. This is a script defect for a later phase.
- Notices: the rows for `backend/applog/applog.go` and `internal/app/operation.go` are still `planned`. L2 reported that it did not open applog.go. L3 reported that it opened operation.go as a pattern reference. The next integration lane that edits notices should mark them `not used: pattern only, not consulted` and `referenced b301829`.
- Integration commits were picked with `cherry-pick --ff`, so the hashes recorded in the notices match the phase branch.

## Phase document defects

- The lane branch names in §5 (`phase/01-foundation-and-guardrails-<lane>`) do not match the protocol slug `foundation`. The table won, as the protocol directs.
- §4 names no `.gitignore` content and no contract files. §5.2 gives a `logging.New` signature that differs from operations.md §14.1.
- §5.4 does not require `nocomments.sh` to detect doc comments, which claude.md §8.1 forbids.

## Open questions for the owner

- Key fingerprint format: operations.md §5.2 and §2.8 say base64url, 22 characters. storage-model.md §8.1 says `bkf1_` plus 32 hex characters. This must be resolved before phase 03 (keyring).
- s3-api.md §2.1 names `s3.domains` and `s3.max_clock_skew`. operations.md §2.4 names `s3.base_domains` and makes the skew a constant. Phase 01 implemented operations.md. This must be resolved before phase 08.
- Should `limits.RangeReadAlignment` live in `internal/domain/limits` (claude.md, operations.md) or in `internal/tgclient` (tdrive-port-map)? And `MaxMessageTextChars` (plan.md) or `MaxPutOpChars` (spec)?
- Should the four invented error codes be added to claude.md §8.4 and the API mapping tables?
- Should `butterdbd setup --key-only` arrive before the first `serve`?

## Worktrees and branches left in place

none
