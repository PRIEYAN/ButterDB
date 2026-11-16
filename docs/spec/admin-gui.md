# Admin GUI specification (`apps/admin`)

The admin GUI is a native Fyne desktop app for Windows, Linux and macOS (D13).
It is a remote client of the admin API ([admin-api.md](admin-api.md)) and holds
no Telegram session, no server key and no object data beyond what the owner
explicitly downloads (D14). Phase 10 builds it; phase 12 adds the replica
screen.

| Item | Value |
| --- | --- |
| Module | `github.com/PRIEYAN/ButterDB/apps/admin` (own `go.mod`, joined by the root `go.work`, E14) |
| Imports from the root module | `github.com/PRIEYAN/ButterDB/api/adminv1` only |
| cgo | Required (`CGO_ENABLED=1`): Fyne renders through OpenGL via GLFW |
| UI toolkit | `fyne.io/fyne/v2`, minimum v2.6.0 (the release that introduced `fyne.Do`) |
| Keychain | `github.com/zalando/go-keyring`, minimum v0.2.4 |
| Image decoders | standard library `image/jpeg`, `image/png`, `image/gif`; `golang.org/x/image/webp`, `golang.org/x/image/bmp` |
| Packaging tool | `fyne.io/tools/cmd/fyne` (`fyne package`), `github.com/fyne-io/fyne-cross` for cross builds |
| App ID | `io.github.prieyan.butterdb.admin` |
| Display name | `butterDB Admin` |
| Executable | `butterdb-admin` (`butterdb-admin.exe`, `butterDB Admin.app`) |
| Built in | Phase 10 (replica screen: phase 12 L6) |

The phase 10 prelude pins each module to the newest release that is at least
30 days old and at or above the minimum shown, with `go get` on exact package
paths (E12). Bound by: D3, D7, D10, D11, D12, D13, D14, D15, A1, A2, A3, A9.

---

## 1. Principles

1. **The server is the source of truth.** Every list, count and state on screen
   comes from an `adminv1.Client` call. The app caches nothing across restarts
   except server profiles (§4.1) and app preferences (§11.7).
2. **No business logic in widgets.** Validation that matters, Telegram login
   decisions, scope rules and limits live on the server. The app pre-checks
   only to give faster feedback (bucket name syntax, `api_hash` shape), and the
   server's answer always wins.
3. **View models are plain Go.** A view model package never imports
   `fyne.io/...`. Widgets render view-model state and forward user intent.
4. **Branch on codes.** The app decides what to show from `ErrorBody.Code`
   (§12), never from `message` text.
5. **Secrets pass through, never stay.** The setup token, pairing code,
   `api_hash`, phone number, login code, 2FA password and access-key secret
   live only in the widget and view model that collect or show them, are never
   logged, and are cleared when the step ends. The device token is kept only in
   the OS keychain (§5).
6. **Nothing blocks the UI goroutine.** Every API call, file read and file
   write runs on a worker goroutine; results reach widgets through `fyne.Do`
   (§13.3).

---

## 2. Module layout and lane ownership

```
apps/admin/
├── go.mod                         prelude
├── FyneApp.toml                   prelude (app ID, name, version, icon path)
├── main.go                        prelude stub, integration lane final
├── internal/
│   ├── model/                     prelude: pure Go contracts (no fyne import)
│   ├── shell/                     prelude: window, sidebar, navigator, shared widgets
│   ├── app/                       integration lane: composition root
│   └── ui/
│       ├── connect/               L1: view, wizard steps
│       │   ├── vm/                L1: view model
│       │   ├── credstore/         L1: keychain TokenStore and file fallback
│       │   └── profilestore/      L1: servers.json ProfileStore
│       ├── telegramlink/          L2: view, reusable LoginWizard
│       │   └── vm/                L2
│       ├── buckets/  buckets/vm/  L3
│       ├── keys/     keys/vm/     L3
│       ├── objects/  objects/vm/  L4 (browser, transfers, preview)
│       ├── dashboard/ dashboard/vm/  L5
│       ├── settings/ settings/vm/    L5 (connection, devices, Telegram account, activity, app preferences)
│       └── replica/  replica/vm/  phase 12 L6
└── packaging/                     L6: icon, release program, per-OS notes
```

| Lane (phase 10) | Owned paths |
| --- | --- |
| Prelude | `apps/admin/go.mod`, `apps/admin/go.sum`, `go.work` (`go work use ./apps/admin`), `apps/admin/FyneApp.toml`, `apps/admin/main.go` (stub), `apps/admin/internal/model/`, `apps/admin/internal/shell/` |
| L1 connect | `apps/admin/internal/ui/connect/` |
| L2 Telegram link | `apps/admin/internal/ui/telegramlink/` |
| L3 buckets and keys | `apps/admin/internal/ui/buckets/`, `apps/admin/internal/ui/keys/` |
| L4 objects | `apps/admin/internal/ui/objects/` |
| L5 dashboard and settings | `apps/admin/internal/ui/dashboard/`, `apps/admin/internal/ui/settings/` |
| L6 packaging | `apps/admin/packaging/` |
| Integration | `apps/admin/main.go`, `apps/admin/internal/app/` |
| Phase 12 L6 | `apps/admin/internal/ui/replica/` |

Import rules inside the module, checked by `TestImportRules` in
`apps/admin/internal/app` (it parses every package with `go/parser`):

| Package | May import |
| --- | --- |
| `internal/model` | standard library, `api/adminv1` |
| `internal/ui/*/vm` | standard library, `api/adminv1`, `internal/model` |
| `internal/ui/connect/credstore`, `internal/ui/connect/profilestore` | standard library, `internal/model`, `github.com/zalando/go-keyring` |
| `internal/shell` | `internal/model`, `api/adminv1`, `fyne.io/fyne/v2/...` |
| `internal/ui/<screen>` | its own `vm`, `internal/model`, `internal/shell`, `api/adminv1`, `fyne.io/fyne/v2/...`, image decoders (objects only), `internal/ui/telegramlink` (replica only) |
| `internal/app` | everything in the module |

No screen package imports another screen package, except `replica` importing
the exported `telegramlink.LoginWizard`.

---

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

## 4. Connection, trust and pairing

### 4.1 Server profiles

Profiles live in `servers.json` under `os.UserConfigDir()`:

| OS | Path |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/butterdb-admin/servers.json` (default `~/.config/butterdb-admin/servers.json`) |
| macOS | `~/Library/Application Support/butterdb-admin/servers.json` |
| Windows | `%AppData%\butterdb-admin\servers.json` |

```json
{
  "version": 1,
  "last_server_id": "k3v9m2q8t4w7x1z5a6b2c3d4e5",
  "servers": [
    {
      "server_id": "k3v9m2q8t4w7x1z5a6b2c3d4e5",
      "label": "home server",
      "base_url": "https://203.0.113.10:9443",
      "trust": {"mode": "pinned", "spki_sha256": "SHA256:4f1c9a0e…"},
      "device_id": "a1b2c3d4e5f6g7h2",
      "device_name": "Office laptop",
      "token_storage": "keychain",
      "token_issued_at": "2026-11-12T09:41:07.312Z",
      "added_at": "2026-11-12T09:41:07.312Z",
      "last_used_at": "2026-11-13T08:00:00.000Z"
    }
  ]
}
```

`profilestore` writes the file through a temp file, `fsync` and rename, with
mode `0600` and directory mode `0700`. The file holds no secret: the pin is
public information, and its integrity is only as strong as the user's own
account, which also guards the keychain. The `trust` object is
`adminv1.Trust` with its JSON tags (admin-api.md §16.7). A profile is keyed by
`server_id`, so a server reached through a new URL or IP keeps its token.

### 4.2 First contact (TOFU)

```mermaid
sequenceDiagram
  participant O as Owner
  participant G as GUI
  participant S as butterdbd
  O->>G: server URL
  G->>S: TLS handshake only (ProbeCertificate)
  alt verifies with system roots
    G->>O: show issuer and host; trust mode "system"
  else self-signed
    G->>O: show display fingerprint
    O->>O: compare with "admin listener" log line
    O->>G: Fingerprints match
  end
  G->>S: GET /admin/v1/server-info (pinned client)
  G->>G: server-info tls.spki_sha256 equals probed pin?
  alt unclaimed
    G->>O: claim form (§4.3)
  else claimed, no token
    G->>O: pairing form (§4.3)
  end
```

URL normalisation in `connect/vm`:

| Input | Result |
| --- | --- |
| `host` or `host:port` | `https://host[:port]` |
| No port | Port `9443` (the default admin listener) |
| `http://` | Accepted only for `localhost`, `127.0.0.1` or `::1` (an SSH tunnel to a `proxy`-mode listener), with a warning line; the certificate steps are skipped and the profile stores trust mode `system`. Otherwise rejected |
| Path | Stripped; the client adds `/admin/v1` |

Certificate confirmation dialog, for a certificate that does not verify with
system roots:

| Element | Content |
| --- | --- |
| Title | `Verify this server` |
| Fingerprint | `CertificateInfo.DisplayFingerprint` in a monospace, selectable label, 4 lines of 4 groups |
| Instructions | `On the server, run: journalctl -u butterdbd \| grep "admin listener"` (or `docker logs <container>`) and compare with `tls_fingerprint_display` |
| Certificate facts | Subject, DNS names, IP addresses, expiry |
| Buttons | `Fingerprints match` (not the default button), `Cancel` (default, bound to Escape) |

For a certificate that verifies with system roots, the dialog shows the subject
and issuer and offers `Trust system certificate` (mode `system`) with an
advanced toggle to pin the key instead. After confirmation the app builds a
pinned or system client and calls `ServerInfo`. When `tls.spki_sha256` in the
response is non-empty and differs from the probed pin, the app aborts with
`The server reports a different certificate than the one presented. A proxy
or interceptor is in the path.` and saves nothing. `api_version` other than
`v1` aborts with `This server speaks admin API <version>; update butterDB
Admin.`

### 4.3 Claim and pairing

| `server-info` state | Form | Fields | Call |
| --- | --- | --- | --- |
| `unclaimed` | Claim | Setup token (password entry; trimmed; must start `bdst1_`), device name (default `os.Hostname()`, 1 to 64 characters) | `Claim` |
| `claimed`, no stored token | Pair | Delivery choice, device name, code | `RequestPairing` then `CompletePairing` |

Pairing form behaviour:

1. Two choices. `Send a code to my Telegram Saved Messages` is enabled only
   when `pairing_methods` contains `saved_messages`; otherwise it shows
   `Telegram is not linked yet. Use the host command instead.`
   `I will run butterdbd admin-token on the server` is always enabled and
   shows the exact command.
2. Choosing Saved Messages calls `RequestPairing` with the device name and
   `platform = runtime.GOOS + "/" + runtime.GOARCH`, then shows the expiry
   countdown from `expires_at`.
3. The code entry accepts `XXXXX-XXXXX`, inserts the hyphen after 5
   characters, uppercases as typed, and enables `Pair` at 10 characters. The
   server normalises again (admin-api.md §5.4).
4. `pairing_code_invalid` clears the entry, keeps focus on it and shows
   `That code is wrong or expired. Request a new one if it expired.`

On `201 DeviceCredentials` from either call, `connect/vm` performs, in order:

1. `TokenStore.Set(server_id, device_token)` (§5). If that fails with
   `ErrKeychainUnavailable`, the fallback choice of §5.3 runs first.
2. `ProfileStore.Put` with the trust, device ID, device name, token storage
   kind and `token_issued_at`.
3. `ProfileStore.SetLastUsed(server_id)`.
4. Clear the setup token or code from the view model and the entry widget.
5. Emit `Connected(profile)` to the composition root, which builds the session
   client and routes by Telegram state (§4.6).

### 4.4 Fingerprint change

When a pinned client gets `*adminv1.PinMismatchError`, no request has been sent
(admin-api.md §16.7). The app replaces the whole content area with a blocking
view; no other screen is reachable for that server.

| Element | Content |
| --- | --- |
| Title | `Server identity changed` |
| Body | `The certificate key of <label> is not the one this device trusted. This happens when the server's tls/admin.key was deleted or replaced, or when someone is intercepting the connection.` |
| Facts | Expected and received fingerprints, both in display form |
| Actions | `Disconnect` (default); `Trust the new key…` |

`Trust the new key…` reruns §4.2 against the same URL and requires the owner to
confirm the new fingerprint against the server log. The stored device token is
kept and tried with the new pin; if the server answers `unauthenticated`, the
token is deleted and the pairing form opens. A changed key never silently
updates the pin.

### 4.5 Token rotation and revocation

| Event | App behaviour |
| --- | --- |
| Connect with `token_issued_at` older than 30 days | Call `RotateDeviceToken`; `TokenStore.Set` the new token; only after a successful store call `SetToken(new)` and update `token_issued_at`. If the store fails, keep using the old token and show a banner `Could not save the rotated token; this device must re-pair within 10 minutes.` Default (owner may change): 30 days |
| `Rotate token now` in Settings | Same sequence |
| `unauthenticated` on any call | Delete the stored token, show `This device is no longer signed in to <label>.`, route to the pairing form |
| `device_revoked` | Same, with `This device was revoked at <revoked_at>.` |
| Revoke this device in Settings | `ConfirmDestructive`, `RevokeDevice` with this device's ID, then delete the stored token and the profile, and route to Connect |

### 4.6 Session start and routing

After `Connected`, the composition root:

1. Builds `*adminv1.Client` from the profile (`BaseURL`, `Trust`, token from
   `TokenStore.Get`, `UserAgent` `butterdb-admin/<version> (<goos>/<goarch>)`,
   `JSONTimeout` 75 s).
2. Calls `ServerInfo` and `TelegramStatus`.
3. Constructs every screen with the session client and registers it.
4. Routes:

| Condition | Route | Sidebar |
| --- | --- | --- |
| `TelegramStatus.state != "linked"` | `telegram` | Dashboard, Buckets, Keys, Objects and Replica disabled with reason `Link Telegram first`; Settings enabled |
| Linked | `dashboard`, or the last route used for this server | All enabled |

Switching server (Settings or the sidebar footer) cancels the session context,
unregisters every screen, clears view-model state, and returns to Connect with
the profile list.

### 4.7 Connect screen layout

| Region | Content |
| --- | --- |
| Profile list | One row per profile: label, URL, `last used` age, trust mode icon with text; `Connect`, `Rename`, `Forget` |
| Add server | URL entry, optional label, `Continue` |
| Forget | `ConfirmDestructive`: deletes the profile and its stored token. It does not revoke the device on the server; the body says so and suggests revoking from another device |

On start the app opens the `last_server_id` profile automatically when its
token is present; a failure leaves the owner on the Connect screen with the
problem shown.

---

## 5. Device token storage

### 5.1 Keychain (default)

`credstore.Keychain` implements `model.TokenStore` with
`github.com/zalando/go-keyring`:

| Item | Value |
| --- | --- |
| Service | `butterdb-admin` |
| User (account) | `server_id` |
| Secret | The device token string (`bdt1_…`, 74 characters) |
| `Kind()` | `keychain` |

| OS | Backend used by go-keyring | Runtime requirement |
| --- | --- | --- |
| Windows | Credential Manager (generic credential, through `github.com/danieljoos/wincred`) | None |
| macOS | Login keychain, through the `/usr/bin/security` tool | None; the first access may show a keychain prompt |
| Linux | Secret Service API over D-Bus (`github.com/godbus/dbus/v5`): GNOME Keyring, KWallet 5.97+ or KeePassXC with Secret Service enabled | A session D-Bus and a running Secret Service provider |

go-keyring needs no cgo for any of these. Errors are mapped:
`keyring.ErrNotFound` → `model.ErrTokenNotFound`; `keyring.ErrUnsupportedPlatform`,
a D-Bus connection failure or a missing `org.freedesktop.secrets` name →
`model.ErrKeychainUnavailable`.

### 5.2 Probe at start

At start the composition root calls `Keychain.Set("butterdb-admin-probe",
"probe")` then `Delete`. Success selects the keychain store; failure with
`ErrKeychainUnavailable` selects the fallback flow below on the first token
write.

### 5.3 Fallback

When the keychain is unavailable, the app asks once per profile:

| Choice | Store | Behaviour |
| --- | --- | --- |
| `Keep me signed in for this session only` (default) | `credstore.Memory` (`Kind() = memory`) | Token in process memory; the device stays registered on the server, and the next start needs a new pairing code. The app tells the owner to revoke stale devices in Settings |
| `Store the token in a protected file` | `credstore.File` (`Kind() = file`) | `<UserConfigDir>/butterdb-admin/tokens/<server_id>.token`, file mode `0600`, directory mode `0700`, written by temp file, `fsync` and rename. The dialog states that any program running as this user can read it |

Default (owner may change): memory-only as the default fallback. The chosen
kind is recorded in the profile (`token_storage`), and Settings shows it with a
`Move to keychain` action when a keychain later becomes available.

### 5.4 What is never stored

The app never writes to disk, the keychain or its log: the setup token, a
pairing code, `api_id`/`api_hash`, the phone number, a login code, the 2FA
password, an access-key secret or a presigned URL.

---

## 6. Telegram link wizard (`ui/telegramlink`)

### 6.1 Port

```go
package vm

type LoginFlow interface {
	Status(ctx context.Context) (adminv1.TelegramStatus, error)
	SaveApp(ctx context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error)
	DeleteApp(ctx context.Context) (adminv1.TelegramStatus, error)
	Start(ctx context.Context, phone string) (adminv1.TelegramStatus, error)
	SubmitCode(ctx context.Context, loginID, code string) (adminv1.TelegramStatus, error)
	SubmitPassword(ctx context.Context, loginID, password string) (adminv1.TelegramStatus, error)
	Cancel(ctx context.Context) (adminv1.TelegramStatus, error)
}
```

`primaryFlow` maps these to `TelegramStatus`, `SaveTelegramCredentials`,
`DeleteTelegramCredentials`, `StartTelegramLogin`, `SubmitTelegramCode`,
`SubmitTelegramPassword` and `CancelTelegramLogin`. `replicaFlow` maps them to
the replica methods and converts `ReplicaStatus` into a `TelegramStatus`
(`SaveApp` and `DeleteApp` are unsupported there; the replica's own
`api_id`/`api_hash` travel with `Start`, admin-api.md §13).

The exported, reusable component is:

```go
package telegramlink

type LoginWizard struct{}

func NewLoginWizard(win fyne.Window, flow vm.LoginFlow, opts WizardOptions, n shell.Notifier) *LoginWizard
func (w *LoginWizard) Content() fyne.CanvasObject
func (w *LoginWizard) Start(ctx context.Context)
func (w *LoginWizard) OnLinked(fn func(adminv1.TelegramAccount))

type WizardOptions struct {
	AskAppCredentials bool
	AllowReuseApp     bool
	Title             string
}
```

### 6.2 Steps

The wizard renders the step named by `TelegramStatus.next` (admin-api.md §8.2);
it never infers the step locally.

| `next` | Step | Inputs | Submit | Notes |
| --- | --- | --- | --- | --- |
| `app` | 1. App credentials | `api_id` (digits only), `api_hash` (password entry, 32 hex characters, lowercased) | `SaveApp` | Shows `Get these at my.telegram.org → API development tools` as text and a hyperlink opened with `fyne.App.OpenURL`. Shows the account-safety notice (§6.4) |
| `phone` | 2. Phone number | International number, `+` prefix enforced | `Start` | `Change app credentials` link calls `DeleteApp` after confirmation |
| `wait` | Sending code | none | none | Indeterminate progress; polls `Status` every 2 s |
| `code` | 3. Login code | Code entry sized to `code_delivery.length`; numeric keyboard hint | `SubmitCode` | Shows where the code went (`code_delivery.type`, e.g. `Sent to your Telegram app on another device`), `phone_masked`, attempts left, and `expires_at` as a countdown |
| `password` | 4. Two-step password | Password entry, `Show` toggle | `SubmitPassword` | Shows `password_hint` as `Hint: <hint>` when non-empty, attempts left |
| `none` | 5. Done | none | none | Shows account `first_name`, `@username`, `phone_masked`; `Continue to dashboard` |

Every step except Done has `Cancel login`, which calls `Cancel` and returns to
the phone step.

### 6.3 Errors inside the wizard

| Code | Wizard reaction |
| --- | --- |
| `telegram_code_invalid` | Stay on step 3; clear and focus the entry; inline `That code is not right. <attempts_left> attempts left.` No new code is requested; the same code request is still valid |
| `telegram_password_invalid` | Stay on step 4; clear and focus; inline `Wrong password. <attempts_left> attempts left.` |
| `telegram_code_expired` | Go to step 2 with inline `The code expired. Request a new one.` |
| `telegram_attempts_exhausted` | Go to step 2 with inline `Too many wrong attempts. Start again.` |
| `telegram_flood_wait` | Stay on the step; disable submit; banner countdown to `RetryAt`; re-enable when it reaches zero |
| `telegram_phone_invalid`, `telegram_account_not_registered` | Inline on the phone field |
| `telegram_api_id_invalid` | Go to step 1 with inline `Telegram rejected this api_id and api_hash.` |
| `telegram_email_setup_required` | Dialog: `Telegram asks this account to set up a login email first. Do that in an official Telegram app, then try again.` |
| `telegram_account_mismatch` | Dialog: `This server is bound to Telegram user <bound_user_id>. Log in with that account.` |
| `telegram_login_in_progress` | Refresh `Status` and resume at its `next` (another device started the attempt) |
| `telegram_login_not_active` | Refresh `Status` and render its `next` |
| `telegram_unavailable` | Banner with `Retry`; the attempt stays where it was |
| `telegram_login_failed` | Dialog with `details.telegram_error`; go to step 2 |

While the wizard is visible it polls `Status` every 10 s, so a second device
sees progress made elsewhere. Default (owner may change): 2 s and 10 s.

### 6.4 Account-safety notice

Shown on step 1 and in Settings, from decisions.md §5:

> Use a dedicated Telegram account that is not new, with two-step verification
> on. Never share your api_id. butterDB keeps a send-rate limit on; if Telegram
> restricts the account, objects that are not mirrored to a replica account are
> lost.

### 6.5 Secret handling

Entries for `api_hash`, code and password are `widget.Entry` with
`Password = true` (except the code). Each is cleared with `SetText("")` after
its submit returns, when the step changes, and when the wizard is hidden. The
view model holds no copy after the call returns. Go strings cannot be zeroed;
this residual exposure in process memory is accepted.

---

## 7. Dashboard (`ui/dashboard`)

Sources: `Status` and `Usage` (admin-api.md §9), refreshed on show, every 15 s
while visible, and on `Ctrl+R`.

| Card | Content |
| --- | --- |
| Server | State (`starting`, `unlinked`, `syncing`, `ready`, `degraded`) with icon and text; version and commit; uptime |
| Telegram | Account name and `@username`; `Connected` / `Disconnected`; `Flood wait until <time>` when set; send-rate ceiling per minute |
| Storage | Buckets, objects, stored bytes, plaintext bytes, encrypted objects |
| Cache and spool | Block cache used of limit as a progress bar with text, or the text "Off" with no bar and no hit ratio when the limit is 0 (D21), 1 h hit ratio as a percentage; spool used of limit, jobs in flight |
| Components | One row per `components[]` entry: name, status word, detail, since age |
| Endpoints | S3 public URL and region with copy buttons (`shell.NewCopyField`); a line `Use these with any AWS SDK: endpoint, region, path-style addressing.` |
| Replica | `state` and pending ops from `Status.replica`; hidden when `not_available` |

Status is never conveyed by colour alone: each status has an icon
(`theme.ConfirmIcon`, `theme.WarningIcon`, `theme.ErrorIcon`) and a word.

---

## 8. Buckets (`ui/buckets`)

### 8.1 List

`widget.Table` with sortable column headers:

| Column | Source |
| --- | --- |
| Name | `name` |
| Access | `private` or `public-read` |
| Encryption | `none` or `tde1` shown as `Off` / `On` |
| Cache max-age | `cache_max_age_seconds` as a duration |
| Objects | `object_count` |
| Size | `stored_bytes` |
| Created | `created_at` |

Actions: `New bucket` (`Ctrl+N`), `Settings…`, `Browse objects` (double-click
or Enter opens Objects with `bucket=<name>`), `Delete…`.

### 8.2 Create and configure

| Field | Control | Client pre-check |
| --- | --- | --- |
| Name | Entry | 3 to 63 characters; lowercase letters, digits, `-` and `.`; starts and ends with a letter or digit; no `..`; not an IPv4 address |
| Access | Radio `Private` / `Public read` | Choosing `Public read` shows `Anyone with the URL can read every object in this bucket.` |
| Encryption default | Radio `Off` / `On (TDE1)` | Choosing `On` shows `Encryption keys depend on the server key file. Back it up off the host; losing it makes these objects unrecoverable.` (A6) |
| Cache max-age | Entry in seconds with presets 0, 1 h, 1 day, 7 days | 0 to 31536000 |

The settings dialog edits Access, Encryption default and Cache max-age with
`UpdateBucket`; it states that encryption changes apply to new objects only.
Creating can take seconds because the server creates a Telegram channel and
commits a registry op before answering; the dialog shows progress and stays
open until the response.

### 8.3 Delete

`ConfirmTyped` requiring the bucket name. `bucket_not_empty` shows
`<object_count> objects remain. Delete them first.` with `Open in object
browser`.

---

## 9. Access keys (`ui/keys`)

### 9.1 List

| Column | Source |
| --- | --- |
| Name | `name` |
| Access key ID | `access_key_id`, monospace, copy on `Ctrl+C` when the row is focused |
| Buckets | `scope.buckets` joined, `All buckets` for `["*"]` |
| Permissions | `scope.actions` as words: Read, List, Write, Delete |
| Status | `active` / `revoked` |
| Last used | `last_used_at` age or `never` |
| Created | `created_at` |

A `Show revoked` check toggles `include_revoked`.

### 9.2 Create

Form: name (1 to 64 characters), buckets (`All buckets, including future
ones` check, or a check list of current buckets from `ListBuckets`), actions
(check group `Read`, `List`, `Write`, `Delete`, with presets `Read only` =
read+list and `Read and write` = all four). Submit calls `CreateAccessKey`.

### 9.3 Secret shown once

On `AccessKeyCreated` the app opens a modal that cannot be dismissed by Escape
or by clicking outside:

| Element | Content |
| --- | --- |
| Title | `Save this secret now` |
| Body | `The secret access key is shown only once. butterDB cannot show it again; if you lose it, revoke the key and create a new one.` |
| Fields | Access key ID, secret access key, S3 endpoint, region; each a `shell.NewCopyField` with a `Copy` button |
| Extra copy | `Copy as environment variables` producing `AWS_ACCESS_KEY_ID=…`, `AWS_SECRET_ACCESS_KEY=…`, `AWS_ENDPOINT_URL_S3=…`, `AWS_REGION=…` on four lines |
| Gate | Check `I have stored the secret`; `Done` is disabled until it is ticked |

On `Done` the dialog and view model drop the secret. If the clipboard still
holds text the app put there 60 s earlier, the app clears it. Default (owner
may change): 60 s.

### 9.4 Edit and revoke

`Edit…` changes name and scope with `UpdateAccessKey` (the scope is replaced
whole). `Revoke…` uses `ConfirmDestructive`: `Applications using this key stop
working immediately, and presigned URLs signed with it stop working.`

---

## 10. Object browser (`ui/objects`)

### 10.1 Layout

| Region | Content |
| --- | --- |
| Top bar | Bucket `widget.Select`; breadcrumb of prefix segments as buttons (`photos` › `2026` › `11`); `Up` button; prefix filter entry |
| Main | `widget.Table`: selection check, icon, name relative to the prefix, size, content type, last modified, lock icon for encrypted |
| Side panel | Details and preview of the focused object (§10.7); collapsible with `container.NewHSplit` |
| Bottom | Transfers panel (§10.3, §10.4), hidden when empty |
| Toolbar | `Upload files…`, `Download`, `Delete…`, `Copy presigned URL…`, `Refresh` |

Listing uses `ListObjects` with `delimiter="/"`, `limit=200`. Common prefixes
render first as folders. When `next_cursor` is set, the last row is `Load
more`, which appends the next page. The prefix filter, applied on Enter,
lists with `prefix = current prefix + filter`. Navigating into a folder sets
`prefix` and resets the cursor. S3 has no folders, so the app offers no
`New folder` action; uploading under a prefix is how a folder appears.

### 10.2 Keyboard and mouse

| Input | Action |
| --- | --- |
| Double-click or Enter on a folder | Open it |
| Double-click or Enter on an object | Focus the side panel preview |
| Backspace or `Alt+Left` | Up one prefix level |
| Space | Toggle selection of the focused row |
| `Ctrl+A` | Select all loaded rows |
| `Delete` | `Delete…` for the selection |
| `Ctrl+U` | `Upload files…` |
| `Ctrl+D` | Download the selection |
| `Ctrl+L` | Focus the prefix filter |
| Files dropped on the window | Upload them under the current prefix (`fyne.Window.SetOnDropped`) |

### 10.3 Upload

1. Files come from `dialog.NewFileOpen` (one at a time) or from a drop (many).
   Each becomes a queue item with key `prefix + base name`.
2. For a single file, `Upload options…` lets the owner edit the key, content
   type, encryption (`Bucket default`, `Encrypt`, `Do not encrypt`, sent as
   `X-Butterdb-Encryption` only when not the default) and user metadata pairs.
   Content type defaults to `mime.TypeByExtension`, then
   `http.DetectContentType` on the first 512 bytes.
3. Before sending, the app calls `HeadObject`. If the key exists, it asks
   `Replace`, `Skip`, `Replace all`, `Skip all`. Replacing sends `If-Match:
   <etag seen>`; a new key sends `If-None-Match: *`. A `precondition_failed`
   answer re-asks, because the object changed in between.
4. A file larger than `Status.limits.max_object_bytes` is marked
   `Too large` without sending. The server stays authoritative, because
   encryption overhead can push a file just under the limit over it.
5. Each item gets a random 16-byte hex `Idempotency-Key`, kept for every retry
   of that item.
6. `PutObject` streams the file with `Content-Length` = file size through a
   counting reader. At most 2 uploads run at once. Default (owner may change):
   2.

Item states and display:

| State | Display |
| --- | --- |
| `queued` | `Waiting` |
| `sending` | Progress bar, bytes sent of total, rate |
| `committing` | Indeterminate bar, `Saving to Telegram…` (the body is sent and the server is waiting for the Telegram commit, D8) |
| `done` | `Uploaded`, ETag |
| `failed` | Problem title; `Retry` and `Remove` |
| `cancelled` | `Cancelled` |

`commit_outcome_unknown` is retried automatically with the same
`Idempotency-Key` after `retry_after_seconds` (minimum 5 s), up to 5 times,
showing `Checking whether Telegram saved it…`. `idempotency_conflict` gives the
item a new key and asks the owner before resending. `telegram_flood_wait`
pauses the whole queue until `RetryAt`, with a countdown on the panel.
Cancelling an item in `sending` cancels its context, and nothing is stored. An
item in `committing` cannot be cancelled; the button reads `Finishing…`.

Closing the window while transfers run triggers `SetCloseIntercept`:
`<n> transfers are running. Uploads already saving to Telegram will finish on
the server.` with `Keep open` (default) and `Quit anyway`.

### 10.4 Download

1. One object: `dialog.NewFileSave` with the last key segment as the default
   name, starting in the download folder from preferences (§11.7). Several
   objects: `dialog.NewFolderOpen`; keys are written under the folder using
   their path relative to the current prefix, with `/` mapped to the OS
   separator. A key segment of `.` or `..`, or one containing a path
   separator after mapping, is replaced by `_` so a key cannot escape the
   folder.
2. `GetObject` streams into `.<name>.butterdb-part` in the target directory;
   progress shows bytes of `Content-Length`.
3. On completion the app checks the length and, when the ETag is 32 hex
   characters without a `-`, the MD5. Then it renames the part file over the
   target. An existing target asks `Replace` or `Keep both` (`name (1).ext`).
4. On failure or cancel the part file is deleted.

The server decrypts encrypted objects; the app never sees ciphertext.

### 10.5 Delete

`ConfirmDestructive` with the count and up to 10 keys listed:
`This permanently deletes <n> objects from Telegram. There is no trash and no
undo.` (D10). One object uses `DeleteObject`; several use `DeleteObjects` in
batches of 1000 (`limits.max_delete_batch`). Partial failures list each key
with its problem title and offer `Retry failed`. Selecting a folder row
deletes nothing by itself; the dialog explains that folders disappear when
their objects are deleted.

### 10.6 Presigned URL

Dialog fields:

| Field | Control |
| --- | --- |
| Object | The focused key, read-only |
| Signing key | `widget.Select` of active keys whose scope has `read` on this bucket (from `ListAccessKeys`); empty list shows `Create an access key with Read on this bucket first.` |
| Expires in | `15 minutes`, `1 hour` (default), `24 hours`, `7 days`, `Custom` (1 s to 604800 s) |

`Generate` calls `Presign`; the result shows in a read-only multi-line entry
with `Copy` and the `expires_at` time. The dialog warns: `Anyone with this URL
can download the object until it expires or the key is revoked.`
`s3_public_url_unset` shows `The server has no public S3 URL configured. Set it
in the server configuration (docs/spec/operations.md) and restart.`

### 10.7 Details and preview

The side panel shows `ObjectMeta`: key, size, stored size, content type, last
modified, ETag, SHA-256, encryption, version ID, replicated, user metadata, each
copyable.

Image preview:

| Rule | Value |
| --- | --- |
| Types | `image/jpeg`, `image/png`, `image/gif` (first frame), `image/webp`, `image/bmp` |
| Size ceiling | 20 MiB object size and 40 megapixels from `image.DecodeConfig` |
| Fetch | `GetObject` into memory on a worker goroutine; cancelled when focus moves |
| Render | `canvas.NewImageFromImage`, `FillMode = canvas.ImageFillContain`, `ScaleMode = canvas.ImageScaleSmooth` |
| Other types | `No preview for <content type>` |

Default (owner may change): the ceilings. EXIF orientation is not applied to
the preview. HEIC and AVIF are not previewed; phase 13 transforms can serve a
WebP rendition later.

---

## 11. Settings (`ui/settings`)

Settings is a list of sections on the left of its own content area.

### 11.1 Connection

Server label (editable), URL, server ID, server version, trust mode, pinned
fingerprint in display form, token storage kind with `Move to keychain` when
applicable, `Re-verify certificate` (reruns the probe and compares, changing
nothing on a match), `Switch server`.

### 11.2 This device

Name (`RenameDevice`), created via, token issued at, `Rotate token now`
(§4.5).

### 11.3 Devices

Table from `ListDevices`: name, platform, created via, last seen age, last seen
IP, status (`active`, `revoked at <time>`), with `This device` marked. `Revoke…`
uses `ConfirmDestructive`; on `last_device` the app asks again: `This is the
last active device. After revoking it, only butterdbd admin-token on the host
can add a device.` and resends with `confirm_last=true`.

### 11.4 Telegram account

Account summary from `TelegramStatus`, the safety notice (§6.4) and
`Unlink…`, a `ConfirmTyped` requiring the word `unlink`: `The server stops
syncing and uploading until an account is linked again. Only the same
Telegram account can be relinked.`

### 11.5 Activity

Table from `Audit`: time, actor (device name or `host`, `system`,
`anonymous`), action, target, outcome, error code, address. Filter select by
group (`All`, `setup.`, `pairing.`, `device.`, `telegram.`, `bucket.`,
`access_key.`, `object.`, `replica.`) and `Load more` paging with 100 rows.

### 11.6 About

App version, Go version, Fyne version, server version, links to licences of
Fyne (BSD-3-Clause), go-keyring (MIT) and `golang.org/x/image` (BSD-3-Clause)
as text.

### 11.7 App preferences

Stored with `fyne.App.Preferences()` (not secrets):

| Key | Default | Meaning |
| --- | --- | --- |
| `theme` | `system` | `system`, `light`, `dark` |
| `download_dir` | `os.UserHomeDir()/Downloads` | Start folder for downloads |
| `upload_concurrency` | `2` | 1 to 4 |
| `clipboard_clear_seconds` | `60` | 0 disables clearing |
| `last_route.<server_id>` | `dashboard` | Route restored on reconnect |

Theme override uses a custom `fyne.Theme` that wraps `theme.DefaultTheme()` and
forces the variant; `system` uses the OS setting.

### 11.8 Replica (phase 12, `ui/replica`)

A separate sidebar entry, registered only by a phase 12 build:

| `ReplicaStatus.state` | View |
| --- | --- |
| `not_linked` | Explanation of A9 and D12, then `telegramlink.LoginWizard` over `replicaFlow` with `AllowReuseApp = true` (`Use the same api_id as the primary account`, default on) |
| `sending_code`, `awaiting_code`, `awaiting_password` | The wizard at that step |
| `backfilling` | Account summary; backfill progress bar `objects_done / objects_total` with bytes; pending ops |
| `linked` | Account summary; lag (`pending_ops`, oldest pending age) |
| `degraded` | As linked, with `last_error` as a banner |

`Unlink replica…` is a `ConfirmTyped` requiring `unlink`. A server older than
phase 12 answers `501 not_implemented`; the screen then shows `This server does
not support a replica account yet.`

---

## 12. Error display by stable code

`model.Describe` maps every `adminv1.Code` (admin-api.md §4.2) to a
presentation. `Field` comes from `details.field` when present. `RetryAt` is
`now + retry_after_seconds`, or `details.until` for `telegram_flood_wait`.

| Code | Presentation | Owner-facing title (body adds `hint` when present) |
| --- | --- | --- |
| `bad_request`, `length_required`, `unsupported_media_type`, `range_not_satisfiable` | dialog | `The app and server disagree about this request. Update both to the same version.` |
| `validation_failed` | inline on `Field`, else dialog | `Check this value: <reason>` |
| `unauthenticated` | route `connect` | `This device is no longer signed in.` (token deleted, §4.5) |
| `device_revoked` | route `connect` | `This device was revoked.` |
| `not_found`, `method_not_allowed` | dialog | `The server does not offer this. It may be older than this app.` |
| `misdirected_request` | dialog | `The server rejected this host name. Add it to admin.hostnames on the server.` |
| `payload_too_large` | dialog | `The request is too large.` |
| `rate_limited` | banner, countdown | `Too many attempts. Try again in <n> s.` |
| `internal` | dialog | `The server hit an error. Check its log for request <request_id>.` |
| `server_not_ready` | banner, auto-retry | `The server is <state>. Waiting…` |
| `not_implemented` | screen placeholder | `This server does not support this feature yet.` |
| `already_claimed` | route `connect` pairing form | `This server is already set up. Pair this device instead.` |
| `not_claimed` | route `connect` claim form | `This server is not set up yet. Use its setup token.` |
| `setup_token_invalid` | inline | `That setup token is wrong, used or expired.` |
| `pairing_code_invalid` | inline | `That code is wrong or expired.` |
| `pairing_unavailable` | inline | `Telegram is not linked. Use butterdbd admin-token on the server.` |
| `last_device` | dialog with second confirmation | §11.3 |
| `device_not_found` | banner, list refresh | `That device no longer exists.` |
| `telegram_not_linked` | route `telegram` | `Link a Telegram account first.` |
| `telegram_unavailable` | banner with Retry | `The server cannot reach Telegram right now.` |
| `telegram_flood_wait` | banner, countdown | `Telegram asked the server to wait until <time>.` |
| `telegram_credentials_missing`, `telegram_already_linked`, `telegram_login_in_progress`, `telegram_login_not_active`, `telegram_api_id_invalid`, `telegram_phone_invalid`, `telegram_phone_banned`, `telegram_account_not_registered`, `telegram_code_invalid`, `telegram_code_expired`, `telegram_password_invalid`, `telegram_attempts_exhausted`, `telegram_email_setup_required`, `telegram_account_mismatch`, `telegram_login_failed` | as §6.3 | as §6.3; `telegram_phone_banned` is a dialog: `Telegram has banned this phone number.` |
| `bucket_not_found` | banner, list refresh | `That bucket no longer exists.` |
| `bucket_exists` | inline on name | `A bucket with this name exists.` |
| `bucket_name_invalid` | inline on name | `<reason>` |
| `bucket_not_empty` | dialog | §8.3 |
| `bucket_limit_reached` | dialog | `This server allows at most <limit> buckets.` |
| `access_key_not_found` | banner, list refresh | `That access key no longer exists.` |
| `scope_invalid` | inline on scope | `Choose at least one bucket and one permission.` |
| `object_not_found` | banner, listing refresh | `That object no longer exists.` |
| `object_key_invalid` | inline on key | `Keys are 1 to 1024 bytes of UTF-8.` |
| `object_too_large` | transfer item | `Too large (max <max_bytes>).` |
| `metadata_too_large` | inline on metadata | `Metadata is limited to <max_bytes> bytes.` |
| `checksum_mismatch` | transfer item | `The file changed while uploading. Retry.` |
| `precondition_failed` | re-ask | §10.3 step 3 |
| `idempotency_conflict` | transfer item | §10.3 |
| `commit_outcome_unknown` | transfer item, auto-retry | §10.3 |
| `presign_key_invalid` | inline on signing key | `This key cannot read this bucket.` |
| `s3_public_url_unset` | dialog | §10.6 |
| `replica_not_linked`, `replica_already_linked` | banner, screen refresh | `The replica state changed. Refreshed.` |
| `replica_same_account` | inline in wizard | `Use a different Telegram account from the primary.` |
| unknown code | dialog | `message` and `hint` from the server, with the code |

Non-API errors:

| Error | Presentation | Title |
| --- | --- | --- |
| `*adminv1.PinMismatchError` | blocking view | §4.4 |
| TLS failure in `system` trust mode | dialog | `The server certificate is not trusted by this computer.` |
| Dial error, timeout, connection reset | banner with Retry, backoff 2, 4, 8, 16, 30 s for polls | `Cannot reach <label>.` |
| `context.Canceled` | nothing | (the screen was left) |

Every dialog and banner built from a `Problem` has a `Details` expander
showing the code, HTTP status, `request_id` and `details`, with `Copy details`.
That text never contains a secret, because the API never returns one in an
error.

---

## 13. Navigation, window and threading

### 13.1 Window

| Item | Value |
| --- | --- |
| Main window | One, title `butterDB Admin — <server label>`; minimum size 1024 × 680; size and position not persisted |
| Layout | `container.NewBorder`: banner strip top, sidebar left (`widget.List` of registered screens with icon and title), content centre, status line bottom (server label, connection dot with the word `Connected`/`Offline`, running transfers count) |
| Sidebar order | `Link Telegram` (only while not linked), Dashboard, Buckets, Access keys, Objects, Replica (phase 12 builds only), Settings; the footer holds `Switch server` |
| Dialogs | Fyne in-window dialogs (`dialog.NewCustom`, `dialog.NewForm`, `dialog.NewFileOpen`, `dialog.NewFileSave`, `dialog.NewFolderOpen`) |

### 13.2 Shortcuts

Registered on the window canvas with `desktop.CustomShortcut` and
`fyne.KeyModifierShortcutDefault` (Ctrl on Windows and Linux, Cmd on macOS):

| Shortcut | Action |
| --- | --- |
| `Mod+1` … `Mod+6` | Dashboard, Buckets, Access keys, Objects, Replica, Settings, in sidebar order; a number with no registered screen does nothing |
| `Mod+R` | Refresh the current screen |
| `Mod+N` | New item on Buckets and Keys |
| `Mod+U`, `Mod+D`, `Mod+L` | Objects: upload, download, filter (§10.2) |
| `Mod+W` | Switch server |
| `Mod+Q` | Quit (with the transfer check of §10.3) |
| `Escape` | Close the topmost dismissable dialog |
| `F1` | About |

### 13.3 Threading

Fyne v2.6 requires widget updates from other goroutines to go through
`fyne.Do` (or `fyne.DoAndWait`). The rules:

1. Widget callbacks (`OnTapped`, `OnSubmitted`) start work with
   `go vm.Command(ctx, …)` and return at once.
2. View models never touch widgets. Views subscribe to the view model's
   `Observable` and render inside `fyne.Do`.
3. Polls use a `time.Ticker` on a goroutine tied to the `Show` context.
4. Long transfers report progress at most 10 times per second to the view.
5. No `sync.Mutex` is held across a client call.

---

## 14. Accessibility and keyboard navigation

| Area | Rule |
| --- | --- |
| Keyboard | Every action is reachable without a pointer. `Tab` and `Shift+Tab` move through focusable widgets in reading order; tables and lists take arrow keys when focused; dialogs focus their first input and bind Enter to the safe default and Escape to cancel |
| Destructive defaults | Destructive and trust-granting buttons are never the default button |
| Focus | Focus moves to the first field of a new step or dialog and returns to the invoking control on close; inline errors move focus to the offending field |
| Text | All status is text plus icon, never colour alone. Toolbar and row buttons carry a text label next to the icon (`widget.NewButtonWithIcon`), never an icon alone |
| Scale | The app respects Fyne's scale setting (`FYNE_SCALE` and the Fyne settings app); layouts use `container` layouts and no fixed pixel sizes beyond the window minimum |
| Contrast | Only the stock light and dark themes are used, so contrast follows Fyne's defaults |
| Copy | Every ID, fingerprint, URL and key is selectable and has a copy action |
| Time limits | The app imposes none. Telegram's code expiry and pairing expiry are shown as countdowns with their remaining time in text |
| Screen readers | Fyne v2 does not expose widgets to OS accessibility APIs, so screen readers cannot read the app. This is a known limit of the toolkit and is listed in the operator guide |

---

## 15. Logging and privacy

| Item | Value |
| --- | --- |
| Logger | `log/slog` text handler |
| File | `<UserConfigDir>/butterdb-admin/logs/admin.log`, rotated at 5 MiB, 3 files kept. Default (owner may change) |
| Content | Route changes, API method name, HTTP status, error code, `request_id`, durations, transfer sizes |
| Never logged | Tokens, codes, `api_hash`, phone numbers, passwords, access-key secrets, presigned URLs, object keys of downloads to disk (keys are logged only as their length) |
| Telemetry | None. The app talks only to the configured servers |

---

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

## 18. Phase 10 integration lane

`apps/admin/internal/app` owns:

```go
package app

type Options struct {
	Version string
	Now     func() time.Time
	Dialer  connectvm.Dialer
	Tokens  model.TokenStore
	Store   model.ProfileStore
}

func Run(opts Options) error
func Build(a fyne.App, w fyne.Window, opts Options) (*shell.Shell, error)
```

`main.go` calls `app.Run` with the real dialer, the keychain probe result of
§5.2 and `profilestore` rooted at `os.UserConfigDir()`. `Build` exists so the
acceptance tests drive the same composition with `test.NewTempApp`. The
`fyne.App` is created with `app.NewWithID("io.github.prieyan.butterdb.admin")`.

Verification:

```
CGO_ENABLED=1 go -C apps/admin vet -mod=readonly ./...
CGO_ENABLED=1 go -C apps/admin test -mod=readonly ./...
CGO_ENABLED=1 go -C apps/admin build -mod=readonly -o /dev/null .
```

---

## 19. Defaults chosen here (owner may change)

| Item | Default |
| --- | --- |
| App ID | `io.github.prieyan.butterdb.admin` |
| Keychain library | `github.com/zalando/go-keyring`, service `butterdb-admin`, account `server_id` |
| Keychain fallback | Memory-only; protected file on explicit opt-in |
| Token rotation age | 30 days |
| Wizard polling | 2 s while sending code, 10 s otherwise |
| Dashboard polling | 15 s |
| Upload concurrency | 2 (1 to 4 in preferences) |
| Commit-unknown auto retries | 5, minimum 5 s apart |
| Listing page size | 200 |
| Preview ceiling | 20 MiB, 40 megapixels |
| Clipboard clearing for secrets | 60 s |
| Log rotation | 5 MiB, 3 files |
| Release signing | Unsigned in v0.1.0 |
| Linux release display server | X11/XWayland build |
