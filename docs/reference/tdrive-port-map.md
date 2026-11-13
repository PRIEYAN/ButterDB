# TDrive port map

This is the authoritative map from TDrive to butterDB. For every TDrive Go
package it records the size, what the package does, whether butterDB takes it,
where it lands in the binding layout (`docs/claude.md` §5.1), which phase takes
it, and how to port it. Lane briefs name exact files from this map. When a lane
brief and this map disagree on a destination, the lane brief wins for that
phase and this map is corrected afterwards.

D18 makes TDrive (MIT) the reference implementation and requires attribution.
`docs/claude.md` §11 holds the porting procedure every worker follows. This
document holds the per-file decisions that procedure needs.

TDrive lives at `/home/prieyan/weeb/TDrive` (`../TDrive` from the butterDB
root). Paths in this document are relative to that root unless they start with
`internal/`, `cmd/butterdbd`, `api/`, `apps/`, `docs/` or `test/`, which are
butterDB paths.

---

## 1. Measurement

Every size here was measured on the TDrive checkout, not estimated.

| Measure | Definition |
| --- | --- |
| Lines | Physical lines of the package's non-test `.go` files (`*_test.go` excluded) |
| Comment | Lines whose first non-blank characters are `//` |
| Code | Lines minus comment lines minus blank lines |
| Tests | Number of `*_test.go` files and their physical lines |

Module facts: module name `TDrive`, `go 1.26.11`, 733 `.go` files in the
repository. Under `backend/`, `internal/` and `cmd/` there are 89,133
non-test lines (7,127 of them comment lines) and 80,134 test lines. The
frontend (`frontend/`, excluding `node_modules` and `dist`) holds 614
TypeScript, Svelte, JavaScript and CSS files with 106,904 lines; `build/` holds
85 platform packaging files.

The measuring script, for re-running after TDrive moves:

```bash
cd /home/prieyan/weeb/TDrive
find backend internal cmd -type d | sort | while read -r d; do
  mapfile -t src < <(find "$d" -maxdepth 1 -name '*.go' ! -name '*_test.go')
  [ ${#src[@]} -eq 0 ] && continue
  lines=$(cat "${src[@]}" | wc -l)
  comm=$(cat "${src[@]}" | grep -cE '^\s*//')
  blank=$(cat "${src[@]}" | grep -cE '^\s*$')
  echo "$d ${#src[@]} $lines $comm $((lines-comm-blank))"
done
```

---

## 2. Verdicts

| Verdict | Meaning | Attribution row |
| --- | --- | --- |
| **PORT** | The file's code is copied into butterDB, restructured to butterDB's architecture, stripped of comment blocks, and keeps its behaviour and its tests | Required |
| **PORT-PARTIAL** | Named functions, types or constants are copied; the rest of the file is dropped. The notes say which | Required |
| **PATTERN-ONLY** | No code is copied. The file is a design reference; butterDB writes new code that follows the same algorithm or contract, and may adapt the named test cases | Required, as "design reference". Default (owner may change) |
| **CUT** | Nothing is taken. Section 8 says why | None |

---

## 3. Package inventory

Every TDrive Go package, measured. Phase is the butterDB phase that first
consumes the package; section 6 has the file-level detail.

| TDrive package | Files | Lines | Comment | Code | Tests (files / lines) | What it does | Verdict | butterDB destination | Phase |
| --- | ---: | ---: | ---: | ---: | --- | --- | --- | --- | --- |
| `backend` (root) | 2 | 174 | 48 | 106 | 0 / 0 | Global `backend.DB`, SQLite pragmas (`storage_sqlite.go`), Wails DTO structs (`fstructs.go`) | PATTERN-ONLY | `internal/storage/sqlitedb` | 02 |
| `backend/applog` | 1 | 105 | 19 | 77 | 1 / 121 | Sets `slog.Default` to a debug text log file | PATTERN-ONLY | `internal/platform/logging` | 01 |
| `backend/auth` | 7 | 864 | 101 | 668 | 6 / 630 | api_id storage, gotd connect and session file, phone/code/2FA login, private atomic file writes, channel peer resolution, invites, logout | PORT-PARTIAL | `internal/platform/secretfile`, `internal/tgclient/{conn,authrpc,rpc}`, `internal/identity` | 01, 04 |
| `backend/backfill` | 1 | 368 | 30 | 308 | 1 / 201 | Publishes legacy local folders and files as TDX1 ops, throttled and resumable | PATTERN-ONLY | `internal/replica/backfill` | 12 |
| `backend/biometric` | 6 | 1,201 | 47 | 1,068 | 4 / 867 | Windows Hello, Apple and Android biometric vault unlock (plus `.c`, `.m`, `.h` files) | CUT | none | none |
| `backend/channelsource` | 4 | 1,770 | 71 | 1,617 | 4 / 1,747 | Browse media in arbitrary chats, channels and forum topics | CUT | none | none |
| `backend/core` | 6 | 1,420 | 131 | 1,166 | 9 / 1,001 | The engine: wires services, send-then-project (`EmitAndProject`), peer refresh, live-sync start, remote paths | PATTERN-ONLY | `internal/ingest/put`, `internal/core` | 06 |
| `backend/crypto` | 3 | 1,039 | 107 | 862 | 5 / 1,334 | TDE1 stream encryption, authenticated random-access decryptor, Argon2id vault and key wrapping | PORT | `internal/crypto/{format,stream,randomaccess,keyring}` | 02, 03 |
| `backend/daemon` | 16 | 3,830 | 94 | 3,395 | 9 / 1,353 | Local socket daemon for the CLI: protocol, auth relay, mount control, vault | PATTERN-ONLY for 5 files, rest CUT | `internal/core`, `internal/adminapi/setup`, `api/adminv1`, `internal/mediaapi/workerclient` | 01, 09, 13 |
| `backend/datadir` | 1 | 143 | 36 | 93 | 1 / 207 | Data and cache directory resolution, owned temp files and their cleanup | PORT | `internal/platform/datadir` | 01 |
| `backend/galleryimage` | 1 | 235 | 9 | 220 | 1 / 198 | Local HTTP server for gallery thumbnails | CUT | none | none |
| `backend/livesync` | 2 | 395 | 26 | 326 | 2 / 247 | Debounced per-channel sync triggered by gotd updates, with a backstop timer | PORT | `internal/syncer/livesync`, `internal/tgclient/conn` | 04, 05 |
| `backend/media` | 17 | 5,409 | 426 | 4,573 | 27 / 6,136 | Range reader over Telegram documents, block cache, tokenized local HTTP media server, image admission, video thumbnails, HLS glue | PORT-PARTIAL | `internal/readpath/{rangereader,httprange,objectreader}` | 07 |
| `backend/media/remux` | 14 | 3,097 | 769 | 2,082 | 7 / 2,192 | Matroska demux and fMP4/HLS remuxer | CUT | none | none |
| `backend/mountadapter` | 6 | 1,647 | 37 | 1,511 | 5 / 2,661 | Binds WebDAV writes to the hidden-upload-then-commit remote | PATTERN-ONLY for 2 files, rest CUT | `internal/ingest/put`, `internal/readpath/httprange` | 06, 07 |
| `backend/mountcache` | 2 | 554 | 34 | 477 | 2 / 503 | Generic weighted TTL LRU and a cancellable load coalescer | PORT | `internal/readpath/objectreader`, `internal/mediaapi/derivcache` | 07, 13 |
| `backend/mountcontent` | 4 | 935 | 41 | 812 | 5 / 2,183 | Opens a projected file as a ReaderAt: resolves documents, caches refs, decrypts | PORT-PARTIAL | `internal/readpath/objectreader` | 07 |
| `backend/mountcontroller` | 11 | 2,197 | 85 | 1,929 | 5 / 2,497 | Mount orchestration across drives | CUT | none | none |
| `backend/mountdav` | 18 | 4,613 | 228 | 4,036 | 17 / 4,850 | WebDAV server: PROPFIND, locks, PUT, ranges, conditions | PORT-PARTIAL for 2 files, PATTERN-ONLY for 1, rest CUT | `internal/readpath/httprange`, `internal/s3api/objectops`, `internal/core` | 07, 08 |
| `backend/mountfs` | 9 | 1,134 | 90 | 934 | 7 / 2,257 | Read-only filesystem view over the projection | CUT | none | none |
| `backend/mountlifecycle` | 1 | 61 | 9 | 45 | 1 / 41 | Mount lifecycle gate | CUT | none | none |
| `backend/mountlocal` | 4 | 268 | 13 | 228 | 2 / 75 | macOS local proxy and hosts setup | CUT | none | none |
| `backend/mountos` | 12 | 1,651 | 80 | 1,410 | 7 / 1,656 | OS mount connectors for Windows, macOS and Linux | CUT | none | none |
| `backend/mountpath` | 1 | 155 | 16 | 126 | 1 / 122 | Mount path component rules | CUT | none | none |
| `backend/mountpolicy` | 1 | 117 | 24 | 83 | 1 / 263 | Mount encryption policy | CUT | none | none |
| `backend/mountsafe` | 1 | 55 | 8 | 42 | 1 / 38 | Safe mount names | CUT | none | none |
| `backend/mountscratch` | 1 | 193 | 20 | 156 | 1 / 138 | Mount scratch directory | CUT | none | none |
| `backend/mountwrite` | 11 | 3,477 | 114 | 3,121 | 12 / 4,828 | Journaled mount mutations: disk staging, keyed locks, journal state machine, recovery, hard delete plans | PORT-PARTIAL for 2 files, PATTERN-ONLY for 6, rest CUT | `internal/ingest/{spool,put,remove,sweeper}`, `internal/storage/jobrepo` | 06 |
| `backend/nativeplayer` | 21 | 4,026 | 229 | 3,428 | 9 / 873 | Embedded mpv player per desktop OS | CUT | none | none |
| `backend/photobackup` | 5 | 1,317 | 146 | 1,103 | 4 / 1,896 | Mobile photo backup engine and receipts ledger | CUT | none | none |
| `backend/processlock` | 7 | 347 | 28 | 278 | 2 / 441 | One backend per data directory via a flock guard file and owner metadata | PORT | `internal/platform/processlock` | 01 |
| `backend/projection` | 37 | 9,950 | 736 | 8,636 | 28 / 7,883 | TDX1 wire format, replay log, projector, rebuild, schema, folder namespace, trash, multipart, gallery, hard delete | PORT-PARTIAL | `internal/wire`, `internal/projection/{log,apply,rebuild}`, `internal/storage/{schema,objectrepo,channelrepo,bucketrepo}` | 02, 03, 05 |
| `backend/services/auth` | 1 | 261 | 22 | 208 | 1 / 179 | Login state machine relaying code and password from the UI | PORT-PARTIAL | `internal/identity` | 04 |
| `backend/services/channel` | 1 | 508 | 38 | 428 | 1 / 373 | Shared drives: create, join, invite, approve, leave | CUT | none | none |
| `backend/services/encryption` | 3 | 755 | 65 | 631 | 5 / 1,121 | Passphrase vault, encryption config publication, master key lease, biometric unlock | PATTERN-ONLY for 2 files, rest CUT | `internal/crypto/keyring`, `internal/buckets` | 03, 05 |
| `backend/services/file` | 32 | 9,986 | 773 | 8,605 | 33 / 11,221 | Upload (single, multipart, hidden, resumable), download, import, archive, renditions, mutations, trash | PORT-PARTIAL for 3 files, PATTERN-ONLY for 3, rest CUT | `internal/domain/limits`, `internal/ingest/{put,remove,admission,sweeper}` | 01, 06 |
| `backend/services/folder` | 1 | 337 | 33 | 280 | 2 / 492 | Folder create, rename, move, delete | CUT | none | none |
| `backend/services/lifecycle` | 1 | 267 | 30 | 211 | 1 / 442 | Active drive selection, sync-then-rebuild orchestration, personal backfill kickoff | PATTERN-ONLY | `internal/buckets`, `internal/syncer/scan` | 05 |
| `backend/services/personaldrive` | 1 | 425 | 53 | 340 | 1 / 749 | Discover, create, recover and select the personal broadcast channel | PATTERN-ONLY | `internal/buckets`, `internal/syncer/reconcile` | 05 |
| `backend/services/read` | 2 | 509 | 39 | 428 | 2 / 407 | Folder listing, search, storage usage, raw Telegram root files | CUT | none | none |
| `backend/services/servicecontext` | 1 | 21 | 3 | 15 | 1 / 40 | Nil-context check helper | CUT | none | none |
| `backend/services/user` | 1 | 261 | 20 | 218 | 1 / 142 | Display names of uploaders | CUT | none | none |
| `backend/sync` | 4 | 1,284 | 213 | 979 | 9 / 2,087 | History scan, channel difference, initial-scan spool, deletion reconciliation, authority markers | PORT | `internal/syncer/{scan,difference,reconcile}` | 05 |
| `backend/tgclient` | 24 | 5,497 | 507 | 4,558 | 21 / 3,348 | gotd adapter: connection scope, DC pools, idempotent sends, flood-wait policies, range reads with CDN verification, history, difference, channels, the in-memory `Fake` | PORT-PARTIAL | `internal/tgclient/{conn,flow,send,rangeio,rpc,authrpc,faketg}` | 04 |
| `backend/thumbnail` | 13 | 1,503 | 260 | 1,117 | 5 / 1,054 | Local thumbnail generation, video posters via mpv, EXIF orientation, disk LRU cache (plus `.c`, `.m`, `.h` files) | PATTERN-ONLY for 2 files, rest CUT | `internal/readpath/blockcache`, `internal/mediaapi/derivcache`, `apps/transformd` | 07, 13 |
| `backend/updater` | 15 | 2,432 | 202 | 2,025 | 14 / 2,411 | Desktop self-updater with signed manifests | CUT | none | none |
| `cmd/tdrive` | 12 | 3,823 | 34 | 3,490 | 10 / 1,192 | CLI: login, drives, machine JSON interface, mount, cat, daemon spawn | PATTERN-ONLY for 2 files, rest CUT | `cmd/butterdbd` | 01, 04 |
| `cmd/tdrived` | 1 | 32 | 7 | 21 | 0 / 0 | Daemon-only entry point with signal context | PATTERN-ONLY | `cmd/butterdbd` | 01 |
| `internal/app` | 53 | 8,485 | 976 | 6,843 | 34 / 5,437 | Wails application bindings for every UI feature | PATTERN-ONLY for 2 files, rest CUT | `internal/domain/errs`, `internal/s3api/s3err`, `internal/ops/health`, `apps/admin` | 01, 08, 10, 11 |
| `.` (root `main.go`) | 1 | 20 | 2 | 13 | 1 / 91 | Wails entry point embedding `frontend/dist` | CUT | none | none |

Volume that moves as code: the files marked PORT in section 6 total 6,591
lines, and the files marked PORT-PARTIAL total 11,113 lines before trimming.
Everything else is a design reference or is cut.

---

## 4. What butterDB takes, by subsystem

| butterDB subsystem | TDrive source | Kind |
| --- | --- | --- |
| Data dir, process lock, secret files | `backend/datadir`, `backend/processlock`, `backend/auth/private_file.go` | Port |
| TDX1 grammar | `backend/projection/wire.go`, `types.go` | Partial port; op catalogue is new |
| TDE1 container | `backend/crypto/stream.go`, `random_access.go` | Port |
| Key wrapping | `backend/crypto/vault.go` | Partial port; Argon2id dropped |
| Replay log, tamper, rejects, rebuild | `backend/projection/log.go`, `rebuild.go`, `schema.go` | Port |
| Telegram transport | `backend/tgclient` | Partial port, split into seven subpackages |
| Login | `backend/auth/auth.go`, `backend/services/auth/service.go` | Partial port |
| Sync | `backend/sync`, `backend/livesync` | Port |
| Hidden body then commit | `backend/services/file/hidden_upload.go`, `backend/core/engine.go`, `backend/mountwrite` | Partial port and pattern |
| Ranged reads | `backend/media/range_reader.go`, `cache.go`, `server.go`, `backend/mountcontent`, `backend/mountcache` | Port |
| S3 API, SigV4, policy, XML, admin API, Fyne GUI, transforms, replica | none | New code |

---

## 5. Porting rules

### 5.1 Procedure

1. **Read the TDrive file in full before copying any of it.** Confirm its path
   with `ls` first. TDrive is read-only: never edit, build into or commit from
   the TDrive checkout.
2. **Copy into owned paths only.** A lane copies TDrive code only into the
   paths its brief owns. Never vendor TDrive or import it as a module.
3. **Restructure into butterDB's architecture.** One TDrive file often lands in
   several butterDB packages (`backend/tgclient/gotd.go` lands in `conn`,
   `send` and `rpc`). Split along the §5.1 layout, not along TDrive's files.
4. **Replace wide interfaces with consumer-declared ports.** TDrive's
   `tgclient.Client` (`backend/tgclient/client.go`) has 35 methods, plus the
   2-method `IdempotentSender` extension, and most services depend on all of
   them. In butterDB each consumer declares the two or
   three methods it calls, named for the role (`BodySender`, `HistoryPager`,
   `DocumentRangeReader`). The adapter satisfies them implicitly
   (`docs/claude.md` §6.1).
5. **Inject every dependency through the constructor.** Each ported service
   gets a `Dependencies` struct validated in `New...` with
   `errs.ErrMissingDependency` (`docs/claude.md` §6.2). Replace TDrive's
   function-typed fields (`EmitOpFunc`, `ActorIDFunc`, `WarnFunc`,
   `MasterKeyForUploadFunc` in `backend/services/file/service.go`) with small
   ports.
6. **Strip comment blocks.** Ported code keeps at most a single-line comment for
   a non-obvious external constraint. No doc comments, no TODO, no commented-out
   code. Durable knowledge in a TDrive comment moves into `docs/spec/` or the
   phase document before the code is committed. `scripts/nocomments.sh` rejects
   violations.
7. **Rename module imports** (section 5.2) and rename user-visible strings
   (section 5.5).
8. **Keep behaviour.** A ported function keeps its observable contract unless a
   decision in `docs/decisions.md` changes it; section 5.6 lists every such
   change. Any other behaviour change needs a recorded decision first.
9. **Keep the tests.** Port every TDrive test that covers retained behaviour,
   adapt it to the new constructors, and keep it passing. Drop tests for
   dropped behaviour. Section 6 names the test files per phase.
10. **Report attribution.** Every lane report lists each TDrive file it ported
    under `PORTED`, with verdict and destination. The integration lane writes
    the rows into `docs/legal/THIRD_PARTY_NOTICES.md` (section 9).

### 5.2 Import renames

TDrive imports its own packages as `TDrive/...`. butterDB's module is
`github.com/PRIEYAN/ButterDB` (D20) and every ported package lives under
`internal/`. There is no one-to-one rename: the destination depends on which
symbol the code uses.

| TDrive import | butterDB import |
| --- | --- |
| `TDrive/backend/datadir` | `github.com/PRIEYAN/ButterDB/internal/platform/datadir` |
| `TDrive/backend/processlock` | `github.com/PRIEYAN/ButterDB/internal/platform/processlock` |
| `TDrive/backend/auth` (private file writes) | `github.com/PRIEYAN/ButterDB/internal/platform/secretfile` |
| `TDrive/backend/auth` (credentials, login steps) | `github.com/PRIEYAN/ButterDB/internal/identity` |
| `TDrive/backend/auth` (gotd connect, session storage) | `github.com/PRIEYAN/ButterDB/internal/tgclient/conn` |
| `TDrive/backend/crypto` (constants, header, sizes, offsets) | `github.com/PRIEYAN/ButterDB/internal/crypto/format` |
| `TDrive/backend/crypto` (`EncryptStream`, `DecryptStream`) | `github.com/PRIEYAN/ButterDB/internal/crypto/stream` |
| `TDrive/backend/crypto` (`RandomAccessDecryptor`) | `github.com/PRIEYAN/ButterDB/internal/crypto/randomaccess` |
| `TDrive/backend/crypto` (key wrap, key check) | `github.com/PRIEYAN/ButterDB/internal/crypto/keyring` |
| `TDrive/backend/projection` (`Parse`, `Format`, `Op`, `OpType`) | `github.com/PRIEYAN/ButterDB/internal/wire` |
| `TDrive/backend/projection` (`ProjectFromOp`, `HashHeader`) | `github.com/PRIEYAN/ButterDB/internal/projection/log` |
| `TDrive/backend/projection` (`RebuildProjection`) | `github.com/PRIEYAN/ButterDB/internal/projection/rebuild` |
| `TDrive/backend/projection` (channel rows, managed message lookups) | `github.com/PRIEYAN/ButterDB/internal/storage/{channelrepo,objectrepo}` |
| `TDrive/backend/tgclient` (value types and sentinels) | `github.com/PRIEYAN/ButterDB/internal/tgclient` |
| `TDrive/backend/tgclient` (behaviour) | `github.com/PRIEYAN/ButterDB/internal/tgclient/{conn,flow,send,rangeio,rpc,authrpc}` |
| `TDrive/backend/tgclient` (`Fake`) | `github.com/PRIEYAN/ButterDB/internal/tgclient/faketg` |
| `TDrive/backend/sync` | `github.com/PRIEYAN/ButterDB/internal/syncer/{scan,difference,reconcile}` |
| `TDrive/backend/livesync` | `github.com/PRIEYAN/ButterDB/internal/syncer/livesync` |
| `TDrive/backend/media` | `github.com/PRIEYAN/ButterDB/internal/readpath/{rangereader,httprange}` |
| `TDrive/backend/mountcontent`, `TDrive/backend/mountcache` | `github.com/PRIEYAN/ButterDB/internal/readpath/objectreader` |

Application packages never import a `tgclient` subpackage that holds behaviour.
They name only the root package's shared value types (`docs/claude.md` §5.4).

```go
import (
	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)
```

### 5.3 Ambient state to remove

These TDrive globals and ambient calls appear in files butterDB ports. Each one
becomes an injected dependency (`docs/claude.md` §6.4).

| TDrive site | Ambient state | butterDB replacement |
| --- | --- | --- |
| `backend/storage_sqlite.go` | `backend.DB` package global, used by `core`, `media`, `sync` callers | `*sqlitedb.DB` handles built in `internal/core` and passed to repositories |
| `backend/datadir/datadir.go` | `dataOverride`, `cacheOverride` under a package mutex; `os.UserConfigDir`, `os.UserCacheDir` | A `datadir` value built from the configured data directory |
| `backend/processlock/lock.go` | `Path()` reads `datadir.Dir()` | Lock path passed in from the `datadir` value |
| `backend/auth/private_file.go` | `privateFileRename = os.Rename` package variable for tests | Rename function injected through `internal/platform/fsx` |
| `backend/tgclient/getfile_limiter.go` | `getFileSlots`, `backgroundGetFileSlots`, `uploadPartSlots` package semaphores | One limiter value in `tgclient/flow`, built once by `core` |
| `backend/tgclient/idempotency.go` | `fallbackRandomCounter` package counter and `crypto/rand` | Randomness from an injected `io.Reader` |
| `backend/media/server.go` | `mediaStreamBufferPool` package `sync.Pool` | A pool owned by the `httprange` handler value |
| `backend/applog/applog.go` | `slog.SetDefault`, `initOnce` | Injected `*slog.Logger`; nothing calls `slog.Default()` |
| `backend/projection/log.go` | `slog.Warn` global, `time.Now().Unix()` for `seen_at` | Injected logger; `seen_at` and `detected_at` come from the incoming message, never the local clock |
| `backend/crypto/stream.go`, `vault.go` | `crypto/rand.Read` inside `EncryptStream`, `NewMasterKey`, `WrapMasterKey` | Injected `io.Reader`; salt and nonce prefix passed in (E8) |
| Every ported file | `time.Now`, `time.Since`, `time.After`, `time.NewTicker` | `internal/platform/clock` |

### 5.4 Comment stripping

TDrive explains itself in comments. The packages butterDB ports carry these
comment loads, all of which go: `backend/projection` 736 lines,
`backend/services/file` 773, `backend/tgclient` 507, `backend/media` 426,
`backend/sync` 213, `backend/mountwrite` 114, `backend/crypto` 107,
`backend/auth` 101. Before deleting a comment, check whether it records an
external constraint (a Telegram limit, a gotd quirk, a SQLite behaviour). If it
does, keep one line in the code or move the explanation to the phase document.
Examples worth moving, not losing:

| TDrive comment | Knowledge to keep |
| --- | --- |
| `backend/storage_sqlite.go` above `TuneSQLite` | Every pragma except `journal_mode` is per connection, so pragmas must be set on each connection before the pool grows (E7) |
| `backend/tgclient/client.go` above `ErrSendOutcomeUnknown` | Telegram may have accepted an idempotent send whose receipt was lost |
| `backend/tgclient/getfile_limiter.go` above `MaxConcurrentGetFile` | Process-wide getFile cap prevents multiplicative fan-out into FLOOD_WAIT |
| `backend/media/range_reader.go` above `openingChunkBytes` | First read serves a 256 KiB prefix; `upload.getFile` requires 4 KiB alignment |
| `backend/crypto/stream.go` above the constants | The TDE1 byte layout |
| `backend/services/file/hidden_upload.go` in `recoverHiddenParts` | Only the in-flight body can have an accepted-but-unrecorded receipt |

### 5.5 Constants and renamed strings

A retained constant keeps its value and moves to `internal/domain/limits` or the
owning package. Where TDrive's string carries the TDrive name, butterDB uses
the specification's string from `docs/butterDBCore.md` §28. butterDB does not
read data written by TDrive, so no compatibility is lost.

| TDrive constant | TDrive value | butterDB value | butterDB home |
| --- | --- | --- | --- |
| `streamMagic` | `"TDE1"` | `"TDE1"` (unchanged) | `internal/crypto/format` |
| `wireVersionPrefix` | `"TDX1"` | `"TDX1"` (unchanged) | `internal/wire` |
| `hkdfInfo` | `"tdrive/file/v1"` | `"butterDB/file/v1"` | `internal/crypto/stream` |
| `keyCheckPlaintext` | `"tdrive-key-check-v1"` | `"butterDB-key-check-v1"` | `internal/crypto/keyring` |
| `keyCheckEnvelopeLen` | 24 + 19 + 16 = 59 | 24 + 21 + 16 = 61, computed with `len()` | `internal/crypto/keyring` |
| `ErrBadMagic` text | `"crypto: not a TDrive encrypted stream"` | `"crypto: not a butterDB encrypted stream"` | `internal/crypto/format` |
| `processlock.ErrAlreadyRunning` text | `"tdrive backend already running"` | `"butterdbd already running"` | `internal/platform/processlock` |
| Lock file | `<config dir>/TDrive/backend.lock` and `.guard` | `<data dir>/butterdbd.lock` and `butterdbd.lock.guard`. Default (owner may change) | `internal/platform/processlock` |
| `appDir` | `"TDrive"` | none; the data directory is configured, not derived | `internal/platform/config` |
| `cacheTempPrefixes` | `tdrive-upload-`, `.tdrive-upload-part-`, `tdrive-enc-`, `tdrive-rendition-source-` | `butterdb-spool-`, `butterdb-part-`. Default (owner may change) | `internal/platform/datadir` |
| `MaxPartBytes` | `1900 * 1024 * 1024` | same value, named `MaxStoredObjectBytes`. Default (owner may change) | `internal/domain/limits` |
| `maxWireUnixTimestamp` | `253402300799` | unchanged | `internal/wire` |

Every other retained constant is listed with its file in section 6 and keeps
its name and value.

### 5.6 Behaviour that butterDB changes on purpose

| TDrive behaviour | Source | butterDB behaviour | Decision |
| --- | --- | --- | --- |
| Unknown `t=` value is `ErrWireBadOpType`, a parse failure; the message is skipped as unparseable | `backend/projection/wire.go` `Parse` | Unknown op type is a skippable `unknown_op` reject; raw header stays in `replay_log` so a newer binary's rebuild applies it | E5 |
| A replayed op above the watermark marks the whole channel `needs_projection_rebuild` and rebuilds all of it | `backend/sync/sync.go` `incrementalLockedWithReplayStatus` | Repair triggers only when a newly ingested op has a lower message ID than an already-recorded op for the same key, and repairs that key | E6 |
| One SQLite connection per database | `backend/storage_sqlite.go` `TuneSQLite` | One writer connection plus a read-only WAL pool, pragmas per connection | E7 |
| `EncryptStream` draws file salt and nonce prefix from `crypto/rand` | `backend/crypto/stream.go` lines 82 and 87 | Salt and nonce prefix are generated once, persisted in the job row, and passed in, so a retried send produces identical ciphertext | E8 |
| Control sends derive `random_id` from a fresh UUID per call | `backend/core/engine.go` line 541 | `random_id` derives from the server-minted job ID scoped per access key | E9 |
| No send-rate limiter; flood waits are only honoured | `backend/tgclient/write_coordinator.go` | A conservative send-rate limiter is on by default in `tgclient/flow`; FLOOD_WAIT stays a floor | E10 |
| Large files split into `t=part` documents plus a `t=manifest` | `backend/services/file/upload.go` `uploadMultipart` | One document per object, at most 1900 MiB stored; S3 multipart parts are assembled on disk first | D9 |
| Deleted files go to trash, revisions are retained | `backend/projection/trash.go`, `retention.go` | Immediate hard delete: `t=del` tombstone first, then `deleteMessages` in batches of 100 | D10, E4 |
| Passphrase vault with Argon2id-derived KEK | `backend/crypto/vault.go`, `backend/services/encryption/service.go` | Server key file is the KEK; per-bucket DEK wrapped under it | D11, A6 |
| Projector stamps `seen_at` with `time.Now()` | `backend/projection/log.go` | Projector has no clock; timestamps come from the op or the message date | `docs/claude.md` §6.4 |

### 5.7 Tests

- Port a test with the code it covers, in the same lane, beside the new source.
- Rename `TDrive` in test data, captions and expected strings by the rules in
  5.5. Regenerate any vector that depends on `hkdfInfo` or the key-check
  plaintext; never edit an expected value to match without understanding why
  it changed (TDrive `TESTING.md`, "Investigate changed expected output").
- Replace `tgclient.NewFake` with `faketg` and `backend.DB` setups with a
  `sqlitedb` handle opened on `t.TempDir()`.
- Keep TDrive's test discipline from `TESTING.md`: assert consequences (bytes,
  durable rows, released resources), inject failures at the real boundary
  (before acceptance, after acceptance with a lost receipt, unknown outcome),
  use `t.Context()` and `t.Cleanup()`, never sleep to wait.
- Keep fuzz targets and their seeds: `FuzzRandomAccessDecryptorChunkBoundaries`
  in `backend/crypto/random_access_test.go`.

### 5.8 Attribution

`docs/legal/THIRD_PARTY_NOTICES.md` is created in the phase 01 prelude with
TDrive's MIT notice copied verbatim from `LICENSE` (it begins `MIT License`,
`Copyright (c) 2026 Jeetraj`). Each phase's integration lane appends one row
per TDrive file the phase ported or used as a design reference. Section 9 gives
the row format and the full expected list.

---

## 6. Map by phase

Columns: TDrive file, its physical line count, verdict, butterDB destination
with the owning lane, and porting notes. "Keep" lists what moves, "Change"
what must differ, "Drop" what stays behind. Lane IDs refer to the phase list in
the design brief (`docs/phases/NN-*.md` once written).

### Phase 01 — Foundation and guardrails

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/datadir/datadir.go` | 143 | PORT | `internal/platform/datadir` (L1) | Keep: owned-temp naming, `CreateCacheTemp`, `CleanupCacheTemps` that removes only owned scratch files and keeps going when one removal fails, directory mode `0o700`. Change: no package overrides or mutex; the root comes from configuration and the value is constructed once; temp prefixes per 5.5. Drop: `Set`, `SetCache`, mobile sandbox handling, `os.UserConfigDir` fallback |
| `backend/processlock/lock.go` | 249 | PORT | `internal/platform/processlock` (L1) | Keep: persistent `.guard` file holding the exclusive lock (never unlinked, so contenders lock the same inode), JSON owner metadata `role`, `pid`, `started_at`, `lock_version` published through a temp file, stale-owner reclaim, `ErrAlreadyRunning`, `Read`, `ReadActive`, `ProcessRunning`. Change: lock path injected; error text per 5.5 |
| `backend/processlock/lock_unix.go` | 18 | PORT | `internal/platform/processlock` (L1) | `unix.Flock(LOCK_EX\|LOCK_NB)` from `golang.org/x/sys/unix` |
| `backend/processlock/process_unix.go` | 13 | PORT | `internal/platform/processlock` (L1) | Process liveness check |
| `backend/processlock/lock_windows.go`, `process_windows.go` | 18, 30 | CUT | none | `butterdbd` targets Linux and macOS. Default (owner may change) |
| `backend/processlock/legacy_android.go`, `legacy_other.go` | 12, 7 | CUT | none | Legacy record migration from older TDrive builds |
| `backend/auth/private_file.go` | 95 | PORT | `internal/platform/secretfile` (L1) | Keep: create temp in the target directory, `chmod 0o600` before writing, write, check short write, `fsync`, close, rename, `fsync` the directory (skipped on Windows); remove the temp on every failure path; directory mode `0o700`. Change: rename function injected, no package variable |
| `backend/applog/applog.go` | 105 | PATTERN-ONLY | `internal/platform/logging` (L2) | TDrive sets a global debug text handler writing to a file. butterDB builds a JSON `slog.Handler` on stdout, returns a `*slog.Logger`, never calls `slog.SetDefault` |
| `backend/services/file/limits.go` | 106 | PORT-PARTIAL | `internal/domain/limits` (L3) | Keep: `MaxPartBytes` value `1900 * 1024 * 1024` under the name in 5.5, the "empty files" and "too large" conditions as sentinels in `internal/domain/errs`. Drop: `MinResumableUploadBytes`, `LargeFileMaxBytes`, `MaxParts`, `planUpload`, `humanByteSize`. Also into `limits`: delete batch 100 (`backend/services/file/upload.go` `deleteMessagesChunked`) |
| `internal/app/operation.go` | 203 | PATTERN-ONLY | `internal/domain/errs` (L3) | Stable snake_case error codes carried on a typed error (`not_found`, `permission_denied`, `already_exists`, `canceled`, `deadline_exceeded`). butterDB defines its own code set |
| `backend/daemon/server.go` | 193 | PATTERN-ONLY | `internal/lifecycle` (L3), `internal/core` (integration) | `Run` acquires the process lock, starts logging, builds the engine, opens the listener, and releases in reverse through `defer`. butterDB makes this explicit with `lifecycle.Group` and the close order in `docs/claude.md` §6.5 |
| `cmd/tdrived/main.go` | 32 | PATTERN-ONLY | `cmd/butterdbd` (integration) | `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` around the run function; exit code 1 on error |
| `cmd/tdrive/output.go` | 109 | PATTERN-ONLY | `cmd/butterdbd` (integration) | `classifyCommandError` / `writeCommandError`: one place maps errors to exit codes and to JSON or text output. `terminalSafeText` strips control characters from strings that came from Telegram before printing |

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/datadir/datadir_test.go` | 207 | Partial | `TestCreateCacheTempUsesPrivateCacheOverride`, `TestCleanupCacheTempsRemovesOnlyOwnedScratchFiles`, `TestCleanupCacheTempsKeepsGoingWhenOneCannotBeRemoved`. Drop the four default and override tests |
| `backend/processlock/lock_test.go` | 286 | Partial | All except `TestAcquireReclaimsLegacyRecordNamingOwnPID` |
| `backend/processlock/owner_process_test.go` | 155 | Full | Helper-process ownership tests |
| `backend/auth/private_file_test.go` | 97 | Full | `TestWritePrivateFileReplacesContents`, `TestWritePrivateFileUsesRestrictiveModes`, `TestWritePrivateFileRenameFailurePreservesOldFileAndCleansTemp` |

### Phase 02 — Formats gate

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/projection/wire.go` | 708 | PORT-PARTIAL | `internal/wire/{grammar.go,parse.go,format.go,errors.go}` (L1) | Keep: `TDX1` prefix check, `ExtractHeaderLine` (first line only), `\|`-separated `key=value` pairs with `=` required after a non-empty key, `url.QueryEscape` / `url.QueryUnescape` for text values, `base64.RawURLEncoding` for binary values (`setB64`), positive integer parsing (`setPositiveInt64`), timestamp ceiling `maxWireUnixTimestamp`, sentinels `ErrWireMissingHeader`, `ErrWireBadVersion`, `ErrWireMalformed` (renamed by the phase 02 prelude). Change: unknown `t=` returns a parsed op marked unknown, not `ErrWireBadOpType` (E5); unknown keys inside a known op are ignored. Drop: every folder and namespace op (`f`, `meta`, `mkdir`, `rename`, `move`, `rmdir`, `tomb`), multipart (`part`, `manifest`), writable ops (`fcommit`, `freplace`, `dcommit`, `relocate`, `trash`, `harddel`, `untrash`), `d:` / `f:` object prefixes, KDF fields of `encfg` (`KDFSalt`, `KDFParamsJSON`, `Hint`) |
| `backend/projection/types.go` | 237 | PORT-PARTIAL | `internal/wire` (prelude contract) | Keep: `type OpType string` and one typed `Op` struct with optional fields per op. Drop: `Rendition`, `Channel`, `Folder`, `Dirent`, `File`, kind constants, id-prefix helpers. The op catalogue (`body`, `put`, `del`, `bcreate`, `bcfg`, `bdel`, `encfg`, registry marker) is new and owned by L2 |
| `backend/projection/wire.go` `setWritableEnvelope`, `setContentReference` | (in 708) | PATTERN-ONLY | `internal/wire/ops_*.go` (L2) | TDrive's writable ops carry a version and a durable operation ID and reference the hidden body by message ID and content hash. butterDB's `t=body` carries `j=<job id>` and `t=put` references the body message ID, sizes, MD5 and SHA-256 the same way |
| `backend/crypto/stream.go` (constants, `ValidatePlaintextSize`, `CiphertextSize`, `buildNonce`, header layout) | 318 | PORT-PARTIAL | `internal/crypto/format` (L3) | Keep verbatim: magic `TDE1`, header 50 bytes (`4+1+1+16+20+8`), `chunkSizeLog2 = 16`, chunk 65,536 bytes, salt 16, nonce prefix 20, tag 16, `finalChunkBit = 1 << 31`, `maxPlaintextSize = 2^47 − 1`, plaintext size little-endian. Change: `streamTagLen` becomes the literal `16` because `format` imports the standard library only (rule L1) and cannot import `chacha20poly1305`; sentinels `ErrShortHeader`, `ErrBadMagic`, `ErrUnsupported`, `ErrNegativePlaintextSize`, `ErrPlaintextTooLarge` move here |
| `backend/crypto/random_access.go` `StoredOffset`, `ciphertextChunkOffset` | (in 413) | PORT-PARTIAL | `internal/crypto/format` (L3) | Plaintext offset to stored offset arithmetic, overflow-checked |
| `backend/projection/schema.go` | 898 | PORT-PARTIAL | `internal/storage/schema` `projection/0001_*.sql` (L4) | Keep tables with these columns: `replay_log (channel_id, msg_id, op_type, op_payload_json, raw_header, first_seen_hash, actor_user_id, seen_at, PRIMARY KEY (channel_id, msg_id))`; `replay_log_tamper (channel_id, msg_id, old_hash, new_hash, detected_at)`; `replay_log_rejects (channel_id, msg_id, error, detected_at)`, which butterDB extends with a stable reason code so `unknown_op` is distinguishable (E5); `channels` reduced to `channel_id, access_hash, title, last_synced_msg, initial_sync_done, needs_projection_rebuild, pts`; `projection_operations (channel_id, op_id, msg_id, op_type, outcome CHECK IN ('applied','rejected'), error)`. Change: imperative migrations with `PRAGMA table_info` probes become numbered embedded SQL files. Drop: `backfill_progress`, `pending_joins`, `encryption`, `file_parts`, `pending_part_cleanup`, `dirents`, `file_revisions`, `trash_entries`, `hard_delete_jobs`, `hard_delete_intents`, `hard_delete_plan_items`, `folders`, `files`. Object and bucket tables are new and defined in `docs/spec/storage-model.md` |
| `backend/projection/gallery_schema.go` | 256 | CUT | none | Gallery tables |
| `backend/sync/initial_scan_spool.go` (DDL only) | (in 136) | PORT-PARTIAL | `internal/storage/schema` (L4) | `initial_scan_spool (channel_id, msg_id, from_id, date, text, has_media, media_size, document_name, PRIMARY KEY (channel_id, msg_id))` |
| `backend/storage_sqlite.go` | 117 | PATTERN-ONLY | `internal/storage/sqlitedb` (L4) | Keep the pragma values: `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`, `temp_store=MEMORY`, `cache_size=-32768`, `mmap_size=134217728`. Change: one writer connection plus a read-only WAL pool, pragmas applied per connection through the DSN (E7); no global `DB` |
| `backend/fstructs.go` | 57 | CUT | none | Wails DTOs |

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/projection/wire_test.go` | 255 | Partial, rewritten for new ops | `TestParseEmptyHeader`, `TestParseUnknownVersion`, `TestExtractHeaderLineStripsBody`, `TestRoundTripFormatParse`, `TestParseRejectsUploadTimeOutsideSQLiteRange` (applies to the put timestamp), `TestParseRejectsNegativeFileSizes` (stored and plaintext sizes). Invert `TestParseUnknownOpType`: it must now yield an unknown op, not an error |
| `backend/projection/writable_wire_test.go` | 57 | Pattern | `TestWritableWireRequiresVersionAndOperationID` becomes "body and put require `j=`" |
| `backend/crypto/ciphertext_size_test.go` | 169 | Partial | `TestCiphertextSizeSaturatesOnOverflow`, `TestValidatePlaintextSizeEnforcesTDE1CounterCapacity` here; the cases that call `EncryptStream` move to phase 03 |
| `backend/crypto/stored_offset_test.go` | 21 | Full | `TestStoredOffsetSkipsHeaderAndEarlierChunkTags` |
| `backend/projection/migration_test.go` | 543 | CUT | Tests TDrive's legacy schema upgrades |

### Phase 03 — Storage engine

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/projection/log.go` | 197 | PORT | `internal/projection/log` (L1) | Keep: `HashHeader` (SHA-256 hex of the raw header), `ProjectFromOp` (owns its transaction) and `ProjectFromOpTx` (joins the caller's), `alreadySeen` on a repeated `(channel_id, msg_id)`, tamper row when the hash differs while the first op stays canonical, skippable apply errors recorded as rejects so replay continues, other errors fail the transaction, `ChannelIsEmpty`, `ErrChannelNotEmpty`. Change: `*sql.Tx` becomes the prelude's `Tx` port; context on every call; `seen_at` and `detected_at` from the incoming message; `unknown_op` recorded as a reject with the raw header kept (E5); injected logger |
| `backend/projection/rebuild.go` | 170 | PORT | `internal/projection/rebuild` (L3) | Keep: clear derived state for one channel, replay `replay_log` ascending by `msg_id` in keyset batches of 256 (`rebuildReplayBatchSize`), one transaction rolled back as a whole on failure, channels isolated. Add: per-key repair that replays only the rows for one key (E6) |
| `backend/projection/encryption_rebuild.go` | 113 | PORT-PARTIAL | `internal/projection/rebuild` (L3) | Rebuild of the `encfg` state from replay, rejecting tampered and malformed canonical headers. Drop password-config selection |
| `backend/projection/apply.go` | 435 | PATTERN-ONLY | `internal/projection/apply` (L2) | TDrive's `ApplyOp` switch becomes a `Registry` of `Applier` values, one per op type (open/closed, `docs/claude.md` §7). Its skippable error classes (`ErrCycleRejected`, `ErrBadOp`, `ErrNameConflict`, `ErrRevisionConflict`) are replaced by object and bucket reject reasons |
| `backend/projection/writable_apply.go` | 754 | PATTERN-ONLY | `internal/projection/apply` (L2) | `fcommit` makes a hidden body visible and records an outcome per operation ID in `projection_operations`; repeated publication of the same operation under a different message ID resolves to one outcome. The `put` applier follows this |
| `backend/projection/encryption.go` | 118 | PATTERN-ONLY | `internal/storage/bucketrepo` (L6) | Exact-length validation of persisted key material before use; reject corrupt rows on read |
| `backend/projection/managed_messages.go` | 73 | PORT | `internal/storage/objectrepo` (L6) | `ManagedMsgIDsIn`: which candidate message IDs the projection owns, in batches of 200 (`managedMessageBatchSize`), cancellable. Used by the sweeper to find orphan bodies |
| `backend/projection/reconcile.go` | 61 | PORT-PARTIAL | `internal/storage/objectrepo` (L6) | `LiveFileMessageIDs` becomes "live body and put message IDs per bucket channel", excluding tombstoned objects |
| `backend/projection/read.go` | 908 | PATTERN-ONLY | `internal/storage/objectrepo` (L6) | Read helpers such as `FileEncryptionMeta`. Listing is new: keyset pagination with prefix and delimiter for ListObjectsV2 |
| `backend/crypto/stream.go` (`EncryptStream`, `DecryptStream`, `deriveSubkey`, `ensureNoExtraPlaintext`, `writeExact`) | 318 | PORT | `internal/crypto/stream` (L4) | Keep: HKDF-SHA256 32-byte per-file subkey from the 32-byte key and the file salt (`deriveSubkey`), XChaCha20-Poly1305 per 64 KiB chunk with the 24-byte nonce `prefix(20) ‖ counter_be32` and the final-chunk bit, `maxConsecutiveEmptyReads = 100`, rejection of trailing plaintext and length mismatch. Change: `hkdfInfo = "butterDB/file/v1"`; salt and nonce prefix are parameters (E8); randomness injected. Add: `EncryptingReaderAt`, new code with no TDrive source, which yields ciphertext at any offset from the DEK, salt, prefix and a plaintext `ReaderAt`, so a resumed send reproduces the same bytes |
| `backend/crypto/random_access.go` | 413 | PORT | `internal/crypto/randomaccess` (L4) | Keep: `ContextReaderAt`, `NewRandomAccessDecryptor` (authenticates the final chunk on open), `Clone` (independent, no storage read), `ReadAt` (authenticates every chunk read), `Size`, `Close` (cancels blocked reads, then clears the key), `ErrCiphertextSize`, `ErrDecryptorClosed`, `ErrNilContext`. Offset arithmetic moved to `format` in phase 02 |
| `backend/crypto/vault.go` | 308 | PORT-PARTIAL | `internal/crypto/keyring` (L5) | Keep: `WrapMasterKey` / `UnwrapMasterKey` (XChaCha20-Poly1305, output `nonce(24) ‖ ciphertext`, 72 bytes for a 32-byte key), `EncodeKeyCheck` / `VerifyKeyCheck`, `NewMasterKey` (32 random bytes) as the per-bucket DEK generator, exact envelope length checks. Change: the KEK is the 32-byte server key file (A6), key-check plaintext per 5.5, randomness injected. Drop: `KDFArgon2id`, `Params`, `DefaultParams`, `DeriveKEK`, `NewSalt`, `MarshalParams`, `UnmarshalParams`, the KDF half of `ValidateVaultMaterial` |
| `backend/services/encryption/key_lease.go` | 92 | PATTERN-ONLY | `internal/crypto/keyring` (L5) | A lease returns a private copy of key bytes and zeroes it on `Close`; the store zeroes a replaced key. butterDB leases unwrapped DEKs the same way |
| `backend/projection/errors.go` | 8 | PORT | `internal/ingest/put` (phase 06 L2) | `ErrControlProjection` ("control message accepted but local projection failed") |

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/projection/log_test.go` | 199 | Full | `TestProjectFromOpInsertsLogAndProjectsAtomically`, `TestProjectFromOpIsIdempotentSameHash`, `TestProjectFromOpDetectsTamper`, `TestProjectFromOpRecordsAndSkipsRejectedApplyError`, `TestProjectFromOpTxAtomicWithCallerCheckpoint`, `TestChannelIsEmptyDetectsAnyRow` |
| `backend/projection/rebuild_test.go` | 221 | Partial | `TestRebuildEmptyChannelClears`, `TestRebuildReplaysAscending`, `TestRebuildReplaysAscendingAcrossBatches`, `TestRebuildRollsBackAllBatchesOnReplayFailure`, `TestRebuildDifferentChannelsAreIsolated`, `TestRebuildReplaysEncryptionConfig`. Drop `TestRebuildClearsAndReplaysFileParts`; rewrite `TestRebuildHandlesTombstones` for `t=del` |
| `backend/projection/encryption_rebuild_test.go` | 211 | Partial | The `RejectsTamper`, `RejectsInvalidCanonicalHeader`, `RejectsHashValidMalformedHeader` and `RejectsInvalidBoundariesAndStorage` cases |
| `backend/projection/apply_test.go` | 445 | Pattern | `TestApplyFileUploadIdempotentOnSameMsgID`, `TestApplyRejectsUnknownType` and `TestApplyMkdirSameObjIDIsolatedAcrossChannels` become put idempotency, unknown-op reject and cross-bucket isolation cases |
| `backend/projection/managed_messages_test.go` | 98 | Full | All three |
| `backend/projection/reconcile_test.go` | 109 | Partial | Empty, single and tombstoned cases; drop multipart |
| `backend/crypto/stream_test.go` | 230 | Full | All nine, vectors regenerated for the new `hkdfInfo` |
| `backend/crypto/ciphertext_size_test.go` | 169 | Partial | `TestCiphertextSizeMatchesEncryptStream`, `TestEncryptStreamRejectsSourceLengthMismatch`, `TestEncryptStreamRejectsTrailingDataAfterEmptyRead`, `TestEncryptStreamRejectsUnendingEmptyTrailingSource` |
| `backend/crypto/random_access_test.go` | 699 | Full | All, including `FuzzRandomAccessDecryptorChunkBoundaries` and its seeds |
| `backend/crypto/vault_test.go` | 215 | Partial | `TestKeyWrapRoundTrip`, `TestVaultBlobsRequireExactAuthenticatedEnvelopeSizes`. Drop the Argon2id and params cases |
| `backend/services/encryption/key_lease_test.go` | 235 | Pattern | `TestMasterKeyLeaseOwnsIndependentKeyAndReturnsCopies`, `TestMasterKeyLeaseCloseIsIdempotentAndZeroesKey`, `TestStoreMasterKeyZeroesReplacedOwnedBuffer` |

### Phase 04 — Telegram transport and identity

Only `internal/tgclient/...` imports gotd (rule L2). TDrive's `backend/auth`,
`backend/livesync` and `backend/media/raw_drive.go` also import gotd; their
gotd-touching code moves into `tgclient`.

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/tgclient/client.go` | 367 | PORT-PARTIAL | `internal/tgclient` (prelude) | Keep as shared value types: `ErrFloodWait`, `FloodWaitError` with `Is`, `NewFloodWaitError`, `FloodWaitDuration`, `ErrSendOutcomeUnknown`, `ErrMessageNotFound`, `ErrNotFile`, `ErrEmptyDocument`, `ErrChannelUnavailable`, `InputPeer`, `PeerKind`, `HistoryMessage`, `ChannelDifference`, `SendFileResult`, `OwnedBroadcastChannel`. Drop: the 35-method `Client` interface and the `IdempotentSender` extension (consumers declare their own ports), `FileDocument`, `FileThumb`, `InviteInfo`, `JoinRequest`, `UserMessageRef`, `JoinedBroadcastChannel`, `SourcePeer`, forum fields of `HistoryMessage` (`TopicID`) |
| `backend/tgclient/range.go` | 63 | PORT | `internal/tgclient` (prelude) | `DocumentRef`, `RangeReadAlignment = 4 * 1024`, `RangeReadMaxBytes = 1024 * 1024`. Drop the `RangeClient` and `DocumentBatchResolver` interfaces; consumers declare their own |
| `backend/tgclient/session.go` | 161 | PORT | `internal/tgclient/conn` (L1) | `liveConn`: one long-lived run scope started once under concurrency, `acquire` waits for ready or context, restarts after the scope dies, `Close` stops it |
| `backend/tgclient/gotd.go` | 1,299 | PORT-PARTIAL | `conn` (L1), `send` (L3), `rpc` (L4) | `conn`: `NewGotd`, `scope`, `acquire`, `run`, `runClient`, `Close`, `closeMediaConns`, `normalizeError`. `send`: `SendControlWithRandomID`, `SendFileWithRandomID` (always `InputMediaUploadedDocument{MimeType: "application/octet-stream", ForceFile: true}` with a filename attribute), message-ID extraction that classifies an accepted response without a receipt as `ErrSendOutcomeUnknown` (lines 276, 1065). `rpc`: `GetHistory`, `GetChannelMessage`, `DeleteMessages` (batches of 100), `MissingMessages`, `GetChannelDifference`, `GetChannelPts`, `SelfID`, `historyMessageFromTG`, `placeholderMessage`, `restrictedHere`, `channelsGetMessagesLimit = 100`. Drop: `SendControl` and `SendFile` without random ID, thumbnails, `SearchChannelMessages`, `SelfProfile`, `ResolveUsersFromMessages`, `DownloadChannelPhoto`, `DownloadFileThumbnail`, `GetFileDocument`, megagroups, invites, join requests, `LookupChannelTitle`, `LeaveChannel` |
| `backend/tgclient/gotd_pool.go` | 139 | PORT | `internal/tgclient/conn` (L1) | Per-DC file pools, `MediaPoolSize = 4`, `UploadThreads = 8`, `poolRetryAfter = 30 * time.Second`, `FILE_MIGRATE` detection |
| `backend/tgclient/gotd_range.go` | 388 | PORT | `internal/tgclient/rangeio` (L3), file-reference refresh in `conn` (L1) | `ResolveDocument`, `ResolveDocuments` (order-preserving), `ReadDocumentRange` with alignment preconditions (`roundedTelegramLimit`, `crossesRangeBoundary`, `copyRequestedRange`), CDN redirect with AES-CTR decrypt and `verifyFileHashes` (rejects coverage gaps and mismatches), `IsFileReferenceError` |
| `backend/tgclient/gotd_media.go` | 110 | PORT-PARTIAL | `internal/tgclient/rangeio` (L3) | `mediaRefFromTG`, `getMediaRefByMessageID`. Drop inline photo reads and `largestPhotoSize`; butterDB bodies are always documents |
| `backend/tgclient/download.go` | 173 | PORT-PARTIAL | `internal/tgclient/rangeio` (L3) | Sequential `stream` with per-block retry and one file-reference refresh (`refreshRef`). Phase 12 copies bodies to the replica with it. Drop the parallel `WriterAt` download |
| `backend/tgclient/floodwait_retry.go` | 249 | PORT | `internal/tgclient/flow` (L2) | `FloodWaitRetryPolicy` with `Validate` and `Do`, `DefaultWriteFloodWaitRetryPolicy` (2 retries, 30 s max, 60 s total), transient budget (4 retries, 2 s backoff doubling to 30 s, 750 ms jitter), independent budgets for flood and transient failures, `saturatingDurationAdd`, `ErrInvalidFloodWaitRetryPolicy`. Change: sleep and jitter via injected clock and random source |
| `backend/tgclient/write_coordinator.go` | 113 | PORT | `internal/tgclient/flow` (L2) | Shares the longest FLOOD_WAIT across write classes, serializes message sends, rechecks cooldown after queueing. Add: the default-on send-rate limiter (E10) sits here |
| `backend/tgclient/getfile_limiter.go` | 125 | PORT | `internal/tgclient/flow` (L2) | `MaxConcurrentGetFile = 12`, `PlaybackGetFileReserve = 4`, `MaxConcurrentBackgroundGetFile = 8`, `DefaultDownloadThreads = 4`, upload-part slots `UploadThreads`. Change: package semaphores become one limiter value; "playback" reserve becomes the foreground (API GET) reserve |
| `backend/tgclient/transient.go` | 97 | PORT | `internal/tgclient/flow` (L2) | `IsTransientTransport`: typed checks first, then the transport string patterns |
| `backend/tgclient/idempotency.go` | 69 | PORT | `internal/tgclient/send` (L3) | `StableRandomID(operationID, step)`: deterministic, namespaced, positive, rejects ambiguous keys over `maxIdempotencyKeyBytes = 256`. Change: callers pass the server-minted job ID (E9); fallback randomness injected |
| `backend/tgclient/upload_part_retry.go` | 49 | PORT | `internal/tgclient/send` (L3) | Retries one `upload.saveFilePart` through throttling, releasing the slot during cooldown |
| `backend/tgclient/upload_media_retry.go` | 44 | PORT | `internal/tgclient/send` (L3) | Keeps the uploaded handle and `random_id` across retries; a send whose receipt cannot be recovered returns `ErrSendOutcomeUnknown` |
| `backend/tgclient/personal_drive.go` | 229 | PORT-PARTIAL | `internal/tgclient/rpc` (L4) | `CreateBroadcastChannel` (createChannel, broadcast), `ListOwnedBroadcastChannels` (dialog walk in batches of 100, including archived folder 1, deduplicated, no partial result on failure), `GetBroadcastChannel`. Registry rediscovery uses them. Drop joined-channel listing |
| `backend/auth/peer_resolution.go` | 56 | PORT | `internal/tgclient/rpc` (L4) | `ResolveDriveChannel`: recover a channel's access hash by walking dialogs in folders 0 and 1, rejecting inaccessible entities, preserving RPC failure and cancellation |
| `backend/auth/session_storage.go` | 39 | PORT | `internal/tgclient/conn` (L1) | gotd `session.Storage` adapter. Change: it reads and writes through a byte-store port; the bytes live in a secret file owned by `identity` |
| `backend/auth/auth.go` | 230 | PORT-PARTIAL | `internal/tgclient/authrpc` (L4), `internal/identity` (L6), `internal/tgclient/conn` (L1) | `authrpc`: `SendCode`, `SignIn`, mapping `auth.ErrPasswordAuthNeeded` to a password step, `AccountGetPassword` hint, `Password`, auth status. `identity`: api_id and api_hash save and load through `secretfile` (TDrive `imp_config.json`). `conn`: `ConnectWithOptions` builds `telegram.NewClient(apiID, apiHash, options)` with the session store. Drop the blocking `StartLogin` loop; butterDB relays one step per admin API call |
| `backend/services/auth/service.go` | 261 | PORT-PARTIAL | `internal/identity` (L6) | Keep: the login stage machine (`loginStage`, `accepts`, `resetAttempt`, `finishAttempt`), code rejection allowing a new code, password with hint, "needs setup" when no credentials exist. Change: the channel-based UI handshake (`WaitCode`, `WaitPassword`, `sendLatest`, `drain`, `EventSink`) becomes explicit steps (submit phone, submit code, submit password) that return the next step; account binding records the user ID from `users.getUsers(self)` |
| `backend/livesync/activity.go` (`Handle`, `ChannelIDsFromUpdates`, `channelIDFromUpdate`) | (in 127) | PORT-PARTIAL | `internal/tgclient/conn` (L1) | gotd `UpdateHandler` extracting channel IDs from `UpdateShort`, `Updates`, `UpdatesCombined`, `UpdateNewChannelMessage` and friends, delivered without blocking |
| `backend/tgclient/fake.go` | 1,154 | PORT-PARTIAL | `internal/tgclient/faketg` (L5) | See section 7.1 for what to keep and what must change |
| `backend/tgclient/document_thumbnail.go` | 38 | CUT | none | Document thumbnails |
| `backend/tgclient/forum.go`, `gotd_forum.go`, `fake_forum.go` | 40, 179, 97 | CUT | none | Forum topics (channelsource) |
| `backend/tgclient/gotd_sources.go` | 259 | CUT | none | Media source browsing (channelsource) |
| `backend/tgclient/invite.go` | 7 | CUT | none | Shared-drive invites |
| `backend/tgclient/media_name.go` | 48 | CUT | none | Display names for external media |
| `backend/auth/group.go` | 288 | CUT | none | Megagroups, invites, join requests |
| `backend/auth/logout.go` | 90 | CUT | none | Clears TDrive desktop caches; butterDB has no logout flow in phase 04 |
| `backend/auth/config.go` | 66 | PATTERN-ONLY | `internal/buckets` (phase 05 L1) | See phase 05 |
| `backend/core/peer_resolution.go` | 37 | PATTERN-ONLY | `internal/buckets` (phase 05 L1) | See phase 05 |
| `cmd/tdrive/auth_drive_commands.go` | 762 | PATTERN-ONLY | `cmd/butterdbd` `setup` (integration) | `runSetup` / `runLogin` / `parseSetupArgs`: api_id and api_hash, then phone, code and 2FA password at the terminal; `promptSecret` with `golang.org/x/term` `ReadPassword`; `terminalSafeTitle`. Drop drive commands |
| `backend/daemon/auth_drive.go` | 519 | PATTERN-ONLY | `internal/adminapi/setup` (phase 09 L1) | See phase 09 |

New in phase 04 with no TDrive source: `sendMessage` to Saved Messages (pairing
codes, A1), `users.getUsers(self)` account binding, `tgprobe`.

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/tgclient/session_test.go` | 180 | Full | All eight `TestLiveConn*` |
| `backend/tgclient/floodwait_retry_test.go` | 583 | Full | All |
| `backend/tgclient/write_coordinator_test.go` | 230 | Full | All three |
| `backend/tgclient/getfile_limiter_test.go` | 123 | Full | Both, with "playback" read as foreground |
| `backend/tgclient/transient_test.go` | 64 | Full | `TestIsTransientTransport` |
| `backend/tgclient/idempotency_test.go` | 150 | Full | All seven, rewritten against `faketg` |
| `backend/tgclient/upload_part_retry_test.go` | 265 | Full | All five |
| `backend/tgclient/upload_media_retry_test.go` | 138 | Full | All four, including `TestSendUploadedMediaRecoversLostReceiptWithoutDuplicate` |
| `backend/tgclient/gotd_updates_test.go` | 60 | Full | `TestExtractMsgIDHandlesTelegramSendVariants`, `TestRequiredSendMsgIDClassifiesAcceptedResponseWithoutReceiptAsUnknown` |
| `backend/tgclient/range_test.go` | 164 | Full | All, including the `TestVerifyFileHashes*` CDN cases |
| `backend/tgclient/download_test.go` | 211 | Partial | `TestDocumentDownloadRetriesOnlyTheFailedBlock`, `TestDocumentDownloadRefreshesAnExpiredReferenceOnce`, `TestDocumentDownloadFailsFastOnADefiniteRejection`, `TestDocumentDownloadGivesUpAfterTheBlockBudget`, `TestDocumentDownloadStreamsInOrder` |
| `backend/tgclient/gotd_pool_test.go` | 39 | Full | Both |
| `backend/tgclient/gotd_documents_test.go` | 178 | Partial | `TestHistoryMessagePreservesOutgoingChannelPost`, `TestDocumentRefsInOrderFollowsRequestedIDs`, `TestFakeResolvesDocumentBatchInOrder` |
| `backend/tgclient/history_placeholder_test.go` | 33 | Full | `TestPlaceholderMessageReportsIDsThatOccupyHistorySlots` |
| `backend/tgclient/personal_drive_test.go` | 210 | Partial | `TestCollectOwnedBroadcastChannelsPaginatesFiltersAndDeduplicates`, `TestCollectOwnedBroadcastChannelsReturnsNoPartialResultsOnFailure` |
| `backend/tgclient/fake_test.go`, `fake_difference_test.go` | 174, 163 | Full | Rewritten against `faketg`, plus per-channel isolation cases (7.1) |
| `backend/auth/peer_resolution_test.go` | 142 | Full | All four |
| `backend/auth/session_storage_test.go` | 88 | Partial | `TestPrivateSessionStorageMissingSession`, `TestPrivateSessionStorageReplacesSession` |
| `backend/services/auth/service_test.go` | 179 | Partial | `TestCodeRejectedEmitsInvalidEvent` and `TestCodeRetryDeliversNewCodeAfterRejection` as step tests; `TestSystemStatusNeedsSetupWhenNoCreds`, `TestSystemStatusReadyWhenCredsExist` |
| `backend/livesync/activity_test.go` | 52 | Full | All three |

### Phase 05 — Buckets and sync

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/sync/sync.go` | 874 | PORT | `internal/syncer/scan` (L2), `internal/syncer/reconcile` (L5) | `scan`: per-channel admission gate (`lockFor`, a weighted semaphore; queued callers cancel without waiting for another scan's network calls), `planHistory` (walk backwards for page bounds, only an empty page proves exhaustion, fail on no progress), `applyHistoryPlan` (oldest first), `applyInitialHistoryPlan` / `spoolHistoryPlan` / `commitInitialHistoryPlan` (fetch into the spool with no transaction open, then apply atomically), `EnsureAuthoritative`, `InitialSyncEmptyChannel`, `Incremental`, `retryFloodWait`, `getHistory` with four transport retries; constants `defaultPageSize = 100`, `maxFloodWaitRetries = 5`, `maxFloodWaitSleep = 60 * time.Second`. `reconcile`: `ReconcileDeletions` and `tombstoneMissing`, routed through the projector. Change: repair rule per E6; progress events become metrics. Drop: `adoptRecentCaptionlessMedia`, `PrepareHardDeleteProjection`, captionless media adoption options |
| `backend/sync/difference.go` | 169 | PORT | `internal/syncer/difference` (L3) | Bootstrap `pts` before a history scan, page through `getChannelDifference`, commit new ops, watermark and `pts` in one transaction per page, fall back to history on `TooLong`, apply deletion IDs |
| `backend/sync/parse.go` | 105 | PORT-PARTIAL | `internal/syncer/scan` (L2) | `ParseHistoryPage`, `SortAscending`, `ParsedMessage`. Change: parse with `internal/wire`; keep unknown ops (E5). Drop `ParseOptions` adoption |
| `backend/sync/initial_scan_spool.go` | 136 | PORT | `internal/syncer/scan` (L2) | Spool writes and reads in batches of 256 (`initialScanSpoolBatch`), stale rows deleted at the start of a full scan. DDL moved to phase 02 |
| `backend/livesync/coordinator.go` | 268 | PORT | `internal/syncer/livesync` (L4) | Coalesce signal bursts per channel (debounce 750 ms), ignore unknown channels, backstop sync of known channels, per-sync timeout, failure reporting. Change: `Start` / `Close` per `docs/claude.md` §6.5; time from the clock |
| `backend/livesync/activity.go` (`TelegramActivity`, `Signals`, `Signal`, `Dropped`) | (in 127) | PORT-PARTIAL | `internal/syncer/livesync` (L4) | Bounded, never-blocking signal buffer that counts drops. The gotd part moved to `tgclient/conn` in phase 04 |
| `backend/projection/channels.go` | 271 | PORT-PARTIAL | `internal/storage/channelrepo` (L6) | `InsertChannel` (upsert with input validation), `GetChannel`, `ListChannels`, `ChannelExists`, `UpdateAccessHash`, `SetChannelPts`, `SetChannelPtsTx`, `DeleteChannel` cascading every channel-scoped table. Drop `UpdateInviteLink`, `MarkPersonalBackfillDone`, personal-first ordering |
| `backend/services/personaldrive/service.go` | 425 | PATTERN-ONLY | `internal/buckets` (L1), `internal/syncer/reconcile` (L5) | `Create`: create the broadcast channel, then sync and publish; `recover` / `dropProvisionalChannel`: a channel created but never committed is found and removed; `Discover` via owned channels. butterDB: a bucket channel without a committed `t=bcreate` is provisional; registry rediscovery finds the registry by its marker |
| `backend/services/lifecycle/service.go` | 267 | PATTERN-ONLY | `internal/buckets` (L1) | `SyncChannel` (incremental sync, then rebuild when repair is pending), `RebuildProjection` |
| `backend/auth/config.go` | 66 | PATTERN-ONLY | `internal/buckets` (L1) | Channel ID in a small JSON file written with `writePrivateFile`; a present-but-unparseable file is `ErrConfigInvalid`, distinct from absent. butterDB's `state.json` holds the registry channel ID the same way |
| `backend/core/peer_resolution.go` | 37 | PATTERN-ONLY | `internal/buckets` (L1) | `RefreshPeer`: on a stale access hash, resolve again, validate the channel ID and non-zero hash, persist the new hash |
| `backend/services/encryption/service.go` | 508 | PATTERN-ONLY | `internal/buckets` (L1) | `configOp` / `publishConfig`: encryption config travels as a TDX1 op and is projected like any other. butterDB publishes `t=encfg` with the wrapped DEK to the registry. Drop passwords, hints, biometrics |

Tests to carry:

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

### Phase 06 — Write path

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/mountwrite/staging.go` | 555 | PORT-PARTIAL | `internal/ingest/spool` (L1) | Keep: `DiskStagingStore` streaming a body to a private file while hashing, `exactLengthReader` (length mismatch and trailing data rejected), per-object and aggregate byte quotas, slot semaphore with cancellable wait, startup accounting of existing files, partial-file cleanup on every failure, safe keys from operation IDs, private modes. Change: hash MD5 (S3 ETag) and SHA-256 together; spool plaintext and encrypt during send through `crypto/stream.EncryptingReaderAt`. Drop `copyEncryptedToStage` |
| `backend/services/file/hidden_upload.go` | 585 | PORT-PARTIAL | `internal/ingest/put` (L2) | Keep: send the hidden body with `random_id = StableRandomID(jobID, step)`; validate metadata and source size before any remote call; on `ErrSendOutcomeUnknown` resend with the same `random_id` so Telegram returns the original message; persist a recovered receipt before cleanup; `discardHiddenBody` when the commit never happens; refuse a receipt whose message ID is already owned. Drop: the part loop, `partAttachmentName` part suffixes, `hiddenUploadUUID`, `PartsForUUIDContext` |
| `backend/core/engine.go` (`EmitAndProjectContext`, `sendControlWithRetry`, `controlPeer`) | 932 | PORT-PARTIAL | `internal/ingest/put` (L2) | Send the control message, then `ProjectFromOp` its receipt; a projection error after acceptance returns the positive message ID wrapped in `ErrControlProjection` and is never resent. Change: `random_id` from the job ID (E9) |
| `backend/mountwrite/locks.go` | 112 | PORT | `internal/ingest/put` (L2) | `KeyedLocker`: serialize writers of one `(bucket, key)`, sorted and deduplicated multi-key acquisition, entries cleaned up, cancellable wait |
| `backend/mountadapter/remote.go` | 759 | PATTERN-ONLY | `internal/ingest/put` (L2) | Upload hidden, then commit; an unknown outcome or a hidden receipt needing recovery is surfaced as "recovery required", never as failure |
| `backend/mountwrite/coordinator.go` | 452 | PATTERN-ONLY | `internal/ingest/put` (L2) | `createOrLoad` keyed by operation ID so a retried request resumes, `withOperation`, `transition`, `markAborted`, `deferCleanup` |
| `backend/services/file/upload.go` `deleteMessagesChunked` | (in 777) | PORT-PARTIAL | `internal/ingest/remove` (L3) | Chunks of 100, each under the write retry policy, continue after a failed chunk, return the first error |
| `backend/mountwrite/hard_delete.go`, `journal_hard_delete.go` | 184, 303 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/storage/jobrepo` (L5) | Durable plan of message IDs appended once, then `NextHardDeleteBatch` / `MarkHardDeleteBatchDone` so a crash resumes mid-delete. butterDB writes the `t=del` tombstone before the first batch (E4) |
| `backend/projection/hard_delete.go`, `hard_delete_intent.go` | 660, 179 | PATTERN-ONLY | `internal/ingest/remove` (L3) | Plan pages (`maxHardDeletePlanPageSize = 1000`), plan validation that refuses control messages and bodies referenced elsewhere |
| `backend/services/file/mutations.go` (`Delete`, `purgeObject`, `deletePlannedBodies`, `SweepOrphanParts`) | 599 | PATTERN-ONLY | `internal/ingest/remove` (L3), `internal/ingest/sweeper` (L5) | Purge claims a durable plan, deletes bodies, then completes. `SweepOrphanParts` becomes the sweeper's orphan-body pass using `objectrepo.ManagedMsgIDsIn` |
| `backend/services/file/service.go` (`acquireUploadSlot`, `uploadConcurrency`, `retryVisibleSend`, `stageUploadPart`, `createTempWithFallback`) | 797 | PATTERN-ONLY | `internal/ingest/admission` (L4), `internal/ingest/spool` (L1) | Process-wide upload slots; `retryVisibleSend` stops retrying once the outcome is unknown. SlowDown mapping from the flood-wait budget (A7) is new |
| `backend/services/file/upload_failures.go` | 126 | PATTERN-ONLY | `internal/ingest/put` (L2) | `classifyUploadFailure`: not sent, uncertain and canceled are distinct outcomes; phase 08 maps them to S3 errors |
| `backend/mountwrite/journal.go` | 482 | PATTERN-ONLY | `internal/storage/jobrepo` (L5) | `SQLiteJournal`: `Create`, `Get`, compare-and-swap `Transition`, `ListRecoverable` in stable order, typed uniqueness-violation detection, corrupt-row rejection. butterDB's job rows also persist file salt and nonce prefix before the first send (E8) |
| `backend/mountwrite/types.go` | 618 | PATTERN-ONLY | `internal/storage/jobrepo` (L5) | `JournalState` with an explicit `allowedTransitions` table and `ValidateTransition`. TDrive states: `receiving`, `staged`, `uploading`, `uploaded`, `committing`, `reconciling`, `remote_committed`, `projection_pending`, `cleanup_pending`, `delete_plan_pending`, `deleting_bodies`, `delete_finalizing`, `done`, `aborted`. butterDB's job states are defined by the phase 06 document |
| `backend/mountwrite/recovery.go` | 188 | PATTERN-ONLY | `internal/ingest/sweeper` (L5) | `Recover` walks recoverable records: reconcile an uploading receipt before removing staged bytes, retry a committing operation idempotently, finish cleanup |
| `backend/projection/multipart.go` (`QueuePartCleanup`, `PendingPartCleanup`, `ClearPartCleanup`) | (in 332) | PATTERN-ONLY | `internal/ingest/sweeper` (L5) | A durable cleanup queue of message IDs. The rest of the file is cut |
| `backend/mountdav/put_body_buffer.go` | 65 | CUT | none | Unknown-length WebDAV bodies |

New in phase 06 with no TDrive source: MD5 ETag, the `t=put` commit with
key and metadata, overwrite cleanup of superseded messages (E3), SlowDown
mapping, `internal/ingest/multipartstage`.

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/mountwrite/staging_test.go` | 357 | Full | All eight |
| `backend/mountwrite/locks_test.go` | 111 | Full | All three |
| `backend/services/file/hidden_upload_test.go` | 1,021 | Partial | `TestUploadHiddenSingleIsNotVisibleBeforeCommit`, `TestUploadHiddenRetryUsesStableTelegramMessage`, `TestUploadHiddenRetriesBoundedFloodWaitAndRewinds`, `TestUploadHiddenValidatesMetadataAndSourceBeforeRemoteAccess`, `TestUploadHiddenRejectsEncryptedPlaintextAboveTDE1Capacity`, the zero and exact-chunk size cases. Drop multipart cases |
| `backend/services/file/hidden_upload_recovery_test.go` | 606 | Partial | `TestRecoverAndDiscardHiddenUploadClosesSendReceiptCrashGap`, `TestRecoverAndDiscardHiddenUploadPersistsRecoveredReceiptBeforeDeleteRetry`, `TestUploadHiddenUnknownSendOutcomeReconcilesCurrentPartBeforeCleanup`, `TestUploadHiddenAcceptedSendWithoutReceiptIsRecoveredBeforeCleanup`, `TestDiscardHiddenReceiptRejectsMessageIDsWithoutProjectedOwnership` |
| `backend/services/file/upload_retry_test.go` | 731 | Partial | `TestSingleUploadRecoversAfterTransientFailure`, `TestUploadFailsWhenTransientBudgetExhausted`, `TestSingleUploadRetriesLostAcceptedReceiptIdempotently`. Drop multipart and manifest cases |
| `backend/core/engine_write_test.go` | 248 | Partial | `TestEmitAndProjectContextRetriesControlWithStableRandomID`, `TestEmitAndProjectBatchMarksPostSendProjectionFailure` |
| `backend/mountwrite/journal_test.go` | 488 | Pattern | Compare-and-swap transition, invalid transition without mutation, recoverable ordering, corrupt row detection, duplicate create |
| `backend/mountwrite/recovery_test.go`, `hidden_receipt_recovery_test.go` | 598, 463 | Pattern | Receipt reconciled before staged bytes are removed; persisted receipt reused; stage retained until cleanup succeeds |
| `backend/mountwrite/hard_delete_test.go`, `journal_hard_delete_test.go` | 256, 141 | Pattern | Batch resume after a failed batch |

### Phase 07 — Read path and origin cache

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/media/range_reader.go` | 753 | PORT | `internal/readpath/rangereader` (L1) | Keep: `RangeReaderConfig`, `RangeReader`, block-aligned fetches of `RangeReadMaxBytes`, single-flight per block shared by concurrent readers, foreground joining or promoting a background flight, read-ahead window cancelled on seek, `openingChunkBytes = 256 * 1024` prefix on first read, `defaultBackgroundSlots = 4`, `defaultRangeCacheBytes = 64 * 1024 * 1024`, `defaultRangeRetryPolicy` (5 flood retries, 2 min max, 5 min total; 3 transient retries, 250 ms backoff to 3 s, 100 ms jitter), short-read retry, `ReaderAt` EOF semantics, `RangeStats`. Change: the client port (`ResolveDocument`, `ReadDocumentRange`) is declared here; shutdown context comes from `Start`, not a config field; clock injected |
| `backend/media/cache.go` | 81 | PORT | `internal/readpath/rangereader` (L1) | In-memory LRU of blocks with a byte budget, oversized-entry rejection |
| `backend/mountcontent/reader.go` | 687 | PORT-PARTIAL | `internal/readpath/objectreader` (L3) | Keep: `Opener` / `Reader` shape, validation that `CiphertextSize(plaintext) == stored` before asking for a key, key requested only for encrypted objects and only after the body resolves, `RandomAccessDecryptor` over the range reader, `ReadAt` / `Size` / `Close`, encrypted readers closed when the opener closes, bounded concurrent resolution. Drop multipart segments |
| `backend/mountcontent/document_cache.go`, `document_resolution.go`, `peer_cache.go` | 85, 75, 88 | PORT | `internal/readpath/objectreader` (L3) | `DocumentRef` cache with TTL and defensive copies, keyed by the full projected identity; size validated before caching; coalesced resolution where each waiter cancels independently and an abandoned load never caches a late success; peer cache |
| `backend/mountcache/lru.go` | 192 | PORT | `internal/readpath/objectreader` (L3) | Generic weighted LRU with TTL used by the caches above |
| `backend/mountcache/coalescer.go` | 362 | PORT | `internal/readpath/objectreader` (L3) | Generic cancellable load coalescer with invalidation, used by document resolution. Phase 13 needs the same algorithm (see phase 13) |
| `backend/media/server.go` (`parseRangeHeader`, `streamSessionRange`, range response writing) | 502 | PORT-PARTIAL | `internal/readpath/httprange` (L4) | Keep: single-range forms `bytes=a-b`, `bytes=a-`, `bytes=-n`; `Accept-Ranges: bytes`; `206` with `Content-Range: bytes a-b/size`; `416` with `Content-Range: bytes */size`; chunked streaming with a buffer pool (256 KiB chunks). Drop: session tokens, CORS headers, playback updates, thumbnails, idle sweep |
| `backend/mountdav/conditions.go` (`parseETagConditions`, `consumeEntityTag`, `validStrongETag`, `maxConditionalHeaderBytes`) | 318 | PORT-PARTIAL | `internal/readpath/httprange` (L4) | `If-Match` / `If-None-Match` entity-tag list parsing with a 16 KiB header cap. Drop the WebDAV `If` grammar (`parseDAVIf`, lexer) |
| `backend/mountadapter/conditions.go` | 121 | PATTERN-ONLY | `internal/readpath/httprange` (L4) | `matchesIfMatch` / `matchesIfNoneMatch` evaluation against the current entity. butterDB adds `If-Modified-Since` / `If-Unmodified-Since` and `304` |
| `backend/thumbnail/cache.go` | 535 | PATTERN-ONLY | `internal/readpath/blockcache` (L2) | Disk LRU: SHA-256 keyed sharded paths, byte budget plus entry cap (`defaultMaxCacheEntries = 64 << 10`), temp-then-rename writes, index rebuilt by scanning on first use, stray temp cleanup, modes `0o700` / `0o600`, a nil cache is a disabled cache. butterDB caches stored bytes (ciphertext for encrypted objects) keyed by bucket channel, body message ID and block index; bodies are immutable, so overwrite needs no invalidation, only eviction |
| `backend/media/resolver.go`, `service.go`, `types.go` | 120, 431, 114 | PATTERN-ONLY | `internal/readpath/objectreader` (L3) | `Resolve` then `Open`: projected row to document refs to reader; `MasterKeyProvider` as a consumer port |
| `backend/core/projection_change.go` | 84 | CUT | none | Change notifications for the UI. Cache keys are immutable body IDs, so readers need none |
| `backend/media/session.go`, `warm.go`, `mp4_index.go`, `hls.go`, `image_preview.go`, `video_thumbnails.go`, `mpv_thumbnail_session_unix.go`, `raw_drive.go` | 572, 136, 119, 200, 95, 1,062, 345, 298 | CUT | none | Playback sessions, transport warm-up, MP4 index warming, HLS, previews, seek thumbnails, unmanaged attachments. `external.go` is a phase 12 design reference only |

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/media/range_reader_test.go` | 685 | Full | All eleven; fakes `strictRangeFake`, `testBytes` |
| `backend/media/range_reader_window_test.go` | 267 | Full | All four; fake `gatedRangeFake` |
| `backend/media/range_reader_retry_test.go` | 264 | Full | All eight |
| `backend/media/range_reader_opening_test.go` | 68 | Full | Both |
| `backend/media/range_reader_refresh_test.go` | 39 | Full | `TestRangeReaderKeepsRefreshedFileReference` |
| `backend/media/range_reader_stats_test.go` | 34 | Full | `TestRangeReaderStatsCountFetchesAndCacheHits` |
| `backend/media/range_reader_benchmark_test.go` | 105 | Full | Benchmarks |
| `backend/media/cache_test.go` | 26 | Full | Both |
| `backend/media/server_test.go` | 668 | Partial | `TestParseRangeHeader`, `TestMediaServerRangeForms` (rewritten for `httprange`) |
| `backend/mountcontent/encrypted_reader_test.go` | 438 | Partial | `TestOpenerReadsEncryptedFileAsPlaintext`, `TestOpenerRequestsKeyOnlyForEncryptedFilesAfterResolvingBodies`, `TestOpenerRejectsUnavailableWrongAndTamperedEncryptedKeys`, `TestEncryptedOpenAndReaderLifecycleAreCancellationSafe`, `TestOpenerCloseClosesPublishedEncryptedReaders`, `TestEncryptedMetadataRejectsPlaintextBeyondTDE1Capacity` |
| `backend/mountcontent/reader_test.go` | 660 | Partial | `TestOpenerReadsSingleFileRanges`, `TestReaderUsesReaderAtEOFContract`, `TestOpenerRejectsEncryptedFilesWithoutKeyProvider`, `TestNewValidatesDependencies`. Drop multipart and segment cases |
| `backend/mountcontent/document_cache_test.go` | 476 | Full | All |
| `backend/mountcontent/lifecycle_test.go` | 264 | Full | All five |
| `backend/mountcontent/peer_cache_test.go` | 345 | Full | All |
| `backend/mountcache/lru_test.go` | 156 | Full | All, including benchmarks |
| `backend/mountcache/coalescer_test.go` | 347 | Full | All seven |
| `backend/mountdav/conditions_test.go` | 123 | Partial | `TestParseETagConditions`, `TestEntityTagSerializationAndValidation` |
| `backend/thumbnail/cache_test.go` | 431 | Pattern | Budget never exceeded under concurrent writes, LRU eviction, index reload from disk, stray temp cleanup, private permissions, nil cache disabled |

### Phase 08 — S3 API

TDrive has no S3, SigV4, XML or bucket policy code. Phase 08 is new code
except for these.

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/mountdav/body_deadline.go` | 107 | PORT | `internal/s3api/objectops` (L5) | `idleDeadlineBody`: a PUT body read with an idle deadline that resets on progress, not an absolute deadline, so a long upload survives and a stalled one releases its write slot |
| `backend/mountdav/server.go` (listener timeouts) | 436 | PATTERN-ONLY | `internal/core` (integration) | `ReadHeaderTimeout = 5s`, `IdleTimeout = 60s`, `MaxHeaderBytes = 32 << 10`, shutdown 5 s. Do not copy `ReadTimeout = 15s`: it would cut every large PUT; the idle body deadline replaces it |
| `internal/app/operation.go` | 203 | PATTERN-ONLY | `internal/s3api/s3err` (L4) | One typed error with a stable code mapped at the edge |
| `backend/services/file/upload_failures.go` | 126 | PATTERN-ONLY | `internal/s3api/s3err` (L4) | Not sent maps to a retryable 5xx; unknown outcome maps to `503 SlowDown` or `500 InternalError` per the phase 08 document |

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/mountdav/body_deadline_test.go` | 218 | Full | All five |

### Phase 09 — Admin API

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/daemon/auth_drive.go` | 519 | PATTERN-ONLY | `internal/adminapi/setup` (L1) | `authSetup` (save api_id and api_hash), `authStatus`, `authLogin` (start with phone), `completeLogin`; one login flow at a time guarded by a mutex and an "active" flag. butterDB relays the same steps over HTTPS behind the setup token (A2) |
| `backend/daemon/protocol.go` | 488 | PATTERN-ONLY | `api/adminv1` (prelude) | `ProtocolVersion` gates every request so a mismatched client fails with a clear version error |

New in phase 09 with no TDrive source: device tokens, pairing via Saved
Messages (A1), setup token (A2), self-signed TLS with pinned fingerprint (A3).

### Phase 10 — Fyne admin app

No TDrive code is ported. TDrive's UI is Svelte inside Wails (section 8).

| TDrive file | Lines | Verdict | Destination | Porting notes |
| --- | ---: | --- | --- | --- |
| `internal/app/backend_startup.go` | 69 | PATTERN-ONLY | `apps/admin/internal/ui/connect` | The UI waits on a readiness signal before enabling actions |

### Phase 11 — Operations and first release

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/media/throughput_meter.go` | 131 | PATTERN-ONLY | `internal/ops/metrics` (L1) | Sliding-window bytes per second in time buckets with an injectable clock, plus flood-wait notes |
| `internal/app/backend_startup.go` | 69 | PATTERN-ONLY | `internal/ops/health` (L1) | Readiness reported only after startup finishes or fails |
| `cmd/tdrive/auth_drive_commands.go` (`runRebuild`, `validateRebuildOptions`) | (in 762) | PATTERN-ONLY | `cmd/butterdbd` `rebuild` (integration) | Rebuild refuses ambiguous options and requires confirmation when non-interactive |

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/media/throughput_meter_test.go` | 150 | Pattern | Window rollover with a fake clock |

### Phase 12 — Replica account

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/backfill/backfill.go` | 368 | PATTERN-ONLY | `internal/replica/backfill` (L3) | Resumable checkpoint cursor (`backfill_progress`), `alreadyDone` / `markDone` gate, `loadAlreadyPublished` so a restart never republishes, throttle `OpsPerSec = 5` via a ticker, progress events |
| `backend/tgclient/download.go` | 173 | PORT-PARTIAL (phase 04) | `internal/replica/worker` (L2) consumes `tgclient/rangeio` | Copy a primary body by streaming it, then send it to the replica with the same job-derived `random_id` |
| `backend/media/external.go` (`externalRangeClient`) | (in 274) | PATTERN-ONLY | `internal/readpath/replicasource` (L4) | A second range client behind the same read port, checked periodically for access (`externalAccessCheckInterval = 30 * time.Second`) |

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/backfill/backfill_test.go` | 201 | Pattern | Resume after interruption without duplicate ops; runs once per channel |

### Phase 13 — Media transforms

| TDrive file | Lines | Verdict | Destination (lane) | Porting notes |
| --- | ---: | --- | --- | --- |
| `backend/mountcache/coalescer.go` | 362 | PORT | `internal/mediaapi/derivcache` (L3) | One transform per derivative key at a time; abandoned loads never cache late results. A second copy of the phase 07 port, trimmed to what derivcache uses, because the binding layout has no shared package for it. Default (owner may change) |
| `backend/thumbnail/cache.go` | 535 | PATTERN-ONLY | `internal/mediaapi/derivcache` (L3) | Same disk-LRU design as `blockcache`, keyed by put message ID (the object version) and the normalized transform |
| `backend/media/image_admission.go` | 176 | PATTERN-ONLY | `apps/transformd` (L1) | Input admission before decode: `defaultMaxImageStreamBytes = 256 << 20`, `defaultMaxImageStreamPixels = 32_000_000`, `defaultMaxImageHeaderBytes = 4 << 20`, animated WebP probe (`imageAnimationProbeBytes = 32`) |
| `backend/thumbnail/orientation.go` | 152 | PATTERN-ONLY | `apps/transformd` (L1) | EXIF orientation 1 to 8; its tests become auto-orient expectations for libvips output |
| `backend/daemon/socket_unix.go` | 79 | PATTERN-ONLY | `internal/mediaapi/workerclient` (L4) | Unix socket `0o600`, parent directory forced to `0o700` after `Lstat`, stale socket removed before listen and on close |
| `backend/daemon/server_connection.go` | 106 | PATTERN-ONLY | `internal/mediaapi/workerclient` (L4) | `readLimitedLine`: bounded request frames; cancel the request when the connection closes |

Tests to carry:

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/mountcache/coalescer_test.go` | 347 | Full | The cases that cover what derivcache keeps |
| `backend/thumbnail/orientation_test.go` | 115 | Pattern | Expected orientation per EXIF value |

---

## 7. Test fixtures and fakes worth reusing

TDrive `TESTING.md` names the fixtures to extend before building a new
harness: the file service fixture, sync fixtures, the single-connection
regression, crypto random-access tests, range-window tests and encrypted media
tests. These are the ones butterDB carries.

### 7.1 The fake Telegram client

`backend/tgclient/fake.go` (1,154 lines) is TDrive's in-memory `Client`. It is
the base for `internal/tgclient/faketg` (phase 04 L5).

Keep:

| Capability | TDrive API |
| --- | --- |
| Records sends for assertions | `SentControls()`, `SentFiles()`, `DeletedBatches()` with `SentControl{Peer, Text, Silent, MsgID, RandomID}` |
| Idempotent sends by `random_id` per channel | `sendDedupeKey{ChannelID, RandomID}`; a repeated `random_id` returns the original message |
| History paging as Telegram does it (newest first, `minID`, `offsetID`) | `SeedHistory`, `GetHistory` |
| Bodies for range reads | `SeedDocumentBody`, `ResolveDocument`, `ResolveDocuments`, `ReadDocumentRange`, `DownloadFile` |
| Channel difference with `pts` | `GetChannelDifference`, `GetChannelPts`, `InjectDifferenceTooLong` |
| Out-of-band deletion | `DeleteMessages` on the fake, `MissingMessages` |
| Caption tamper | `EditLastControlText` |
| Fault injection | `InjectFloodWaits`, `InjectReadFloodWaits`, `InjectTransientFailures` (`ErrInjectedTransport`), `FailNextSend` (`ErrInjectedSend`) |
| Owned channels | `CreateBroadcastChannel`, `SeedOwnedBroadcastChannels`, `ListOwnedBroadcastChannels` |

Must change, because butterDB keeps one channel per bucket (E1) and the fake
was effectively single-channel:

| TDrive behaviour | Required `faketg` behaviour |
| --- | --- |
| `nextMsgID` is one counter across all channels | Message IDs per channel, as Telegram assigns them |
| Messages recorded by sends have `ChannelID` 0, so `GetHistory` returns them for every peer | Every message carries its channel; history is per channel |
| `fileBodies` keyed by message ID only | Bodies keyed by `(channel_id, msg_id)` |
| `DeleteMessages` ignores the peer | Deletion scoped to the peer's channel |
| One `pts` and one event log for the whole fake | `pts` and events per channel |
| No way to accept a send and lose its receipt | Script "accept, then return `ErrSendOutcomeUnknown`" for sends and control sends; the message exists and a resend with the same `random_id` returns it |
| Flood waits always 10 ms (send) or 1 ms (read) | Scripted duration per call, so the SlowDown budget (A7) is testable |
| Faults are counters on sends and history only | Fault scripts on any exported method (`docs/claude.md` §7, Liskov row) |
| Implements the 35-method TDrive `Client` plus `IdempotentSender` | Implements the exported method set of butterDB's `tgclient` adapters |

Drop: forums (`fake_forum.go`), media sources, public channels, invites, join
requests, user profiles, channel photos, thumbnails.

Its own tests come along: `backend/tgclient/fake_test.go` (174 lines) and
`backend/tgclient/fake_difference_test.go` (163 lines), plus new isolation
cases (two channels never see each other's messages, bodies, deletions or
`pts`).

### 7.2 Other fixtures and fakes

| TDrive helper | File | What it gives | butterDB use |
| --- | --- | --- | --- |
| `newSyncEnv` | `backend/sync/sync_test.go:25` | Real SQLite projection, a fake client and a sync engine | `syncer/scan` and `syncer/difference` tests |
| `newPooledSyncEnv`, `probingClient` | `backend/sync/connection_hold_test.go:24,45` | A client wrapper that probes the database during every network call and fails if a transaction is held | Every phase that holds the single writer near a network call (05, 06, 12). This is the E7 regression |
| `failingHistoryClient` | `backend/sync/history_retry_test.go:15` | Scripted history failures | `syncer/scan` retry tests |
| `recordingHistoryPager` | `backend/sync/sync_test.go:297` | Records paging calls | Page-planning tests |
| `newTestDB`, `mustOp` | `backend/projection/apply_test.go:12,435` | In-memory projection database and op application helper | `projection/apply` tests |
| `newRebuildDB`, `seedReplay` | `backend/projection/rebuild_test.go:13,26` | Seeds `replay_log` rows directly | `projection/rebuild` and per-key repair tests |
| `newTestService` | `backend/services/file/service_test.go:93` | Real SQLite projection plus `tgclient.Fake` file service | Pattern for the `ingest/put` fixture |
| `newTestCoordinator`, `fakeRemote`, `failingTransitionJournal`, `failingStaging` | `backend/mountwrite/coordinator_test.go:1142,1186,1525,1530` | Coordinator with injected remote and failure wrappers at commit points | Pattern for `ingest/put` and `ingest/sweeper` crash-point tests |
| `newEncryptedTestStore` | `backend/mountwrite/encrypted_staging_test.go:379` | Staging store with quotas | `ingest/spool` tests |
| `openJournalDB` | `backend/mountwrite/journal_test.go:479` | File-backed SQLite for restart durability | `jobrepo` reopen tests |
| `fakeHardDeleteRemote`, `failingHardDeleteJournal` | `backend/mountwrite/hard_delete_test.go:173,204` | Partial batch failure | `ingest/remove` tests |
| `strictRangeFake`, `testBytes` | `backend/media/range_reader_test.go:431,627` | Range client that enforces alignment and limits | `readpath/rangereader` tests |
| `gatedRangeFake` | `backend/media/range_reader_window_test.go:26` | Range client that holds reads until released | Read-ahead and seek-cancel tests |
| `testRetryPolicy` | `backend/media/range_reader_retry_test.go:17` | Retry policy with an injected sleeper | Rangereader retry tests |
| `newMediaRangeFake`, `newCountingMediaRangeFake` | `backend/media/server_test.go:574`, `service_encrypted_test.go:528` | Bodies by message ID, with read counts | `objectreader` and `httprange` tests |
| `newRangeFake`, `newTestOpener` | `backend/mountcontent/reader_test.go:548,618` | Opener over a fake range client and test database | `objectreader` tests |
| `newCountingDocumentResolver`, `newCountingPeerResolver` | `backend/mountcontent/document_cache_test.go:379`, `peer_cache_test.go:264` | Count resolutions to prove caching and coalescing | `objectreader` cache tests |
| `newBlockSource` | `backend/tgclient/download_test.go:27` | Block source for download retry tests | `tgclient/rangeio` tests |
| `newCoordinatorTestClock` | `backend/tgclient/write_coordinator_test.go:20` | Fake clock for cooldowns | `tgclient/flow` tests; replace with `platform/clock` fake |
| `fakeActivity`, `fakeSyncer` | `backend/livesync/coordinator_test.go:11,23` | Signal source and sync recorder | `syncer/livesync` tests |
| `scriptedDialogsQuery` | `backend/tgclient/personal_drive_test.go:13` | Scripted dialog pages | `tgclient/rpc` owned-channel tests |
| `newMeterClock` | `backend/media/throughput_meter_test.go:15` | Fake clock for windows | `ops/metrics` tests |
| `FuzzRandomAccessDecryptorChunkBoundaries` | `backend/crypto/random_access_test.go:513` | Fuzz target with seeds | `crypto/randomaccess` |

Media fixtures `backend/media/remux/testdata/{clip.mkv,clip.packets.csv,laced.mkv,laced.packets.csv}`
are not carried; butterDB has no remuxer. Phase 13 generates its own small
redistributable images and videos.

---

## 8. Cut subsystems

| Subsystem | TDrive paths | Measured size (non-test lines) | Why it is cut |
| --- | --- | ---: | --- |
| Wails desktop shell | root `main.go`, `internal/app` (53 files), Wails bindings in `frontend/bindings` | 20 + 8,485 | D13 replaces the web UI with a native Fyne app, and D14 puts the engine in a headless server. `github.com/wailsapp/wails` never enters `go.mod` (`docs/claude.md` §11) |
| Frontend | `frontend/` (614 source files) | 106,904 (TS, Svelte, JS, CSS) | No Node, no Svelte (house rules). The admin UI is Fyne (phase 10) |
| Mobile | `build/android`, `build/ios`, `ios_build_metadata_test.go`, `internal/app/{mobile.go,mobile_stub.go,gallery_platform_mobile.go,upload_backup_ios*.go,visible_storage_ios.go,safe_area.go}`, `backend/thumbnail/{local_android,local_apple,poster_android,poster_apple,poster_mobile}.go` and their C and Objective-C files | part of the totals above | butterDB is a server plus a desktop admin app. No phone client exists |
| Mount and WebDAV | `backend/mountadapter`, `mountcontroller`, `mountdav`, `mountfs`, `mountlifecycle`, `mountlocal`, `mountos`, `mountpath`, `mountpolicy`, `mountsafe`, `mountscratch`, `mountwrite`, `backend/core/mount_policy.go`, `cmd/tdrive/mount.go`, `internal/app/mount.go` | 17,057 across the `mount*` packages | The access surface is S3 (D5), not a mounted drive. The files kept from `mountcache`, `mountcontent`, `mountwrite`, `mountdav` and `mountadapter` are listed in section 6 |
| Native player | `backend/nativeplayer` (21 files) | 4,026 | Playback belongs to the client application. butterDB serves byte ranges |
| Photo backup | `backend/photobackup`, `internal/app/photo_backup*.go` | 1,317 + app files | A mobile feature of a personal drive. Applications push objects through S3 |
| Biometric unlock | `backend/biometric`, `backend/services/encryption/biometric.go`, `internal/app/biometric.go` | 1,201 + 155 + 309 | There is no passphrase vault to unlock. The server key file is the KEK (A6) |
| Updater | `backend/updater`, `internal/app/updates*.go` | 2,432 | `butterdbd` is deployed by package, container or systemd (phase 11). The Fyne app is packaged per OS (phase 10) |
| Gallery | `backend/projection/gallery*.go`, `backend/galleryimage`, `backend/services/read/gallery.go`, `internal/app/gallery*.go` | 1,064 (projection gallery files) + 235 + 100 | A photo timeline is a client feature. Listing is ListObjectsV2 |
| Remux, HLS and playback media | `backend/media/remux`, `backend/media/{hls.go,mp4_index.go,session.go,warm.go,video_thumbnails.go,mpv_thumbnail_session_unix.go,image_preview.go}` | 3,097 + 2,529 | Transforms are URL-driven images and video posters through `butterdb-transformd` (D16). Streaming is plain HTTP ranges |
| Daemon and CLI | `backend/daemon` (except the 5 pattern files), `cmd/tdrive` (except the 2 pattern files), `cmd/tdrive/daemon_*.go` | 3,830 + 3,823 | `butterdbd` is itself the long-running server; its subcommands are listed in the brief. There is no local socket protocol for a CLI |
| Channel sources | `backend/channelsource`, `backend/tgclient/{forum.go,gotd_forum.go,fake_forum.go,gotd_sources.go,media_name.go}`, `backend/media/{external.go,raw_drive.go}` (except the phase 12 reference to `externalRangeClient`), `internal/app/channel_sources.go` | 1,770 + 623 + 572 | butterDB reads only the bucket channels it writes. Browsing arbitrary chats and forum topics is not object storage |
| TDX1 multipart parts | `backend/projection/multipart.go`, `t=part` / `t=manifest` in `wire.go` and `types.go`, `backend/services/file/{upload.go (multipart path),resumable_upload.go,resumable_run.go,resumable_download.go,resumable_download_run.go}`, `MaxParts`, `LargeFileMaxBytes`, `MinResumableUploadBytes` | 332 + 2,102 + multipart sections | D9: one Telegram document per object, at most 1900 MiB stored. S3 multipart is assembled on disk by `internal/ingest/multipartstage`, never split across messages |
| Folder namespace | `backend/projection/{namespace.go,namespace_read.go,dirents in schema.go,writable_schema.go,writable_revision_migration.go,file_revision_state.go,restore_apply.go}`, `backend/services/folder`, `backend/core/paths.go` | 1,235 + 337 + 281 plus schema sections | S3 keys are flat byte strings with prefix and delimiter listing (D6). No NFC normalization, no portable-name rules, no folder objects |
| Trash, revisions and retention | `backend/projection/{trash.go,retention.go}`, `internal/app/{trash.go,trash_sweep.go}` | 486 + 212 | D10: immediate hard delete, no versioning, no trash |
| Renditions | `backend/projection/{renditions.go,rendition_outbox.go,rendition_migration.go}`, `backend/services/file/{rendition*.go,backup_rendition_worker.go}`, `backend/services/file/thumbnail.go` | 373 + 1,494 | TDrive stores thumbnails in Telegram. butterDB derivatives live in the local derivative cache (phase 13) |
| Shared drives and invites | `backend/services/channel`, `backend/auth/group.go`, `backend/projection/pending_joins.go`, `backend/tgclient/invite.go` | 508 + 288 + 104 + 7 | One owner per deployment (D3). Applications get access keys, not channel membership |
| Passphrase vault | `backend/services/encryption/service.go` (password paths), `crypto/vault.go` Argon2id half, `internal/app/{encryption.go,vault_session.go}` | part of 755 + 308 | The server key file replaces the passphrase (A6) |
| Personal drive bootstrap and legacy backfill | `backend/services/personaldrive` (code), `backend/backfill` (code), `backend/services/lifecycle` (code) | 425 + 368 + 267 | butterDB has no legacy local namespace to publish. These remain only as design references (section 6) |
| Import, archive, folder download | `backend/services/file/{import.go,import_policy.go,import_walk.go,archive.go,folder_download.go,download*.go}` | 2,492 | Desktop features. Applications upload and download objects individually |
| User display names | `backend/services/user` | 261 | No multi-user channel. Identity binding records only the owner's user ID |
| Read service | `backend/services/read` | 509 | Folder listing and search are replaced by ListObjectsV2 |
| Misc helpers | `backend/services/servicecontext` | 21 | A nil-context check; butterDB rejects nil contexts at each exported function |

---

## 9. Attribution checklist

`docs/legal/THIRD_PARTY_NOTICES.md` carries TDrive's MIT notice once, then one
table row per TDrive file butterDB ported or used as a design reference. Row
format, Default (owner may change):

```
| TDrive source | butterDB destination | Kind | Phase |
| backend/media/range_reader.go | internal/readpath/rangereader | ported | 07 |
```

`Kind` is `ported` for PORT and PORT-PARTIAL, `design reference` for
PATTERN-ONLY. The expected rows when phase 13 closes:

| Phase | Ported | Design reference |
| --- | --- | --- |
| 01 | `backend/datadir/datadir.go`, `backend/processlock/lock.go`, `backend/processlock/lock_unix.go`, `backend/processlock/process_unix.go`, `backend/auth/private_file.go`, `backend/services/file/limits.go` | `backend/applog/applog.go`, `internal/app/operation.go`, `backend/daemon/server.go`, `cmd/tdrived/main.go`, `cmd/tdrive/output.go` |
| 02 | `backend/projection/wire.go`, `backend/projection/types.go`, `backend/crypto/stream.go`, `backend/crypto/random_access.go`, `backend/projection/schema.go`, `backend/sync/initial_scan_spool.go` | `backend/storage_sqlite.go` |
| 03 | `backend/projection/log.go`, `backend/projection/rebuild.go`, `backend/projection/encryption_rebuild.go`, `backend/projection/managed_messages.go`, `backend/projection/reconcile.go`, `backend/crypto/stream.go`, `backend/crypto/random_access.go`, `backend/crypto/vault.go` | `backend/projection/apply.go`, `backend/projection/writable_apply.go`, `backend/projection/encryption.go`, `backend/projection/read.go`, `backend/services/encryption/key_lease.go` |
| 04 | `backend/tgclient/client.go`, `range.go`, `session.go`, `gotd.go`, `gotd_pool.go`, `gotd_range.go`, `gotd_media.go`, `download.go`, `floodwait_retry.go`, `write_coordinator.go`, `getfile_limiter.go`, `transient.go`, `idempotency.go`, `upload_part_retry.go`, `upload_media_retry.go`, `personal_drive.go`, `fake.go`; `backend/auth/peer_resolution.go`, `session_storage.go`, `auth.go`; `backend/services/auth/service.go`; `backend/livesync/activity.go` | `cmd/tdrive/auth_drive_commands.go` |
| 05 | `backend/sync/sync.go`, `difference.go`, `parse.go`, `initial_scan_spool.go`; `backend/livesync/coordinator.go`, `activity.go`; `backend/projection/channels.go` | `backend/services/personaldrive/service.go`, `backend/services/lifecycle/service.go`, `backend/auth/config.go`, `backend/core/peer_resolution.go`, `backend/services/encryption/service.go` |
| 06 | `backend/mountwrite/staging.go`, `backend/mountwrite/locks.go`, `backend/services/file/hidden_upload.go`, `backend/core/engine.go`, `backend/services/file/upload.go`, `backend/projection/errors.go` | `backend/mountadapter/remote.go`, `backend/mountwrite/{coordinator.go,journal.go,types.go,recovery.go,hard_delete.go,journal_hard_delete.go}`, `backend/projection/{hard_delete.go,hard_delete_intent.go,multipart.go}`, `backend/services/file/{mutations.go,service.go,upload_failures.go}` |
| 07 | `backend/media/range_reader.go`, `backend/media/cache.go`, `backend/media/server.go`, `backend/mountcontent/{reader.go,document_cache.go,document_resolution.go,peer_cache.go}`, `backend/mountcache/{lru.go,coalescer.go}`, `backend/mountdav/conditions.go` | `backend/mountadapter/conditions.go`, `backend/thumbnail/cache.go`, `backend/media/{resolver.go,service.go,types.go}` |
| 08 | `backend/mountdav/body_deadline.go` | `backend/mountdav/server.go`, `internal/app/operation.go`, `backend/services/file/upload_failures.go` |
| 09 | none | `backend/daemon/auth_drive.go`, `backend/daemon/protocol.go` |
| 10 | none | `internal/app/backend_startup.go` |
| 11 | none | `backend/media/throughput_meter.go`, `internal/app/backend_startup.go`, `cmd/tdrive/auth_drive_commands.go` |
| 12 | none beyond phase 04 | `backend/backfill/backfill.go`, `backend/media/external.go` |
| 13 | `backend/mountcache/coalescer.go` | `backend/thumbnail/cache.go`, `backend/media/image_admission.go`, `backend/thumbnail/orientation.go`, `backend/daemon/socket_unix.go`, `backend/daemon/server_connection.go` |

A ported test file is covered by the row of the source file it tests. A file
that appears in two phases gets one row per destination.
