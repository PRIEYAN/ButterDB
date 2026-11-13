# Phase 04 — Telegram transport and identity
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement telegram transport and identity as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 03 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/storage-model.md`.
- `docs/spec/operations.md`.
- `docs/reference/tdrive-port-map.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit the following shared types in `internal/tgclient` before fan-out. They adapt
`backend/tgclient/client.go` and `backend/tgclient/range.go` to channel-only,
document-only storage. Do not carry TDrive's photo, group, topic, or multipart
fields into the public transport port.

```go
package tgclient

import (
	"errors"
	"time"
)

type InputPeer struct {
	ChannelID int64
	AccessHash int64
}

type DocumentRef struct {
	Peer InputPeer
	MsgID int64
	Size int64
	DocumentID int64
	AccessHash int64
	FileReference []byte
	DCID int
}

type HistoryMessage struct {
	MsgID int64
	Date int64
	FromID int64
	Text string
	HasMedia bool
	MediaSize int64
	DocumentName string
	Placeholder bool
}

type FloodWaitError struct {
	Duration time.Duration
}

var ErrFloodWait = errors.New("tgclient: flood wait")
var ErrMessageNotFound = errors.New("tgclient: message not found")
var ErrChannelUnavailable = errors.New("tgclient: peer unavailable")
var ErrSendOutcomeUnknown = errors.New("tgclient: send outcome unknown")

```

L3 exports `func StableRandomID(jobID, purpose string) (int64, error)`.
L1 owns the connection and `DocumentRef` refresh implementation; L2 owns
flood-wait budgeting and the write coordinator; L3 owns send and range
operations; L4 owns the RPC and authentication adapters; L5 implements the
same consumer-facing methods in `faketg`; L6 owns persisted identity and login.
Add `github.com/gotd/td/telegram@v0.145.1` and every exact gotd subpackage
imported by these adapters, using TDrive's `go.mod` version. Also add
`golang.org/x/time/rate` by exact package path, pinned to its newest release
that is at least two weeks old; `internal/tgclient/flow` (L2) builds the E10
send limiter on it, and it is the only rate limiter in the system. Only
`internal/tgclient` may import gotd. Commit as
`define telegram transport contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/04-telegram-transport-and-identity-l1` | internal/tgclient/conn |
| L2 | `phase/04-telegram-transport-and-identity-l2` | internal/tgclient/flow |
| L3 | `phase/04-telegram-transport-and-identity-l3` | internal/tgclient/{send,rangeio} |
| L4 | `phase/04-telegram-transport-and-identity-l4` | internal/tgclient/{rpc,authrpc} |
| L5 | `phase/04-telegram-transport-and-identity-l5` | internal/tgclient/faketg |
| L6 | `phase/04-telegram-transport-and-identity-l6` | internal/identity |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — internal/tgclient/conn

**Objective.** Port the Telegram connection core: one long-lived gotd run scope, per-DC media pools with dial backoff, `DocumentRef` resolution and deduplicated file-reference refresh.

**Owned paths.** `internal/tgclient/conn/`.

**Read.** Legacy `docs/butterDBCore.md` lines 956–1000 (connection architecture, document references); `docs/spec/operations.md` §13.1 (retained constants); `docs/claude.md` (only `internal/tgclient` imports gotd).

**Rules.**
- One `client.Run` scope per account, started lazily, with every call dispatched onto it. A failed connect is not fatal to the engine.
- `MediaPoolSize = 4` per DC. Concurrent first reads share one dial (singleflight), and a failed dial backs off for 30 s per DC.
- An expired `FileReference` is normal: re-resolve once, deduplicated across concurrent readers, and keep the fresh reference.
- Support a second, independent instance for the replica account (phase 12). No package-level state.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/tgclient/session.go` | 161 | PORT | `internal/tgclient/conn` (L1) | `liveConn`: one long-lived run scope started once under concurrency, `acquire` waits for ready or context, restarts after the scope dies, `Close` stops it |
| `backend/tgclient/gotd.go` | 1,299 | PORT-PARTIAL | `conn` (L1), `send` (L3), `rpc` (L4) | `conn`: `NewGotd`, `scope`, `acquire`, `run`, `runClient`, `Close`, `closeMediaConns`, `normalizeError`. `send`: `SendControlWithRandomID`, `SendFileWithRandomID` (always `InputMediaUploadedDocument{MimeType: "application/octet-stream", ForceFile: true}` with a filename attribute), message-ID extraction that classifies an accepted response without a receipt as `ErrSendOutcomeUnknown` (lines 276, 1065). `rpc`: `GetHistory`, `GetChannelMessage`, `DeleteMessages` (batches of 100), `MissingMessages`, `GetChannelDifference`, `GetChannelPts`, `SelfID`, `historyMessageFromTG`, `placeholderMessage`, `restrictedHere`, `channelsGetMessagesLimit = 100`. Drop: `SendControl` and `SendFile` without random ID, thumbnails, `SearchChannelMessages`, `SelfProfile`, `ResolveUsersFromMessages`, `DownloadChannelPhoto`, `DownloadFileThumbnail`, `GetFileDocument`, megagroups, invites, join requests, `LookupChannelTitle`, `LeaveChannel` |
| `backend/tgclient/gotd_pool.go` | 139 | PORT | `internal/tgclient/conn` (L1) | Per-DC file pools, `MediaPoolSize = 4`, `UploadThreads = 8`, `poolRetryAfter = 30 * time.Second`, `FILE_MIGRATE` detection |
| `backend/tgclient/gotd_range.go` | 388 | PORT | `internal/tgclient/rangeio` (L3), file-reference refresh in `conn` (L1) | `ResolveDocument`, `ResolveDocuments` (order-preserving), `ReadDocumentRange` with alignment preconditions (`roundedTelegramLimit`, `crossesRangeBoundary`, `copyRequestedRange`), CDN redirect with AES-CTR decrypt and `verifyFileHashes` (rejects coverage gaps and mismatches), `IsFileReferenceError` |
| `backend/auth/session_storage.go` | 39 | PORT | `internal/tgclient/conn` (L1) | gotd `session.Storage` adapter. Change: it reads and writes through a byte-store port; the bytes live in a secret file owned by `identity` |
| `backend/auth/auth.go` | 230 | PORT-PARTIAL | `internal/tgclient/authrpc` (L4), `internal/identity` (L6), `internal/tgclient/conn` (L1) | `authrpc`: `SendCode`, `SignIn`, mapping `auth.ErrPasswordAuthNeeded` to a password step, `AccountGetPassword` hint, `Password`, auth status. `identity`: api_id and api_hash save and load through `secretfile` (TDrive `imp_config.json`). `conn`: `ConnectWithOptions` builds `telegram.NewClient(apiID, apiHash, options)` with the session store. Drop the blocking `StartLogin` loop; butterDB relays one step per admin API call |
| `backend/livesync/activity.go` (`Handle`, `ChannelIDsFromUpdates`, `channelIDFromUpdate`) | (in 127) | PORT-PARTIAL | `internal/tgclient/conn` (L1) | gotd `UpdateHandler` extracting channel IDs from `UpdateShort`, `Updates`, `UpdatesCombined`, `UpdateNewChannelMessage` and friends, delivered without blocking |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/tgclient/session.go`: `func (l *liveConn) Close() {`
- `backend/tgclient/gotd.go`: `type Gotd struct {`; `func NewGotd(connect func() (*telegram.Client, error)) *Gotd {`; `func (g *Gotd) Close() {`; `func (g *Gotd) SelfID(ctx context.Context) (int64, error) {`; `func (g *Gotd) SelfProfile(ctx context.Context) (UserProfile, error) {`; `func (g *Gotd) ResolveUsersFromMessages(ctx context.Context, peer InputPeer, refs []UserMessageRef) ([]UserProfile, error) {`; `func (g *Gotd) SendControl(ctx context.Context, peer InputPeer, text string, silent bool) (int64, error) {`; `func (g *Gotd) SendControlWithRandomID(ctx context.Context, peer InputPeer, text string, silent bool, sendRandomID int64) (int64, error) {`
- `backend/tgclient/gotd_range.go`: `func (g *Gotd) ResolveDocument(ctx context.Context, peer InputPeer, msgID int64) (DocumentRef, error) {`; `func (g *Gotd) ResolveDocuments(ctx context.Context, peer InputPeer, msgIDs []int64) ([]DocumentRef, error) {`; `func (g *Gotd) ReadDocumentRange(ctx context.Context, ref DocumentRef, offset int64, dst []byte) (int, error) {`; `func IsFileReferenceError(err error) bool {`
- `backend/auth/session_storage.go`: `func (s *privateSessionStorage) LoadSession(_ context.Context) ([]byte, error) {`; `func (s *privateSessionStorage) StoreSession(_ context.Context, data []byte) error {`
- `backend/auth/auth.go`: `type ImpCredentials struct {`; `func GetConfigPath() string {`; `func SaveImpCredentials(id int, hash string) error {`; `func LoadImpCredentials() (ImpCredentials, error) {`; `func Connect() (*telegram.Client, error) {`; `func ConnectWithOptions(options telegram.Options) (*telegram.Client, error) {`; `func CheckLogin(ctx context.Context) (bool, error) {`; `func StartLogin(ctx context.Context, client *telegram.Client, ch getChannel, phone string) error {`
- `backend/livesync/activity.go`: `type Activity interface {`; `type TelegramActivity struct {`; `func NewTelegramActivity(buffer int) *TelegramActivity {`; `func (a *TelegramActivity) Signals() <-chan int64 {`; `func (a *TelegramActivity) Dropped() int64 {`; `func (a *TelegramActivity) Signal(channelID int64) {`; `func (a *TelegramActivity) Handle(_ context.Context, updates tg.UpdatesClass) error {`; `func ChannelIDsFromUpdates(updates tg.UpdatesClass) []int64 {`

**Tests (binding names).** the carried `session_test.go` (all eight `TestLiveConn*`), `gotd_pool_test.go` and the listed `gotd_documents_test.go` cases, plus `TestTwoAccountsIndependent`, `TestFileReferenceRefreshDeduplicated`.

**Verify.**

```bash
go vet -mod=readonly ./internal/tgclient/conn/...
go test -mod=readonly -race ./internal/tgclient/conn/...
gofmt -l internal/tgclient/conn
```

**Commits.** `port telegram connection scope and media pools`, `add document reference resolution and refresh`.

### 5.2 Lane L2 — internal/tgclient/flow

**Objective.** Port flow control: typed flood waits, retry policies with independent flood and transient budgets, the write coordinator that shares the longest cooldown, the getFile limiter with a foreground reserve, and the E10 send-rate limiter.

**Owned paths.** `internal/tgclient/flow/`.

**Read.** Legacy `docs/butterDBCore.md` lines 2922–3086 (range transport and rate limiting); `docs/spec/operations.md` §2.4 `telegram.send_rate.*`, §13.3–§13.4 (limiter classes); `docs/decisions.md` E10.

```go
package flow

type SendClass int

const (
	SendControl SendClass = iota + 1
	SendDocument
)

type SendLimiter struct{}

func NewSendLimiter(controlPerMinute, controlBurst, documentsPerMinute, documentsBurst int) *SendLimiter
func (l *SendLimiter) Reserve(class SendClass) (delay time.Duration, cancel func())
func (l *SendLimiter) Wait(ctx context.Context, class SendClass) error
```

`Reserve` wraps `golang.org/x/time/rate` reservations, and the prelude adds `golang.org/x/time/rate` by exact path. `ingest/admission` (phase 06) consumes `Reserve` through its `Pacer` port. The limiter is the only E10 token bucket in the system.

**Rules.**
- Convert gotd flood waits to `FloodWaitError` at the boundary. Callers branch on the type, never on text. Recognise `FLOOD_PREMIUM_WAIT_X` as a flood wait.
- The write coordinator serialises message sends, rechecks cooldown after queueing, and records server-directed waits for every class. The longest cooldown applies across classes.
- getFile: 12 total slots, 4 reserved for foreground API reads, 8 for background work acquired with a non-queueing `TryAcquire` so background work never blocks a foreground read.
- The send limiter is on by default, with operations §2.4 rates. Flood waits remain an additional floor on top of it.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/tgclient/floodwait_retry.go` | 249 | PORT | `internal/tgclient/flow` (L2) | `FloodWaitRetryPolicy` with `Validate` and `Do`, `DefaultWriteFloodWaitRetryPolicy` (2 retries, 30 s max, 60 s total), transient budget (4 retries, 2 s backoff doubling to 30 s, 750 ms jitter), independent budgets for flood and transient failures, `saturatingDurationAdd`, `ErrInvalidFloodWaitRetryPolicy`. Change: sleep and jitter via injected clock and random source |
| `backend/tgclient/write_coordinator.go` | 113 | PORT | `internal/tgclient/flow` (L2) | Shares the longest FLOOD_WAIT across write classes, serializes message sends, rechecks cooldown after queueing. Add: the default-on send-rate limiter (E10) sits here |
| `backend/tgclient/getfile_limiter.go` | 125 | PORT | `internal/tgclient/flow` (L2) | `MaxConcurrentGetFile = 12`, `PlaybackGetFileReserve = 4`, `MaxConcurrentBackgroundGetFile = 8`, `DefaultDownloadThreads = 4`, upload-part slots `UploadThreads`. Change: package semaphores become one limiter value; "playback" reserve becomes the foreground (API GET) reserve |
| `backend/tgclient/transient.go` | 97 | PORT | `internal/tgclient/flow` (L2) | `IsTransientTransport`: typed checks first, then the transport string patterns |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/tgclient/floodwait_retry.go`: `type FloodWaitRetryPolicy struct {`; `func DefaultWriteFloodWaitRetryPolicy() FloodWaitRetryPolicy {`; `func DefaultUploadFloodWaitRetryPolicy() FloodWaitRetryPolicy {`; `func DefaultUploadNetworkRetryPolicy() FloodWaitRetryPolicy {`; `func (p FloodWaitRetryPolicy) Validate() error {`; `func (p FloodWaitRetryPolicy) Do(ctx context.Context, action func() error) error {`
- `backend/tgclient/write_coordinator.go`: `func (c *writeCoordinator) Do(ctx context.Context, class writeClass, action func() error) error {`
- `backend/tgclient/getfile_limiter.go`: `func AcquireGetFileSlots(ctx context.Context, n int) (func(), error) {`; `func AcquireBackgroundGetFileSlots(ctx context.Context, n int) (func(), error) {`
- `backend/tgclient/transient.go`: `func IsTransientTransport(err error) bool {`

**Tests (binding names).** the carried `floodwait_retry_test.go`, `write_coordinator_test.go`, `getfile_limiter_test.go` ("playback" read as foreground) and `transient_test.go` cases, plus `TestPremiumFloodWaitIsFloodWait`, `TestSendLimiterBurstThenSteady`, `TestReserveCancelReturnsToken`, `TestBackgroundNeverBlocksForeground`.

**Verify.**

```bash
go vet -mod=readonly ./internal/tgclient/flow/...
go test -mod=readonly -race ./internal/tgclient/flow/...
gofmt -l internal/tgclient/flow
```

**Commits.** `port flood wait retry policies`, `port write coordinator and getfile limiter`, `add default on send rate limiter`.

### 5.3 Lane L3 — internal/tgclient/{send,rangeio}

**Objective.** Port idempotent sending and bounded range reads: `StableRandomID`, document and control sends that classify uncertain outcomes, `ReadDocumentRange` with its preconditions, and CDN redirect reads with hash verification.

**Owned paths.** `internal/tgclient/send/`, `internal/tgclient/rangeio/`.

**Read.** Legacy `docs/butterDBCore.md` lines 1001–1022 (idempotency), 2643–2697 (transport layer and retry), 2946–2963 (range preconditions), 3042–3086 (CDN reads); `docs/spec/storage-model.md` §2.2 (send rules for every message) and §6.3.

**Rules.**
- `StableRandomID(jobID, step) = int64(LE64(sha256(jobID + "\x00" + step)[:8])) | 1`. Retries reuse it.
- A send returning a message ID and an error means the message exists. Classify it as accepted. Use the distinct `ErrSendOutcomeUnknown` when acceptance cannot be determined, and never collapse it into a generic failure.
- Every body is a document with `ForceFile`, `application/octet-stream` and `silent`. Bodies are `io.ReadSeeker`s, rewound before a resend and never resumed mid-stream. Telegram upload parts are 512 KiB, 8 threads.
- Range reads require 4 KiB alignment, at most 1 MiB, and must not cross a 1 MiB boundary. Validate before the wire.
- CDN bytes are AES-CTR with IV counter `offset/16`, verified against `upload.getCdnFileHashes`. Re-upload is requested at most once.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/tgclient/gotd.go` | 1,299 | PORT-PARTIAL | `conn` (L1), `send` (L3), `rpc` (L4) | `conn`: `NewGotd`, `scope`, `acquire`, `run`, `runClient`, `Close`, `closeMediaConns`, `normalizeError`. `send`: `SendControlWithRandomID`, `SendFileWithRandomID` (always `InputMediaUploadedDocument{MimeType: "application/octet-stream", ForceFile: true}` with a filename attribute), message-ID extraction that classifies an accepted response without a receipt as `ErrSendOutcomeUnknown` (lines 276, 1065). `rpc`: `GetHistory`, `GetChannelMessage`, `DeleteMessages` (batches of 100), `MissingMessages`, `GetChannelDifference`, `GetChannelPts`, `SelfID`, `historyMessageFromTG`, `placeholderMessage`, `restrictedHere`, `channelsGetMessagesLimit = 100`. Drop: `SendControl` and `SendFile` without random ID, thumbnails, `SearchChannelMessages`, `SelfProfile`, `ResolveUsersFromMessages`, `DownloadChannelPhoto`, `DownloadFileThumbnail`, `GetFileDocument`, megagroups, invites, join requests, `LookupChannelTitle`, `LeaveChannel` |
| `backend/tgclient/gotd_range.go` | 388 | PORT | `internal/tgclient/rangeio` (L3), file-reference refresh in `conn` (L1) | `ResolveDocument`, `ResolveDocuments` (order-preserving), `ReadDocumentRange` with alignment preconditions (`roundedTelegramLimit`, `crossesRangeBoundary`, `copyRequestedRange`), CDN redirect with AES-CTR decrypt and `verifyFileHashes` (rejects coverage gaps and mismatches), `IsFileReferenceError` |
| `backend/tgclient/gotd_media.go` | 110 | PORT-PARTIAL | `internal/tgclient/rangeio` (L3) | `mediaRefFromTG`, `getMediaRefByMessageID`. Drop inline photo reads and `largestPhotoSize`; butterDB bodies are always documents |
| `backend/tgclient/download.go` | 173 | PORT-PARTIAL | `internal/tgclient/rangeio` (L3) | Sequential `stream` with per-block retry and one file-reference refresh (`refreshRef`). Phase 12 copies bodies to the replica with it. Drop the parallel `WriterAt` download |
| `backend/tgclient/idempotency.go` | 69 | PORT | `internal/tgclient/send` (L3) | `StableRandomID(operationID, step)`: deterministic, namespaced, positive, rejects ambiguous keys over `maxIdempotencyKeyBytes = 256`. Change: callers pass the server-minted job ID (E9); fallback randomness injected |
| `backend/tgclient/upload_part_retry.go` | 49 | PORT | `internal/tgclient/send` (L3) | Retries one `upload.saveFilePart` through throttling, releasing the slot during cooldown |
| `backend/tgclient/upload_media_retry.go` | 44 | PORT | `internal/tgclient/send` (L3) | Keeps the uploaded handle and `random_id` across retries; a send whose receipt cannot be recovered returns `ErrSendOutcomeUnknown` |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/tgclient/gotd.go`: `type Gotd struct {`; `func NewGotd(connect func() (*telegram.Client, error)) *Gotd {`; `func (g *Gotd) Close() {`; `func (g *Gotd) SelfID(ctx context.Context) (int64, error) {`; `func (g *Gotd) SelfProfile(ctx context.Context) (UserProfile, error) {`; `func (g *Gotd) ResolveUsersFromMessages(ctx context.Context, peer InputPeer, refs []UserMessageRef) ([]UserProfile, error) {`; `func (g *Gotd) SendControl(ctx context.Context, peer InputPeer, text string, silent bool) (int64, error) {`; `func (g *Gotd) SendControlWithRandomID(ctx context.Context, peer InputPeer, text string, silent bool, sendRandomID int64) (int64, error) {`
- `backend/tgclient/gotd_range.go`: `func (g *Gotd) ResolveDocument(ctx context.Context, peer InputPeer, msgID int64) (DocumentRef, error) {`; `func (g *Gotd) ResolveDocuments(ctx context.Context, peer InputPeer, msgIDs []int64) ([]DocumentRef, error) {`; `func (g *Gotd) ReadDocumentRange(ctx context.Context, ref DocumentRef, offset int64, dst []byte) (int, error) {`; `func IsFileReferenceError(err error) bool {`
- `backend/tgclient/idempotency.go`: `func StableRandomID(operationID, step string) (int64, error) {`
- `backend/tgclient/upload_part_retry.go`: `func (c *retryingUploadClient) UploadSaveFilePart(ctx context.Context, request *tg.UploadSaveFilePartRequest) (bool, error) {`; `func (c *retryingUploadClient) UploadSaveBigFilePart(ctx context.Context, request *tg.UploadSaveBigFilePartRequest) (bool, error) {`

**Tests (binding names).** the carried `idempotency_test.go`, `upload_part_retry_test.go`, `upload_media_retry_test.go`, `gotd_updates_test.go`, `range_test.go` (including CDN hash cases) and listed `download_test.go` cases, plus `TestStableRandomIDVectors`, `TestAcceptedWithErrorIsAccepted`, `TestRangePreconditions`.

**Verify.**

```bash
go vet -mod=readonly ./internal/tgclient/send/... ./internal/tgclient/rangeio/...
go test -mod=readonly -race ./internal/tgclient/send/... ./internal/tgclient/rangeio/...
gofmt -l internal/tgclient/send internal/tgclient/rangeio
```

**Commits.** `port idempotent document and control sends`, `port bounded range reads with cdn verification`.

### 5.4 Lane L4 — internal/tgclient/{rpc,authrpc}

**Objective.** Expose the remaining RPCs every later phase needs: channel creation and admin edits, history paging, channel differences, message lookup and deletion, dialogs for access-hash refresh, Saved Messages sends, and the login RPCs behind `internal/identity`.

**Owned paths.** `internal/tgclient/rpc/`, `internal/tgclient/authrpc/`.

**Read.** Legacy `docs/butterDBCore.md` lines 865–955 (login state machine, drive creation, RPC catalogue); `docs/spec/storage-model.md` §4.1, §5.2, §9; `docs/spec/admin-api.md` §5.4 (Saved Messages delivery) and §8.

**Rules.**
- Methods include `CreateChannel`, `EditAdmin`, `GetHistory`, `GetChannelDifference`, `GetMessages` (batched), `DeleteMessages` (batches of 100), `GetDialogs` (primary and archived), `SendToSavedMessages` (`inputPeerSelf`, no link previews), `UsersGetSelf`, and `authrpc` `SendCode`, `SignIn`, `CheckPassword`, `GetPasswordHint`, `LogOut`.
- Never call a channel with a zero access hash. Refresh stale hashes from dialogs.
- Every method goes through `flow` (coordinator and limiter) and returns typed errors. `PHONE_CODE_INVALID` and `PASSWORD_HASH_INVALID` map to sentinels that the identity layer retries in place.
- The exported method set is what `faketg` (L5) mirrors. Keep it explicit and small.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/tgclient/gotd.go` | 1,299 | PORT-PARTIAL | `conn` (L1), `send` (L3), `rpc` (L4) | `conn`: `NewGotd`, `scope`, `acquire`, `run`, `runClient`, `Close`, `closeMediaConns`, `normalizeError`. `send`: `SendControlWithRandomID`, `SendFileWithRandomID` (always `InputMediaUploadedDocument{MimeType: "application/octet-stream", ForceFile: true}` with a filename attribute), message-ID extraction that classifies an accepted response without a receipt as `ErrSendOutcomeUnknown` (lines 276, 1065). `rpc`: `GetHistory`, `GetChannelMessage`, `DeleteMessages` (batches of 100), `MissingMessages`, `GetChannelDifference`, `GetChannelPts`, `SelfID`, `historyMessageFromTG`, `placeholderMessage`, `restrictedHere`, `channelsGetMessagesLimit = 100`. Drop: `SendControl` and `SendFile` without random ID, thumbnails, `SearchChannelMessages`, `SelfProfile`, `ResolveUsersFromMessages`, `DownloadChannelPhoto`, `DownloadFileThumbnail`, `GetFileDocument`, megagroups, invites, join requests, `LookupChannelTitle`, `LeaveChannel` |
| `backend/tgclient/personal_drive.go` | 229 | PORT-PARTIAL | `internal/tgclient/rpc` (L4) | `CreateBroadcastChannel` (createChannel, broadcast), `ListOwnedBroadcastChannels` (dialog walk in batches of 100, including archived folder 1, deduplicated, no partial result on failure), `GetBroadcastChannel`. Registry rediscovery uses them. Drop joined-channel listing |
| `backend/auth/peer_resolution.go` | 56 | PORT | `internal/tgclient/rpc` (L4) | `ResolveDriveChannel`: recover a channel's access hash by walking dialogs in folders 0 and 1, rejecting inaccessible entities, preserving RPC failure and cancellation |
| `backend/auth/auth.go` | 230 | PORT-PARTIAL | `internal/tgclient/authrpc` (L4), `internal/identity` (L6), `internal/tgclient/conn` (L1) | `authrpc`: `SendCode`, `SignIn`, mapping `auth.ErrPasswordAuthNeeded` to a password step, `AccountGetPassword` hint, `Password`, auth status. `identity`: api_id and api_hash save and load through `secretfile` (TDrive `imp_config.json`). `conn`: `ConnectWithOptions` builds `telegram.NewClient(apiID, apiHash, options)` with the session store. Drop the blocking `StartLogin` loop; butterDB relays one step per admin API call |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/tgclient/gotd.go`: `type Gotd struct {`; `func NewGotd(connect func() (*telegram.Client, error)) *Gotd {`; `func (g *Gotd) Close() {`; `func (g *Gotd) SelfID(ctx context.Context) (int64, error) {`; `func (g *Gotd) SelfProfile(ctx context.Context) (UserProfile, error) {`; `func (g *Gotd) ResolveUsersFromMessages(ctx context.Context, peer InputPeer, refs []UserMessageRef) ([]UserProfile, error) {`; `func (g *Gotd) SendControl(ctx context.Context, peer InputPeer, text string, silent bool) (int64, error) {`; `func (g *Gotd) SendControlWithRandomID(ctx context.Context, peer InputPeer, text string, silent bool, sendRandomID int64) (int64, error) {`
- `backend/tgclient/personal_drive.go`: `func (q folderDialogsQuery) Query(ctx context.Context, request dialogs.Request) (tg.MessagesDialogsClass, error) {`; `func (g *Gotd) ListOwnedBroadcastChannels(ctx context.Context) ([]OwnedBroadcastChannel, error) {`; `func (g *Gotd) ListJoinedBroadcastChannels(ctx context.Context) ([]JoinedBroadcastChannel, error) {`; `func (g *Gotd) GetBroadcastChannel(ctx context.Context, peer InputPeer) (JoinedBroadcastChannel, error) {`; `func (g *Gotd) CreateBroadcastChannel(ctx context.Context, title, about string) (OwnedBroadcastChannel, error) {`
- `backend/auth/peer_resolution.go`: `type FolderDialogsQuery struct {`; `func (q FolderDialogsQuery) Query(ctx context.Context, request dialogs.Request) (tg.MessagesDialogsClass, error) {`; `func ResolveDriveChannel(ctx context.Context, api *tg.Client, channelID int64) (*tg.InputChannel, *tg.InputPeerChannel, error) {`
- `backend/auth/auth.go`: `type ImpCredentials struct {`; `func GetConfigPath() string {`; `func SaveImpCredentials(id int, hash string) error {`; `func LoadImpCredentials() (ImpCredentials, error) {`; `func Connect() (*telegram.Client, error) {`; `func ConnectWithOptions(options telegram.Options) (*telegram.Client, error) {`; `func CheckLogin(ctx context.Context) (bool, error) {`; `func StartLogin(ctx context.Context, client *telegram.Client, ch getChannel, phone string) error {`

**Tests (binding names).** the carried `history_placeholder_test.go`, `personal_drive_test.go` (owned broadcast channel collection), `fake_test.go`/`fake_difference_test.go` (rewritten against `faketg`) and `peer_resolution_test.go` cases, plus `TestDeleteMessagesBatchesOf100`, `TestZeroAccessHashRefused`, `TestSavedMessagesNoPreview`, `TestAuthErrorsTyped`.

**Verify.**

```bash
go vet -mod=readonly ./internal/tgclient/rpc/... ./internal/tgclient/authrpc/...
go test -mod=readonly -race ./internal/tgclient/rpc/... ./internal/tgclient/authrpc/...
gofmt -l internal/tgclient/rpc internal/tgclient/authrpc
```

**Commits.** `add channel history and message rpcs`, `add login rpcs`.

### 5.5 Lane L5 — internal/tgclient/faketg

**Objective.** Build `faketg`, the hand-written fake of the whole `tgclient` exported method set, with per-channel message history, differences, documents, multiple accounts and scripted faults, so every later phase tests against realistic Telegram behaviour.

**Owned paths.** `internal/tgclient/faketg/`.

**Read.** `docs/plan.md` (testing strategy and evidence classes); `docs/reference/tdrive-port-map.md` (the TDrive fake to port); the exported method sets of L1–L4 as the prelude contracts state them.

**Rules.**
- Mirror the concrete exported methods of `tgclient` adapters, not consumer ports (later consumers' ports are subsets). Add a compile-time assertion for each mirrored type.
- Scriptable faults: `FloodWaitError` (including premium), accepted-with-error, `ErrSendOutcomeUnknown`, transport drop, expired file reference, CDN redirect, latency, and deleted messages.
- Each account and channel has its own history, `pts` and message-ID sequence. Random-ID dedup returns the original message, as Telegram does.
- Deterministic: no wall-clock sleeps (use the injected clock), and safe for concurrent use.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/tgclient/fake.go` | 1,154 | PORT-PARTIAL | `internal/tgclient/faketg` (L5) | See section 7.1 for what to keep and what must change |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/tgclient/fake.go`: `type Fake struct {`; `type SentControl struct {`; `type SentFile struct {`; `type HiddenJoinRequest struct {`; `func NewFake(selfID int64) *Fake {`; `func (f *Fake) InjectTransientFailures(n int) {`; `func (f *Fake) SetSelfID(id int64) {`; `func (f *Fake) Close() {}`

**Tests (binding names).** `TestFakeMirrorsTgclient` (method-set comparison by reflection), `TestFakeRandomIDDedup`, `TestFakePerChannelIsolation`, `TestFakeDifferencePages`, `TestFakeScriptedFaults` (one case per fault), `TestFakeTwoAccounts`.

**Verify.**

```bash
go vet -mod=readonly ./internal/tgclient/faketg/...
go test -mod=readonly -race ./internal/tgclient/faketg/...
gofmt -l internal/tgclient/faketg
```

**Commits.** `add scriptable fake telegram client`.

### 5.6 Lane L6 — internal/identity

**Objective.** Own the Telegram account binding: api_id and api_hash credentials, the hand-rolled login state machine that keeps the phone code hash across attempts, the sealed session storage, and the account repository.

**Owned paths.** `internal/identity/`, `internal/storage/accountrepo/` (the new repository over `telegram_accounts`, `docs/spec/schema.md` §5.3 and §6).

**Read.** Legacy `docs/butterDBCore.md` lines 851–893 (credentials, session and login state machine), 704–730 (secret files); `docs/spec/admin-api.md` §8.1 (states the relay exposes) and §16.8 (`LoginRelay`, which `identity` satisfies); `docs/spec/security.md` §5.4 and §16 (`TestSessionFileSealed`).

**Rules.**
- Never use gotd's one-shot `auth.Flow`. Keep the code hash so an invalid code is retryable in place. 2FA fetches the hint best-effort.
- Credentials and session go through `secretfile`. The session is sealed with `keyring.SessionKey()`, so the file holds no raw gotd auth key, and opening with another server key fails.
- `identity.Snapshot` carries state, login metadata and an account summary, and by construction has no field that can hold session bytes or the code hash.
- Two instances can coexist (primary and replica) with separate files.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/auth/auth.go` | 230 | PORT-PARTIAL | `internal/tgclient/authrpc` (L4), `internal/identity` (L6), `internal/tgclient/conn` (L1) | `authrpc`: `SendCode`, `SignIn`, mapping `auth.ErrPasswordAuthNeeded` to a password step, `AccountGetPassword` hint, `Password`, auth status. `identity`: api_id and api_hash save and load through `secretfile` (TDrive `imp_config.json`). `conn`: `ConnectWithOptions` builds `telegram.NewClient(apiID, apiHash, options)` with the session store. Drop the blocking `StartLogin` loop; butterDB relays one step per admin API call |
| `backend/services/auth/service.go` | 261 | PORT-PARTIAL | `internal/identity` (L6) | Keep: the login stage machine (`loginStage`, `accepts`, `resetAttempt`, `finishAttempt`), code rejection allowing a new code, password with hint, "needs setup" when no credentials exist. Change: the channel-based UI handshake (`WaitCode`, `WaitPassword`, `sendLatest`, `drain`, `EventSink`) becomes explicit steps (submit phone, submit code, submit password) that return the next step; account binding records the user ID from `users.getUsers(self)` |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/auth/auth.go`: `type ImpCredentials struct {`; `func GetConfigPath() string {`; `func SaveImpCredentials(id int, hash string) error {`; `func LoadImpCredentials() (ImpCredentials, error) {`; `func Connect() (*telegram.Client, error) {`; `func ConnectWithOptions(options telegram.Options) (*telegram.Client, error) {`; `func CheckLogin(ctx context.Context) (bool, error) {`; `func StartLogin(ctx context.Context, client *telegram.Client, ch getChannel, phone string) error {`
- `backend/services/auth/service.go`: `type EventSink interface {`; `type Service struct {`; `func NewService(events EventSink) *Service {`; `func (s *Service) StartLogin(ctx context.Context, phoneNumber string) error {`; `func (s *Service) IsLoggedIn(ctx context.Context) bool {`; `func (s *Service) SystemStatus() string {`; `func (s *Service) SaveSetup(apiID int, apiHash string) string {`; `func (s *Service) SubmitCode(code string) {`

**Tests (binding names).** the carried `peer_resolution_test.go`, `session_storage_test.go` and `services/auth/service_test.go` cases, plus `TestSessionFileSealed` (security §16), `TestCodeInvalidRetryInPlace`, `TestPasswordHintBestEffort`, `TestSnapshotHasNoSecrets`, `TestAccountRepoRoundTrip`.

**Verify.**

```bash
go vet -mod=readonly ./internal/identity/... ./internal/storage/accountrepo/...
go test -mod=readonly -race ./internal/identity/... ./internal/storage/accountrepo/...
gofmt -l internal/identity internal/storage/accountrepo
```

**Commits.** `add telegram login state machine`, `add sealed session storage and account repository`.

## 6. Integration lane

core wiring, cmd/butterdbd setup + tgprobe subcommands, test/acceptance/phase04 (fake-based; Telegram test-DC smoke behind a build tag/env gate)
Own `internal/core`, `cmd/`, and `test/acceptance/phase04` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase04/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestUncertainSendRetriesWithStableRandomID`: uncertain send retries with stable random ID.
  - `TestFloodWaitAndTransportRetryBudgetsAreSeparate`: flood wait and transport retry budgets are separate.
  - `TestRangeReadsVerifyCDNHashes`: range reads verify CDN hashes.
  - `TestLoginStateSurvivesRestart`: login state survives restart.
### TDrive regression tests to adapt

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

## 8. Owner gates

The Telegram test-DC smoke test is optional and requires owner-supplied test-DC `api_id` and `api_hash`. Record a gated skip when they are absent; fake-based tests remain mandatory.

## 9. Exit criteria and handoff

All L1–L6 commits have passed monitoring and review; the integration build and phase 04 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 05.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding behavior for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/storage-model.md`

## 9. Sync and authority

Ported from legacy section 11 with butterDB's channel layout.

### 9.1 Per-channel gate and fields

Initial, incremental, authoritative and reconciliation work for one channel
serialize behind one gate per channel. A queued caller can cancel its own
admission without disturbing the active pass.

| Field (`channels`) | Means | Does not mean |
| --- | --- | --- |
| `last_synced_msg` | Contiguous scanned boundary, including messages that carry no op | The highest message projected |
| `pts` | `updates.getChannelDifference` cursor | Anything about coverage |
| `initial_sync_done` | A full-history observation committed | Up to date |
| `needs_projection_rebuild` | A full-channel rebuild is pending (only after a failed rebuild or a schema-driven rebuild; per-key repair never sets it) | A corrupt database |

### 9.2 Order

1. The registry channel syncs first and must reach authority.
2. Bucket channels named by live `buckets` rows sync next, each independently. A `t=bcreate` seen during registry sync inserts the bucket's `channels` row with `access_hash = 0`; the syncer fills the hash from `messages.getDialogs` before its first read.
3. The S3 listener answers `503 ServiceUnavailable` until every bucket channel's first difference reaches `Final` (plan.md section 2.7).

Incremental sync, full history scan (empty page proves exhaustion; spool in
short transactions; apply atomically), the retry budgets (flood-wait retries
plus four transport retries, independent) and live signals (debounce 750 ms,
backstop 5 min, per-pass timeout 2 min, sequential flushes) follow legacy
section 11 unchanged. The scan spool is `initial_scan_spool` in `projection.db` (schema.md §4.13).

### 9.3 Authority gates sends

| Write | Requires authority on |
| --- | --- |
| `t=bcreate`, `t=bcfg`, `t=bdel`, `t=encfg` | Registry channel |
| `t=body`, `t=put`, `t=del` for bucket B | Registry channel **and** B's channel |

A bucket without authority (scan in progress, rebuild pending) rejects writes
for **that bucket only** with `503 SlowDown` and `Retry-After: 5`; other
buckets keep working. Reads are served from the projection regardless.
`EnsureAuthoritative` runs an incremental refresh only when authority exists
and no rebuild is pending; otherwise it scans from message 0.

Registry authority matters for encryption the way the legacy vault did:
"no `t=encfg` seen yet" means "no key" only after the registry's complete
history has been observed. A put that would mint a second key because the
first was not yet synced is prevented by this gate.

### 9.4 Per-key ordering repair (E6)

Trigger: a put or tombstone for key K at message M is being recorded, and
`replay_log` already holds an op for K in the same channel with a message ID
greater than M. Action, inside the same transaction that records M:

1. Remove K's `objects` and `object_tombstones` rows and back their sizes out of the bucket counters.
2. Replay every `replay_log` row for `(channel, K)` in ascending message ID, re-parsing `raw_header`, through the put and del appliers.
3. Upsert `projection_operations` outcomes; cleanup rows are inserted with `INSERT OR IGNORE`.

Only key K is touched. Nothing else in the channel is rebuilt. With the
order-independent rules of section 6.4 the result equals an in-order apply;
the repair exists so that an applier bug surfaces as a rebuild-equivalence
test failure rather than divergent state. `TestRepairKeyEqualsInOrderApply`
asserts it.

The common source of out-of-order arrival is the server itself: two
concurrent PUTs to one key project their receipts in completion order, not in
message ID order.

### 9.5 Deletion reconciliation

| Source | Handling |
| --- | --- |
| `updates.getChannelDifference` other-updates with deleted message IDs | After the page transaction, record each ID in `deleted_messages` (source `external` unless a cleanup row already recorded it). If an ID is the put or body of a live object, remove that object row |
| History fallback | `internal/syncer/reconcile` checks the existence of every live object's put and body with `channels.getMessages` in batches of 100, at most once per channel per 24 h (`channels.last_reconciled_at`), and records missing IDs the same way with source `reconcile` |
| Own cleanup | Confirmed `deleteMessages` IDs are recorded with source `cleanup` |

`deleted_messages` is preserved across rebuilds, so a rebuild never revives an
object whose messages are gone.

### 9.6 Tamper

The first op observed for `(channel_id, msg_id)` stays canonical. A later
observation with a different header hash writes `replay_log_tamper` and changes
nothing else (legacy section 10).

---
