# Phase 02 — Formats gate (TDX1 object ops, TDE1 container, schema v1) — HARD GATE
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement formats gate (tdx1 object ops, tde1 container, schema v1) — hard gate as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 01 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/wire-format.md`.
- `docs/spec/schema.md`.
- `docs/spec/storage-model.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Create `phase/02-formats-gate` from main. Commit `internal/wire/types.go` with the following full contract from `docs/spec/wire-format.md` §2. L1 and L2 both compile against this file. The source is binding; update both copies together if the format gate finds a defect.

```go
package wire

type OpType string

const (
	OpBody         OpType = "body"
	OpPut          OpType = "put"
	OpDel          OpType = "del"
	OpRegistry     OpType = "reg"
	OpBucketCreate OpType = "bcreate"
	OpBucketConfig OpType = "bcfg"
	OpBucketDelete OpType = "bdel"
	OpEncConfig    OpType = "encfg"
)

const (
	Prefix              = "TDX1"
	FormatVersion       = 1
	MaxUnixTimestamp    = int64(253402300799)
	MaxTextChars        = 4096
	MaxCaptionChars     = 1024
	MaxMsgID            = int64(2147483647)
	MaxKeyBytes         = 1024
	MaxEncodedKeyChars  = 3 * MaxKeyBytes
	MaxContentTypeBytes = 1024
	MaxUserMetaBytes    = 2048
	MaxSysHeaderBytes   = 1024
	MaxPutFixedChars    = 512
	MaxStoredBytes      = int64(1900 * 1024 * 1024)
	MaxCacheMaxAge      = int64(31536000)
	DefaultContentType  = "application/octet-stream"
	EmptyMD5            = "d41d8cd98f00b204e9800998ecf8427e"
	EmptySHA256         = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var (
	ErrWireMissingHeader = errors.New("wire: header missing")
	ErrWireBadVersion    = errors.New("wire: unknown version")
	ErrWireMalformed     = errors.New("wire: malformed header")
	ErrWireUnknownOp     = errors.New("wire: unknown op type")
	ErrWireTooLong       = errors.New("wire: encoded op exceeds the telegram length limit")
)

type Op struct {
	Type              OpType
	JobID             string
	Key               string
	BodyMsgID         int64
	StoredSize        int64
	PlaintextSize     int64
	Encrypted         bool
	EncryptionVersion int
	KeyID             string
	ETag              string
	SHA256            string
	ContentType       string
	UserMeta          map[string]string
	SystemMeta        map[string]string
	Writer            string
	Timestamp         int64
	PutVersion        int64
	Bucket            string
	BucketChannelID   int64
	Policy            string
	DefaultEncryption string
	CacheMaxAge       int64
	WrappedKey        []byte
	KeyCheck          []byte
	KEKFingerprint    string
	ConfigVersion     int
	DeploymentID      string
	FormatVersion     int
	MinReaderVersion  int
	PrimaryUserID     int64
}

func ExtractHeaderLine(raw string) string
func Parse(raw string) (Op, error)
func Format(op Op) (string, error)
func Known(t OpType) bool
func OnDocument(t OpType) bool
func PutRecordLen(key, contentType string, userMeta, systemMeta map[string]string, writer string) (int, error)
func FitsPut(key, contentType string, userMeta, systemMeta map[string]string, writer string) error
func KeyOf(op Op) (string, bool)
func HasPrefix(raw string) bool
```

Commit `internal/domain/object/types.go` and `internal/domain/bucket/types.go`
with these shared types before fan-out. They mirror the live `objects` and
`buckets` columns in `docs/spec/schema.md` §4; the corresponding validators
remain L5's implementation. `MsgDate` is the Telegram put message timestamp,
which S3 exposes as `LastModified`.

```go
package object

type ObjectMeta struct {
	ChannelID int64
	Key string
	PutMsgID int64
	BodyMsgID int64
	JobID string
	StoredSize int64
	PlaintextSize int64
	ETag string
	SHA256 string
	ContentType string
	UserMeta map[string]string
	SystemMeta map[string]string
	Encrypted bool
	EncryptionVersion int
	KeyID string
	Writer string
	MsgDate int64
}

type Key string

type PutRequest struct {
	Bucket string
	Key Key
	ContentLength int64
	ContentType string
	UserMeta map[string]string
	SystemMeta map[string]string
	Encryption string
	AccessKeyID string
	IdempotencyKey string
	IfMatch string
	IfNoneMatchAny bool
}

type ListQuery struct {
	Prefix string
	Delimiter string
	StartAfter string
	ContinuationToken string
	Limit int
}

type ListPage struct {
	Objects []ObjectMeta
	CommonPrefixes []string
	NextToken string
}
```

```go
package bucket

type BucketName string

type Info struct {
	Name BucketName
	ChannelID int64
	Policy string
	DefaultEncryption string
	CacheMaxAge int64
	CreatedDate int64
	ObjectCount int64
	StoredBytes int64
	PlaintextBytes int64
}

type Spec struct {
	Name BucketName
	Policy string
	DefaultEncryption string
	CacheMaxAge int64
}

type Patch struct {
	Policy *string
	DefaultEncryption *string
	CacheMaxAge *int64
}
```

The prelude also commits `internal/storage/sqlitedb/contract.go` with this shared shape from `docs/spec/schema.md` §2.2; L4 implements it.

```go
package sqlitedb

type Name string

const (
	Projection Name = "projection"
	Control    Name = "control"
)

type Options struct {
	ReadConns       int
	BusyTimeout     time.Duration
	SlowTxThreshold time.Duration
}

type DB struct{}

func Open(ctx context.Context, name Name, path string, opts Options, log *slog.Logger) (*DB, error)
func (d *DB) Name() Name
func (d *DB) Write(ctx context.Context, fn func(tx *sql.Tx) error) error
func (d *DB) Read(ctx context.Context, fn func(tx *sql.Tx) error) error
func (d *DB) Reader() *sql.DB
func (d *DB) VacuumInto(ctx context.Context, dstPath string) error
func (d *DB) Close() error
``` Add
`modernc.org/sqlite` with `go get modernc.org/sqlite@v1.45.0`, the version in
TDrive's `go.mod`. Do not run `go mod tidy` until integration. Commit as
`define phase 02 format contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/02-formats-gate-l1` | wire core: internal/wire/{grammar.go,parse.go,format.go,errors.go} (port TDrive backend/projection wire code) |
| L2 | `phase/02-formats-gate-l2` | op catalogue: internal/wire/ops_*.go (body, put, del, bcreate, bcfg, bdel, encfg, registry marker) + round-trip table tests |
| L3 | `phase/02-formats-gate-l3` | TDE1 format: internal/crypto/format |
| L4 | `phase/02-formats-gate-l4` | schema and sqlite: internal/storage/{sqlitedb,schema} |
| L5 | `phase/02-formats-gate-l5` | domain model: internal/domain/{object,bucket,access} |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — wire core

**Objective.** Port the TDX1 grammar: extract the header line, split pairs, unescape, and format, with the parser skeleton and setters, but no op-specific fields (those are L2's).

**Owned paths.** `internal/wire/grammar.go`, `internal/wire/parse.go`, `internal/wire/format.go`, `internal/wire/errors.go` and their `_test.go` files. The `Op` and `OpType` contract comes from the prelude; do not edit it.

**Read.** `docs/spec/wire-format.md` §1 (grammar), §2 (Go contract), §7 (versioning and the unknown-op rule E5), §8 (parser skeleton), §9 (setters), §12 (round-trip obligation); legacy `docs/butterDBCore.md` lines 1023–1249.

**Rules.**
- Only the first line is the header. The version prefix is literal `TDX1`, the separator is `|`, and a pair is `key=value` with `=` not at index 0. Text uses `url.QueryEscape`; binary uses `base64.RawURLEncoding`.
- Unknown keys are ignored. An unknown `t=` value parses into the prelude's unknown-op form, not into a fatal error, per wire-format §7.3 (E5).
- Timestamps lie in `0..253402300799`. Every malformed case returns its specific sentinel from `errors.go`.
- The standard library only. No allocation-heavy regexes in the hot path.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/projection/wire.go` | 708 | PORT-PARTIAL | `internal/wire/{grammar.go,parse.go,format.go,errors.go}` (L1) | Keep: `TDX1` prefix check, `ExtractHeaderLine` (first line only), `\|`-separated `key=value` pairs with `=` required after a non-empty key, `url.QueryEscape` / `url.QueryUnescape` for text values, `base64.RawURLEncoding` for binary values (`setB64`), positive integer parsing (`setPositiveInt64`), timestamp ceiling `maxWireUnixTimestamp`, sentinels `ErrWireMissingHeader`, `ErrWireBadVersion`, `ErrWireMalformed` (renamed by the phase 02 prelude). Change: unknown `t=` returns a parsed op marked unknown, not `ErrWireBadOpType` (E5); unknown keys inside a known op are ignored. Drop: every folder and namespace op (`f`, `meta`, `mkdir`, `rename`, `move`, `rmdir`, `tomb`), multipart (`part`, `manifest`), writable ops (`fcommit`, `freplace`, `dcommit`, `relocate`, `trash`, `harddel`, `untrash`), `d:` / `f:` object prefixes, KDF fields of `encfg` (`KDFSalt`, `KDFParamsJSON`, `Hint`) |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/projection/wire.go`: `func ExtractHeaderLine(raw string) string {`; `func Parse(raw string) (Op, error) {`; `func Format(op Op) string {`

**Tests (binding names).** the carried `backend/projection/wire_test.go` cases in this phase's regression table, plus `TestHeaderFirstLineOnly`, `TestUnknownKeysIgnored`, `TestUnknownOpTypeIsUnknownOp`, `TestEscapingRoundTrip`, `TestEmptyPairRejected`, `TestEqualsAtIndexZeroRejected`, and `FuzzParseNoPanic`.

**Verify.**

```bash
go vet -mod=readonly ./internal/wire/...
go test -mod=readonly -race ./internal/wire/...
gofmt -l internal/wire
```

**Commits.** `port tdx1 grammar parser and formatter`.

### 5.2 Lane L2 — op catalogue

**Objective.** Define every butterDB op (`body`, `put`, `del`, `reg`, `bcreate`, `bcfg`, `bdel`, `encfg`) with its required keys and validation, the length and overflow rules, and the round-trip table that gates the formats.

**Owned paths.** `internal/wire/ops_*.go` and `internal/wire/ops_*_test.go`, plus `internal/wire/testdata/`.

**Read.** `docs/spec/wire-format.md` §3–§6 (catalogue, field vocabulary, op definitions, length limits and the §6.3 overflow rule), §10 (worked examples, byte-exact), §11 (legacy ops not used), §12 (round-trip obligation).

**Rules.**
- Each op file registers its type with the L1 parser through the prelude's registration hook. If the prelude lacks one, stop and report; do not add one to L1's files.
- Text-message headers must fit 4096 characters, and the `t=body` caption must fit 1024. Overflow returns the §6.3 error before any send. Keys are capped at 1024 bytes.
- None of the legacy ops in §11 parse as known ops.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/projection/wire.go` `setWritableEnvelope`, `setContentReference` | (in 708) | PATTERN-ONLY | `internal/wire/ops_*.go` (L2) | TDrive's writable ops carry a version and a durable operation ID and reference the hidden body by message ID and content hash. butterDB's `t=body` carries `j=<job id>` and `t=put` references the body message ID, sizes, MD5 and SHA-256 the same way |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/projection/wire.go`: `func ExtractHeaderLine(raw string) string {`; `func Parse(raw string) (Op, error) {`; `func Format(op Op) string {`

**Tests (binding names).** `TestRoundTripEveryOp` (`Parse(Format(op)) == op` for every op), `TestEveryMalformedCase` (one row per setter failure), `TestWorkedExamplesByteExact` (every §10 example), `TestOverflowRule`, `TestCaptionLimitBody`, `TestLegacyOpsNotKnown`, `TestPutRequiresJob`, `TestBodyRequiresJob` (the carried writable-wire pattern), and `FuzzRoundTrip`.

**Verify.**

```bash
go vet -mod=readonly ./internal/wire/...
go test -mod=readonly -race ./internal/wire/...
gofmt -l internal/wire
```

**Commits.** `add tdx1 object and registry ops`, `add tdx1 round trip and malformed case tables`.

### 5.3 Lane L3 — TDE1 format

**Objective.** Port the TDE1 container constants and pure arithmetic: header layout, nonce construction with the final-chunk bit, ciphertext size, stored offsets, and plaintext validation.

**Owned paths.** `internal/crypto/format/`.

**Read.** `docs/spec/storage-model.md` §7.1 (size arithmetic) and §8 (encryption model); legacy `docs/butterDBCore.md` lines 2017–2222 (TDE1, normative byte for byte).

**Rules.**
- The 50-byte header is `TDE1`, flags 0, `chunk_log2` 16, a 16-byte salt, a 20-byte nonce prefix, and the LE64 plaintext size. Chunks are 65,536 bytes with a 16-byte tag.
- `CiphertextSize(L) = 50 + L + 16 × (L/65536 + 1)`. There is always a final chunk, so an empty file is 66 bytes. It saturates at `MaxInt64` on overflow.
- The nonce is `prefix(20) ‖ BE32(counter)`, with bit 31 marking the final chunk.
- The largest encrypted plaintext that fits `limits.MaxStoredObjectBytes` is 1,991,808,062 bytes. Export it as a constant computed from the formula, and test it.
- The standard library only. This package does no encryption at all; that is phase 03.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/crypto/stream.go` (constants, `ValidatePlaintextSize`, `CiphertextSize`, `buildNonce`, header layout) | 318 | PORT-PARTIAL | `internal/crypto/format` (L3) | Keep verbatim: magic `TDE1`, header 50 bytes (`4+1+1+16+20+8`), `chunkSizeLog2 = 16`, chunk 65,536 bytes, salt 16, nonce prefix 20, tag 16, `finalChunkBit = 1 << 31`, `maxPlaintextSize = 2^47 − 1`, plaintext size little-endian. Change: `streamTagLen` becomes the literal `16` because `format` imports the standard library only (rule L1) and cannot import `chacha20poly1305`; sentinels `ErrShortHeader`, `ErrBadMagic`, `ErrUnsupported`, `ErrNegativePlaintextSize`, `ErrPlaintextTooLarge` move here |
| `backend/crypto/random_access.go` `StoredOffset`, `ciphertextChunkOffset` | (in 413) | PORT-PARTIAL | `internal/crypto/format` (L3) | Plaintext offset to stored offset arithmetic, overflow-checked |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/crypto/stream.go`: `func ValidatePlaintextSize(plaintextSize int64) error {`; `func EncryptStream(src io.Reader, dst io.Writer, masterKey []byte, plaintextSize int64) error {`; `func CiphertextSize(plaintextSize int64) int64 {`; `func DecryptStream(src io.Reader, dst io.Writer, masterKey []byte) (int64, error) {`
- `backend/crypto/random_access.go`: `type ContextReaderAt interface {`; `type RandomAccessDecryptor struct {`; `func NewRandomAccessDecryptor(`; `func (decryptor *RandomAccessDecryptor) Clone(source ContextReaderAt) (*RandomAccessDecryptor, error) {`; `func (decryptor *RandomAccessDecryptor) Size() int64 {`; `func (decryptor *RandomAccessDecryptor) ReadAt(`; `func (decryptor *RandomAccessDecryptor) Close() error {`; `func StoredOffset(plaintextOffset int64) (int64, bool) {`

**Tests (binding names).** `TestCiphertextSizeSaturatesOnOverflow`, `TestValidatePlaintextSizeEnforcesTDE1CounterCapacity`, `TestStoredOffsetSkipsHeaderAndEarlierChunkTags` (carried), plus `TestCiphertextSizeVectors` (0→66, 1→67, 65535→65601, 65536→65618, 65537→65619), `TestMaxEncryptedPlaintextFits`, `TestHeaderRoundTrip`, `TestHeaderRejectsBadMagicFlagsLog2`, `TestNonceFinalBit`.

**Verify.**

```bash
go vet -mod=readonly ./internal/crypto/format/...
go test -mod=readonly -race ./internal/crypto/format/...
gofmt -l internal/crypto/format
```

**Commits.** `port tde1 header nonce and size arithmetic`.

### 5.4 Lane L4 — schema and sqlite

**Objective.** Write all schema-version-1 DDL for both databases as embedded migrations, and the SQLite adapter: one writer connection plus a read-only WAL pool, per-connection pragmas, `VacuumInto`, and the migration runner.

**Owned paths.** `internal/storage/sqlitedb/`, `internal/storage/schema/` (including `projection/0001_init.sql` and `control/0001_init.sql`).

**Read.** `docs/spec/schema.md` §1–§3 (databases, connections, the §2.2 Go contract, the no-transaction-across-network rule, migrations), §4.1 and §5.1 (full DDL, copied byte for byte), §6 (ownership), §9 (required tests); `docs/decisions.md` E7.

Implement the schema.md §2.2 contract exactly. `schema` exposes `Apply(ctx, db, name string) error` plus the sentinels `ErrSchemaTooNew`, `ErrSchemaChecksum` and `ErrWrongDatabase`.

**Rules.**
- This lane is the only importer of `modernc.org/sqlite` in this phase, and the prelude adds it by exact package path.
- Apply pragmas on every pool connection through a connector hook, never once per `*sql.DB`. Database files and their `-wal` and `-shm` siblings are `0600`.
- Migrations are embedded with a glob and applied in name order, with the version, name and SHA-256 recorded in `schema_version`. There is no shared list file.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/projection/schema.go` | 898 | PORT-PARTIAL | `internal/storage/schema` `projection/0001_*.sql` (L4) | Keep tables with these columns: `replay_log (channel_id, msg_id, op_type, op_payload_json, raw_header, first_seen_hash, actor_user_id, seen_at, PRIMARY KEY (channel_id, msg_id))`; `replay_log_tamper (channel_id, msg_id, old_hash, new_hash, detected_at)`; `replay_log_rejects (channel_id, msg_id, error, detected_at)`, which butterDB extends with a stable reason code so `unknown_op` is distinguishable (E5); `channels` reduced to `channel_id, access_hash, title, last_synced_msg, initial_sync_done, needs_projection_rebuild, pts`; `projection_operations (channel_id, op_id, msg_id, op_type, outcome CHECK IN ('applied','rejected'), error)`. Change: imperative migrations with `PRAGMA table_info` probes become numbered embedded SQL files. Drop: `backfill_progress`, `pending_joins`, `encryption`, `file_parts`, `pending_part_cleanup`, `dirents`, `file_revisions`, `trash_entries`, `hard_delete_jobs`, `hard_delete_intents`, `hard_delete_plan_items`, `folders`, `files`. Object and bucket tables are new and defined in `docs/spec/storage-model.md` |
| `backend/sync/initial_scan_spool.go` (DDL only) | (in 136) | PORT-PARTIAL | `internal/storage/schema` (L4) | `initial_scan_spool (channel_id, msg_id, from_id, date, text, has_media, media_size, document_name, PRIMARY KEY (channel_id, msg_id))` |
| `backend/storage_sqlite.go` | 117 | PATTERN-ONLY | `internal/storage/sqlitedb` (L4) | Keep the pragma values: `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`, `temp_store=MEMORY`, `cache_size=-32768`, `mmap_size=134217728`. Change: one writer connection plus a read-only WAL pool, pragmas applied per connection through the DSN (E7); no global `DB` |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/projection/schema.go`: `func EnsureSchema(db *sql.DB) error {`; `func MigratePersonalChannel(db *sql.DB, personalChannelID int64) error {`
- `backend/storage_sqlite.go`: `func InitDB() error {`; `func TuneSQLite(db *sql.DB) error {`; `func EnsureSchema() error {`; `func MigratePersonalChannel(personalChannelID int64) error {`

**Tests (binding names).** every `storage/schema` and `storage/sqlitedb` row of schema.md §9: `TestMigrationsContiguous`, `TestApplyFreshAndIdempotent`, `TestSchemaTooNew`, `TestSchemaChecksum`, `TestWrongDatabase`, `TestDDLMatchesSpec`, `TestPragmasOnEveryConnection`, `TestPoolIsQueryOnly`, `TestReadsDoNotWaitForWriter`, `TestVacuumIntoConsistent`, `TestFileMode0600`.

**Verify.**

```bash
go vet -mod=readonly ./internal/storage/sqlitedb/... ./internal/storage/schema/...
go test -mod=readonly -race ./internal/storage/sqlitedb/... ./internal/storage/schema/...
gofmt -l internal/storage/sqlitedb internal/storage/schema
```

**Commits.** `add sqlite writer and read pool adapter`, `add schema version one migrations`.

### 5.5 Lane L5 — domain model

**Objective.** Implement the domain value types and their validation for objects, buckets and access keys, so every later layer shares one definition of a valid key, bucket name, scope and ETag.

**Owned paths.** `internal/domain/object/`, `internal/domain/bucket/`, `internal/domain/access/`. The prelude commits the type contracts (including `object.PutRequest`, `ListQuery`, `ListPage`, `ObjectMeta`); this lane adds constructors, validation and tests. Never change a field the prelude defined.

**Read.** `docs/spec/s3-api.md` §4.9–§4.10 (key IDs, scopes and actions), §6.4 (bucket names), §8.7 (keys and metadata), §8.8 (ETag rules); `docs/spec/storage-model.md` §5 and §6; `docs/spec/admin-api.md` §16.8 (domain names the admin layer expects).

**Rules.**
- Keys are UTF-8, 1 to 1024 bytes, with no control characters (s3-api §8.7). User metadata is at most 2048 bytes. Bucket names follow the S3 rules in s3-api §6.4.
- A scope's buckets are a list of names or exactly `["*"]`. Actions are a non-empty subset of `read`, `list`, `write`, `delete`.
- An ETag is the quoted MD5 hex. A multipart ETag is the MD5 of the binary part MD5s, plus `-N`.
- The standard library only. Values are immutable after construction.

**Tests (binding names).** `TestKeyValidation` (empty, 1024, 1025 bytes, control characters, invalid UTF-8), `TestUserMetadataLimit`, `TestBucketNameRules` (the s3-api §6.4 table), `TestScopeValidation`, `TestActionParsing`, `TestAccessKeyIDFormat`, `TestETagFormat`, `TestMultipartETag`.

**Verify.**

```bash
go vet -mod=readonly ./internal/domain/object/... ./internal/domain/bucket/... ./internal/domain/access/...
go test -mod=readonly -race ./internal/domain/object/... ./internal/domain/bucket/... ./internal/domain/access/...
gofmt -l internal/domain/object internal/domain/bucket internal/domain/access
```

**Commits.** `add object bucket and access domain validation`.

## 6. Integration lane

test/acceptance/phase02 (round trip every op, every malformed case, unknown op handling, TDE1 size vectors)
Own `internal/core`, `cmd/`, and `test/acceptance/phase02` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase02/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestEveryOpParsesAfterFormatting`: every op parses after formatting.
  - `TestMalformedWireDataReturnsTheSpecifiedError`: malformed wire data returns the specified error.
  - `TestUnknownOpRemainsAReplayableReject`: unknown op remains a replayable reject.
  - `TestSchemaMigrationsBuildAndIntegrityCheck`: schema migrations build and integrity-check.
  - `TestTDE1SizeVectorsMatch`: TDE1 size vectors match.
### TDrive regression tests to adapt

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/projection/wire_test.go` | 255 | Partial, rewritten for new ops | `TestParseEmptyHeader`, `TestParseUnknownVersion`, `TestExtractHeaderLineStripsBody`, `TestRoundTripFormatParse`, `TestParseRejectsUploadTimeOutsideSQLiteRange` (applies to the put timestamp), `TestParseRejectsNegativeFileSizes` (stored and plaintext sizes). Invert `TestParseUnknownOpType`: it must now yield an unknown op, not an error |
| `backend/projection/writable_wire_test.go` | 57 | Pattern | `TestWritableWireRequiresVersionAndOperationID` becomes "body and put require `j=`" |
| `backend/crypto/ciphertext_size_test.go` | 169 | Partial | `TestCiphertextSizeSaturatesOnOverflow`, `TestValidatePlaintextSizeEnforcesTDE1CounterCapacity` here; the cases that call `EncryptStream` move to phase 03 |
| `backend/crypto/stored_offset_test.go` | 21 | Full | `TestStoredOffsetSkipsHeaderAndEarlierChunkTags` |
| `backend/projection/migration_test.go` | 543 | CUT | Tests TDrive's legacy schema upgrades |

## 8. Owner gates

The owner signs off the TDX1 operation catalogue, TDE1 byte layout, and schema v1 after the phase 02 acceptance suite. No persistent format bytes change after this gate without a new decision record entry.

## 9. Exit criteria and handoff

All L1–L5 commits have passed monitoring and review; the integration build and phase 02 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 03.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding behavior for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/schema.md`

## 3. Migrations (`internal/storage/schema`)

### 3.1 Layout

```text
internal/storage/schema/
  schema.go
  projection/0001_init.sql
  control/0001_init.sql
```

```go
//go:embed projection/*.sql control/*.sql
var files embed.FS
```

Files are discovered by the embed glob; there is no list file to keep in sync
(claude.md §9.2). A file name must match `^([0-9]{4})_([a-z0-9_]+)\.sql$`.
Versions in a set start at 1 and are contiguous; a gap, a duplicate number or a
non-matching name is `ErrSchemaGap`, raised by a unit test at build time and by
`Apply` at run time.

### 3.2 `schema_version`

Created by the migrator itself, before the first file runs, in each database:

```sql
CREATE TABLE IF NOT EXISTS schema_version (
  version    INTEGER PRIMARY KEY CHECK (version >= 1),
  name       TEXT    NOT NULL,
  sha256     TEXT    NOT NULL CHECK (length(sha256) = 64),
  applied_at INTEGER NOT NULL
) STRICT;
```

One row per applied file. `sha256` is the lowercase hex SHA-256 of the file's
bytes as embedded. `applied_at` is Unix milliseconds.

### 3.3 Apply

```go
package schema

type Set string

const (
	Projection Set = "projection"
	Control    Set = "control"
)

type Migration struct {
	Version int
	Name    string
	SHA256  string
	SQL     string
}

var (
	ErrSchemaTooNew   = errors.New("schema: database schema is newer than this binary")
	ErrSchemaChecksum = errors.New("schema: applied migration differs from the embedded file")
	ErrSchemaGap      = errors.New("schema: embedded migrations are not contiguous")
	ErrWrongDatabase  = errors.New("schema: database belongs to the other set")
)

func Migrations(set Set) ([]Migration, error)
func Latest(set Set) int
func Current(ctx context.Context, db *sqlitedb.DB) (int, error)
func Pending(ctx context.Context, db *sqlitedb.DB, set Set) ([]Migration, error)
func Apply(ctx context.Context, db *sqlitedb.DB, set Set) ([]int, error)
```

| Step | Rule |
| --- | --- |
| 1 | Check `application_id` (section 2.1); stamp it on a new file |
| 2 | `CREATE TABLE IF NOT EXISTS schema_version` |
| 3 | For every row in `schema_version`, the embedded file of that version must exist with the same `sha256`. A missing file means the database is newer: `ErrSchemaTooNew`. A different hash: `ErrSchemaChecksum` |
| 4 | Each pending file, in version order, runs in its own `Write` transaction together with the `INSERT INTO schema_version` of its row. A failure rolls that file back and stops; earlier files stay applied |
| 5 | After the last file, `PRAGMA foreign_key_check` on `control.db` and `PRAGMA quick_check` on both; any result row fails the start |

Errors map to exit codes in operations.md §4.2: `ErrSchemaTooNew`,
`ErrSchemaChecksum` and `ErrWrongDatabase` exit 5; any other failure exits 1.
`butterdbd serve` calls `Pending` first and, when it is non-empty and
`snapshot.before_migrate` is true, takes a snapshot before `Apply`
(operations.md §7.1 step 6). `butterdbd restore` refuses a snapshot whose
`schema` versions exceed `Latest` (operations.md §10.2 step 7).

### 3.4 Rules for writing a migration

1. An applied file is never edited. A change is a new file with the next number.
2. Migrations are forward only (operations.md §12).
3. A migration contains DDL and data fixes only. No `PRAGMA` that is per connection, no `ATTACH`, no `VACUUM` (it cannot run inside a transaction).
4. Changing a `CHECK` or a column type in SQLite means the twelve-step table rebuild (create new, copy, drop, rename) inside the migration's transaction.
5. A `projection.db` migration that changes how ops project ends with `UPDATE channels SET needs_projection_rebuild = 1;`, so the next start rebuilds from the replay log (storage-model.md §9.1) instead of shipping a migration that rewrites derived rows by hand.
6. Schema version 1 is frozen by the phase 02 format gate (plan.md §8). Tables used only by later phases (admin, replica, snapshot) are created in version 1 so that their shape is reviewed with the formats; a later phase that needs a new column adds a migration in its own prelude and records it in its status note.

---

### Contract source: `docs/spec/wire-format.md`

## 12. Round-trip test obligation

For every op type, `Parse(Format(op))` returns `op` exactly, for a table of ops
that covers each optional key present and absent. `Parse` must reject every
malformed case with its specific error. Write this table test first.

Normalisation the test relies on: `Parse` returns nil maps when there is no
`m.` or `s.` entry; `PlaintextSize = StoredSize` for unencrypted puts; `ContentType =
"application/octet-stream"` when `ct` is absent; `EncryptionVersion = 1` with
`enc=1`; `ConfigVersion = 1` when `cv` is absent. Test fixtures use these
normalised values.

Named tests in `internal/wire`:

| Test | Asserts |
| --- | --- |
| `TestRoundTripEveryOp` | Section 10 examples and generated variants round-trip |
| `TestFormatCanonicalOrder` | `Format` output equals the section 10 strings byte for byte |
| `TestParseRejectsMalformed` | Every row of the rejected-examples table |
| `TestUnknownOpReturnsType` | `ErrWireUnknownOp` with `Type` set, for `snapshot` and every legacy op of section 11 |
| `TestUnknownKeysIgnored` | A known op with an extra `zz=1` parses to the same `Op` |
| `TestRepeatedKeyLastWins` | `k=a\|k=b` yields key `b` |
| `TestRemainderIgnored` | Text after `\n` does not change the result |
| `TestSharedAttrsOnEveryType` | `sz=-1` on `t=reg` is `ErrWireMalformed` |
| `TestTimestampBounds` | `ts=253402300799` parses; `ts=253402300800` fails |
| `TestKeyEscapingHostile` | Keys with leading and trailing spaces, `\n`, `\|`, `=`, `%`, `+`, emoji and 1024 bytes of multi-byte characters round-trip |
| `TestKeyLimits` | 1024 bytes passes, 1025 fails, invalid UTF-8 fails, U+0000 fails |
| `TestMetaEncoding` | Entries emitted sorted by name, `m.` before `s.`; unreserved bytes cost one character and each entry four extra; 2048 bytes passes, 2049 fails; uppercase name fails; `m.a` with `m.%61` fails; empty `m.` name fails; CR or LF in a value fails; tab passes; an `s.` name outside the list fails; a 1025-byte `s.` value fails |
| `TestPutFixedPartBound` | The widest put with an empty key, no `ct` and no metadata is 286 characters, at most `MaxPutFixedChars` |
| `TestKeyOf` | `put` and `del` return their key; every other type returns false |
| `TestEmptyObjectRules` | `bm=0` with each forbidden companion fails |
| `TestFormatTooLong` | A put with a 1024-byte multi-byte key and 2048 bytes of metadata returns `ErrWireTooLong`; a body is always under 1024 characters |
| `TestFitsPutMatchesFormat` | For random inputs, `FitsPut == nil` implies the real put, with any legal sizes and message IDs, formats within 4096 characters, and `PutRecordLen` is never below the real length |
| `TestHasPrefixClassification` | `hello`, `hello\|t=put` and an empty caption are not candidates; `TDX1`, `TDX1x\|t=put` and `TDX1\|t=zz` are |
| `TestHeaderIsASCII` | Every formatted header is ASCII with no `\n` and no surrounding whitespace |

Phase 02 integration (`test/acceptance/phase02`) repeats the round trip for
every op through the projector and asserts that `wire` constants equal
`internal/domain/limits` values. Phase 04 integration sends hostile keys
through the Telegram test DC behind its environment gate and asserts the text
read back is byte-identical (plan.md risk R3).

---
