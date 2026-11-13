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

**Objective.** Expose liveness, readiness and metrics: the server state machine behind `/healthz` and `/readyz`, the serving gate, and every Prometheus metric the specs name, on the loopback ops listener.

**Owned paths.** `internal/ops/health/`, `internal/ops/metrics/`.

**Read.** `docs/spec/operations.md` §7.2–§7.5 (states, endpoints, serving gate, the §7.5 Go contract), §15.1–§15.3 (exposition, every metric, alerts); `docs/spec/security.md` §16 (`TestMetricsTokenRequired`, `TestHostHeaderRebinding`).

Implement the operations §7.5 contract exactly. `metrics` exposes a registry constructor plus typed recorders for each §15.2 metric family; packages record through narrow ports they declare. Add `github.com/prometheus/client_golang/prometheus` and `.../promhttp` by exact path in the prelude, or write the text exposition format by hand. Choose one and record it.

**Rules.**
- `/healthz` is liveness only. `/readyz` follows the §7.3 table. Readiness never depends on the media worker.
- The ops listener defaults to loopback. It requires the metrics bearer token when configured and rejects foreign `Host` headers.
- Metric names, types and labels match §15.2 exactly. Labels never carry keys, tokens or object names.

Verify every command, configuration key, flag, and endpoint against
`docs/spec/operations.md` and `docs/spec/s3-api.md`. Check all local links and
run the guide's SDK example against the fake-backed acceptance server. The
integration lane owns that executable example test.

Verified source instructions for this lane:

| TDrive file | Lines | Verdict | Destination | Keep, change, and drop |
|---|---:|---|---|---|
| `backend/media/throughput_meter.go` | 131 | PATTERN-ONLY | `internal/ops/metrics` (L1) | Sliding-window bytes per second in time buckets with an injectable clock, plus flood-wait notes |
| `internal/app/backend_startup.go` | 69 | PATTERN-ONLY | `internal/ops/health` (L1) | Readiness reported only after startup finishes or fails |

TDrive exported declarations to compare while defining the butterDB adapter (the destination signatures must follow the butterDB spec):

- `backend/media/throughput_meter.go`: `type ThroughputStats struct {`; `func (m *throughputMeter) Add(n int) {`; `func (m *throughputMeter) NoteFloodWait(d time.Duration) {`; `func (m *throughputMeter) Stats() ThroughputStats {`
- `internal/app/backend_startup.go`: `func (a *App) WaitForBackendReady(ctx context.Context) error {`

**Tests (binding names).** `TestReadinessStates` (every §7.2 state), `TestHealthzAlwaysLive`, `TestServingGate`, `TestEveryMetricRegistered` (table over §15.2), `TestNoHighCardinalityLabels`, `TestMetricsTokenRequired`, `TestHostHeaderRebinding`.

**Verify.**

```bash
go vet -mod=readonly ./internal/ops/health/... ./internal/ops/metrics/...
go test -mod=readonly -race ./internal/ops/health/... ./internal/ops/metrics/...
gofmt -l internal/ops/health internal/ops/metrics
```

**Commits.** `add health and readiness endpoints`, `add prometheus metrics`.

### 5.2 Lane L2 — internal/ops/snapshot

**Objective.** Take, seal, prune, upload, verify and restore `bdsnap1` snapshots of `projection.db`, `control.db` and `state.json`.

**Owned paths.** `internal/ops/snapshot/`, `internal/storage/opsrepo/` (the new snapshot-index repository over the `snapshots` and `ops_channels` tables, `docs/spec/schema.md` §5.10 and §6; it implements `Records`).

**Read.** `docs/spec/operations.md` §3.3 (`ops_channels`), §9.1–§9.6, §10.2, §10.5; `docs/spec/security.md` §5.2 (`K_snapshot`) and §12.3 (archive allowlist); `docs/spec/schema.md` (`snapshots`, `ops_channels`).

**Contracts.** Implement the operations §9.3 contract and add restore and verify:

```go
package snapshot

type Vacuumer interface {
	VacuumInto(ctx context.Context, database string, dstPath string) error
}

type Sealer interface {
	Seal(dst io.Writer, src io.Reader, plaintextSize int64) error
	Open(src io.ReaderAt, storedSize int64) (io.Reader, error)
}

type Uploader interface {
	UploadSnapshot(ctx context.Context, name string, size int64, body io.ReadSeeker, caption string, randomID int64) (msgID int64, err error)
	DeleteSnapshotMessages(ctx context.Context, msgIDs []int64) error
	LatestSnapshot(ctx context.Context, fingerprint string, name string) (io.ReadCloser, int64, error)
}

type Dependencies struct {
	Vacuum      Vacuumer
	Seal        Sealer
	Upload      Uploader
	Records     Records
	FS          fsx.FS
	Clock       clock.Clock
	Config      Config
	Fingerprint string
	ServerID    string
}

type Records interface {
	Insert(ctx context.Context, r Record) error
	List(ctx context.Context) ([]Record, error)
	Delete(ctx context.Context, path string) error
}

type Result struct {
	Path     string
	Bytes    int64
	SHA256   [32]byte
	Uploaded bool
}

type Manifest struct {
	Format            string
	CreatedAt         time.Time
	ServerID          string
	BinaryVersion     string
	Schema            map[string]int
	KEKFingerprint    string
	DeploymentID      string
	RegistryChannelID int64
	Members           []Member
}

type Member struct {
	Name   string
	Bytes  int64
	SHA256 string
}

func New(deps Dependencies) (*Service, error)
func (s *Service) Create(ctx context.Context) (Result, error)
func (s *Service) Verify(ctx context.Context, path string) (Manifest, error)
func (s *Service) Restore(ctx context.Context, src io.ReaderAt, size int64, dataDir string) (Manifest, error)
func (s *Service) Prune(ctx context.Context) (int, error)
```

`Sealer` is satisfied by a phase 03 `crypto/stream` adapter keyed with `keyring.Ring.SnapshotKey()`. `Uploader` is satisfied by a `tgclient` adapter that the integration lane builds. Declare both here.

**Rules.**
- Member order is exactly `manifest.json`, `state.json`, `control.db`, `projection.db`. Every member is a regular file with mode `0600`. The archive is gzip level 1 over USTAR tar.
- Assemble in `snapshot.dir/.tmp-<id>/` (mode `0700`), seal into the final file, rename, then remove the temp directory, including on failure.
- The upload caption is `bdsnap1|<file>|<sha256>|<fingerprint>`, which is not TDX1. The random ID is `StableRandomID(<snapshot id>, "snap")`. Sealed files above 1900 MiB are never uploaded; increment `butterdb_snapshot_upload_failures_total{reason="too_large"}`.
- Restore and verify enforce the §9.2 allowlist, member hashes, the fingerprint, sizes up to `2 × manifest total`, and `PRAGMA integrity_check` (through a port), and they refuse a newer schema. Write into `tmp/` and rename into `data_dir`, with `state.json` last.
- No SQLite transaction is held while uploading.

**Tests (binding names).** `TestCreateMemberOrder`, `TestManifestHashes`, `TestSealedWithSnapshotKey`, `TestTempDirRemovedOnFailure`, `TestPruneKeepsRetain`, `TestUploadCaptionAndRandomID`, `TestTooLargeNotUploaded`, `TestVerifyWrongKeyFails`, `TestRestoreRejectsExtraMember`, `TestRestoreRejectsSymlink`, `TestRestoreRejectsSizeMismatch`, `TestRestoreFingerprintMismatch`, `TestRestoreRefusesNewerSchema`, `TestRestoreWritesStateLast`.

**Verify.**

```bash
go test -mod=readonly -race ./internal/ops/snapshot/...
gofmt -l internal/ops/snapshot
```

**Commits.** `add encrypted snapshot creation and pruning`, `add snapshot verify restore and ops channel upload`.

### 5.3 Lane L3 — deploy/ (Dockerfile distroless, systemd unit, compose, Caddy example)

**Objective.** Ship the deployment artifacts for the server: the systemd unit, a distroless container image, a compose example and a Caddy reverse-proxy example. These are configuration files, not Go code.

**Owned paths.** `deploy/systemd/butterdbd.service`, `deploy/systemd/butterdbd.sysusers.conf`, `deploy/Dockerfile`, `deploy/compose.yaml`, `deploy/caddy/Caddyfile`, `deploy/prometheus/alerts.yml`, `deploy/config/butterdbd.toml.example`, `deploy/test.sh`. `deploy/transformd/` belongs to phase 13.

**Read.** `docs/spec/operations.md` §2.5–§2.6 (secret files, example config), §3.1–§3.2 (data directory), §6 (single instance), §8 (shutdown and drain), §11.1–§11.4 (targets and proxies); `docs/spec/security.md` (hardening and secrets). TDrive pattern only: `/home/prieyan/weeb/TDrive/.github/appimage-smoke.Dockerfile` (hermetic smoke test).

**Rules.**
- The systemd unit uses `Type=notify` only if the integration lane wires `sd_notify`; otherwise `Type=simple`. Check `cmd/butterdbd` and record the choice. The unit loads secret paths with `LoadCredential=` for the server key file. Apply the sandboxing in §11.1 (`DynamicUser` or a dedicated user, `ProtectSystem=strict`, `ReadWritePaths=` the data directory, `NoNewPrivileges=yes`, `PrivateTmp=yes`). `TimeoutStopSec` is above the drain budget of §8.
- The Dockerfile is multi-stage: `golang` pinned by digest builds with `CGO_ENABLED=0 GOWORK=off go build -trimpath`, onto `gcr.io/distroless/static-debian12:nonroot` pinned by digest, with one volume for the data directory and no shell. Secrets arrive as Docker secrets mounted as files. Never use `ENV` for secrets.
- `compose.yaml` runs exactly one replica with `restart: unless-stopped` and no scale setting, and notes the Recreate-only rule of §6.
- `deploy/prometheus/alerts.yml` ships the suggested alerts of operations §15.3; validate with `promtool check rules` when available.
- The Caddyfile terminates TLS for the S3 host, passes the admin listener through, sets the upload body limit and timeouts from §11.3–§11.4, and preserves `Host` (SigV4 needs it).
- The example config contains no secret values, only `*_file` paths.

**Tests.** `deploy/test.sh` must:
1. Run `systemd-analyze verify deploy/systemd/butterdbd.service` when available, and skip with a message otherwise.
2. Run `docker build -f deploy/Dockerfile .` and then `docker run --rm --network none <image> version` when Docker is available.
3. Run `caddy validate --config deploy/caddy/Caddyfile` and `promtool check rules deploy/prometheus/alerts.yml` when available.
4. Run `butterdbd check-config --config deploy/config/butterdbd.toml.example`, built from source.

**Verify.**

```bash
bash -n deploy/test.sh
bash deploy/test.sh
```

**Commits.** `add systemd unit and example configuration`, `add distroless container image and compose example`, `add caddy reverse proxy example`, `add prometheus alert rules`.

### 5.4 Lane L4 — test/s3compat

**Objective.** Build the S3 compatibility suite: aws-sdk-go-v2 against an in-process `butterdbd` backed by `faketg` in CI, and against a real deployment when the environment variables are set.

**Owned paths.** `test/s3compat/`.

**Read.** `docs/spec/s3-api.md` §16.2 (binding test list and environment variables), §16.6 (exit criteria), §17 (accepted deviations; do not test against them), §12.4 (SDK settings); `docs/phases/08-s3-api.md` §6 (how the integration lane starts the server in-process).

**Contracts.**

```go
package s3compat

type Target struct {
	Endpoint     string
	AccessKeyID  string
	Secret       string
	Bucket       string
	PublicBucket string
	Fake         *faketg.Fake
	Close        func()
}

func NewTarget(t *testing.T) Target
```

`NewTarget` reads the `BUTTERDB_S3COMPAT_*` variables. If they are present, it targets the real deployment and reads the secret from the file the variable names. Otherwise it starts `butterdbd` in process through `internal/core` with `faketg` and a temporary data directory. Fault-injection tests (`TestCompleteKeepalive`, `TestCompleteErrorIn200`, `TestPutSlowDown`) call `t.Skip` when `Fake` is nil.

**Rules.**
- Run every test both path-style and virtual-hosted-style. Virtual-hosted uses a base domain resolved through a custom dialer, as §16.2 describes.
- The aws-sdk-go-v2 packages are test-only dependencies, added by the prelude at exact package paths (`github.com/aws/aws-sdk-go-v2/service/s3`, `github.com/aws/aws-sdk-go-v2/feature/s3/manager`, `github.com/aws/aws-sdk-go-v2/aws/signer/v4`, `github.com/aws/aws-sdk-go-v2/credentials`).
- Never print the secret. Each test creates its own key prefix and cleans up.

**Tests (binding names).** Every test in the s3-api §16.2 table, from `TestPutGetHeadDelete` through `TestCORSPreflight`.

**Verify.**

```bash
go test -mod=readonly -race -count=1 ./test/s3compat/...
gofmt -l test/s3compat
```

**Commits.** `add s3 compatibility suite harness`, `add s3 compatibility object and listing tests`, `add s3 compatibility multipart auth and fault tests`.

### 5.5 Lane L5 — test/load

**Objective.** Build the load and soak harness that measures the numbers the specs mark "measure on the phase 11 load harness": throughput, latency percentiles, cache hit ratio, `projection.db` size per object, `VACUUM INTO` time and memory.

**Owned paths.** `test/load/`.

**Read.** `docs/spec/operations.md` §9.5, §10.4, §13.2–§13.6 (capacity and sizing), §15.2 (metrics to scrape); `docs/decisions.md` D4.

**Contracts.**

```go
package main

type Profile struct {
	Name        string
	Duration    time.Duration
	Writers     int
	Readers     int
	ObjectSizes []int64
	HotSetRatio float64
	Keys        int
}

type Report struct {
	Profile         string
	Puts, Gets      int
	PutP50, PutP99  time.Duration
	GetP50, GetP99  time.Duration
	SlowDowns       int
	CacheHitRatio   float64
	ProjectionBytes int64
	BytesPerObject  float64
	RSSPeakBytes    int64
}
```

`test/load/main.go` is a command (`go run ./test/load -profile small -target fake|<endpoint>`). It runs against an in-process faketg-backed server by default, or against a deployment named on the command line. It prints the `Report` as JSON and writes `test/load/results/<profile>-<timestamp>.json`, which is git-ignored.

**Profiles.** `smoke` (30 s), `small` (D4 shape: 1 writer per 10 readers, 80% of reads on a 5% hot set, sizes drawn from 50 KiB, 500 KiB, 5 MiB and 50 MiB), `soak` (2 h at `small` rates), and `fill` (puts until `-keys` objects exist, then reports bytes per object and `VACUUM INTO` time).

**Rules.**
- Use aws-sdk-go-v2 with adaptive retry, counting `SlowDown` separately from errors.
- With faketg, inject Telegram latency and flood waits from `-flood-rate`.
- Read cache-hit and queue metrics from the ops listener's `/metrics`.

**Tests.** `TestProfilesValid`, `TestPercentiles`, `TestReportJSONShape`, and `TestSmokeProfileAgainstFake` (runs `smoke` for 5 s, gated by `-short`).

**Verify.**

```bash
go vet -mod=readonly ./test/load/...
go test -mod=readonly -short ./test/load/...
go run ./test/load -profile smoke -target fake -duration 10s
```

**Commits.** `add load and soak harness`, `add load profiles and json reports`.

### 5.6 Lane L6 — docs/guide/ (self-hosting guide, runbook, SDK configuration, Telegram account safety)

**Objective.** Write the operator-facing guides for people self-hosting butterDB.

**Owned paths.** `docs/guide/self-hosting.md`, `docs/guide/runbook.md`, `docs/guide/sdk-configuration.md`, `docs/guide/telegram-account-safety.md`. `docs/guide/admin-app-build.md` belongs to phase 10 L6; link to it, but do not edit it.

**Read.** `docs/spec/operations.md` (all), `docs/spec/security.md` §5, §7 and §13, `docs/spec/s3-api.md` §12.3–§12.4 and §17, `docs/spec/admin-gui.md` §4, `docs/decisions.md` (risks section).

**Content.**
- **`self-hosting.md`:** server setup, data directory permissions, server key generation and an off-host backup step, reverse proxy, the admin certificate fingerprint, first claim from the GUI, Telegram link, and creating the first bucket and access key.
- **`runbook.md`:** readiness failures by state, drain, snapshot verification, restore, rebuild from Telegram, disaster recovery on a new host, and loss of the server key.
- **`sdk-configuration.md`:** a working aws-sdk-go-v2 client (endpoint, path-style, region, timeouts and adaptive retry), PUT/GET/list/delete examples, boto3 and AWS CLI settings, `503 SlowDown` handling, multipart limits, and the documented deviations.
- **`telegram-account-safety.md`:** owner-supplied `api_id`, a dedicated aged account with 2FA, the send-rate limiter, flood waits, account-loss consequences, replica limits, and Telegram's own `api_id` guidance.

Keep secrets, setup tokens and real account identifiers out of every example.

**Checks.**
- Every command and flag matches `cmd/butterdbd` (`butterdbd --help`). Every config key matches operations §2.4.
- Every relative link resolves to an existing file.
- The Go example in `sdk-configuration.md` compiles. Copy it into a scratch module outside the repository and run `go vet` on it.

**Tests.** None in Go. The checks above are this lane's acceptance, and the reviewer runs them.

**Commit.** `write self hosting and operations guides`.

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
