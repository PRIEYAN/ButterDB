# Phase 08 — S3 API
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement s3 api as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 07 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/s3-api.md`.
- `docs/spec/security.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit the HTTP handler service ports and S3 error type before fan-out. The exact request, auth, response, and Go contracts are in `docs/spec/s3-api.md` §§4–5 and 15. Add AWS SDK packages only for integration tests, through exact imported paths in §16; no runtime AWS SDK dependency is required. Commit as `define s3 api contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/08-s3-api-l1` | internal/access/keys + internal/storage/keyrepo |
| L2 | `phase/08-s3-api-l2` | internal/access/sigv4 (header auth, query presign, aws-chunked streaming payloads) |
| L3 | `phase/08-s3-api-l3` | internal/access/policy |
| L4 | `phase/08-s3-api-l4` | internal/s3api/{router,xmlcodec,s3err,bucketops} |
| L5 | `phase/08-s3-api-l5` | internal/s3api/{objectops,listing} |
| L6 | `phase/08-s3-api-l6` | internal/s3api/multipart |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — internal/access/keys + internal/storage/keyrepo

**Objective.** Issue, store, look up and revoke S3 access keys. Secrets are sealed at rest and unsealed only to verify SigV4.

**Owned paths.** `internal/access/keys/`, `internal/storage/keyrepo/`. The `access_keys` table already exists in `control/0001_init.sql` (phase 02 L4, `docs/spec/schema.md` §6); add no migration.

**Read.** `docs/spec/s3-api.md` §4.9 and §4.10; `docs/spec/security.md` §5.2 and §6; `docs/spec/schema.md` `access_keys` DDL; `docs/spec/admin-api.md` §11 and the `KeyService` port in §16.8. No TDrive source exists for this lane.

**Contracts to implement.**

```go
package keys

type Repository interface {
	Insert(ctx context.Context, rec keyrepo.Record) error
	Get(ctx context.Context, id access.AccessKeyID) (keyrepo.Record, error)
	List(ctx context.Context, includeRevoked bool) ([]keyrepo.Record, error)
	Update(ctx context.Context, id access.AccessKeyID, name *string, scope *access.Scope, now time.Time) (keyrepo.Record, error)
	Revoke(ctx context.Context, id access.AccessKeyID, now time.Time) (keyrepo.Record, error)
	TouchLastUsed(ctx context.Context, id access.AccessKeyID, now time.Time) error
}

type Sealer interface {
	SealSecret(accessKeyID string, secret []byte) ([]byte, error)
	OpenSecret(accessKeyID string, sealed []byte) ([]byte, error)
}

type Dependencies struct {
	Repo   Repository
	Sealer Sealer
	Clock  clock.Clock
	Random io.Reader
}

func NewService(deps Dependencies) (*Service, error)
func (s *Service) List(ctx context.Context, includeRevoked bool) ([]access.KeyInfo, error)
func (s *Service) Create(ctx context.Context, name string, scope access.Scope) (access.KeyInfo, string, error)
func (s *Service) Get(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error)
func (s *Service) Update(ctx context.Context, id access.AccessKeyID, name *string, scope *access.Scope) (access.KeyInfo, error)
func (s *Service) Revoke(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error)
func (s *Service) Secret(ctx context.Context, id access.AccessKeyID) ([]byte, access.KeyInfo, error)
```

`*Service` satisfies `sigv4.SecretSource` (s3-api §15.1) and the admin `KeyService` port (admin-api §16.8). `keyrepo.Record` mirrors the `access_keys` row; `keyrepo` is the only package here that touches SQL.

**Rules.**
- ID `BDBK` + 16 characters `[A-Z0-9]`; secret 30 random bytes as 40 characters of standard base64; both from the injected `Random`.
- `Create` returns the plaintext secret exactly once. It never appears in a log field, an error string or `KeyInfo`.
- Sealing goes through `keyring.Ring.SealSecret` (phase 03) with `aad = "butterdb/access/v1|" + id`; the sealed value is 80 bytes.
- In-memory lookup cache: bounded LRU of 1,024 entries, invalidated by every `Update` and `Revoke` in the same call. Scope and status are never served stale after an invalidation.
- `last_used_at` is written at most once per 60 s per key.
- Revoked key: `Secret` returns `errs.ErrAccessKeyRevoked`. Unknown key: `errs.ErrAccessKeyNotFound`. The sigv4 lane maps both to `InvalidAccessKeyId`.
- Zero unsealed secret buffers when the caller is done; the cache holds sealed bytes, never plaintext.

**Tests (binding names).** `TestCreateFormat`, `TestSecretShownOnce`, `TestSealedSecretIs80Bytes`, `TestSealBindsKeyID` (swapping sealed values between rows fails to open), `TestRevokeInvalidatesCache`, `TestUpdateScopeInvalidatesCache`, `TestLastUsedCoalesced60s`, `TestUnknownKey`, `TestRevokedKey`, `TestSecretNeverLogged`; `keyrepo`: `TestInsertGetRoundTrip`, `TestListOrderNewestFirst`, `TestRevokeSetsRevokedAt`, `TestCheckConstraintsEnforced`.

**Verify.**

```bash
go build -mod=readonly ./internal/access/keys/... ./internal/storage/keyrepo/...
go test -mod=readonly -race ./internal/access/keys/... ./internal/storage/keyrepo/...
gofmt -l internal/access/keys internal/storage/keyrepo
```

**Commits.** `add access key repository`, then `add access key service with sealed secrets`.

### 5.2 Lane L2 — internal/access/sigv4 (header auth, query presign, aws-chunked streaming payloads)

**Objective.** Verify every SigV4-authenticated S3 request (header and presigned query), and stream and verify request payloads in every mode s3-api §4.4 marks supported.

**Owned paths.** `internal/access/sigv4/` (including `testdata/`).

**Read.** `docs/spec/s3-api.md` §4.1–§4.8, §4.12, §14.2 (auth error codes), §15.1 (binding contract), §16.1 `access/sigv4` row. No TDrive source exists for this lane.

**Contract.** Implement §15.1 exactly: `AuthKind`, `PayloadMode`, `Reason`, `Error` (with `Error`/`Unwrap`), `Config`, `SecretSource`, `Auth`, `Verifier`, `NewVerifier(cfg Config, secrets SecretSource) *Verifier`, `(*Verifier).Authenticate(ctx, r) (Auth, error)`, `(*Verifier).Body(r, a) (*PayloadReader, error)`, `PayloadReader` (`Read`, `Close`, `Checksum`), `PresignInput` and `Presign(in PresignInput) (string, error)`. Do not add exported identifiers beyond §15.1 without recording them in the lane report.

**Rules.**
- Canonical URI is encoded once, with no path normalisation. Query parameters are sorted, with spaces as `%20`. Header values have collapsed whitespace. `host` comes from `r.Host`.
- Clock skew limit 15 minutes, inclusive at 14:59. Presign expiry at most 604,800 s. Region `us-east-1` is always accepted, along with the configured region.
- SigV2, SigV4A, `X-Amz-Security-Token`, and header-plus-query on the same request are all rejected with the §14.2 codes.
- `x-amz-content-sha256`: `UNSIGNED-PAYLOAD`, a signed hex digest (verified on EOF as `XAmzContentSHA256Mismatch`), `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` with per-chunk signatures, and the trailer variants per §4.6. Trailer checksums: crc32, crc32c, crc64nvme, sha1, sha256. Mismatch is `BadDigest`.
- The chunk decoder bounds its header line length and checks `x-amz-decoded-content-length` exactly; a truncated body is an error, never a short success.
- Derived signing keys are cached per `(access key, date, region)` in a bounded LRU of 1,024 entries for at most 24 h.
- The signature is never echoed in an error message (s3err owns rendering).

**Tests (binding names).** Every test in s3-api §16.1 for `access/sigv4`, including `FuzzChunkDecoder`. Copy the AWS documentation examples verbatim into `testdata/`. `TestCrossCheckWithSDKSigner` uses `github.com/aws/aws-sdk-go-v2/aws/signer/v4` as a test-only import; the prelude adds that exact package path.

**Verify.**

```bash
go test -mod=readonly -race ./internal/access/sigv4/...
go test -mod=readonly -run '^$' -fuzz FuzzChunkDecoder -fuzztime 60s ./internal/access/sigv4/
gofmt -l internal/access/sigv4
```

**Commits.** `add sigv4 canonical request and header verification`, `add sigv4 presigned query verification`, `add aws chunked payload and trailer verification`.

### 5.3 Lane L3 — internal/access/policy

**Objective.** Decide whether an authenticated principal, or an anonymous caller, may perform an S3 action on a bucket.

**Owned paths.** `internal/access/policy/`.

**Read.** `docs/spec/s3-api.md` §4.10 (action matrix and evaluation order), §4.11 (anonymous access), §10.1 (canned policies), §15.2 (binding contract), §16.1 `access/policy` row.

**Contract.** Implement §15.2 exactly: `Principal`, `BucketSource` (`Lookup(ctx, name) (bucket.Info, bool, error)`), `Evaluator`, `NewEvaluator(buckets BucketSource) *Evaluator`, `Authorize`, `AuthorizeAnonymousRead`, `VisibleBuckets`. `BucketSource` is satisfied implicitly by the phase 03 `bucketrepo` reader; do not import it.

**Rules.**
- Signed requests are evaluated in the order of §4.10: out of scope gives `AccessDenied` even when the bucket does not exist; in scope but missing gives `NoSuchBucket`; missing action gives `AccessDenied`.
- Anonymous callers may only `GetObject` and `HeadObject` on a `public-read` bucket. Anything else, including a missing bucket, is `AccessDenied` (no existence leak).
- Return the domain sentinels from `internal/domain/errs`; never HTTP statuses. `s3err.Map` translates.
- Stateless and safe for concurrent use. No caching beyond what `BucketSource` provides.

**Tests (binding names).** `TestScopeBucketOutsideDenied`, `TestScopeWildcard`, `TestMissingInScopeBucketNotFound`, `TestActionMatrix` (one case per row of §4.10), `TestAnonymousPublicReadGetOnly`, `TestAnonymousPrivateDenied`, `TestAnonymousMissingBucketDenied`, `TestVisibleBucketsFiltersByScope`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/access/policy/...
gofmt -l internal/access/policy
```

**Commit.** `add s3 access policy evaluator`.

### 5.4 Lane L4 — internal/s3api/{router,xmlcodec,s3err,bucketops}

**Objective.** Build the S3 front door: path-style and virtual-hosted target parsing, operation dispatch, XML decoding and encoding with the long-response protocol, the error mapper, and the bucket-level operations.

**Owned paths.** `internal/s3api/router/`, `internal/s3api/xmlcodec/`, `internal/s3api/s3err/`, `internal/s3api/bucketops/`.

**Read.** `docs/spec/s3-api.md` §2.2–§2.7 (server settings, addressing, host validation, readiness), §5 (request processing order), §6 (operation table, dispatch, unsupported features, CreateBucket/DeleteBucket), §7.1–§7.2, §10 (bucket configuration getters), §11 (keepalive and errors in a 200 body), §13 (CORS), §14 (errors), §15.3–§15.5 (binding contracts), §16.1 rows for `router`, `xmlcodec` and `s3err`.

Implement s3-api §15.3, §15.4 and §15.5 exactly. `bucketops` exposes handlers with the router's dispatch function type and declares its own ports for the bucket service and policy evaluator.

**Rules.**
- Do not clean the path: an encoded slash in a key stays part of the key. Virtual-hosted addressing picks the longest matching base domain.
- Every response carries `X-Content-Type-Options: nosniff`. A HEAD error has no body. The signature is never echoed.
- `Map` is total: every domain sentinel maps to a §14.2 code and status. `SlowDown` sets `Retry-After`.
- XML decoding rejects a DOCTYPE and enforces a body limit without reading past it.
- CreateBucket and DeleteBucket are admin-only over S3: `409` on an existing bucket and `403` on a new one, as §6.4 says.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `internal/app/operation.go` | 203 | PATTERN-ONLY | `internal/s3api/s3err` (L4) | One typed error with a stable code mapped at the edge |
| `backend/services/file/upload_failures.go` | 126 | PATTERN-ONLY | `internal/s3api/s3err` (L4) | Not sent maps to a retryable 5xx; unknown outcome maps to `503 SlowDown` or `500 InternalError` per the phase 08 document |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `internal/app/operation.go`: `type OperationError struct {`; `func (e *OperationError) Error() string {`; `func (e *OperationError) Unwrap() error {`; `type OperationResult struct {`; `func (r OperationResult) Err() error {`; `type UploadResult struct {`; `type DownloadResult struct {`; `type ResumableDownloadsResult struct {`
- `backend/services/file/upload_failures.go`: `type UploadFailure struct {`; `func NotSentFailures(filePaths []string, err error) []UploadFailure {`; `func (s *Service) UploadWithFailures(ctx context.Context, channelID int64, filePaths, parentIDs []string, encrypt bool) ([]Metadata, []UploadFailure, error) {`; `func (o *failureUploadObserver) Started(id int, name string, size int64, parentID string) {`; `func (o *failureUploadObserver) Accepted(id int) {`; `func (o *failureUploadObserver) Completed(id int, name string) {`; `func (o *failureUploadObserver) Failed(id int, name string, err error) {`

**Tests (binding names).** every s3-api §16.1 test for `s3api/router`, `s3api/xmlcodec` and `s3api/s3err`, plus `TestXMLBodyLimit` and `TestNosniffOnEveryResponse` (security §16), `TestBucketGettersStatic` (the §10 getters), `TestCreateBucketDenied`.

**Verify.**

```bash
go vet -mod=readonly ./internal/s3api/router/... ./internal/s3api/xmlcodec/... ./internal/s3api/s3err/... ./internal/s3api/bucketops/...
go test -mod=readonly -race ./internal/s3api/router/... ./internal/s3api/xmlcodec/... ./internal/s3api/s3err/... ./internal/s3api/bucketops/...
gofmt -l internal/s3api/router internal/s3api/xmlcodec internal/s3api/s3err internal/s3api/bucketops
```

**Commits.** `add s3 error mapping and xml codec`, `add s3 router and dispatch`, `add s3 bucket operations and configuration getters`.

### 5.5 Lane L5 — internal/s3api/{objectops,listing}

**Objective.** Serve S3 object operations (PutObject, GetObject, HeadObject, DeleteObject, DeleteObjects, CopyObject) and listings (ListBuckets, ListObjectsV2, ListObjects v1) on top of the write path, read path and repositories.

**Owned paths.** `internal/s3api/objectops/`, `internal/s3api/listing/`.

**Read.** `docs/spec/s3-api.md` §7.1, §7.3–§7.5 (listings and key characters in XML), §8.1–§8.10 (object operations, keys and metadata, ETags, Last-Modified, encryption), §10.2 (synthesised ACLs), §12.1 (budget), §16.1 rows for `objectops` and `listing`.

**Rules.**
- PutObject refuses `EntityTooLarge`, `KeyTooLongError` and `MetadataTooLarge` before reading the body. It streams the verified payload from `sigv4.PayloadReader` into `put.Service` and answers only after commit (D8).
- GetObject uses `httprange` for Range and conditionals. `response-*` overrides apply only to signed requests. An encrypted object echoes `x-amz-server-side-encryption: AES256`.
- DeleteObjects handles up to 1000 keys, supports quiet mode, and checks `delete` per key. A key that hits the budget returns a per-key `SlowDown` entry.
- CopyObject reads the source and puts the destination within the size limit. Encryption is not inherited unless requested. Copying an object onto itself without a metadata change is illegal.
- Listing follows ListObjectsV2 semantics exactly: `MaxKeys` counts prefixes, the continuation token is opaque, and `EncodingType=url` is honoured.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/mountdav/body_deadline.go` | 107 | PORT | `internal/s3api/objectops` (L5) | `idleDeadlineBody`: a PUT body read with an idle deadline that resets on progress, not an absolute deadline, so a long upload survives and a stalled one releases its write slot |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/mountdav/body_deadline.go`: `func (body *idleDeadlineBody) Read(buffer []byte) (int, error) {`; `func (body *idleDeadlineBody) Close() error {`

**Tests (binding names).** every s3-api §16.1 test for `s3api/objectops` and `s3api/listing`, plus `TestTraversalKeysNeverTouchFilesystem` (security §16, unit-level variant).

**Verify.**

```bash
go vet -mod=readonly ./internal/s3api/objectops/... ./internal/s3api/listing/...
go test -mod=readonly -race ./internal/s3api/objectops/... ./internal/s3api/listing/...
gofmt -l internal/s3api/objectops internal/s3api/listing
```

**Commits.** `add s3 put get head and delete object handlers`, `add s3 delete objects and copy object`, `add s3 bucket and object listings`.

### 5.6 Lane L6 — internal/s3api/multipart

**Objective.** Serve the S3 multipart-upload operations over HTTP on top of `internal/ingest/multipartstage` (phase 06 L6) and `internal/ingest/put` (phase 06 L2).

**Owned paths.** `internal/s3api/multipart/`.

**Read.** `docs/spec/s3-api.md` §9.1–§9.7, §11 (long responses), §12.1 (request budget), §14.2; `docs/phases/06-write-path.md` §5.6 for the `multipartstage` contract; s3-api §15.4–§15.5 for `s3err` and `xmlcodec`, which the prelude commits.

**Contracts.**

```go
package multipart

type Stage interface {
	Create(ctx context.Context, req multipartstage.CreateRequest) (multipartstage.Upload, error)
	PutPart(ctx context.Context, uploadID string, partNumber int, size int64, body io.Reader) (multipartstage.Part, error)
	Complete(ctx context.Context, uploadID string, parts []multipartstage.CompletedPart, cond multipartstage.Conditions) (object.ObjectMeta, error)
	Abort(ctx context.Context, uploadID string) error
	ListParts(ctx context.Context, uploadID string, marker int, max int) (multipartstage.PartPage, error)
	ListUploads(ctx context.Context, bucketChannelID int64, q multipartstage.UploadQuery) (multipartstage.UploadPage, error)
}

type Dependencies struct {
	Stage    Stage
	Verifier PayloadOpener
	Clock    clock.Clock
	Config   Config
}

type PayloadOpener interface {
	Body(r *http.Request, a sigv4.Auth) (*sigv4.PayloadReader, error)
}

func NewHandlers(deps Dependencies) (*Handlers, error)
func (h *Handlers) CreateMultipartUpload(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth)
func (h *Handlers) UploadPart(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth)
func (h *Handlers) CompleteMultipartUpload(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth)
func (h *Handlers) AbortMultipartUpload(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth)
func (h *Handlers) ListParts(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth)
func (h *Handlers) ListMultipartUploads(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth)
```

The handler signature must match the dispatch function type the prelude commits in `internal/s3api/router`. If the prelude's type differs, follow the prelude and record it in the lane report.

**Rules.**
- Validate part number (1–10000) and the running stored-size total **before** reading the body (`EntityTooLarge` early).
- Complete validates everything in the §9.4 table before the first response byte. Then it calls `xmlcodec.StartLongResponse` with a 10 s keepalive, and reports the job outcome inside the `200` body exactly as the §9.4 outcome table says.
- State handling on Complete follows the §9.4 state table: attach to a `committing` job, never start a second one, and replay a completed result for an identical part list.
- Abort of a `committing` upload is `409 OperationAborted`.
- UploadPartCopy returns `NotImplemented` unless s3-api §9.3 marks it supported; follow §9.3.
- Telegram flood waits past the request budget surface as `SlowDown` with `Retry-After`; never hold the request open past the budget.

**Tests (binding names).** Every s3-api §16.1 `s3api/multipart` test: `TestPartNumberRange`, `TestEntityTooSmall`, `TestInvalidPartOrder`, `TestInvalidPartETag`, `TestPartChecksumVerified`, `TestRunningTotalLimit`, `TestCompleteIdempotentRetry`, `TestCompleteAttachesToCommitting`, `TestAbortWhileCommitting`, `TestMultipartETag`. Add `TestCompleteKeepaliveThenSuccess` and `TestCompleteSlowDownInBody`, using a fake `Stage`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/s3api/multipart/...
gofmt -l internal/s3api/multipart
```

**Commits.** `add s3 multipart create upload part and abort handlers`, `add s3 complete multipart with keepalive responses`, `add s3 multipart listing handlers`.

## 6. Integration lane

core wiring, cmd serve wiring of the S3 listener, test/acceptance/phase08 (aws-sdk-go-v2 against faketg-backed server)
Own `internal/core`, `cmd/`, and `test/acceptance/phase08` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase08/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestAwsSdkGoV2CanPUTGETHEADLISTAndDELETE`: aws-sdk-go-v2 can PUT GET HEAD LIST and DELETE.
  - `TestSigV4HeaderAndPresignedRequestsAuthenticate`: SigV4 header and presigned requests authenticate.
  - `TestMultipartCompletesToOneObject`: multipart completes to one object.
  - `TestXMLErrorsCarryStableS3Codes`: XML errors carry stable S3 codes.
### TDrive regression tests to adapt

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/mountdav/body_deadline_test.go` | 218 | Full | All five |

## 8. Owner gates

None beyond the entry criteria and any explicit gates recorded in the applicable specification.

## 9. Exit criteria and handoff

All L1–L6 commits have passed monitoring and review; the integration build and phase 08 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 09.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding contracts for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/s3-api.md`

## 15. Go contracts

Exact exported signatures that phase 08 lanes and the integration lane share.
No comments, per the code policy.

### 15.1 `internal/access/sigv4`

```go
package sigv4

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
)

type AuthKind int

const (
	AuthAnonymous AuthKind = iota
	AuthHeader
	AuthPresigned
)

type PayloadMode int

const (
	PayloadNone PayloadMode = iota
	PayloadSignedHex
	PayloadUnsigned
	PayloadStreamingSigned
	PayloadStreamingSignedTrailer
	PayloadStreamingUnsignedTrailer
)

type Reason int

const (
	ReasonSignature Reason = iota + 1
	ReasonUnknownKey
	ReasonHeaderMalformed
	ReasonQueryMalformed
	ReasonWrongRegion
	ReasonSkew
	ReasonExpired
	ReasonNotYetValid
	ReasonMissingDate
	ReasonMissingContentSHA256
	ReasonBadContentSHA256
	ReasonContentSHA256Mismatch
	ReasonSecurityToken
	ReasonSigV2
	ReasonSigV4A
	ReasonBothMechanisms
	ReasonUnsupportedScheme
	ReasonChunkFraming
	ReasonIncompleteBody
	ReasonChecksumMismatch
	ReasonChecksumMalformed
)

type Error struct {
	Reason           Reason
	Err              error
	AccessKeyID      string
	StringToSign     string
	CanonicalRequest string
	Region           string
	RequestTime      time.Time
	ServerTime       time.Time
	Expires          time.Time
	Algorithm        string
	ClientSHA256     string
	ServerSHA256     string
}

func (e *Error) Error() string
func (e *Error) Unwrap() error

type Config struct {
	Region     string
	MaxSkew    time.Duration
	MaxPresign time.Duration
	Now        func() time.Time
}

type SecretSource interface {
	Secret(ctx context.Context, id access.AccessKeyID) ([]byte, access.KeyInfo, error)
}

type Auth struct {
	Kind          AuthKind
	Key           access.KeyInfo
	Mode          PayloadMode
	ContentSHA256 string
	DecodedLength int64
	Trailer       string
	RequestTime   time.Time
	Region        string
	seed          string
	signingKey    []byte
}

type Verifier struct {
}

func NewVerifier(cfg Config, secrets SecretSource) *Verifier
func (v *Verifier) Authenticate(ctx context.Context, r *http.Request) (Auth, error)
func (v *Verifier) Body(r *http.Request, a Auth) (*PayloadReader, error)

type PayloadReader struct {
}

func (p *PayloadReader) Read(b []byte) (int, error)
func (p *PayloadReader) Close() error
func (p *PayloadReader) Checksum() (algorithm string, value string)

type PresignInput struct {
	Method      string
	URL         *url.URL
	AccessKeyID access.AccessKeyID
	Secret      []byte
	Region      string
	Time        time.Time
	Expires     time.Duration
}

func Presign(in PresignInput) (string, error)

var _ io.ReadCloser = (*PayloadReader)(nil)
```

- `Config.MaxSkew` is 15 minutes and `Config.MaxPresign` is 7 days; `Region`
  is `s3.region` (`us-east-1` is always accepted, §3).
- `Authenticate` never reads the body. It returns `AuthAnonymous` with a nil
  error when no credentials are present.
- `Body` returns a reader yielding decoded object bytes. Signature, hash and
  checksum failures are returned from `Read` as `*Error` no later than at EOF,
  and always before `Read` returns `io.EOF`. `Checksum` reports the verified
  `x-amz-checksum-*` after EOF.
- `Error.Err` is the `internal/domain/errs` sentinel; `s3err` maps
  `Reason` to the S3 code (§14.2).

### 15.2 `internal/access/policy`

```go
package policy

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
)

type Principal struct {
	Anonymous bool
	Key       access.KeyInfo
}

type BucketSource interface {
	Lookup(ctx context.Context, name bucket.BucketName) (bucket.Info, bool, error)
}

type Evaluator struct {
}

func NewEvaluator(buckets BucketSource) *Evaluator
func (e *Evaluator) Authorize(ctx context.Context, p Principal, b bucket.BucketName, a access.Action) (bucket.Info, error)
func (e *Evaluator) AuthorizeAnonymousRead(ctx context.Context, b bucket.BucketName) (bucket.Info, error)
func (e *Evaluator) VisibleBuckets(ctx context.Context, p Principal) ([]bucket.Info, error)
```

`Authorize` implements §4.10 steps 1 to 3, returning `errs.ErrAccessDenied` or
`errs.ErrNoSuchBucket`. `AuthorizeAnonymousRead` returns
`errs.ErrAccessDenied` for a missing or private bucket.

### 15.3 `internal/s3api/router`

```go
package router

import "net/http"

type Operation string

const (
	OpListBuckets             Operation = "ListBuckets"
	OpCreateBucket            Operation = "CreateBucket"
	OpHeadBucket              Operation = "HeadBucket"
	OpDeleteBucket            Operation = "DeleteBucket"
	OpGetBucketLocation       Operation = "GetBucketLocation"
	OpListObjectsV2           Operation = "ListObjectsV2"
	OpListObjects             Operation = "ListObjects"
	OpPutObject               Operation = "PutObject"
	OpGetObject               Operation = "GetObject"
	OpHeadObject              Operation = "HeadObject"
	OpDeleteObject            Operation = "DeleteObject"
	OpDeleteObjects           Operation = "DeleteObjects"
	OpCopyObject              Operation = "CopyObject"
	OpCreateMultipartUpload   Operation = "CreateMultipartUpload"
	OpUploadPart              Operation = "UploadPart"
	OpUploadPartCopy          Operation = "UploadPartCopy"
	OpCompleteMultipartUpload Operation = "CompleteMultipartUpload"
	OpAbortMultipartUpload    Operation = "AbortMultipartUpload"
	OpListParts               Operation = "ListParts"
	OpListMultipartUploads    Operation = "ListMultipartUploads"
	OpGetBucketPolicy         Operation = "GetBucketPolicy"
	OpGetBucketPolicyStatus   Operation = "GetBucketPolicyStatus"
	OpGetBucketCors           Operation = "GetBucketCors"
	OpGetBucketAcl            Operation = "GetBucketAcl"
	OpPutBucketAcl            Operation = "PutBucketAcl"
	OpGetObjectAcl            Operation = "GetObjectAcl"
	OpPutObjectAcl            Operation = "PutObjectAcl"
	OpGetBucketVersioning     Operation = "GetBucketVersioning"
	OpGetBucketEncryption     Operation = "GetBucketEncryption"
	OpGetObjectTagging        Operation = "GetObjectTagging"
	OpPutObjectTagging        Operation = "PutObjectTagging"
	OpDeleteObjectTagging     Operation = "DeleteObjectTagging"
	OpGetBucketConfig         Operation = "GetBucketConfig"
	OpPreflight               Operation = "Preflight"
)

type Target struct {
	Bucket        string
	Key           string
	HasKey        bool
	VirtualHosted bool
}

type Route struct {
	Target      Target
	Operation   Operation
	Subresource string
}

type Addressing struct {
	BaseDomains  []string
	AllowedHosts []string
	LoopbackOnly bool
}

func ParseTarget(r *http.Request, a Addressing) (Target, error)
func Resolve(r *http.Request, t Target) (Route, error)
```

`OpGetBucketConfig` covers the fixed getters of §10.6 with `Subresource`
naming which. `Resolve` returns an error mapping to `405 MethodNotAllowed` or
`501 NotImplemented` per §6.2. The action for each operation is the table in
§4.10, held as a `map[Operation]access.Action` in `router`.

### 15.4 `internal/s3api/s3err`

```go
package s3err

import (
	"net/http"
	"time"
)

type Code string

type Field struct {
	Name  string
	Value string
}

type APIError struct {
	Code       Code
	Status     int
	Message    string
	BucketName string
	Key        string
	Resource   string
	RetryAfter time.Duration
	Extra      []Field
}

func Map(err error) APIError
func Encode(e APIError, requestID, hostID string) []byte
func Write(w http.ResponseWriter, r *http.Request, e APIError)
```

`Code` constants are exactly the `Code` column of §14.2, named
`Code<CodeValue>` (`CodeNoSuchKey`, `CodeSlowDown` ...). `Encode` returns the
`Error` element without an XML declaration; `Write` adds the declaration,
headers and `Retry-After`, and writes no body for HEAD.

### 15.5 `internal/s3api/xmlcodec`

```go
package xmlcodec

import (
	"io"
	"net/http"
	"time"
)

const Namespace = "http://s3.amazonaws.com/doc/2006-03-01/"

const TimeFormat = "2006-01-02T15:04:05.000Z"

func Decode(r io.Reader, limit int64, v any) error
func WriteResult(w http.ResponseWriter, status int, v any) error

type LongResponse struct {
}

func StartLongResponse(w http.ResponseWriter, interval time.Duration) (*LongResponse, error)
func (l *LongResponse) Succeed(v any) error
func (l *LongResponse) Fail(errorXML []byte) error
```

`Decode` rejects DOCTYPE, bodies over `limit` (mapped to
`MaxMessageLengthExceeded`) and empty bodies (`MissingRequestBodyError`), and
accepts elements with or without the S3 namespace. `StartLongResponse`
implements §11 steps 2 and 3; `Succeed` and `Fail` stop the keepalive and
write step 4. `Fail` takes `s3err.Encode` output so `xmlcodec` does not import
`s3err`.

---
