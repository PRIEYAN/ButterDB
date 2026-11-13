# butterDB — Working Instructions

Standing instructions for every human and every agent writing code in this
repository. Read this file completely before touching a file, then read the
documents your task names.

Claude Code auto-loads `CLAUDE.md` only from the repository root. Phase 01's
prelude therefore adds a root `CLAUDE.md` whose single line points here:

```
Read docs/claude.md before doing anything in this repository.
```

Nothing else goes into the root `CLAUDE.md`. This file is the instruction set.

---

## 1. The product

butterDB is self-hosted object storage that speaks the S3 protocol and offers
Cloudinary-style media transforms, with its bytes stored in the deploying
developer's own Telegram account. The full product and engineering record,
with the reason behind every choice, is [`docs/decisions.md`](decisions.md).
Refer to its entries by ID (D7, E5, A6) instead of restating them.

What that means for code:

- `butterdbd` is a headless Go server. It owns the Telegram session, the
  SQLite databases and the HTTP listeners (S3 API, admin API, media API) (D14).
- The admin GUI is a Fyne desktop app. It is a remote client of the admin API
  and never holds a Telegram session (D13, D14).
- The transform worker `butterdb-transformd` is a separate process that wraps
  libvips and ffmpeg. The server stays pure Go (D16, E14).
- Telegram is the storage backend. Each bucket is a private channel; a registry
  channel holds bucket configuration (E1). An object is a hidden body document
  plus a `t=put` control message, and the put message is the commit (E2).
- A successful PUT means Telegram accepted the object and the projection
  applied it (D8). Delete is immediate and hard (D10).

There is no hosted service, no shared credential and no Node, Svelte or Wails
anywhere in the tree.

---

## 2. Document authority

When two documents disagree, the one higher in this table wins until the lower
one is changed deliberately and the reason is recorded in `docs/decisions.md`.

| Rank | Document | Owns |
| --- | --- | --- |
| 1 | [`docs/decisions.md`](decisions.md) | Product decisions (D*), unconfirmed assumptions (A*), engineering decisions (E*), accepted risks |
| 2 | `docs/spec/*.md` (starting with `docs/spec/storage-model.md`) | Wire formats, op catalogue, DDL, algorithms, constants, API behaviour |
| 3 | [`docs/plan.md`](plan.md) | Layering, package map, phase order, cross-phase contracts |
| 4 | `docs/phases/NN-*.md` | One phase: prelude, lane briefs, exact signatures, tests, commit messages |
| 5 | [`docs/butterDBCore.md`](butterDBCore.md) | **Legacy.** TDrive's engine explainer with names swapped |

Documents outside the ranking, each authoritative only for its own subject:

| Document | Owns |
| --- | --- |
| `docs/claude.md` (this file) | How to write code, how to commit it, what "done" means |
| `docs/phases/00-execution-protocol.md` | How an orchestrator runs a phase: branches, worktrees, agents, review, merge |
| `docs/reference/tdrive-port-map.md` | Which TDrive file becomes which butterDB package |
| `docs/legal/THIRD_PARTY_NOTICES.md` | Attribution for every ported TDrive package and every third-party dependency |
| `docs/guide/` | Operator-facing guides written in phase 11 |

Rules for reading the legacy specification:

- `docs/butterDBCore.md` describes a desktop drive, not object storage. Use it
  only for formats, constants and algorithms that a higher-ranked document
  retains or does not mention.
- These parts of it do not apply and are never built: the folder namespace,
  shared drives, the OS mount and WebDAV, HLS and in-app media streaming, the
  passphrase vault, the TDX1 multipart `t=part` / `t=manifest` machinery, the
  resumable upload journal, mobile backup, and the Wails frontend.
- A retained constant keeps its value exactly. Never change a specified
  constant, format byte or size to make a test pass. If the spec looks wrong,
  stop and report it.

---

## 3. Hard rules

Work that breaks one of these is rejected without further review.

1. **Go only.** All application logic is Go. The admin GUI is Fyne, which is
   Go. No other language holds domain rules, formats, transport policy or
   security decisions. Shell is allowed only in `scripts/` and `deploy/`.
2. **No comment blocks.** No comment spans more than one line. No doc comments,
   no `TODO`, no `FIXME`, no commented-out code. See §8.1.
3. **Every document lives in `docs/`.** The only files outside `docs/` that
   carry prose are the root `CLAUDE.md` pointer line, the agent definitions in
   `.claude/agents/*.md`, and the project `LICENSE` once the owner chooses one
   (A10).
4. **Never push.** Nobody working from these instructions runs `git push`.
5. **Commit author is `prieyan <mnprieyan@gmail.com>`**, set with `git -c` flags
   on every commit. See §12.
6. **Commit messages are one lowercase line of simple English** with no body.
   See §12.
7. **Clean architecture, SOLID, constructor injection, consumer-declared ports
   and one composition root** are structural requirements. See §5 to §7.
8. **Lanes never touch `go.mod`, `go.sum` or `go.work`.** Only a phase prelude
   adds modules and only the integration lane tidies (E11, E12).
9. **The root module builds with `CGO_ENABLED=0`** (E14). A change that needs
   cgo in the server is rejected.
10. **Stay inside your owned paths.** A lane writes only the paths its brief
    assigns. Anything else is reported, not edited.

---

## 4. Modules and the cgo boundary

Three Go modules live in one repository and are joined for local development
by a root `go.work`.

| Artifact | Path | Module | cgo | Imports from the root module |
| --- | --- | --- | --- | --- |
| Server `butterdbd` | `cmd/butterdbd` | `github.com/PRIEYAN/ButterDB` | no, `CGO_ENABLED=0` | everything it needs |
| Admin GUI | `apps/admin` | `github.com/PRIEYAN/ButterDB/apps/admin` | yes (Fyne, OpenGL) | `api/adminv1` only |
| Transform worker `butterdb-transformd` | `apps/transformd` | `github.com/PRIEYAN/ButterDB/apps/transformd` | yes (libvips through govips; ffmpeg as a subprocess) | `api/transformv1` only |

Boundary rules:

- The root module never imports anything under `apps/`.
- `apps/` modules import only `github.com/PRIEYAN/ButterDB/api/...`. Go's
  `internal` visibility rule is path based and would let `apps/admin` import
  `github.com/PRIEYAN/ButterDB/internal/...`, so `scripts/layering.sh` checks
  this explicitly.
- `api/...` packages are public contracts: DTOs, the admin API Go client and
  the transform worker protocol. They import the standard library only and
  never import `internal/...`.
- The server talks to the transform worker over its protocol in
  `api/transformv1`, never by linking libvips. ffmpeg is always a subprocess
  of the worker, never of the server.
- Root-module commands run with `GOWORK=off`. In workspace mode `./...`
  reaches into the `apps/` modules, which need cgo and would break a
  `CGO_ENABLED=0` build.

Server subcommands, all in `cmd/butterdbd`: `serve`, `setup`, `admin-token`,
`rebuild`, `snapshot`, `restore`, `check-config`, `version`, `tgprobe`.

---

## 5. Package layout and layering

### 5.1 Root module layout

This layout is binding. Do not create a top-level package that is not listed
here without a recorded decision.

```
github.com/PRIEYAN/ButterDB
├── cmd/butterdbd/                     main and subcommands
├── api/
│   ├── adminv1/                       admin API DTOs and Go client
│   └── transformv1/                   transform worker protocol DTOs
├── internal/
│   ├── platform/
│   │   ├── datadir/  processlock/  secretfile/  clock/  fsx/
│   │   ├── config/                    TOML file + BUTTERDB_* env overrides; secrets only via file paths
│   │   └── logging/                   log/slog JSON
│   ├── lifecycle/                     Component{Start,Close}, Group with reverse-order close
│   ├── domain/                        stdlib only
│   │   ├── errs/                      sentinel errors + stable codes
│   │   ├── limits/                    every size and count ceiling
│   │   ├── object/                    Key, ObjectMeta, ETag, S3 key rules
│   │   ├── bucket/                    BucketName, Policy, EncryptionDefault
│   │   └── access/                    AccessKeyID, Scope, Action
│   ├── wire/                          TDX1 parse/format and op catalogue; stdlib only
│   ├── crypto/
│   │   ├── format/                    TDE1 header, nonce, size arithmetic; stdlib only
│   │   ├── stream/                    EncryptStream, DecryptStream, EncryptingReaderAt
│   │   ├── randomaccess/              authenticated random-access decryptor
│   │   └── keyring/                   server key file, per-bucket DEK wrap/unwrap, fingerprint
│   ├── storage/                       the only importer of modernc.org/sqlite
│   │   ├── sqlitedb/  schema/
│   │   └── objectrepo/  bucketrepo/  channelrepo/  keyrepo/  adminrepo/  jobrepo/  replicarepo/
│   ├── projection/
│   │   ├── log/                       ProjectFromOp / ProjectFromOpTx, tamper, rejects
│   │   ├── apply/                     one Applier per op type, in a Registry
│   │   └── rebuild/                   replay from replay_log, per-key repair
│   ├── tgclient/                      the only importer of github.com/gotd/td
│   │   ├── conn/  flow/  send/  rangeio/  rpc/  authrpc/  faketg/
│   ├── identity/                      api_id/api_hash, login state machine, session, account binding
│   ├── syncer/
│   │   ├── scan/  difference/  livesync/  reconcile/
│   ├── buckets/                       bucket service, channel per bucket, registry channel
│   ├── ingest/
│   │   ├── spool/  put/  remove/  admission/  multipartstage/  sweeper/
│   ├── readpath/
│   │   ├── rangereader/  blockcache/  objectreader/  httprange/  replicasource/
│   ├── access/
│   │   ├── keys/  sigv4/  policy/
│   ├── s3api/
│   │   ├── router/  xmlcodec/  s3err/  bucketops/  objectops/  listing/  multipart/
│   ├── adminapi/
│   │   ├── setup/  session/  tlscert/  handlers/
│   ├── replica/
│   │   ├── account/  worker/  backfill/  deletes/
│   ├── mediaapi/
│   │   ├── grammar/  presets/  signing/  derivcache/  workerclient/  handlers/
│   ├── ops/
│   │   ├── health/  metrics/  snapshot/
│   └── core/                          the only composition root
├── apps/admin/                        Fyne GUI module
├── apps/transformd/                   transform worker module
├── deploy/                            Dockerfiles, systemd units, compose and Caddy examples
├── scripts/                           layering, comment and verify checks, commit-msg hook, worktree helpers
├── test/
│   ├── acceptance/phaseNN/            one package per phase, owned by its integration lane
│   ├── s3compat/                      aws-sdk-go-v2 compatibility suite
│   └── load/                          load and soak harness
├── docs/                              every document
└── .claude/agents/                    worker, reviewer and git-monitor agent definitions
```

Tests live beside the code they test. Acceptance tests live in
`test/acceptance/phaseNN` and belong to that phase's integration lane.

### 5.2 Layers

Dependencies point inward. A package may import its own layer or a layer
further in, never a layer further out.

| Layer | Packages | May import |
| --- | --- | --- |
| Domain | `internal/domain/...`, `internal/wire`, `internal/crypto/format` | the standard library only |
| Shared libraries | `internal/crypto/{stream,randomaccess}`, `internal/lifecycle`, `internal/platform/clock` | domain, `golang.org/x/crypto` |
| Application | `internal/projection/...`, `internal/buckets`, `internal/syncer/...`, `internal/ingest/...`, `internal/readpath/...`, `internal/access/...`, `internal/identity`, `internal/replica/...`, `internal/mediaapi/{grammar,presets,signing,derivcache}`, `internal/ops/snapshot` | domain, shared libraries, shared value types of `internal/tgclient` (§5.4) |
| Infrastructure | `internal/storage/...`, `internal/tgclient/...`, `internal/crypto/keyring`, `internal/platform/...`, `internal/mediaapi/workerclient` | domain, shared libraries, `api/...`, their own third-party drivers |
| Interface | `internal/s3api/...`, `internal/adminapi/...`, `internal/mediaapi/handlers`, `internal/ops/{health,metrics}` | domain, shared libraries, application packages, `api/...` |
| Composition | `internal/core`, `cmd/butterdbd` | everything in the root module |

The standard library is importable from every layer.

### 5.3 Enforced import rules

`scripts/layering.sh` checks these with `go list` in CI and in
`scripts/verify.sh`. A violation fails the build.

| # | Rule |
| --- | --- |
| L1 | `internal/domain/...`, `internal/wire` and `internal/crypto/format` import the standard library only |
| L2 | Only `internal/tgclient/...` imports `github.com/gotd/td/...` |
| L3 | Only `internal/storage/...` imports `modernc.org/sqlite` |
| L4 | Only `internal/core` and `cmd/butterdbd` call a constructor of an adapter from another package |
| L5 | Only `cmd/butterdbd` imports `internal/core` |
| L6 | `api/...` imports the standard library only, and never `internal/...` |
| L7 | The root module never imports `apps/...` |
| L8 | `apps/admin` and `apps/transformd` import only `github.com/PRIEYAN/ButterDB/api/...` from the root module |
| L9 | Interface packages never import `internal/storage/...` or `internal/tgclient/...`; they reach data through application services. Default (owner may change) |
| L10 | No package imports a package of an outer layer (§5.2) |

### 5.4 Shared transport types

The phase 04 prelude defines a small set of value types in `internal/tgclient`
(`DocumentRef`, the `InputPeer` wrappers, `FloodWaitError`,
`ErrSendOutcomeUnknown`). Application packages may name these types in their
ports. They never import a `tgclient` subpackage that holds behaviour, and they
never import gotd.

---

## 6. Architecture rules

### 6.1 Interfaces belong to the consumer

Declare a port in the package that calls it. Name it for the role it plays,
not the implementation behind it: `ControlSender`, not `TgClientInterface`.
Keep it to the methods that caller uses. A port with more than about five
methods means two roles were merged; split it.

This is the shape. Each phase document owns the exact signatures.

```go
package put

type BodySender interface {
	SendFileIdempotent(ctx context.Context, req tgclient.SendFileRequest) (tgclient.SendFileResult, error)
}

type ControlSender interface {
	SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error)
}

type OpProjector interface {
	ProjectFromOp(ctx context.Context, in projlog.Incoming) (projlog.Outcome, error)
}
```

Adapters satisfy ports implicitly. An adapter package never imports a consumer
package to declare conformance, and never exports an interface "for callers to
use".

### 6.2 Constructor injection only

Every dependency arrives through the constructor and is validated there.

```go
type Dependencies struct {
	Bodies    BodySender
	Controls  ControlSender
	Projector OpProjector
	Jobs      JobStore
	Clock     clock.Clock
	Logger    *slog.Logger
}

func NewService(deps Dependencies) (*Service, error) {
	if deps.Bodies == nil {
		return nil, fmt.Errorf("put: %w: bodies", errs.ErrMissingDependency)
	}
	if deps.Controls == nil {
		return nil, fmt.Errorf("put: %w: controls", errs.ErrMissingDependency)
	}
	if deps.Projector == nil {
		return nil, fmt.Errorf("put: %w: projector", errs.ErrMissingDependency)
	}
	if deps.Jobs == nil {
		return nil, fmt.Errorf("put: %w: jobs", errs.ErrMissingDependency)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("put: %w: clock", errs.ErrMissingDependency)
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("put: %w: logger", errs.ErrMissingDependency)
	}
	return &Service{deps: deps}, nil
}
```

No service locator. No dependencies carried in a `context.Context`. No setter
injection. No package-level singletons. A constructed value is fully usable or
it does not exist. Constructors do not start goroutines or open connections;
`Start` does (§6.5).

### 6.3 One composition root

`internal/core` is the only package that constructs concrete adapters and wires
them together. `cmd/butterdbd` parses the subcommand and flags, loads
configuration through `internal/platform/config`, and hands control to `core`.
If any other package calls a constructor from an adapter package, that is a
layering defect (rule L4).

### 6.4 No ambient state

- No `init()` that does work. No package-level mutable variables. Package-level
  `const` values and immutable sentinel errors are fine.
- Time comes from the injected `clock.Clock` in `internal/platform/clock`.
  Nothing outside that package calls `time.Now`, `time.Since` or `time.After`
  for decisions; tests use a fake clock.
- Randomness comes from an injected `io.Reader`. `core` passes
  `crypto/rand.Reader`; tests pass a deterministic reader.
- The filesystem is reached through `internal/platform/fsx` and
  `internal/platform/datadir`. Secret files are written only through
  `internal/platform/secretfile` (§9.5).
- Configuration is read only by `internal/platform/config` and passed on as
  typed values. Nothing else calls `os.Getenv` or reads `BUTTERDB_*` variables.
- Logging uses an injected `*slog.Logger`. Never `slog.Default()`, never the
  `log` package.
- The projector is not given a clock. Domain state never comes from the local
  clock; timestamps the projection stores come from the op or the Telegram
  message date, so replay is deterministic.

### 6.5 Lifecycles are explicit

```go
type Component interface {
	Start(ctx context.Context) error
	Close(ctx context.Context) error
}
```

`lifecycle.Group` starts components in dependency order and closes them in
strict reverse, each close with a bounded context. Every goroutine has an owner
that can stop it and wait for it. A goroutine started without an owner is a
defect.

The `butterdbd serve` close order, outermost first:

1. HTTP listeners (S3, media, admin, ops) stop accepting and drain.
2. The transform worker supervisor stops `butterdb-transformd`.
3. The replica worker and backfill stop at a journal boundary.
4. The ingest sweeper and in-flight jobs stop at a job-row boundary.
5. The syncer stops.
6. The Telegram connection closes.
7. `projection.db` and `control.db` close.
8. Logging flushes.
9. The process lock releases last.

---

## 7. SOLID in practice

| Principle | What it means here |
| --- | --- |
| **Single responsibility** | One package, one reason to change. `internal/wire` changes when the TDX1 grammar changes and for no other reason. `internal/s3api/xmlcodec` encodes and decodes S3 XML and knows nothing about Telegram |
| **Open/closed** | A new op type registers an `Applier` in the `internal/projection/apply` Registry; no caller's switch grows. An op type this binary does not know is a skippable `unknown_op` reject with its raw header preserved, never a parse failure (E5). Unknown keys inside a known op are ignored |
| **Liskov substitution** | Every implementation honours the port's error contract. `faketg` must be able to return `ErrSendOutcomeUnknown` and `FloodWaitError` on any call, because a fake that cannot hides the failure the write path exists to survive. Read-only behaviour is a separate type: the replica read source implements the read port only and has no write method returning "unsupported" |
| **Interface segregation** | `internal/readpath/rangereader` depends on a port with one method, `ReadDocumentRange`, so it cannot reach a send path. The S3 listing handler depends on a lister port, not on the whole object service |
| **Dependency inversion** | Services depend on ports they declare. `internal/core` supplies the adapters. `internal/projection/log` declares the `Tx` port; `internal/storage` satisfies it |

---

## 8. Code style

### 8.1 Comments

No comment spans more than one line. Two consecutive comment lines are a
defect; extract a named function instead. A single-line comment is allowed
only where a non-obvious external constraint cannot be expressed in a name: an
ordering requirement, a protocol quirk, a deliberate deviation.

Allowed:

```go
// Telegram rejects range offsets that are not 4 KiB aligned.
if offset%limits.RangeReadAlignment != 0 {
	return nil, errMisalignedOffset
}
```

```go
// The put flag must be durable before the send, or a crash hides an uncertain commit.
if err := jobs.MarkPutAttempted(ctx, job.ID); err != nil {
	return err
}
```

Not allowed:

```text
// NewService creates a new put service.
// It validates its dependencies.
func NewService(deps Dependencies) (*Service, error) {
```

```text
// increment the counter
attempts++
```

```text
// return s.legacySend(ctx, op)
```

```text
// TODO: handle flood wait
```

- No comment blocks, including doc comments on exported identifiers.
- No comment restating what the next line does.
- No commented-out code.
- No `TODO`, `FIXME` or `XXX`. Do the work or report it.
- No `/* */` comments anywhere.

`scripts/nocomments.sh` enforces this. Build constraints (`//go:build`) and
`//go:embed` directives are directives, not comments, and are allowed. If an
explanation needs a paragraph, it belongs in `docs/`.

Go snippets inside documents obey the same policy. Forbidden examples are shown in `text` fences so they are never mistaken for code to copy.

### 8.2 Naming carries the meaning

Names replace comments. `putAttempted`, `storedSize`, `supersededBodyMsgID`,
`tombstoneMsgID`, `acquireGetFileSlot`, `publishByRename` each say what they
are without help.

Never name by type: not `objectStruct`, not `dataMap`, not `utils`, not
`helpers`, not `common`. Never abbreviate beyond the domain's own vocabulary:
`DEK`, `KEK`, `AEAD`, `ETag`, `DC`, `pts`, `SigV4` and `TDX1` are established
terms and stay.

### 8.3 Functions

Small, one level of abstraction each, early returns over nesting. A function
that needs a comment to explain its sections is several functions.

### 8.4 Errors and stable codes

- Sentinel errors live in `internal/domain/errs` and are wrapped with `%w` at
  every boundary, prefixed with the package name:
  `fmt.Errorf("objectrepo: get %q: %w", key, errs.ErrNoSuchKey)`.
- Every sentinel maps to a stable `errs.Code` (a string type). Callers branch
  on `errors.Is`, `errors.As` and codes, never on message text.
- Transport errors keep their types across every boundary.
  `FloodWaitError` (in `internal/tgclient/flow`) carries the wait duration.
  `ErrSendOutcomeUnknown` (in `internal/tgclient/send`) is never collapsed into
  a generic failure: a message that may exist is a different fact from a
  message that does not.
- Only `internal/s3api/s3err` turns errors into S3 XML error responses. Only
  `internal/adminapi/handlers` turns them into admin API JSON
  `{"code": "...", "message": "..."}` bodies. No other package writes an HTTP
  status.
- Error messages never contain secrets, signatures, session data or object
  bytes.

The starting catalogue. Default (owner may change); once phase 01 lands,
`internal/domain/errs` and `internal/s3api/s3err` are the source of truth and
this table follows them.

| Sentinel | `errs.Code` | S3 error code | HTTP |
| --- | --- | --- | --- |
| `ErrNoSuchBucket` | `bucket.not_found` | `NoSuchBucket` | 404 |
| `ErrNoSuchKey` | `object.not_found` | `NoSuchKey` | 404 |
| `ErrBucketExists` | `bucket.exists` | `BucketAlreadyOwnedByYou` | 409 |
| `ErrBucketNotEmpty` | `bucket.not_empty` | `BucketNotEmpty` | 409 |
| `ErrInvalidBucketName` | `bucket.invalid_name` | `InvalidBucketName` | 400 |
| `ErrInvalidKey` | `object.invalid_key` | `KeyTooLongError` or `InvalidArgument` | 400 |
| `ErrObjectTooLarge` | `object.too_large` | `EntityTooLarge` | 400 |
| `ErrAccessDenied` | `access.denied` | `AccessDenied` | 403 |
| `ErrUnknownAccessKey` | `access.unknown_key` | `InvalidAccessKeyId` | 403 |
| `ErrSignatureMismatch` | `access.signature_mismatch` | `SignatureDoesNotMatch` | 403 |
| `ErrRequestExpired` | `access.request_expired` | `AccessDenied` (presign expired) or `RequestTimeTooSkewed` | 403 |
| `ErrPreconditionFailed` | `request.precondition_failed` | `PreconditionFailed` | 412 |
| `ErrInvalidRange` | `request.invalid_range` | `InvalidRange` | 416 |
| `ErrBadDigest` | `request.bad_digest` | `BadDigest` | 400 |
| `ErrNoSuchUpload` | `multipart.no_such_upload` | `NoSuchUpload` | 404 |
| `ErrInvalidPart` | `multipart.invalid_part` | `InvalidPart` | 400 |
| `ErrSlowDown` | `telegram.slow_down` | `SlowDown` with `Retry-After` | 503 |
| `ErrNotImplemented` | `request.not_implemented` | `NotImplemented` | 501 |
| `ErrMissingDependency` | `internal.missing_dependency` | `InternalError` | 500 |
| any unmapped error | `internal.error` | `InternalError` | 500 |

`ErrSendOutcomeUnknown` never reaches a client as success. A PUT that ends in
an uncertain send returns `InternalError` 500 and the sweeper reconciles the
job (§10, invariant 4).

### 8.5 Concurrency

- Shared state sits behind the narrowest lock that works. Prefer ownership by
  one goroutine over shared locking.
- Every blocking call takes a `context.Context` and honours cancellation.
- Process-wide limits (Telegram send slots, `getFile` slots, the send-rate
  limiter, transform worker slots) are injected values, not package globals.

### 8.6 Tests

- Table tests beside the source, with named cases.
- Unit tests never touch the network or real Telegram. Telegram behaviour is
  scripted through `internal/tgclient/faketg`, including flood waits, uncertain
  sends, partial deletes and file-reference expiry.
- A Telegram test-DC smoke test runs only behind the build tag and environment
  gate defined in phase 04.
- Tests that need SQLite open real databases in `t.TempDir()` through
  `internal/storage/sqlitedb`; they never mock SQL.
- Format packages (`wire`, `crypto/format`) carry round-trip and malformed-input
  vectors, and fuzz targets where a phase doc names them.

### 8.7 Formatting

`gofmt` is final. Tabs for indentation. Imports in three groups separated by a
blank line: standard library, third party, this module.

---

## 9. Storage engine rules

The full model is in `docs/spec/storage-model.md`. These are the rules every
engine change must respect.

### 9.1 Telegram layout

- The registry channel holds `t=bcreate`, `t=bcfg`, `t=bdel` and per-bucket
  `t=encfg`. Its ID lives in `state.json` and is checked against the registry
  marker message on every start (E1).
- Each bucket is its own private broadcast channel holding object bodies and
  object ops (E1).
- An object is a body document with caption `TDX1|t=body|j=<job id>|...` plus a
  `t=put` text message carrying key, body message ID, stored size, plaintext
  size, MD5 ETag, SHA-256, content type, encryption fields and user metadata.
  The put message ID is the object's version identity (E2).
- Documents are always sent as `ForceFile` with `application/octet-stream`, so
  Telegram never recompresses or re-encodes them.

### 9.2 Databases

- `projection.db` holds `replay_log` and every derived table, so they commit
  atomically together (E7).
- `control.db` holds local-only state: access keys, admin devices, the setup
  token, jobs and their journal, idempotency records, the replica journal and
  the multipart staging index (E7).
- Each database has exactly one writer connection and a read-only WAL pool,
  with pragmas applied per connection by `internal/storage/sqlitedb` (E7).
  Writes never go through a pool connection.
- Migrations are embedded SQL files under `internal/storage/schema`:
  `projection/NNNN_*.sql` and `control/NNNN_*.sql`, discovered by embed glob
  with no shared list file.

### 9.3 Write path

Spool to disk, hash (MD5 for the ETag, SHA-256 for integrity), plan by stored
size, persist the job row, send the body, mark the put attempted, send the put,
project, then delete superseded messages. The phase 06 document owns the exact
state machine.

### 9.4 Telegram account safety

The conservative send-rate limiter is on by default and `FLOOD_WAIT` is an
additional floor (E10). Never disable the limiter in production code, never
retry inside a flood wait, and never add a code path that sends without going
through `internal/tgclient/flow`.

### 9.5 Secrets

- One primitive, `internal/platform/secretfile`, writes every secret file: temp
  file in the same directory, `chmod 0600` before the first secret byte, write,
  sync, close, rename, fsync the directory.
- Secrets enter configuration only as file paths, never as inline values or
  environment variable contents.
- Never log, return in an error, or send to the GUI: the server key file, any
  DEK, `api_hash`, the Telegram session, login codes, the 2FA password,
  access-key secrets, SigV4 signatures, setup tokens or device tokens.
- Startup refuses to run with a server key file that does not match the
  registry fingerprint (A6).

---

## 10. Invariants

Assert these in code and test each one by name. Each has a failure mode that
loses or corrupts data.

1. **Single projection door.** Every op, local or synced, enters through
   `ProjectFromOp` or `ProjectFromOpTx` in `internal/projection/log`. There is
   no other legal way to mutate `replay_log` or a derived table.
2. **No transaction across network I/O.** No SQLite transaction is open across
   a Telegram call, an HTTP body read, a transform worker call or any other
   network wait. Spool first, then apply in a short transaction.
3. **Plan by stored size.** Size checks use the stored (ciphertext) size from
   `internal/crypto/format`, never the plaintext size. An object just under
   1900 MiB of plaintext can exceed `limits.MaxStoredObjectBytes`
   (`1900 * 1024 * 1024`) once encrypted, and is rejected with
   `EntityTooLarge` before anything is sent.
4. **Put attempted before send.** The job row's `put_attempted` flag is
   committed **before** the `t=put` send, never after. Before it is set, a
   failed job may delete its body message. After it is set, the job is
   uncertain: the sweeper reconciles it against Telegram and never deletes the
   body or resends blind.
5. **Block rows after fsync.** A `blockcache` row is inserted only after the
   block's bytes are written and synced. A row never claims bytes that are not
   on disk.
6. **Key copies are cleared.** Every copy of the server key, a KEK or a DEK is
   zeroed on every return path, error paths included.
7. **Generation gate.** A component that holds a decrypted DEK beyond a single
   request (a cached decrypting reader, a transform input session) re-checks
   the keyring generation under the gate before publishing it. Deleting a
   bucket or changing its encryption configuration advances the generation.
8. **Rewind, never resume.** A send retry rewinds a seekable body to offset
   zero. It never resumes mid-stream.
9. **Bodies are not objects.** A body message never creates an object row. An
   object exists only through an applied `t=put`.
10. **The projector never reads the clock.** Domain timestamps come from the op
    or the message date.
11. **Codes, not text.** Error branching uses `errors.Is`, `errors.As` and
    stable codes, never message text.
12. **Tokens and hosts are checked.** The S3 listener authenticates every
    request by SigV4 header or presigned query, or allows it only under a
    public-read bucket policy for `GET` and `HEAD`. The admin listener requires
    a valid device token on every request except the one-time setup-token claim
    (A1, A2). Signed media URLs are verified before any work. Any listener bound
    to loopback validates `Host` against its configured names to block DNS
    rebinding. The transform worker accepts requests only over its unix socket,
    created mode `0600` inside the data directory. Default (owner may change)
    for the socket rule.
13. **Last writer wins by message ID.** For one key, the `t=put` with the
    highest message ID is the object. Never resolve by local clock, request
    arrival order or `Last-Modified`. Superseded body and put messages are
    deleted only after the new put is committed and projected (E3).
14. **Tombstone before delete.** A delete sends `t=del` and projects it before
    calling `deleteMessages`, which runs in batches of 100 IDs. A failed batch
    is retried from the tombstone; nothing resurrects the key (E4).
15. **Unknown op is skippable.** An op type this binary does not know is
    recorded as an `unknown_op` reject with its raw header kept in `replay_log`,
    so a newer binary's rebuild applies it. It never fails the sync page and is
    never silently dropped (E5).
16. **Per-key ordering repair.** Repair runs only when a newly ingested op has a
    lower message ID than an op already recorded for the same key, and it
    replays that key alone. Never rebuild the whole projection for one
    out-of-order op (E6).
17. **Salt and nonce before first send.** For an encrypted object, the TDE1
    file salt (16 bytes) and nonce prefix (20 bytes) are persisted in the job
    row before the first byte is sent. A retry reuses them; it never
    re-encrypts under new parameters (E8).
18. **Idempotency per access key.** Every idempotency record is keyed by access
    key ID. Telegram `random_id` values come from `StableRandomID` over a
    server-minted job ID and a step name, never from a client-supplied value
    (E9).
19. **Originals byte for byte.** An object is stored and served exactly as
    uploaded. Metadata stripping and auto-orient apply only to transform output
    (A4).
20. **Startup checks before serving.** The registry marker must match
    `state.json` and the key file must match the registry fingerprint before
    any listener starts.

### The three things that bite

- **"Telegram accepted it" is not "the projection applied it."** A send can
  return a valid message ID *and* an error. The message exists. Do not resend
  it; record what you know and let the sweeper or the syncer reconcile.
- **S3 multipart upload is not TDX1 multipart.** S3 parts are staged on disk by
  `internal/ingest/multipartstage`, assembled into one spool file and sent as
  one Telegram document (D9). The legacy `t=part` / `t=manifest` ops do not
  exist in butterDB.
- **There is one writer connection per database.** A write transaction held
  across any network wait stalls every writer in the process, and every PUT
  behind it.

---

## 11. Porting TDrive

TDrive (MIT) is the reference implementation of the storage engine (D18). The
file-by-file mapping is [`docs/reference/tdrive-port-map.md`](reference/tdrive-port-map.md);
the lane brief names the exact files for each port.

1. **Confirm before citing.** Open or list a TDrive file before naming it in
   code review, a commit or a document. Never cite a path you have not seen.
2. **TDrive is read-only.** Never edit, build into or commit from the TDrive
   checkout. Copy what you port into your owned paths.
3. **Port into butterDB's architecture, not TDrive's.** Replace TDrive's
   `TDrive/backend/...` imports with this module's packages, move globals and
   ambient time into injected dependencies, split packages along the §5.1
   layout, and declare ports on the consumer side.
4. **Strip every comment block.** TDrive is heavily commented. Ported code
   keeps at most a single-line comment for an external constraint. Knowledge
   worth keeping moves into `docs/spec/` or the phase document.
5. **Keep constants verbatim.** A retained constant keeps its value and moves
   into `internal/domain/limits` or the owning package.
6. **Drop what butterDB does not ship.** Wails bindings, the mount and WebDAV,
   HLS, folders, shared drives, the passphrase vault, mobile and Android or iOS
   build files, the updater, and TDX1 multipart are not ported.
7. **Port the tests too.** Every TDrive test that covers retained behaviour is
   ported and kept passing; tests for dropped behaviour are dropped.
8. **Never add a TDrive dependency by habit.** A module enters `go.mod` only
   through a phase prelude. `github.com/wailsapp/wails` never does.
9. **Attribute.** Every ported package is listed in
   `docs/legal/THIRD_PARTY_NOTICES.md` with TDrive's MIT notice, copied
   verbatim from TDrive's `LICENSE`. The lane that ports a package reports it;
   the integration lane adds the row if the lane does not own that file.

---

## 12. Git

The orchestration procedure (branches, worktrees, agents, review, cherry-pick,
merge) is in [`docs/phases/00-execution-protocol.md`](phases/00-execution-protocol.md).
It is not restated here. These rules apply to everyone who commits.

### 12.1 Author

Every commit, with no exceptions, uses `git -c` flags. Never edit git config.

```bash
git -c user.name=prieyan -c user.email=mnprieyan@gmail.com commit -m "<one line>"
```

### 12.2 Message

One line. Simple English. Lowercase. Imperative or simple present. No body, no
description, no trailing period, no scope prefix, no issue reference, no
co-author or generated-by footer. `scripts/hooks/commit-msg`, installed by
`scripts/install-hooks.sh`, rejects anything else.

Good:

```
add tdx1 put op formatter and round trip tests
port range reader from tdrive into readpath
fix stored size check for encrypted objects
wire s3 listener into the serve command
```

Not acceptable:

```
feat(wire): add put op

Adds the put op with tests.
```

```
Fixed bug.
```

### 12.3 Never push

`git push` is never run. `.claude/settings.json` denies `Bash(git push:*)`.
Commits stay local; publishing is the owner's decision. Tags are created only
by the owner gate in phase 11, and are local.

### 12.4 Worktrees and lanes

- One worktree per lane, created by the orchestrator at
  `../butterdb-wt/NN-<lane>` on branch `phase/NN-slug-<lane>`. A worker works
  only inside its own worktree.
- A worker never creates, removes or switches worktrees or branches, never
  checks out `main`, and never touches another lane's worktree.
- Stage only owned paths, by name: `git add -- <path> <path>`. Never
  `git add -A`, `git add .` or `git commit -a`.
- Never touch `go.mod`, `go.sum` or `go.work` in a lane. Build and test with
  `-mod=readonly`. If a module is missing, stop and report; do not work around
  it with `replace`, vendoring or a copied file.
- Never amend, rebase, reset, revert or cherry-pick over a commit you did not
  make in this task. Never use `--force` in any git command.
- Git serializes on lock files. On a held lock, wait and retry; never delete a
  `.lock` file.
- Never `git stash` work you do not own.
- Commit in the steps the lane brief lists, with the exact messages it gives.
- Report every resulting commit hash and subject.

Branch creation, worktree setup, cherry-picks onto the phase branch and the
fast-forward of `main` belong to the orchestrator alone.

---

## 13. Before you call something done

Run the checks for every module you changed. `scripts/verify.sh` runs the
root-module set once phase 01 has landed it; until then run the commands
directly.

Root module:

```bash
gofmt -l .
GOWORK=off go vet -mod=readonly ./...
GOWORK=off CGO_ENABLED=0 go build -mod=readonly ./...
GOWORK=off go test -mod=readonly ./...
scripts/layering.sh
scripts/nocomments.sh
```

Where a C toolchain is available, also run
`GOWORK=off go test -race -mod=readonly ./...` over the packages you changed.
The race detector needs cgo for the test binary only; the server build stays
`CGO_ENABLED=0`.

Admin GUI, when `apps/admin` changed (needs the Fyne system libraries):

```bash
cd apps/admin && go vet -mod=readonly ./... && go test -mod=readonly ./...
```

Transform worker, when `apps/transformd` changed (needs libvips, libheif and
ffmpeg):

```bash
cd apps/transformd && go vet -mod=readonly ./... && go test -mod=readonly ./...
```

Then check by hand:

- `gofmt -l .` printed nothing.
- No comment spans more than one line; no `TODO`, `FIXME` or doc comment.
- Every changed path is inside your owned paths.
- `go.mod`, `go.sum` and `go.work` are unchanged (lanes).
- No new import breaks §5.3.
- Every new dependency arrives through a constructor and is nil-checked.
- Every new port is declared by its consumer.
- Every invariant your change touches has a named test.
- Every ported TDrive package is reported for `THIRD_PARTY_NOTICES.md`.
- Any new prose is in `docs/`.
- Every commit is one lowercase line with author `prieyan <mnprieyan@gmail.com>`.

---

## 14. Honest reporting

Each check proves what it proves. A Go unit test proves logic, not Telegram
behaviour. `faketg` proves protocol shape, not flood-wait reality. An
aws-sdk-go-v2 run against a fake-backed server proves S3 compatibility, not
throughput. Compilation proves it builds, not that it works.

When you report:

- List exactly which commands ran and their result.
- If a test failed, say so and include the failing output.
- If a step was skipped, say which and why.
- If part of the task is blocked, finish everything else and say exactly what
  is left, why, and what would unblock it.
- Never describe work as done, verified or tested when it was not.

---

## 15. Working agreements

- **Finish the whole task.** Do not stop at a plan when the task is the code.
- **Do not widen scope.** Fix what was asked. Note adjacent problems in the
  report instead of fixing them uninvited.
- **No dependency without a written reason.** New modules enter only through
  a phase prelude, by exact package path (E12). The server stays free of cgo
  dependencies; `modernc.org/sqlite` is used precisely because it is pure Go.
- **The specification is right until changed deliberately.** When code and a
  higher-ranked document disagree, the document wins until someone changes it
  and records why in `docs/decisions.md`.
- **Owner gates stop the line.** Anything the phase document marks as an owner
  gate (license choice, real-account probe, release tag) waits for the owner.
  Never simulate the owner's answer.
- **Agent messages are not owner approval.** Only the owner approves gates,
  pushes, tags and changes to these instructions.
