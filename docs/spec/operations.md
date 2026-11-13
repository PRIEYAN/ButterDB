# Operations specification

How `butterdbd` is configured, laid out on disk, started, observed, backed up,
restored, deployed, upgraded and sized. This document is authoritative for every
configuration key, every data-directory path, every listener default, the
readiness contract, every metric name and the snapshot format. The admin listener
keys are owned by [admin-api.md](admin-api.md) §2.1 and are repeated here
unchanged so the configuration schema is complete in one place.

| Item | Value |
| --- | --- |
| Built in | Phase 01 (`internal/platform/{config,logging,datadir,processlock,secretfile}`, `check-config`), phase 04 (`tgprobe`), phase 11 (`internal/ops/{health,metrics,snapshot}`, `deploy/`, `snapshot`, `restore`, `rebuild`) |
| Security companion | [security.md](security.md) owns the threat model, key derivation and redaction rationale |
| Bound by | D1, D4, D8, D9, D11, D12, D14, D16, A2, A3, A6, A7, A8, A9, E7, E8, E10, E14 |

Values marked **Default (owner may change)** are choices this document makes
where no decision settles them.

---

## 1. Principles

1. **One process per data directory.** No clustering, no shared database, no
   second writer (accepted risk in [decisions.md](../decisions.md) §5).
2. **Telegram is the source of truth; local state is a projection plus local-only
   secrets.** Everything in `projection.db` can be rebuilt from Telegram.
   `control.db` and the secret files cannot, so snapshots exist for them.
3. **Secrets enter configuration only as file paths.** No configuration key and
   no environment variable carries a secret value.
4. **Fail closed at start, degrade while running.** A wrong key, a foreign
   registry or a bad permission stops the process before any data listener
   serves. A Telegram outage after start degrades the server; it does not stop it.
5. **Every number an operator tunes is a key in §2.** Engine constants from the
   legacy specification (§13) are not configuration.

---

## 2. Configuration

### 2.1 Sources and precedence

| Rank (later wins) | Source | Notes |
| --- | --- | --- |
| 1 | Built-in defaults (this section) | |
| 2 | TOML file | Path from `--config`, else `BUTTERDB_CONFIG`, else `/etc/butterdb/butterdb.toml` |
| 3 | `BUTTERDB_*` environment variables | One per key, naming rule §2.3 |

- A path given with `--config` or `BUTTERDB_CONFIG` must exist. The default path
  may be absent; defaults plus environment then apply (container use).
- Decoding is strict. An unknown TOML key, an unknown `BUTTERDB_*` variable, a
  wrong type or an out-of-range value is a configuration error (exit code 2).
  Reserved variables that are not keys: `BUTTERDB_CONFIG`, and every variable
  whose name starts with `BUTTERDB_TGTEST` or `BUTTERDB_S3COMPAT_` (test
  harnesses only, for example `BUTTERDB_TGTEST_API_ID`,
  `BUTTERDB_S3COMPAT_ENDPOINT`; `serve` ignores them).
- The `[admin]`, `[s3]` and `[media]` tables are owned by
  [admin-api.md](admin-api.md) §2.1, [s3-api.md](s3-api.md) §2.1 and
  [media-api.md](media-api.md) §2.1 and are copied below unchanged. When a copy
  here and its owner differ, the owner wins and this file is corrected.
- Configuration is read once at start. Changes need a restart, except the admin
  certificate files in `files` mode, which `SIGHUP` reloads ([admin-api.md](admin-api.md) §2.2).
- TOML parser: `github.com/BurntSushi/toml`. Default (owner may change).

### 2.2 Value types

| Type | TOML form | Environment form | Rule |
| --- | --- | --- | --- |
| string | `"text"` | `text` | |
| path | `"/abs/path"` | `/abs/path` | Must be absolute after expansion. The only expansion is `${CREDENTIALS_DIRECTORY}` (systemd credentials, §11.1; unset while referenced is an error) and the token `<data_dir>` at the start of a default |
| bool | `true` | `true`, `false` | |
| int | `20` | `20` | Range per key |
| duration | `"30s"` | `30s` | Go `time.ParseDuration` syntax; `"0s"` only where the key says it disables |
| size | `"20GiB"` or `21474836480` | `20GiB` | Integer bytes, or an integer with suffix `KiB`, `MiB`, `GiB`, `TiB` (powers of 1024). Decimal suffixes (`GB`) are rejected so nobody guesses |
| address | `"0.0.0.0:9000"` | `0.0.0.0:9000` | `host:port`; IPv6 as `[::]:9000` |
| list | `["a", "b"]` | `a,b` | Comma-separated in the environment; empty string means empty list |

### 2.3 Environment naming

`BUTTERDB_` + the key path in upper case with `.` replaced by `_`.
`telegram.send_rate.control_per_minute` becomes
`BUTTERDB_TELEGRAM_SEND_RATE_CONTROL_PER_MINUTE`. Top-level keys have no section:
`data_dir` becomes `BUTTERDB_DATA_DIR`.

### 2.4 Every key

#### Top level

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `data_dir` | `BUTTERDB_DATA_DIR` | path | `/var/lib/butterdb` | Data directory (§3) |
| `server_key_file` | `BUTTERDB_SERVER_KEY_FILE` | path | `/etc/butterdb/server.key` | Server key file (A6; [security.md](security.md) §5) |

#### `[log]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `log.level` | `BUTTERDB_LOG_LEVEL` | string | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | `BUTTERDB_LOG_FORMAT` | string | `json` | `json` (production) or `text` (development) |
| `log.access_log` | `BUTTERDB_LOG_ACCESS_LOG` | bool | `true` | One `INFO` line per HTTP request on the S3, admin and media listeners |
| `log.object_keys` | `BUTTERDB_LOG_OBJECT_KEYS` | string | `full` | How object keys appear in logs: `full`, `hash` (first 16 hex characters of SHA-256), `none`. Default (owner may change) |

#### `[s3]` (owned by [s3-api.md](s3-api.md) §2.1)

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `s3.listen` | `BUTTERDB_S3_LISTEN` | address | `0.0.0.0:9000` | S3 listener |
| `s3.public_url` | `BUTTERDB_S3_PUBLIC_URL` | string | empty | External base URL, e.g. `https://s3.example.com`. Used for presigned URLs and reported as `endpoints.s3_public_url` |
| `s3.region` | `BUTTERDB_S3_REGION` | string | `us-east-1` | Region clients sign with |
| `s3.base_domains` | `BUTTERDB_S3_BASE_DOMAINS` | list | `[]` | Domains under which `<bucket>.<domain>` selects virtual-hosted-style. Empty means path-style only |
| `s3.tls_mode` | `BUTTERDB_S3_TLS_MODE` | string | `none` | `none` (plain HTTP, normally behind a TLS proxy) or `files` |
| `s3.tls_cert_file` | `BUTTERDB_S3_TLS_CERT_FILE` | path | empty | PEM chain, `files` mode |
| `s3.tls_key_file` | `BUTTERDB_S3_TLS_KEY_FILE` | path | empty | PEM key, `files` mode; a secret file (§2.5) |
| `s3.trusted_proxies` | `BUTTERDB_S3_TRUSTED_PROXIES` | list | `["127.0.0.1/32", "::1/128"]` | CIDRs whose `X-Forwarded-For` is honoured |
| `s3.request_budget` | `BUTTERDB_S3_REQUEST_BUDGET` | duration | `30s` | Total time one S3 request may spend waiting on admission, the E10 send-rate limiter, Telegram flood waits and `getFile` slots before `503 SlowDown`; transfer time is not counted ([s3-api.md](s3-api.md) §12.1, A7) |
| `s3.body_idle_timeout` | `BUTTERDB_S3_BODY_IDLE_TIMEOUT` | duration | `60s` | Longest gap between request body reads |
| `s3.write_idle_timeout` | `BUTTERDB_S3_WRITE_IDLE_TIMEOUT` | duration | `60s` | Longest blocked response write |
| `s3.keepalive_interval` | `BUTTERDB_S3_KEEPALIVE_INTERVAL` | duration | `15s` | Whitespace interval for long responses |
| `s3.completed_upload_retention` | `BUTTERDB_S3_COMPLETED_UPLOAD_RETENTION` | duration | `24h` | How long a completed multipart upload answers a repeated Complete |
| `s3.multipart_idle_expiry` | `BUTTERDB_S3_MULTIPART_IDLE_EXPIRY` | duration | `168h` | The sweeper aborts multipart uploads idle this long |
| `s3.auth_failure_limit` | `BUTTERDB_S3_AUTH_FAILURE_LIMIT` | int | `100` | Failed authentications per client address per 10 minutes before blocking |
| `s3.cors_allowed_origins` | `BUTTERDB_S3_CORS_ALLOWED_ORIGINS` | list | `[]` | CORS origins; empty disables CORS |
| `s3.cors_allowed_methods` | `BUTTERDB_S3_CORS_ALLOWED_METHODS` | list | `["GET", "HEAD", "PUT", "POST", "DELETE"]` | CORS methods |
| `s3.cors_allowed_headers` | `BUTTERDB_S3_CORS_ALLOWED_HEADERS` | list | `["*"]` | CORS request headers |
| `s3.cors_expose_headers` | `BUTTERDB_S3_CORS_EXPOSE_HEADERS` | list | [s3-api.md](s3-api.md) §13.3 list | CORS exposed response headers |
| `s3.cors_max_age` | `BUTTERDB_S3_CORS_MAX_AGE` | int | `3600` | Preflight cache seconds |
| `s3.anonymous_rate` | `BUTTERDB_S3_ANONYMOUS_RATE` | int 1–10000 | `50` | Anonymous (public-read) requests per second per client address, enforced in `internal/access/policy` before any Telegram read ([security.md](security.md) §9). Added by this document; Default (owner may change) |
| `s3.anonymous_burst` | `BUTTERDB_S3_ANONYMOUS_BURST` | int 1–10000 | `100` | Burst for the limiter above. Added by this document; Default (owner may change) |

The SigV4 clock skew (15 minutes) and presign maximum (7 days) are constants of
`internal/access/sigv4`, not keys ([s3-api.md](s3-api.md) §15).

#### `[ingest]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `ingest.max_concurrent_puts` | `BUTTERDB_INGEST_MAX_CONCURRENT_PUTS` | int 1–64 | `4` | Process-wide put slots in `ingest/admission`, shared by S3 and admin uploads. Default (owner may change) |
| `ingest.max_concurrent_puts_per_key` | `BUTTERDB_INGEST_MAX_CONCURRENT_PUTS_PER_KEY` | int 1–64 | `2` | Put slots one access key (or one admin device) may hold at once. Default (owner may change) |

#### `[admin]` (owned by [admin-api.md](admin-api.md) §2.1)

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `admin.listen` | `BUTTERDB_ADMIN_LISTEN` | address | `0.0.0.0:9443` | Admin listener |
| `admin.tls_mode` | `BUTTERDB_ADMIN_TLS_MODE` | string | `self-signed` | `self-signed`, `files`, `proxy` |
| `admin.tls_cert_file` | `BUTTERDB_ADMIN_TLS_CERT_FILE` | path | empty | PEM chain, `files` mode only |
| `admin.tls_key_file` | `BUTTERDB_ADMIN_TLS_KEY_FILE` | path | empty | PEM key, `files` mode only; a secret file (§2.5) |
| `admin.hostnames` | `BUTTERDB_ADMIN_HOSTNAMES` | list | `[]` | Extra SANs for the self-signed certificate |
| `admin.trusted_proxies` | `BUTTERDB_ADMIN_TRUSTED_PROXIES` | list | `["127.0.0.1/32", "::1/128"]` | CIDRs whose `X-Forwarded-For` is honoured |
| `admin.control_socket` | `BUTTERDB_ADMIN_CONTROL_SOCKET` | path | `<data_dir>/admin.sock` | Local control socket ([admin-api.md](admin-api.md) §5.5) |
| `admin.setup_token_ttl` | `BUTTERDB_ADMIN_SETUP_TOKEN_TTL` | duration | `24h` | Setup token lifetime |
| `admin.pairing_code_ttl` | `BUTTERDB_ADMIN_PAIRING_CODE_TTL` | duration | `10m` | Pairing code lifetime |
| `admin.device_idle_revoke` | `BUTTERDB_ADMIN_DEVICE_IDLE_REVOKE` | duration | `4320h` | Idle device revocation |
| `admin.upload_commit_budget` | `BUTTERDB_ADMIN_UPLOAD_COMMIT_BUDGET` | duration | `10m` | Longest an admin upload waits on Telegram |

#### `[media]` (phase 13; owned by [media-api.md](media-api.md) §2.1)

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `media.enabled` | `BUTTERDB_MEDIA_ENABLED` | bool | `false` | Bind the media listener and supervise the worker |
| `media.listen` | `BUTTERDB_MEDIA_LISTEN` | address | `0.0.0.0:9080` | Media listener |
| `media.path_prefix` | `BUTTERDB_MEDIA_PATH_PREFIX` | string | `""` | Optional prefix such as `/media` |
| `media.public_url` | `BUTTERDB_MEDIA_PUBLIC_URL` | string | `""` | Reported as `endpoints.media_public_url` |
| `media.hostnames` | `BUTTERDB_MEDIA_HOSTNAMES` | list | `[]` | Allowed `Host` values when the listener is bound to loopback |
| `media.trusted_proxies` | `BUTTERDB_MEDIA_TRUSTED_PROXIES` | list | `["127.0.0.1/32", "::1/128"]` | As `s3.trusted_proxies` |
| `media.signing_key_file` | `BUTTERDB_MEDIA_SIGNING_KEY_FILE` | path | `<data_dir>/secrets/media_signing.key` | Current media signing key; a secret file (§2.5) |
| `media.signing_key_previous_file` | `BUTTERDB_MEDIA_SIGNING_KEY_PREVIOUS_FILE` | path | `""` | Previous signing key, verification only |
| `media.private_max_expiry` | `BUTTERDB_MEDIA_PRIVATE_MAX_EXPIRY` | duration | `168h` | Furthest `exp_` a private-bucket URL may carry |
| `media.width_ladder` | `BUTTERDB_MEDIA_WIDTH_LADDER` | list | media-api §8 ladder | Allowed output widths |
| `media.worker_mode` | `BUTTERDB_MEDIA_WORKER_MODE` | string | `spawn` | `spawn`: `butterdbd` runs the worker as a child. `external`: systemd or a container runs it as its own user ([security.md](security.md) §12.4) |
| `media.worker_path` | `BUTTERDB_MEDIA_WORKER_PATH` | string | `butterdb-transformd` | Worker binary, resolved with `exec.LookPath` at start; spawn mode only |
| `media.worker_socket` | `BUTTERDB_MEDIA_WORKER_SOCKET` | path | `<data_dir>/run/transformd.sock` | Worker Unix socket. External mode: `/run/butterdb-transformd/transformd.sock` |
| `media.worker_tmp_dir` | `BUTTERDB_MEDIA_WORKER_TMP_DIR` | path | `<data_dir>/run/transformd-tmp` | Worker scratch; spawn mode only |
| `media.ffmpeg_path` | `BUTTERDB_MEDIA_FFMPEG_PATH` | path | `/usr/bin/ffmpeg` | Spawn mode only |
| `media.worker_concurrency` | `BUTTERDB_MEDIA_WORKER_CONCURRENCY` | int | `min(NumCPU, 4)` | Concurrent worker jobs |
| `media.worker_memory_limit` | `BUTTERDB_MEDIA_WORKER_MEMORY_LIMIT` | size | `1GiB` | Worker `RLIMIT_AS`; spawn mode only |
| `media.worker_sandbox` | `BUTTERDB_MEDIA_WORKER_SANDBOX` | string | `auto` | `auto`, `require`, `off`; spawn mode only |
| `media.queue_depth` | `BUTTERDB_MEDIA_QUEUE_DEPTH` | int | `64` | Waiters for a worker slot before `503` |
| `media.queue_timeout` | `BUTTERDB_MEDIA_QUEUE_TIMEOUT` | duration | `10s` | Longest wait for a worker slot |
| `media.source_timeout` | `BUTTERDB_MEDIA_SOURCE_TIMEOUT` | duration | `120s` | Longest time to read the original |
| `media.image_timeout` | `BUTTERDB_MEDIA_IMAGE_TIMEOUT` | duration | `20s` | Image job budget |
| `media.poster_timeout` | `BUTTERDB_MEDIA_POSTER_TIMEOUT` | duration | `30s` | Poster job budget, ffmpeg included |
| `media.poison_ttl` | `BUTTERDB_MEDIA_POISON_TTL` | duration | `1h` | Quarantine of a source that crashed the worker twice |
| `media.negative_ttl` | `BUTTERDB_MEDIA_NEGATIVE_TTL` | duration | `10m` | Memory of a deterministic transform failure |
| `media.cache_max_bytes` | `BUTTERDB_MEDIA_CACHE_MAX_BYTES` | size | `10GiB` | Derivative disk tier (`cache/derived/`) ceiling |
| `media.cache_memory_max_bytes` | `BUTTERDB_MEDIA_CACHE_MEMORY_MAX_BYTES` | size | `128MiB` | Memory tier ceiling (derivatives of encrypted objects) |
| `media.cache_sweep_interval` | `BUTTERDB_MEDIA_CACHE_SWEEP_INTERVAL` | duration | `1h` | Stale-version sweep period |
| `media.max_image_source_bytes` | `BUTTERDB_MEDIA_MAX_IMAGE_SOURCE_BYTES` | size | `64MiB` | Largest plaintext image source |
| `media.max_video_source_bytes` | `BUTTERDB_MEDIA_MAX_VIDEO_SOURCE_BYTES` | size | `1GiB` | Largest plaintext video source for a poster |
| `media.max_source_pixels` | `BUTTERDB_MEDIA_MAX_SOURCE_PIXELS` | int | `50000000` | Largest width × height of one source frame |
| `media.max_source_side` | `BUTTERDB_MEDIA_MAX_SOURCE_SIDE` | int | `32768` | Largest source width or height |
| `media.max_output_side` | `BUTTERDB_MEDIA_MAX_OUTPUT_SIDE` | int | `4096` | Largest output width or height |
| `media.max_output_pixels` | `BUTTERDB_MEDIA_MAX_OUTPUT_PIXELS` | int | `16777216` | Largest output width × height |
| `media.max_output_bytes` | `BUTTERDB_MEDIA_MAX_OUTPUT_BYTES` | size | `32MiB` | Largest encoded derivative |
| `media.max_frames` | `BUTTERDB_MEDIA_MAX_FRAMES` | int | `200` | Largest frame count kept animated |
| `media.max_animated_pixels` | `BUTTERDB_MEDIA_MAX_ANIMATED_PIXELS` | int | `200000000` | Largest width × height × frames kept animated |
| `media.avif_auto_max_pixels` | `BUTTERDB_MEDIA_AVIF_AUTO_MAX_PIXELS` | int | `4194304` | `f_auto` chooses AVIF only at or below this output size |

Presets and per-bucket media settings (`media.presets.<name>`,
`media.buckets.<bucket>.*`) and the hard ceilings in `internal/domain/limits`
are specified in [media-api.md](media-api.md) §2.1 and §2.2; `SIGHUP` reloads
them. Every `[media]` default is Default (owner may change).

#### `[ops]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `ops.listen` | `BUTTERDB_OPS_LISTEN` | address | `127.0.0.1:9090` | Ops listener (`/healthz`, `/readyz`, `/metrics`) |
| `ops.hostnames` | `BUTTERDB_OPS_HOSTNAMES` | list | `[]` | Extra `Host` values accepted on a loopback ops listener (DNS-rebinding guard) |
| `ops.metrics_token_file` | `BUTTERDB_OPS_METRICS_TOKEN_FILE` | path | empty | Secret file holding a bearer token required on `/metrics` |
| `ops.allow_unauthenticated_metrics` | `BUTTERDB_OPS_ALLOW_UNAUTHENTICATED_METRICS` | bool | `false` | Permits a non-loopback ops listener without a token |
| `ops.ready_when_degraded` | `BUTTERDB_OPS_READY_WHEN_DEGRADED` | bool | `true` | `/readyz` answers `200` in `degraded` (§7.3) |

#### `[telegram]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `telegram.app_file` | `BUTTERDB_TELEGRAM_APP_FILE` | path | `<data_dir>/secrets/telegram_app.json` | `api_id` and `api_hash`, written by the server |
| `telegram.session_file` | `BUTTERDB_TELEGRAM_SESSION_FILE` | path | `<data_dir>/secrets/session_primary.json` | Primary gotd session, sealed ([security.md](security.md) §5.4) |
| `telegram.replica_session_file` | `BUTTERDB_TELEGRAM_REPLICA_SESSION_FILE` | path | `<data_dir>/secrets/session_replica.json` | Replica session (phase 12) |
| `telegram.connect_timeout` | `BUTTERDB_TELEGRAM_CONNECT_TIMEOUT` | duration | `30s` | First connection attempt at start before the server proceeds degraded |
| `telegram.device_model` | `BUTTERDB_TELEGRAM_DEVICE_MODEL` | string | `butterDB server` | Shown in Telegram's active sessions list |
| `telegram.test_dc` | `BUTTERDB_TELEGRAM_TEST_DC` | bool | `false` | Use Telegram test data centres. Development only; `check-config` warns when true |
| `telegram.send_rate.control_per_minute` | `BUTTERDB_TELEGRAM_SEND_RATE_CONTROL_PER_MINUTE` | int 1–600 | `20` | E10 limiter, `message` class (§13.3) |
| `telegram.send_rate.control_burst` | `BUTTERDB_TELEGRAM_SEND_RATE_CONTROL_BURST` | int 1–100 | `5` | Burst of the class above |
| `telegram.send_rate.documents_per_minute` | `BUTTERDB_TELEGRAM_SEND_RATE_DOCUMENTS_PER_MINUTE` | int 1–120 | `6` | E10 limiter, `document` class |
| `telegram.send_rate.documents_burst` | `BUTTERDB_TELEGRAM_SEND_RATE_DOCUMENTS_BURST` | int 1–20 | `1` | Burst of the class above. Default (owner may change) |

The send-rate defaults are the plan's values until the phase 11 owner gate
replaces them with `tgprobe` measurements (§13.5). The limiter cannot be
disabled; there is no `0` value.

#### `[cache]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `cache.dir` | `BUTTERDB_CACHE_DIR` | path | `<data_dir>/cache` | Holds `blocks/` and `derived/`; may live on another local filesystem |
| `cache.blocks_max_bytes` | `BUTTERDB_CACHE_BLOCKS_MAX_BYTES` | size | `20GiB` | Origin block cache ceiling. The derivative cache ceiling is `media.cache_max_bytes` |
| `cache.warm_on_put_max_bytes` | `BUTTERDB_CACHE_WARM_ON_PUT_MAX_BYTES` | size | `64MiB` | Objects up to this stored size are copied from the spool into the block cache after commit; `0` disables |
| `cache.min_free_bytes` | `BUTTERDB_CACHE_MIN_FREE_BYTES` | size | `2GiB` | Eviction also runs when the cache filesystem has less free space than this |

#### `[spool]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `spool.max_bytes` | `BUTTERDB_SPOOL_MAX_BYTES` | size | `8GiB` | Reservation ceiling for `spool/`. Two concurrent maximum-size encrypted puts need about 7.5 GiB (plain plus sealed copy). Default (owner may change) |
| `spool.multipart_max_bytes` | `BUTTERDB_SPOOL_MULTIPART_MAX_BYTES` | size | `16GiB` | Ceiling for staged S3 multipart parts in `multipart/` |
| `spool.min_free_bytes` | `BUTTERDB_SPOOL_MIN_FREE_BYTES` | size | `1GiB` | A reservation fails when it would leave less free space than this on the data filesystem |

#### `[snapshot]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `snapshot.dir` | `BUTTERDB_SNAPSHOT_DIR` | path | `<data_dir>/snapshots` | Local snapshot directory |
| `snapshot.interval` | `BUTTERDB_SNAPSHOT_INTERVAL` | duration | `24h` | Scheduled snapshot period inside `serve`; `0s` disables |
| `snapshot.retain` | `BUTTERDB_SNAPSHOT_RETAIN` | int 1–365 | `7` | Local snapshots kept, newest first |
| `snapshot.upload_to_telegram` | `BUTTERDB_SNAPSHOT_UPLOAD_TO_TELEGRAM` | bool | `false` | Also send each snapshot to the ops channel (§9.4) |
| `snapshot.telegram_retain` | `BUTTERDB_SNAPSHOT_TELEGRAM_RETAIN` | int 1–365 | `7` | Snapshots kept in the ops channel |
| `snapshot.before_migrate` | `BUTTERDB_SNAPSHOT_BEFORE_MIGRATE` | bool | `true` | Take a local snapshot before applying a schema migration (§12) |

#### `[shutdown]`

| Key | Env | Type | Default | Meaning |
| --- | --- | --- | --- | --- |
| `shutdown.drain_timeout` | `BUTTERDB_SHUTDOWN_DRAIN_TIMEOUT` | duration | `10s` | Listener drain budget |
| `shutdown.component_timeout` | `BUTTERDB_SHUTDOWN_COMPONENT_TIMEOUT` | duration | `5s` | Close budget per lifecycle component |
| `shutdown.total_timeout` | `BUTTERDB_SHUTDOWN_TOTAL_TIMEOUT` | duration | `60s` | Hard ceiling; past it the process exits with code 1 |

### 2.5 Secret files

Configuration names secret files; it never holds secret values.

| File | Key | Written by | Required mode |
| --- | --- | --- | --- |
| Server key | `server_key_file` | Operator, `butterdbd setup --key-only`, or first `serve` (§5.3) | See rules below |
| Telegram app credentials | `telegram.app_file` | Server (`PUT /admin/v1/telegram/credentials`, `setup`) | `0600` |
| Primary session | `telegram.session_file` | Server | `0600` |
| Replica session | `telegram.replica_session_file` | Server (phase 12) | `0600` |
| Admin TLS key, `files` mode | `admin.tls_key_file` | Operator | See rules below |
| Admin TLS key, `self-signed` mode | `<data_dir>/tls/admin.key` (fixed, [admin-api.md](admin-api.md) §2.3) | Server | `0600` |
| S3 TLS key, `files` mode | `s3.tls_key_file` | Operator | See rules below |
| Media signing key (phase 13) | `media.signing_key_file`, `media.signing_key_previous_file` | Server creates the current file when absent ([media-api.md](media-api.md) §7.1) | `0600` |
| Metrics token | `ops.metrics_token_file` | Operator | See rules below |

Rules for files the server writes: `internal/platform/secretfile` (temp file in the
same directory, `chmod 0600` before the first secret byte, write, fsync, close,
rename, fsync the directory; ported from TDrive `backend/auth/private_file.go`).

Rules for operator-supplied secret files, checked by `check-config` and at every
start:

| Condition | Result |
| --- | --- |
| Any permission bit for others | Error |
| Group write or execute | Error |
| Group read, file group equals the process's effective group, file owned by root | Warning. Kubernetes secret volumes with `fsGroup` cannot be `0600` for a non-root process, so this is the one exception to the `0600` rule of [storage-model.md](storage-model.md) §8.1 |
| Group read, any other group | Error |
| Owner is neither the process user nor root | Error |
| Symlink | Followed once; the target is checked |

### 2.6 Example file

```toml
data_dir = "/var/lib/butterdb"
server_key_file = "${CREDENTIALS_DIRECTORY}/server.key"

[log]
level = "info"

[s3]
listen = "127.0.0.1:9000"
public_url = "https://s3.example.com"
region = "us-east-1"
base_domains = ["s3.example.com"]

[admin]
listen = "0.0.0.0:9443"
tls_mode = "self-signed"
hostnames = ["admin.example.com"]

[ops]
listen = "127.0.0.1:9090"

[telegram.send_rate]
control_per_minute = 20
documents_per_minute = 6

[cache]
blocks_max_bytes = "100GiB"

[snapshot]
interval = "24h"
upload_to_telegram = true
```

### 2.7 Go contract (`internal/platform/config`)

```go
package config

type Config struct {
	DataDir       string         `toml:"data_dir"`
	ServerKeyFile string         `toml:"server_key_file"`
	Log           LogConfig      `toml:"log"`
	S3            S3Config       `toml:"s3"`
	Ingest        IngestConfig   `toml:"ingest"`
	Admin         AdminConfig    `toml:"admin"`
	Media         MediaConfig    `toml:"media"`
	Ops           OpsConfig      `toml:"ops"`
	Telegram      TelegramConfig `toml:"telegram"`
	Cache         CacheConfig    `toml:"cache"`
	Spool         SpoolConfig    `toml:"spool"`
	Snapshot      SnapshotConfig `toml:"snapshot"`
	Shutdown      ShutdownConfig `toml:"shutdown"`
}

type Duration time.Duration

type ByteSize int64

type LoadOptions struct {
	Path         string
	PathExplicit bool
	Environ      []string
}

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Finding struct {
	Severity Severity
	Key      string
	Message  string
}

func Default() Config
func Load(opts LoadOptions) (Config, []Finding, error)
func (d *Duration) UnmarshalText(b []byte) error
func (s *ByteSize) UnmarshalText(b []byte) error
```

Every section struct has one field per key in §2.4, named in Go case
(`ControlPerMinute`), with the TOML tag equal to the key's last segment. `Load`
returns decoding and range findings; host checks (§2.8) live in
`cmd/butterdbd` and use `internal/platform/{datadir,secretfile,fsx}`.

### 2.8 `butterdbd check-config`

```
butterdbd check-config [--config <path>] [--json]
```

Reads configuration, inspects the host, prints findings and exits. It never
contacts Telegram, never takes the process lock and never writes a file.

| Check | Severity on failure |
| --- | --- |
| TOML parses; no unknown keys; no unknown `BUTTERDB_*` variable; types and ranges | error |
| The four listen addresses parse and no two share a port on overlapping addresses | error |
| `data_dir` exists with mode `0700` owned by the process user, or its parent is writable so it can be created | error |
| `data_dir` and `cache.dir` are local filesystems (Linux `statfs` magic is not NFS `0x6969`, SMB `0x517B`, CIFS `0xFF534D42` or FUSE `0x65735546`) | error |
| `spool/` and `multipart/` resolve to the same filesystem as `data_dir` | error |
| Free space on the data filesystem at least `spool.max_bytes + spool.min_free_bytes` | warning |
| Server key file exists, passes §2.5 and is exactly 32 bytes (§5.1) | error (absent is a warning when no `state.json` exists, because `serve` generates it) |
| When `state.json` exists, its `kek_fingerprint` equals the key file's fingerprint | error |
| Every configured secret file passes §2.5 | error |
| Admin rules of [admin-api.md](admin-api.md) §2.1 (cert/key pair in `files` mode, loopback in `proxy` mode, control socket path at most 103 bytes) | error |
| S3 rules of [s3-api.md](s3-api.md) §2.1 (`files` TLS pair, `public_url` form, base domains, region pattern, CORS origins) | error |
| Media rules of [media-api.md](media-api.md) §2.3, when `media.enabled` | error |
| `ops.listen` is not loopback and neither `ops.metrics_token_file` nor `ops.allow_unauthenticated_metrics` is set | error |
| `s3.public_url` is empty | warning (presigned URLs unavailable) |
| `media.enabled`, `media.worker_mode = "spawn"` and `media.worker_sandbox = "off"` | warning (no sandbox around the decoders; [security.md](security.md) §12.4) |
| `snapshot.upload_to_telegram = false` and `snapshot.dir` is on the same filesystem as `data_dir` | warning (no off-host copy of `control.db`) |
| `telegram.test_dc = true` | warning |

Output, one line per finding, then a summary line:

```
error   server_key_file  /etc/butterdb/server.key: mode 0644 allows group or other access
warning s3.public_url    empty; presigned URLs are unavailable
check-config: 1 error, 1 warning
```

With `--json`: `{"ok": false, "findings": [{"severity": "error", "key": "server_key_file", "message": "…"}], "kek_fingerprint": "Hx3ZfP76d3p7TxUkGr9XvQ"}`.
The fingerprint is printed when the key file is readable, so an operator can
verify a backup copy against `state.json` (§9.6). Exit codes follow §4.2.

---

## 3. Data directory

### 3.1 Layout

| Path under `data_dir` | Mode | Holds | Owner package |
| --- | --- | --- | --- |
| `.` | `0700` | | `internal/platform/datadir` |
| `butterdbd.lock` | `0600` | Owner metadata JSON of the process lock (§6) | `internal/platform/processlock` |
| `butterdbd.lock.guard` | `0600` | Guard file that carries the kernel lock; never unlinked (§6) | `internal/platform/processlock` |
| `state.json` | `0600` | Deployment anchor ([storage-model.md](storage-model.md) §4.3) | `internal/buckets` through `secretfile` |
| `projection.db`, `projection.db-wal`, `projection.db-shm` | `0600` | `replay_log`, rejects, tamper, derived tables (E7) | `internal/storage/sqlitedb` |
| `control.db`, `control.db-wal`, `control.db-shm` | `0600` | Access keys, admin devices, tokens, jobs, idempotency, multipart index, replica journal, audit, `ops_channels` (§3.3) (E7) | `internal/storage/sqlitedb` |
| `admin.sock` | `0600` | Control socket ([admin-api.md](admin-api.md) §5.5) | `internal/adminapi/session` |
| `tls/` | `0700` | | `internal/adminapi/tlscert` |
| `tls/admin.key` | `0600` | Self-signed admin key (A3, [admin-api.md](admin-api.md) §2.3) | `internal/adminapi/tlscert` |
| `tls/admin.crt` | `0644` | Self-signed admin certificate (public; still behind the `0700` directories) | `internal/adminapi/tlscert` |
| `secrets/` | `0700` | | `internal/identity` |
| `secrets/telegram_app.json` | `0600` | `api_id`, `api_hash` | `internal/identity` |
| `secrets/session_primary.json` | `0600` | Sealed gotd session ([security.md](security.md) §5.4) | `internal/identity` |
| `secrets/session_replica.json` | `0600` | Sealed replica session (phase 12) | `internal/replica/account` |
| `secrets/media_signing.key` | `0600` | Media signing key (phase 13, [media-api.md](media-api.md) §7.1) | `internal/mediaapi/signing` |
| `spool/` | `0700` | `<job id>.plain`, `<job id>.sealed` while a put job runs | `internal/ingest/spool` |
| `multipart/<upload id>/` | `0700` | `part-<NNNNN>` files (`0600`) of open S3 multipart uploads | `internal/ingest/multipartstage` |
| `cache/blocks/` | `0700` | Origin block cache, stored bytes (ciphertext for encrypted objects) | `internal/readpath/blockcache` |
| `cache/derived/` | `0700` | Derivatives of unencrypted objects only (phase 13, [media-api.md](media-api.md) §11) | `internal/mediaapi/derivcache` |
| `snapshots/` | `0700` | `*.bdsnap` files (§9) | `internal/ops/snapshot` |
| `tmp/` | `0700` | Scratch for snapshot assembly, restore and rebuild; emptied at start | `internal/platform/datadir` |
| `run/` | `0700` | Created by the media supervisor (phase 13) | `internal/mediaapi/workerclient` |
| `run/transformd.sock` | `0600` | Worker socket, `media.worker_mode = "spawn"` | `internal/mediaapi/workerclient` |
| `run/transformd-tmp/` | `0700` | Worker scratch, spawn mode; holds only unlinked files while a job runs | `apps/transformd` |
| `rekey.json` | `0600` | Progress of an interrupted server key rotation; `serve` refuses to start while it exists ([security.md](security.md) §5.6) | `cmd/butterdbd` |

Every file the server creates is `0600` and every directory `0700`, except
`tls/admin.crt`. The process runs with umask `0077`. On start `datadir`
refuses a data directory whose mode grants group or other access, or whose owner
is not the process user; it never silently changes modes. Paths below
`data_dir` are fixed; only `cache.dir`, `snapshot.dir`, the media worker keys and
the secret-file keys move things. [plan.md](../plan.md) §4 names
`admin/tls_key.pem` and `admin/tls_cert.pem`; [admin-api.md](admin-api.md) §2.3
owns these files and names them `tls/admin.key` and `tls/admin.crt`, which this
table follows.

All filesystem access below `data_dir` goes through an `os.Root` opened on the
directory (`internal/platform/fsx`), so no computed path can escape it
([security.md](security.md) §11).

### 3.2 Filesystem rules

| Group | Rule | Reason |
| --- | --- | --- |
| `data_dir` as a whole | One local filesystem with POSIX advisory locks and shared-memory `mmap`. Never NFS, SMB or a FUSE network mount | SQLite WAL needs a working `-shm` and locks; `flock` over NFS is unreliable |
| `projection.db` with its `-wal` and `-shm`; `control.db` with its `-wal` and `-shm` | Same directory | SQLite requirement |
| `spool/` and `multipart/` | Same filesystem as `data_dir` | Multipart assembly and spool hand-off use `rename` |
| `state.json`, every secret file | Temp file in the same directory, then `rename` | Atomic replace (`secretfile`) |
| `tmp/` | Same filesystem as `data_dir` | Restore and rebuild publish by `rename` into `data_dir` |
| `cache.dir` | Any local filesystem | Block files are written as temp then renamed inside `cache.dir` |
| `snapshot.dir` | Any local filesystem; best on a different disk | A snapshot is assembled in `snapshot.dir/.tmp-<id>/` and renamed |

### 3.3 `state.json` and `ops_channels`

`state.json` is specified by [storage-model.md](storage-model.md) §4.3: fields
`version`, `deployment_id`, `primary_user_id`, `registry.{channel_id,
access_hash, marker_msg_id}`, `kek_fingerprint` (base64url of the 16-byte key
fingerprint, §5.2), `format_version`, `created_at`. Unknown fields are rejected
on load, so operations state is not kept there.

The two channels this document adds, the ops channel (§9.4) and the probe
channel (§13.5), are recorded in `control.db`. Default (owner may change); the
phase 11 prelude adds the migration:

```sql
CREATE TABLE ops_channels (
  purpose     TEXT    PRIMARY KEY CHECK (purpose IN ('ops', 'probe')),
  account     TEXT    NOT NULL CHECK (account IN ('primary', 'replica')),
  channel_id  INTEGER NOT NULL,
  access_hash INTEGER NOT NULL,
  created_at  INTEGER NOT NULL
) STRICT;
```

Neither channel is a bucket or registry channel: the syncer never reads it and
the projection never sees it. When `control.db` is lost, both are found again
by title (§9.4).

---

## 4. Command line

### 4.1 Subcommands

| Subcommand | Takes the process lock | Contacts Telegram | Notes |
| --- | --- | --- | --- |
| `serve` | yes | yes | §7 |
| `setup` | yes | yes (except `--key-only`) | TTY Telegram link; key flags in §4.3 |
| `admin-token` | only when no server answers the control socket | no | [admin-api.md](admin-api.md) §5.5 |
| `snapshot` | only when no server answers the control socket | only with upload | §9.3 |
| `restore` | yes | only with `--from-telegram` | §10.2 |
| `rebuild` | yes | only with `--from-telegram` | §10.3 |
| `check-config` | no | no | §2.8 |
| `version` | no | no | Prints `butterdbd <version> (<commit>, <go version>, <GOOS>/<GOARCH>)`; `--json` for one object |
| `tgprobe` | yes | yes | §13.5 |

Every subcommand accepts `--config <path>`.

### 4.2 Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Runtime failure not covered below, including shutdown past `shutdown.total_timeout` |
| 2 | Invalid usage or configuration (any `check-config` error) |
| 3 | Data directory locked by another process |
| 4 | Server key missing, unreadable, malformed, wrongly permissioned, or fingerprint mismatch with `state.json` or the registry marker |
| 5 | Data directory incompatible: schema newer than this binary, `restore` target not empty, registry in `state.json` differs from the snapshot |
| 6 | Telegram required and unavailable (unlinked, unreachable, or authorization revoked) |
| 130 | Interrupted by `SIGINT` before completion (one-shot subcommands only) |

### 4.3 `setup` key flags

| Invocation | Effect |
| --- | --- |
| `butterdbd setup --key-only` | Creates `server_key_file` if absent (never overwrites), prints its fingerprint and the backup instructions of [security.md](security.md) §5.3, exits. Touches neither `data_dir` nor Telegram |
| `butterdbd setup --rotate-key --new-key-file <path>` | Re-wraps every secret under a new key ([security.md](security.md) §5.6). Server stopped, Telegram reachable |
| `butterdbd setup --replace-lost-key --new-key-file <path>` | Adopts a new key after the old one is lost, discarding what only the old key could open ([security.md](security.md) §5.7). Requires typing `replace lost key` on the TTY |

Default (owner may change): these flags live on `setup` because the brief fixes
the subcommand list. Rotation and replacement are built in phase 11.

---

## 5. Server key at start

### 5.1 Format

The key file holds exactly 32 random bytes and nothing else
([storage-model.md](storage-model.md) §8.1). `keyring.Load` rejects any other
length. An operator can create one with `head -c 32 /dev/urandom > server.key`
or `openssl rand -out server.key 32`. Off-host text backups use base64
([security.md](security.md) §5.3).

### 5.2 Fingerprint

The first 16 bytes of
`HKDF-SHA256(ikm = key, salt = empty, info = "butterdb/keyring/fingerprint/v1")`
([storage-model.md](storage-model.md) §8.1), written everywhere as base64url
without padding (22 characters, for example `Hx3ZfP76d3p7TxUkGr9XvQ`). It is
logged at `INFO` on every start as `kek_fingerprint`, stored in `state.json`
(`kek_fingerprint`), carried by `t=reg` and `t=encfg` (`kfp`), and written into
every snapshot manifest and ops-channel caption.

### 5.3 Generation

| Situation at `serve` start | Action |
| --- | --- |
| Key file present | Load and verify (§7.1 step 5) |
| Key file absent, no `state.json`, no linked primary in `control.db` | Generate with `crypto/rand`, write through `secretfile` (`0600`), log one `WARN` line `{"msg":"server key generated","path":"…","kek_fingerprint":"Hx3ZfP76d3p7TxUkGr9XvQ","action":"back this file up off the host now"}` |
| Key file absent, `state.json` present | Exit 4: the deployment exists and its key is missing ([security.md](security.md) §5.7) |
| Key file absent and its directory is read-only (systemd credential, Kubernetes secret) | Exit 4 with the hint `create it with butterdbd setup --key-only` |

---

## 6. Process lock and single instance

| Property | Value |
| --- | --- |
| Files | `<data_dir>/butterdbd.lock` (owner metadata) and `<data_dir>/butterdbd.lock.guard` (the locked file), both mode `0600` |
| Mechanism | `unix.Flock(LOCK_EX \| LOCK_NB)` on the guard file, which is never unlinked so every contender locks the same inode. Linux and macOS only; the Windows lock files are not ported ([tdrive-port-map.md](../reference/tdrive-port-map.md) phase 01). Ported from TDrive `backend/processlock/lock.go` and `lock_unix.go` |
| Metadata | JSON `{"role":"serve","pid":4242,"started_at":1763000000,"lock_version":1}`, published through a temp file after the lock is held. Diagnostic only; the kernel lock is the truth |
| Stale lock | A crashed process releases the kernel lock. Metadata naming a dead PID is overwritten |
| Held by another process | Exit 3 with `data directory locked by pid 4242 (serve, started …)` |
| Release | Last step of shutdown (§8) |

Single-instance rule: one `butterdbd` process owns one data directory and one
Telegram account binding. Two data directories bound to the same Telegram
account are not supported: they would share Telegram's per-account limits, and
each would see the other's registry as foreign at start. The registry marker
fingerprint check (§7.1) refuses a second deployment that holds a different
server key; a copied data directory with the same key is not detectable and must
not be run. In containers this rule is enforced by one replica, a
`ReadWriteOncePod` volume and the `Recreate` strategy (§11.2).

---

## 7. Startup and readiness

### 7.1 Startup order (`butterdbd serve`)

The order follows [plan.md](../plan.md) §2.5 and §2.7. Steps 1–7 run before any
listener binds; that is how invariant 20 of [claude.md](../claude.md) holds.

| Step | Action | On failure |
| --- | --- | --- |
| 1 | Load and validate configuration (§2) | Exit 2 |
| 2 | Build the logger (§14) | Exit 2 |
| 3 | `datadir.Open`: create or verify `data_dir`, `0700`, owner; empty `tmp/` | Exit 2 |
| 4 | `processlock.Acquire` | Exit 3 |
| 5 | `keyring.Load(server_key_file)` (or generate, §5.3); permission rules of §2.5; refuse while `rekey.json` exists | Exit 4 |
| 6 | `sqlitedb.Open` for `projection.db` and `control.db`; snapshot before migration when `snapshot.before_migrate` and a migration is pending; apply embedded migrations | Exit 5 (newer schema) or 1 |
| 7 | **Local registry check** ([plan.md](../plan.md) §3.5 step 4): `state.json` loads and validates; its `kek_fingerprint` equals the loaded key's; when it names a registry channel, the projected registry `channels` row and marker in `projection.db` carry the same channel ID and fingerprint | Exit 4 (fingerprint), exit 5 (`state_invalid`, `projection_mismatch`) |
| 8 | Bind the ops listener. `/healthz` answers `200` from here | Exit 1 |
| 9 | Bind the admin listener and the control socket. Setup and pairing work even while unlinked | Exit 1 |
| 10 | Bind the S3 listener and, when `media.enabled`, the media listener. Both answer `503` until the serving gate opens (§7.4) | Exit 1 |
| 11 | When linked: start the primary `tgclient` run scope and wait up to `telegram.connect_timeout`. On timeout continue; the connection keeps retrying in the background | Proceed degraded |
| 12 | **Remote registry check** (once connected), steps 3 and 5–7 of [storage-model.md](storage-model.md) §4.4: authorized user equals `primary_user_id`; `channels.getMessages` on `marker_msg_id` returns a `t=reg` whose `dep`, `uid` and `kfp` equal `state.json`; marker `mr` is not above this binary's format version | Failure (`telegram_account_mismatch`, `registry_marker_missing`, `registry_marker_mismatch`, `format_too_new`): one `ERROR` line, the server **refuses**: no sync, no sweeper, S3 stays `503`, the admin listener keeps serving and reports the code in `GET /admin/v1/status`. No automatic repair. Unreachable: stays pending |
| 13 | Syncer: registry channel to `Final`, then every bucket channel to `Final`; then live sync | Stays `syncing`; retries per the sync budgets |
| 14 | Start the ingest sweeper, the replica worker (phase 12) and the transform worker supervisor (phase 13) | Component reported `down` |
| 15 | Open the serving gate | |

An unlinked server stops after step 10 in state `unlinked`: the admin listener
serves setup, the S3 listener answers `503`. Completing the Telegram link
([admin-api.md](admin-api.md) §8) runs steps 11–15 without a restart. A linked
server with no `state.json` (lost file, or a stray registry from an interrupted
link) enters operator-confirmed rediscovery ([storage-model.md](storage-model.md)
§4.5) and stays in `syncing` until the owner confirms a registry channel.

### 7.2 States

`state` values are those of `GET /admin/v1/status` ([admin-api.md](admin-api.md) §9.1).

| State | Entered when | S3 reads | S3 writes |
| --- | --- | --- | --- |
| `starting` | Steps 1–10 | `503` | `503` |
| `unlinked` | No Telegram session | `503` | `503` |
| `syncing` | Linked; remote registry check pending or refused, rediscovery awaiting confirmation, or a channel not yet `Final` since start | `503` | `503` |
| `ready` | Serving gate open, Telegram connected | served | served |
| `degraded` | Serving gate opened earlier in this process, Telegram now disconnected or the write coordinator cooling down longer than `s3.request_budget` | served from projection and block cache; a cache miss that cannot be fetched within `s3.request_budget` returns `503 SlowDown` | `503 SlowDown` with `Retry-After` ([s3-api.md](s3-api.md) §12.1) |

A server that starts while Telegram is unreachable stays `syncing`; it does not
serve from a projection it has not re-verified. The process is the only writer
to its channels, so a projection verified earlier in the same process stays
correct while Telegram is away; that is why `degraded` may serve reads.

### 7.3 `/healthz` and `/readyz`

Both are on the ops listener, unauthenticated, `GET` only, JSON, `Cache-Control: no-store`.

`/healthz` (liveness):

| Condition | Response |
| --- | --- |
| Process past step 8 and no lifecycle component has reported a fatal error | `200 {"status":"ok"}` |
| A component reported fatal (writer connection I/O error, disk full on the database file, panic recovered in a supervisor) | `503 {"status":"fatal","component":"projection","error_code":"disk_full"}` |

`/readyz` (readiness):

```json
{
  "status": "ready",
  "state": "ready",
  "checks": [
    {"name": "database", "status": "ok"},
    {"name": "server_key", "status": "ok"},
    {"name": "telegram", "status": "connected"},
    {"name": "registry", "status": "verified"},
    {"name": "sync", "status": "authoritative"}
  ]
}
```

| Check | Values (first listed is healthy) |
| --- | --- |
| `database` | `ok`, `migrating`, `error` |
| `server_key` | `ok` (a mismatch never reaches `/readyz`: the process exits 4 at step 5 or 7) |
| `telegram` | `connected`, `connecting`, `disconnected`, `flood_wait`, `unlinked`, `unauthorized` |
| `registry` | `verified`, `pending`, `absent` (unlinked or no registry yet), `rediscovery` (no `state.json`, awaiting the owner), `refused` (a [storage-model.md](storage-model.md) §4.4 check failed; `detail` carries its code) |
| `sync` | `authoritative`, `catching_up` |

| `status` | HTTP | When |
| --- | --- | --- |
| `ready` | `200` | State `ready` |
| `degraded` | `200` when `ops.ready_when_degraded`, else `503` | State `degraded` |
| `not_ready` | `503` | Any other state |
| `draining` | `503` | Shutdown started (§8) |

`/readyz?listener=admin` answers `200` once the admin listener is bound and the
process is not draining, whatever the Telegram state. Use it for the readiness
of a load balancer or Kubernetes `Service` that fronts only the admin port, so a
fresh unlinked server stays reachable for setup.

### 7.4 Serving gate

The gate is one `atomic.Bool` owned by `internal/ops/health` and read by the S3
and media routers. Closed, they answer:

| Listener | Response |
| --- | --- |
| S3 | `503`, S3 XML error `ServiceUnavailable`, `Retry-After: 30` ([s3-api.md](s3-api.md) §2.7); `OPTIONS` preflights are still answered |
| Media | `503`, media error envelope with code `media.not_ready`, `Retry-After: 30`. Default (owner may change); [media-api.md](media-api.md) §12.4 must list the code |

### 7.5 Go contract (`internal/ops/health`)

```go
package health

type State string

const (
	StateStarting State = "starting"
	StateUnlinked State = "unlinked"
	StateSyncing  State = "syncing"
	StateReady    State = "ready"
	StateDegraded State = "degraded"
)

type Check struct {
	Name   string
	Status string
	Detail string
	Since  time.Time
}

type Source interface {
	Checks() []Check
}

type Gate interface {
	Open() bool
}

func NewHandler(sources []Source, gate Gate, readyWhenDegraded bool, clk clock.Clock) http.Handler
```

---

## 8. Graceful shutdown

Triggered by `SIGTERM` or `SIGINT` (`os/signal.NotifyContext`). A second signal
skips the remaining budgets and exits with code 1.

| Order | Action | Budget |
| --- | --- | --- |
| 1 | `/readyz` turns `503 draining`; the serving gate closes for new requests | immediate |
| 2 | S3, media, admin and ops listeners stop accepting (`http.Server.Shutdown`) and drain | `shutdown.drain_timeout` (10 s) shared |
| 3 | Requests still running at the deadline have their contexts cancelled | |
| 4 | Transform worker supervisor stops `butterdb-transformd` (`SIGTERM`, then `SIGKILL` after the budget) | `shutdown.component_timeout` (5 s) |
| 5 | Replica worker and backfill stop at a journal boundary | 5 s |
| 6 | Ingest sweeper and in-flight jobs stop at a job-row boundary | 5 s |
| 7 | Syncer stops | 5 s |
| 8 | Telegram run scope closes | 5 s |
| 9 | `projection.db` and `control.db` checkpoint (`wal_checkpoint(TRUNCATE)`) and close | 5 s |
| 10 | Logging flushes | |
| 11 | Process lock releases | |

The order is [claude.md](../claude.md) §6.5. Past `shutdown.total_timeout` (60 s)
the process exits with code 1; SQLite WAL makes that safe.

A PUT cut off by step 3 ends in the job state the write path defines: before
`put_attempted` it is aborted and its orphan body is deleted by the sweeper on
the next start; after it the job is uncertain and is reconciled on the next start.
The client sees a reset connection and retries; its retry is a new put that wins
by message ID (E3). Orchestrator and service-manager stop timeouts must exceed
`shutdown.total_timeout` (systemd `TimeoutStopSec=75`, Kubernetes
`terminationGracePeriodSeconds: 75`).

`SIGHUP` reloads the admin certificate in `files` mode and nothing else.

---

## 9. Snapshots

### 9.1 What a snapshot holds

| Member | Source | Why |
| --- | --- | --- |
| `manifest.json` | Generated | Format, versions, fingerprint, member hashes |
| `projection.db` | `VACUUM INTO` | Avoids a full rebuild from Telegram (§10.4) |
| `control.db` | `VACUUM INTO` | Access-key secrets, devices, jobs, multipart index, replica journal, audit. **Not recoverable from Telegram** |
| `state.json` | Copy | Registry anchor |

Not included: `secrets/` (Telegram sessions and app credentials are re-created by
logging in again; keeping session material out of snapshots limits what a stolen
snapshot plus key yields; the media signing key must be backed up with the
server key if applications embed it), `tls/` (a new self-signed certificate means
devices re-pin; see [security.md](security.md) §7), `spool/`, `multipart/`, `run/`,
caches. Default (owner may change).

### 9.2 Format (`bdsnap1`)

| Layer | Content |
| --- | --- |
| File name | `butterdb-<server_id>-<YYYYMMDDTHHMMSSZ>.bdsnap` |
| Outer | TDE1 container ([security.md](security.md) §4) under `K_snapshot = HKDF-SHA256(ikm = server key, salt = empty, info = "butterdb/keyring/snapshot/v1")`, 32 bytes ([security.md](security.md) §5.2) |
| Inner | gzip (level 1) of a POSIX tar (`archive/tar`, USTAR) with exactly the four members of §9.1, regular files only, mode `0600`, in the order `manifest.json`, `state.json`, `control.db`, `projection.db` |

`manifest.json`:

```json
{
  "format": "bdsnap1",
  "created_at": "2026-11-13T03:00:00.000Z",
  "server_id": "…",
  "binary_version": "0.1.0",
  "schema": {"projection": 1, "control": 1},
  "kek_fingerprint": "Hx3ZfP76d3p7TxUkGr9XvQ",
  "deployment_id": "6pzyoxbfwcf6ubwcq5gpvjg5c4",
  "registry_channel_id": 2045100001,
  "members": [
    {"name": "state.json", "bytes": 412, "sha256": "…"},
    {"name": "control.db", "bytes": 8388608, "sha256": "…"},
    {"name": "projection.db", "bytes": 1073741824, "sha256": "…"}
  ]
}
```

TDE1 needs the plaintext size before sealing, so the tar.gz is assembled in
`snapshot.dir/.tmp-<id>/` (mode `0700`) and sealed into the final file, which is
renamed into place; the temp directory is then removed.

### 9.3 Taking a snapshot

| Trigger | Path |
| --- | --- |
| Schedule | `serve` every `snapshot.interval`, first run one interval after start |
| `butterdbd snapshot` with a server running | Sends `{"version":1,"command":"snapshot"}` on the control socket; the running server takes it and answers `{"version":1,"ok":true,"kind":"snapshot","path":"…","bytes":123,"uploaded":true}`. Default (owner may change): the control socket of [admin-api.md](admin-api.md) §5.5 gains this command |
| `butterdbd snapshot` with no server | Takes the process lock and runs offline |
| Before a migration | Step 6 of §7.1 |

Procedure (`internal/ops/snapshot` through ports; `internal/storage/sqlitedb`
exposes `VacuumInto`):

1. `VACUUM INTO 'tmp path'` on a read-pool connection of `projection.db`, then of
   `control.db`. Each is a transactionally consistent copy of its database. The
   two copies are not one atomic cut; that is safe because Telegram is the truth:
   after restore, sync catches the projection up and the sweeper reconciles any
   job whose state disagrees with it.
2. Copy `state.json`; hash every member; write `manifest.json`.
3. tar, gzip, seal, rename. `PRAGMA integrity_check` is not run on the copy
   (it is a fresh `VACUUM` output); restore runs it.
4. Prune local snapshots beyond `snapshot.retain`.
5. When `snapshot.upload_to_telegram`, upload (§9.4).

```go
package snapshot

type Vacuumer interface {
	VacuumInto(ctx context.Context, database string, dstPath string) error
}

type Sealer interface {
	Seal(dst io.Writer, src io.Reader, plaintextSize int64) error
}

type Result struct {
	Path     string
	Bytes    int64
	SHA256   [32]byte
	Uploaded bool
}

func (s *Service) Create(ctx context.Context) (Result, error)
```

### 9.4 Ops channel upload

| Property | Value |
| --- | --- |
| Channel | Private broadcast channel titled `butterDB ops`, created with `channels.createChannel` on first upload; ID and access hash saved in `control.db` `ops_channels` (§3.3) |
| Message | The `.bdsnap` file as a document, `ForceFile`, `application/octet-stream` |
| Caption | `bdsnap1\|<file name>\|<sha256 hex of the sealed file>\|<server key fingerprint>` — not TDX1, so no syncer or projection ever reads it |
| Random ID | `StableRandomID(<snapshot id>, "snap")` |
| Send rules | As every body ([storage-model.md](storage-model.md) §2.2): `inputMediaUploadedDocument{force_file: true}`, `silent: true`, through the E10 `document` limiter and the write coordinator |
| Size | A sealed snapshot above 1900 MiB is not uploaded; a `WARN` line and `butterdb_snapshot_upload_failures_total{reason="too_large"}` record it |
| Retention | After a successful upload, older snapshot messages beyond `snapshot.telegram_retain` are deleted with `channels.deleteMessages` |
| Rediscovery | After host loss, `restore --from-telegram` finds the channel by title among the account's own broadcast channels, then the newest caption whose fingerprint equals the loaded key's |

The ops channel is the only off-host copy of `control.db` that butterDB makes
by itself. It is encrypted under the server key; without the key file it is
useless ([security.md](security.md) §5.3).

### 9.5 Sizes at D4 scale

Estimates for 500,000 objects with average key 60 bytes and 200 bytes of user
metadata; measure on the phase 11 load harness and update.

| Item | Estimate |
| --- | --- |
| `projection.db` | 1.0–1.5 GB (derived rows about 0.8 KB per object, replay log about 0.6 KB per op, roughly 1.3 ops per object, plus indexes) |
| `control.db` | 10–100 MB (dominated by job and audit retention) |
| Sealed snapshot (gzip level 1) | 0.4–0.7 GB |
| `VACUUM INTO` time on SSD | 20–60 s |
| Upload to the ops channel at the §13.2 planning rate | 2–3 min |

### 9.6 Verifying a snapshot

`butterdbd restore --snapshot <file> --verify-only` opens and authenticates the
container, checks every member hash and the fingerprint, runs
`PRAGMA integrity_check` on extracted copies in `snapshot.dir/.verify-<id>/`
(mode `0700`), prints the manifest and deletes the copies. It does not need an empty data directory and does not take
the process lock. Run it monthly against an off-host copy, with the off-host key
backup, to prove both work.

---

## 10. Restore and rebuild

### 10.1 Which procedure

| Lost | Have | Procedure |
| --- | --- | --- |
| `projection.db` only (corrupt) | Everything else | `rebuild` from the replay log if `replay_log` is readable (§10.3), else `rebuild --from-telegram` |
| `projection.db` and `control.db` | Snapshot | §10.2 |
| Whole host | Key backup + snapshot (local copy or ops channel) | §10.5 |
| Whole host | Key backup, no snapshot | §10.5 with step 4 instead of step 3: the projection is rebuilt from Telegram, access keys and admin devices are re-created |
| Server key file | | [security.md](security.md) §5.7. Encrypted objects are gone |
| `state.json` only | Everything else | `butterdbd rebuild --from-telegram --registry-channel <id>` (§10.3), or restore the newest snapshot's `state.json` with `restore` |

### 10.2 `butterdbd restore`

```
butterdbd restore --snapshot <file> [--config <path>]
butterdbd restore --from-telegram [--name <file name>] [--config <path>]
butterdbd restore --snapshot <file> --verify-only
```

1. Take the process lock (exit 3 if held).
2. Refuse unless `projection.db` and `control.db` are absent (exit 5). `secrets/`,
   `tls/` and `state.json` may exist (§10.5 creates them).
3. `--from-telegram`: requires a linked session; finds the ops channel (§9.4) and
   downloads the newest snapshot, or `--name`, into `tmp/`. Exit 6 when
   Telegram is unavailable.
4. Open the TDE1 container with `K_snapshot`; an authentication failure means a
   wrong key or a damaged file (exit 4).
5. Read the tar stream with the allowlist of §9.2: any other member name, a
   non-regular entry, a size different from the manifest or a total above
   `2 × manifest total` is rejected ([security.md](security.md) §12.3).
6. Check every member hash, `manifest.kek_fingerprint` against the
   loaded key (exit 4), and, when `state.json` already exists, that its
   `registry_channel_id` equals the manifest's (exit 5).
7. Write the members into `tmp/`, run `PRAGMA integrity_check` on both
   databases, refuse a schema newer than the binary (exit 5), then rename into
   `data_dir` (`state.json` last).
8. Print the manifest summary and the next step: `butterdbd serve`.

On the next `serve`, sync resumes from the snapshot's stored `pts` per channel;
a cursor Telegram rejects as too old falls back to a full history scan of that
channel. The sweeper reconciles every job row that references a spool file
which no longer exists. Changes made only in `control.db` after the snapshot
(access keys created, devices paired) are lost.

### 10.3 `butterdbd rebuild`

```
butterdbd rebuild [--config <path>]
butterdbd rebuild --from-telegram [--bucket <name>] [--config <path>]
butterdbd rebuild --from-telegram --registry-channel <id> [--config <path>]
```

| Mode | What it does |
| --- | --- |
| default | `internal/projection/rebuild` replays `replay_log` into fresh derived tables in one transaction per keyset batch of 256 rows; byte-identical to the incremental projection (rebuild equivalence test) |
| `--from-telegram` | Builds `tmp/projection.rebuild.db` by full history scan of the registry channel, then every bucket channel it names (legacy S11 two-phase scan: discover extent newest-first, then read oldest-first into the spool table, then apply). On success the live `projection.db` is renamed to `projection.db.pre-rebuild-<UTC timestamp>` and the new file renamed into place |
| `--from-telegram --bucket <name>` | Rescans one bucket channel into the live `projection.db`, replacing that bucket's rows in one transaction |
| `--from-telegram --registry-channel <id>` | Operator-confirmed rediscovery ([storage-model.md](storage-model.md) §4.5). Without the flag on a TTY it lists the candidate registry channels (ID, title, creation date, `dep`, `fv`, whether `kfp` and `uid` match) and exits; with it, it refuses a candidate whose `kfp` or `uid` does not match, writes `state.json` from the newest accepted `t=reg`, then runs the full rebuild above |

Both require the process lock (server stopped) and print progress per channel
every 10 s. `serve` also performs a full scan by itself for any channel whose
`initial_sync_done` is 0, which is what happens after the projection file is
deleted.

### 10.4 Time estimates at D4 scale

Planning figures until the phase 11 load harness and `tgprobe` (history step)
measure them. Message count for 500,000 objects: about 1,000,000 body and put
messages plus tombstones; superseded messages are deleted (E3, E4).

| Procedure | Dominant cost | Estimate |
| --- | --- | --- |
| Restore from a local snapshot | Decrypt, gunzip, integrity check of about 1.5 GB | 2–5 min |
| Restore from the ops channel | Download of about 0.6 GB at 2–4 MB/s, then as above | 5–10 min |
| Catch-up after a 24 h old snapshot | `updates.getChannelDifference` per channel | under 1 min to a few minutes |
| `rebuild` from the replay log | Re-applying about 1.3 million ops in SQLite | 3–10 min |
| `rebuild --from-telegram` | About 10,500 `messages.getHistory` pages of 100, read twice (discovery and read) = about 21,000 calls; at 1–3 calls/s without flood waits | 2–6 h |
| Re-creating access keys and re-pairing devices after a loss without snapshot | Manual | owner time |

Bodies are never downloaded during a rebuild; the put op carries every field the
projection needs (E2). The block cache stays valid across a rebuild because its
key is `(channel_id, body_msg_id, block_index)`.

### 10.5 Disaster recovery on a new host

1. Install `butterdbd`, write the configuration, place the key file from the
   off-host backup, run `butterdbd check-config` and compare the printed
   fingerprint with the one recorded at setup.
2. `butterdbd setup` (TTY) and link the **same** Telegram account. Setup never
   adopts an existing registry by itself and never creates a second one while
   candidates exist: it reports that the account holds registry candidates and
   exits.
3. With a snapshot: `butterdbd restore --snapshot <file>` or
   `butterdbd restore --from-telegram`. The snapshot carries `state.json`, so no
   rediscovery is needed.
4. Without a snapshot: `butterdbd rebuild --from-telegram` lists candidates;
   confirm the one whose `kfp` matches with
   `butterdbd rebuild --from-telegram --registry-channel <id>` (§10.3). This
   scans every channel from message zero (§10.4).
5. `butterdbd serve`. Without a snapshot the server starts `unclaimed` with a new
   setup token.
6. Pair the GUI again: the admin certificate is new, so every device re-pins
   ([security.md](security.md) §7). Without a snapshot, re-create access keys and
   update every application's credentials.

---

## 11. Deployment targets

### 11.1 Single static binary with systemd

Build: `CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags "-s -w -X main.version=<v> -X main.commit=<c>" -o butterdbd ./cmd/butterdbd`
(E14). Install to `/usr/local/bin/butterdbd`, mode `0755`.

Host preparation:

```bash
useradd --system --home /var/lib/butterdb --shell /usr/sbin/nologin butterdb
install -d -m 0750 -o root -g butterdb /etc/butterdb
install -m 0640 -o root -g butterdb butterdb.toml /etc/butterdb/butterdb.toml
BUTTERDB_SERVER_KEY_FILE=/etc/butterdb/server.key butterdbd setup --key-only --config /etc/butterdb/butterdb.toml
chown root:root /etc/butterdb/server.key && chmod 0600 /etc/butterdb/server.key
```

`setup --key-only` runs as root with the key path overridden in the environment,
because the configuration names the service's credential directory, which exists
only inside the running unit.

`deploy/systemd/butterdbd.service`:

```ini
[Unit]
Description=butterDB object storage server
Documentation=https://github.com/PRIEYAN/ButterDB
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
User=butterdb
Group=butterdb
ExecStart=/usr/local/bin/butterdbd serve --config /etc/butterdb/butterdb.toml
LoadCredential=server.key:/etc/butterdb/server.key
StateDirectory=butterdb
StateDirectoryMode=0700
UMask=0077
Restart=on-failure
RestartSec=5s
RestartPreventExitStatus=2 3 4 5
TimeoutStopSec=75s
KillSignal=SIGTERM
LimitNOFILE=65536
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectHostname=yes
ProtectClock=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectProc=invisible
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources

[Install]
WantedBy=multi-user.target
```

With this unit the configuration sets
`server_key_file = "${CREDENTIALS_DIRECTORY}/server.key"`; systemd copies the key
into a private, non-swappable credential directory readable only by the service.
`RestartPreventExitStatus` stops a restart loop on configuration, lock, key and
compatibility errors. On hosts with a TPM2, `systemd-creds encrypt` plus
`LoadCredentialEncrypted=` keeps the key file encrypted at rest so a stolen
disk does not yield it ([security.md](security.md) §5.5). When `snapshot.dir` is outside
`/var/lib/butterdb`, add it to `ReadWritePaths=`.

The transform worker unit (phase 13, `deploy/transformd/butterdb-transformd.service`)
is specified in [media-api.md](media-api.md) §21.2: user `butterdb-transformd`,
group `butterdb`, socket `/run/butterdb-transformd/transformd.sock` mode `0660`.
`butterdbd` then runs with `media.worker_mode = "external"`; its own user is in
group `butterdb` already, and the worker cannot read `data_dir` (`0700`) or the
key ([security.md](security.md) §12.4).

### 11.2 Distroless container

`deploy/Dockerfile`:

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags "-s -w" -o /out/butterdbd ./cmd/butterdbd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/butterdbd /usr/local/bin/butterdbd
ENV BUTTERDB_DATA_DIR=/var/lib/butterdb \
    BUTTERDB_SERVER_KEY_FILE=/run/secrets/butterdb/server.key \
    BUTTERDB_OPS_LISTEN=0.0.0.0:9090
VOLUME ["/var/lib/butterdb"]
EXPOSE 9000 9443 9080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/butterdbd"]
CMD ["serve"]
```

Both base images are pinned by digest (`@sha256:…`) in the committed file; the
tags above are for reading. The ops listener binds all interfaces inside the
container so the kubelet can probe it, so `/metrics` needs
`ops.metrics_token_file` or `ops.allow_unauthenticated_metrics = true` behind a
network policy.

Kubernetes shape (phase 11 `deploy/` ships a full manifest):

| Setting | Value | Reason |
| --- | --- | --- |
| Kind | `Deployment` with `replicas: 1` (a `StatefulSet` with one replica is equivalent) | Single instance (§6) |
| `strategy.type` | `Recreate` | A rolling update starts a second pod against the same volume and Telegram account |
| Volume | `PersistentVolumeClaim`, `accessModes: [ReadWriteOncePod]`, local SSD class | One writer; SQLite needs a local filesystem (§3.2) |
| Key | `Secret` mounted at `/run/secrets/butterdb`, `defaultMode: 0440`, pod `securityContext.fsGroup: 65532` | Readable by the process; the group-read warning of §2.5 applies |
| `securityContext` | `runAsNonRoot: true`, `runAsUser: 65532`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile.type: RuntimeDefault` | |
| `startupProbe` | `GET /healthz` on 9090, `failureThreshold: 30`, `periodSeconds: 2` | Migrations may take time |
| `livenessProbe` | `GET /healthz`, `periodSeconds: 10`, `failureThreshold: 3` | |
| `readinessProbe` | `GET /readyz` | |
| Admin `Service` | Separate `Service` for 9443 with `publishNotReadyAddresses: true`, or probe `GET /readyz?listener=admin` | Setup must be reachable while unlinked (§7.3) |
| `terminationGracePeriodSeconds` | `75` | §8 |
| Resources | requests `cpu: 500m`, `memory: 512Mi`; limits `memory: 2Gi` | §13.6 |

The same rules apply to Docker or Compose: one container, `restart: unless-stopped`,
a named local volume, `stop_grace_period: 75s`, and never `docker compose up
--scale`.

### 11.3 Reverse proxy

A proxy is optional. Caddy is the shipped example (`deploy/caddy/Caddyfile`).

| Rule | S3 listener | Admin listener |
| --- | --- | --- |
| Stream request and response bodies | Required; nginx `proxy_request_buffering off; proxy_buffering off;` | Required ([admin-api.md](admin-api.md) §2.4) |
| Body size limit | None (nginx `client_max_body_size 0;`): an aws-chunked body is larger than the object it carries ([s3-api.md](s3-api.md) §2.6) | `1900m` |
| Read timeout | At least `s3.request_budget` plus the slowest Telegram upload of the largest object plus 60 s: about 1,086 s at 2 MB/s; recommended 1,200 s ([s3-api.md](s3-api.md) §2.6) | `admin.upload_commit_budget` + 60 s |
| Request URI | Forwarded byte for byte (nginx `proxy_pass http://127.0.0.1:9000;` with no URI part); the canonical URI is signed | |
| `Host` header | Preserved unchanged; SigV4 signs it and virtual-hosted-style routing reads it | Preserved |
| Client address | `X-Forwarded-For` appended; the proxy's address in `s3.trusted_proxies` | Same with `admin.trusted_proxies` |
| `Expect: 100-continue` | Passed through (AWS SDKs send it on large PUTs) | |
| TLS | Terminated at the proxy; `s3.listen` on loopback | `admin.tls_mode = "proxy"` with loopback `admin.listen`, or TLS passthrough |

**Cloudflare.** The proxied (orange-cloud) plans Free and Pro reject request
bodies above 100 MB with `413`; Business allows 200 MB and Enterprise 500 MB by
default. Cloudflare also returns `524` when the origin does not start a response
within 100 seconds on non-Enterprise plans, and a PUT response in butterDB waits
for the Telegram commit (D8). For uploads through Cloudflare:

- Serve the S3 API for writes from a DNS-only (grey-cloud) hostname, and use a
  proxied hostname only for public-read GETs where caching helps; or
- Set every client's multipart threshold and part size to 64 MiB, so no single
  request body reaches 100 MB. `UploadPart` only stages to disk and answers fast;
  only `CompleteMultipartUpload` waits for Telegram, and it writes its `200`
  status and a whitespace byte every `s3.keepalive_interval`
  ([s3-api.md](s3-api.md) §11), which keeps the 100 s timer from firing.
  A single `PutObject` below the part size still waits for the commit with no
  response byte; it normally commits in seconds, but behind a long flood wait
  Cloudflare answers `524`, which SDKs retry as a server error. Verify the `524`
  behaviour against Cloudflare's current documentation before release.

Client settings for multipart through Cloudflare:

| Client | Setting |
| --- | --- |
| aws-sdk-go-v2 `feature/s3/manager` | `PartSize: 64 << 20` |
| AWS CLI | `aws configure set default.s3.multipart_threshold 64MB` and `multipart_chunksize 64MB` |
| boto3 | `TransferConfig(multipart_threshold=64*1024*1024, multipart_chunksize=64*1024*1024)` |
| rclone | `--s3-upload-cutoff 64M --s3-chunk-size 64M` |

### 11.4 SDK timeouts and retries

Because a PUT is acknowledged only after the Telegram commit (D8), client read
timeouts must cover the server's upload to Telegram, not only the network
transfer. A plain `PutObject` sends no byte back until the commit, so a client's
wait for response headers must exceed the commit budget for the object's size:
20 min 50 s for the largest object with the defaults. Multipart Complete,
CopyObject and UploadPartCopy send a whitespace byte every 15 s, so any idle-read
timeout above 30 s works for them.

[s3-api.md](s3-api.md) §12.3 and §12.4 own the per-SDK settings (25-minute read
timeout, at least 6 attempts in standard retry mode, path-style addressing,
multipart above 16 MiB). Every AWS SDK retries `503 SlowDown` with its own
backoff (A7). The phase 11 guide (`docs/guide/`) copies those settings.

--- | --- |
| boto3 / botocore | `Config(read_timeout=900, retries={"max_attempts": 10, "mode": "adaptive"})`; the default 60 s read timeout fails large single PUTs |
| AWS CLI | `--cli-read-timeout 900`, `retry_mode = adaptive`, `max_attempts = 10` |
| aws-sdk-go-v2 | No overall timeout by default; set `retry.AddWithMaxAttempts(…, 10)` and `retry.AddWithMaxBackoffDelay(…, 60*time.Second)` |
| AWS SDK for JavaScript v3 | `maxAttempts: 10`, `requestHandler` with `requestTimeout: 900000` |

Every AWS SDK retries `503 SlowDown` with backoff and honours `Retry-After` where
supported (A7). The phase 11 guide (`docs/guide/`) copies this table.

---

## 12. Upgrades

| Topic | Rule |
| --- | --- |
| Procedure | `butterdbd snapshot` → stop → replace the binary → `butterdbd check-config` → start → wait for `/readyz` `200` |
| Downtime | Seconds plus migration time (accepted risk) |
| Migrations | Forward only, embedded in the binary, one transaction per file; a snapshot is taken first when `snapshot.before_migrate` |
| Downgrade | Not supported in place. A binary that finds a newer schema exits 5. To downgrade, restore the pre-upgrade snapshot with the old binary; ops written to Telegram since then are re-ingested, and op types the old binary does not know are kept as `unknown_op` rejects (E5) |
| Wire format | New op types are skippable by older binaries (E5). A change that breaks old readers needs a new format version and a decision record entry |
| Admin API | `/admin/v1` changes are additive. The GUI reads `server_version` from `GET /admin/v1/server-info` and refuses a server older than its minimum |
| Transform worker | Upgraded together with the server; `api/transformv1` carries a protocol version and the supervisor refuses a mismatched worker |
| Containers | `Recreate` strategy; never two versions at once |
| Before each release | Re-run `tgprobe` (§13.5) on the dedicated account; Telegram limits change without notice (R17) |

---

## 13. Telegram throughput and capacity

### 13.1 Constants retained from the legacy specification (S7, S16, S28)

| Constant | Value | Effect |
| --- | --- | --- |
| `RangeReadAlignment` | 4 KiB | `upload.getFile` offset alignment |
| `RangeReadMaxBytes` | 1 MiB | Largest `getFile` request; a request never spans a 1 MiB boundary. Also the block-cache block size |
| `MediaPoolSize` | 4 | Pooled connections per data centre |
| `UploadThreads` | 8 | Upload parts in flight per upload |
| `MaxConcurrentGetFile` | 12 | Global `getFile` slots |
| `PlaybackGetFileReserve` | 4 | Slots reserved for foreground reads |
| `MaxConcurrentBackgroundGetFile` | 8 | Slots read-ahead and replica reads may use |
| `DefaultDownloadThreads` | 4 | |
| Telegram upload part size | 512 KiB | A 1900 MiB body is 3,800 parts |
| `MaxPartBytes` (`limits.MaxStoredObjectBytes`) | 1900 MiB | Largest stored object, ciphertext counted |
| Message delete batch | 100 IDs | `channels.deleteMessages` (`limits.DeleteBatchSize`) |
| Write flood-wait retries / max single / total | 2 / 30 s / 60 s | |
| Transient retries / backoff / max / jitter | 4 / 2 s / 30 s / 750 ms | |
| Pool dial backoff | 30 s per DC | |
| Live sync debounce / backstop / pass timeout | 750 ms / 5 min / 2 min | |
| Message text limit | 4096 characters | `limits.MaxPutOpChars` |
| Document caption limit | 1024 characters | Why the key lives in the put message (E2) |
| `messages.getHistory` page | 100 messages | Rebuild cost (§10.4) |

### 13.2 Throughput realities

| Fact | Planning figure | Source |
| --- | --- | --- |
| One MTProto connection after a `FILE_MIGRATE` hop moves file bytes at about 1 MB/s | 1 MB/s per connection | Legacy S7 rationale for `MediaPoolSize` |
| Cold reads with a 4-connection pool | 2–4 MB/s per object stream, about 4 MB/s aggregate per DC | Derived; measure with `tgprobe` |
| Uploads with 8 parts in flight | 2–4 MB/s per upload | Planning; measure with `tgprobe` |
| `FLOOD_WAIT_X` is server-directed and per account | The longest observed wait applies to every write class (S16 write coordinator) | Legacy S16 |
| The E10 limiter, not Telegram, is the steady-state ceiling | 20 control messages/min (burst 5), 6 documents/min (burst 1) | [plan.md](../plan.md) §8.6 |
| A client-side limiter keeps a server that writes around the clock away from the flood-wait escalation pattern that precedes account bans | Never disabled | E10, [security.md](security.md) §14 |

### 13.3 Limiter classes

Default (owner may change), for `internal/tgclient/flow`:

| Class | RPCs | Rate keys |
| --- | --- | --- |
| `message` | `messages.sendMessage` (ops, and pairing codes to Saved Messages), `channels.deleteMessages`, `channels.createChannel` | `control_per_minute`, `control_burst` |
| `document` | `messages.sendMedia` (body and snapshot documents) | `documents_per_minute`, `documents_burst` |
| not limited by E10 | `upload.saveBigFilePart` (bounded by `UploadThreads` and the write coordinator), `upload.getFile`, `messages.getHistory`, `updates.getChannelDifference`, `channels.getMessages`, `messages.getDialogs` | Bounded by their own slot limits and flood-wait handling |

Each account (primary, replica) has its own limiter (two `tgclient` values).

### 13.4 What the limiter allows

| Workload | Telegram calls | Ceiling at defaults |
| --- | --- | --- |
| New object PUT | 1 document + 1 message | 6/min = 360/h = 8,640/day |
| Overwrite PUT | 1 document + 1 message + 1 delete batch | 6/min (document-bound) |
| DeleteObject | 1 message (`t=del`) + 1 delete batch | 10/min (message-bound) |
| DeleteObjects of 1,000 keys | 1,000 tombstones + delete batches | about 1 h |
| Bucket create | 1 `createChannel` + registry messages | seconds |

Consequences at D4 scale:

- 500,000 objects in the first year average about 1,370 PUTs per day, 16 % of
  the default ceiling. Peaks queue in admission and surface as `503 SlowDown`
  past `s3.request_budget` ([s3-api.md](s3-api.md) §12.1).
- A bulk import is limited by the document rate: 100,000 objects take about 11.6
  days at 6/min. Plan migrations in days, or raise the limiter only from
  measured `tgprobe` results (§13.5). Raising it is an owner decision.
- 1 TB per year is about 2.7 GB/day, about 32 KB/s average: bandwidth is not the
  constraint; document count is.

### 13.5 `butterdbd tgprobe`

Owner-run only, against the dedicated account, with the server stopped (it takes
the process lock and uses the configured session).

```
butterdbd tgprobe --out tgprobe-<YYYYMMDD>.json [--steps control,upload,download,history,delete] [--yes]
```

Without `--yes` it prints the plan and the account (user ID and masked phone)
and asks for confirmation on the TTY.

| Step | Procedure | Stops at |
| --- | --- | --- |
| setup | Creates or reuses a private broadcast channel `butterDB probe` (row `probe` in `ops_channels`, §3.3) | |
| `control` | Sends 200-character text messages, bypassing the E10 limiter for this step only, at 1 per 6 s for 2 min, then 1 per 3 s, 1 per 2 s, 1/s, 2/s, each for 2 min | The first `FLOOD_WAIT`, or 300 messages. It never waits out a flood wait and retries |
| `upload` | Sends three 64 MiB random documents, then one 1 GiB document, `ForceFile`, through the normal document path (limiter on) | Any flood wait longer than 60 s |
| `download` | Reads 64 MiB of the 1 GiB document with 1, 4, 8 and 12 concurrent `getFile` requests of 1 MiB | |
| `history` | Pages the probe channel with `messages.getHistory` (limit 100) as fast as responses arrive, up to 200 calls | The first `FLOOD_WAIT` |
| `delete` | Deletes the probe messages in batches of 100 and records latency | |
| cleanup | Deletes every probe message; the empty channel stays for the next run | |

Report shape:

```json
{
  "probe_version": 1,
  "binary_version": "0.1.0",
  "started_at": "…",
  "finished_at": "…",
  "account_user_id": 777000123,
  "home_dc": 2,
  "steps": {
    "control": {"sent": 64, "first_flood_wait_seconds": 27, "rate_at_flood_per_minute": 30, "sustained_rate_per_minute": 20},
    "upload": {"runs": [{"bytes": 67108864, "seconds": 21.4, "mb_per_s": 3.1}], "flood_waits": []},
    "download": {"runs": [{"concurrency": 4, "mb_per_s": 3.8}]},
    "history": {"calls": 200, "seconds": 96.2, "calls_per_s": 2.08, "first_flood_wait_seconds": null},
    "delete": {"batches": 3, "p50_ms": 180, "p95_ms": 420}
  },
  "recommended": {"control_per_minute": 15, "documents_per_minute": 6}
}
```

Sizing rule: `recommended.control_per_minute` is half the rate at which the first
flood wait occurred, never above the highest rate sustained without one;
`documents_per_minute` is the minimum of the current value and the measured
upload throughput divided into the average object size, capped at half of any
document-rate flood. The probe never writes the configuration; the owner copies
the values. Results describe one account at one time ([plan.md](../plan.md) §8.7).

### 13.6 Host sizing for D4

| Resource | Minimum | Recommended | Basis |
| --- | --- | --- | --- |
| CPU | 1 vCPU | 2 vCPU | SigV4, MD5 plus SHA-256 hashing of every PUT, XChaCha20-Poly1305 at hundreds of MB/s per core; phase 13 adds transforms (+2 vCPU) |
| Memory | 1 GiB | 2 GiB (+1–2 GiB with the transform worker) | Go heap, SQLite page caches for one writer and the read pool on two databases, 1 MiB `getFile` buffers × 12, 512 KiB upload parts × 8 per put slot |
| Disk, data filesystem | 50 GiB SSD | 150 GiB SSD | Databases 1.5 GB plus WAL, `spool.max_bytes` 8 GiB, `spool.multipart_max_bytes` 16 GiB, local snapshots 7 × 0.7 GB, block cache |
| Block cache | 20 GiB | 5–10 % of stored bytes (50–100 GiB at 1 TB) | Hit ratio target ≥ 0.9; cold reads are limited to a few MB/s (§13.2) |
| Network | 20 Mbit/s | 100 Mbit/s | 2,000 daily users × 50 requests × 200 KB is about 20 GB/day served (about 2 Mbit/s average, 10× peak); misses add the same again towards Telegram |
| File descriptors | 4,096 | 65,536 | Listeners, MTProto pools, cache files |

A CDN in front of public-read buckets (A8) removes most repeated reads; private
objects are served through presigned URLs and cannot be shared-cached.

---

## 14. Logging

### 14.1 Format

`log/slog` with the JSON handler on stdout (journald and container runtimes
collect it). Time is RFC 3339 UTC with milliseconds. Built by
`internal/platform/logging`:

```go
package logging

type Secret string

func (Secret) LogValue() slog.Value

func New(w io.Writer, cfg config.LogConfig) *slog.Logger
func ReplaceAttr(groups []string, a slog.Attr) slog.Attr
```

`Secret.LogValue` returns the string `[redacted]`. `ReplaceAttr` applies the
key denylist of §14.4 to every record.

### 14.2 Standard fields

| Field | Type | Present on |
| --- | --- | --- |
| `time`, `level`, `msg` | | every line |
| `component` | string (`s3api`, `adminapi`, `mediaapi`, `ingest`, `readpath`, `syncer`, `tgclient`, `replica`, `snapshot`, `lifecycle`, `keyring`, `transformd` for re-logged worker lines) | every line from a component logger |
| `request_id` | 16 lowercase hex characters, also returned as `x-amz-request-id` (S3) and `X-Request-Id` | request-scoped lines |
| `listener` | `s3`, `admin`, `media`, `ops` | access lines |
| `method`, `operation`, `status`, `error_code` | | access lines; `operation` is the S3 operation name (`PutObject`) or the admin route pattern |
| `bucket`, `key` | | object operations; `key` per `log.object_keys` |
| `bytes_in`, `bytes_out`, `duration_ms` | int | access lines |
| `remote_addr` | client address after trusted-proxy resolution | access lines |
| `access_key_id` | | S3 requests that authenticated |
| `device_id` | | admin requests that authenticated |
| `job_id`, `channel_id`, `msg_id` | | write path and sync lines |
| `account` | `primary`, `replica` | Telegram lines |
| `tg_method`, `flood_wait_s`, `dc` | | Telegram lines |
| `err` | error string after redaction | failures |

Access line example:

```json
{"time":"2026-11-13T09:41:07.312Z","level":"INFO","msg":"request","component":"s3api","listener":"s3","request_id":"9f2c41d07ab36e15","method":"PUT","operation":"PutObject","bucket":"photos","key":"2026/11/cat.jpg","status":200,"bytes_in":734003,"bytes_out":0,"duration_ms":8412,"remote_addr":"203.0.113.7","access_key_id":"BDBK7Q2M9X4T1W8Z3R6Y"}
```

### 14.3 Levels

| Level | Use |
| --- | --- |
| `ERROR` | Lost or at-risk data, a refused start, an integrity failure (CDN hash mismatch, TDE1 authentication failure, tamper evidence) |
| `WARN` | Flood waits longer than 30 s, `SlowDown` responses (sampled to 1 per 10 s per reason), uncertain jobs, key generation, setup tokens ([admin-api.md](admin-api.md) §5.2), group-readable secret files |
| `INFO` | Start and stop, state changes, listener addresses and fingerprints, access lines, snapshots |
| `DEBUG` | RPC-level detail; never enabled by default; still redacted |

### 14.4 Redaction rules

[security.md](security.md) §13 gives the rationale. These are the mechanics.

| Never logged | How it is prevented |
| --- | --- |
| Server key, DEKs, derived keys, `api_hash`, session contents, login codes, 2FA password, access-key secrets, device tokens, pairing codes after issue | Held only in types whose `LogValue` redacts (`logging.Secret`, `keyring.Key`); never placed in error strings |
| `Authorization` header | Access lines log `access_key_id` only; the header is never logged |
| Query parameters `X-Amz-Signature`, `X-Amz-Credential`, `X-Amz-Security-Token` | Request URLs are logged as path only; the query string is never logged |
| Media signature segment `s--<sig>--` | Logged as `s--…--` ([media-api.md](media-api.md) §20.2) |
| `Cookie`, `Set-Cookie`, `x-amz-server-side-encryption-customer-key` | Headers are never logged except an allowlist: `User-Agent`, `Content-Type`, `Content-Length`, `Range`, `x-amz-content-sha256` (value class only) |
| Full phone numbers | Masked to country code plus last three digits (`+44*******134`) |
| Object contents and user metadata values | Never logged; metadata keys may be |

`ReplaceAttr` also drops any attribute whose key, lower-cased, is one of
`password`, `secret`, `token`, `api_hash`, `authorization`, `signature`,
`session`, `code`, `key_material`, replacing the value with `[redacted]`, as a
backstop for mistakes. The exceptions are the setup-token line
([admin-api.md](admin-api.md) §5.2), written with the field `setup_token`
deliberately, and `error_code`.

A test drives a full put, presign, admin login relay and pairing through the
composed engine with a buffer logger and fails if any secret value appears in
the output ([security.md](security.md) §16).

---

## 15. Metrics

### 15.1 Exposition

`GET /metrics` on the ops listener, Prometheus text format, served by
`github.com/prometheus/client_golang` with the Go and process collectors.
Default (owner may change). When `ops.metrics_token_file` is set the request
needs `Authorization: Bearer <token>`, compared in constant time. Every metric is
registered once in `internal/ops/metrics`; producers depend on small ports they
declare (for example `PutObserver`), and `internal/core` passes the metrics
adapter.

Label rules: no label carries an object key, an access-key ID, a device ID, an IP
address or a request ID. `bucket` appears only on the sync and usage metrics
named below; at most 100 buckets keep it bounded. `operation` takes the S3
operation names and the admin route patterns (`POST /admin/v1/buckets`); an
unmatched route is `unknown`. The media listener has its own metrics (below)
and is not counted in `butterdb_http_*`.

### 15.2 Every metric

HTTP:

| Metric | Type | Labels |
| --- | --- | --- |
| `butterdb_http_requests_total` | counter | `listener`, `operation`, `code` (HTTP status) |
| `butterdb_http_request_duration_seconds` | histogram (0.005 … 600 s) | `listener`, `operation`, `code` |
| `butterdb_http_requests_in_flight` | gauge | `listener` |
| `butterdb_http_request_bytes_total` | counter | `listener`, `operation` |
| `butterdb_http_response_bytes_total` | counter | `listener`, `operation` |
| `butterdb_s3_errors_total` | counter | `operation`, `error_code` (S3 code, e.g. `SlowDown`, `AccessDenied`) |
| `butterdb_s3_auth_failures_total` | counter | `reason` (`signature`, `expired`, `skew`, `unknown_key`, `revoked_key`, `scope`) |
| `butterdb_admin_auth_failures_total` | counter | `scope` |
| `butterdb_rate_limited_total` | counter | `listener`, `scope` |

Telegram:

| Metric | Type | Labels |
| --- | --- | --- |
| `butterdb_telegram_connected` | gauge 0/1 | `account` |
| `butterdb_telegram_rpc_total` | counter | `account`, `method` (TL name, e.g. `messages.sendMedia`), `result` (`ok`, `flood_wait`, `transient`, `outcome_unknown`, `error`) |
| `butterdb_telegram_rpc_duration_seconds` | histogram | `account`, `method` |
| `butterdb_telegram_flood_waits_total` | counter | `account`, `method` |
| `butterdb_telegram_flood_wait_seconds` | histogram (1 … 86400 s) | `account`, `method` |
| `butterdb_telegram_flood_wait_remaining_seconds` | gauge | `account` (write coordinator cooldown left) |
| `butterdb_telegram_send_queue_depth` | gauge | `account`, `class` (`message`, `document`) |
| `butterdb_telegram_send_wait_seconds` | histogram | `account`, `class` (time spent waiting for the limiter and coordinator) |
| `butterdb_telegram_send_rate_limit_per_minute` | gauge | `account`, `class` |
| `butterdb_telegram_upload_bytes_total` | counter | `account` |
| `butterdb_telegram_download_bytes_total` | counter | `account`, `source` (`dc`, `cdn`) |
| `butterdb_telegram_getfile_slots_in_use` | gauge | `account`, `class` (`foreground`, `background`) |
| `butterdb_telegram_file_reference_refreshes_total` | counter | `account` |
| `butterdb_telegram_cdn_hash_failures_total` | counter | `account` |

Write path and spool:

| Metric | Type | Labels |
| --- | --- | --- |
| `butterdb_put_jobs_total` | counter | `outcome` (`committed`, `aborted`, `slowdown`, `uncertain_body`, `uncertain_put`) |
| `butterdb_put_jobs_in_flight` | gauge | |
| `butterdb_put_commit_seconds` | histogram | (spool complete to projection applied) |
| `butterdb_jobs_uncertain` | gauge | |
| `butterdb_cleanup_pending_messages` | gauge | (superseded and deleted messages queued for `deleteMessages`) |
| `butterdb_slowdown_total` | counter | `reason` (`flood_wait`, `admission`, `spool_full`, `telegram_down`) |
| `butterdb_spool_used_bytes` | gauge | |
| `butterdb_spool_reserved_bytes` | gauge | |
| `butterdb_spool_limit_bytes` | gauge | |
| `butterdb_multipart_staged_bytes` | gauge | |
| `butterdb_multipart_uploads_open` | gauge | |
| `butterdb_filesystem_free_bytes` | gauge | `path` (`data`, `cache`, `snapshot`) |

Read path and caches:

| Metric | Type | Labels |
| --- | --- | --- |
| `butterdb_block_cache_requests_total` | counter | `result` (`hit`, `miss`) |
| `butterdb_block_cache_hit_ratio` | gauge | (hits / requests over the last hour; same value as `hit_ratio_1h` in `GET /admin/v1/usage`) |
| `butterdb_block_cache_bytes` | gauge | |
| `butterdb_block_cache_limit_bytes` | gauge | |
| `butterdb_block_cache_evictions_total` | counter | |
| `butterdb_block_cache_fill_bytes_total` | counter | `source` (`telegram`, `put_warm`, `replica`) |
| `butterdb_readahead_blocks_total` | counter | |
| `butterdb_read_auth_failures_total` | counter | (TDE1 chunk authentication failed after response headers were sent; the connection is aborted) |
| `butterdb_key_errors_total` | counter | `reason` (`unwrap`, `check`): a bucket DEK failed to unwrap or verify ([storage-model.md](storage-model.md) §8.3) |

Sync and projection:

| Metric | Type | Labels |
| --- | --- | --- |
| `butterdb_sync_lag_seconds` | gauge | `bucket` (`_registry` for the registry channel): seconds since the channel last reached `Final` in a difference pass |
| `butterdb_sync_last_success_timestamp_seconds` | gauge | `bucket` |
| `butterdb_sync_passes_total` | counter | `kind` (`difference`, `history`), `result` (`ok`, `error`) |
| `butterdb_projection_ops_applied_total` | counter | `op` (`body`, `put`, `del`, `reg`, `bcreate`, `bcfg`, `bdel`, `encfg`, `unknown`; [wire-format.md](wire-format.md) §3) |
| `butterdb_projection_rejects_total` | counter | `reason` (includes `unknown_op`) |
| `butterdb_projection_tamper_total` | counter | |
| `butterdb_projection_key_repairs_total` | counter | |
| `butterdb_objects` | gauge | `bucket` (from the usage cache, refreshed every 30 s) |
| `butterdb_stored_bytes` | gauge | `bucket` |
| `butterdb_sqlite_write_wait_seconds` | histogram | `db` (`projection`, `control`) |
| `butterdb_sqlite_file_bytes` | gauge | `db`, `file` (`main`, `wal`) |

Process, snapshot, replica:

| Metric | Type | Labels |
| --- | --- | --- |
| `butterdb_build_info` | gauge 1 | `version`, `commit`, `go_version` |
| `butterdb_state` | gauge 1 for the current state | `state` |
| `butterdb_ready` | gauge 0/1 | |
| `butterdb_snapshot_last_success_timestamp_seconds` | gauge | |
| `butterdb_snapshot_duration_seconds` | histogram | |
| `butterdb_snapshot_bytes` | gauge | |
| `butterdb_snapshot_failures_total` | counter | `stage` (`vacuum`, `seal`, `prune`) |
| `butterdb_snapshot_upload_failures_total` | counter | `reason` (`telegram`, `too_large`) |
| `butterdb_replica_lag_ops` | gauge | (phase 12; name fixed by [plan.md](../plan.md) §6.7) |
| `butterdb_replica_lag_seconds` | gauge | (phase 12; age of the oldest pending journal row) |

Media (phase 13), owned by [media-api.md](media-api.md) §20.1 and listed here so
this table names every metric:

| Metric | Type | Labels |
| --- | --- | --- |
| `butterdb_media_requests_total` | counter | `status` (HTTP code), `resource` |
| `butterdb_media_request_seconds` | histogram | `outcome` (`hit`, `miss`, `error`) |
| `butterdb_media_cache_hits_total` | counter | `tier` (`disk`, `memory`) |
| `butterdb_media_cache_misses_total` | counter | |
| `butterdb_media_cache_bytes` | gauge | `tier` |
| `butterdb_media_cache_evictions_total` | counter | `tier` |
| `butterdb_media_transform_seconds` | histogram | `resource`, `format` |
| `butterdb_media_worker_inflight` | gauge | |
| `butterdb_media_worker_queue` | gauge | |
| `butterdb_media_worker_restarts_total` | counter | `reason` (`exit`, `health`, `watchdog`) |
| `butterdb_media_worker_up` | gauge | |
| `butterdb_media_quarantined` | gauge | |
| `butterdb_media_signature_failures_total` | counter | `code` |

### 15.3 Useful queries and alerts

Cache hit ratio over 5 minutes:

```
sum(rate(butterdb_block_cache_requests_total{result="hit"}[5m]))
  / sum(rate(butterdb_block_cache_requests_total[5m]))
```

S3 p95 latency per operation:

```
histogram_quantile(0.95, sum by (le, operation) (rate(butterdb_http_request_duration_seconds_bucket{listener="s3"}[5m])))
```

Suggested alerts (shipped as `deploy/prometheus/alerts.yml` in phase 11):

| Alert | Expression | For |
| --- | --- | --- |
| `ButterdbNotReady` | `butterdb_ready == 0` | 10m |
| `ButterdbTelegramDisconnected` | `butterdb_telegram_connected{account="primary"} == 0` | 5m |
| `ButterdbLongFloodWait` | `butterdb_telegram_flood_wait_remaining_seconds > 300` | 1m |
| `ButterdbSyncLag` | `max(butterdb_sync_lag_seconds) > 900` | 10m |
| `ButterdbUncertainJobs` | `butterdb_jobs_uncertain > 0` | 1h |
| `ButterdbSpoolNearlyFull` | `butterdb_spool_reserved_bytes / butterdb_spool_limit_bytes > 0.8` | 10m |
| `ButterdbDiskLow` | `butterdb_filesystem_free_bytes{path="data"} < 5e9` | 5m |
| `ButterdbSnapshotStale` | `time() - butterdb_snapshot_last_success_timestamp_seconds > 129600` | 0m |
| `ButterdbIntegrity` | `increase(butterdb_telegram_cdn_hash_failures_total[1h]) > 0 or increase(butterdb_projection_tamper_total[1h]) > 0 or increase(butterdb_read_auth_failures_total[1h]) > 0 or increase(butterdb_key_errors_total[1h]) > 0` | 0m |
| `ButterdbReplicaLag` | `butterdb_replica_lag_seconds > 3600` | 15m |

---

## 16. Operator checklist

| When | Do |
| --- | --- |
| Before first start | Dedicated aged Telegram account with 2FA ([security.md](security.md) §14); `setup --key-only`; key backed up off-host twice; `check-config` clean |
| At first start | Record the server key fingerprint and the admin TLS fingerprint from the log |
| After linking | Enable `snapshot.upload_to_telegram` or copy `snapshot.dir` off-host daily |
| Monthly | `restore --verify-only` against an off-host snapshot with the off-host key |
| Before each release upgrade | Snapshot, `tgprobe`, read the release notes for migrations |
| On a long flood wait | Do nothing; the server honours it. Do not restart repeatedly or log in from other clients to "test" |
