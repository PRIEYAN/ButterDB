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

Implement internal/access/keys + internal/storage/keyrepo. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 08 internal access keys internal storage keyrepo` after its package tests pass.

### 5.2 Lane L2 — internal/access/sigv4 (header auth, query presign, aws-chunked streaming payloads)

Implement internal/access/sigv4 (header auth, query presign, aws-chunked streaming payloads). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 08 internal access sigv4 header auth query presi` after its package tests pass.

### 5.3 Lane L3 — internal/access/policy

Implement internal/access/policy. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 08 internal access policy` after its package tests pass.

### 5.4 Lane L4 — internal/s3api/{router,xmlcodec,s3err,bucketops}

Implement internal/s3api/{router,xmlcodec,s3err,bucketops}. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `internal/app/operation.go` | 203 | PATTERN-ONLY | `internal/s3api/s3err` (L4) | One typed error with a stable code mapped at the edge |
| `backend/services/file/upload_failures.go` | 126 | PATTERN-ONLY | `internal/s3api/s3err` (L4) | Not sent maps to a retryable 5xx; unknown outcome maps to `503 SlowDown` or `500 InternalError` per the phase 08 document |

Commit this lane as `implement phase 08 internal s3api router xmlcodec s3err bucketop` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `internal/app/operation.go`: `type OperationError struct {`; `func (e *OperationError) Error() string {`; `func (e *OperationError) Unwrap() error {`; `type OperationResult struct {`; `func (r OperationResult) Err() error {`; `type UploadResult struct {`; `type DownloadResult struct {`; `type ResumableDownloadsResult struct {`
- `backend/services/file/upload_failures.go`: `type UploadFailure struct {`; `func NotSentFailures(filePaths []string, err error) []UploadFailure {`; `func (s *Service) UploadWithFailures(ctx context.Context, channelID int64, filePaths, parentIDs []string, encrypt bool) ([]Metadata, []UploadFailure, error) {`; `func (o *failureUploadObserver) Started(id int, name string, size int64, parentID string) {`; `func (o *failureUploadObserver) Accepted(id int) {`; `func (o *failureUploadObserver) Completed(id int, name string) {`; `func (o *failureUploadObserver) Failed(id int, name string, err error) {`

### 5.5 Lane L5 — internal/s3api/{objectops,listing}

Implement internal/s3api/{objectops,listing}. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/mountdav/body_deadline.go` | 107 | PORT | `internal/s3api/objectops` (L5) | `idleDeadlineBody`: a PUT body read with an idle deadline that resets on progress, not an absolute deadline, so a long upload survives and a stalled one releases its write slot |

Commit this lane as `implement phase 08 internal s3api objectops listing` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/mountdav/body_deadline.go`: `func (body *idleDeadlineBody) Read(buffer []byte) (int, error) {`; `func (body *idleDeadlineBody) Close() error {`

### 5.6 Lane L6 — internal/s3api/multipart

Implement internal/s3api/multipart. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 08 internal s3api multipart` after its package tests pass.

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
