# Phase 10b — Local app, Telegram login and projects
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

> **For agentic workers:** this phase follows `docs/phases/00-execution-protocol.md`: one worktree and branch per lane, the lane brief passed verbatim, then a monitor, a reviewer and a cherry-pick per lane. Lane steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Linux admin app runs the engine in its own process. A user logs in with Telegram and picks or creates a project. A project is its own registry channel with its own buckets and server key. The key is escrowed in Telegram under a project password. No address, port, certificate, setup token or pairing code is ever shown.

**Architecture:** A new root-module package `internal/embedded` is a second composition root beside `cmd/butterdbd`. It owns the account (`api_id`/`api_hash`, a sealed session and login), the project list, and one open `core.App` per project in `ModeEmbedded`. The admin app keeps its `adminv1` client and view models, and talks to the open project through an in-process `http.RoundTripper` over the admin handler. Login and project screens call the façade directly.

**Tech stack:** Go 1.27; Fyne v2.6.0 (`apps/admin`, cgo, Linux only); gotd through `internal/tgclient`; modernc SQLite; `golang.org/x/crypto/argon2` and `chacha20poly1305`, both already in `go.mod`.

**Spec:** `docs/superpowers/specs/2026-11-17-local-app-projects-design.md`, approved by the owner on 2026-11-17. Its §7.9 and §7.10 are binding. Where this phase doc and the design disagree, the design wins until the prelude has moved it into `docs/decisions.md` and `docs/spec/*`. From then on those documents win, as usual.

## Global constraints

- Linux only. No Windows or macOS build or test. Code must not use a Linux-only syscall outside the files that already have one.
- The root module builds with `CGO_ENABLED=0` and `GOWORK=off`. `internal/embedded` is pure Go.
- `apps/admin` imports from the root module only `api/...` and `internal/embedded` (new rule L8). Only `cmd/butterdbd` and `internal/embedded` import `internal/core` (new rule L5).
- D2 stands: no built-in `api_id`/`api_hash`.
- The password, the server key in clear, `api_hash`, the phone, login codes and the 2FA password never reach disk (except the key file), logs, the audit log or Telegram.
- New wire op `t=kesc` and `t=reg` key `pkfp` follow wire-format §7. `wire.FormatVersion` becomes `2`. New registries write `fv=2|mr=1`.
- Argon2id defaults: `t=3`, `m=65536` KiB, `p=4`, salt 16 bytes, output 32 bytes. Accepted ranges on parse: `1 ≤ t ≤ 10`, `19456 ≤ m ≤ 1048576`, `1 ≤ p ≤ 16`.
- The minimum project password is 10 characters. The confirmation word for a key reset is exactly `reset project key`.
- Local root is `$XDG_DATA_HOME/butterdb`, else `~/.local/share/butterdb`, with directories `0700` and files `0600`.
- Existing hard rules apply: no comment blocks, docs only in `docs/`, the commit author `prieyan <mnprieyan@gmail.com>` via `git -c`, one-line lowercase messages, never push, and lanes never touch `go.mod`, `go.sum` or `go.work`.

## Review focus

The inputs most likely to bite a user that no single lane's happy path covers. Each has a binding test in the lane named.

1. **Wrong password on unlock.** Expect a clear "wrong password" with no file written and nothing in Telegram changed. Test: `TestUnlockWrongPasswordWritesNothing` (L4).
2. **Interrupted create.** The app is killed after the registry channel exists but before `kesc` or `state.json`. On reopen, the project must be listed as openable, not duplicated and not stranded. Test: `TestCreateResumesAfterCrashBeforeEscrow` (L4), with the façade writing a `create.json` journal first.
3. **Two app instances.** Expect the second to exit with "butterDB is already open" and not corrupt the session. Test: `TestSecondHostRefused` (L4).
4. **Session expired or revoked from another Telegram device.** Expect a return to the phone step, with projects and keys left on disk. Test: `TestRevokedSessionReturnsToLogin` (L4) and `TestSessionLostRoutesToLogin` (L5).
5. **A registry made by an older `butterdbd setup` (no `kesc`), or one with a newer `mr`.** Expect it listed greyed out with the reason, and never opened. Test: `TestDiscoverMarksLegacyAndTooNew` (L2).

---

## 1. Entry criteria

- Branch `phase/10b-local-app` from `phase/10-admin-gui` head (`e5bc33f` or later).
- Phase 10's owner gate (the GUI walkthrough) is folded into this phase's gate (§8). Phase 10 and 10b fast-forward to `main` together.

## 2. Read before starting

`docs/claude.md`, the design doc above, `docs/decisions.md`, `docs/spec/admin-gui.md`, `docs/spec/admin-api.md` §5 and §16, `docs/spec/wire-format.md` §5, §7 and §9, `docs/spec/storage-model.md` §4, `docs/spec/security.md` §5, `docs/phases/10-admin-gui.md`. Also the TDrive references `internal/app/app.go` (engine in the GUI) and `frontend/src/modules/auth.ts` (login order).

## 3. Prelude (orchestrator)

Three commits on `phase/10b-local-app` before fan-out. No lane starts before all three are committed.

### 3.1 Decisions and specs

Commit message: `record local app and project decisions`.

- [ ] **`docs/decisions.md`:**
  - edit D1, D13, D14 and A6, and add D-entry "Projects", all per design §7.1
  - add to the accepted-risk table "same project open on two machines" (design §7.9)
  - add an E-entry: "`internal/embedded` is the second composition root"
  - add an E-entry: "account key seals the session; `K_session` info `butterdb/account/session-seal/v1`"
- [ ] **`docs/claude.md` §5:** amend L5 and L8 per the global constraints.
- [ ] **`docs/claude.md` §5.1 layout:** add the line `internal/embedded/  local app composition root: account, projects, in-process admin transport`.
- [ ] **`docs/spec/wire-format.md`:**
  - **§5.4 `t=reg`:** add `pkfp`, optional: a fingerprint pattern or the literal `lost`. It is allowed only with `w=sys.rekey`.
  - **New §5.9 `t=kesc`:**

    | Key | Req | Rule |
    | --- | --- | --- |
    | `j` | Req | Job ID |
    | `kfp` | Req | Fingerprint of the escrowed key |
    | `kdf` | Req | Enum `a2id` |
    | `salt` | Req | `setB64Len` 16 |
    | `kt` | Req | `setBoundedInt` 1–10 |
    | `km` | Req | `setBoundedInt` 19456–1048576 |
    | `kp` | Req | `setBoundedInt` 1–16 |
    | `wrap` | Req | `setB64Len` 72: nonce(24) ‖ ciphertext(32) ‖ tag(16) |
    | `w` | Opt | Writer `sys.escrow` |
    | `ts` | Req | Format time |

  - **§6 limits:** add `kesc` to the text-message carrier and give its worst case. The implementer computes it and asserts it in `TestKescFitsCarrier`.
  - **§7.1:** butterDB 0.2 writes `fv=2`, `mr=1`.
  - **§10:** add one worked `kesc` example and one `reg` with `pkfp`.
  - **§12:** add test rows `TestKescRoundTrip`, `TestKescRejectsBadParams`, `TestRegPkfpRules`.
- [ ] **`docs/spec/security.md`:**
  - **§5.3:** add `K_escrow = Argon2id(password, salt, kt, km, kp, 32)`, and `wrap = nonce ‖ XChaCha20-Poly1305(K_escrow).Seal(nonce, serverKey, aad)` with `aad = "butterdb/escrow/v1|" + dep + "|" + kfp`.
  - **§5.4:** sessions are sealed under `K_session = HKDF-SHA256(account key, info "butterdb/account/session-seal/v1")`.
  - **New §5.8 "Project password":**
    - change of password: needs the old password; publish the new `kesc`, verify it, then delete older `kesc` messages
    - the limit on old copies that someone may have kept
    - the in-app lost-key reset reuses §5.7 with `pkfp=lost`
  - **§2:** rewrite T4 and T5 for the desktop model.
- [ ] **`docs/spec/storage-model.md` §4:**
  - registry create takes a project name; the title is `butterDB · <name>`, with the name 1–64 characters and no control characters
  - non-interactive adopt by channel ID
  - discovery classifies only `user` as foreign
  - `kesc` is projected as a no-op (`replay_log` row, no reject)
- [ ] **`docs/spec/admin-api.md`:** new §17 "Embedded mode":
  - no admin TCP listener, TLS, setup token, control socket, device sweep or S3 listener
  - every request runs as principal `local-owner`
  - the device and pairing endpoints return `404 not_found`
- [ ] **`docs/spec/admin-gui.md`:**
  - replace §4 with "Account and projects" (design §7.7)
  - Linux only
  - remove the claim, pairing and device sections and their §16.2 test rows; add the rows named in lane L5 here
- [ ] **`docs/plan.md`:** insert phase 10b after phase 10. Phase 11's rotate-key and lost-key tooling must reuse the `pkfp` grammar and the reset core from 10b.

### 3.2 Wire op and shared pure types

Commit message: `add key escrow op and format version two`.

- [ ] Add `OpKeyEscrow OpType = "kesc"` to `internal/wire/ops_catalog.go`. Add the op fields to `wire.Op`: `KDF string`, `Salt []byte`, `KDFTime`, `KDFMemory`, `KDFParallel int`, and reuse `Wrap []byte` and `KeyFingerprint`. Add `PrevKeyFingerprint string` for `pkfp`.
- [ ] Put Parse and Format for `kesc` in a new `internal/wire/ops_kesc.go`. Handle `pkfp` in `ops_registry.go`. Set `FormatVersion = 2` in `types.go`.
- [ ] Add tests `TestKescRoundTrip`, `TestKescRejectsBadParams`, `TestRegPkfpRules` and `TestKescFitsCarrier`. Extend `TestRoundTripEveryOp` and `TestFormatCanonicalOrder` with the §10 examples.
- [ ] Update every root-module test that asserts `fv=1` for a newly written marker.
- [ ] Run `GOWORK=off CGO_ENABLED=0 go test -mod=readonly ./...`. It must pass.

### 3.3 Façade contract

Commit message: `define embedded host contract`.

- [ ] Create `internal/embedded/contract.go`. Types and interfaces only: no logic, and no imports beyond the standard library, `api/adminv1` and `internal/domain/errs`.

```go
type ProjectID string

type ProjectState string

const (
	ProjectReady       ProjectState = "ready"
	ProjectLocked      ProjectState = "locked"
	ProjectLegacy      ProjectState = "legacy"
	ProjectTooNew      ProjectState = "too_new"
	ProjectUnfinished  ProjectState = "unfinished"
)

type Project struct {
	ID          ProjectID
	Name        string
	ChannelID   int64
	State       ProjectState
	CreatedAt   time.Time
	LiveBuckets int
	LastOpened  time.Time
}

type ResetReport struct {
	UnreadableObjects map[string]int64
	RevokedAccessKeys int
}

type Account interface {
	Status(ctx context.Context) (adminv1.TelegramStatus, error)
	SaveApp(ctx context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error)
	DeleteApp(ctx context.Context) (adminv1.TelegramStatus, error)
	Start(ctx context.Context, phone string) (adminv1.TelegramStatus, error)
	SubmitCode(ctx context.Context, loginID, code string) (adminv1.TelegramStatus, error)
	SubmitPassword(ctx context.Context, loginID, password string) (adminv1.TelegramStatus, error)
	Cancel(ctx context.Context) (adminv1.TelegramStatus, error)
	Logout(ctx context.Context) error
	RemoveDevice(ctx context.Context) error
}

type Projects interface {
	List(ctx context.Context) ([]Project, error)
	Create(ctx context.Context, name string, password []byte) (Project, error)
	Unlock(ctx context.Context, id ProjectID, password []byte) error
	Open(ctx context.Context, id ProjectID) (Opened, error)
	Close(ctx context.Context) error
	Rename(ctx context.Context, id ProjectID, name string) error
	ChangePassword(ctx context.Context, id ProjectID, oldPassword, newPassword []byte) error
	ResetKey(ctx context.Context, id ProjectID, newPassword []byte, confirmation string) (ResetReport, error)
}

type Opened struct {
	Project   Project
	BaseURL   string
	Transport http.RoundTripper
}

var (
	ErrAlreadyRunning   = errors.New("embedded: butterDB is already open")
	ErrWrongPassword    = errors.New("embedded: wrong project password")
	ErrPasswordTooShort = errors.New("embedded: project password too short")
	ErrProjectNotFound  = errors.New("embedded: project not found")
	ErrProjectNotReady  = errors.New("embedded: project cannot be opened")
	ErrConfirmation     = errors.New("embedded: confirmation text does not match")
	ErrNotLoggedIn      = errors.New("embedded: not logged in to telegram")
)
```

- `Account` method sets must equal `apps/admin/internal/ui/telegramlink/vm.LoginFlow`, plus `Logout` and `RemoveDevice`.
- `BaseURL` is the constant `http://127.0.0.1` and is never dialled.
- Password parameters are `[]byte` so callers can zero them.

**Prelude review.** Run the prelude monitor and reviewer per protocol §6. The reviewer checks that the spec text equals the design and that the `kesc` byte layouts match between security.md and wire-format.md.

## 4. Lanes and waves

| Wave | Lane | Branch | Owned paths |
| --- | --- | --- | --- |
| 1 | L1 | `phase/10b-local-app-l1` | `internal/crypto/keyring/escrow*.go`, `internal/crypto/keyring/accountkey*.go` |
| 1 | L2 | `phase/10b-local-app-l2` | `internal/buckets/registry*.go`, `internal/syncer/reconcile/registry*.go`, `internal/syncer/reconcile/escrow*.go`, `internal/projection/apply/kesc*.go` and its registration line in the Registry, `internal/adminapi/session/local*.go`, `internal/tgclient/rpc/channels*.go` and `internal/tgclient/faketg/channel*.go` (edit title only) |
| 1 | L5 | `phase/10b-local-app-l5` | `apps/admin/internal/ui/projects/`, `apps/admin/internal/ui/settings/`, `apps/admin/internal/ui/objects/` (presign hide only), `apps/admin/internal/model/` (routes and describe), removal of `apps/admin/internal/ui/connect/` |
| 2 | L3 | `phase/10b-local-app-l3` | `internal/core/` |
| 3 | L4 | `phase/10b-local-app-l4` | `internal/embedded/` except `contract.go` |
| — | INT | `phase/10b-local-app-int` | `apps/admin/main.go`, `apps/admin/internal/app/`, `scripts/layering.sh`, `test/acceptance/phase10b/`, `docs/phases/status/10b-local-app.md` |

- Wave 2 starts from the wave 1 heads after they are cherry-picked. Wave 3 starts from the wave 2 head.
- L5 builds only against `contract.go` and fakes.
- Every lane follows the module rules of its tree. `apps/admin` lanes copy the "Module rules for every phase 10 lane" paragraph of `docs/phases/10-admin-gui.md` §5.1, with the import rule replaced by the new L8.

### 4.1 Lane L1 — escrow and account key crypto (`internal/crypto/keyring`)

**Objective:**
- seal and open a 32-byte server key under a password, per security.md §5.3 as amended
- generate and load an account key, and derive its session-seal key

**Interfaces produced:**

```go
type EscrowParams struct{ Time, MemoryKiB, Parallel uint32 }
var DefaultEscrowParams = EscrowParams{Time: 3, MemoryKiB: 65536, Parallel: 4}
type Escrow struct {
	Params      EscrowParams
	Salt        [16]byte
	Wrap        [72]byte
	Fingerprint string
}
func SealEscrow(serverKey *Key, password []byte, deploymentID string, params EscrowParams, rand io.Reader) (Escrow, error)
func OpenEscrow(e Escrow, password []byte, deploymentID string) (*Key, error)
var ErrEscrowPassword = errors.New("keyring: escrow password does not open the key")
func FingerprintOf(k *Key) string

type AccountKey struct{ ... }
func GenerateAccountKeyFile(path string, w SecretFileWriter, rand io.Reader) error
func LoadAccountKey(path string) (*AccountKey, error)
func (a *AccountKey) LeaseSessionKey() ([]byte, error)
func (a *AccountKey) Clear()
```

- `FingerprintOf` returns the same `bkf1_` string as `Ring.FingerprintString`.
- `AccountKey` satisfies `identity.SessionKeys`. Its HKDF info is `butterdb/account/session-seal/v1`.

**Tests (binding):**
- [ ] `TestEscrowRoundTrip`: seal and then open returns the same bytes, and the fingerprint matches.
- [ ] `TestEscrowWrongPassword`: returns `ErrEscrowPassword` and no key.
- [ ] `TestEscrowAADBindsDeployment`: opening with another deployment ID fails.
- [ ] `TestEscrowParamsOutOfRangeRefused`: the global ranges, enforced in `OpenEscrow` before any KDF work.
- [ ] `TestEscrowVector`: a fixed salt, nonce reader and password give a fixed `Wrap` hex, committed in `testdata/escrow_vector.json`.
- [ ] `TestAccountKeySessionKeyDiffersFromServerSessionKey`
- [ ] `TestAccountKeyFileMode0600`

**Verify:** run `GOWORK=off CGO_ENABLED=0 go test -mod=readonly ./internal/crypto/keyring/...` and `gofmt -l internal/crypto/keyring`.

**Commits:** `add password key escrow`, `add account key for session sealing`.

### 4.2 Lane L2 — registries as projects (`buckets`, `reconcile`, `apply`, `session`)

**Objective:**
- name registry channels by project
- list every own registry whatever its key
- read the newest valid escrow from a registry
- project `kesc` as a no-op
- provide the local-owner authenticator

**Interfaces produced:**

```go
// internal/buckets
func ProjectTitle(name string) (string, error)
func ProjectNameFromTitle(title string) (string, bool)
// new method beside the existing Create, which stays unchanged so core keeps building in wave 1:
// func (p *RegistryProvisioner) CreateNamed(ctx context.Context, name string) (syncer.ChannelState, bool, error)
// the old fixed title "butterDB registry" reads back as name "registry"

// internal/tgclient/rpc and faketg
// func (c *Client) EditChannelTitle(ctx context.Context, peer tgclient.InputPeer, title string) error

// internal/syncer/reconcile
// NewDiscoverer accepts Identity.KeyFingerprint == "" meaning any key; classify then reports only ForeignAccount.
type Candidate struct{ ...existing...; Name string; HasEscrow bool }
func (d *Discoverer) Discover(ctx context.Context) ([]Candidate, error)
type EscrowReader struct{ ... }
func NewEscrowReader(tg RegistryReader) (*EscrowReader, error)
func (r *EscrowReader) Latest(ctx context.Context, peer tgclient.InputPeer, deploymentID, fingerprint string) (wire.Op, int64, error)
func (r *EscrowReader) All(ctx context.Context, peer tgclient.InputPeer) ([]int64, error)
var ErrNoEscrow = errors.New("reconcile: registry has no key escrow")

// internal/projection/apply
// a kescApplier registered for wire.OpKeyEscrow that records nothing beyond replay_log

// internal/adminapi/session
func LocalOwner() func(http.Handler) http.Handler
```

- `Latest` returns the newest `kesc` whose `kfp` equals `fingerprint` (the marker's), plus its message ID.
- `All` lists every `kesc` message ID in the registry.
- `LocalOwner` sets the same request principal the bearer middleware sets for a device, with device ID `local-owner`.

**Tests (binding):**
- [ ] `TestProjectTitleRoundTrip`: covers 1 and 64 characters, refuses 65 and control characters, and handles a `·` inside the name.
- [ ] `TestDiscoverAnyKeyListsAllOwnRegistries`: two registries with different `kfp`, both not foreign. A third with another `uid` is `ForeignAccount`.
- [ ] `TestDiscoverMarksLegacyAndTooNew`: no `kesc` gives `HasEscrow=false`, and `mr=3` keeps `MinReaderVersion` 3.
- [ ] `TestEscrowLatestPicksNewestMatchingFingerprint`: an older `kesc`, a newer one, and one with a mismatched `kfp` that is ignored.
- [ ] `TestKescProjectsWithoutReject`
- [ ] `TestLocalOwnerSetsPrincipal`
- [ ] `TestEditChannelTitle`: faketg records the new title, and `ProjectNameFromTitle` reads it back.

Use `internal/tgclient/faketg` for every Telegram-facing test.

**Verify:** run `GOWORK=off CGO_ENABLED=0 go test -mod=readonly ./internal/buckets/... ./internal/syncer/reconcile/... ./internal/projection/... ./internal/adminapi/session/...`.

**Commits:** `add channel title edit`, `name registry channels by project`, `discover registries of any key`, `read key escrow from a registry`, `project key escrow as a no-op`, `add local owner authenticator`.

### 4.3 Lane L5 — app screens for account and projects (`apps/admin`)

**Objective:**
- **Replace the connect flow:**
  - add a project picker, a new-project form and an unlock form
  - add a project name and a switch control in the shell header
  - add Settings actions: Log out, Remove this account from this device, Change project password, Reset project key, Rename project
- **Hide** the access-keys route, "Copy presigned URL" and the device list.
- **Delete** `ui/connect/` and its tests, and remove `RouteConnect`.

**Interfaces consumed:** `embedded.Projects`, `embedded.Account`, `embedded.Project`, the `embedded.Err*` values and `embedded.ResetReport`, all from prelude §3.3. Telegram login keeps `telegramlink`, whose `LoginFlow` is satisfied by `embedded.Account`.

**Interfaces produced:**
- `projects/vm.New(deps Deps) *ViewModel` with the §3.4 shape, where `Deps{Projects embedded.Projects; Now func() time.Time}`.
- `projects.NewView(vm *vm.ViewModel, nav shell.Navigator) fyne.CanvasObject`.
- Routes `RouteProjects Route = "projects"` and `RouteUnlock Route = "unlock"`.
- `model.Describe` maps:
  - `ErrWrongPassword` to inline "That password does not open this project."
  - `ErrProjectNotReady` to inline "This project was created outside the app and cannot be opened here."
  - `ErrNotLoggedIn` to route `RouteTelegram`
  - `adminv1.CodeUnauthenticated` to route `RouteTelegram`, never connect

**Copy (binding):**
- **New project:**
  - fields "Project name", "Password", "Confirm password"
  - warning "If you forget this password and lose every device that has this project open, encrypted files cannot be recovered."
  - button "Create project"
- **Unlock:**
  - title "Unlock <name>"
  - note "This project was set up on another device. If it is open there too, both devices can change the same file at the same moment and one change can be lost."
- **Reset:** the field label is "Type reset project key to confirm".
- **Empty picker:** "No projects yet. Create one to start storing files in your Telegram."

**Tests (binding):**
- [ ] `TestPickerListsStatesAndSortsLastOpenedFirst`
- [ ] `TestCreateRequiresMatchingPasswordsOfTenOrMore`
- [ ] `TestCreateClearsPasswordFields`
- [ ] `TestUnlockWrongPasswordKeepsForm`
- [ ] `TestLegacyAndTooNewNotOpenable`
- [ ] `TestResetRequiresExactConfirmation`
- [ ] `TestSwitchProjectClosesThenOpens`
- [ ] `TestSessionLostRoutesToLogin`
- [ ] `TestKeysRouteAndPresignHidden`
- [ ] `TestNoConnectRoute`: `model` has no `RouteConnect`, and no package imports `ui/connect`.

Use the Fyne test driver and a fake `embedded.Projects`.

**Verify:**

```bash
cd apps/admin
CGO_ENABLED=1 go vet -mod=readonly ./internal/...
CGO_ENABLED=1 go test -mod=readonly -race ./internal/ui/... ./internal/model/...
```

**Commits:** `add project picker and unlock screens`, `add project actions to settings`, `hide access keys and presign until the sdk`, `remove the connect flow`.

### 4.4 Lane L3 — embedded mode in core (`internal/core`)

**Objective:**
- `core.App` runs without listeners, with `local-owner` auth
- creates a named registry with escrow, or adopts a given channel
- changes the project password and resets a lost key

**Interfaces consumed:** L1 `SealEscrow`, `OpenEscrow` and `FingerprintOf`. L2 `ProjectTitle`, `Discoverer` (any key), `EscrowReader`, `LocalOwner` and the `kesc` applier.

**Interfaces produced:**

```go
type Mode string
const (
	ModeServer   Mode = "server"
	ModeEmbedded Mode = "embedded"
)
type Dependencies struct{ ...existing...; Mode Mode; Registry RegistryOptions; Sessions identity.SessionKeys }
type RegistryOptions struct {
	RegistryChannelID int64
	Create            bool
	ProjectName       string
	Password          []byte
}
func (a *App) AdminHandler() (http.Handler, error)
func (a *App) ChangeProjectPassword(ctx context.Context, oldPassword, newPassword []byte) error
func ResetLostKey(ctx context.Context, deps Dependencies, newPassword []byte) (ResetOutcome, error)
func (a *App) RenameProject(ctx context.Context, name string) error
type ResetOutcome struct {
	UnreadableObjects map[string]int64
	RevokedAccessKeys int
}
```

- **`Mode`:** empty means server, so `cmd/butterdbd` is unchanged.
- **`Dependencies.Sessions`:** when it is non-nil, it replaces the ring-derived session key, and the façade passes the account key here.
- **`StartServing` in `ModeEmbedded`:**
  - does not add the admin listener, the TLS certificate, the setup-token mint, the control socket, the device sweep or the S3 server
  - wraps the router with `session.LocalOwner()` instead of the bearer authenticator
  - device and pairing routes answer `404 not_found`
- **`AdminHandler`:** returns the handler once `StartServing` has returned. Calling it in `ModeServer` is an error.
- **`ensureRegistry` with `Create`:**
  1. Create the channel with `ProjectTitle(name)`.
  2. Write the marker (`fv=2`).
  3. Seal the escrow with `DefaultEscrowParams` and send `kesc`.
  4. Write `state.json`.
  - It never runs discovery and never prompts.
- **`ensureRegistry` with `RegistryChannelID`:** adopts that channel without prompting.
- **`ChangeProjectPassword`:**
  1. Find the latest escrow.
  2. `OpenEscrow` with the old password. On failure, return `keyring.ErrEscrowPassword` wrapped. Core never imports `internal/embedded`; the façade maps the error.
  3. Seal and send the new `kesc`.
  4. Re-read it and open it with the new password.
  5. Only then delete every older `kesc` message ID.
- **`ResetLostKey`:** a package function, not a method. It takes the process lock on the project directory, connects to Telegram and opens the projection, but skips the key and fingerprint checks of `VerifyLocalState`, because the key is the thing being replaced. It is a no-listener start. It follows security.md §5.7 with `pkfp=lost`:
  1. Generate a new key.
  2. Send `reg` with `w=sys.rekey` and `pkfp=lost`.
  3. Give each encrypted bucket a new `kid`.
  4. Delete the access-key rows.
  5. Send `kesc` under the new password.
  6. Rewrite `server.key` and `state.json`.
  7. Count the objects per bucket whose `kid` is now unopenable.
  - Write `rekey.json` before step 2 so that a crash resumes, per §5.6.

**Tests (binding):**
- [ ] `TestEmbeddedStartsNoListeners`: no TCP port opens and no `admin.sock` exists.
- [ ] `TestEmbeddedHandlerAuthenticatesLocalOwner`
- [ ] `TestEmbeddedDeviceRoutesNotFound`
- [ ] `TestCreateNamedRegistryWritesEscrowAndFv2`
- [ ] `TestAdoptByChannelIDNoPrompt`
- [ ] `TestChangePasswordDeletesOlderEscrowOnlyAfterVerify`
- [ ] `TestChangePasswordWrongOldPassword`
- [ ] `TestResetLostKeyCountsUnreadableAndKeepsPlain`
- [ ] `TestResetLostKeyResumesFromRekeyJSON`
- [ ] `TestRenameProjectEditsRegistryTitle`
- [ ] `TestServerModeUnchanged`: the existing `serve` tests pass untouched.

Use `faketg` for Telegram.

**Verify:** run `GOWORK=off CGO_ENABLED=0 go test -mod=readonly ./internal/core/... ./cmd/...` and `GOWORK=off CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd`.

**Commits:** `add embedded mode without listeners`, `create named registries with key escrow`, `adopt a registry by channel id`, `change project password`, `reset a lost project key`, `rename a project`.

### 4.5 Lane L4 — the embedding façade (`internal/embedded`)

**Objective:** implement `Account` and `Projects` from `contract.go` as one `*Host`.

**Interfaces produced:**

```go
type Options struct {
	DataHome  string
	LogOutput io.Writer
	Clock     clock.Clock
	Version   string
	Telegram  TelegramFactory
}
type TelegramFactory interface { ... }
func Open(opts Options) (*Host, error)
func (h *Host) Close(ctx context.Context) error
func DefaultDataHome() (string, error)
```

- `DataHome == ""` means `DefaultDataHome()`.
- `TelegramFactory` is a narrow port so tests inject `faketg`. Production wires `internal/tgclient` through `core`.
- `*Host` implements `Account` and `Projects`.
- `DefaultDataHome` returns `$XDG_DATA_HOME/butterdb` or `~/.local/share/butterdb`.

**Rules:**
- **Layout:** design §7.4, extended:
  - `account/account.json`: an `identity.AccountStore` for role `primary`, written with `secretfile`
  - `projects/<id>/project.json`: `{name, channel_id, last_opened}`
  - `projects/<id>/create.json`: a journal written before the channel is created and removed after `state.json`
- **Lock:** `Open` takes `processlock` on `<root>/app.lock`. If it is held, return `ErrAlreadyRunning`.
- **Telegram connection:**
  - One connection at a time. The account connection serves login, `List`, `Unlock` and `Create`'s pre-checks.
  - `Open` and `Create` close it, then start `core.App` (`ModeEmbedded`, `Sessions` = the account key) on the project directory.
  - Project `Close` stops the app and reopens the account connection.
- **Seeding `control.db`:** before the first `core.App` start in a new or unlocked project directory, write the primary account row from `account.json` into the project's `control.db`, so the account binding is set.
- **Per-project engine config:**
  - `data_dir` = the project directory
  - `server_key_file` = `<dir>/server.key`
  - `telegram.app_file` and `session_file` = the shared `account/` files
  - cache off
- **`List`** maps each candidate to a state:

  | State | When |
  | --- | --- |
  | `legacy` | no escrow |
  | `too_new` | `mr` > `wire.FormatVersion` |
  | `ready` | the key is on disk and its fingerprint matches |
  | `locked` | escrow exists but there is no key on disk |
  | `unfinished` | a `create.json` exists with no channel found |

  It sorts by `LastOpened` descending, then name.
- **`Unlock`:**
  1. `EscrowReader.Latest`.
  2. `OpenEscrow`. On failure, return `ErrWrongPassword` with nothing written.
  3. Write `server.key`.
  4. Write `project.json`.
- **`Create`:**
  1. Refuse a password under 10 characters with `ErrPasswordTooShort`.
  2. Write `create.json`.
  3. Run core create.
  - On restart, a `create.json` whose channel exists resumes at escrow. One whose channel does not exist restarts creation.
- **Passwords:** every `[]byte` password is zeroed after use.
- **`Rename` and `ChangePassword`:** act on the open project only, through the core methods. Another `id` returns `ErrProjectNotReady`. `keyring.ErrEscrowPassword` maps to `ErrWrongPassword`.
- **`ResetKey`:** works on any `ready` or `locked` project. It first closes the project if it is open, checks the confirmation (`ErrConfirmation`) and the password length, seeds `control.db` as for an unlock, then calls `core.ResetLostKey`.
- **`Logout`:** deletes the session file only.
- **`RemoveDevice`:** closes everything and deletes `<root>` entirely.
- **Session revoked:** a revoked or expired session (`AUTH_KEY_UNREGISTERED`, `SESSION_REVOKED`) closes any open project and returns `ErrNotLoggedIn` from every method until login completes.

**Tests (binding, all with `faketg`):**
- [ ] `TestFirstRunAsksForAppCredentials`
- [ ] `TestSameMachineLoginSkipsAppCredentials`
- [ ] `TestCreateThenListShowsReady`
- [ ] `TestSecondMachineUnlockThenOpen`: two `DataHome` directories, one fake account.
- [ ] `TestUnlockWrongPasswordWritesNothing`
- [ ] `TestCreateResumesAfterCrashBeforeEscrow`
- [ ] `TestSecondHostRefused`
- [ ] `TestRevokedSessionReturnsToLogin`
- [ ] `TestOpenedTransportServesAdminAPI`: `adminv1.NewClient` with `Opened.Transport` lists buckets and creates and deletes an object.
- [ ] `TestSwitchProjectsKeepsDataSeparate`: an object put in A is absent from B.
- [ ] `TestLogoutKeepsProjectsRemoveDeviceDeletesRoot`
- [ ] `TestPasswordsZeroedAfterUse`

**Verify:** run `GOWORK=off CGO_ENABLED=0 go test -mod=readonly ./internal/embedded/...` and `GOWORK=off CGO_ENABLED=0 go vet ./internal/embedded/...`.

**Commits:** `add embedded host account and lock`, `list create and unlock projects`, `open projects through an in-process transport`, `add logout and device removal`.

## 5. Integration lane

Owned paths: `apps/admin/main.go`, `apps/admin/internal/app/`, `scripts/layering.sh`, `test/acceptance/phase10b/` and the status note.

- [ ] **`scripts/layering.sh`:** allow `apps/admin` to import `github.com/PRIEYAN/ButterDB/internal/embedded` and nothing else under `internal/`. Add an L5 check: only `cmd/butterdbd` and `internal/embedded` import `internal/core`. Run `bash scripts/layering.sh` and expect exit 0.
- [ ] **`apps/admin/main.go`:**
  - call `embedded.Open(embedded.Options{DataHome: "", LogOutput: <rotating file under DataHome>/app.log, Clock: clock.System{}, Version: version})`
  - on `ErrAlreadyRunning`, show a one-line Fyne dialog and exit 1
  - drop `profilestore` and `credstore`
- [ ] **`apps/admin/internal/app` composition:**
  - start route `RouteTelegram` when `Status` reports not linked, otherwise `RouteProjects`
  - after `Open`, build the `adminv1` client with `ClientConfig{BaseURL: opened.BaseURL, HTTPTransport: opened.Transport}` and no token, then reuse `guardedClient` for every screen
  - an `ErrNotLoggedIn` or `unauthenticated` from any screen routes to `RouteTelegram`
  - `Mod+Q` and window close call `Projects.Close` and then `Host.Close`, keeping the phase 10 transfer check
- [ ] **Acceptance tests in `test/acceptance/phase10b/`:**
  - root module, `faketg`, no GUI: `TestLocalHostEndToEnd` (first run → login → create → put → close → reopen → get), `TestNewMachineUnlocksExistingProject`, `TestEmbeddedOpensNoNetworkListener`
  - GUI side in `apps/admin/internal/app`, with the Fyne test driver and a fake host: `TestLoginThenPickerThenDashboard`, `TestNoAddressFieldAnywhere`. The second walks every route and asserts that no entry has placeholder "host or host:port".
- [ ] **Tidy:** run `go -C apps/admin mod tidy` once. Commit messages: `wire the admin app to the embedded host`, `allow the admin app to import the embedded host`, `add phase 10b acceptance tests`.

## 6. Verification

On the phase branch head, with no walkthrough server running:

| ID | Command | Pass when |
| --- | --- | --- |
| V1 | `GOWORK=off CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` | exit 0 |
| V2 | `GOWORK=off CGO_ENABLED=0 go test -mod=readonly -count=1 ./...` | all pass |
| V3 | `GOWORK=off CGO_ENABLED=1 go test -mod=readonly -race -count=1 ./internal/...` | all pass |
| V4 | `CGO_ENABLED=1 go -C apps/admin test -mod=readonly -race -count=1 ./...` | all pass |
| V5 | `CGO_ENABLED=1 go -C apps/admin build -mod=readonly -o ../../bin/butterdb-admin .` | exit 0 |
| V6 | `bash scripts/layering.sh` | exit 0 |
| V7 | `bash scripts/nocomments.sh` | exit 0 |
| V8 | `GOWORK=off go test -mod=readonly ./test/acceptance/phase10b/...` | all pass |
| V9 | `bash scripts/verify.sh` | exit 0 |
| V10 | `grep -rn "host or host:port\|setup token\|pairing code" apps/admin --include=*.go` | no match outside tests asserting absence |
| V11 | `git status --porcelain` | empty |

## 7. Status note

Write `docs/phases/status/10b-local-app.md` with the protocol template. Record:

- the phase 10 gate folded into this phase
- every self-made decision from the design's §7.10
- any deviation by a lane

## 8. Owner gate

The owner runs `bin/butterdb-admin` on Linux with a real Telegram account and confirms:

1. a first run asks for `api_id`/`api_hash`, then phone and code
2. creating Project-A and Project-B makes two `butterDB · …` channels in Telegram
3. uploading and viewing a file works
4. switching projects shows separate buckets
5. quitting and restarting asks for nothing, or only for phone and code if the session was dropped
6. a second `DataHome` (`XDG_DATA_HOME=/tmp/x bin/butterdb-admin`) unlocks Project-A with its password and sees the file

Then fast-forward `main` to the phase head, covering phase 10 and 10b.

## 9. Risks

- **One connection at a time.** The account and project connections must never both write the session file. L4's single-connection rule covers this.
- **Rebuilding the index on a second machine** can take a while for a large project. L5 shows the engine's existing `syncing` status rather than blocking.
- **Escrow lookup.** Deleting old `kesc` messages is best effort, as in rotation step 8. `Latest` must never pick a `kesc` whose `kfp` differs from the marker's.
- **FormatVersion 2.** Raising it to 2 changes `TestRoundTripEveryOp` fixtures and every test that asserts `fv=1` in a freshly created marker. Prelude §3.2 updates every such test across the root module in the same commit, so the branch stays green before fan-out. Run `GOWORK=off CGO_ENABLED=0 go test -mod=readonly ./...` before committing.
