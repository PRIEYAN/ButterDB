# Phase 10 — Fyne admin app (apps/admin module)
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement fyne admin app (apps/admin module) as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 09 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/admin-gui.md`.
- `docs/spec/admin-api.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Create `apps/admin/go.mod` with module `github.com/PRIEYAN/ButterDB/apps/admin`, update `go.work`, and commit the shell, navigation, and client interfaces in `docs/spec/admin-gui.md` §3 before fan-out. Add exact Fyne import paths such as `fyne.io/fyne/v2/app@v2.6.0` and `fyne.io/fyne/v2/test@v2.6.0`; the spec sets v2.6.0 as the minimum. Commit as `define fyne admin app contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/10-fyne-admin-app-l1` | apps/admin/internal/ui/connect (server URL, cert pin, setup token, device token in OS keychain) |
| L2 | `phase/10-fyne-admin-app-l2` | apps/admin/internal/ui/telegramlink (api_id, api_hash, phone, code, 2FA wizard) |
| L3 | `phase/10-fyne-admin-app-l3` | apps/admin/internal/ui/{buckets,keys} |
| L4 | `phase/10-fyne-admin-app-l4` | apps/admin/internal/ui/objects (browse, upload, download, delete, presign, preview) |
| L5 | `phase/10-fyne-admin-app-l5` | apps/admin/internal/ui/{dashboard,settings} |
| L6 | `phase/10-fyne-admin-app-l6` | apps/admin/packaging (fyne package, icons, build scripts for Windows, Linux, macOS) |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — apps/admin/internal/ui/connect (server URL, cert pin, setup token, device token in OS keychain)

Implement apps/admin/internal/ui/connect (server URL, cert pin, setup token, device token in OS keychain). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 10 apps admin internal ui connect server url cer` after its package tests pass.

### 5.2 Lane L2 — apps/admin/internal/ui/telegramlink (api_id, api_hash, phone, code, 2FA wizard)

Implement apps/admin/internal/ui/telegramlink (api_id, api_hash, phone, code, 2FA wizard). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 10 apps admin internal ui telegramlink api id ap` after its package tests pass.

### 5.3 Lane L3 — apps/admin/internal/ui/{buckets,keys}

Implement apps/admin/internal/ui/{buckets,keys}. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 10 apps admin internal ui buckets keys` after its package tests pass.

### 5.4 Lane L4 — apps/admin/internal/ui/objects (browse, upload, download, delete, presign, preview)

Implement apps/admin/internal/ui/objects (browse, upload, download, delete, presign, preview). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 10 apps admin internal ui objects browse upload ` after its package tests pass.

### 5.5 Lane L5 — apps/admin/internal/ui/{dashboard,settings}

Implement apps/admin/internal/ui/{dashboard,settings}. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 10 apps admin internal ui dashboard settings` after its package tests pass.

### 5.6 Lane L6 — apps/admin/packaging (fyne package, icons, build scripts for Windows, Linux, macOS)

Implement apps/admin/packaging (fyne package, icons, build scripts for Windows, Linux, macOS). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 10 apps admin packaging fyne package icons build` after its package tests pass.

## 6. Integration lane

apps/admin/main.go, apps/admin/internal/app wiring, apps/admin acceptance tests (fyne test driver)
Own `internal/core`, `cmd/`, and `test/acceptance/phase10` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase10/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestSetupAndTelegramLinkWizardCompletes`: setup and Telegram link wizard completes.
  - `TestBucketAndKeyCRUDUseTheAdminClient`: bucket and key CRUD use the admin client.
  - `TestObjectUploadDownloadAndPreviewWork`: object upload download and preview work.
  - `TestDeviceTokenIsStoredInOSKeychain`: device token is stored in OS keychain.
## 8. Owner gates

The owner performs the GUI walkthrough on Windows, Linux, and macOS before phase 10 acceptance. Record unavailable operating systems as explicit gaps in the phase status note.

## 9. Exit criteria and handoff

All L1–L6 commits have passed monitoring and review; the integration build and phase 10 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 11.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding contracts for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/admin-gui.md`

## 3. Prelude contracts

### 3.1 `internal/model` (pure Go)

```go
package model

type Route string

const (
	RouteConnect   Route = "connect"
	RouteTelegram  Route = "telegram"
	RouteDashboard Route = "dashboard"
	RouteBuckets   Route = "buckets"
	RouteKeys      Route = "keys"
	RouteObjects   Route = "objects"
	RouteReplica   Route = "replica"
	RouteSettings  Route = "settings"
)

type Params map[string]string

const (
	ParamBucket = "bucket"
	ParamPrefix = "prefix"
	ParamStep   = "step"
)

type TokenStorage string

const (
	TokenKeychain TokenStorage = "keychain"
	TokenFile     TokenStorage = "file"
	TokenMemory   TokenStorage = "memory"
)

type Profile struct {
	ServerID      string        `json:"server_id"`
	Label         string        `json:"label"`
	BaseURL       string        `json:"base_url"`
	Trust         adminv1.Trust `json:"trust"`
	DeviceID      string        `json:"device_id"`
	DeviceName    string        `json:"device_name"`
	TokenStorage  TokenStorage  `json:"token_storage"`
	TokenIssuedAt time.Time     `json:"token_issued_at"`
	AddedAt       time.Time     `json:"added_at"`
	LastUsedAt    time.Time     `json:"last_used_at"`
}

var (
	ErrTokenNotFound       = errors.New("device token not found")
	ErrKeychainUnavailable = errors.New("os keychain unavailable")
	ErrProfileNotFound     = errors.New("server profile not found")
)

type TokenStore interface {
	Get(serverID string) (string, error)
	Set(serverID, token string) error
	Delete(serverID string) error
	Kind() TokenStorage
}

type ProfileStore interface {
	List() ([]Profile, error)
	Get(serverID string) (Profile, error)
	Put(p Profile) error
	Delete(serverID string) error
	LastUsed() (string, error)
	SetLastUsed(serverID string) error
}

type Clipboard interface {
	SetText(text string)
	Text() string
}

type Observable[T any] struct {
	mu   sync.Mutex
	v    T
	subs map[int]func(T)
	next int
}

func NewObservable[T any](initial T) *Observable[T]
func (o *Observable[T]) Get() T
func (o *Observable[T]) Set(v T)
func (o *Observable[T]) Subscribe(fn func(T)) (cancel func())

type Presentation string

const (
	PresentInline Presentation = "inline"
	PresentBanner Presentation = "banner"
	PresentDialog Presentation = "dialog"
	PresentRoute  Presentation = "route"
)

type Problem struct {
	Code         adminv1.Code
	Title        string
	Body         string
	Hint         string
	Field        string
	Presentation Presentation
	Route        Route
	Retryable    bool
	RetryAt      time.Time
	RequestID    string
	Details      map[string]any
}

func Describe(err error, now time.Time) Problem
```

`Observable` is a tiny goroutine-safe value cell. `Set` calls subscribers
synchronously on the caller's goroutine, outside the lock; views wrap their
subscriber in `fyne.Do`. `Describe` turns any error from `adminv1` (an
`*adminv1.APIError`, a `*adminv1.PinMismatchError`, a transport error or a
context error) into a `Problem` using the §12 table; its text catalogue lives in
`internal/model/problems.go`.

### 3.2 `internal/shell` (Fyne)

```go
package shell

type Screen interface {
	Route() model.Route
	Title() string
	Icon() fyne.Resource
	Shortcut() fyne.KeyName
	Content() fyne.CanvasObject
	Show(ctx context.Context, p model.Params)
	Hide()
}

type Navigator interface {
	Navigate(r model.Route, p model.Params)
	Current() model.Route
	SetEnabled(r model.Route, enabled bool, reason string)
}

type Notifier interface {
	Problem(p model.Problem)
	Info(text string)
	ClearBanner()
}

type Shell struct{}

func New(app fyne.App, win fyne.Window) *Shell
func (s *Shell) Register(sc Screen)
func (s *Shell) Navigator() Navigator
func (s *Shell) Notifier() Notifier
func (s *Shell) SetServerLabel(label string, connected bool)
func (s *Shell) Window() fyne.Window

func NewCopyField(label string, value string, clip model.Clipboard) fyne.CanvasObject
func ConfirmTyped(win fyne.Window, title, body, mustType, confirmLabel string, onConfirm func())
func ConfirmDestructive(win fyne.Window, title, body, confirmLabel string, onConfirm func())
func NewProblemView(p model.Problem, clip model.Clipboard) fyne.CanvasObject
func FormatBytes(n int64) string
func FormatAge(t, now time.Time) string
```

`Show` receives a context that the shell cancels when the screen is hidden or
the session changes, so every in-flight call and poll of a hidden screen stops.
`Shell.Problem` renders `PresentBanner` problems in the banner strip above the
content, `PresentDialog` problems as a modal built from `NewProblemView`, and
for `PresentRoute` navigates to `Problem.Route` after showing the dialog.
`PresentInline` problems are rendered by the screen itself; if a screen passes
one to the notifier, it falls back to a dialog.

### 3.3 Client ports

Each view-model package declares the narrow interface it consumes, with method
signatures copied exactly from `*adminv1.Client` (admin-api.md §16.7), so the
composition root passes the concrete client and tests pass a hand-written fake.

| Package | Port | Methods |
| --- | --- | --- |
| `connect/vm` | `Dialer` | `Probe(ctx, baseURL) (adminv1.CertificateInfo, error)`, `Dial(cfg adminv1.ClientConfig) (ConnectClient, error)` |
| `connect/vm` | `ConnectClient` | `ServerInfo`, `Claim`, `RequestPairing`, `CompletePairing`, `RotateDeviceToken`, `SetToken` |
| `telegramlink/vm` | `LoginFlow` | `Status`, `SaveApp`, `DeleteApp`, `Start`, `SubmitCode`, `SubmitPassword`, `Cancel` (§6.1) |
| `dashboard/vm` | `Client` | `Status`, `Usage` |
| `buckets/vm` | `Client` | `ListBuckets`, `CreateBucket`, `GetBucket`, `UpdateBucket`, `DeleteBucket` |
| `keys/vm` | `Client` | `ListAccessKeys`, `CreateAccessKey`, `UpdateAccessKey`, `RevokeAccessKey`, `ListBuckets`, `Status` |
| `objects/vm` | `Client` | `ListBuckets`, `ListObjects`, `HeadObject`, `ObjectMeta`, `GetObject`, `PutObject`, `DeleteObject`, `DeleteObjects`, `Presign`, `ListAccessKeys`, `Status` |
| `settings/vm` | `Client` | `ServerInfo`, `ListDevices`, `RenameDevice`, `RevokeDevice`, `RotateDeviceToken`, `TelegramStatus`, `UnlinkTelegram`, `Audit` |
| `replica/vm` | `Client` | `ReplicaStatus`, `UnlinkReplica`, plus a `LoginFlow` adapter over the replica methods |

`internal/app` provides the `Dialer` (wrapping `adminv1.ProbeCertificate` and
`adminv1.NewClient`) and two `LoginFlow` adapters, `primaryFlow` and
`replicaFlow`, over the Telegram and replica methods of `*adminv1.Client`.

### 3.4 View-model shape

Every view model follows one pattern so lanes stay uniform:

```go
package vm

type State struct {
	Loading bool
	Problem *model.Problem
}

type ViewModel struct {
	state *model.Observable[State]
}

func New(c Client, now func() time.Time) *ViewModel
func (v *ViewModel) State() *model.Observable[State]
func (v *ViewModel) Refresh(ctx context.Context)
```

The real `State` structs carry screen data (§7 to §11). Command methods
(`Refresh`, `Create`, `Delete`, `Submit…`) are **synchronous**: they call the
client, update the observable and return. Views call them with `go`, which
keeps the view model deterministic under test.

---

## Additional binding contracts and checks

### admin-gui.md: 16. Testing

## 16. Testing

### 16.1 Tools

| Layer | Tool |
| --- | --- |
| View models | Plain `testing` with hand-written fakes of each `vm` port; no Fyne |
| Views | `fyne.io/fyne/v2/test`: `test.NewTempApp(t)`, `test.NewTempWindow(t, content)`, `test.Type`, `test.Tap`, `test.FocusNext`, `test.AssertRendersToMarkup` for stable layout snapshots under `testdata/` |
| Stores | Temp directories; `keyring.MockInit()` from go-keyring for the keychain store |
| App acceptance | `httptest.NewTLSServer` serving canned admin API JSON, driven through the real `adminv1.Client` and the real composition root with the test driver |

Tests never need a display: the Fyne test driver renders in software. They
still build with `CGO_ENABLED=1`, because the module links GLFW.

### 16.2 Required tests (names are binding)

| Package | Tests |
| --- | --- |
| `internal/model` | `TestObservableNotifiesSubscribers`, `TestObservableCancel`, `TestDescribeCoversEveryCode`, `TestDescribeFloodWaitUsesUntil`, `TestDescribePinMismatch`, `TestDescribeUnknownCodeFallsBack` |
| `ui/connect/vm` | `TestNormaliseURL`, `TestHTTPOnlyForLoopback`, `TestProbeThenConfirmPins`, `TestServerInfoPinDisagreementAborts`, `TestAPIVersionMismatchAborts`, `TestClaimStoresTokenBeforeProfile`, `TestClaimClearsSetupToken`, `TestPairingSavedMessagesDisabledWhenUnlinked`, `TestPairingCodeFormatting`, `TestPairingInvalidKeepsForm`, `TestRotateStoresBeforeSwitching`, `TestRotateStoreFailureKeepsOldToken`, `TestUnauthenticatedDeletesToken`, `TestPinMismatchBlocks`, `TestTrustNewKeyRequiresConfirmation` |
| `ui/connect/credstore` | `TestKeychainRoundTrip`, `TestKeychainNotFoundMapped`, `TestFileStoreMode0600`, `TestFileStoreAtomicWrite`, `TestMemoryStoreKind` |
| `ui/connect/profilestore` | `TestProfilesRoundTrip`, `TestProfilesFileMode`, `TestProfilesKeyedByServerID` |
| `ui/telegramlink/vm` | `TestStepFollowsNext`, `TestCodeInvalidStaysOnStep`, `TestPasswordInvalidStaysOnStep`, `TestCodeExpiredReturnsToPhone`, `TestAttemptsExhaustedReturnsToPhone`, `TestFloodWaitDisablesSubmit`, `TestLoginInProgressResumes`, `TestSecretsClearedAfterSubmit`, `TestCancelReturnsToPhone` |
| `ui/telegramlink` | `TestWizardRendersEachStep`, `TestCodeEntryFocusedAfterInvalid`, `TestHintShown` |
| `ui/buckets/vm` | `TestListSorted`, `TestNamePrecheck`, `TestCreateShowsServerError`, `TestDeleteNotEmpty` |
| `ui/keys/vm` | `TestCreateReturnsSecretOnce`, `TestSecretDroppedOnDone`, `TestScopeAllBuckets`, `TestRevokeRefreshes`, `TestEnvVarCopyFormat` |
| `ui/keys` | `TestSecretDialogNotDismissableUntilAcknowledged`, `TestCopyButtonsUseClipboard` |
| `ui/objects/vm` | `TestListFoldersFirst`, `TestLoadMoreAppendsPage`, `TestBreadcrumbSegments`, `TestUploadAsksBeforeReplace`, `TestUploadUsesIfMatch`, `TestUploadUsesIfNoneMatchForNewKey`, `TestUploadIdempotencyKeyStableAcrossRetries`, `TestCommitOutcomeUnknownRetries`, `TestUploadTooLargeNotSent`, `TestCommittingNotCancellable`, `TestDownloadWritesPartThenRenames`, `TestDownloadVerifiesMD5`, `TestDownloadKeySanitised`, `TestDeleteBatchesOf1000`, `TestDeletePartialFailure`, `TestPresignKeysFilteredByReadScope`, `TestPreviewSizeCeiling` |
| `ui/objects` | `TestDropUploadsUnderPrefix`, `TestBackspaceGoesUp`, `TestDeleteKeyAsksConfirmation` |
| `ui/dashboard/vm` | `TestStatusAndUsageMerged`, `TestPollStopsOnHide` |
| `ui/settings/vm` | `TestRevokeLastDeviceAsksTwice`, `TestRevokeSelfRoutesToConnect`, `TestUnlinkRequiresTypedWord`, `TestAuditFilterPrefix` |
| `ui/replica/vm` (phase 12) | `TestNotImplementedPlaceholder`, `TestReplicaWizardReusesFlow`, `TestBackfillProgress` |
| `internal/app` | `TestImportRules`, `TestRoutingUnlinkedGoesToTelegram`, `TestRoutingLinkedGoesToDashboard`, `TestSwitchServerCancelsSession`, `TestAcceptanceClaimLinkCreateUploadDownload`, `TestAcceptancePinMismatch`, `TestAcceptanceDeviceRevoked` |

`TestAcceptanceClaimLinkCreateUploadDownload` runs the canned-API TLS server
through: probe and confirm, claim, link with one invalid code, create a bucket
and a key (secret dialog acknowledged), upload with progress, list, download,
presign and delete.

Manual walkthrough on each OS is the phase 10 owner gate: connect and pin,
keychain prompt behaviour, drag-and-drop upload, download dialog, theme
switching, and `Tab` traversal of every screen.

---

### admin-gui.md: 17. Packaging

## 17. Packaging (`apps/admin/packaging`, phase 10 L6)

### 17.1 Metadata

`apps/admin/FyneApp.toml` (prelude):

```toml
Website = "https://github.com/PRIEYAN/ButterDB"

[Details]
Icon = "packaging/icon.png"
Name = "butterDB Admin"
ID = "io.github.prieyan.butterdb.admin"
Version = "0.1.0"
Build = 1
```

L6 adds `packaging/icon.png` (1024 × 1024 PNG) and a release program
`packaging/cmd/release` (package `main`, Go only, because shell scripts are
allowed only in `scripts/` and `deploy/`, docs/claude.md hard rule 1). It runs
`fyne package` for the host OS, then writes the archive and a
`SHA256SUMS` line into `apps/admin/dist/`:

```
go -C apps/admin run ./packaging/cmd/release -version 0.1.0
```

| Host | Command the release program runs | Output |
| --- | --- | --- |
| Linux | `go -C apps/admin run fyne.io/tools/cmd/fyne package -os linux -release` | `butterdb-admin_0.1.0_linux_<arch>.tar.xz` (binary, `.desktop` file, icon, Makefile installer) |
| Windows | same with `-os windows` | `butterdb-admin_0.1.0_windows_amd64.zip` holding `butterdb-admin.exe` with embedded icon and manifest |
| macOS | same with `-os darwin` | `butterdb-admin_0.1.0_darwin_<arch>.zip` holding `butterDB Admin.app` |

The phase 10 prelude adds `fyne.io/tools` to `apps/admin/go.mod` so the
`go run` form uses a pinned version. L6 confirms the flag spelling of the pinned
tool with `fyne package --help` and records it in the release program.

### 17.2 Cross builds

`fyne-cross` builds inside Docker or Podman images that carry the C toolchain:

| Target | Command |
| --- | --- |
| Linux amd64 and arm64 | `fyne-cross linux -arch=amd64,arm64 -app-id io.github.prieyan.butterdb.admin` (run in `apps/admin`) |
| Windows amd64 | `fyne-cross windows -arch=amd64 -app-id io.github.prieyan.butterdb.admin` |
| macOS | Built natively on a Mac. `fyne-cross darwin` needs a macOS SDK image extracted from Xcode, which the owner must provide under Apple's licence |

### 17.3 cgo toolchain requirements

| OS | Build requirements | Runtime requirements |
| --- | --- | --- |
| Linux (Debian, Ubuntu) | `gcc`, `pkg-config`, `libgl1-mesa-dev`, `xorg-dev` (X11, Xcursor, Xrandr, Xinerama, Xi, Xxf86vm headers) | OpenGL 2.1 driver, X11 or XWayland, a session D-Bus and a Secret Service provider for the keychain |
| Linux (Fedora) | `gcc`, `libXcursor-devel`, `libXrandr-devel`, `mesa-libGL-devel`, `libXi-devel`, `libXinerama-devel`, `libXxf86vm-devel` | As above |
| Linux (Arch) | `base-devel`, `libxcursor`, `libxrandr`, `libxinerama`, `libxi`, `mesa` | As above |
| Linux, native Wayland | Add `-tags wayland`; needs `libwayland-dev`, `libxkbcommon-dev`, `wayland-protocols`. Default (owner may change): release builds use X11/XWayland |  |
| macOS | Xcode Command Line Tools (`xcode-select --install`) | macOS 11 or newer |
| Windows | A MinGW-w64 GCC on `PATH` (MSYS2 `mingw-w64-x86_64-gcc`, or TDM-GCC), `CGO_ENABLED=1` | An OpenGL 2.1 driver; in VMs without one, Mesa's `opengl32.dll` placed beside the exe |

The phase 10 pre-flight check is `cc --version` and `pkg-config --exists gl x11
xrandr xcursor xinerama xi` (docs/phases/00-execution-protocol.md §17.3).

### 17.4 Signing

Release archives are unsigned in v0.1.0. Default (owner may change). The
operator guide documents the first-run steps: macOS Gatekeeper `Open` from the
context menu (or `xattr -d com.apple.quarantine`), and Windows SmartScreen
`More info → Run anyway`. Code signing and notarisation are an owner gate for a
later release because they need the owner's certificates.

---
