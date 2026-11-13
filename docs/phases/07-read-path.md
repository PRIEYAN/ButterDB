# Phase 07 — Read path and origin cache
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement read path and origin cache as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 06 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/storage-model.md`.
- `docs/spec/s3-api.md`.
- `docs/spec/operations.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit the object-reader surface below before fan-out. It accepts the live
`object.ObjectMeta` from phase 03, so it never chooses an older version by key.
`Open` returns plaintext for TDE1 objects. `ReadAt` follows `io.ReaderAt`
semantics and honors the context on every Telegram request; the returned
`io.Closer` releases any session resources. L4 applies HTTP `Range` and
conditional headers using `docs/spec/s3-api.md` §8, without reaching Telegram
when a condition fails.

```go
package objectreader

import (
	"context"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

type Opened interface {
	ReadAt(ctx context.Context, dst []byte, off int64) (int, error)
	Close() error
}

type Service interface {
	Open(ctx context.Context, meta object.ObjectMeta) (Opened, error)
}

type Source interface {
	ReadDocumentRange(ctx context.Context, meta object.ObjectMeta, offset int64, dst []byte) (int, error)
}

```

The cache key includes the bucket channel, body message ID, block offset,
and stored-byte length. That prevents an overwrite from returning bytes from
an older body. L1 ports TDrive's coalescing range reader; L2 owns the
write-through disk block cache; L3 composes cache, range transport, and TDE1
random-access decryption; L4 owns HTTP range and condition parsing. The
configuration values belong to `docs/spec/operations.md`. Commit as
`define read path contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/07-read-path-and-origin-cache-l1` | internal/readpath/rangereader (port TDrive backend/media range reader) |
| L2 | `phase/07-read-path-and-origin-cache-l2` | internal/readpath/blockcache |
| L3 | `phase/07-read-path-and-origin-cache-l3` | internal/readpath/objectreader |
| L4 | `phase/07-read-path-and-origin-cache-l4` | internal/readpath/httprange |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — internal/readpath/rangereader (port TDrive backend/media range reader)

Implement internal/readpath/rangereader (port TDrive backend/media range reader). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/media/range_reader.go` | 753 | PORT | `internal/readpath/rangereader` (L1) | Keep: `RangeReaderConfig`, `RangeReader`, block-aligned fetches of `RangeReadMaxBytes`, single-flight per block shared by concurrent readers, foreground joining or promoting a background flight, read-ahead window cancelled on seek, `openingChunkBytes = 256 * 1024` prefix on first read, `defaultBackgroundSlots = 4`, `defaultRangeCacheBytes = 64 * 1024 * 1024`, `defaultRangeRetryPolicy` (5 flood retries, 2 min max, 5 min total; 3 transient retries, 250 ms backoff to 3 s, 100 ms jitter), short-read retry, `ReaderAt` EOF semantics, `RangeStats`. Change: the client port (`ResolveDocument`, `ReadDocumentRange`) is declared here; shutdown context comes from `Start`, not a config field; clock injected |
| `backend/media/cache.go` | 81 | PORT | `internal/readpath/rangereader` (L1) | In-memory LRU of blocks with a byte budget, oversized-entry rejection |

Commit this lane as `implement phase 07 internal readpath rangereader port tdrive bac` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/media/range_reader.go`: `type RangeReaderConfig struct {`; `type RangeReader struct {`; `type RangeStats struct {`; `func NewRangeReader(cfg RangeReaderConfig) *RangeReader {`; `func (r *RangeReader) Close() {`; `func (r *RangeReader) Throughput() ThroughputStats {`; `func (r *RangeReader) Stats() RangeStats {`; `func (r *RangeReader) ReadStoredAt(ctx context.Context, ref tgclient.DocumentRef, p []byte, off int64) (int, error) {`

### 5.2 Lane L2 — internal/readpath/blockcache

Implement internal/readpath/blockcache. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/thumbnail/cache.go` | 535 | PATTERN-ONLY | `internal/readpath/blockcache` (L2) | Disk LRU: SHA-256 keyed sharded paths, byte budget plus entry cap (`defaultMaxCacheEntries = 64 << 10`), temp-then-rename writes, index rebuilt by scanning on first use, stray temp cleanup, modes `0o700` / `0o600`, a nil cache is a disabled cache. butterDB caches stored bytes (ciphertext for encrypted objects) keyed by bucket channel, body message ID and block index; bodies are immutable, so overwrite needs no invalidation, only eviction |

Commit this lane as `implement phase 07 internal readpath blockcache` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/thumbnail/cache.go`: `type Cache struct {`; `func NewCache(dir string, maxBytes int64) *Cache {`; `func (c *Cache) Get(key string) ([]byte, bool) {`; `func (c *Cache) GetLimited(key string, limit int64) ([]byte, bool) {`; `func (c *Cache) Has(key string) bool {`; `func (c *Cache) Put(key string, data []byte) error {`; `func (c *Cache) Usage() (used, maxBytes int64, entries int) {`; `func (c *Cache) Clear() error {`

### 5.3 Lane L3 — internal/readpath/objectreader

Implement internal/readpath/objectreader. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/mountcontent/reader.go` | 687 | PORT-PARTIAL | `internal/readpath/objectreader` (L3) | Keep: `Opener` / `Reader` shape, validation that `CiphertextSize(plaintext) == stored` before asking for a key, key requested only for encrypted objects and only after the body resolves, `RandomAccessDecryptor` over the range reader, `ReadAt` / `Size` / `Close`, encrypted readers closed when the opener closes, bounded concurrent resolution. Drop multipart segments |
| `backend/mountcontent/document_cache.go`, `document_resolution.go`, `peer_cache.go` | 85, 75, 88 | PORT | `internal/readpath/objectreader` (L3) | `DocumentRef` cache with TTL and defensive copies, keyed by the full projected identity; size validated before caching; coalesced resolution where each waiter cancels independently and an abandoned load never caches a late success; peer cache |
| `backend/mountcache/lru.go` | 192 | PORT | `internal/readpath/objectreader` (L3) | Generic weighted LRU with TTL used by the caches above |
| `backend/mountcache/coalescer.go` | 362 | PORT | `internal/readpath/objectreader` (L3) | Generic cancellable load coalescer with invalidation, used by document resolution. Phase 13 needs the same algorithm (see phase 13) |
| `backend/media/resolver.go`, `service.go`, `types.go` | 120, 431, 114 | PATTERN-ONLY | `internal/readpath/objectreader` (L3) | `Resolve` then `Open`: projected row to document refs to reader; `MasterKeyProvider` as a consumer port |

Commit this lane as `implement phase 07 internal readpath objectreader` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/mountcontent/reader.go`: `type PeerResolver interface {`; `type MasterKeyProvider interface {`; `func (provider MasterKeyProviderFunc) MasterKey(ctx context.Context, channelID int64) ([]byte, error) {`; `type Config struct {`; `type Opener struct {`; `func New(cfg Config) (*Opener, error) {`; `func (o *Opener) Close() {`; `func (o *Opener) Open(ctx context.Context, channelID, fileID int64) (*Reader, error) {`
- `backend/mountcache/lru.go`: `func NewLRU[K comparable, V any](config LRUConfig[V]) *LRU[K, V] {`; `func (cache *LRU[K, V]) Get(key K) (V, bool) {`; `func (cache *LRU[K, V]) Put(key K, value V) bool {`; `func (cache *LRU[K, V]) Delete(key K) (V, bool) {`; `func (cache *LRU[K, V]) Clear() {`; `func (cache *LRU[K, V]) Len() int {`; `func (cache *LRU[K, V]) Weight() int {`
- `backend/mountcache/coalescer.go`: `func NewCoalescer[K comparable, V any](maxConcurrent int) *Coalescer[K, V] {`; `func (loads *Coalescer[K, V]) Load(`; `func (loads *Coalescer[K, V]) Invalidate(`; `func (loads *Coalescer[K, V]) InvalidateAll(terminalErr error, evict func()) {`; `func (loads *Coalescer[K, V]) Close(terminalErr error) {`; `func (loads *Coalescer[K, V]) Waiters(key K) int {`; `func (loads *Coalescer[K, V]) Len() int {`
- `backend/media/resolver.go`: `type Resolver struct {`; `func NewResolver(db *sql.DB) *Resolver {`; `func (r *Resolver) Resolve(ctx context.Context, channelID, fileID int64) (LogicalFile, error) {`

### 5.4 Lane L4 — internal/readpath/httprange

Implement internal/readpath/httprange. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/media/server.go` (`parseRangeHeader`, `streamSessionRange`, range response writing) | 502 | PORT-PARTIAL | `internal/readpath/httprange` (L4) | Keep: single-range forms `bytes=a-b`, `bytes=a-`, `bytes=-n`; `Accept-Ranges: bytes`; `206` with `Content-Range: bytes a-b/size`; `416` with `Content-Range: bytes */size`; chunked streaming with a buffer pool (256 KiB chunks). Drop: session tokens, CORS headers, playback updates, thumbnails, idle sweep |
| `backend/mountdav/conditions.go` (`parseETagConditions`, `consumeEntityTag`, `validStrongETag`, `maxConditionalHeaderBytes`) | 318 | PORT-PARTIAL | `internal/readpath/httprange` (L4) | `If-Match` / `If-None-Match` entity-tag list parsing with a 16 KiB header cap. Drop the WebDAV `If` grammar (`parseDAVIf`, lexer) |
| `backend/mountadapter/conditions.go` | 121 | PATTERN-ONLY | `internal/readpath/httprange` (L4) | `matchesIfMatch` / `matchesIfNoneMatch` evaluation against the current entity. butterDB adds `If-Modified-Since` / `If-Unmodified-Since` and `304` |

Commit this lane as `implement phase 07 internal readpath httprange` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/media/server.go`: `type PlaybackUpdate struct {`; `type Server struct {`; `func NewServer(owner *Service) *Server {`; `func (s *Server) Add(session *Session) error {`; `func (s *Server) CloseSession(token string) error {`; `func (s *Server) UpdatePlayback(update PlaybackUpdate) error {`; `func (s *Server) Stats(token string) MediaStats {`; `func (s *Server) Close() error {`

## 6. Integration lane

core wiring, test/acceptance/phase07 (ranged reads across encrypted chunks, cache hit without Telegram, conditional requests)
Own `internal/core`, `cmd/`, and `test/acceptance/phase07` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase07/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestEncryptedRangeCrossingChunksReturnsExactBytes`: encrypted range crossing chunks returns exact bytes.
  - `TestCacheHitMakesZeroTelegramCalls`: cache hit makes zero Telegram calls.
  - `TestConditionalGETAndInvalidRangeFollowHTTPRules`: conditional GET and invalid range follow HTTP rules.
### TDrive regression tests to adapt

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

## 8. Owner gates

None beyond the entry criteria and any explicit gates recorded in the applicable specification.

## 9. Exit criteria and handoff

All L1–L4 commits have passed monitoring and review; the integration build and phase 07 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 08.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding behavior for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/s3-api.md`

## 8. Object operations

### 8.1 PutObject

Request headers:

| Header | Rule |
| --- | --- |
| `Content-Length` or `x-amz-decoded-content-length` | Required (§4.4). Stored size (`format.CiphertextSize` when encrypting) above `limits.MaxStoredObjectBytes`: `400 EntityTooLarge` with `<ProposedSize>` and `<MaxSizeAllowed>1992294400</MaxSizeAllowed>`, before the body is read |
| `Content-Type` | Stored. Absent: `application/octet-stream` (AWS stores `binary/octet-stream`; deviation, §17). At most 1024 bytes |
| `Cache-Control`, `Content-Disposition`, `Content-Encoding` (minus `aws-chunked`), `Content-Language`, `Expires` | Stored verbatim and returned on GET and HEAD. Each at most 1024 bytes; over: `400 InvalidArgument` |
| `x-amz-meta-*` | User metadata (§8.7) |
| `Content-MD5`, `x-amz-checksum-*` | §4.8 |
| `x-amz-server-side-encryption` | §8.10 |
| `x-amz-acl` | §10.2 |
| `x-amz-storage-class` | `STANDARD` or absent |
| `If-None-Match` | `*` only: write only if the key does not exist; else `412 PreconditionFailed`. Any other value: `400 InvalidArgument` |
| `If-Match` | Write only if the current object's ETag matches; no object or another ETag: `412 PreconditionFailed` |
| `Expect: 100-continue` | Honoured; Go sends `100 Continue` on the first body read, which happens only after step 9 of §5 |

Flow: plan §7.1. Write preconditions are checked twice: once before
spooling, so an obvious failure costs no upload, and authoritatively inside
`ingest/put` under the per-key commit lock immediately before the `t=put`
send, which is the same mechanism the admin API's upload uses
([admin-api.md](../spec/admin-api.md) §12.4). A failure at the second check deletes
the already-sent body message and returns `412`.

Response `200`, empty body, headers: `ETag: "<md5 hex>"`,
`x-amz-server-side-encryption: AES256` when encrypted, the echoed
`x-amz-checksum-<alg>` when one was supplied. No `x-amz-version-id`.

A client that disconnects after the body is complete does not cancel the
commit; the object appears (plan §7.1 failure table). A client that
disconnects before the body is complete leaves nothing in Telegram.

Zero-byte objects and keys ending in `/` (folder markers) are ordinary
objects.

### 8.2 GetObject

Served by `internal/readpath/httprange` and `objectreader` (plan §7.2).

**Range.**

| `Range` header | Response |
| --- | --- |
| Absent | `200`, full object |
| `bytes=a-b`, `bytes=a-`, `bytes=-n`, satisfiable | `206` with `Content-Range: bytes a-b/size`; `b` past the end is clamped; `-n` larger than the object returns the whole object as `206` |
| Single range, unsatisfiable (`a >= size`, or any range on a zero-byte object) | `416 InvalidRange` with `Content-Range: bytes */size` and `<RangeRequested>` and `<ActualObjectSize>` elements |
| More than one range (`bytes=0-1,5-6`) | `200`, full object, as S3 does (S3 does not support multipart byteranges) |
| Syntactically invalid, or a unit other than `bytes` | Ignored; `200`, full object (RFC 9110) |

`Accept-Ranges: bytes` is always sent. Ranges are over plaintext.
`readpath/httprange` ports `parseRangeHeader` from TDrive
`backend/media/server.go` and changes two behaviours: a multi-range header
yields the full object instead of an error, and an invalid header is ignored
instead of answered with `416`.

**`partNumber`.** `partNumber=1` returns the whole object as `206` with
`Content-Range` covering it, without `x-amz-mp-parts-count`. Any other part
number returns `416 InvalidPartNumber`. Part boundaries of multipart uploads
are not kept after assembly. With no `x-amz-mp-parts-count`, the
aws-sdk-go-v2 transfer manager treats the object as one part. `partNumber`
together with `Range`: `400 InvalidRequest`.

**Conditionals**, evaluated before `Range`, in this order:

| Step | Header | Outcome |
| --- | --- | --- |
| 1 | `If-Match` | ETag not matched (strong comparison; `*` matches any existing object): `412 PreconditionFailed`. When present and matched, step 2 is skipped |
| 2 | `If-Unmodified-Since` | `Last-Modified` after the date: `412 PreconditionFailed` |
| 3 | `If-None-Match` | ETag matched (weak comparison; `*` matches): `304 Not Modified`. When present and not matched, step 4 is skipped |
| 4 | `If-Modified-Since` | `Last-Modified` not after the date: `304 Not Modified` |
| 5 | `If-Range` | ETag (strong) or HTTP-date equal to the object's: honour `Range`; otherwise ignore `Range` and return `200` |

This yields the two cases AWS documents: `If-Match` true with
`If-Unmodified-Since` false returns `200`; `If-None-Match` false with
`If-Modified-Since` true returns `304`.

- ETags in conditions are accepted with or without quotes; AWS accepts both.
  TDrive's `consumeEntityTag` (`backend/mountdav/conditions.go`) requires
  quotes and is adapted accordingly.
- Dates are parsed with `http.ParseTime`. An unparseable date, or a date in
  the future, makes that header ignored.
- Comparison is at one-second precision.
- `304` carries `ETag`, `Last-Modified`, `Cache-Control`, `Expires` and no
  body. `412` is an S3 error XML (`PreconditionFailed`, `<Condition>` naming
  the header).

**Response overrides.** Signed and presigned requests only (§4.11):

| Query | Overrides |
| --- | --- |
| `response-content-type` | `Content-Type` |
| `response-content-language` | `Content-Language` |
| `response-expires` | `Expires` |
| `response-cache-control` | `Cache-Control` |
| `response-content-disposition` | `Content-Disposition` |
| `response-content-encoding` | `Content-Encoding` |

Each value is at most 1024 bytes and must contain no control characters
(`400 InvalidArgument`). Overrides apply to `200` and `206` responses.

**Response headers.** `Content-Type`, `Content-Length` (plaintext bytes
sent), `Content-Range` (on `206`), `ETag`, `Last-Modified`, `Accept-Ranges`,
the stored system headers of §8.1, `x-amz-meta-*` (§8.7),
`x-amz-server-side-encryption: AES256` when encrypted, `Cache-Control` per
§13.6. Never sent: `x-amz-version-id`, `x-amz-storage-class` (S3 omits it for
`STANDARD`), `x-amz-tagging-count`, checksum headers.

**Read failures.** If no body byte can be produced within
`s3.request_budget` because of `getFile` slot waits or a Telegram flood wait,
the response is `503 SlowDown` with `Retry-After` (the remaining flood wait,
rounded up, at least 1 s). A non-transient Telegram failure before
headers is `500 InternalError` (from phase 12, the replica is tried first).
After headers are sent, any failure, including a TDE1 chunk authentication
failure, aborts the connection; the client sees a short body.

### 8.3 HeadObject

Same as GetObject without a body: same conditionals (`304`, `412`), same
`Range` and `partNumber` handling (a satisfiable range returns `206` with
`Content-Range` and the range's `Content-Length`), same headers. Errors have
no body: the status line and `x-amz-request-id` only. `response-*` overrides
are honoured.

### 8.4 DeleteObject

Flow: plan §7.3. `204 No Content`, also when the key does not exist (no
Telegram traffic then). `versionId=null` is accepted. A tombstone send whose
limiter or flood wait does not fit `s3.request_budget` returns
`503 SlowDown`; nothing was
committed and the object still exists. An unknown tombstone send outcome
returns `503 SlowDown`; the sweeper reconciles and the client's retry is
idempotent.

### 8.5 DeleteObjects

`POST /<b>?delete`, body:

```xml
<Delete xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Quiet>true</Quiet>
  <Object><Key>2026/a.jpg</Key></Object>
  <Object><Key>2026/b.jpg</Key><VersionId>null</VersionId></Object>
</Delete>
```

| Rule | Failure |
| --- | --- |
| Body at most 2 MiB | `400 MaxMessageLengthExceeded` |
| Empty body | `400 MissingRequestBodyError` |
| 1 to 1000 `Object` entries | `400 MalformedXML` |
| Namespace optional; DOCTYPE or unknown root | `400 MalformedXML` |
| `Content-MD5` or `x-amz-checksum-*` | Verified when present; not required (§4.8) |

Per-key handling, in body order, through the same path as DeleteObject,
batching Telegram message deletions across keys (plan §7.3):

| Per-key situation | Result entry |
| --- | --- |
| Deleted, or did not exist | `Deleted` (omitted in quiet mode) |
| Key fails validation (§8.7) | `Error` with `InvalidArgument` or `KeyTooLongError` |
| `VersionId` other than `null` | `Error` with `InvalidArgument` |
| Key lacks the `delete` action | `Error` with `AccessDenied` |
| Limiter or flood wait past the remaining `s3.request_budget` | `Error` with `SlowDown` for this and every later key |
| Unknown send outcome or internal failure | `Error` with `SlowDown` or `InternalError` |

```xml
<?xml version="1.0" encoding="UTF-8"?>
<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Deleted><Key>2026/a.jpg</Key></Deleted>
  <Error><Key>2026/b.jpg</Key><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>
</DeleteResult>
```

The status is `200` whenever the request itself is valid, whatever the
per-key results. Quiet mode lists only `Error` entries.

The whole request shares one `s3.request_budget`. Each key costs one
`t=del` tombstone, a `message`-class send under the E10 limiter (20 per minute,
burst 5, by default; [operations.md](../spec/operations.md) §13.3), so one request
deletes about 15 existing keys before the budget runs out at the defaults. Keys
that do not exist cost nothing and always report `Deleted`. Every key after the
budget is spent reports `SlowDown`; clients re-send those keys. The AWS CLI
(`aws s3 rm --recursive`) prints `delete failed` for them and exits `1`, and a
second run continues. Raising the limiter is an owner decision based on
`tgprobe` results.

### 8.6 CopyObject

Decision: CopyObject is supported as a server-side read of the source through
`readpath/objectreader` (decrypting when needed) streamed into a new put job
through `ingest/spool` and `ingest/put` (re-encrypting under the destination's
encryption), never as a Telegram forward of the source document. Every copy is
a full new object with its own body and put messages.

| Header | Rule |
| --- | --- |
| `x-amz-copy-source` | `<bucket>/<key>` or `/<bucket>/<key>`, URL-encoded, optional `?versionId=null`. Malformed: `400 InvalidArgument`. Source bucket outside scope or without `read`: `403 AccessDenied` |
| `x-amz-metadata-directive` | `COPY` (default): content type, system headers and user metadata come from the source. `REPLACE`: from this request. Other: `400 InvalidArgument` |
| `x-amz-tagging-directive` | `COPY` or `REPLACE` with an empty `x-amz-tagging`: accepted. Tags: `501` |
| `x-amz-copy-source-if-match`, `-if-none-match`, `-if-modified-since`, `-if-unmodified-since` | Evaluated against the source as §8.2; failure is `412 PreconditionFailed` (S3 uses `412` for all four on copy, not `304`) |
| `x-amz-server-side-encryption` | Destination encryption; absent means the destination bucket's default. The source's encryption is never inherited, as in S3 |
| `If-Match`, `If-None-Match` (destination) | `501 NotImplemented` in v1 |
| `x-amz-copy-source-server-side-encryption-customer-*` | `501 NotImplemented` |

Rules:

- Copying an object onto itself with `COPY` directive and no change to
  encryption returns `400 InvalidRequest`, `This copy request is illegal
  because it is trying to copy an object to itself without changing the
  object's metadata, storage class, website redirect location or encryption
  attributes.` (S3 behaviour.)
- The destination's stored size is computed before any read: source
  plaintext size, through `format.CiphertextSize` when the destination is
  encrypted. Over the limit: `400 EntityTooLarge`. A plaintext source just
  under 1900 MiB copied into an encrypted destination can fail this check
  (claude.md §10 invariant 3).
- The destination's put record cap (§8.7) is checked before any read.
- The new ETag is the MD5 of the content, computed by the spool, even when
  the source has a multipart ETag; S3 also returns a non-multipart ETag for a
  single CopyObject of a multipart source.

Response: once validation passes, the long-response protocol of §11 starts
(`200`, XML declaration, whitespace every `s3.keepalive_interval`). Success
body:

```xml
<CopyObjectResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <ETag>"3858f62230ac3c915f300c664312c11f"</ETag>
  <LastModified>2026-11-13T10:02:11.000Z</LastModified>
</CopyObjectResult>
```

Failure after the `200` status: an `Error` document in the body (§11).
`x-amz-server-side-encryption` is sent with the `200` headers, because the
destination encryption is decided during validation.

### 8.7 Keys and metadata

**Keys.**

| Rule | Failure |
| --- | --- |
| Valid UTF-8 | `400 InvalidArgument`, `Object key is not valid UTF-8` |
| 1 to 1024 bytes | Empty: bucket operation; longer: `400 KeyTooLongError`, `Your key is too long` with `<Size>` and `<MaxSizeAllowed>1024</MaxSizeAllowed>` |
| No U+0000 to U+001F, no U+007F | `400 InvalidArgument`, `Object key contains a control character` (§7.5) |

**User metadata.**

- Request headers `x-amz-meta-<name>`. Names are stored lower case, without
  the prefix. Repeated headers with the same name are joined with `,`.
- Values encoded as RFC 2047 (`=?UTF-8?B?...?=` or `=?UTF-8?Q?...?=`) are
  decoded with `mime.WordDecoder` before storing. On GET and HEAD, a value
  containing non-ASCII bytes is returned RFC 2047 B-encoded with
  `mime.BEncoding`, which is what S3 does.
- Size: the sum over entries of the UTF-8 byte length of the name (without
  `x-amz-meta-`) and of the value, after decoding, must be at most 2048 bytes
  (`limits.MaxUserMetadataBytes`). Over: `400 MetadataTooLarge` with `<Size>`
  and `<MaxSizeAllowed>2048</MaxSizeAllowed>`. System headers (§8.1) do not
  count toward the 2048 bytes, as in S3.
- A name must be a valid HTTP token; anything else is `400 InvalidArgument`.

**The put record cap.** An object is committed by one `t=put` Telegram text
message that carries the key, user metadata and system headers, all
URL-escaped (E2, [storage-model.md](../spec/storage-model.md)). Telegram limits a
message to 4096 characters, so the formatted message text, header line plus
any remainder, must satisfy `len(wire.Format(op)) <= limits.MaxPutOpChars`
(4096). The check runs in step 7 of §5, before admission and before any byte
is spooled, for PutObject, CopyObject and CreateMultipartUpload (so
CompleteMultipartUpload cannot fail on it). On failure:

| Condition | Response |
| --- | --- |
| The op formatted without user metadata and system headers already exceeds 4096 | `400 KeyTooLongError` |
| Otherwise | `400 MetadataTooLarge`, message `The encoded object record is <n> characters; the limit is 4096`, with `<Size>` and `<MaxSizeAllowed>4096</MaxSizeAllowed>` |

Escaping makes each byte outside `A-Z a-z 0-9 - _ . ~` cost three characters,
so a 1024-byte key of non-ASCII text alone uses 3072 characters and keys with
many `/` grow by two characters per slash. What clients can rely on: a key of
up to 1024 bytes and user metadata of up to 2048 bytes always fit when both,
plus the system headers, consist of characters from `A-Z a-z 0-9 - _ . ~`,
there are at most 16 metadata entries, and the system headers total at most
384 bytes. This guarantee requires that [storage-model.md](../spec/storage-model.md)
keeps the fixed part of the `t=put` op at most 512 characters and the
per-entry metadata overhead at most 4 characters. Deviation (§17, plan R4).

### 8.8 ETag rules

| Object origin | ETag |
| --- | --- |
| PutObject | `"<32 lowercase hex MD5 of the plaintext>"` |
| CompleteMultipartUpload | `"<32 lowercase hex MD5 of the concatenated 16-byte binary MD5s of the listed parts, in order>-<part count>"` |
| CopyObject | `"<MD5 of the plaintext>"` |
| UploadPart, UploadPartCopy | `"<MD5 of the part bytes>"` |

- Encrypted objects (TDE1) have the plaintext MD5 as ETag, as S3 does for
  SSE-S3. The ETag never depends on the ciphertext.
- The `t=put` op stores the ETag string without quotes, including any `-N`
  suffix ([storage-model.md](../spec/storage-model.md)); responses add the quotes.
- ETags in XML (`ETag` elements) and headers are quoted.

### 8.9 Last-Modified

`Last-Modified` is the date of the object's `t=put` message (Telegram's server
time, one-second precision, UTC), the moment of the commit (D8). Two
overwrites in the same second share `Last-Modified` and differ in ETag.

### 8.10 Server-side encryption (D11, A5)

| Request `x-amz-server-side-encryption` | Bucket encryption default | Stored as |
| --- | --- | --- |
| absent | `none` | Plaintext |
| absent | `tde1` | TDE1 |
| `AES256` | either | TDE1 |
| `aws:kms`, `aws:kms:dsse`, other | either | `400 InvalidArgument` (§6.3) |

- Encryption is resolved at PutObject, CopyObject (destination) and
  CreateMultipartUpload; UploadPart and Complete use the upload's decision.
- There is no way to request plaintext storage in a `tde1` bucket, as S3 has
  no such header.
- An `AES256` request in a bucket that has never had an encryption key
  requires the bucket's data encryption key to exist. `internal/buckets`
  creates it on first need with a single `t=encfg` registry commit under a
  per-bucket lock; its waits count against the request budget (§12.1).
- Every response describing an encrypted object (PutObject, GetObject,
  HeadObject, CopyObject, CreateMultipartUpload, UploadPart, UploadPartCopy,
  CompleteMultipartUpload) carries `x-amz-server-side-encryption: AES256`.
  Plaintext objects carry no such header.
- Encryption keys are held by the server (A6); an encrypted object is
  readable by any request authorised to read it.

---
