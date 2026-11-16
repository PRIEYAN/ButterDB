# Third-party notices

butterDB contains code adapted from TDrive and links or invokes the third-party
software listed below. This file carries the notices those licenses require and
records every TDrive file ported into butterDB (D18).

| Item | Rule |
| --- | --- |
| Created | Phase 01 prelude |
| Kept current by | Each phase's integration lane, from the `PORTED` sections of the lane reports ([00-execution-protocol.md](../phases/00-execution-protocol.md) §10) |
| New dependency | Listed in §3 in the same commit that adds it to a `go.mod` |
| Before a release | §4 |

butterDB's own license is an owner gate (A10) and is recorded in the root
`LICENSE` once chosen. The TDrive notice below is carried unchanged.

---

## 1. TDrive

| Field | Value |
| --- | --- |
| Project | TDrive |
| Source | https://github.com/jeetrex17/TDrive |
| License | MIT |
| Copyright | Copyright (c) 2026 Jeetraj |
| Local reference checkout | `/home/prieyan/weeb/TDrive` (module `TDrive`, Go 1.26.11) |
| Used as | Reference implementation of the storage engine; files ported package by package into butterDB's layout, with comment blocks removed and package-level state replaced by injected structs ([plan.md](../plan.md) §1) |

License text, copied exactly from `/home/prieyan/weeb/TDrive/LICENSE`:

```text
MIT License

Copyright (c) 2026 Jeetraj

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

Every source file in a ported package is covered by this notice. Source files
carry no per-file header, because butterDB's code policy forbids comment blocks
([claude.md](../claude.md) §8.1); this file is the attribution.

---

## 2. butterDB packages ported from TDrive

Paths in the `TDrive source` column are relative to the TDrive repository root;
paths in the `butterDB destination` column are under `github.com/PRIEYAN/ButterDB`.
Every TDrive path listed was confirmed to exist in the reference checkout. The
rows follow the attribution list of
[tdrive-port-map.md](../reference/tdrive-port-map.md) §9, which derives them from
the per-phase verdicts in its §6.

| Column | Values |
| --- | --- |
| `Kind` | `ported` for a PORT or PORT-PARTIAL verdict (code was adapted, so this notice is required); `design reference` for PATTERN-ONLY (no code copied; listed for transparency) |
| `Status` | `planned` until the integration lane of the named phase replaces it with `ported <short commit hash>` or `referenced <short commit hash>`. A row is never deleted; a file that turned out not to be used becomes `not used: <reason>`. A lane that ports a file not listed here adds a row in the same commit |

A ported test file is covered by the row of the source file it tests. A file
that lands in two destinations has one row per destination.

### 2.1 Ported

| TDrive source | butterDB destination | Kind | Phase | Status |
| --- | --- | --- | --- | --- |
| `backend/datadir/datadir.go` | `internal/platform/datadir` | ported | 01 | ported 0afdef4 |
| `backend/processlock/lock.go` | `internal/platform/processlock` | ported | 01 | ported bea07c5 |
| `backend/processlock/lock_unix.go` | `internal/platform/processlock` | ported | 01 | ported bea07c5 |
| `backend/processlock/process_unix.go` | `internal/platform/processlock` | ported | 01 | ported bea07c5 |
| `backend/auth/private_file.go` | `internal/platform/secretfile` | ported | 01 | ported 5e14d5f |
| `backend/services/file/limits.go` | `internal/domain/limits` | ported | 01 | referenced b301829 |
| `backend/projection/wire.go` | `internal/wire` | ported | 02 | ported eeb198b |
| `backend/projection/types.go` | `internal/wire` | ported | 02 | not used: the Op and OpType contract came from wire-format.md section 2 |
| `backend/crypto/stream.go` (constants, size arithmetic, nonce and header layout) | `internal/crypto/format` | ported | 02 | ported 1b2385f |
| `backend/crypto/random_access.go` (`StoredOffset`, `ciphertextChunkOffset`) | `internal/crypto/format` | ported | 02 | ported 1b2385f |
| `backend/projection/schema.go` | `internal/storage/schema` | ported | 02 | not used: DDL taken from schema.md section 4.1, file not opened |
| `backend/sync/initial_scan_spool.go` (DDL) | `internal/storage/schema` | ported | 02 | not used: DDL taken from schema.md section 4.1, file not opened |
| `backend/projection/log.go` | `internal/projection/log` | ported | 03 | ported eff7c7b |
| `backend/projection/rebuild.go` | `internal/projection/rebuild` | ported | 03 | ported d442493 |
| `backend/projection/encryption_rebuild.go` | `internal/projection/rebuild` | ported | 03 | ported d442493 |
| `backend/projection/managed_messages.go` | `internal/storage/objectrepo` | ported | 03 | ported 9be17ab |
| `backend/projection/reconcile.go` | `internal/storage/objectrepo` | ported | 03 | ported 9be17ab |
| `backend/crypto/stream.go` (`EncryptStream`, `DecryptStream`, subkey derivation) | `internal/crypto/stream` | ported | 03 | ported ce83399 |
| `backend/crypto/random_access.go` | `internal/crypto/randomaccess` | ported | 03 | ported bb20600 |
| `backend/crypto/vault.go` (key wrap and key check only; Argon2id and the passphrase vault are not ported) | `internal/crypto/keyring` | ported | 03 | ported d447325 |
| `backend/projection/errors.go` | `internal/ingest/put` | ported | 03 (lands in 06) | ported dd7c636 |
| `backend/tgclient/client.go` | `internal/tgclient` | ported | 04 | ported d62c211 |
| `backend/tgclient/range.go` | `internal/tgclient` | ported | 04 | ported d62c211 |
| `backend/tgclient/session.go` | `internal/tgclient/conn` | ported | 04 | ported 262a9e6 |
| `backend/tgclient/gotd.go` | `internal/tgclient/conn`, `internal/tgclient/send`, `internal/tgclient/rpc` | ported | 04 | ported 262a9e6, 292a62b, 53b52e2 |
| `backend/tgclient/gotd_pool.go` | `internal/tgclient/conn` | ported | 04 | ported 262a9e6 |
| `backend/tgclient/gotd_range.go` | `internal/tgclient/rangeio`, `internal/tgclient/conn` | ported | 04 | ported 09263ec, ed7d766 |
| `backend/tgclient/gotd_media.go` | `internal/tgclient/rangeio` | ported | 04 | ported 09263ec |
| `backend/tgclient/download.go` | `internal/tgclient/rangeio` | ported | 04 | ported 09263ec |
| `backend/tgclient/floodwait_retry.go` | `internal/tgclient/flow` | ported | 04 | ported c505673 |
| `backend/tgclient/write_coordinator.go` | `internal/tgclient/flow` | ported | 04 | ported 2433425 |
| `backend/tgclient/getfile_limiter.go` | `internal/tgclient/flow` | ported | 04 | ported 2433425 |
| `backend/tgclient/transient.go` | `internal/tgclient/flow` | ported | 04 | ported c505673 |
| `backend/tgclient/idempotency.go` | `internal/tgclient/send` | ported | 04 | ported 292a62b |
| `backend/tgclient/upload_part_retry.go` | `internal/tgclient/send` | ported | 04 | ported 292a62b |
| `backend/tgclient/upload_media_retry.go` | `internal/tgclient/send` | ported | 04 | ported 292a62b |
| `backend/tgclient/personal_drive.go` | `internal/tgclient/rpc` | ported | 04 | ported 53b52e2 |
| `backend/tgclient/fake.go` | `internal/tgclient/faketg` | ported | 04 | ported 0922506 |
| `backend/auth/peer_resolution.go` | `internal/tgclient/rpc` | ported | 04 | ported 53b52e2 |
| `backend/auth/session_storage.go` | `internal/tgclient/conn` | ported | 04 | ported 262a9e6 |
| `backend/auth/auth.go` | `internal/tgclient/authrpc`, `internal/identity`, `internal/tgclient/conn` | ported | 04 | ported 7b3357c, 4dd305a, 262a9e6 |
| `backend/services/auth/service.go` | `internal/identity` | ported | 04 | ported 4dd305a |
| `backend/livesync/activity.go` (`Handle`, `ChannelIDsFromUpdates`) | `internal/tgclient/conn` | ported | 04 | ported 262a9e6 |
| `backend/sync/sync.go` | `internal/syncer/scan`, `internal/syncer/reconcile` | ported | 05 | ported ace28dd, 04f8f18 |
| `backend/sync/difference.go` | `internal/syncer/difference` | ported | 05 | ported fa2cf9a |
| `backend/sync/parse.go` | `internal/syncer/scan` | ported | 05 | ported ace28dd |
| `backend/sync/initial_scan_spool.go` | `internal/syncer/scan` | ported | 05 | ported ace28dd |
| `backend/livesync/coordinator.go` | `internal/syncer/livesync` | ported | 05 | ported 39cbec0 |
| `backend/livesync/activity.go` (`TelegramActivity`, `Signals`) | `internal/syncer/livesync` | ported | 05 | ported 262a9e6 (landed in `internal/tgclient/conn` in phase 04; `internal/syncer/livesync` consumes it through a `SignalSource` port) |
| `backend/projection/channels.go` | `internal/storage/channelrepo` | ported | 05 | ported c98a591, 2c8e952 |
| `backend/mountwrite/staging.go` | `internal/ingest/spool` | ported | 06 | ported 63c5665 |
| `backend/mountwrite/locks.go` | `internal/ingest/put` | ported | 06 | ported dd7c636 |
| `backend/services/file/hidden_upload.go` | `internal/ingest/put` | ported | 06 | ported dd7c636 |
| `backend/core/engine.go` (`EmitAndProjectContext`, `sendControlWithRetry`, `controlPeer`) | `internal/ingest/put` | ported | 06 | ported a8df612 |
| `backend/services/file/upload.go` (`deleteMessagesChunked`) | `internal/ingest/remove` | ported | 06 | ported 3c0a999 |
| `backend/media/range_reader.go` | `internal/readpath/rangereader` | ported | 07 | ported 6a2ef30 |
| `backend/media/cache.go` | `internal/readpath/rangereader` | ported | 07 | ported 6a2ef30 |
| `backend/media/server.go` (range parsing and range responses) | `internal/readpath/httprange` | ported | 07 | ported 30888a5 |
| `backend/mountcontent/reader.go` | `internal/readpath/objectreader` | ported | 07 | ported 7173877 |
| `backend/mountcontent/document_cache.go` | `internal/readpath/objectreader` | ported | 07 | ported 7173877 |
| `backend/mountcontent/document_resolution.go` | `internal/readpath/objectreader` | ported | 07 | ported 7173877 |
| `backend/mountcontent/peer_cache.go` | `internal/readpath/objectreader` | ported | 07 | ported 7173877 |
| `backend/mountcache/lru.go` | `internal/readpath/objectreader` | ported | 07 | ported 7173877 |
| `backend/mountcache/coalescer.go` | `internal/readpath/objectreader` | ported | 07 | ported 7173877 |
| `backend/mountdav/conditions.go` (ETag conditions) | `internal/readpath/httprange` | ported | 07 | ported 30888a5 (`serializeEntityTag` from `backend/mountdav/locks.go` landed as `SerializeEntityTag`) |
| `backend/mountdav/body_deadline.go` | `internal/s3api/objectops` | ported | 08 | ported f363e50 (`idlebody.go` and 4 of its 5 tests; the control-body and PROPFIND cases are WebDAV-only and dropped) |
| `backend/mountcache/coalescer.go` | `internal/mediaapi/derivcache` | ported | 13 | planned |

### 2.2 Design references

| TDrive source | butterDB destination | Kind | Phase | Status |
| --- | --- | --- | --- | --- |
| `backend/applog/applog.go` | `internal/platform/logging` | design reference | 01 | not used: pattern only, not consulted |
| `internal/app/operation.go` | `internal/domain/errs` | design reference | 01 | referenced b301829 |
| `backend/daemon/server.go` | `internal/lifecycle`, `internal/core` | design reference | 01 | referenced bc16165 |
| `cmd/tdrived/main.go` | `cmd/butterdbd` | design reference | 01 | referenced 21a40db |
| `cmd/tdrive/output.go` | `cmd/butterdbd` | design reference | 01 | referenced 21a40db |
| `backend/storage_sqlite.go` | `internal/storage/sqlitedb` | design reference | 02 | referenced 9d868f4 |
| `backend/projection/apply.go` | `internal/projection/apply` | design reference | 03 | not used: pattern only, not consulted |
| `backend/projection/writable_apply.go` | `internal/projection/apply` | design reference | 03 | not used: pattern only, not consulted |
| `backend/projection/encryption.go` | `internal/storage/bucketrepo` | design reference | 03 | referenced 8b8b620 |
| `backend/projection/read.go` | `internal/storage/objectrepo` | design reference | 03 | referenced 9be17ab |
| `backend/services/encryption/key_lease.go` | `internal/crypto/keyring` | design reference | 03 | referenced 9bcd5d3 |
| `cmd/tdrive/auth_drive_commands.go` | `cmd/butterdbd` (`setup`, `rebuild`) | design reference | 04, 11 | referenced 9a4561c for setup (pattern from the port map, file not read); rebuild planned in 11 |
| `backend/auth/config.go` | `internal/buckets` | design reference | 05 | referenced 3e85118 (pattern only, no code copied) |
| `backend/core/peer_resolution.go` | `internal/buckets` | design reference | 05 | referenced 3b00024 (pattern only, no code copied) |
| `backend/services/personaldrive/service.go` | `internal/buckets`, `internal/syncer/reconcile` | design reference | 05 | referenced 3b00024, 95ba346 (pattern only, no code copied) |
| `backend/services/lifecycle/service.go` | `internal/buckets` | design reference | 05 | referenced 3b00024 (pattern only, no code copied) |
| `backend/services/encryption/service.go` | `internal/buckets` | design reference | 05 | referenced 3b00024 (pattern only, no code copied) |
| `backend/mountadapter/remote.go` | `internal/ingest/put` | design reference | 06 | referenced dd7c636, a8df612 |
| `backend/mountwrite/coordinator.go` | `internal/ingest/put` | design reference | 06 | referenced dd7c636, a8df612 |
| `backend/mountwrite/journal.go`, `types.go` | `internal/storage/jobrepo` | design reference | 06 | referenced e86e985 |
| `backend/mountwrite/recovery.go` | `internal/ingest/sweeper` | design reference | 06 | referenced 74c4f17 |
| `backend/mountwrite/hard_delete.go`, `journal_hard_delete.go` | `internal/ingest/remove`, `internal/storage/jobrepo` | design reference | 06 | referenced 3c0a999, e86e985 |
| `backend/projection/hard_delete.go`, `hard_delete_intent.go` | `internal/ingest/remove` | design reference | 06 | referenced 3c0a999 |
| `backend/projection/multipart.go` (part cleanup queue) | `internal/ingest/sweeper` | design reference | 06 | referenced bf1d9d7, 74c4f17 (the queue itself landed in `internal/storage/objectrepo`) |
| `backend/services/file/mutations.go` | `internal/ingest/remove`, `internal/ingest/sweeper` | design reference | 06 | referenced 3c0a999, b49af86, 74c4f17 |
| `backend/services/file/service.go` | `internal/ingest/admission`, `internal/ingest/spool` | design reference | 06 | referenced 8ce0972, 63c5665 |
| `backend/services/file/upload_failures.go` | `internal/ingest/put`, `internal/s3api/s3err` | design reference | 06, 08 | referenced dd7c636 for put; s3err: not used, the unknown-outcome mapping is `s3err.Map` per s3-api section 12.1 |
| `backend/mountadapter/conditions.go` | `internal/readpath/httprange` | design reference | 07 | referenced 30888a5 |
| `backend/thumbnail/cache.go` | `internal/readpath/blockcache`, `internal/mediaapi/derivcache` | design reference | 07, 13 | referenced e1edf6a for blockcache; derivcache planned in 13 |
| `backend/media/resolver.go`, `service.go`, `types.go` | `internal/readpath/objectreader` | design reference | 07 | referenced 7173877 |
| `backend/mountdav/server.go` (listener timeouts) | `internal/core` | design reference | 08 | referenced 0db6453 (server timeouts and bounded graceful shutdown; the values are the s3-api section 2.2 ones) |
| `internal/app/operation.go` | `internal/s3api/s3err` | design reference | 08 | not used: s3err maps `errs` sentinels to S3 codes per s3-api section 14 and adopts no `OperationError` type |
| `backend/daemon/auth_drive.go` | `internal/adminapi/setup` | design reference | 09 | planned |
| `backend/daemon/protocol.go` | `api/adminv1` | design reference | 09 | planned |
| `internal/app/backend_startup.go` | `apps/admin/internal/ui/connect`, `internal/ops/health` | design reference | 10, 11 | planned |
| `backend/media/throughput_meter.go` | `internal/ops/metrics` | design reference | 11 | planned |
| `backend/backfill/backfill.go` | `internal/replica/backfill` | design reference | 12 | planned |
| `backend/media/external.go` (`externalRangeClient`) | `internal/readpath/replicasource` | design reference | 12 | planned |
| `backend/media/image_admission.go` | `apps/transformd` | design reference | 13 | planned |
| `backend/thumbnail/orientation.go` | `apps/transformd` | design reference | 13 | planned |
| `backend/daemon/socket_unix.go` | `internal/mediaapi/workerclient` | design reference | 13 | planned |
| `backend/daemon/server_connection.go` | `internal/mediaapi/workerclient` | design reference | 13 | planned |

### 2.3 Not taken

Not ported and not used as a reference, so not covered here: the Windows process
lock files (`backend/processlock/lock_windows.go`, `process_windows.go`; butterdbd
targets Linux and macOS), the Wails shell and Svelte frontend, mobile builds, the
updater, biometrics, the folder namespace, trash and revisions, TDX1 multipart
and the resumable upload journal, the passphrase vault, shared drives, mount and
WebDAV, HLS and media streaming, and the daemon socket protocol
([plan.md](../plan.md) §1, [tdrive-port-map.md](../reference/tdrive-port-map.md) §8).

When this section and the port map disagree about which files were ported, the
lane reports win and both files are corrected in the integration commit.

---

## 3. Third-party software

### 3.1 Linked into `butterdbd` (root module, `CGO_ENABLED=0`)

`Checked` means the license file was read in the local module cache at the
version shown; that version is the one TDrive pins and may differ from what
butterDB's `go.mod` ends up requiring. Everything marked **verify before
release** must be confirmed against the exact version in `go.sum`.

| Module | Used for | License | Version seen | Status |
| --- | --- | --- | --- | --- |
| Go standard library and runtime | Everything | BSD-3-Clause (Go license) | Go 1.26 | Checked by policy; verify before release |
| `github.com/gotd/td` | MTProto client, only in `internal/tgclient` (E13) | MIT, Copyright (c) 2020 Aleksandr Razumov | v0.145.1 | Checked |
| `modernc.org/sqlite` | SQLite driver, only in `internal/storage` (E13) | BSD-3-Clause, Copyright (c) 2017 The Sqlite Authors; embeds SQLite, which is in the public domain | v1.45.0 | Checked |
| `modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory` | `modernc.org/sqlite` runtime | BSD-3-Clause; `modernc.org/libc` also carries code under the licenses of its upstream C sources (musl, MIT) | v1.67.6, v1.7.1, v1.11.0 | Checked header; verify before release |
| `golang.org/x/crypto` | XChaCha20-Poly1305, HKDF | BSD-3-Clause, Copyright 2009 The Go Authors | v0.57.0 | Checked |
| `golang.org/x/sync`, `golang.org/x/sys`, `golang.org/x/net` | Semaphores and single-flight, OS calls, gotd networking | BSD-3-Clause, Copyright 2009 The Go Authors | v0.23.0, v0.48.0, v0.60.0 | Checked |
| `golang.org/x/time` | E10 send-rate limiter (`golang.org/x/time/rate`), only in `internal/tgclient/flow` | BSD-3-Clause, Copyright 2009 The Go Authors | v0.16.0 | Checked |
| `golang.org/x/text`, `golang.org/x/term` | gotd and `setup` TTY input | BSD-3-Clause | as resolved | Verify before release |
| `github.com/BurntSushi/toml` | Configuration file ([operations.md](../spec/operations.md) §2.1) | MIT, Copyright (c) 2013 TOML authors | v1.6.0 | Checked; adoption is a Default (owner may change) |
| `github.com/prometheus/client_golang` | `/metrics` ([operations.md](../spec/operations.md) §15) | Apache-2.0, with a `NOTICE` file reproduced in §3.5 | v1.23.2 | Checked; adoption is a Default (owner may change) |
| `github.com/prometheus/client_model`, `github.com/prometheus/common`, `github.com/prometheus/procfs`, `github.com/beorn7/perks`, `github.com/cespare/xxhash/v2`, `google.golang.org/protobuf` | `client_golang` dependencies | Apache-2.0 (Prometheus modules), MIT (`perks`, `xxhash`), BSD-3-Clause (`protobuf`) | as resolved | Verify before release |
| `github.com/go-faster/errors` | gotd dependency | BSD-3-Clause | v0.7.1 | Checked |
| `github.com/go-faster/jx` | gotd dependency | MIT | v1.2.0 | Checked |
| `github.com/go-faster/xor` | gotd dependency | BSD-3-Clause | v1.0.0 | Checked |
| `github.com/gotd/ige` | gotd dependency | MIT | v0.2.2 | Checked |
| `github.com/gotd/neo` | gotd dependency | BSD-3-Clause | v0.1.5 | Checked |
| `github.com/klauspost/compress` | gotd dependency | BSD-3-Clause, with some subpackages under Apache-2.0 or MIT | v1.18.5 | Checked header; verify before release |
| `github.com/cenkalti/backoff/v4` | gotd dependency | MIT | v4.3.0 | Checked |
| `github.com/coder/websocket` | gotd dependency | ISC | v1.8.14 | Checked |
| `go.uber.org/zap`, `go.uber.org/atomic`, `go.uber.org/multierr` | gotd dependencies | MIT | v1.27.1, v1.11.0, v1.11.0 | Checked |
| `go.opentelemetry.io/otel` and its `metric` and `trace` modules | gotd dependencies | Apache-2.0 | v1.42.0 | Checked; verify any `NOTICE` before release |
| `github.com/google/uuid` | Job IDs; gotd and sqlite dependency | BSD-3-Clause | v1.6.0 | Checked |
| `github.com/dustin/go-humanize` | sqlite dependency | MIT | v1.0.1 | Checked |

Test-only modules are not distributed in any binary and need no notice:
`github.com/aws/aws-sdk-go-v2/...` (Apache-2.0, `test/s3compat`),
`github.com/stretchr/testify` and other test requirements of dependencies.

### 3.2 Admin GUI (`apps/admin`, cgo)

| Component | Used for | License | Status |
| --- | --- | --- | --- |
| `fyne.io/fyne/v2` | GUI toolkit (D13) | BSD-3-Clause | Verify before release |
| `fyne.io/systray`, `github.com/go-gl/glfw/v3.3/glfw`, `github.com/go-gl/gl` and the rest of Fyne's module graph | Fyne dependencies | BSD-3-Clause, MIT and others per module | Verify before release |
| GLFW (C, vendored by `go-gl/glfw`) | Window and input | zlib/libpng license | Verify before release |
| `github.com/zalando/go-keyring` (minimum v0.2.4, [admin-gui.md](../spec/admin-gui.md)) and its dependencies `github.com/godbus/dbus/v5` (Linux Secret Service) and `github.com/danieljoos/wincred` (Windows) | Device token storage in the OS keychain | MIT (go-keyring, wincred), BSD-2-Clause (godbus/dbus) | Verify before release |
| `golang.org/x/image` | WebP preview decoding ([security.md](../spec/security.md) §12.5) | BSD-3-Clause | Verify before release |
| Space Mono Bold (`apps/admin/internal/shell/fonts/SpaceMono-Bold.ttf`), embedded | Headings, section labels and button labels (D22) | SIL Open Font License 1.1, Copyright 2016 The Space Mono Project Authors; licence text in [`docs/legal/fonts/SpaceMono-OFL.txt`](fonts/SpaceMono-OFL.txt). Source https://github.com/google/fonts/tree/main/ofl/spacemono, font revision 1.003 | Checked 2026-11-16 |
| Inter Regular (`apps/admin/internal/shell/fonts/Inter-Regular.ttf`), embedded | Body text and form input (D22) | SIL Open Font License 1.1, Copyright 2016 The Inter Project Authors; licence text in [`docs/legal/fonts/Inter-OFL.txt`](fonts/Inter-OFL.txt). Source https://github.com/rsms/inter/releases/tag/v4.1 (`extras/ttf`), font version 4.001 | Checked 2026-11-16 |
| JetBrains Mono NL Regular (`apps/admin/internal/shell/fonts/JetBrainsMonoNL-Regular.ttf`), embedded | Numbers, sizes, IDs, keys, fingerprints, timestamps and badges (D22) | SIL Open Font License 1.1, Copyright 2020 The JetBrains Mono Project Authors; licence text in [`docs/legal/fonts/JetBrainsMono-OFL.txt`](fonts/JetBrainsMono-OFL.txt). Source https://github.com/JetBrains/JetBrainsMono, version 2.304 (copied from the distribution package `ttf-jetbrains-mono` 2.304) | Checked 2026-11-16 |
| `github.com/PRIEYAN/ButterDB/api/adminv1` | Admin API client | butterDB's license | n/a |

### 3.3 Transform worker (`apps/transformd`, cgo; phase 13)

| Component | How it is used | License | Status |
| --- | --- | --- | --- |
| `github.com/davidbyttow/govips/v2` | Go binding to libvips | MIT | Verify before release |
| libvips | Image processing; **dynamically linked into `butterdb-transformd` only**, never into `butterdbd` (E14). The LGPL lets users replace the shared library; the worker image keeps it as a separate `.so` and the source offer below applies | LGPL-2.1-or-later | Verify before release |
| libheif (`libheif1`) and its plugins `libheif-plugin-libde265`, `libheif-plugin-aomenc`, `libheif-plugin-dav1d` | HEIC and AVIF decoding and AVIF encoding through libvips, dynamically linked and loaded as plugins | LGPL-3.0-or-later (library and plugin wrappers) | Verify before release |
| libde265 | HEVC decoding for HEIC, dynamically linked | LGPL-3.0-or-later | Verify before release |
| libaom, dav1d, libwebp, libjpeg-turbo, libspng or libpng, libexif, libexpat, glib | libvips codecs and runtime as packaged by Debian trixie | BSD-2-Clause (libaom, dav1d), BSD-3-Clause (libwebp), IJG and BSD-3-Clause (libjpeg-turbo), BSD-2-Clause (libspng) or libpng license, LGPL-2.1-or-later (libexif, glib), MIT (expat) | Verify before release against the image's package list |
| ffmpeg | Poster frames; **invoked as a separate executable**, never linked. Its license depends on the build: LGPL-2.1-or-later for a default build, GPL-2.0-or-later when built with `--enable-gpl` (distribution packages usually are). Running it as a separate program keeps butterDB's binaries out of its license scope; the worker image redistributes it, so the image must ship its license and a source offer | LGPL-2.1-or-later or GPL-2.0-or-later, per build | Verify before release: record the exact `ffmpeg -version` configuration line |

Source offer for LGPL and GPL components in the worker image: the image is built
from Debian trixie packages at pinned versions (`deploy/transformd/Dockerfile`,
[media-api.md](../spec/media-api.md) §21.1: `libvips42t64`, `libheif1`,
`libheif-plugin-libde265`, `libheif-plugin-aomenc`, `libheif-plugin-dav1d`,
`ffmpeg`);
the corresponding source is the distribution's source packages for those
versions, and the release notes name them. Default (owner may change).

### 3.4 Container images

| Image | Contents | License | Status |
| --- | --- | --- | --- |
| `gcr.io/distroless/static-debian12:nonroot` | Base of the server image: CA certificates, tzdata, `/etc/passwd` | Distroless project Apache-2.0; contained Debian packages under their own licenses (MPL-2.0 for `ca-certificates` data, public domain for `tzdata`) | Verify before release |
| `golang:1.26` | Build stage only; not shipped | BSD-3-Clause and Debian packages | n/a |
| `debian:trixie-slim` | Runtime for `butterdb-transformd` and the libraries of §3.3 | Per package; `/usr/share/doc/*/copyright` stays in the image | Verify before release |
| `golang:1.26-trixie` | Worker build stage only (`libvips-dev`, `pkg-config`); not shipped | BSD-3-Clause and Debian packages | n/a |

### 3.5 NOTICE reproduced for `github.com/prometheus/client_golang`

```text
Prometheus instrumentation library for Go applications
Copyright 2012-2015 The Prometheus Authors

This product includes software developed at
SoundCloud Ltd. (http://soundcloud.com/).


The following components are included in this product:

perks - a fork of https://github.com/bmizerany/perks
https://github.com/beorn7/perks
Copyright 2013-2015 Blake Mizerany, Björn Rabenstein
See https://github.com/beorn7/perks/blob/master/README.md for license details.

Go support for Protocol Buffers - Google's data interchange format
http://github.com/golang/protobuf/
Copyright 2010 The Go Authors
See source code for license details.
```

Copied from the module cache at v1.23.2; re-copy if the required version's
`NOTICE` differs.

---

## 4. Release checklist for this file

1. Every `planned` row in §2 for a merged phase reads `ported <hash>`,
   `referenced <hash>` or `not used: <reason>`.
2. For each shipped binary, list the modules actually compiled in:
   `go version -m bin/butterdbd`, and the same for `butterdb-admin` packages and
   `butterdb-transformd`. Every module printed appears in §3 with a license.
3. Generate the full license texts for the release archive with
   `go run github.com/google/go-licenses@<pinned version> save ./cmd/butterdbd --save_path=dist/licenses/butterdbd`
   (and for `apps/admin` and `apps/transformd`). `go-licenses` is Apache-2.0 and
   runs at release time only. Default (owner may change).
4. Clear every **verify before release** mark in §3 against the exact versions
   in `go.sum` and the worker image's package list.
5. Confirm no dependency is under a license incompatible with the project
   license chosen at the phase 01 owner gate (A10); a GPL library may never be
   linked into `butterdbd` or the admin GUI.
