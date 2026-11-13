# Phase 13 — Media transforms
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement media transforms as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 12 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/media-api.md`.
- `docs/spec/security.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit `api/transformv1` request, response, and error DTOs from `docs/spec/media-api.md` §14, create `apps/transformd/go.mod`, and update `go.work` before fan-out. The worker process and server-side package signatures are in §§14–19. Add the exact govips package `github.com/davidbyttow/govips/v2/vips@v2.20.0` during the prelude. Commit as `define media worker contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/13-media-transforms-l1` | apps/transformd (govips/libvips image ops, HEIC input, WebP/AVIF/JPEG/PNG output, metadata stripping with auto-orient, ffmpeg poster frames) |
| L2 | `phase/13-media-transforms-l2` | internal/mediaapi/{grammar,presets,signing} |
| L3 | `phase/13-media-transforms-l3` | internal/mediaapi/derivcache |
| L4 | `phase/13-media-transforms-l4` | internal/mediaapi/workerclient (process supervision, unix socket) |
| L5 | `phase/13-media-transforms-l5` | internal/mediaapi/handlers |
| L6 | `phase/13-media-transforms-l6` | deploy/transformd/ (image with libvips, libheif, libde265, ffmpeg; systemd unit) |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — apps/transformd (govips/libvips image ops, HEIC input, WebP/AVIF/JPEG/PNG output, metadata stripping with auto-orient, ffmpeg poster frames)

**Objective.** Build the `butterdb-transformd` worker: a separate cgo binary that decodes images with libvips (HEIC input included), resizes, crops, pads and orients them, strips metadata, and encodes JPEG, PNG, WebP or AVIF. It extracts video posters through an ffmpeg subprocess, speaks `api/transformv1` over a Unix socket, and sandboxes itself.

**Owned paths.** Everything under `apps/transformd/` except `go.mod` and `go.sum`, which the prelude owns: `main.go`, `internal/{server,sniff,pipeline,vipsguard,poster,sandbox}/`, `testdata/` (fixtures and `generate.sh`).

**Read.** `docs/spec/media-api.md` §13 (stripping and orientation), §14 (protocol; the `api/transformv1` code is the prelude's), §16.1–§16.8, §17.1–§17.3, §18.1–§18.2, §22.1 (fixtures), §22.3 (binding worker tests). TDrive patterns: `/home/prieyan/weeb/TDrive/backend/media/image_admission.go` (admission limits checked before decode) and `/home/prieyan/weeb/TDrive/backend/thumbnail/orientation.go` (EXIF orientations 1–8; reuse its cases as auto-orient expectations).

**Contracts.** The worker's only exported surface is its command line (§16.3 flags: `--socket`, `--socket-mode`, `--tmp-dir`, `--ffmpeg`, `--concurrency`, `--memory-limit`, `--sandbox`) and the `transformv1` protocol. Internal packages expose what §16 describes. Import nothing from the root module except `github.com/PRIEYAN/ButterDB/api/transformv1`.

**Rules.**
- Classify sources by magic number (`sniff`), never by extension or content type. The `vipsguard` cgo shim blocks every libvips loader outside the allowlist, so SVG, PDF, TIFF and magick loaders fail even when handed bytes directly.
- Enforce the source pixel limit **before** a full decode, from the header. Bomb fixtures must finish in under 100 ms with under 32 MiB of RSS growth. Enforce the output byte limit too.
- Auto-orient before stripping. Strip all metadata except the ICC profile. Output is deterministic: the same input and spec give identical bytes.
- ffmpeg runs with the exact argument slice of §17.2. Use no shell and no user-controlled arguments, disable network protocols, give it a clean environment and a timeout, and kill its whole process group.
- The socket has mode `0600` (or the `--socket-mode` value). Concurrency is gated, and the worker answers `busy` beyond the limit. A per-job budget watchdog exits the process when a job overruns.
- The sandbox applies rlimits plus Landlock when available, and `--sandbox require` exits if Landlock is missing.

**Tests (binding names).** Every media-api §22.3 test for `internal/sniff`, `internal/pipeline`, `internal/vipsguard`, `internal/poster`, `internal/sandbox` and `internal/server`, including `FuzzClassify`.

**Verify.** This needs libvips ≥ 8.15 headers (`pkg-config vips`) and ffmpeg on the host.

```bash
CGO_ENABLED=1 go -C apps/transformd vet -mod=readonly ./...
CGO_ENABLED=1 go -C apps/transformd test -mod=readonly -race ./...
CGO_ENABLED=1 go -C apps/transformd test -mod=readonly -run '^$' -fuzz FuzzClassify -fuzztime 60s ./internal/sniff
gofmt -l apps/transformd
```

**Commits.** `add transform worker server and framing`, `add source sniffing and libvips loader guard`, `add image transform pipeline with orientation and stripping`, `add ffmpeg poster extraction`, `add worker sandbox and test fixtures`.

### 5.2 Lane L2 — internal/mediaapi/{grammar,presets,signing}

**Objective.** Implement the media URL language: parsing and canonicalising transformation strings, named presets per bucket, the width ladder, and HMAC-signed transformations with expiry.

**Owned paths.** `internal/mediaapi/grammar/`, `internal/mediaapi/presets/`, `internal/mediaapi/signing/`.

**Read.** `docs/spec/media-api.md` §2.2 (per-bucket settings and presets), §3–§9 (grammar, parameters, presets, canonicalisation, signing, width ladder, limits), §19.1–§19.3 (binding contracts), §22.2 (binding test rows for `grammar`, `presets` and `signing`).

**Contracts.** Implement §19.1, §19.2 and §19.3 exactly. `grammar` imports only the standard library and `internal/domain`. `presets` and `signing` import nothing from `internal/mediaapi/handlers`, `derivcache` or `workerclient`.

**Rules.**
- `Parse(Canonical(x))` equals `x` for every valid input. Canonical form is stable and is the only input to cache keys and signatures.
- Reject leading zeros, duplicate keys, unknown keys, empty segments, more than 16 parameters, and strings over 512 bytes.
- Snap widths to the ladder and quality to its steps, and clamp DPR as §4 and §8 specify.
- Signatures use HMAC over the exact §7.2 input, and verification compares in constant time. Accept the previous key during rotation, then zero old keys after rotation. The key file is 43 characters of base64url.
- The §7.3 matrix decides when a signature is required: private buckets always, and arbitrary parameters unless the bucket allows unsigned use. Private URLs need `exp_` within the configured maximum.

**Tests (binding names).** Every media-api §22.2 row for `grammar` (including `FuzzParse`), `presets` and `signing`.

**Verify.**

```bash
GOWORK=off CGO_ENABLED=0 go test -mod=readonly -race ./internal/mediaapi/grammar/... ./internal/mediaapi/presets/... ./internal/mediaapi/signing/...
GOWORK=off go test -mod=readonly -run '^$' -fuzz FuzzParse -fuzztime 60s ./internal/mediaapi/grammar
gofmt -l internal/mediaapi/grammar internal/mediaapi/presets internal/mediaapi/signing
```

**Commits.** `add media transformation grammar and canonical form`, `add media presets`, `add media url signing`.

### 5.3 Lane L3 — internal/mediaapi/derivcache

**Objective.** Build the derivative cache for transform output: disk and memory tiers keyed by bucket, key, object version and canonical transform, with eviction, stale-version reclamation, single flight and a negative cache.

**Owned paths.** `internal/mediaapi/derivcache/`.

**Read.** `docs/spec/media-api.md` §11.1–§11.8, §19.4 (binding contract), §22.2 `derivcache` row; `docs/spec/security.md` §16 (`TestDerivativesOfEncryptedMemoryOnly`).

Implement media-api §19.4 exactly (`Key`, `Entry`, `Config`, `VersionLookup`, `Stats`, `Open`, `Get`, `Put`, `PurgeObject`, `Sweep`, `Stats`, `Close`).

**Rules.**
- The cache key includes the object version (put message ID) and the pipeline version, so an overwrite or a worker upgrade never serves stale output.
- Derivatives of encrypted objects stay in the memory tier and never touch disk.
- Writes are temp-and-rename, a torn file is a miss, `Open` clears stale temp files, and touch updates are throttled.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/mountcache/coalescer.go` | 362 | PORT | `internal/mediaapi/derivcache` (L3) | One transform per derivative key at a time; abandoned loads never cache late results. A second copy of the phase 07 port, trimmed to what derivcache uses, because the binding layout has no shared package for it. Default (owner may change) |
| `backend/thumbnail/cache.go` | 535 | PATTERN-ONLY | `internal/mediaapi/derivcache` (L3) | Same disk-LRU design as `blockcache`, keyed by put message ID (the object version) and the normalized transform |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/mountcache/coalescer.go`: `func NewCoalescer[K comparable, V any](maxConcurrent int) *Coalescer[K, V] {`; `func (loads *Coalescer[K, V]) Load(`; `func (loads *Coalescer[K, V]) Invalidate(`; `func (loads *Coalescer[K, V]) InvalidateAll(terminalErr error, evict func()) {`; `func (loads *Coalescer[K, V]) Close(terminalErr error) {`; `func (loads *Coalescer[K, V]) Waiters(key K) int {`; `func (loads *Coalescer[K, V]) Len() int {`
- `backend/thumbnail/cache.go`: `type Cache struct {`; `func NewCache(dir string, maxBytes int64) *Cache {`; `func (c *Cache) Get(key string) ([]byte, bool) {`; `func (c *Cache) GetLimited(key string, limit int64) ([]byte, bool) {`; `func (c *Cache) Has(key string) bool {`; `func (c *Cache) Put(key string, data []byte) error {`; `func (c *Cache) Usage() (used, maxBytes int64, entries int) {`; `func (c *Cache) Clear() error {`

**Tests (binding names).** every media-api §22.2 `derivcache` test, plus `TestDerivativesOfEncryptedMemoryOnly`.

**Verify.**

```bash
go vet -mod=readonly ./internal/mediaapi/derivcache/...
go test -mod=readonly -race ./internal/mediaapi/derivcache/...
gofmt -l internal/mediaapi/derivcache
```

**Commits.** `add derivative cache tiers and eviction`, `add derivative cache sweep and purge`.

### 5.4 Lane L4 — internal/mediaapi/workerclient (process supervision, unix socket)

**Objective.** Run and talk to the transform worker: spawn it (or connect to an external one), health-check it, queue jobs within the concurrency limit, restart it with backoff, quarantine poison sources, and fail fast while it is down. A worker crash must never affect the S3 API.

**Owned paths.** `internal/mediaapi/workerclient/`.

**Read.** `docs/spec/media-api.md` §14 (protocol), §15.1–§15.6 (modes, spawn contract, backoff, queueing, crash isolation; §15.6 is the binding contract), §20.1 (worker metrics), §22.2 `workerclient` row. TDrive patterns: `/home/prieyan/weeb/TDrive/backend/daemon/socket_unix.go` (socket `0600`, parent directory `0700` after `Lstat`, stale socket removal) and `/home/prieyan/weeb/TDrive/backend/daemon/server_connection.go` (bounded frames, cancel on connection close).

**Contract.** Implement §15.6 exactly (`Mode`, `ModeSpawn`, `ModeExternal`, `Config`, the client type and its methods). The `Transformer` port in `handlers` (§19.5) must be satisfied implicitly. Do not import `handlers`.

**Rules.**
- In spawn mode, the binary runs with the exact §15.2 arguments and a clean environment, in its own process group. In external mode it never spawns (`TestExternalModeNeverSpawns`).
- Verify peer credentials on the socket: a UID mismatch is refused.
- Restart backoff follows the §15.3 sequence and resets after healthy uptime. A crash retries the job once, and two crashes on the same source quarantine it (§15.5).
- The queue is bounded, with a wait timeout. While the worker is down, return `media.worker_unavailable` immediately instead of queueing.
- The watchdog kills the whole process group of a hung worker.
- Use only injected time (a fake clock in tests). No goroutine outlives `Close`.

**Tests (binding names).** Every media-api §22.2 `workerclient` test, using the `TestMain` helper-process fake worker: `TestSpawnArgsAndEnv`, `TestHealthHandshake`, `TestRestartBackoffSequence`, `TestBackoffResetsAfterHealthyUptime`, `TestCrashRetriesOnce`, `TestQuarantineAfterTwoCrashes`, `TestQueueDepthAndTimeout`, `TestWatchdogKillsGroup`, `TestExternalModeNeverSpawns`, `TestPeerCredMismatch`, `TestFailFastWhileDown`.

**Verify.**

```bash
GOWORK=off CGO_ENABLED=0 go test -mod=readonly -race ./internal/mediaapi/workerclient/...
gofmt -l internal/mediaapi/workerclient
```

**Commits.** `add transform worker supervision and restart backoff`, `add worker job queue and poison quarantine`.

### 5.5 Lane L5 — internal/mediaapi/handlers

**Objective.** Serve `GET` and `HEAD` media requests: split the path, authorise (signature, expiry, bucket media settings) **before any work**, look up the source object, consult the derivative cache, call the worker under single flight, and answer with the §12 headers, conditional and range handling, and error codes.

**Owned paths.** `internal/mediaapi/handlers/`.

**Read.** `docs/spec/media-api.md` §3.2 (path splitting), §10 (request processing order), §11.6–§11.7 (single flight, negative cache), §12.1–§12.4 (headers, Cache-Control, error body and table), §19.5 (binding contract and ports), §22.2 `handlers` row; `docs/phases/07-read-path.md` (`httprange` helpers to reuse for `Range` and conditionals).

**Contract.** Implement §19.5 exactly: `BucketInfo`, `SourceInfo`, the ports `Buckets`, `Objects`, `Transformer`, `Cache`, `Presets`, `Authorizer`, plus `Config`, `Dependencies` and `New(deps Dependencies, cfg Config) (http.Handler, error)`. Declare every port here. Never import the L2, L3 or L4 packages; their types satisfy the ports implicitly.

**Rules.**
- Do not use `http.ServeMux` path cleaning. Split the raw path exactly as §3.2 says, honouring `//` and percent-encoding in keys.
- Verify the signature and expiry before any `Stat`, cache or worker call. A `403` must leave every fake untouched (`TestVerifyBeforeWork`).
- Without a signature, only presets are allowed on public buckets. Private buckets require `exp_`.
- Sniff the source early and return `415` for unsupported sources. A source above the size limit is refused before reading.
- Single flight: 50 concurrent identical requests make one worker call, and the flight survives one client cancelling.
- Encrypted sources use the memory cache tier only; nothing is written to disk.
- Cache-Control follows the §12.2 matrix, with `Vary: Accept` whenever `f_auto` applies. Never mark a response immutable, because keys are overwritable (A8).
- Transforms never write to Telegram or the databases. Originals stay byte-for-byte (A4).

**Tests (binding names).** Every media-api §22.2 `handlers` test, from `TestPathSplitting` through `TestEncryptedUsesMemoryTier`.

**Verify.**

```bash
GOWORK=off CGO_ENABLED=0 go test -mod=readonly -race ./internal/mediaapi/handlers/...
gofmt -l internal/mediaapi/handlers
```

**Commits.** `add media request handler with authorise first processing`, `add media response headers conditionals and single flight`.

### 5.6 Lane L6 — deploy/transformd/ (image with libvips, libheif, libde265, ffmpeg; systemd unit)

**Objective.** Ship the transform worker's deployment artifacts: a container image with libvips, libheif and ffmpeg, a hardened systemd unit for external mode, and the compose service.

**Owned paths.** `deploy/transformd/Dockerfile`, `deploy/transformd/butterdb-transformd.service`, `deploy/transformd/compose.transformd.yaml`, `deploy/transformd/test.sh`. `deploy/compose.yaml` belongs to phase 11. Do not edit it; the integration lane adds the `include` of the transformd compose file.

**Read.** `docs/spec/media-api.md` §21.1–§21.3, §18.2 (sandbox), §15.1 (external mode).

**Rules.**
- The image has a `golang:1.26-trixie` build stage with `libvips-dev` and `pkg-config`, and builds with `CGO_ENABLED=1 go -C apps/transformd build -trimpath`. The runtime stage is `debian:trixie-slim` with the §21.1 package list. Pin package versions after checking `apt-cache policy` in the build stage, and pin base images by digest.
- The container runs as uid `10002` and gid `10001`, with a read-only root filesystem, a tmpfs at `/tmp/transformd`, and `network_mode: none`.
- The systemd unit uses exactly the §21.2 directives, including `PrivateNetwork=yes`, `RestrictAddressFamilies=AF_UNIX`, `InaccessiblePaths=/var/lib/butterdb /etc/butterdb`, and `MemoryMax=1G`.
- Note the HEVC patent responsibility in a single-line comment in the Dockerfile. The guide carries the details.

**Tests.** `deploy/transformd/test.sh` must:
1. Run `systemd-analyze verify` on the unit when available.
2. Run `docker build -f deploy/transformd/Dockerfile .` when Docker is available.
3. Run the image with `--network none` against a socket in a temporary volume, perform the `transformv1` health exchange, and transform one JPEG fixture.

**Verify.**

```bash
bash -n deploy/transformd/test.sh
bash deploy/transformd/test.sh
```

**Commits.** `add transform worker container image`, `add transform worker systemd unit and compose service`.

## 6. Integration lane

core wiring, media listener routes, test/acceptance/phase13
Own `internal/core`, `cmd/`, and `test/acceptance/phase13` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase13/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestURLCanonicalizationAndSigningRejectVariants`: URL canonicalization and signing reject variants.
  - `TestImageOrientationAndMetadataRulesHold`: image orientation and metadata rules hold.
  - `TestWorkerCrashIsIsolatedAndRestarted`: worker crash is isolated and restarted.
  - `TestDerivativeCacheAvoidsRepeatTransforms`: derivative cache avoids repeat transforms.
  - `TestVideoPosterTimeoutIsBounded`: video poster timeout is bounded.
### TDrive regression tests to adapt

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/mountcache/coalescer_test.go` | 347 | Full | The cases that cover what derivcache keeps |
| `backend/thumbnail/orientation_test.go` | 115 | Pattern | Expected orientation per EXIF value |

## 8. Owner gates

Confirm libvips with HEIF support and ffmpeg on the build host before running worker acceptance and packaging tests; record a gated skip if the host cannot supply them.

## 9. Exit criteria and handoff

All lanes, integration tests, and media worker packaging checks pass. Record phase status and final documentation discrepancies before declaring the project plan complete.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding contracts for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/media-api.md`

## 14. Worker protocol (`api/transformv1`)

`api/transformv1` is a public contract package (standard library only),
committed by the phase 13 prelude. The server's `workerclient` and the worker's
`internal/server` both use it.

### 14.1 Transport

- One unix stream socket at `media.worker_socket`. The worker listens; the
  server connects.
- **One request per connection.** The server dials, writes one request frame
  with its payload, reads one response frame with its payload, and closes.
  Dialing a unix socket costs microseconds, and a broken connection affects
  exactly one job.
- Spawn mode: the worker creates the socket with mode `0600` inside
  `<data_dir>/run/` (mode `0700`), as invariant 12 requires. External mode: the
  worker is started with `--socket-mode 0660` and both processes share a group
  (§21.2). Default (owner may change).
- The server verifies after connect, with `SO_PEERCRED`, that the peer's uid is
  its own uid (spawn) or the configured worker uid (external); a mismatch
  closes the connection and logs `ERROR`.

### 14.2 Framing

```
frame   = magic header_len header payload
magic   = "BTX1"                       4 bytes
header_len = uint32 big-endian         1 .. 65536
header  = UTF-8 JSON                   Request or Response
payload = exactly SourceSize (request) or OutputSize (response) bytes
```

The reader rejects a wrong magic (`ErrBadMagic`), a header length of `0` or
above `MaxHeaderBytes` (`ErrHeaderTooLarge`), JSON with unknown fields
(`ErrBadHeader`), a version other than `ProtocolVersion` (`ErrVersion`), and a
payload that ends early (`io.ErrUnexpectedEOF`).

### 14.3 Contract

```go
package transformv1

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const (
	ProtocolVersion = 1
	Magic           = "BTX1"
	MaxHeaderBytes  = 64 << 10
)

var (
	ErrBadMagic       = errors.New("transformv1: bad magic")
	ErrHeaderTooLarge = errors.New("transformv1: header too large")
	ErrBadHeader      = errors.New("transformv1: bad header")
	ErrVersion        = errors.New("transformv1: unsupported protocol version")
)

type Op string

const (
	OpTransform Op = "transform"
	OpHealth    Op = "health"
)

type Resource string

const (
	ResourceImage Resource = "image"
	ResourceVideo Resource = "video"
)

type Crop string

const (
	CropFill  Crop = "fill"
	CropFit   Crop = "fit"
	CropLimit Crop = "limit"
	CropPad   Crop = "pad"
	CropThumb Crop = "thumb"
)

type Gravity string

const (
	GravityCenter    Gravity = "center"
	GravityNorth     Gravity = "north"
	GravitySouth     Gravity = "south"
	GravityEast      Gravity = "east"
	GravityWest      Gravity = "west"
	GravityNorthEast Gravity = "north_east"
	GravityNorthWest Gravity = "north_west"
	GravitySouthEast Gravity = "south_east"
	GravitySouthWest Gravity = "south_west"
	GravityAuto      Gravity = "auto"
)

type Format string

const (
	FormatJPEG   Format = "jpg"
	FormatPNG    Format = "png"
	FormatWebP   Format = "webp"
	FormatAVIF   Format = "avif"
	FormatSource Format = "src"
	FormatLegacy Format = "legacy"
)

type Spec struct {
	Width         int     `json:"w"`
	Height        int     `json:"h"`
	Crop          Crop    `json:"c"`
	Gravity       Gravity `json:"g"`
	Format        Format  `json:"f"`
	Quality       int     `json:"q"`
	Background    [4]byte `json:"b"`
	HasBackground bool    `json:"has_b"`
	KeepICC       bool    `json:"keep_icc"`
	Progressive   bool    `json:"progressive"`
	Lossless      bool    `json:"lossless"`
	Animated      bool    `json:"animated"`
	PosterMillis  int64   `json:"poster_ms"`
}

type Limits struct {
	MaxSourcePixels   int64 `json:"max_source_pixels"`
	MaxSourceSide     int   `json:"max_source_side"`
	MaxOutputSide     int   `json:"max_output_side"`
	MaxOutputPixels   int64 `json:"max_output_pixels"`
	MaxOutputBytes    int64 `json:"max_output_bytes"`
	MaxFrames         int   `json:"max_frames"`
	MaxAnimatedPixels int64 `json:"max_animated_pixels"`
}

type Request struct {
	Version       int      `json:"v"`
	Op            Op       `json:"op"`
	ID            string   `json:"id"`
	Resource      Resource `json:"resource"`
	Spec          Spec     `json:"spec"`
	Limits        Limits   `json:"limits"`
	ProcessMillis int64    `json:"process_ms"`
	SourceSize    int64    `json:"source_size"`
}

type Status string

const (
	StatusOK    Status = "ok"
	StatusError Status = "error"
)

type Code string

const (
	CodeUnsupportedSource Code = "unsupported_source_format"
	CodeUnsupportedOutput Code = "unsupported_output_format"
	CodeUndecodable       Code = "source_undecodable"
	CodeTooManyPixels     Code = "source_too_many_pixels"
	CodeTooManyFrames     Code = "too_many_frames"
	CodeOutputTooLarge    Code = "output_too_large"
	CodePosterFailed      Code = "poster_failed"
	CodeInvalidSpec       Code = "invalid_spec"
	CodeTimeout           Code = "timeout"
	CodeBusy              Code = "busy"
	CodeInternal          Code = "internal"
)

type Health struct {
	PipelineVersion string   `json:"pipeline_version"`
	Libvips         string   `json:"libvips"`
	Libheif         string   `json:"libheif"`
	FFmpeg          string   `json:"ffmpeg"`
	InputFormats    []string `json:"input_formats"`
	OutputFormats   []string `json:"output_formats"`
	Concurrency     int      `json:"concurrency"`
	InFlight        int      `json:"in_flight"`
	Sandbox         string   `json:"sandbox"`
	StartedUnix     int64    `json:"started_unix"`
}

type Response struct {
	Version     int     `json:"v"`
	ID          string  `json:"id"`
	Status      Status  `json:"status"`
	Code        Code    `json:"code,omitempty"`
	Message     string  `json:"message,omitempty"`
	ContentType string  `json:"content_type,omitempty"`
	Format      Format  `json:"format,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	OutputSize  int64   `json:"output_size"`
	Health      *Health `json:"health,omitempty"`
}

func WriteRequest(w io.Writer, req Request, src io.Reader) error
func ReadRequest(r io.Reader) (Request, io.Reader, error)
func WriteResponse(w io.Writer, resp Response, out io.Reader) error
func ReadResponse(r io.Reader) (Response, io.Reader, error)
func (s Spec) Validate(l Limits) error
```

`ReadRequest` and `ReadResponse` return the payload as an `io.LimitReader` of
exactly the declared size. The files are `api/transformv1/{protocol.go,
frame.go,spec.go}` with `frame_test.go` beside them.

### 14.4 Transform exchange

| Field | Set by the server to |
| --- | --- |
| `Version` | `ProtocolVersion` |
| `Op` | `transform` |
| `ID` | The cache key hex (64 characters); used for quarantine and log correlation |
| `Resource` | `image` or `video` |
| `Spec` | The resolved `grammar.Spec`, converted in `handlers` |
| `Limits` | From `[media]` |
| `ProcessMillis` | `image_timeout` or `poster_timeout` in milliseconds; the budget starts when the worker has received the whole payload |
| `SourceSize` | The object's plaintext size; the payload is exactly this many bytes |

The worker answers `StatusOK` with `ContentType`, the concrete `Format` (`src`
and `legacy` never appear in a response), `Width`, `Height`, `OutputSize` and
the encoded bytes; or `StatusError` with a `Code`, a short `Message` and
`OutputSize: 0`.

### 14.5 Worker code to server error

| Worker `Code` | Server sentinel | Negative-cached |
| --- | --- | --- |
| `unsupported_source_format` | `ErrMediaUnsupportedSource` (415) | yes |
| `unsupported_output_format` | `ErrMediaUnsupportedOutput` (415) | yes |
| `source_undecodable` | `ErrMediaSourceUndecodable` (422) | yes |
| `source_too_many_pixels` | `ErrMediaSourceTooManyPixels` (422) | yes |
| `too_many_frames` | `ErrMediaTooManyFrames` (422) | yes |
| `output_too_large` | `ErrMediaOutputTooLarge` (422) | yes |
| `poster_failed` | `ErrMediaPosterFailed` (422) | yes |
| `invalid_spec` | `internal.error` (500); the server and worker disagree, which is a bug | no |
| `timeout` | `ErrMediaTimeout` (503) | no |
| `busy` | `ErrMediaWorkerBusy` (503) | no |
| `internal` | `internal.error` (500) | no |

### 14.6 Health exchange

`Op: health`, `SourceSize: 0`, `ID` a random 16-hex-character string. The
response carries `Health` and `OutputSize: 0`.

- `PipelineVersion` is `"<worker semver>+vips<libvips version>+heif<libheif version>"`,
  for example `0.3.0+vips8.16.1+heif1.19.8`. It enters every cache key (§11.2).
- `OutputFormats` contains `avif` only when libheif reports an AV1 encoder at
  start; `InputFormats` contains `heic` only when it reports an HEVC decoder.
- `Sandbox` is `landlock-v<abi>`, `external` or `none` (§18.2).

---

### Contract source: `docs/spec/media-api.md`

## 19. Server package contracts

Ownership follows the phase 13 lanes of the design brief. Every constructor
validates its dependencies and returns `errs.ErrMissingDependency` for a nil
one (docs/claude.md §6.2).

### 19.1 `internal/mediaapi/grammar` (application, standard library and `internal/domain` only)

```go
package grammar

type Resource string

const (
	ResourceImage Resource = "image"
	ResourceVideo Resource = "video"
)

type Crop string
type Gravity string
type Format string
type Flag string

const (
	FlagStrip       Flag = "strip"
	FlagKeepICC     Flag = "keep_icc"
	FlagProgressive Flag = "progressive"
	FlagLossless    Flag = "lossless"
	FlagAnimated    Flag = "animated"
)

type Aspect struct {
	Num     int64
	Den     int64
	Decimal bool
}

type RGBA struct {
	R, G, B, A uint8
}

type Transformation struct {
	Preset        string
	Width         int
	Height        int
	Aspect        Aspect
	Crop          Crop
	Gravity       Gravity
	DPRCenti      int
	Quality       int
	QualityAuto   bool
	Format        Format
	Background    RGBA
	HasBackground bool
	Flags         []Flag
	PosterMillis  int64
	HasPoster     bool
	ExpiresUnix   int64
}

type Limits struct {
	MaxOutputSide   int
	MaxOutputPixels int64
	MaxParams       int
	MaxBytes        int
}

type AcceptClass string

const (
	AcceptAVIF   AcceptClass = "avif"
	AcceptWebP   AcceptClass = "webp"
	AcceptLegacy AcceptClass = "legacy"
)

type Spec struct {
	Resource      Resource
	Width         int
	Height        int
	Crop          Crop
	Gravity       Gravity
	Format        string
	Quality       int
	Background    RGBA
	HasBackground bool
	KeepICC       bool
	Progressive   bool
	Lossless      bool
	Animated      bool
	PosterMillis  int64
	UsedAuto      bool
}

type Error struct {
	Sentinel error
	Param    string
	Message  string
}

func (e *Error) Error() string
func (e *Error) Unwrap() error

func Parse(segment string, l Limits) (Transformation, error)
func (t Transformation) Canonical() string
func (t Transformation) IsPresetOnly() bool
func (t Transformation) UsesAuto() bool
func Merge(preset, request Transformation) Transformation
func Validate(t Transformation, r Resource, l Limits) error
func ClassifyAccept(accept string) []AcceptClass
func Resolve(t Transformation, r Resource, accepted []AcceptClass, avif AVIFPolicy, ladder []int, l Limits) (Spec, error)
func (s Spec) Key() string

type AVIFPolicy struct {
	Available bool
	MaxPixels int64
}
```

Files: `parse.go`, `canonical.go`, `validate.go`, `resolve.go`, `accept.go`,
`ladder.go`, `errors.go`, each with a `_test.go`, plus `fuzz_test.go`.

### 19.2 `internal/mediaapi/presets` (application)

```go
package presets

type BucketSettings struct {
	Enabled       *bool
	AllowUnsigned bool
	Presets       map[string]string
}

type Config struct {
	Global  map[string]string
	Buckets map[string]BucketSettings
}

type Access struct {
	Enabled       bool
	AllowUnsigned bool
}

func New(cfg Config, l grammar.Limits) (*Store, error)
func (s *Store) Preset(bucket, name string) (grammar.Transformation, bool)
func (s *Store) Access(bucket string) Access
func (s *Store) Replace(cfg Config) error
```

`Replace` builds a new snapshot and swaps it atomically; an invalid
configuration returns an error and keeps the old snapshot. `internal/core`
calls it on `SIGHUP`.

### 19.3 `internal/mediaapi/signing` (application)

```go
package signing

type Input struct {
	Bucket    string
	Resource  grammar.Resource
	Canonical string
	Key       string
}

type Policy struct {
	Private       bool
	AllowUnsigned bool
}

func ParseKeyFile(data []byte) ([]byte, error)
func StringToSign(in Input) []byte
func NewSigner(current, previous []byte, maxPrivateExpiry time.Duration) (*Signer, error)
func (s *Signer) Sign(in Input) string
func (s *Signer) Verify(in Input, segment string) error
func (s *Signer) Authorize(p Policy, t grammar.Transformation, in Input, segment string, now time.Time) error
func (s *Signer) Rotate(current, previous []byte) error
```

`Authorize` implements §7.3 and §7.4 and returns the `ErrMedia*` sentinels.
`NewSigner` copies the keys; `Rotate` replaces them on `SIGHUP`; the old copies
are zeroed (invariant 6 applies to every key copy). `segment` is empty for an
unsigned request.

### 19.4 `internal/mediaapi/derivcache` (application)

```go
package derivcache

type Key struct {
	Bucket    string
	ObjectKey string
	Version   int64
	Resource  string
	Effective string
	Protocol  int
	Pipeline  string
}

func (k Key) Sum() [32]byte
func (k Key) Hex() string
func (k Key) ETag() string

type Entry struct {
	ContentType string    `json:"content_type"`
	Format      string    `json:"format"`
	Size        int64     `json:"size"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	ETag        string    `json:"etag"`
	Created     time.Time `json:"created"`
}

type Config struct {
	Root           string
	MaxBytes       int64
	MemoryMaxBytes int64
	TouchInterval  time.Duration
}

type VersionLookup interface {
	CurrentVersion(ctx context.Context, bucket, key string) (int64, bool, error)
}

type Stats struct {
	DiskBytes   int64
	DiskEntries int
	MemBytes    int64
	MemEntries  int
}

var ErrMiss = errors.New("derivcache: miss")

func Open(cfg Config, clk clock.Clock, logger *slog.Logger) (*Cache, error)
func (c *Cache) Get(k Key) (Entry, io.ReadSeekCloser, error)
func (c *Cache) Put(k Key, e Entry, body io.Reader, persist bool) (Entry, error)
func (c *Cache) PurgeObject(bucket, objectKey string) error
func (c *Cache) Sweep(ctx context.Context, lookup VersionLookup) error
func (c *Cache) Stats() Stats
func (c *Cache) Close() error
```

`persist` is `false` for encrypted sources (memory tier). `Put` returns the
stored entry with `ETag` set from `k.ETag()`.

### 19.5 `internal/mediaapi/handlers` (interface)

```go
package handlers

type BucketInfo struct {
	Name        string
	Private     bool
	CacheMaxAge time.Duration
}

type SourceInfo struct {
	Version      int64
	PlainSize    int64
	Encrypted    bool
	LastModified time.Time
}

type Buckets interface {
	MediaBucket(ctx context.Context, name string) (BucketInfo, error)
}

type Objects interface {
	Stat(ctx context.Context, bucket, key string) (SourceInfo, error)
	Open(ctx context.Context, bucket, key string, version int64) (io.ReadCloser, error)
}

type Transformer interface {
	Transform(ctx context.Context, req transformv1.Request, src io.Reader, out io.Writer) (transformv1.Response, error)
	Health() (transformv1.Health, bool)
}

type Cache interface {
	Get(k derivcache.Key) (derivcache.Entry, io.ReadSeekCloser, error)
	Put(k derivcache.Key, e derivcache.Entry, body io.Reader, persist bool) (derivcache.Entry, error)
	PurgeObject(bucket, objectKey string) error
}

type Presets interface {
	Preset(bucket, name string) (grammar.Transformation, bool)
	Access(bucket string) presets.Access
}

type Authorizer interface {
	Authorize(p signing.Policy, t grammar.Transformation, in signing.Input, segment string, now time.Time) error
}

type Config struct {
	PathPrefix    string
	Hostnames     []string
	Limits        grammar.Limits
	Ladder        []int
	AVIFMaxAuto   int64
	MaxImageSrc   int64
	MaxVideoSrc   int64
	SourceTimeout time.Duration
	ImageTimeout  time.Duration
	PosterTimeout time.Duration
	NegativeTTL   time.Duration
	WorkerLimits  transformv1.Limits
}

type Dependencies struct {
	Buckets     Buckets
	Objects     Objects
	Transformer Transformer
	Cache       Cache
	Presets     Presets
	Authorizer  Authorizer
	Clock       clock.Clock
	Logger      *slog.Logger
}

func New(deps Dependencies, cfg Config) (http.Handler, error)
```

`Objects.Stat` returns `errs.ErrNoSuchBucket` or `errs.ErrNoSuchKey`;
`Objects.Open` returns a reader of exactly `PlainSize` plaintext bytes, through
`readpath/objectreader` and the block cache. `internal/core` adapts
`ObjectMeta` to `SourceInfo` and the bucket configuration to `BucketInfo`; the
handlers never see a `DocumentRef` or a DEK.

Files: `router.go` (path splitting), `serve.go`, `flight.go`, `negative.go`,
`sniff.go`, `errors.go`, `headers.go`, each with tests.

### 19.6 Wiring in `internal/core` (phase 13 integration lane)

1. When `media.enabled`, read or create the signing key, build `presets.Store`,
   `signing.Signer`, `derivcache.Cache`, `workerclient.Supervisor` and the
   handler.
2. Register the supervisor and the cache with the lifecycle group; the
   supervisor starts at step 7 of the startup order and stops at step 2 of
   shutdown ([plan.md](../plan.md) §3, docs/claude.md §7).
3. Bind the media listener at step 8 with `ReadHeaderTimeout 10s`,
   `IdleTimeout 120s`, `MaxHeaderBytes 16 KiB` and no `WriteTimeout` (ranges of
   large derivatives to slow clients).
4. Run `derivcache.Sweep` every `cache_sweep_interval` with a `VersionLookup`
   adapter over `objectrepo`.
5. On `SIGHUP`, re-read `[media]` and call `presets.Store.Replace` and
   `signing.Signer.Rotate`.
6. Report the supervisor `State` as the admin status component `transformd`.

---
