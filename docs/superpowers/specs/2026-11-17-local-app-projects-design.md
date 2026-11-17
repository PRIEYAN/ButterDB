# Local admin app with Telegram login and projects

Status: proposal, awaiting owner approval (2026-11-17). Once approved, the
decision changes below are recorded in `docs/decisions.md` before any code
changes, because that document outranks this one.

## 1. Intent

A first-time user should be able to install butterDB and start storing images,
videos and files in their own Telegram without hosting anything, typing a
server address, or reading about ports, certificates, setup tokens or pairing
codes.

The owner's words: the user logs in the way TDrive does. They enter
`api_id`/`api_hash` the first time, then their phone number and Telegram's
code. After that they choose a project, and the dashboard shows what that
project stores. A user with two projects, Project-A and Project-B, has separate
Telegram channels for each.

Today the admin GUI is a remote client of a headless `butterdbd` (D13, D14).
That split is the only reason the GUI asks for an address and pairs devices.

## 2. Goals

1. The Linux desktop app runs the engine inside its own process, as TDrive's
   GUI does (`TDrive/internal/app/app.go` holds a `*core.Engine`). It opens no
   network listener.
2. Login:
   - First run on a machine: `api_id`/`api_hash`, then phone, code and 2FA
     password if the account has one.
   - Same machine later: the pair is saved, so login is phone, code and 2FA
     only. A still-valid saved session skips login entirely.
   - New machine, existing butterDB user: the pair again, then phone and code.
     Their projects appear after login.
3. **Projects.** A project is a separate workspace: its own registry channel,
   its own buckets (one channel each), its own server key and its own local
   index. One Telegram account can hold any number of projects.
4. After login the app lists the account's projects and offers **New
   project**. Opening a project shows the existing dashboard, buckets and
   object browser for that project. The user can switch projects without
   logging out.
5. **Project password (key escrow).**
   - Creating a project asks for a project password.
   - The project's server key is stored in its registry channel, encrypted
     with a key derived from that password.
   - Opening a project on a machine that does not have its key asks for the
     password once. After that the key is kept locally and no password is
     needed on that machine.
   - Telegram never sees the key in the clear.
6. Object bytes never live on the user's disk beyond what D21 already allows:
   the index, the spool during an upload, and the optional cache, off by
   default.

## 3. Non-goals

- **Windows and macOS.** Linux only for now. Windows is planned later. Nothing
  in this design may make Windows impossible, but nothing is built or tested
  for it.
- **The SDK, S3 API and media API for other applications.** In embedded mode
  the S3 listener does not start. The owner will specify the SDK later.
- **A headless mode or daemon.** The future SDK work will add one, like
  TDrive's `tdrived`. This design only has to leave room for it (§7.6).
- **Remote servers.** The address, certificate, claim and pairing screens are
  removed from the app. `butterdbd` and its admin API stay in the tree
  unchanged for the future headless mode.
- **Sharing a project with another Telegram account.**

## 4. Anti-goals

These would count as failure even if everything else worked:

- The app ever asks a normal user for an address, port, certificate
  fingerprint, setup token or pairing code.
- butterDB ships a shared `api_id`/`api_hash`. D2 stands: each user brings
  their own.
- The project password, or the server key in the clear, ever reaches Telegram,
  a log or the audit log.
- Opening Project-B can read or write Project-A's data.
- Object bytes are kept on disk when the cache is off.

## 5. Constraints

- Hard rules in `docs/claude.md` §3 still apply: Go only, no comment blocks,
  docs in `docs/`, never push, the commit author and message rules, the root
  module builds with `CGO_ENABLED=0`, and lanes stay inside their owned paths.
- Retained wire formats and constants do not change. A new op goes through
  `docs/spec/wire-format.md` §7: format version and minimum reader.
- `golang.org/x/crypto` is already a dependency, so Argon2id is available
  without adding a module.
- The engine is pure Go and uses no process-wide state: no `slog.SetDefault`,
  no `os.Exit`, and no signal handling outside `cmd/`. It can be hosted inside
  the cgo GUI binary as it is.

## 6. Approaches considered

| Approach | Trade-off | Outcome |
| --- | --- | --- |
| **A. Engine inside the app** (TDrive's GUI model) | One process and nothing in the background. Storage is reachable only while the app is open, which is fine until the SDK exists. Needs layering rules L5 and L8 relaxed for one package | **Chosen** by the owner |
| B. App starts a local `butterdbd` daemon over a private Unix socket | Keeps the current module split, and the engine survives a closed window, which suits the SDK. But it adds a background process to manage, and TDrive's GUI does not work this way | Rejected for now. Becomes the headless mode later |
| C. Keep remote servers as an "advanced" option beside A | Brings back the screens the owner does not want, for a need nobody has yet | Dropped |

Key placement, chosen by the owner:

| Option | Outcome |
| --- | --- |
| **1. Key in Telegram, wrapped by a project password** | **Chosen** |
| 2. Key in Telegram, unwrapped | Rejected: encryption would stop protecting against Telegram |
| 3. Key file the user backs up themselves (today's A6) | Rejected: manual and easy to lose |

## 7. Design

### 7.1 Decision changes

These are recorded in `docs/decisions.md` first.

| Entry | Change |
| --- | --- |
| D1 | Still self-hosted per developer, now meaning "runs on the developer's own machine inside the app". Still no central hosted service |
| D13 | Fyne desktop app, **Linux only** for now. Windows and macOS are deferred |
| D14 | Replaced. The app embeds the engine. `butterdbd` stays as the future headless form of the same engine. The two never open the same project at once, which the process lock enforces |
| A6 | Amended. The server key is generated per project. A copy wrapped by the project password is stored in the project's registry channel. Losing both the local key and the password still makes encrypted objects unrecoverable |
| L5, L8 (`docs/claude.md` §5) | `apps/admin` may import exactly one root-module package, the embedding façade (§7.2), besides `api/...`. Only `cmd/butterdbd` and that façade import `internal/core` |
| New D entry | Projects: one Telegram account holds many projects, each with its own registry channel, buckets, server key and local data directory |

### 7.2 Embedding façade

Add one new root-module package, the only engine entry point the app imports.
Suggested name: `internal/embedded`. The name is the builder's call. It is a
second composition root beside `cmd/butterdbd`. It owns:

- **Account:**
  - the saved `api_id`/`api_hash`
  - the Telegram session
  - the login state machine: phone, code and 2FA, reusing `identity.Service`
  - logout
- **Projects:**
  - list the account's projects: their registry channels, each with its name
    and whether this machine already holds its key
  - create a project from a name and password
  - open a project, with the password if the key is not local
  - close the open project
  - rename a project
- **Open project:** gives the app an `http.RoundTripper` that serves the admin
  API in-process (§7.3).

Exactly one project is open at a time. Switching closes the current engine
(lifecycle group, reverse-order close) and opens the next.

### 7.3 How the app talks to the open project

The app keeps its admin API client and every view model. The reader found one
swap point: `Dialer.Dial` (`apps/admin/internal/app/dialer.go:20`) builds the
`adminv1` client, and the client already accepts a custom `HTTPTransport` for
loopback `http://` base URLs (`api/adminv1/client.go:117-121`).

- The façade returns a RoundTripper that calls the admin `http.Handler`
  (`Admin.Handler()`, `internal/core/admin.go:251`) directly, with no socket.
  The base URL is a fixed loopback `http://` value that is never dialled.
- **Embedded admin mode in core:**
  - no TCP listener, no TLS certificate, no setup-token mint and no control
    socket
  - no S3 listener
  - authentication is a fixed local-owner principal
  - safe because no listener exists: only code in the same process can reach
    the handler
- The audit log records the actor as the local owner. The device sweep and
  device endpoints are inert.
- Telegram login screens talk to the façade's account API, not to a project's
  admin API, because login happens before any project is open.

### 7.4 Local layout

Everything lives under `$XDG_DATA_HOME/butterdb`, by default
`~/.local/share/butterdb`, with mode `0700`:

```
butterdb/
  app.lock                         one running app per user
  account/
    account.key                    32 random bytes; seals the session (0600)
    telegram_app.json              api_id / api_hash (0600)
    session_primary.json           Telegram session, sealed under account.key (0600)
  projects/
    <deployment-id>/               a normal engine data_dir (0700)
      server.key                   this project's key (0600)
      control.db  projection.db  state.json  spool/  multipart/  cache/
```

- Each project's engine config points `telegram.app_file` and
  `telegram.session_file` at the shared `account/` files.
- `server_key_file` points inside the project directory.
- Each project directory keeps its own `butterdbd.lock`, so a future headless
  `butterdbd` can open the same directory, and the two cannot run at once.
- The account binding in each project's `control.db` keeps refusing a
  different Telegram account (`identity/login.go:526-540`).

### 7.5 Projects in Telegram

- **A project is a registry channel.** Discovery reuses
  `reconcile.Discoverer`, which identifies registries by their marker op and
  ignores titles.
- **Name:**
  - The project name becomes the registry channel title, in the form
    `butterDB · <name>`. The exact format is the builder's call.
  - The deployment ID stays in the about text.
  - Renaming a project edits the title.
- **Creating:**
  1. Generate a server key.
  2. Create the registry channel with its marker.
  3. Write the key-escrow message (§7.6).
  4. Write `state.json`.
- **Opening a project this machine does not have:**
  1. Read the escrow message.
  2. Derive the key from the password and unwrap it.
  3. Check it against the marker's fingerprint.
  4. Write `server.key`.
  5. Adopt the registry by its channel ID, rebuild the local index from
     Telegram, then open it.
- **Two changes to setup.** Today `SetupRegistry` creates a registry only when
  none exists, and the GUI path refuses to adopt one
  (`internal/core/storagestarter.go:500-513`). In the façade:
  - "create" always creates a new registry
  - "open" adopts a named registry
  - Neither prompts.
- A registry whose marker belongs to a different Telegram user is not listed.

### 7.6 Key escrow

- **A new op in the registry channel** carries:
  - the server key fingerprint
  - the KDF parameters: Argon2id, its salt, and its time, memory and
    parallelism costs
  - the wrapped key: an AEAD over the 32-byte key, keyed by the derived key,
    with the fingerprint and deployment ID as associated data
- The op is specified in `docs/spec/wire-format.md` and
  `docs/spec/storage-model.md`, following §7's versioning rules.
  - An older reader ignores it safely, so `mr` does not rise.
  - The exact op letter, field names and default costs are spec work for the
    plan.
- **Changing the password** writes a new escrow op. The newest valid one wins.
- **The password:**
  - It is entered twice at creation.
  - A plain warning says that forgetting it, and losing every machine that has
    the key, loses encrypted files.
  - Strength and length rules are the builder's call, with a sensible minimum.
- **The password never leaves memory.** It is not written to disk, logs or
  audit. The same rule the GUI spec already applies to `api_hash` and login
  codes covers it.

### 7.7 App screens

| Step | Screen | Notes |
| --- | --- | --- |
| 1 | Telegram app credentials | Only when `telegram_app.json` is missing. The existing login wizard step, with a link explaining my.telegram.org |
| 2 | Phone, then code, then 2FA | Existing wizard steps, now driven by the façade's account API |
| 3 | Project picker | Lists projects with name, a "needs password on this device" marker, and **New project**. Shows the last opened project first. Includes an empty state for a new account |
| 3a | New project | Name, password, confirm password, the loss warning, then Create |
| 3b | Unlock project | Password, shown only when the key is not on this machine |
| 4 | Dashboard | The existing dashboard, buckets and objects screens for the open project. The header shows the project name with a switch-project control. The access-keys screen and "Copy presigned URL" are hidden until the SDK work brings the S3 listener back, because nothing could use what they produce |

Removed from the app:

- the connect screen: address, saved servers, certificate trust, claim and
  pairing
- `servers.json`
- the device list, rotate and revoke parts of Settings

Settings gains **Log out of Telegram**, **Change project password** and
**Reset project key** (§7.9).

When an existing auto-connect profile is found (the owner's test
`servers.json`), it is ignored. The file is left on disk.

### 7.8 Specs and docs that change

- `docs/decisions.md`: §7.1.
- `docs/spec/admin-gui.md`:
  - connection and trust (§4) become account and projects
  - remove the claim and pairing screens
  - Linux only
- `docs/spec/admin-api.md`: embedded mode, with a local principal, no
  listener and no S3.
- `docs/spec/wire-format.md` and `docs/spec/storage-model.md`: the escrow op,
  project naming, create versus adopt.
- `docs/spec/security.md`: the password, the KDF and the threat model of
  escrow.
- `docs/claude.md` §5: L5 and L8.
- `docs/plan.md`: where this work sits. It is a new phase after phase 10, or a
  rework of phase 10 before it is fast-forwarded. The plan step decides which.

### 7.9 Same project on two machines

Owner's choice (2026-11-17): **allowed, with the risk accepted and stated.**

- Nothing stops a project being open on two machines at once. There is no
  lease and no read-only mode.
- The risk goes in the accepted-risk table of `docs/decisions.md`: two
  machines writing the same key at the same moment can silently lose one
  write, and conditional writes (`If-Match`, `If-None-Match`) are exact only
  within one machine. This narrows the "one process per data directory, no
  second writer" assumption of operations.md §1 and §6 to "per machine".
- The app shows the risk once per machine and project: as a plain note in the
  unlock screen (§7.7 step 3b), because unlocking means another machine
  created or holds this project. The operator guide states it too. Nothing
  tries to detect a live session elsewhere.

### 7.10 Calls made after the builder review

Settled by the designer, open to owner correction:

1. **The session is sealed under an account key.** Today `K_session` derives
   from the server key, which is now per project and does not exist at login.
   - A new `account/account.key` (32 random bytes, `0600`, generated on first
     login) seals `session_primary.json` with the same envelope as
     security.md §5.4. The domain label changes to an account-level one.
   - On a desktop, every key sits on the same disk as the data, so
     security.md §5.5 ("key off the data disk") does not apply. T4 and T5 are
     rewritten: a stolen, unlocked or unencrypted laptop gives full access, and
     OS disk encryption is the stated protection, the same as TDrive.
2. **Registries without an escrow message**, made by `butterdbd setup` or by
   the owner's earlier tests, are listed greyed out as "created outside the
   app, cannot be opened here". There is no import path in this work.
   `reconcile.Discoverer` is changed so that a registry whose fingerprint
   differs is a separate project, not foreign. A registry is foreign only when
   its marker's user differs.
3. **Changing the password:**
   - It needs the current password. Holding the key on the machine is not
     enough.
   - It writes the new escrow op, verifies it by unwrapping it, then deletes
     the older escrow messages, as rotation step 8 does.
   - Unlocking tries only the newest valid escrow op.
   - security.md states the limit: a copy of an old escrow message taken
     before deletion still opens with the old password. Rotating the server
     key is the fix and is out of scope here.
4. **Log out of Telegram** deletes the session only. The `api_id` pair,
   project keys and indexes stay, so logging in again on the same machine is
   fast. A separate **Remove this account from this device** deletes
   everything under `butterdb/`. A different Telegram account can log in after
   logout. Its projects get their own directories, and each project's account
   binding keeps them apart.
5. **Forgotten password and no machine with the key:**
   - **Reset project key** in the unlock screen reuses the lost-key flow of
     security.md §5.7 (`pkfp=lost`).
   - It needs a new password and the typed confirmation `reset project key`.
   - Encrypted objects become unreadable and are counted per bucket.
     Unencrypted objects, buckets and settings survive. The project is never
     stranded.
6. **Scope against phases 11 to 13:**
   - Snapshots, `--rotate-key`, the replica account and the transform worker
     stay in the headless `butterdbd` path. None of them is in the app in this
     work.
   - The phase 11 to 13 docs are reviewed for the per-project key when each
     phase starts, not rewritten now.
   - The only phase 11 piece pulled forward is the lost-key reset (item 5).

## 8. Left to the builder

- The façade package name and API shape, and how login runs before any project
  is open: `identity.Service` directly, or a minimal account-only engine.
- The exact escrow op encoding and Argon2id costs, within §7.6.
- The project title format, and how the picker shows a project whose key check
  fails.
- How the app shows engine start-up progress, such as rebuilding the index for
  a newly adopted project.
- Whether the old connect code is deleted or left unused. Deleted is preferred
  if nothing else uses it.
- Test strategy. The existing `faketg` fake should cover login, discovery,
  create, escrow round-trip, adopt on a second "machine", and refusing a wrong
  password.
