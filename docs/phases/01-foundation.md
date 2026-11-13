# Phase 01 — Foundation and guardrails
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement foundation and guardrails as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

The foundation documents have been reviewed. The owner has chosen the project license (owner gate A10) before the prelude commits `LICENSE`.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/decisions.md`.
- `docs/plan.md`.
- `docs/phases/00-execution-protocol.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Create root `go.mod` for `github.com/PRIEYAN/ButterDB` with `go 1.26`, `go.work`, `.gitignore`, the commit hook, `.claude/settings.json`, and the root `CLAUDE.md` pointer. Add `golang.org/x/sys/unix@v0.48.0` for the process lock and `github.com/BurntSushi/toml@v1.6.0`. Commit `docs/legal/THIRD_PARTY_NOTICES.md` with the TDrive MIT notice. The contracts for config, data directory, and process lock are in `docs/spec/operations.md` §§2–6. Commit as `set up butterdb foundation contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/01-foundation-and-guardrails-l1` | platform: internal/platform/{datadir,processlock,secretfile,clock,fsx} (port TDrive backend/datadir, backend/processlock, backend/auth/private_file.go) |
| L2 | `phase/01-foundation-and-guardrails-l2` | config and logging: internal/platform/{config,logging} |
| L3 | `phase/01-foundation-and-guardrails-l3` | lifecycle and errors: internal/lifecycle, internal/domain/{errs,limits} |
| L4 | `phase/01-foundation-and-guardrails-l4` | tooling: scripts/{layering.sh,nocomments.sh,verify.sh,worktree-*.sh}, Taskfile.yml |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — platform

**Objective.** Provide the platform primitives every later package depends on: data-directory resolution with mode enforcement, the single-instance process lock, the atomic private-file writer for secrets, and the clock and filesystem ports.

**Owned paths.** `internal/platform/datadir/`, `internal/platform/processlock/`, `internal/platform/secretfile/`, `internal/platform/clock/`, `internal/platform/fsx/`.

**Read.** `docs/spec/operations.md` §3.1–§3.2 (layout and modes), §6 (single instance); `docs/spec/security.md` §16 (phase 01 rows); `docs/claude.md` (no ambient state: time and filesystem are ports); legacy spec `docs/butterDBCore.md` lines 684–730 (secret-file primitive).

**Rules.**
- The data directory must be `0700`; a looser mode refuses start (`TestDataDirRefusesLooseMode`). There is no environment-variable configuration beyond what `internal/platform/config` defines.
- `secretfile.Write` creates the temp file in the same directory, chmods it `0600` **before** the first byte, writes, syncs, closes, renames and fsyncs the directory. A failure leaves the old file intact and removes the temp file. It never creates an empty session file.
- The process lock is an OS exclusive lock on `butterdbd.lock.guard`, plus `butterdbd.lock` holding role and PID diagnostics. Neither file is deleted on release. A second `serve` exits with code 3. Windows files are not ported (operations §6).
- `clock.Clock` has `Now() time.Time`, plus a fake for tests. `fsx.FS` is the narrow filesystem port that later lanes use for temp files, renames and stats. Neither package keeps global state.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/datadir/datadir.go` | 143 | PORT | `internal/platform/datadir` (L1) | Keep: owned-temp naming, `CreateCacheTemp`, `CleanupCacheTemps` that removes only owned scratch files and keeps going when one removal fails, directory mode `0o700`. Change: no package overrides or mutex; the root comes from configuration and the value is constructed once; temp prefixes per 5.5. Drop: `Set`, `SetCache`, mobile sandbox handling, `os.UserConfigDir` fallback |
| `backend/processlock/lock.go` | 249 | PORT | `internal/platform/processlock` (L1) | Keep: persistent `.guard` file holding the exclusive lock (never unlinked, so contenders lock the same inode), JSON owner metadata `role`, `pid`, `started_at`, `lock_version` published through a temp file, stale-owner reclaim, `ErrAlreadyRunning`, `Read`, `ReadActive`, `ProcessRunning`. Change: lock path injected; error text per 5.5 |
| `backend/processlock/lock_unix.go` | 18 | PORT | `internal/platform/processlock` (L1) | `unix.Flock(LOCK_EX\|LOCK_NB)` from `golang.org/x/sys/unix` |
| `backend/processlock/process_unix.go` | 13 | PORT | `internal/platform/processlock` (L1) | Process liveness check |
| `backend/auth/private_file.go` | 95 | PORT | `internal/platform/secretfile` (L1) | Keep: create temp in the target directory, `chmod 0o600` before writing, write, check short write, `fsync`, close, rename, `fsync` the directory (skipped on Windows); remove the temp on every failure path; directory mode `0o700`. Change: rename function injected, no package variable |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/datadir/datadir.go`: `func Set(dir string) {`; `func SetCache(dir string) {`; `func Dir() (string, error) {`; `func CacheDir() (string, error) {`; `func CreateCacheTemp(pattern string) (*os.File, error) {`; `func CleanupCacheTemps() error {`
- `backend/processlock/lock.go`: `type Info struct {`; `type Lock struct {`; `func Acquire(role string) (*Lock, error) {`; `func (l *Lock) Release() error {`; `func (l *Lock) Info() Info {`; `func Path() (string, error) {`; `func Read() (Info, error) {`; `func ReadActive() (Info, bool, error) {`

**Tests (binding names).** every carried TDrive case in this phase's regression table (datadir three cases, processlock all but `TestAcquireReclaimsLegacyRecordNamingOwnPID`, owner-process helper tests, the three private-file cases), plus `TestSecretFileModeBeforeWrite`, `TestDataDirRefusesLooseMode`, `TestProcessLockSecondInstance` (security §16).

**Verify.**

```bash
go vet -mod=readonly ./internal/platform/datadir/... ./internal/platform/processlock/... ./internal/platform/secretfile/... ./internal/platform/clock/... ./internal/platform/fsx/...
go test -mod=readonly -race ./internal/platform/datadir/... ./internal/platform/processlock/... ./internal/platform/secretfile/... ./internal/platform/clock/... ./internal/platform/fsx/...
gofmt -l internal/platform/datadir internal/platform/processlock internal/platform/secretfile internal/platform/clock internal/platform/fsx
```

**Commits.** `add data directory resolution and mode checks`, `add atomic private secret file writer`, `add process lock with guard file`, `add clock and filesystem ports`.

### 5.2 Lane L2 — config and logging

**Objective.** Load configuration from the TOML file with `BUTTERDB_*` environment overrides, validate every key, read secrets only from files, and provide JSON structured logging with redaction.

**Owned paths.** `internal/platform/config/`, `internal/platform/logging/`.

**Read.** `docs/spec/operations.md` §2.1–§2.8 (sources, types, env naming, every key, secret files, example, the §2.7 Go contract, `check-config`), §14.1–§14.4 (log format, standard fields, levels, redaction); `docs/spec/security.md` §16 row `TestConfigRejectsUnknownKeyAndEnv`.

Implement the operations §2.7 contract exactly. Logging exposes `func New(w io.Writer, level slog.Level) *slog.Logger` plus the redaction helpers that §14.4 names.

**Rules.**
- Precedence is file, then environment. Unknown keys and unknown `BUTTERDB_*` variables are errors. No key or variable may carry an inline secret; secrets come only through `*_file` paths.
- Every key in operations §2.4 has the documented type, range and default. Durations use Go syntax.
- Log lines are JSON `log/slog` with the §14.2 standard fields. Redaction covers tokens, secrets, api_hash, phone numbers (masked), passwords and session bytes. Redaction happens at the value type (`LogValue`), never by string search.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/applog/applog.go` | 105 | PATTERN-ONLY | `internal/platform/logging` (L2) | TDrive sets a global debug text handler writing to a file. butterDB builds a JSON `slog.Handler` on stdout, returns a `*slog.Logger`, never calls `slog.SetDefault` |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/applog/applog.go`: `func Init() {`; `func Close() {`

**Tests (binding names).** `TestConfigRejectsUnknownKeyAndEnv`, `TestPrecedenceFileThenEnv`, `TestEveryKeyHasDefaultAndRange` (table over operations §2.4), `TestSecretsOnlyFromFiles`, `TestExampleFileParses` (the §2.6 example), `TestDurationAndSizeParsing`, `TestLogStandardFields`, `TestRedactionByType`, `TestPhoneMasked`.

**Verify.**

```bash
go vet -mod=readonly ./internal/platform/config/... ./internal/platform/logging/...
go test -mod=readonly -race ./internal/platform/config/... ./internal/platform/logging/...
gofmt -l internal/platform/config internal/platform/logging
```

**Commits.** `add configuration loading with environment overrides`, `add structured logging with redaction`.

### 5.3 Lane L3 — lifecycle and errors

**Objective.** Provide the lifecycle group that starts components in order and closes them in strict reverse, the domain error sentinels with their stable codes, and the single file of size and count limits.

**Owned paths.** `internal/lifecycle/`, `internal/domain/errs/`, `internal/domain/limits/`.

**Read.** `docs/claude.md` (lifecycle and error rules); `docs/spec/storage-model.md` §7 (limits arithmetic); `docs/spec/s3-api.md` §14.2 and `docs/spec/admin-api.md` §4.2 (the codes the sentinels map to); `docs/spec/schema.md` and `docs/spec/wire-format.md` §6 (length limits).

```go
package lifecycle

type Component interface {
	Start(ctx context.Context) error
	Close(ctx context.Context) error
}

func NewGroup(closeTimeout time.Duration) *Group
func (g *Group) Add(name string, c Component)
func (g *Group) Start(ctx context.Context) error
func (g *Group) Close(ctx context.Context) error
```

`errs` exposes sentinel `error` values plus `func Code(err error) string`. `limits` exposes only constants, including `MaxStoredObjectBytes = 1900 * 1024 * 1024`.

**Rules.**
- If `Start` fails partway, already-started components are closed in reverse order. Each `Close` gets a bounded context.
- Every sentinel has exactly one stable code string, and `Code` unwraps with `errors.Is`. `internal/domain/...` imports the standard library only.
- Every limit used anywhere in the code lives in `limits`. No other package may redefine a threshold (claude.md).

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/services/file/limits.go` | 106 | PORT-PARTIAL | `internal/domain/limits` (L3) | Keep: `MaxPartBytes` value `1900 * 1024 * 1024` under the name in 5.5, the "empty files" and "too large" conditions as sentinels in `internal/domain/errs`. Drop: `MinResumableUploadBytes`, `LargeFileMaxBytes`, `MaxParts`, `planUpload`, `humanByteSize`. Also into `limits`: delete batch 100 (`backend/services/file/upload.go` `deleteMessagesChunked`) |
| `internal/app/operation.go` | 203 | PATTERN-ONLY | `internal/domain/errs` (L3) | Stable snake_case error codes carried on a typed error (`not_found`, `permission_denied`, `already_exists`, `canceled`, `deadline_exceeded`). butterDB defines its own code set |
| `backend/daemon/server.go` | 193 | PATTERN-ONLY | `internal/lifecycle` (L3), `internal/core` (integration) | `Run` acquires the process lock, starts logging, builds the engine, opens the listener, and releases in reverse through `defer`. butterDB makes this explicit with `lifecycle.Group` and the close order in `docs/claude.md` §6.5 |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/services/file/upload.go`: `func (s *Service) Upload(ctx context.Context, channelID int64, filePaths []string, parentIDs []string, encrypt bool) ([]Metadata, error) {`; `func (s *Service) CancelUpload(uploadID int) bool {`
- `internal/app/operation.go`: `type OperationError struct {`; `func (e *OperationError) Error() string {`; `func (e *OperationError) Unwrap() error {`; `type OperationResult struct {`; `func (r OperationResult) Err() error {`; `type UploadResult struct {`; `type DownloadResult struct {`; `type ResumableDownloadsResult struct {`
- `backend/daemon/server.go`: `type ServerConfig struct {`; `type Server struct {`; `func Run(ctx context.Context, cfg ServerConfig) error {`

**Tests (binding names).** `TestGroupStartsInOrder`, `TestGroupClosesInReverse`, `TestStartFailureClosesStarted`, `TestCloseBoundedByContext`, `TestEverySentinelHasCode`, `TestCodesUnique`, `TestCodeUnwraps`, `TestLimitsMatchSpec` (1900 MiB, 1024-byte keys, 2048-byte user metadata, 10000 parts, 5 MiB minimum part).

**Verify.**

```bash
go vet -mod=readonly ./internal/lifecycle/... ./internal/domain/errs/... ./internal/domain/limits/...
go test -mod=readonly -race ./internal/lifecycle/... ./internal/domain/errs/... ./internal/domain/limits/...
gofmt -l internal/lifecycle internal/domain/errs internal/domain/limits
```

**Commits.** `add lifecycle group with reverse close`, `add domain error sentinels and limits`.

### 5.4 Lane L4 — tooling

**Objective.** Write the repository guardrail scripts every later phase runs: the layering check, the comment-policy check, the one-command verifier, worktree helpers for the execution protocol, and the task runner file.

**Owned paths.** `scripts/layering.sh`, `scripts/nocomments.sh`, `scripts/verify.sh`, `scripts/worktree-create.sh`, `scripts/worktree-remove.sh`, `scripts/test/` (script tests and fixtures), `Taskfile.yml`. `scripts/hooks/` and `scripts/install-hooks.sh` belong to the prelude.

**Read.** `docs/claude.md` (layering rules and comment policy); `docs/phases/00-execution-protocol.md` (worktree paths, branch names, verification list); `docs/plan.md` (build commands).

**Rules.**
- `layering.sh` uses `go list -deps -json` and fails when: `internal/domain`, `internal/wire` or `internal/crypto/format` import outside the standard library; anything other than `internal/tgclient/...` imports `github.com/gotd/td`; anything other than `internal/storage/...` imports `modernc.org/sqlite`; `api/...` imports `internal/...`; or the root module imports `apps/...`. It prints every violation and exits 1.
- `nocomments.sh` scans tracked `*.go` files and fails on two consecutive comment lines, any `/* */` block, `TODO` or `FIXME`, or commented-out code heuristics. It allows `//go:` directives, build tags and a single standalone line. It runs per module.
- `verify.sh` runs `gofmt -l`, `go vet`, `go test -race` on the root module (with `GOWORK=off CGO_ENABLED=0`), then `layering.sh` and `nocomments.sh`, and exits non-zero on the first failure with a clear message.
- Worktree scripts create and remove `../butterdb-wt/NN-<lane>` on `phase/NN-<slug>-<lane>` exactly as the protocol specifies. They never delete a branch that has unmerged commits.
- Scripts are POSIX-friendly bash with `set -euo pipefail`, run on Linux and macOS, and need no network.

**Tests (binding names).** `scripts/test/run.sh` must exercise fixture modules under `scripts/test/fixtures/`: a forbidden gotd import, a forbidden sqlite import, a domain package importing a third party, two-line and block comments, and an allowed `//go:build` line. Each fixture must produce the expected exit code and message. Cases: `layering_rejects_gotd_outside_tgclient`, `layering_rejects_sqlite_outside_storage`, `layering_rejects_domain_thirdparty`, `nocomments_rejects_two_lines`, `nocomments_rejects_block`, `nocomments_allows_directives`, `worktree_create_remove_roundtrip`.

**Verify.**

```bash
bash -n scripts/*.sh scripts/test/*.sh
bash scripts/test/run.sh
shellcheck scripts/*.sh scripts/test/*.sh || echo 'shellcheck not installed'
```

**Commits.** `add layering and comment policy checks`, `add verify script and worktree helpers`, `add task runner file`.

## 6. Integration lane

cmd/butterdbd (version, check-config), internal/core skeleton, test/acceptance/phase01
Own `internal/core`, `cmd/`, and `test/acceptance/phase01` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase01/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestConfigurationRejectsUnknownAndUnsafeSecretValues`: configuration rejects unknown and unsafe secret values.
  - `TestProcessLockPreventsASecondServer`: process lock prevents a second server.
  - `TestLayeringAndNoCommentChecksEnforceRepositoryRules`: layering and no-comment checks enforce repository rules.
### TDrive regression tests to adapt

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/datadir/datadir_test.go` | 207 | Partial | `TestCreateCacheTempUsesPrivateCacheOverride`, `TestCleanupCacheTempsRemovesOnlyOwnedScratchFiles`, `TestCleanupCacheTempsKeepsGoingWhenOneCannotBeRemoved`. Drop the four default and override tests |
| `backend/processlock/lock_test.go` | 286 | Partial | All except `TestAcquireReclaimsLegacyRecordNamingOwnPID` |
| `backend/processlock/owner_process_test.go` | 155 | Full | Helper-process ownership tests |
| `backend/auth/private_file_test.go` | 97 | Full | `TestWritePrivateFileReplacesContents`, `TestWritePrivateFileUsesRestrictiveModes`, `TestWritePrivateFileRenameFailurePreservesOldFileAndCleansTemp` |

## 8. Owner gates

- **Project license (A10).** The owner chooses butterDB's license before the prelude commits the root `LICENSE`. A draft MIT `LICENSE` exists in the working tree but is not committed until the owner confirms it. TDrive's MIT attribution in `docs/legal/THIRD_PARTY_NOTICES.md` is carried whatever the choice.

## 9. Exit criteria and handoff

All L1–L4 commits have passed monitoring and review; the integration build and phase 01 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 02.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding behavior for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/operations.md`

## 2. Configuration

### 2.1 Sources and precedence

| Rank (later wins) | Source | Notes |
| --- | --- | --- |
| 1 | Built-in defaults (this section) | |
| 2 | TOML file | Path from `--config`, else `BUTTERDB_CONFIG`, else `/etc/butterdb/butterdb.toml` |
| 3 | `BUTTERDB_*` environment variables | One per key, naming rule §2.3 |

- A path given with `--config` or `BUTTERDB_CONFIG` must exist. The default path
  may be absent; defaults plus environment then apply (container use).
- Decoding is strict. An unknown TOML key, an unknown `BUTTERDB_*` variable, a
  wrong type or an out-of-range value is a configuration error (exit code 2).
  Reserved variables that are not keys: `BUTTERDB_CONFIG`, and every variable
  whose name starts with `BUTTERDB_TGTEST` or `BUTTERDB_S3COMPAT_` (test
  harnesses only, for example `BUTTERDB_TGTEST_API_ID`,
  `BUTTERDB_S3COMPAT_ENDPOINT`; `serve` ignores them).
- The `[admin]`, `[s3]` and `[media]` tables are owned by
  [admin-api.md](../spec/admin-api.md) §2.1, [s3-api.md](../spec/s3-api.md) §2.1 and
  [media-api.md](../spec/media-api.md) §2.1 and are copied below unchanged. When a copy
  here and its owner differ, the owner wins and this file is corrected.
- Configuration is read once at start. Changes need a restart, except the admin
  certificate files in `files` mode, which `SIGHUP` reloads ([admin-api.md](../spec/admin-api.md) §2.2).
- TOML parser: `github.com/BurntSushi/toml`. Default (owner may change).

### 2.2 Value types

| Type | TOML form | Environment form | Rule |
| --- | --- | --- | --- |
| string | `"text"` | `text` | |
| path | `"/abs/path"` | `/abs/path` | Must be absolute after expansion. The only expansion is `${CREDENTIALS_DIRECTORY}` (systemd credentials, §11.1; unset while referenced is an error) and the token `<data_dir>` at the start of a default |
| bool | `true` | `true`, `false` | |
| int | `20` | `20` | Range per key |
| duration | `"30s"` | `30s` | Go `time.ParseDuration` syntax; `"0s"` only where the key says it disables |
| size | `"20GiB"` or `21474836480` | `20GiB` | Integer bytes, or an integer with suffix `KiB`, `MiB`, `GiB`, `TiB` (powers of 1024). Decimal suffixes (`GB`) are rejected so nobody guesses |
| address | `"0.0.0.0:9000"` | `0.0.0.0:9000` | `host:port`; IPv6 as `[::]:9000` |
| list | `["a", "b"]` | `a,b` | Comma-separated in the environment; empty string means empty list |

### 2.3 Environment naming

`BUTTERDB_` + the key path in upper case with `.` replaced by `_`.
`telegram.send_rate.control_per_minute` becomes
`BUTTERDB_TELEGRAM_SEND_RATE_CONTROL_PER_MINUTE`. Top-level keys have no section:
`data_dir` becomes `BUTTERDB_DATA_DIR`.

### 2.4 Every key

#### Top level

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `data_dir` | `BUTTERDB_DATA_DIR` | path | `/var/lib/butterdb` | Data directory (§3) |
| `server_key_file` | `BUTTERDB_SERVER_KEY_FILE` | path | `/etc/butterdb/server.key` | Server key file (A6; [security.md](../spec/security.md) §5) |

#### `[log]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `log.level` | `BUTTERDB_LOG_LEVEL` | string | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | `BUTTERDB_LOG_FORMAT` | string | `json` | `json` (production) or `text` (development) |
| `log.access_log` | `BUTTERDB_LOG_ACCESS_LOG` | bool | `true` | One `INFO` line per HTTP request on the S3, admin and media listeners |
| `log.object_keys` | `BUTTERDB_LOG_OBJECT_KEYS` | string | `full` | How object keys appear in logs: `full`, `hash` (first 16 hex characters of SHA-256), `none`. Default (owner may change) |

#### `[s3]` (owned by [s3-api.md](../spec/s3-api.md) §2.1)

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `s3.listen` | `BUTTERDB_S3_LISTEN` | address | `0.0.0.0:9000` | S3 listener |
| `s3.public_url` | `BUTTERDB_S3_PUBLIC_URL` | string | empty | External base URL, e.g. `https://s3.example.com`. Used for presigned URLs and reported as `endpoints.s3_public_url` |
| `s3.region` | `BUTTERDB_S3_REGION` | string | `us-east-1` | Region clients sign with |
| `s3.base_domains` | `BUTTERDB_S3_BASE_DOMAINS` | list | `[]` | Domains under which `<bucket>.<domain>` selects virtual-hosted-style. Empty means path-style only |
| `s3.tls_mode` | `BUTTERDB_S3_TLS_MODE` | string | `none` | `none` (plain HTTP, normally behind a TLS proxy) or `files` |
| `s3.tls_cert_file` | `BUTTERDB_S3_TLS_CERT_FILE` | path | empty | PEM chain, `files` mode |
| `s3.tls_key_file` | `BUTTERDB_S3_TLS_KEY_FILE` | path | empty | PEM key, `files` mode; a secret file (§2.5) |
| `s3.trusted_proxies` | `BUTTERDB_S3_TRUSTED_PROXIES` | list | `["127.0.0.1/32", "::1/128"]` | CIDRs whose `X-Forwarded-For` is honoured |
| `s3.request_budget` | `BUTTERDB_S3_REQUEST_BUDGET` | duration | `30s` | Total time one S3 request may spend waiting on admission, the E10 send-rate limiter, Telegram flood waits and `getFile` slots before `503 SlowDown`; transfer time is not counted ([s3-api.md](s3-api.md) §12.1, A7) |
| `s3.body_idle_timeout` | `BUTTERDB_S3_BODY_IDLE_TIMEOUT` | duration | `60s` | Longest gap between request body reads |
| `s3.write_idle_timeout` | `BUTTERDB_S3_WRITE_IDLE_TIMEOUT` | duration | `60s` | Longest blocked response write |
| `s3.keepalive_interval` | `BUTTERDB_S3_KEEPALIVE_INTERVAL` | duration | `15s` | Whitespace interval for long responses |
| `s3.completed_upload_retention` | `BUTTERDB_S3_COMPLETED_UPLOAD_RETENTION` | duration | `24h` | How long a completed multipart upload answers a repeated Complete |
| `s3.multipart_idle_expiry` | `BUTTERDB_S3_MULTIPART_IDLE_EXPIRY` | duration | `168h` | The sweeper aborts multipart uploads idle this long |
| `s3.auth_failure_limit` | `BUTTERDB_S3_AUTH_FAILURE_LIMIT` | int | `100` | Failed authentications per client address per 10 minutes before blocking |
| `s3.cors_allowed_origins` | `BUTTERDB_S3_CORS_ALLOWED_ORIGINS` | list | `[]` | CORS origins; empty disables CORS |
| `s3.cors_allowed_methods` | `BUTTERDB_S3_CORS_ALLOWED_METHODS` | list | `["GET", "HEAD", "PUT", "POST", "DELETE"]` | CORS methods |
| `s3.cors_allowed_headers` | `BUTTERDB_S3_CORS_ALLOWED_HEADERS` | list | `["*"]` | CORS request headers |
| `s3.cors_expose_headers` | `BUTTERDB_S3_CORS_EXPOSE_HEADERS` | list | [s3-api.md](../spec/s3-api.md) §13.3 list | CORS exposed response headers |
| `s3.cors_max_age` | `BUTTERDB_S3_CORS_MAX_AGE` | int | `3600` | Preflight cache seconds |
| `s3.anonymous_rate` | `BUTTERDB_S3_ANONYMOUS_RATE` | int 1–10000 | `50` | Anonymous (public-read) requests per second per client address, enforced in `internal/access/policy` before any Telegram read ([security.md](../spec/security.md) §9). Added by this document; Default (owner may change) |
| `s3.anonymous_burst` | `BUTTERDB_S3_ANONYMOUS_BURST` | int 1–10000 | `100` | Burst for the limiter above. Added by this document; Default (owner may change) |

The SigV4 clock skew (15 minutes) and presign maximum (7 days) are constants of
`internal/access/sigv4`, not keys ([s3-api.md](../spec/s3-api.md) §15).

#### `[ingest]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `ingest.max_concurrent_puts` | `BUTTERDB_INGEST_MAX_CONCURRENT_PUTS` | int 1–64 | `4` | Process-wide put slots in `ingest/admission`, shared by S3 and admin uploads. Default (owner may change) |
| `ingest.max_concurrent_puts_per_key` | `BUTTERDB_INGEST_MAX_CONCURRENT_PUTS_PER_KEY` | int 1–64 | `2` | Put slots one access key (or one admin device) may hold at once. Default (owner may change) |

#### `[admin]` (owned by [admin-api.md](../spec/admin-api.md) §2.1)

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `admin.listen` | `BUTTERDB_ADMIN_LISTEN` | address | `0.0.0.0:9443` | Admin listener |
| `admin.tls_mode` | `BUTTERDB_ADMIN_TLS_MODE` | string | `self-signed` | `self-signed`, `files`, `proxy` |
| `admin.tls_cert_file` | `BUTTERDB_ADMIN_TLS_CERT_FILE` | path | empty | PEM chain, `files` mode only |
| `admin.tls_key_file` | `BUTTERDB_ADMIN_TLS_KEY_FILE` | path | empty | PEM key, `files` mode only; a secret file (§2.5) |
| `admin.hostnames` | `BUTTERDB_ADMIN_HOSTNAMES` | list | `[]` | Extra SANs for the self-signed certificate |
| `admin.trusted_proxies` | `BUTTERDB_ADMIN_TRUSTED_PROXIES` | list | `["127.0.0.1/32", "::1/128"]` | CIDRs whose `X-Forwarded-For` is honoured |
| `admin.control_socket` | `BUTTERDB_ADMIN_CONTROL_SOCKET` | path | `<data_dir>/admin.sock` | Local control socket ([admin-api.md](../spec/admin-api.md) §5.5) |
| `admin.setup_token_ttl` | `BUTTERDB_ADMIN_SETUP_TOKEN_TTL` | duration | `24h` | Setup token lifetime |
| `admin.pairing_code_ttl` | `BUTTERDB_ADMIN_PAIRING_CODE_TTL` | duration | `10m` | Pairing code lifetime |
| `admin.device_idle_revoke` | `BUTTERDB_ADMIN_DEVICE_IDLE_REVOKE` | duration | `4320h` | Idle device revocation |
| `admin.upload_commit_budget` | `BUTTERDB_ADMIN_UPLOAD_COMMIT_BUDGET` | duration | `10m` | Longest an admin upload waits on Telegram |

#### `[media]` (phase 13; owned by [media-api.md](../spec/media-api.md) §2.1)

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `media.enabled` | `BUTTERDB_MEDIA_ENABLED` | bool | `false` | Bind the media listener and supervise the worker |
| `media.listen` | `BUTTERDB_MEDIA_LISTEN` | address | `0.0.0.0:9080` | Media listener |
| `media.path_prefix` | `BUTTERDB_MEDIA_PATH_PREFIX` | string | `""` | Optional prefix such as `/media` |
| `media.public_url` | `BUTTERDB_MEDIA_PUBLIC_URL` | string | `""` | Reported as `endpoints.media_public_url` |
| `media.hostnames` | `BUTTERDB_MEDIA_HOSTNAMES` | list | `[]` | Allowed `Host` values when the listener is bound to loopback |
| `media.trusted_proxies` | `BUTTERDB_MEDIA_TRUSTED_PROXIES` | list | `["127.0.0.1/32", "::1/128"]` | As `s3.trusted_proxies` |
| `media.signing_key_file` | `BUTTERDB_MEDIA_SIGNING_KEY_FILE` | path | `<data_dir>/secrets/media_signing.key` | Current media signing key; a secret file (§2.5) |
| `media.signing_key_previous_file` | `BUTTERDB_MEDIA_SIGNING_KEY_PREVIOUS_FILE` | path | `""` | Previous signing key, verification only |
| `media.private_max_expiry` | `BUTTERDB_MEDIA_PRIVATE_MAX_EXPIRY` | duration | `168h` | Furthest `exp_` a private-bucket URL may carry |
| `media.width_ladder` | `BUTTERDB_MEDIA_WIDTH_LADDER` | list | media-api §8 ladder | Allowed output widths |
| `media.worker_mode` | `BUTTERDB_MEDIA_WORKER_MODE` | string | `spawn` | `spawn`: `butterdbd` runs the worker as a child. `external`: systemd or a container runs it as its own user ([security.md](../spec/security.md) §12.4) |
| `media.worker_path` | `BUTTERDB_MEDIA_WORKER_PATH` | string | `butterdb-transformd` | Worker binary, resolved with `exec.LookPath` at start; spawn mode only |
| `media.worker_socket` | `BUTTERDB_MEDIA_WORKER_SOCKET` | path | `<data_dir>/run/transformd.sock` | Worker Unix socket. External mode: `/run/butterdb-transformd/transformd.sock` |
| `media.worker_tmp_dir` | `BUTTERDB_MEDIA_WORKER_TMP_DIR` | path | `<data_dir>/run/transformd-tmp` | Worker scratch; spawn mode only |
| `media.ffmpeg_path` | `BUTTERDB_MEDIA_FFMPEG_PATH` | path | `/usr/bin/ffmpeg` | Spawn mode only |
| `media.worker_concurrency` | `BUTTERDB_MEDIA_WORKER_CONCURRENCY` | int | `min(NumCPU, 4)` | Concurrent worker jobs |
| `media.worker_memory_limit` | `BUTTERDB_MEDIA_WORKER_MEMORY_LIMIT` | size | `1GiB` | Worker `RLIMIT_AS`; spawn mode only |
| `media.worker_sandbox` | `BUTTERDB_MEDIA_WORKER_SANDBOX` | string | `auto` | `auto`, `require`, `off`; spawn mode only |
| `media.queue_depth` | `BUTTERDB_MEDIA_QUEUE_DEPTH` | int | `64` | Waiters for a worker slot before `503` |
| `media.queue_timeout` | `BUTTERDB_MEDIA_QUEUE_TIMEOUT` | duration | `10s` | Longest wait for a worker slot |
| `media.source_timeout` | `BUTTERDB_MEDIA_SOURCE_TIMEOUT` | duration | `120s` | Longest time to read the original |
| `media.image_timeout` | `BUTTERDB_MEDIA_IMAGE_TIMEOUT` | duration | `20s` | Image job budget |
| `media.poster_timeout` | `BUTTERDB_MEDIA_POSTER_TIMEOUT` | duration | `30s` | Poster job budget, ffmpeg included |
| `media.poison_ttl` | `BUTTERDB_MEDIA_POISON_TTL` | duration | `1h` | Quarantine of a source that crashed the worker twice |
| `media.negative_ttl` | `BUTTERDB_MEDIA_NEGATIVE_TTL` | duration | `10m` | Memory of a deterministic transform failure |
| `media.cache_max_bytes` | `BUTTERDB_MEDIA_CACHE_MAX_BYTES` | size | `10GiB` | Derivative disk tier (`cache/derived/`) ceiling |
| `media.cache_memory_max_bytes` | `BUTTERDB_MEDIA_CACHE_MEMORY_MAX_BYTES` | size | `128MiB` | Memory tier ceiling (derivatives of encrypted objects) |
| `media.cache_sweep_interval` | `BUTTERDB_MEDIA_CACHE_SWEEP_INTERVAL` | duration | `1h` | Stale-version sweep period |
| `media.max_image_source_bytes` | `BUTTERDB_MEDIA_MAX_IMAGE_SOURCE_BYTES` | size | `64MiB` | Largest plaintext image source |
| `media.max_video_source_bytes` | `BUTTERDB_MEDIA_MAX_VIDEO_SOURCE_BYTES` | size | `1GiB` | Largest plaintext video source for a poster |
| `media.max_source_pixels` | `BUTTERDB_MEDIA_MAX_SOURCE_PIXELS` | int | `50000000` | Largest width × height of one source frame |
| `media.max_source_side` | `BUTTERDB_MEDIA_MAX_SOURCE_SIDE` | int | `32768` | Largest source width or height |
| `media.max_output_side` | `BUTTERDB_MEDIA_MAX_OUTPUT_SIDE` | int | `4096` | Largest output width or height |
| `media.max_output_pixels` | `BUTTERDB_MEDIA_MAX_OUTPUT_PIXELS` | int | `16777216` | Largest output width × height |
| `media.max_output_bytes` | `BUTTERDB_MEDIA_MAX_OUTPUT_BYTES` | size | `32MiB` | Largest encoded derivative |
| `media.max_frames` | `BUTTERDB_MEDIA_MAX_FRAMES` | int | `200` | Largest frame count kept animated |
| `media.max_animated_pixels` | `BUTTERDB_MEDIA_MAX_ANIMATED_PIXELS` | int | `200000000` | Largest width × height × frames kept animated |
| `media.avif_auto_max_pixels` | `BUTTERDB_MEDIA_AVIF_AUTO_MAX_PIXELS` | int | `4194304` | `f_auto` chooses AVIF only at or below this output size |

Presets and per-bucket media settings (`media.presets.<name>`,
`media.buckets.<bucket>.*`) and the hard ceilings in `internal/domain/limits`
are specified in [media-api.md](../spec/media-api.md) §2.1 and §2.2; `SIGHUP` reloads
them. Every `[media]` default is Default (owner may change).

#### `[ops]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `ops.listen` | `BUTTERDB_OPS_LISTEN` | address | `127.0.0.1:9090` | Ops listener (`/healthz`, `/readyz`, `/metrics`) |
| `ops.hostnames` | `BUTTERDB_OPS_HOSTNAMES` | list | `[]` | Extra `Host` values accepted on a loopback ops listener (DNS-rebinding guard) |
| `ops.metrics_token_file` | `BUTTERDB_OPS_METRICS_TOKEN_FILE` | path | empty | Secret file holding a bearer token required on `/metrics` |
| `ops.allow_unauthenticated_metrics` | `BUTTERDB_OPS_ALLOW_UNAUTHENTICATED_METRICS` | bool | `false` | Permits a non-loopback ops listener without a token |
| `ops.ready_when_degraded` | `BUTTERDB_OPS_READY_WHEN_DEGRADED` | bool | `true` | `/readyz` answers `200` in `degraded` (§7.3) |

#### `[telegram]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `telegram.app_file` | `BUTTERDB_TELEGRAM_APP_FILE` | path | `<data_dir>/secrets/telegram_app.json` | `api_id` and `api_hash`, written by the server |
| `telegram.session_file` | `BUTTERDB_TELEGRAM_SESSION_FILE` | path | `<data_dir>/secrets/session_primary.json` | Primary gotd session, sealed ([security.md](../spec/security.md) §5.4) |
| `telegram.replica_session_file` | `BUTTERDB_TELEGRAM_REPLICA_SESSION_FILE` | path | `<data_dir>/secrets/session_replica.json` | Replica session (phase 12) |
| `telegram.connect_timeout` | `BUTTERDB_TELEGRAM_CONNECT_TIMEOUT` | duration | `30s` | First connection attempt at start before the server proceeds degraded |
| `telegram.device_model` | `BUTTERDB_TELEGRAM_DEVICE_MODEL` | string | `butterDB server` | Shown in Telegram's active sessions list |
| `telegram.test_dc` | `BUTTERDB_TELEGRAM_TEST_DC` | bool | `false` | Use Telegram test data centres. Development only; `check-config` warns when true |
| `telegram.send_rate.control_per_minute` | `BUTTERDB_TELEGRAM_SEND_RATE_CONTROL_PER_MINUTE` | int 1–600 | `20` | E10 limiter, `message` class (§13.3) |
| `telegram.send_rate.control_burst` | `BUTTERDB_TELEGRAM_SEND_RATE_CONTROL_BURST` | int 1–100 | `5` | Burst of the class above |
| `telegram.send_rate.documents_per_minute` | `BUTTERDB_TELEGRAM_SEND_RATE_DOCUMENTS_PER_MINUTE` | int 1–120 | `6` | E10 limiter, `document` class |
| `telegram.send_rate.documents_burst` | `BUTTERDB_TELEGRAM_SEND_RATE_DOCUMENTS_BURST` | int 1–20 | `1` | Burst of the class above. Default (owner may change) |

The send-rate defaults are the plan's values until the phase 11 owner gate
replaces them with `tgprobe` measurements (§13.5). The limiter cannot be
disabled; there is no `0` value.

#### `[cache]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `cache.dir` | `BUTTERDB_CACHE_DIR` | path | `<data_dir>/cache` | Holds `blocks/` and `derived/`; may live on another local filesystem |
| `cache.blocks_max_bytes` | `BUTTERDB_CACHE_BLOCKS_MAX_BYTES` | size | `20GiB` | Origin block cache ceiling. The derivative cache ceiling is `media.cache_max_bytes` |
| `cache.warm_on_put_max_bytes` | `BUTTERDB_CACHE_WARM_ON_PUT_MAX_BYTES` | size | `64MiB` | Objects up to this stored size are copied from the spool into the block cache after commit; `0` disables |
| `cache.min_free_bytes` | `BUTTERDB_CACHE_MIN_FREE_BYTES` | size | `2GiB` | Eviction also runs when the cache filesystem has less free space than this |

#### `[spool]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `spool.max_bytes` | `BUTTERDB_SPOOL_MAX_BYTES` | size | `8GiB` | Reservation ceiling for `spool/`. Two concurrent maximum-size encrypted puts need about 7.5 GiB (plain plus sealed copy). Default (owner may change) |
| `spool.multipart_max_bytes` | `BUTTERDB_SPOOL_MULTIPART_MAX_BYTES` | size | `16GiB` | Ceiling for staged S3 multipart parts in `multipart/` |
| `spool.min_free_bytes` | `BUTTERDB_SPOOL_MIN_FREE_BYTES` | size | `1GiB` | A reservation fails when it would leave less free space than this on the data filesystem |

#### `[snapshot]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `snapshot.dir` | `BUTTERDB_SNAPSHOT_DIR` | path | `<data_dir>/snapshots` | Local snapshot directory |
| `snapshot.interval` | `BUTTERDB_SNAPSHOT_INTERVAL` | duration | `24h` | Scheduled snapshot period inside `serve`; `0s` disables |
| `snapshot.retain` | `BUTTERDB_SNAPSHOT_RETAIN` | int 1–365 | `7` | Local snapshots kept, newest first |
| `snapshot.upload_to_telegram` | `BUTTERDB_SNAPSHOT_UPLOAD_TO_TELEGRAM` | bool | `false` | Also send each snapshot to the ops channel (§9.4) |
| `snapshot.telegram_retain` | `BUTTERDB_SNAPSHOT_TELEGRAM_RETAIN` | int 1–365 | `7` | Snapshots kept in the ops channel |
| `snapshot.before_migrate` | `BUTTERDB_SNAPSHOT_BEFORE_MIGRATE` | bool | `true` | Take a local snapshot before applying a schema migration (§12) |

#### `[shutdown]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `shutdown.drain_timeout` | `BUTTERDB_SHUTDOWN_DRAIN_TIMEOUT` | duration | `10s` | Listener drain budget |
| `shutdown.component_timeout` | `BUTTERDB_SHUTDOWN_COMPONENT_TIMEOUT` | duration | `5s` | Close budget per lifecycle component |
| `shutdown.total_timeout` | `BUTTERDB_SHUTDOWN_TOTAL_TIMEOUT` | duration | `60s` | Hard ceiling; past it the process exits with code 1 |

### 2.5 Secret files

Configuration names secret files; it never holds secret values.

| File | Key | Written by | Required mode |
| --- | --- | --- | --- |
| Server key | `server_key_file` | Operator, `butterdbd setup --key-only`, or first `serve` (§5.3) | See rules below |
| Telegram app credentials | `telegram.app_file` | Server (`PUT /admin/v1/telegram/credentials`, `setup`) | `0600` |
| Primary session | `telegram.session_file` | Server | `0600` |
| Replica session | `telegram.replica_session_file` | Server (phase 12) | `0600` |
| Admin TLS key, `files` mode | `admin.tls_key_file` | Operator | See rules below |
| Admin TLS key, `self-signed` mode | `<data_dir>/tls/admin.key` (fixed, [admin-api.md](../spec/admin-api.md) §2.3) | Server | `0600` |
| S3 TLS key, `files` mode | `s3.tls_key_file` | Operator | See rules below |
| Media signing key (phase 13) | `media.signing_key_file`, `media.signing_key_previous_file` | Server creates the current file when absent ([media-api.md](../spec/media-api.md) §7.1) | `0600` |
| Metrics token | `ops.metrics_token_file` | Operator | See rules below |

Rules for files the server writes: `internal/platform/secretfile` (temp file in the
same directory, `chmod 0600` before the first secret byte, write, fsync, close,
rename, fsync the directory; ported from TDrive `backend/auth/private_file.go`).

Rules for operator-supplied secret files, checked by `check-config` and at every
start:

| Condition | Result |
| --- | --- |
| Any permission bit for others | Error |
| Group write or execute | Error |
| Group read, file group equals the process's effective group, file owned by root | Warning. Kubernetes secret volumes with `fsGroup` cannot be `0600` for a non-root process, so this is the one exception to the `0600` rule of [storage-model.md](../spec/storage-model.md) §8.1 |
| Group read, any other group | Error |
| Owner is neither the process user nor root | Error |
| Symlink | Followed once; the target is checked |

### 2.6 Example file

```toml
data_dir = "/var/lib/butterdb"
server_key_file = "${CREDENTIALS_DIRECTORY}/server.key"

[log]
level = "info"

[s3]
listen = "127.0.0.1:9000"
public_url = "https://s3.example.com"
region = "us-east-1"
base_domains = ["s3.example.com"]

[admin]
listen = "0.0.0.0:9443"
tls_mode = "self-signed"
hostnames = ["admin.example.com"]

[ops]
listen = "127.0.0.1:9090"

[telegram.send_rate]
control_per_minute = 20
documents_per_minute = 6

[cache]
blocks_max_bytes = "100GiB"

[snapshot]
interval = "24h"
upload_to_telegram = true
```

### 2.7 Go contract (`internal/platform/config`)

```go
package config

type Config struct {
	DataDir       string         `toml:"data_dir"`
	ServerKeyFile string         `toml:"server_key_file"`
	Log           LogConfig      `toml:"log"`
	S3            S3Config       `toml:"s3"`
	Ingest        IngestConfig   `toml:"ingest"`
	Admin         AdminConfig    `toml:"admin"`
	Media         MediaConfig    `toml:"media"`
	Ops           OpsConfig      `toml:"ops"`
	Telegram      TelegramConfig `toml:"telegram"`
	Cache         CacheConfig    `toml:"cache"`
	Spool         SpoolConfig    `toml:"spool"`
	Snapshot      SnapshotConfig `toml:"snapshot"`
	Shutdown      ShutdownConfig `toml:"shutdown"`
}

type Duration time.Duration

type ByteSize int64

type LoadOptions struct {
	Path         string
	PathExplicit bool
	Environ      []string
}

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Finding struct {
	Severity Severity
	Key      string
	Message  string
}

func Default() Config
func Load(opts LoadOptions) (Config, []Finding, error)
func (d *Duration) UnmarshalText(b []byte) error
func (s *ByteSize) UnmarshalText(b []byte) error
```

Every section struct has one field per key in §2.4, named in Go case
(`ControlPerMinute`), with the TOML tag equal to the key's last segment. `Load`
returns decoding and range findings; host checks (§2.8) live in
`cmd/butterdbd` and use `internal/platform/{datadir,secretfile,fsx}`.

### 2.8 `butterdbd check-config`

```
butterdbd check-config [--config <path>] [--json]
```

Reads configuration, inspects the host, prints findings and exits. It never
contacts Telegram, never takes the process lock and never writes a file.

| Check | Severity on failure |
| --- | --- |
| TOML parses; no unknown keys; no unknown `BUTTERDB_*` variable; types and ranges | error |
| The four listen addresses parse and no two share a port on overlapping addresses | error |
| `data_dir` exists with mode `0700` owned by the process user, or its parent is writable so it can be created | error |
| `data_dir` and `cache.dir` are local filesystems (Linux `statfs` magic is not NFS `0x6969`, SMB `0x517B`, CIFS `0xFF534D42` or FUSE `0x65735546`) | error |
| `spool/` and `multipart/` resolve to the same filesystem as `data_dir` | error |
| Free space on the data filesystem at least `spool.max_bytes + spool.min_free_bytes` | warning |
| Server key file exists, passes §2.5 and is exactly 32 bytes (§5.1) | error (absent is a warning when no `state.json` exists, because `serve` generates it) |
| When `state.json` exists, its `kek_fingerprint` equals the key file's fingerprint | error |
| Every configured secret file passes §2.5 | error |
| Admin rules of [admin-api.md](../spec/admin-api.md) §2.1 (cert/key pair in `files` mode, loopback in `proxy` mode, control socket path at most 103 bytes) | error |
| S3 rules of [s3-api.md](../spec/s3-api.md) §2.1 (`files` TLS pair, `public_url` form, base domains, region pattern, CORS origins) | error |
| Media rules of [media-api.md](../spec/media-api.md) §2.3, when `media.enabled` | error |
| `ops.listen` is not loopback and neither `ops.metrics_token_file` nor `ops.allow_unauthenticated_metrics` is set | error |
| `s3.public_url` is empty | warning (presigned URLs unavailable) |
| `media.enabled`, `media.worker_mode = "spawn"` and `media.worker_sandbox = "off"` | warning (no sandbox around the decoders; [security.md](../spec/security.md) §12.4) |
| `snapshot.upload_to_telegram = false` and `snapshot.dir` is on the same filesystem as `data_dir` | warning (no off-host copy of `control.db`) |
| `telegram.test_dc = true` | warning |

Output, one line per finding, then a summary line:

```
error   server_key_file  /etc/butterdb/server.key: mode 0644 allows group or other access
warning s3.public_url    empty; presigned URLs are unavailable
check-config: 1 error, 1 warning
```

With `--json`: `{"ok": false, "findings": [{"severity": "error", "key": "server_key_file", "message": "…"}], "kek_fingerprint": "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"}`.
The fingerprint is printed when the key file is readable, so an operator can
verify a backup copy against `state.json` (§9.6). Exit codes follow §4.2.

---

### Contract source: `docs/spec/operations.md`

## 3. Data directory

### 3.1 Layout

| Path under `data_dir` | Mode | Holds | Owner package |
| --- | --- | --- | --- |
| `.` | `0700` | | `internal/platform/datadir` |
| `butterdbd.lock` | `0600` | Owner metadata JSON of the process lock (§6) | `internal/platform/processlock` |
| `butterdbd.lock.guard` | `0600` | Guard file that carries the kernel lock; never unlinked (§6) | `internal/platform/processlock` |
| `state.json` | `0600` | Deployment anchor ([storage-model.md](../spec/storage-model.md) §4.3) | `internal/buckets` through `secretfile` |
| `projection.db`, `projection.db-wal`, `projection.db-shm` | `0600` | `replay_log`, rejects, tamper, derived tables (E7) | `internal/storage/sqlitedb` |
| `control.db`, `control.db-wal`, `control.db-shm` | `0600` | Access keys, admin devices, tokens, jobs, idempotency, multipart index, replica journal, audit, `ops_channels` (§3.3) (E7) | `internal/storage/sqlitedb` |
| `admin.sock` | `0600` | Control socket ([admin-api.md](../spec/admin-api.md) §5.5) | `internal/adminapi/session` |
| `tls/` | `0700` | | `internal/adminapi/tlscert` |
| `tls/admin.key` | `0600` | Self-signed admin key (A3, [admin-api.md](../spec/admin-api.md) §2.3) | `internal/adminapi/tlscert` |
| `tls/admin.crt` | `0644` | Self-signed admin certificate (public; still behind the `0700` directories) | `internal/adminapi/tlscert` |
| `secrets/` | `0700` | | `internal/identity` |
| `secrets/telegram_app.json` | `0600` | `api_id`, `api_hash` | `internal/identity` |
| `secrets/session_primary.json` | `0600` | Sealed gotd session ([security.md](../spec/security.md) §5.4) | `internal/identity` |
| `secrets/session_replica.json` | `0600` | Sealed replica session (phase 12) | `internal/replica/account` |
| `secrets/media_signing.key` | `0600` | Media signing key (phase 13, [media-api.md](../spec/media-api.md) §7.1) | `internal/mediaapi/signing` |
| `spool/` | `0700` | `<job id>.plain`, `<job id>.sealed` while a put job runs | `internal/ingest/spool` |
| `multipart/<upload id>/` | `0700` | `part-<NNNNN>` files (`0600`) of open S3 multipart uploads | `internal/ingest/multipartstage` |
| `cache/blocks/` | `0700` | Origin block cache, stored bytes (ciphertext for encrypted objects) | `internal/readpath/blockcache` |
| `cache/derived/` | `0700` | Derivatives of unencrypted objects only (phase 13, [media-api.md](../spec/media-api.md) §11) | `internal/mediaapi/derivcache` |
| `snapshots/` | `0700` | `*.bdsnap` files (§9) | `internal/ops/snapshot` |
| `tmp/` | `0700` | Scratch for snapshot assembly, restore and rebuild; emptied at start | `internal/platform/datadir` |
| `run/` | `0700` | Created by the media supervisor (phase 13) | `internal/mediaapi/workerclient` |
| `run/transformd.sock` | `0600` | Worker socket, `media.worker_mode = "spawn"` | `internal/mediaapi/workerclient` |
| `run/transformd-tmp/` | `0700` | Worker scratch, spawn mode; holds only unlinked files while a job runs | `apps/transformd` |
| `rekey.json` | `0600` | Progress of an interrupted server key rotation; `serve` refuses to start while it exists ([security.md](../spec/security.md) §5.6) | `cmd/butterdbd` |

Every file the server creates is `0600` and every directory `0700`, except
`tls/admin.crt`. The process runs with umask `0077`. On start `datadir`
refuses a data directory whose mode grants group or other access, or whose owner
is not the process user; it never silently changes modes. Paths below
`data_dir` are fixed; only `cache.dir`, `snapshot.dir`, the media worker keys and
the secret-file keys move things. [plan.md](../plan.md) §4 names
`admin/tls_key.pem` and `admin/tls_cert.pem`; [admin-api.md](../spec/admin-api.md) §2.3
owns these files and names them `tls/admin.key` and `tls/admin.crt`, which this
table follows.

All filesystem access below `data_dir` goes through an `os.Root` opened on the
directory (`internal/platform/fsx`), so no computed path can escape it
([security.md](../spec/security.md) §11).

### 3.2 Filesystem rules

| Group | Rule | Reason |
| --- | --- | --- |
| `data_dir` as a whole | One local filesystem with POSIX advisory locks and shared-memory `mmap`. Never NFS, SMB or a FUSE network mount | SQLite WAL needs a working `-shm` and locks; `flock` over NFS is unreliable |
| `projection.db` with its `-wal` and `-shm`; `control.db` with its `-wal` and `-shm` | Same directory | SQLite requirement |
| `spool/` and `multipart/` | Same filesystem as `data_dir` | Multipart assembly and spool hand-off use `rename` |
| `state.json`, every secret file | Temp file in the same directory, then `rename` | Atomic replace (`secretfile`) |
| `tmp/` | Same filesystem as `data_dir` | Restore and rebuild publish by `rename` into `data_dir` |
| `cache.dir` | Any local filesystem | Block files are written as temp then renamed inside `cache.dir` |
| `snapshot.dir` | Any local filesystem; best on a different disk | A snapshot is assembled in `snapshot.dir/.tmp-<id>/` and renamed |

### 3.3 `state.json` and `ops_channels`

`state.json` is specified by [storage-model.md](../spec/storage-model.md) §4.3: fields
`version`, `deployment_id`, `primary_user_id`, `registry.{channel_id,
access_hash, marker_msg_id}`, `kek_fingerprint` (base64url of the 16-byte key
fingerprint, §5.2), `format_version`, `created_at`. Unknown fields are rejected
on load, so operations state is not kept there.

The two channels this document adds, the ops channel (§9.4) and the probe
channel (§13.5), are recorded in `control.db`. Default (owner may change); the
phase 11 prelude adds the migration:

```sql
CREATE TABLE ops_channels (
  purpose     TEXT    PRIMARY KEY CHECK (purpose IN ('ops', 'probe')),
  account     TEXT    NOT NULL CHECK (account IN ('primary', 'replica')),
  channel_id  INTEGER NOT NULL,
  access_hash INTEGER NOT NULL,
  created_at  INTEGER NOT NULL
) STRICT;
```

Neither channel is a bucket or registry channel: the syncer never reads it and
the projection never sees it. When `control.db` is lost, both are found again
by title (§9.4).

---
