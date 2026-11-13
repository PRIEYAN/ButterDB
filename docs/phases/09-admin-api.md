# Phase 09 — Admin API and server-side admin auth
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement admin api and server-side admin auth as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 08 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/admin-api.md`.
- `docs/spec/security.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit all `api/adminv1` DTOs from `docs/spec/admin-api.md` §16 before fan-out. They are the shared wire contract between the server and desktop client. Keep `api/adminv1` independent of `internal/`. Commit as `define admin api v1 contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/09-admin-api-and-auth-l1` | internal/adminapi/setup (setup token claim, api_id save, relayed login steps) |
| L2 | `phase/09-admin-api-and-auth-l2` | internal/adminapi/session + internal/storage/adminrepo (device tokens, pairing via Saved Messages code, admin-token CLI code) |
| L3 | `phase/09-admin-api-and-auth-l3` | internal/adminapi/tlscert (self-signed cert, fingerprint) |
| L4 | `phase/09-admin-api-and-auth-l4` | internal/adminapi/handlers |
| L5 | `phase/09-admin-api-and-auth-l5` | api/adminv1 client (client.go and friends; DTO files belong to the prelude) |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — internal/adminapi/setup (setup token claim, api_id save, relayed login steps)

**Objective.** Own the server-claim state and the Telegram link relay: mint and log the setup token, let exactly one GUI claim the server, and relay api_id/api_hash, phone, code and 2FA steps to `internal/identity` without exposing session material.

**Owned paths.** `internal/adminapi/setup/`.

**Read.** `docs/spec/admin-api.md` §5.1 (claim states), §5.2 (setup token), §5.7 (`HashSecret`), §7.1–§7.2, §8.1–§8.8 (relay state machine and endpoints), §4.2 (codes), and the `LoginRelay` port in §16.8; `docs/spec/security.md` §7. TDrive reference for the login steps behind the relay: `/home/prieyan/weeb/TDrive/backend/auth/auth.go` (`StartLogin`, `signInWithPassword`). Phase 04's `internal/identity` already implements the state machine; this lane only relays.

**Contracts.**

```go
package setup

type LoginRelay interface {
	Status(ctx context.Context) (identity.Snapshot, error)
	SaveCredentials(ctx context.Context, apiID int, apiHash string) error
	DeleteCredentials(ctx context.Context) error
	Start(ctx context.Context, phone string) (identity.Snapshot, error)
	SubmitCode(ctx context.Context, loginID, code string) (identity.Snapshot, error)
	SubmitPassword(ctx context.Context, loginID, password string) (identity.Snapshot, error)
	Cancel(ctx context.Context) (identity.Snapshot, error)
	Unlink(ctx context.Context) (identity.Snapshot, error)
}

type ClaimStore interface {
	ClaimState(ctx context.Context) (string, error)
	ReplaceSetupToken(ctx context.Context, hash [32]byte, expiresAt time.Time) error
	ConsumeSetupTokenAndAddDevice(ctx context.Context, hash [32]byte, now time.Time, dev adminrepo.NewDevice) error
}

type Dependencies struct {
	Relay   LoginRelay
	Claims  ClaimStore
	Tokens  DeviceIssuer
	Clock   clock.Clock
	Random  io.Reader
	Logger  *slog.Logger
	TTL     time.Duration
}

type DeviceIssuer interface {
	NewDeviceToken() (token string, deviceID string, hash [32]byte, err error)
}

func New(deps Dependencies) (*Service, error)
func (s *Service) MintSetupTokenIfUnclaimed(ctx context.Context) (string, time.Time, error)
func (s *Service) Claim(ctx context.Context, token string, device adminv1.DeviceInfo) (adminv1.ClaimResult, error)
func (s *Service) Relay() LoginRelay
```

`DeviceIssuer` is satisfied by `*session.Tokens` (L2). `ClaimStore` is satisfied by `adminrepo` (L2). Declare both here, and never import L2 code.

**Rules.**
- Token format `bdst1_` + 32 base32 characters (20 random bytes). Store only `HashSecret(KindSetup, token)`. Log the token exactly once at `WARN`, with the §5.2 field names.
- The claim consumes the token and inserts the first device in **one** transaction. A second claim gets `already_claimed`. Compare in constant time.
- The relay never returns or logs api_hash, the phone code hash, the password, session bytes, or the full phone number. `identity.Snapshot` has no field for them.
- `PHONE_CODE_INVALID` and a wrong password are retryable in place, up to 5 attempts each. A code expiry ends the attempt. A submit in the wrong stage is rejected with the §4.2 code.
- An attempt times out after 15 minutes. If a different account than the linked one logs in, the result is `account_mismatch`.

**Tests (binding names).** Every admin-api §17 `setup` test: `TestSetupTokenPrintedOnce`, `TestSetupTokenReplacedOnRestart`, `TestClaimConsumesToken`, `TestClaimAfterClaimed`, `TestCodeInvalidRetryInPlace`, `TestPasswordInvalidRetryInPlace`, `TestAttemptsExhausted`, `TestCodeExpiredEndsAttempt`, `TestSubmitInWrongStageRejected`, `TestAttemptTimeout`, `TestAccountMismatch`, `TestStatusNeverContainsSecrets` (driven by `faketg` through a real `identity` service), `TestUnlinkCallsLogOutBestEffort`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/adminapi/setup/...
gofmt -l internal/adminapi/setup
```

**Commits.** `add setup token minting and server claim`, `add telegram login relay for the admin api`.

### 5.2 Lane L2 — internal/adminapi/session + internal/storage/adminrepo (device tokens, pairing via Saved Messages code, admin-token CLI code)

**Objective.** Authenticate every admin request with device tokens, issue and rotate them, pair additional devices with one-time codes, serve the local control socket that `butterdbd admin-token` uses, rate-limit authentication, and persist all of it in `control.db`.

**Owned paths.** `internal/adminapi/session/`, `internal/storage/adminrepo/`. The admin tables already exist in `control/0001_init.sql` (phase 02 L4, `docs/spec/schema.md` §5.2 and §6); add no migration.

**Read.** `docs/spec/admin-api.md` §5.3 (device tokens), §5.4 (pairing), §5.5 (control socket), §5.6 (rate limits), §5.7 (`HashSecret`), §7.3–§7.5, §15 (tables `admin_meta`, `admin_setup_token`, `admin_devices`, `admin_pairing_codes`, `admin_audit`), §16.3 DTOs; `docs/spec/schema.md` for the same tables (schema.md wins on column names); `docs/spec/security.md` §7.

**Contracts.**

```go
package session

type SecretKind string

const (
	KindSetup   SecretKind = "setup"
	KindDevice  SecretKind = "device"
	KindPairing SecretKind = "pairing"
)

func HashSecret(kind SecretKind, normalised string) [32]byte
func NormalisePairingCode(input string) (string, error)

type DeviceStore interface {
	Device(ctx context.Context, id string) (adminrepo.Device, error)
	InsertDevice(ctx context.Context, d adminrepo.NewDevice) error
	Rotate(ctx context.Context, id string, newHash [32]byte, now time.Time) error
	MarkNewTokenUsed(ctx context.Context, id string) error
	Revoke(ctx context.Context, id string, now time.Time) error
	TouchSeen(ctx context.Context, id string, ip string, now time.Time) error
	RevokeIdle(ctx context.Context, before time.Time) (int, error)
	ReplacePairingCode(ctx context.Context, delivery string, hash [32]byte, expiresAt time.Time) error
	ConsumePairingCode(ctx context.Context, hash [32]byte, now time.Time, d adminrepo.NewDevice) error
	RecordPairingFailure(ctx context.Context) (consecutive int, err error)
	InvalidatePairingCodes(ctx context.Context) error
}

type SavedMessagesSender interface {
	SendToSavedMessages(ctx context.Context, text string) error
}

type Tokens struct{}

func NewTokens(random io.Reader) *Tokens
func (t *Tokens) NewDeviceToken() (token string, deviceID string, hash [32]byte, err error)

func NewAuthenticator(store DeviceStore, limits *Limiter, clock clock.Clock) *Authenticator
func (a *Authenticator) Middleware(next http.Handler) http.Handler
func DeviceFromContext(ctx context.Context) (adminrepo.Device, bool)

func NewPairing(store DeviceStore, sender SavedMessagesSender, tokens *Tokens, clock clock.Clock, random io.Reader) *Pairing
func (p *Pairing) Request(ctx context.Context, delivery string, device adminv1.DeviceInfo) (time.Time, error)
func (p *Pairing) Complete(ctx context.Context, code string, device adminv1.DeviceInfo) (adminv1.DeviceTokenResult, error)
func (p *Pairing) HostCode(ctx context.Context) (string, time.Time, error)

func NewLimiter(cfg LimiterConfig, clock clock.Clock) *Limiter

func ServeControlSocket(ctx context.Context, path string, h ControlHandler) error
type ControlHandler interface {
	AdminToken(ctx context.Context) (kind string, value string, expiresAt time.Time, err error)
}
```

`SavedMessagesSender` is satisfied by the phase 04 `tgclient/rpc` client. Declare it here.

**Rules.**
- Device token `bdt1_<16 base32 id>_<52 base32 secret>`. Store only `HashSecret(KindDevice, token)`. Accept it only from `Authorization: Bearer`.
- Middleware order exactly as §5.3. A failed check counts against the `auth_failure` limiter before the response is written.
- Rotation keeps `prev_token_hash` valid for 10 minutes, or until the new token is first used.
- `last_seen` writes are coalesced to at most one per device per 60 s.
- Pairing codes are Crockford base32, 10 characters, valid 10 minutes, single use, at most one active per delivery method. Five consecutive failures invalidate every code. Saved Messages delivery is limited to 5 sends per hour. If the send fails, delete the code row.
- The control socket has mode `0600` and speaks one JSON line per connection (§5.5). It returns a setup token while unclaimed and a pairing code once claimed.
- No SQLite transaction is held while sending to Telegram.

**Tests (binding names).** Every admin-api §17 `session` test: `TestDeviceTokenFormat`, `TestSecretHashDomainSeparated`, `TestRotateGraceWindowEndsOnFirstUse`, `TestRotateGraceWindowEndsAfterTenMinutes`, `TestRevokedDeviceRejected`, `TestLastSeenCoalesced`, `TestIdleRevokeSweeper`, `TestPairingCodeNormalisation`, `TestPairingCodeSingleUse`, `TestPairingCodeExpiry`, `TestFiveFailuresInvalidateCodes`, `TestSavedMessagesSendFailureDeletesCode`, `TestRateLimiterPerScope`, `TestAuthFailureBlock`, `TestControlSocketMode0600`, `TestAdminTokenWithoutRunningServer`. For `adminrepo`, add `TestAdminTablesPresent` and `TestConsumePairingCodeAtomic`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/adminapi/session/... ./internal/storage/adminrepo/...
gofmt -l internal/adminapi/session internal/storage/adminrepo
```

**Commits.** `add admin repository`, `add device tokens and auth middleware`, `add device pairing and control socket`.

### 5.3 Lane L3 — internal/adminapi/tlscert (self-signed cert, fingerprint)

**Objective.** Provide the admin listener's TLS material: a P-256 key generated once and a self-signed certificate renewed with the same key. The SPKI fingerprint is what the GUI pins (A3).

**Owned paths.** `internal/adminapi/tlscert/`.

**Read.** `docs/spec/admin-api.md` §2.2–§2.3 (contract and fingerprint forms); `docs/spec/operations.md` data-directory rows `tls/admin.key` and `tls/admin.crt`; `docs/spec/security.md` §7.

**Contract (binding, from §2.3).**

```go
package tlscert

type Material struct {
	Certificate tls.Certificate
	SPKISHA256  [32]byte
	NotAfter    time.Time
}

func LoadOrCreate(dir string, serverID string, hostnames []string, now time.Time) (Material, error)
func Fingerprint(spki [32]byte) string
func DisplayFingerprint(spki [32]byte) string
```

**Rules.**
- ECDSA P-256 key as PEM PKCS#8 in `tls/admin.key` (mode `0600`), written through `internal/platform/secretfile`. Certificate as PEM in `tls/admin.crt` (mode `0644`).
- CN `butterdbd admin <server_id>`. SANs: `localhost`, `127.0.0.1`, `::1`, `os.Hostname()`, and each configured hostname. Validity 825 days.
- On load, reissue **with the same key** if fewer than 30 days remain or the SAN set changed. The fingerprint must not change on renewal.
- Fingerprint: SHA-256 of the leaf's DER `SubjectPublicKeyInfo`. `Fingerprint` returns `SHA256:` + 64 lowercase hex characters. `DisplayFingerprint` returns 16 space-separated groups of 4 hex characters.
- Reject a key file with group or other permission bits set.

**Tests (binding names).** `TestLoadOrCreateGeneratesP256Key`, `TestRenewalKeepsSPKI`, `TestSANChangeReissues`, `TestKeyFileMode0600`, `TestFingerprintFormats`, plus `TestLoadExistingIsStable` and `TestRejectsGroupReadableKey`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/adminapi/tlscert/...
gofmt -l internal/adminapi/tlscert
```

**Commit.** `add admin tls certificate with stable spki pin`.

### 5.4 Lane L4 — internal/adminapi/handlers

**Objective.** Implement the admin HTTP API: router, JSON codec, error envelope, and every resource endpoint in admin-api §6 except the replica routes, which answer `501 not_implemented` until phase 12.

**Owned paths.** `internal/adminapi/handlers/`, except `replica.go` and `replica_test.go`, which phase 12 L6 adds later.

**Read.** `docs/spec/admin-api.md` §2.5 (hardening), §3 (conventions), §4 (envelope and code table), §6 (endpoint index), §7, §9–§12, §14, §16.2–§16.6 (DTOs, committed by the prelude in `api/adminv1`), and §16.8 (ports declared here).

**Contracts.** Declare exactly the ports listed in admin-api §16.8 (`BucketService`, `KeyService`, `ObjectLister`, `ObjectWriter`, `ObjectServer`, `Presigner`, `StatusSource`, `AuditLog`), plus:

```go
package handlers

type Dependencies struct {
	Buckets  BucketService
	Keys     KeyService
	Lister   ObjectLister
	Writer   ObjectWriter
	Server   ObjectServer
	Presign  Presigner
	Status   StatusSource
	Audit    AuditLog
	Setup    SetupEndpoints
	Pairing  PairingEndpoints
	Auth     func(http.Handler) http.Handler
	Clock    clock.Clock
	Config   Config
}

type SetupEndpoints interface {
	ServerInfo(ctx context.Context) (adminv1.ServerInfo, error)
	Claim(ctx context.Context, token string, device adminv1.DeviceInfo) (adminv1.ClaimResult, error)
	Relay() setup.LoginRelay
}

type PairingEndpoints interface {
	Request(ctx context.Context, delivery string, device adminv1.DeviceInfo) (time.Time, error)
	Complete(ctx context.Context, code string, device adminv1.DeviceInfo) (adminv1.DeviceTokenResult, error)
}

func NewRouter(deps Dependencies) (http.Handler, error)
func MapError(err error) adminv1.APIError
```

`SetupEndpoints` and `PairingEndpoints` mirror the L1 and L2 methods. Import only `internal/adminapi/setup` for the `LoginRelay` type; never call L1 or L2 constructors.

**Rules.**
- JSON bodies are capped at 1 MiB, and unknown fields are rejected. The object key travels in the `key` query parameter, never in the path.
- Every error uses the §4.1 envelope, and every domain sentinel maps to a §4.2 code. `MapError` must be total.
- Uploads require `Content-Length`, stream to `ObjectWriter.Put` without buffering, and are acknowledged only after commit (D8). Idempotent replays honour `Idempotency-Key` scoped by device (E9).
- Deleting a missing object returns `204`. Batch delete reports per-key results.
- Presign requires that the chosen access key holds `read`.
- Every mutation writes an audit record. Audit records contain no secrets.
- Replica routes return `501 not_implemented` (`TestReplicaRoutes501BeforePhase12`).

**Tests (binding names).** Every admin-api §17 `handlers` test: `TestErrorEnvelopeShape`, `TestEveryCodeMapped`, `TestUnknownFieldRejected`, `TestJSONBodyLimit`, `TestObjectKeyFromQuery`, `TestUploadRequiresContentLength`, `TestUploadAcknowledgedAfterCommit`, `TestUploadIdempotentReplay`, `TestUploadIdempotencyConflict`, `TestUploadCommitOutcomeUnknown`, `TestDeleteMissingObject204`, `TestBatchDeletePartialFailure`, `TestPresignRequiresReadScope`, `TestReplicaRoutes501BeforePhase12`, `TestAuditRecordsMutations`. Use hand-written fakes for every port.

**Verify.**

```bash
go test -mod=readonly -race ./internal/adminapi/handlers/...
gofmt -l internal/adminapi/handlers
```

**Commits.** `add admin api router codec and error mapping`, `add admin server claim pairing and device endpoints`, `add admin bucket key and object endpoints`, `add admin status usage and audit endpoints`.

### 5.5 Lane L5 — api/adminv1 client (client.go and friends; DTO files belong to the prelude)

**Objective.** Write the Go client in `api/adminv1` that the Fyne app (phase 10) uses, including TLS pinning and certificate probing. The DTO files already exist from the prelude; do not edit them.

**Owned paths.** `api/adminv1/client.go`, `api/adminv1/client_objects.go`, `api/adminv1/client_test.go`, `api/adminv1/trust.go`, `api/adminv1/trust_test.go`. Every other file in `api/adminv1` belongs to the prelude.

**Read.** `docs/spec/admin-api.md` §16.1 (file list), §16.7 (binding client contract and client rules table), §4.1 (envelope), §5.3 (bearer only).

**Contract.** Implement every `func (c *Client) ...` method listed in admin-api §16.7, plus `NewClient`, `Trust`, `ProbeCertificate`, `PinMismatchError` and `APIError` decoding, exactly as specified there. `api/...` imports only the standard library.

**Rules (from the §16.7 client rules table).**
- Pinned trust: TLS 1.3 minimum, `InsecureSkipVerify` with a `VerifyConnection` that compares the leaf SPKI SHA-256 in constant time. On mismatch, return `*PinMismatchError` before any request byte is sent.
- `http://` is accepted only for `localhost`, `127.0.0.1` or `::1`.
- `ProbeCertificate` completes a handshake and sends no HTTP request.
- `PutObject` sets `Content-Length` and never buffers. The client never retries by itself.
- The JSON timeout defaults to 75 s. Streaming methods use only the caller's context.
- The token is never part of an error string, and `SetToken` is safe for concurrent use.

**Tests (binding names).** `TestPinnedTrustAcceptsMatchingSPKI`, `TestPinnedTrustRejectsMismatchBeforeRequest`, `TestProbeCertificateSendsNoRequest`, `TestAPIErrorDecoding`, `TestHeadErrorFromHeader`, `TestPutObjectStreamsWithoutBuffering`, `TestTokenNotInErrorText`, plus `TestPlainHTTPOnlyLoopback`. Use `httptest.NewUnstartedServer` with TLS.

**Verify.**

```bash
go test -mod=readonly -race ./api/adminv1/...
gofmt -l api/adminv1
test "$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./api/adminv1 | sort -u)" = "github.com/PRIEYAN/ButterDB/api/adminv1"
```

**Commits.** `add admin api go client with pinned trust`, `add admin api client object streaming`.

## 6. Integration lane

core wiring, cmd admin-token subcommand, admin listener, test/acceptance/phase09
Own `internal/core`, `cmd/`, and `test/acceptance/phase09` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase09/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestSetupTokenIsOneTime`: setup token is one-time.
  - `TestDevicePairingCodeExpires`: device pairing code expires.
  - `TestCertificateFingerprintIsPinned`: certificate fingerprint is pinned.
  - `TestAdminTokenScopesAndAuditEventsAreEnforced`: admin token scopes and audit events are enforced.
## 8. Owner gates

None beyond the entry criteria and any explicit gates recorded in the applicable specification.

## 9. Exit criteria and handoff

All L1–L5 commits have passed monitoring and review; the integration build and phase 09 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 10.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding contracts for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/admin-api.md`

## 16. Go packages and DTOs

### 16.1 `api/adminv1` files

| File | Owner | Contents |
| --- | --- | --- |
| `paths.go` | phase 09 prelude | Route constants, header names |
| `errors.go` | phase 09 prelude | `ErrorEnvelope`, `ErrorBody`, `Code` constants, `APIError` |
| `auth.go` | phase 09 prelude | Server info, claim, pairing, devices |
| `telegram.go` | phase 09 prelude | Telegram link relay DTOs |
| `status.go` | phase 09 prelude | `Status`, `Usage` |
| `buckets.go` | phase 09 prelude | Bucket DTOs |
| `keys.go` | phase 09 prelude | Access key DTOs |
| `objects.go` | phase 09 prelude | Object DTOs, upload options |
| `audit.go` | phase 09 prelude | Audit DTOs |
| `replica.go` | phase 12 L6 | Replica DTOs |
| `client.go`, `trust.go`, `transport.go`, `client_*.go` | phase 09 L5 | Go client |

`api/adminv1` imports the standard library only.

### 16.2 Shared and error types

```go
package adminv1

const (
	BasePath             = "/admin/v1"
	HeaderRequestID      = "X-Request-Id"
	HeaderErrorCode      = "X-Butterdb-Error-Code"
	HeaderEncryption     = "X-Butterdb-Encryption"
	HeaderContentSHA256  = "X-Butterdb-Content-Sha256"
	HeaderMetaPrefix     = "X-Butterdb-Meta-"
	HeaderEncrypted      = "X-Butterdb-Encrypted"
	HeaderVersionID      = "X-Butterdb-Version-Id"
	HeaderIdempotencyKey = "Idempotency-Key"
)

type Code string

const (
	CodeBadRequest                   Code = "bad_request"
	CodeValidationFailed             Code = "validation_failed"
	CodeUnauthenticated              Code = "unauthenticated"
	CodeDeviceRevoked                Code = "device_revoked"
	CodeNotFound                     Code = "not_found"
	CodeMethodNotAllowed             Code = "method_not_allowed"
	CodeMisdirectedRequest           Code = "misdirected_request"
	CodeLengthRequired               Code = "length_required"
	CodePayloadTooLarge              Code = "payload_too_large"
	CodeUnsupportedMediaType         Code = "unsupported_media_type"
	CodeRateLimited                  Code = "rate_limited"
	CodeInternal                     Code = "internal"
	CodeServerNotReady               Code = "server_not_ready"
	CodeNotImplemented               Code = "not_implemented"
	CodeAlreadyClaimed               Code = "already_claimed"
	CodeNotClaimed                   Code = "not_claimed"
	CodeSetupTokenInvalid            Code = "setup_token_invalid"
	CodePairingCodeInvalid           Code = "pairing_code_invalid"
	CodePairingUnavailable           Code = "pairing_unavailable"
	CodeLastDevice                   Code = "last_device"
	CodeDeviceNotFound               Code = "device_not_found"
	CodeTelegramNotLinked            Code = "telegram_not_linked"
	CodeTelegramUnavailable          Code = "telegram_unavailable"
	CodeTelegramFloodWait            Code = "telegram_flood_wait"
	CodeTelegramCredentialsMissing   Code = "telegram_credentials_missing"
	CodeTelegramAlreadyLinked        Code = "telegram_already_linked"
	CodeTelegramLoginInProgress      Code = "telegram_login_in_progress"
	CodeTelegramLoginNotActive       Code = "telegram_login_not_active"
	CodeTelegramAPIIDInvalid         Code = "telegram_api_id_invalid"
	CodeTelegramPhoneInvalid         Code = "telegram_phone_invalid"
	CodeTelegramPhoneBanned          Code = "telegram_phone_banned"
	CodeTelegramAccountNotRegistered Code = "telegram_account_not_registered"
	CodeTelegramCodeInvalid          Code = "telegram_code_invalid"
	CodeTelegramCodeExpired          Code = "telegram_code_expired"
	CodeTelegramPasswordInvalid      Code = "telegram_password_invalid"
	CodeTelegramAttemptsExhausted    Code = "telegram_attempts_exhausted"
	CodeTelegramEmailSetupRequired   Code = "telegram_email_setup_required"
	CodeTelegramAccountMismatch      Code = "telegram_account_mismatch"
	CodeTelegramLoginFailed          Code = "telegram_login_failed"
	CodeBucketNotFound               Code = "bucket_not_found"
	CodeBucketExists                 Code = "bucket_exists"
	CodeBucketNameInvalid            Code = "bucket_name_invalid"
	CodeBucketNotEmpty               Code = "bucket_not_empty"
	CodeBucketLimitReached           Code = "bucket_limit_reached"
	CodeAccessKeyNotFound            Code = "access_key_not_found"
	CodeScopeInvalid                 Code = "scope_invalid"
	CodeObjectNotFound               Code = "object_not_found"
	CodeObjectKeyInvalid             Code = "object_key_invalid"
	CodeObjectTooLarge               Code = "object_too_large"
	CodeMetadataTooLarge             Code = "metadata_too_large"
	CodeChecksumMismatch             Code = "checksum_mismatch"
	CodePreconditionFailed           Code = "precondition_failed"
	CodeRangeNotSatisfiable          Code = "range_not_satisfiable"
	CodeIdempotencyConflict          Code = "idempotency_conflict"
	CodeCommitOutcomeUnknown         Code = "commit_outcome_unknown"
	CodePresignKeyInvalid            Code = "presign_key_invalid"
	CodeS3PublicURLUnset             Code = "s3_public_url_unset"
	CodeReplicaNotLinked             Code = "replica_not_linked"
	CodeReplicaAlreadyLinked         Code = "replica_already_linked"
	CodeReplicaSameAccount           Code = "replica_same_account"
)

type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code              Code           `json:"code"`
	Message           string         `json:"message"`
	Retryable         bool           `json:"retryable"`
	Hint              string         `json:"hint"`
	RetryAfterSeconds int            `json:"retry_after_seconds,omitempty"`
	Details           map[string]any `json:"details,omitempty"`
	RequestID         string         `json:"request_id"`
}

type APIError struct {
	Status int
	Body   ErrorBody
}

func (e *APIError) Error() string
func IsCode(err error, code Code) bool
func RetryAfter(err error) (time.Duration, bool)
```

### 16.3 Auth and device DTOs

```go
type ServerInfo struct {
	ServerID       string    `json:"server_id"`
	APIVersion     string    `json:"api_version"`
	ServerVersion  string    `json:"server_version"`
	State          string    `json:"state"`
	TelegramLinked bool      `json:"telegram_linked"`
	PairingMethods []string  `json:"pairing_methods"`
	TLS            TLSInfo   `json:"tls"`
	Time           time.Time `json:"time"`
}

type TLSInfo struct {
	Mode       string `json:"mode"`
	SPKISHA256 string `json:"spki_sha256,omitempty"`
}

type ClaimRequest struct {
	SetupToken    string `json:"setup_token"`
	DeviceName    string `json:"device_name"`
	Platform      string `json:"platform"`
	ClientVersion string `json:"client_version"`
}

type PairingRequest struct {
	Delivery   string `json:"delivery"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
}

type PairingRequested struct {
	Delivery  string    `json:"delivery"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PairingCompleteRequest struct {
	Code          string `json:"code"`
	DeviceName    string `json:"device_name"`
	Platform      string `json:"platform"`
	ClientVersion string `json:"client_version"`
}

type Device struct {
	DeviceID      string     `json:"device_id"`
	Name          string     `json:"name"`
	Platform      string     `json:"platform"`
	CreatedAt     time.Time  `json:"created_at"`
	CreatedVia    string     `json:"created_via"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
	LastSeenIP    string     `json:"last_seen_ip"`
	TokenIssuedAt time.Time  `json:"token_issued_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
	Current       bool       `json:"current"`
}

type DeviceCredentials struct {
	Device      Device `json:"device"`
	DeviceToken string `json:"device_token"`
	ServerID    string `json:"server_id"`
}

type DeviceList struct {
	Devices []Device `json:"devices"`
}

type UpdateDeviceRequest struct {
	Name string `json:"name"`
}

type UnlinkRequest struct {
	Confirm string `json:"confirm"`
}
```

### 16.4 Telegram DTOs

```go
type TelegramState string

const (
	TelegramNoCredentials    TelegramState = "no_credentials"
	TelegramReady            TelegramState = "ready"
	TelegramSendingCode      TelegramState = "sending_code"
	TelegramAwaitingCode     TelegramState = "awaiting_code"
	TelegramAwaitingPassword TelegramState = "awaiting_password"
	TelegramLinked           TelegramState = "linked"
)

type TelegramNext string

const (
	NextApp      TelegramNext = "app"
	NextPhone    TelegramNext = "phone"
	NextCode     TelegramNext = "code"
	NextPassword TelegramNext = "password"
	NextWait     TelegramNext = "wait"
	NextNone     TelegramNext = "none"
)

type TelegramStatus struct {
	State       TelegramState       `json:"state"`
	Next        TelegramNext        `json:"next"`
	Credentials TelegramCredentials `json:"credentials"`
	Login       *TelegramLogin      `json:"login"`
	Account     *TelegramAccount    `json:"account"`
	LastError   *ErrorBody          `json:"last_error"`
}

type TelegramCredentials struct {
	APIID      int  `json:"api_id"`
	APIHashSet bool `json:"api_hash_set"`
}

type TelegramLogin struct {
	LoginID              string       `json:"login_id"`
	PhoneMasked          string       `json:"phone_masked"`
	CodeDelivery         CodeDelivery `json:"code_delivery"`
	PasswordHint         string       `json:"password_hint"`
	CodeAttemptsLeft     int          `json:"code_attempts_left"`
	PasswordAttemptsLeft int          `json:"password_attempts_left"`
	StartedAt            time.Time    `json:"started_at"`
	ExpiresAt            time.Time    `json:"expires_at"`
}

type CodeDelivery struct {
	Type           string `json:"type"`
	Length         int    `json:"length"`
	NextType       string `json:"next_type"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type TelegramAccount struct {
	UserID      int64     `json:"user_id"`
	Username    string    `json:"username"`
	FirstName   string    `json:"first_name"`
	PhoneMasked string    `json:"phone_masked"`
	LinkedAt    time.Time `json:"linked_at"`
	Connected   bool      `json:"connected"`
}

type TelegramCredentialsRequest struct {
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}

type TelegramLoginStartRequest struct {
	Phone string `json:"phone"`
}

type TelegramCodeRequest struct {
	LoginID string `json:"login_id"`
	Code    string `json:"code"`
}

type TelegramPasswordRequest struct {
	LoginID  string `json:"login_id"`
	Password string `json:"password"`
}
```

### 16.5 Status, usage, bucket and key DTOs

```go
type Status struct {
	ServerID      string         `json:"server_id"`
	ServerVersion string         `json:"server_version"`
	Commit        string         `json:"commit"`
	StartedAt     time.Time      `json:"started_at"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	State         string         `json:"state"`
	Telegram      TelegramHealth `json:"telegram"`
	Components    []Component    `json:"components"`
	Endpoints     Endpoints      `json:"endpoints"`
	Limits        Limits         `json:"limits"`
	Replica       ReplicaSummary `json:"replica"`
}

type TelegramHealth struct {
	Linked            bool             `json:"linked"`
	Connected         bool             `json:"connected"`
	Account           *TelegramAccount `json:"account"`
	FloodWaitUntil    *time.Time       `json:"flood_wait_until"`
	SendRatePerMinute int              `json:"send_rate_per_minute"`
}

type Component struct {
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Detail string    `json:"detail"`
	Since  time.Time `json:"since"`
}

type Endpoints struct {
	S3PublicURL    string `json:"s3_public_url"`
	S3Region       string `json:"s3_region"`
	MediaPublicURL string `json:"media_public_url"`
}

type Limits struct {
	MaxObjectBytes       int64 `json:"max_object_bytes"`
	MaxKeyBytes          int   `json:"max_key_bytes"`
	MaxUserMetadataBytes int   `json:"max_user_metadata_bytes"`
	MaxPresignSeconds    int   `json:"max_presign_seconds"`
	MaxBuckets           int   `json:"max_buckets"`
	MaxDeleteBatch       int   `json:"max_delete_batch"`
}

type ReplicaSummary struct {
	State      string `json:"state"`
	PendingOps int64  `json:"pending_ops"`
}

type Usage struct {
	Buckets          int        `json:"buckets"`
	Objects          int64      `json:"objects"`
	StoredBytes      int64      `json:"stored_bytes"`
	PlaintextBytes   int64      `json:"plaintext_bytes"`
	EncryptedObjects int64      `json:"encrypted_objects"`
	BlockCache       CacheUsage `json:"block_cache"`
	Spool            SpoolUsage `json:"spool"`
	ComputedAt       time.Time  `json:"computed_at"`
}

type CacheUsage struct {
	UsedBytes  int64   `json:"used_bytes"`
	LimitBytes int64   `json:"limit_bytes"`
	HitRatio1h float64 `json:"hit_ratio_1h"`
}

type SpoolUsage struct {
	UsedBytes    int64 `json:"used_bytes"`
	LimitBytes   int64 `json:"limit_bytes"`
	JobsInFlight int   `json:"jobs_in_flight"`
}

type Bucket struct {
	Name               string    `json:"name"`
	CreatedAt          time.Time `json:"created_at"`
	Policy             string    `json:"policy"`
	EncryptionDefault  string    `json:"encryption_default"`
	CacheMaxAgeSeconds int       `json:"cache_max_age_seconds"`
	ObjectCount        int64     `json:"object_count"`
	StoredBytes        int64     `json:"stored_bytes"`
	TelegramChannelID  int64     `json:"telegram_channel_id"`
}

type BucketList struct {
	Buckets []Bucket `json:"buckets"`
}

type CreateBucketRequest struct {
	Name               string `json:"name"`
	Policy             string `json:"policy,omitempty"`
	EncryptionDefault  string `json:"encryption_default,omitempty"`
	CacheMaxAgeSeconds *int   `json:"cache_max_age_seconds,omitempty"`
}

type UpdateBucketRequest struct {
	Policy             *string `json:"policy,omitempty"`
	EncryptionDefault  *string `json:"encryption_default,omitempty"`
	CacheMaxAgeSeconds *int    `json:"cache_max_age_seconds,omitempty"`
}

type Scope struct {
	Buckets []string `json:"buckets"`
	Actions []string `json:"actions"`
}

type AccessKey struct {
	AccessKeyID string     `json:"access_key_id"`
	Name        string     `json:"name"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	Status      string     `json:"status"`
	RevokedAt   *time.Time `json:"revoked_at"`
	Scope       Scope      `json:"scope"`
}

type AccessKeyList struct {
	AccessKeys []AccessKey `json:"access_keys"`
}

type CreateAccessKeyRequest struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
}

type UpdateAccessKeyRequest struct {
	Name  *string `json:"name,omitempty"`
	Scope *Scope  `json:"scope,omitempty"`
}

type AccessKeyCreated struct {
	AccessKey       AccessKey `json:"access_key"`
	SecretAccessKey string    `json:"secret_access_key"`
	S3Endpoint      string    `json:"s3_endpoint"`
	S3Region        string    `json:"s3_region"`
}
```

### 16.6 Object, audit and replica DTOs

```go
type ObjectSummary struct {
	Key          string    `json:"key"`
	SizeBytes    int64     `json:"size_bytes"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
	ContentType  string    `json:"content_type"`
	Encrypted    bool      `json:"encrypted"`
}

type ObjectList struct {
	Bucket         string          `json:"bucket"`
	Prefix         string          `json:"prefix"`
	Delimiter      string          `json:"delimiter"`
	Objects        []ObjectSummary `json:"objects"`
	CommonPrefixes []string        `json:"common_prefixes"`
	NextCursor     string          `json:"next_cursor,omitempty"`
}

type ListObjectsOptions struct {
	Prefix    string
	Delimiter string
	Cursor    string
	Limit     int
}

type ObjectMeta struct {
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	VersionID    string            `json:"version_id"`
	SizeBytes    int64             `json:"size_bytes"`
	StoredBytes  int64             `json:"stored_bytes"`
	ETag         string            `json:"etag"`
	SHA256       string            `json:"sha256"`
	ContentType  string            `json:"content_type"`
	LastModified time.Time         `json:"last_modified"`
	Encrypted    bool              `json:"encrypted"`
	UserMetadata map[string]string `json:"user_metadata"`
	Replicated   bool              `json:"replicated"`
}

type PutObjectOptions struct {
	ContentType    string
	Encryption     string
	UserMetadata   map[string]string
	ContentMD5     string
	ContentSHA256  string
	IfNoneMatchAny bool
	IfMatch        string
	IdempotencyKey string
}

type GetObjectOptions struct {
	RangeStart  int64
	RangeEnd    int64
	IfMatch     string
	IfNoneMatch string
}

type ObjectStream struct {
	Body          io.ReadCloser
	Status        int
	ContentLength int64
	ContentRange  string
	Meta          ObjectMeta
}

type DeleteObjectsRequest struct {
	Keys []string `json:"keys"`
}

type DeleteObjectsResult struct {
	Deleted []string            `json:"deleted"`
	Errors  []DeleteObjectError `json:"errors"`
}

type DeleteObjectError struct {
	Key       string `json:"key"`
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type PresignRequest struct {
	Key              string `json:"key"`
	AccessKeyID      string `json:"access_key_id"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

type PresignedURL struct {
	URL       string    `json:"url"`
	Method    string    `json:"method"`
	ExpiresAt time.Time `json:"expires_at"`
}

type AuditActor struct {
	Type       string `json:"type"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

type AuditEvent struct {
	ID         int64      `json:"id"`
	At         time.Time  `json:"at"`
	Actor      AuditActor `json:"actor"`
	Action     string     `json:"action"`
	Target     string     `json:"target"`
	Outcome    string     `json:"outcome"`
	ErrorCode  string     `json:"error_code"`
	RemoteAddr string     `json:"remote_addr"`
	RequestID  string     `json:"request_id"`
}

type AuditPage struct {
	Events     []AuditEvent `json:"events"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type AuditQuery struct {
	Cursor string
	Limit  int
	Action string
	Since  time.Time
	Until  time.Time
}
```

`replica.go` (phase 12):

```go
type ReplicaStatus struct {
	State             string           `json:"state"`
	Next              TelegramNext     `json:"next"`
	CredentialsSource string           `json:"credentials_source"`
	Login             *TelegramLogin   `json:"login"`
	Account           *TelegramAccount `json:"account"`
	Lag               ReplicaLag       `json:"lag"`
	Backfill          ReplicaBackfill  `json:"backfill"`
	LastError         *ErrorBody       `json:"last_error"`
}

type ReplicaLag struct {
	PendingOps      int64      `json:"pending_ops"`
	OldestPendingAt *time.Time `json:"oldest_pending_at"`
}

type ReplicaBackfill struct {
	ObjectsTotal int64      `json:"objects_total"`
	ObjectsDone  int64      `json:"objects_done"`
	BytesDone    int64      `json:"bytes_done"`
	StartedAt    *time.Time `json:"started_at"`
}

type ReplicaLoginStartRequest struct {
	Phone   string `json:"phone"`
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}
```

### 16.7 Go client (`api/adminv1`, phase 09 L5)

```go
type TrustMode string

const (
	TrustPinned TrustMode = "pinned"
	TrustSystem TrustMode = "system"
)

type Trust struct {
	Mode       TrustMode `json:"mode"`
	SPKISHA256 string    `json:"spki_sha256,omitempty"`
}

type ClientConfig struct {
	BaseURL       string
	Trust         Trust
	Token         string
	UserAgent     string
	JSONTimeout   time.Duration
	HTTPTransport http.RoundTripper
}

type CertificateInfo struct {
	SPKISHA256              string
	DisplayFingerprint      string
	Subject                 string
	DNSNames                []string
	IPAddresses             []string
	NotAfter                time.Time
	VerifiesWithSystemRoots bool
}

type PinMismatchError struct {
	Expected string
	Got      string
}

func (e *PinMismatchError) Error() string

func ProbeCertificate(ctx context.Context, baseURL string) (CertificateInfo, error)
func NewClient(cfg ClientConfig) (*Client, error)

func (c *Client) SetToken(token string)
func (c *Client) ServerInfo(ctx context.Context) (ServerInfo, error)
func (c *Client) Claim(ctx context.Context, req ClaimRequest) (DeviceCredentials, error)
func (c *Client) RequestPairing(ctx context.Context, req PairingRequest) (PairingRequested, error)
func (c *Client) CompletePairing(ctx context.Context, req PairingCompleteRequest) (DeviceCredentials, error)
func (c *Client) ListDevices(ctx context.Context) (DeviceList, error)
func (c *Client) RenameDevice(ctx context.Context, deviceID string, req UpdateDeviceRequest) (Device, error)
func (c *Client) RevokeDevice(ctx context.Context, deviceID string, confirmLast bool) error
func (c *Client) RotateDeviceToken(ctx context.Context) (DeviceCredentials, error)
func (c *Client) TelegramStatus(ctx context.Context) (TelegramStatus, error)
func (c *Client) SaveTelegramCredentials(ctx context.Context, req TelegramCredentialsRequest) (TelegramStatus, error)
func (c *Client) DeleteTelegramCredentials(ctx context.Context) (TelegramStatus, error)
func (c *Client) StartTelegramLogin(ctx context.Context, req TelegramLoginStartRequest) (TelegramStatus, error)
func (c *Client) SubmitTelegramCode(ctx context.Context, req TelegramCodeRequest) (TelegramStatus, error)
func (c *Client) SubmitTelegramPassword(ctx context.Context, req TelegramPasswordRequest) (TelegramStatus, error)
func (c *Client) CancelTelegramLogin(ctx context.Context) (TelegramStatus, error)
func (c *Client) UnlinkTelegram(ctx context.Context) (TelegramStatus, error)
func (c *Client) Status(ctx context.Context) (Status, error)
func (c *Client) Usage(ctx context.Context) (Usage, error)
func (c *Client) ListBuckets(ctx context.Context) (BucketList, error)
func (c *Client) CreateBucket(ctx context.Context, req CreateBucketRequest) (Bucket, error)
func (c *Client) GetBucket(ctx context.Context, name string) (Bucket, error)
func (c *Client) UpdateBucket(ctx context.Context, name string, req UpdateBucketRequest) (Bucket, error)
func (c *Client) DeleteBucket(ctx context.Context, name string) error
func (c *Client) ListAccessKeys(ctx context.Context, includeRevoked bool) (AccessKeyList, error)
func (c *Client) CreateAccessKey(ctx context.Context, req CreateAccessKeyRequest) (AccessKeyCreated, error)
func (c *Client) GetAccessKey(ctx context.Context, id string) (AccessKey, error)
func (c *Client) UpdateAccessKey(ctx context.Context, id string, req UpdateAccessKeyRequest) (AccessKey, error)
func (c *Client) RevokeAccessKey(ctx context.Context, id string) (AccessKey, error)
func (c *Client) ListObjects(ctx context.Context, bucket string, opts ListObjectsOptions) (ObjectList, error)
func (c *Client) HeadObject(ctx context.Context, bucket, key string) (ObjectMeta, error)
func (c *Client) ObjectMeta(ctx context.Context, bucket, key string) (ObjectMeta, error)
func (c *Client) GetObject(ctx context.Context, bucket, key string, opts GetObjectOptions) (ObjectStream, error)
func (c *Client) PutObject(ctx context.Context, bucket, key string, body io.Reader, size int64, opts PutObjectOptions) (ObjectMeta, error)
func (c *Client) DeleteObject(ctx context.Context, bucket, key string) error
func (c *Client) DeleteObjects(ctx context.Context, bucket string, req DeleteObjectsRequest) (DeleteObjectsResult, error)
func (c *Client) Presign(ctx context.Context, bucket string, req PresignRequest) (PresignedURL, error)
func (c *Client) Audit(ctx context.Context, q AuditQuery) (AuditPage, error)
func (c *Client) ReplicaStatus(ctx context.Context) (ReplicaStatus, error)
func (c *Client) StartReplicaLogin(ctx context.Context, req ReplicaLoginStartRequest) (ReplicaStatus, error)
func (c *Client) SubmitReplicaCode(ctx context.Context, req TelegramCodeRequest) (ReplicaStatus, error)
func (c *Client) SubmitReplicaPassword(ctx context.Context, req TelegramPasswordRequest) (ReplicaStatus, error)
func (c *Client) CancelReplicaLogin(ctx context.Context) (ReplicaStatus, error)
func (c *Client) UnlinkReplica(ctx context.Context) (ReplicaStatus, error)
```

Client rules:

| Rule | Detail |
| --- | --- |
| Pinned trust | `tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: pin check}`; the pin check hashes the leaf SPKI and compares with `Trust.SPKISHA256` in constant time; mismatch returns `*PinMismatchError` and the request is never sent |
| System trust | Default verification against OS roots; `Trust.SPKISHA256` ignored |
| Plain HTTP | `NewClient` accepts an `http://` `BaseURL` only when the host is `localhost`, `127.0.0.1` or `::1` (an SSH tunnel to a `proxy`-mode listener); `Trust` is then ignored. Any other `http://` URL is an error from `NewClient` |
| `ProbeCertificate` | Completes a TLS handshake without verification, sends no HTTP request, and returns the leaf details for TOFU |
| `UnlinkTelegram`, `UnlinkReplica` | Send `{"confirm":"unlink"}` |
| JSON timeout | `JSONTimeout` (default 75 s, above the 60 s submit wait) applies to JSON routes only; streaming methods rely on the caller's context |
| `PutObject` | Sets `Content-Length` from `size`; never buffers the body; the caller wraps `body` for progress |
| Retries | The client never retries on its own; callers decide using `Retryable` and `RetryAfter` |
| Errors | Non-2xx with an envelope → `*APIError`; transport failures returned wrapped; `HEAD` errors build `APIError` from `X-Butterdb-Error-Code` |
| Token | `SetToken` is safe for concurrent use; the token is never included in error strings |

### 16.8 Server packages

| Package | Lane (phase 09) | Owns |
| --- | --- | --- |
| `internal/adminapi/setup` | L1 | Claim, setup-token issuance at start, Telegram credentials save, relay of login steps to `internal/identity`, unlink |
| `internal/adminapi/session` | L2 | Device tokens, `HashSecret`, pairing codes, Saved Messages delivery port, control socket server, rate limiters, auth middleware, idle-revoke sweeper |
| `internal/storage/adminrepo` | L2 | §15 tables and the `DeviceStore`, `SecretStore`, audit store adapters |
| `internal/adminapi/tlscert` | L3 | §2.3 |
| `internal/adminapi/handlers` | L4 | Router, JSON codec, error mapping table, resource handlers, audit writes, listener construction helpers |
| `api/adminv1` client | L5 | §16.7 |
| `internal/adminapi/handlers/replica.go` | phase 12 L6 | §13 handlers |

Ports declared by `internal/adminapi/handlers` (each implemented by the
service named, wired in `internal/core`):

```go
package handlers

type BucketService interface {
	List(ctx context.Context) ([]bucket.Info, error)
	Create(ctx context.Context, spec bucket.Spec) (bucket.Info, error)
	Get(ctx context.Context, name bucket.BucketName) (bucket.Info, error)
	Update(ctx context.Context, name bucket.BucketName, patch bucket.Patch) (bucket.Info, error)
	Delete(ctx context.Context, name bucket.BucketName) error
}

type KeyService interface {
	List(ctx context.Context, includeRevoked bool) ([]access.KeyInfo, error)
	Create(ctx context.Context, name string, scope access.Scope) (access.KeyInfo, string, error)
	Get(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error)
	Update(ctx context.Context, id access.AccessKeyID, name *string, scope *access.Scope) (access.KeyInfo, error)
	Revoke(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error)
}

type ObjectLister interface {
	List(ctx context.Context, b bucket.BucketName, q object.ListQuery) (object.ListPage, error)
	Meta(ctx context.Context, b bucket.BucketName, k object.Key) (object.ObjectMeta, error)
}

type ObjectWriter interface {
	Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error)
	Delete(ctx context.Context, b bucket.BucketName, k object.Key) error
}

type ObjectServer interface {
	ServeObject(w http.ResponseWriter, r *http.Request, b bucket.BucketName, k object.Key) error
}

type Presigner interface {
	PresignGet(ctx context.Context, id access.AccessKeyID, b bucket.BucketName, k object.Key, ttl time.Duration) (string, time.Time, error)
}

type StatusSource interface {
	Status(ctx context.Context) (adminv1.Status, error)
	Usage(ctx context.Context) (adminv1.Usage, error)
}

type AuditLog interface {
	Record(ctx context.Context, e AuditRecord) error
	Page(ctx context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error)
}
```

The domain type names (`bucket.Info`, `bucket.Spec`, `bucket.Patch`,
`access.KeyInfo`, `object.ListQuery`, `object.ListPage`, `object.PutRequest`)
are the names the admin layer expects from `internal/domain`; the domain
specification is authoritative and the handlers adapt to it.

The identity port declared by `internal/adminapi/setup`:

```go
package setup

type LoginRelay interface {
	Status(ctx context.Context) (identity.Snapshot, error)
	SaveCredentials(ctx context.Context, apiID int, apiHash string) error
	DeleteCredentials(ctx context.Context) error
	Start(ctx context.Context, phone string) (identity.Snapshot, error)
	SubmitCode(ctx context.Context, loginID, code string) (identity.Snapshot, error)
	SubmitPassword(ctx context.Context, loginID, password string) (identity.Snapshot, error)
	Cancel(ctx context.Context) (identity.Snapshot, error)
	Unlink(ctx context.Context) (identity.Snapshot, error)
}
```

`identity.Snapshot` carries the state, login metadata and account summary, and
by construction has no field able to hold session bytes or the code hash.

---
