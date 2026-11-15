# butterDB — Decision Record

Every product and architecture decision that shapes the code, with the reason
behind it. The owner made the product decisions on 2026-11-12. The engineering
decisions at the end follow from them, and were adopted after an adversarial
review of the design.

When any other document disagrees with this one, this one wins until it is
changed deliberately.

---

## 1. What butterDB is

butterDB is **self-hosted object storage that speaks the S3 protocol and offers
Cloudinary-style media transforms, with its bytes stored in Telegram**.

A developer runs their own butterDB server, links their own Telegram account to
it, and their applications store and fetch images, videos and documents through
the S3 API, exactly as they would with AWS S3. Telegram is the storage backend.
butterDB is the service that makes Telegram behave like object storage.

It is infrastructure in its own right. It is not a private backend for one
application.

---

## 2. Product decisions

| # | Topic | Decision |
| --- | --- | --- |
| D1 | Deployment model | **Self-hosted per developer.** Each developer runs their own server with their own Telegram account. There is no central hosted service |
| D2 | Telegram account | **The deploying developer's own account**, with their own `api_id` and `api_hash` from my.telegram.org. butterDB never ships a shared credential |
| D3 | Tenancy | **One admin, many keys.** A deployment has one owner, who creates buckets and any number of scoped access keys for any number of applications |
| D4 | Scale target | **Small.** Under about 2,000 daily users, under 500,000 objects and under 1 TB per deployment in its first year. One server with a disk cache; a CDN is optional |
| D5 | API surface | **S3-compatible API plus a Cloudinary-style media API.** AWS SDKs and tools work against butterDB by changing the endpoint, for the operations `docs/spec/s3-api.md` supports. Its §17 lists the deviations from AWS S3; they await owner review. Media transforms have their own URL space |
| D6 | Object addressing | **S3-style overwritable keys.** Applications choose the bucket and the key; a second PUT to the same key replaces the object |
| D7 | Access model | **Bucket policy plus presigned URLs.** A bucket is public-read or private. Private objects are fetched with SigV4 presigned URLs |
| D8 | Upload acknowledgement | **Acknowledged after the Telegram commit.** A successful PUT means Telegram has accepted the object and the projection has applied it |
| D9 | Object size | **Up to 1.9 GB stored per object**, one Telegram document per object. The S3 multipart-upload API is supported, and its parts are assembled on the server before the single send |
| D10 | Delete | **Immediate hard delete.** DeleteObject removes the object and deletes its Telegram messages. There is no versioning and no trash |
| D11 | Encryption | **Optional.** Each bucket has a default, and each request may ask for encryption. Encrypted objects use the TDE1 container with keys held by the server |
| D12 | Durability | **Second Telegram account.** A deployment may link a replica account. Every object is mirrored to it, and reads fall back to it |
| D13 | Admin interface | **A native desktop GUI written with Fyne** for Windows, Linux and macOS |
| D14 | Engine placement | **Headless server plus remote GUI.** `butterdbd` owns the Telegram session and serves the APIs. The Fyne app (Windows, Linux, macOS) is a remote admin client over an authenticated admin API and holds no Telegram session. Engineering constraint, not an owner decision: the server targets Linux and macOS, because the process lock and sandboxing are Unix-only (`docs/spec/operations.md` §6) |
| D15 | Telegram login | **The standard Telegram user login with the developer's own `api_id`**: api_id, api_hash, phone, code and two-factor password. The GUI relays it to the server once |
| D16 | Media processing | **URL transforms through a separate worker process.** Resize, crop, quality and format conversion (WebP, AVIF, JPEG, with HEIC input) plus video posters, done by `butterdb-transformd` using libvips and ffmpeg. The main server stays pure Go |
| D17 | First release | **Store and serve first.** The first release has the S3 API, cached delivery, the admin GUI and operations. Media transforms and the replica account follow |
| D18 | Reuse of TDrive | **Port package by package.** TDrive (MIT) is the reference implementation of the storage engine. Each phase adapts the relevant TDrive package into butterDB's architecture and keeps the attribution |
| D19 | Phase execution | **One Opus orchestrator per phase, with Sonnet workers in parallel.** Workers are defined in `.claude/agents/`, and each lane runs in its own git worktree |
| D20 | Module path | **`github.com/PRIEYAN/ButterDB`**, matching the git remote |

---

## 3. Assumptions the owner has not confirmed

These follow from the decisions above. Each is a sensible default, and each
can be changed before the phase that builds it begins.

| # | Assumption | Built in |
| --- | --- | --- |
| A1 | After the first Telegram link, the GUI authenticates with a **device token** the server issues. A second device pairs by entering a one-time code that the server posts to the account's own Saved Messages, or a code printed by `butterdbd admin-token` on the host | Phase 09 |
| A2 | A freshly started, unlinked server prints a **one-time setup token** to its log. Only a GUI presenting it can claim the server, so a stranger cannot claim an exposed server first | Phase 09 |
| A3 | The admin API uses a server-generated TLS certificate whose **fingerprint the GUI pins** at pairing, unless a reverse proxy supplies a real certificate | Phase 09 |
| A4 | Objects are stored **byte for byte as uploaded**, as S3 requires. Metadata stripping (GPS, EXIF) applies to transform output, not to originals | Phases 06 and 13 |
| A5 | Encryption is requested with the standard S3 header `x-amz-server-side-encryption`, and the bucket default applies when it is absent | Phase 06 |
| A6 | A **server key file** (32 random bytes, generated at setup) is always required. It protects access-key secrets at rest and wraps every bucket's encryption key. Losing it makes encrypted objects unrecoverable, so the owner must back it up off the host | Phases 03 and 08 |
| A7 | A request may spend at most 30 seconds total waiting on admission, the send-rate limiter, Telegram flood waits and read slots; transfer time itself is not cut short. When a wait would exceed the remaining budget, answer `503 SlowDown` with `Retry-After` | Phase 06 |
| A8 | Public-read buckets serve `Cache-Control` from a per-bucket max-age with ETag revalidation. Because keys can be overwritten, URLs are never marked immutable | Phase 07 |
| A9 | The replica account is **optional per deployment** and, when linked, **mirrors every bucket**. Replication runs after the primary commit, from a durable journal | Phase 12 |
| A10 | The project's own open-source license is an **owner gate** in phase 01. TDrive's MIT notice is carried regardless | Phase 01 |

---

## 4. Engineering decisions

These are not owner preferences. Each one closes a failure mode found in
review.

| # | Decision | Reason |
| --- | --- | --- |
| E1 | **Each bucket is its own private Telegram channel.** A registry channel records bucket configuration. Its ID is stored in local state and checked against a registry marker on startup | Sync, rebuild, keys and blast radius stay isolated per bucket. The spec already keys every engine table by `channel_id` |
| E2 | An object is a **hidden body document plus a `t=put` control message** that names the key and references the body. The put message is the commit | Keys of up to 1024 bytes cannot fit a 1024-character document caption. This is the spec's `fcommit` pattern |
| E3 | Overwrites resolve **last writer wins in message-ID order**. The superseded body and put messages are then deleted from Telegram | Matches S3 semantics and keeps replay deterministic |
| E4 | Delete writes a **`t=del` tombstone first**, then deletes the object's messages in batches of 100. The tombstone blocks resurrection if a batch fails | Telegram deletion is irreversible and can partially fail |
| E5 | An **unknown op type is a skippable reject, not a parse failure that advances past it**. The raw header stays in the replay log so a newer binary's rebuild applies it | Lets new op types be added after the format freeze without breaking older binaries |
| E6 | **Ordering repair triggers only when a newly ingested op has a lower message ID than an already-recorded op for the same key**, and repairs that key alone | The server is now the continuous writer. The spec's rule would rebuild the projection on almost every sync pass |
| E7 | SQLite uses **one writer connection and a read-only WAL pool**, with pragmas applied per connection. `projection.db` holds the replay log and derived tables together. `control.db` holds local-only state | API reads must not queue behind writes. The replay log and derived tables must commit atomically, which SQLite cannot guarantee across attached files |
| E8 | Uploads are spooled to disk, hashed (MD5 for the S3 ETag, SHA-256 for integrity) and, when encrypted, sealed with a **file salt and nonce prefix persisted before the first send** | A retry after an uncertain send must not re-encrypt under different parameters, or reads fail authentication |
| E9 | **Idempotency is scoped per access key.** Server-minted job IDs derive `StableRandomID`, never a client-supplied value | Two clients sending the same idempotency key must never receive each other's Telegram message |
| E10 | A **conservative send-rate limiter is on by default**, sized from measured probe results. Server-directed `FLOOD_WAIT` remains an additional floor | A server writing around the clock while riding flood-wait escalations matches a known account-ban pattern |
| E11 | Phases run **strictly in sequence**. Within a phase, a **prelude** commits shared contracts and module changes before fan-out, and an **integration lane** owns `internal/core`, `cmd/` and acceptance tests | Parallel lanes in separate worktrees can only compile against contracts that already exist |
| E12 | The prelude runs `go get` on **exact package paths**, never module roots, and never runs `go mod tidy`. Only the integration lane tidies | `go mod tidy` drops unimported requirements, and module-root `go get` omits `go.sum` entries for subpackages |
| E13 | Only `internal/tgclient` imports gotd. Only `internal/storage` imports the SQLite driver. `internal/wire`, `internal/crypto/format` and `internal/domain` import the standard library only | Formats stay testable without a network or a database |
| E14 | The root module builds with **`CGO_ENABLED=0`**. The Fyne GUI and the transform worker are **separate Go modules** under `apps/`, joined by `go.work` | Fyne needs OpenGL through cgo and libvips needs cgo. The server must stay a static binary |
| E15 | The server key fingerprint is written everywhere as **`bkf1_` followed by the 32 lowercase hex characters** of its 16 bytes. The owner chose this on 2026-11-13, before the phase 02 format freeze | wire-format.md and storage-model.md used this form while operations.md and security.md used 22-character base64url. The `kfp` wire field is frozen by phase 02, so one form had to win, and the versioned prefix is self-describing |
| E16 | **Two TDX1 parser clarifications, frozen with the phase 02 format gate.** A present but empty `t=` is `ErrWireMalformed` (a `malformed` reject), not an unknown op. Binary values (`wrap`, `check`) must be canonical unpadded base64url with no CR or LF. The owner signed off on 2026-11-13 | `replay_log.op_type` is `CHECK (length(op_type) > 0)`, so an empty type recorded as an unknown op would fail the projector's page transaction. Lenient base64 let two different raw headers project to the same op |
| E17 | **`state.json` uses the flat shape of `docs/spec/storage-model.md` §4.3**: `version`, `registry_channel_id`, `registry_access_hash`, `registry_marker_msg_id`, `primary_user_id`, `server_key_fingerprint`. The ops and probe channels live in `control.db` (`ops_channels`), not in `state.json`. Unknown fields are tolerated on load and preserved on rewrite. The owner chose this on 2026-11-15, during phase 05 | storage-model.md §4.3 and operations.md §3.3 each named the other as the owner of the shape and listed different field names; the startup checks of storage-model §4.4 and §4.5 already used the flat names. Preserving unknown fields lets an older binary rewrite a file a newer binary extended |

---

## 5. Risks the owner accepts

| Risk | Consequence | Mitigation |
| --- | --- | --- |
| Telegram restricts or bans the storage account | Every object not mirrored is lost | Replica account (D12); use a dedicated, aged account with 2FA; never share an `api_id`; keep the send-rate limiter on |
| Telegram changes limits or terms | Throughput falls or the service stops | Typed flood-wait handling, measured limits, a self-hosted model where each deployer carries their own risk |
| The server key file is lost | Encrypted objects and stored access-key secrets are unrecoverable | Setup instructs an off-host backup; startup refuses to run with a key that does not match the registry fingerprint |
| One process per data directory | No horizontal scaling; deploys have seconds of downtime | Accepted at the small scale of D4 |
| Uploads wait for Telegram (D8) | A flood wait can stretch a PUT to minutes | `503 SlowDown` past the request budget (A7); documented SDK timeout settings |

Telegram's own guidance on `api_id` applies to every deployer: each phone
number can hold one `api_id`, accounts using unofficial clients are placed under
observation, and flooding or spamming through the API leads to a permanent ban.
