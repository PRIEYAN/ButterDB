# Phase 11 — Operations and first release (v0.1.0, local tag only)
> Orchestrator: Opus, high effort. Workers: butterdb-lane-worker (Sonnet, high). Execute per docs/phases/00-execution-protocol.md.

## 1. Goal

Implement operations and first release (v0.1.0, local tag only) as the next strictly sequential butterDB phase. Deliver the owned packages below, wire them through the composition root, and prove the listed acceptance cases. The authoritative behavior lives in the linked specifications and decisions; this handoff fixes task ownership and verification.

## 2. Entry criteria

Phase 10 has passed its exit criteria and is merged locally. Read its status note before changing contracts.
Use the owner gates in section 8 before moving main.

## 3. Read before starting

- `docs/claude.md`, `docs/decisions.md`, `docs/plan.md`, `docs/phases/00-execution-protocol.md`.
- `docs/spec/operations.md`.
- `docs/spec/s3-api.md`.
- Read the matching TDrive entries in `docs/reference/tdrive-port-map.md`; verify each source path exists in the TDrive checkout before citing or porting it.



## 4. Prelude (orchestrator)

Commit health, snapshot, and restore ports before fan-out. The exact contracts and procedures are in `docs/spec/operations.md` §§7, 9–11 and 15. Keep deployment files and operator guides separate from server packages. Commit as `define operations contracts`.

## 5. Lanes

| Lane | Worktree branch | Exclusive owned paths and task |
|---|---|---|
| L1 | `phase/11-operations-and-first-release-l1` | internal/ops/{health,metrics} |
| L2 | `phase/11-operations-and-first-release-l2` | internal/ops/snapshot |
| L3 | `phase/11-operations-and-first-release-l3` | deploy/ (Dockerfile distroless, systemd unit, compose, Caddy example) |
| L4 | `phase/11-operations-and-first-release-l4` | test/s3compat |
| L5 | `phase/11-operations-and-first-release-l5` | test/load |
| L6 | `phase/11-operations-and-first-release-l6` | docs/guide/ (self-hosting guide, runbook, SDK configuration, Telegram account safety) |

Each worker receives only its lane brief, `docs/claude.md`, the relevant specification, and the committed prelude. Workers may edit tests beside their owned source files; they must not edit another lane, `internal/core`, `cmd/`, `go.mod`, or `go.sum`. Build with `-mod=readonly` and report a missing module instead of adding it.

### 5.1 Lane L1 — internal/ops/{health,metrics}

Implement internal/ops/{health,metrics}. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Verify every command, configuration key, flag, and endpoint against
`docs/spec/operations.md` and `docs/spec/s3-api.md`. Check all local links and
run the guide's SDK example against the fake-backed acceptance server. The
integration lane owns that executable example test.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/media/throughput_meter.go` | 131 | PATTERN-ONLY | `internal/ops/metrics` (L1) | Sliding-window bytes per second in time buckets with an injectable clock, plus flood-wait notes |
| `internal/app/backend_startup.go` | 69 | PATTERN-ONLY | `internal/ops/health` (L1) | Readiness reported only after startup finishes or fails |

Commit this lane as `implement phase 11 internal ops health metrics` after its package tests pass.

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/media/throughput_meter.go`: `type ThroughputStats struct {`; `func (m *throughputMeter) Add(n int) {`; `func (m *throughputMeter) NoteFloodWait(d time.Duration) {`; `func (m *throughputMeter) Stats() ThroughputStats {`
- `internal/app/backend_startup.go`: `func (a *App) WaitForBackendReady(ctx context.Context) error {`

### 5.2 Lane L2 — internal/ops/snapshot

Implement internal/ops/snapshot. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 11 internal ops snapshot` after its package tests pass.

### 5.3 Lane L3 — deploy/ (Dockerfile distroless, systemd unit, compose, Caddy example)

Implement deploy/ (Dockerfile distroless, systemd unit, compose, Caddy example). Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 11 deploy dockerfile distroless systemd unit com` after its package tests pass.

### 5.4 Lane L4 — test/s3compat

Implement test/s3compat. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 11 test s3compat` after its package tests pass.

### 5.5 Lane L5 — test/load

Implement test/load. Follow the exact protocol, schema, and limits in section 3. For a TDrive port, retain tested behavior, adapt it to bucket and key objects, remove unused desktop or folder behavior, and record attribution in `docs/legal/THIRD_PARTY_NOTICES.md`. Keep dependencies behind consumer-declared interfaces; construct adapters only in `internal/core`.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `implement phase 11 test load` after its package tests pass.

### 5.6 Lane L6 — docs/guide/ (self-hosting guide, runbook, SDK configuration, Telegram account safety)

Write `docs/guide/self-hosting.md`, `docs/guide/runbook.md`,
`docs/guide/sdk-configuration.md`, and
`docs/guide/telegram-account-safety.md`. The first guide walks through
server setup, private data directory, reverse proxy, certificate and
fingerprint, first admin pairing, and bucket/key creation. The runbook covers
readiness failures, drain, snapshot verification, restore, rebuild from
Telegram, and loss of the server key. The SDK guide gives a working
`aws-sdk-go-v2` endpoint and path-style configuration, PUT/GET/list/delete
examples, timeouts, `503 SlowDown` retries, multipart limits, and documented
S3 deviations. The account-safety guide explains owner-supplied credentials,
send-rate limits, flood waits, 2FA, account loss, and replica limits. Keep
secrets, setup tokens, and real account identifiers out of examples.

Write table tests for valid, invalid, boundary, retry, and restart cases that apply to this package. Run `go test -mod=readonly` and `go build -mod=readonly` on owned packages. Commit only owned paths with a one-line lowercase message; report exported contracts and test evidence to the orchestrator.

Commit this lane as `write self hosting and operations guides` after review.

## 6. Integration lane

cmd snapshot/restore/rebuild wiring, readiness gates, test/acceptance/phase11, release checklist. Owner gates: real-account probe, tag.
Own `internal/core`, `cmd/`, and `test/acceptance/phase11` in the root module. Cherry-pick accepted lanes in numeric order, resolve wiring against the prelude contracts, then run `go mod tidy` once. The integration commit must include any cross-package adaptation and the acceptance suite.

## 7. Verification

- `go test -mod=readonly ./test/acceptance/phase11/...` after integration.
- `CGO_ENABLED=0 go build -mod=readonly ./cmd/butterdbd` for root-module phases; phase 10 and 13 also build their own app module with its platform dependencies.
- `go test -mod=readonly ./...` in each affected module.
- Run `scripts/verify.sh`, including layering and comment policy checks, when present.
- Acceptance cases:
  - `TestReadinessFailsOnBrokenDependencies`: readiness fails on broken dependencies.
  - `TestSnapshotRestoreAndRebuildPreserveState`: snapshot restore and rebuild preserve state.
  - `TestS3CompatibilitySuitePasses`: S3 compatibility suite passes.
  - `TestBoundedLoadKeepsQueuesAndCacheHealthy`: bounded load keeps queues and cache healthy.
### TDrive regression tests to adapt

| TDrive test file | Lines | Carry | Cases |
| --- | ---: | --- | --- |
| `backend/media/throughput_meter_test.go` | 150 | Pattern | Window rollover with a fake clock |

## 8. Owner gates

The owner runs the real-account probe and approves the local v0.1.0 tag; do not tag before that evidence exists.

## 9. Exit criteria and handoff

All L1–L6 commits have passed monitoring and review; the integration build and phase 11 acceptance tests pass. Write the phase status note using the template in `docs/phases/00-execution-protocol.md`. Fast-forward main locally only after owner gates close. Record any changed exported contract for phase 12.

## 10. Risks and pitfalls

- Telegram operations can have uncertain outcomes; preserve stable idempotency IDs and reconcile before retrying.
- Never hold a SQLite transaction across network I/O; project only committed control operations.
- Treat local cache and local-only control state separately from Telegram-backed authority.
- Keep phase ownership disjoint so cherry-picks remain reviewable; report any needed cross-lane contract change to the orchestrator.

## Binding contracts for this phase

The source specification is authoritative. Update this copy in the same commit if the source changes.

### Contract source: `docs/spec/operations.md`

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

### Contract source: `docs/spec/operations.md`

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
devices re-pin; see [security.md](../spec/security.md) §7), `spool/`, `multipart/`, `run/`,
caches. Default (owner may change).

### 9.2 Format (`bdsnap1`)

| Layer | Content |
| --- | --- |
| File name | `butterdb-<server_id>-<YYYYMMDDTHHMMSSZ>.bdsnap` |
| Outer | TDE1 container ([security.md](../spec/security.md) §4) under `K_snapshot = HKDF-SHA256(ikm = server key, salt = empty, info = "butterdb/keyring/snapshot/v1")`, 32 bytes ([security.md](../spec/security.md) §5.2) |
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
| `butterdbd snapshot` with a server running | Sends `{"version":1,"command":"snapshot"}` on the control socket; the running server takes it and answers `{"version":1,"ok":true,"kind":"snapshot","path":"…","bytes":123,"uploaded":true}`. Default (owner may change): the control socket of [admin-api.md](../spec/admin-api.md) §5.5 gains this command |
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
| Send rules | As every body ([storage-model.md](../spec/storage-model.md) §2.2): `inputMediaUploadedDocument{force_file: true}`, `silent: true`, through the E10 `document` limiter and the write coordinator |
| Size | A sealed snapshot above 1900 MiB is not uploaded; a `WARN` line and `butterdb_snapshot_upload_failures_total{reason="too_large"}` record it |
| Retention | After a successful upload, older snapshot messages beyond `snapshot.telegram_retain` are deleted with `channels.deleteMessages` |
| Rediscovery | After host loss, `restore --from-telegram` finds the channel by title among the account's own broadcast channels, then the newest caption whose fingerprint equals the loaded key's |

The ops channel is the only off-host copy of `control.db` that butterDB makes
by itself. It is encrypted under the server key; without the key file it is
useless ([security.md](../spec/security.md) §5.3).

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
