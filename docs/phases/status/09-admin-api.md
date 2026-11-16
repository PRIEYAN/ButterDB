# Phase 09 status — Admin API and server-side admin auth

Result: accepted
Date: 2026-11-16
Phase branch: phase/09-admin-api @ 8c64cb0 (before this note)
Main before: 3df37ad
Main after: the commit that records this note
Prelude commits: bad4055, e18be73
Lanes head: 4674388
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 internal/adminapi/setup | phase/09-admin-api-and-auth-l1 | 1 | PASS | ACCEPT | 2 |
| L2 internal/adminapi/session + internal/storage/adminrepo | phase/09-admin-api-and-auth-l2 | 1 | PASS | ACCEPT | 3 |
| L3 internal/adminapi/tlscert | phase/09-admin-api-and-auth-l3 | 1 | PASS | ACCEPT | 1 |
| L4 internal/adminapi/handlers | phase/09-admin-api-and-auth-l4 | 2 | PASS | ACCEPT | 6 |
| L5 api/adminv1 client | phase/09-admin-api-and-auth-l5 | 2 | PASS | ACCEPT | 3 |
| INT integration | phase/09-admin-api-and-auth-int | 1 | PASS | ACCEPT | 5 |
| L6 test/acceptance/phase01 (fix lane) | phase/09-admin-api-and-auth-l6 | 1 | PASS | ACCEPT | 1 |

- **API limit:** at about 09:00 IST all five lane workers hit the 5-hour API limit. None had committed yet; L3 and L5 had uncommitted files. Each worker was resumed in its own context at 11:03 IST. The interruption is not counted as an attempt.
- **L5 attempt 2:**
  - Attempt 1 was rejected because a caller-supplied `HTTPTransport` silently disabled SPKI pinning. A malformed pin or an unknown trust mode was also accepted without error.
  - Attempt 2 fixed both:
    - An https base URL now always validates the trust settings.
    - Pinned trust needs an `*http.Transport`. The client clones it and installs the pin check on the clone, and it rejects DialTLS hooks.
    - The certificate probe now accepts TLS 1.2.
- **L4 attempt 2:** attempt 1 was rejected for writing audit outcome `partial`. The `admin_audit.outcome` CHECK allows only `success` and `failure`, and the binding test asserted the bad value. Attempt 2 records `failure` with the first error code, and the audit fake now enforces the CHECK. It also re-raises `http.ErrAbortHandler`, and answers a client cancellation with a bare 499 instead of a 500.
- **L1 and L2 mid-lane clarification:** after reviewing L1, the orchestrator told the still-running L2 worker that the claim and pairing stores must meet three rules:
  - compare hashes in constant time;
  - use one transaction;
  - return distinguishable sentinels.

  L2 met all three.
- **L6 fix lane:**
  - Integration made `serve` generate the server key at start (operations §5.3) and always bind the admin listener. Phase 01's `TestProcessLockPreventsASecondServer` started `serve` with only `BUTTERDB_DATA_DIR`, so it exited 4.
  - The integration lane does not own that test. The orchestrator opened fix lane L6, based on the INT head, to change only the test environment: a server key in the temp dir and an admin listener on a free loopback port.
  - This is a §15 deviation, the same as in phases 05 and 06.
- **Briefs and reviews:** lane briefs were passed by line reference, and each lane was monitored and reviewed as soon as it returned. L1–L3 were picked while L4 was still running, which keeps the table order.

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| bad4055 | prieyan | define admin api v1 contracts |
| e18be73 | prieyan | define admin server shared contracts |
| cf7020b | prieyan | add setup token minting and server claim |
| eff1b03 | prieyan | add telegram login relay for the admin api |
| 82e6bed | prieyan | add admin repository |
| efc58b5 | prieyan | add device tokens and auth middleware |
| e49664f | prieyan | add device pairing and control socket |
| 947dc06 | prieyan | add admin tls certificate with stable spki pin |
| 6589dfa | prieyan | add admin api router codec and error mapping |
| 17c14f3 | prieyan | add admin server claim pairing and device endpoints |
| 950e64a | prieyan | add admin bucket key and object endpoints |
| 27d36c6 | prieyan | add admin status usage and audit endpoints |
| 3db1af6 | prieyan | restrict audit outcomes to success and failure |
| 4751c0f | prieyan | reraise abort handler panics and handle client cancellation quietly |
| 29a11ad | prieyan | add admin api go client with pinned trust |
| 6cd69ce | prieyan | add admin api client object streaming |
| 4674388 | prieyan | enforce pinned trust with custom transport and relax probe tls |
| d846b56 | prieyan | wire admin api in core |
| d0090ba | prieyan | add admin-token command |
| f4a8b8f | prieyan | start admin listener and control socket in serve |
| 5e3b179 | prieyan | add admin wiring tests in core |
| b89f5c7 | prieyan | add phase 09 acceptance tests |
| 8c64cb0 | prieyan | give phase 01 lock test its own server key and admin listener |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | The whole module, including the acceptance suites of phases 01–09. Before L6, the only failure was phase 01's lock test. |
| V6 race tests | pass | `./internal/...` and `./api/...`. The INT reviewer also ran `./internal/core/...`, `./cmd/...` and `test/acceptance/phase09` twice under -race. |
| V7 module integrity | pass | Tidy changed nothing. |
| V8 layering | pass | |
| V9 comment policy | pass | 750 files. |
| V10 phase acceptance | pass | TestSetupTokenIsOneTime, TestDevicePairingCodeExpires, TestCertificateFingerprintIsPinned and TestAdminTokenScopesAndAuditEventsAreEnforced all reported `--- PASS`. So did the extra tests TestAdminAPIEndToEnd, TestStorageRoutesWaitForTheStorageRuntime and TestCertificateFilesModeReloadsOnRequest. |
| V11 apps modules | n/a | |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 23 commits. Author and committer are prieyan on all. No bodies, no merges, no bad subjects. |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | Every new package. The handlers tests use hand-written fakes for every port. |
| SQLite in t.TempDir() | yes | Real control.db with adminrepo, keyrepo and accountrepo. Atomic consumption of the claim and of pairing codes is tested. |
| faketg | yes | The setup relay drives a real `identity.Service` with code and password retries, expiry, timeout and account mismatch. INT covers the link through the admin API, Saved Messages pairing delivery, and buckets, objects and S3 behind the admin API. |
| Real TLS and pinning | yes | A TLS 1.3 self-signed listener, with the real `api/adminv1` client pinned by SPKI. A wrong pin fails before any request is sent. Files mode reloads its certificate. |
| Unix control socket | yes | Mode 0600, one JSON line per connection, and `admin-token` over the socket and through the direct-database fallback. |
| Built binary | yes | Phase 01 runs the real `butterdbd serve` with the process lock. INT also ran it manually, unlinked. |
| Race detector | yes | |
| Real Telegram | no | |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| none in section 8 | n/a | n/a | n/a |

The phase ran under the owner's standing instructions: "continue everything i going to sleep dont ask me anything do the best", then "continue".

## Module changes

None.

## Ported TDrive files

None. L1 relays to the phase 04 `internal/identity` login state machine and ports nothing. No `THIRD_PARTY_NOTICES.md` rows were added.

## Deviations and minor findings

- **Prelude, orchestrator-designed:**
  - `paths.go` route, parameter and query constants (`Path*` with ServeMux `{param}` placeholders, `Param*`, `Query*`, `HeaderAuthorization`, `BearerPrefix`, `UnlinkConfirmation`). §16.1 names `paths.go` but gives no route constants.
  - The bodies of `APIError.Error`, `IsCode` and `RetryAfter`. No lane owns `errors.go`.
  - `replica.go` DTOs, committed now instead of in phase 12. The §16.7 client contract has replica methods that need `ReplicaStatus`.
  - A second prelude commit, `define admin server shared contracts`, adds the types the lane briefs use but no spec defines:
    - `adminv1.DeviceInfo`, plus `ClaimResult` and `DeviceTokenResult` as aliases of `DeviceCredentials`.
    - `adminrepo.Device`, `NewDevice` and `CreatedVia`.
    - `setup.LoginRelay` in `setup/contract.go`, so that L4 could import it in parallel.

  All five lanes then ran in one wave.
- **L1 (setup):**
  - `Dependencies.ServerID` was added.
  - The setup-token hash is a local copy of the §5.7 construction. It is cross-checked against `session.HashSecret` by test vector 7473e863…65ee.
  - Minor: the claim sentinels are local to `setup`; core translates them.
  - Minor: the constant-time comparison happens in the store (adminrepo).
- **L2 (session, adminrepo):**
  - `DeviceStore` gained `DeletePairingCode`.
  - `adminrepo.New` takes a clock.
  - `DeleteRevokedBefore` keeps the newest revoked row when no active device remains, so the server never returns to `unclaimed` (§5.1 versus §7.5).
  - Pairing codes store `device_name` as ''.
  - Minor:
    - Constructors fixed by the brief cannot reject nil dependencies eagerly.
    - `requireClaimed` uses a fail-open type assertion.
    - The middleware writes its own 401, 429 and 500 envelopes.
    - Sentinels are local to the package.
    - The control socket uses `os.Remove` and `os.Chmod`.
    - Errors from `TouchSeen` and `MarkNewTokenUsed` are dropped.
    - `nullableTime` is unused.
- **L3 (tlscert):**
  - An injectable `LoadOrCreateWith(Config)` sits behind the binding `LoadOrCreate`.
  - `dir` is the data directory.
  - NotBefore is now minus one hour.
  - Minor: an empty Dir or ServerID returns `errs.ErrInvalidKey`, whose message ("invalid object key") is misleading.
  - Minor: the `zero()` helper is redundant.
- **L4 (handlers):**
  - `Dependencies` is extended with `Devices`, `Device`, `Random`, `Logger` and `Config`.
  - The handlers declare their own sentinels and pass `*adminv1.APIError` through.
  - The error mapping imports identity, the ingest packages, httprange, buckets and the root tgclient, which brushes against L9.
  - Idempotency is scoped by setting `device:<id>` in `PutRequest.AccessKeyID` (E9).
  - The upload context is `WithoutCancel`, bounded by `admin.upload_commit_budget`.
  - Replica routes return 501 from `replica_unavailable.go`. Phase 12 must remove that file.
  - Minor: a client cancellation is answered with a bare 499, which is outside the §4.2 table.
  - Minor: some optional `details` fields are not filled.
- **L5 (client):**
  - Pins use the canonical form `SHA256:<hex>`, with lenient parsing.
  - The client follows no redirects.
  - Errors without an envelope get synthetic codes.
  - The token is redacted from echoed error text.
  - `GetObjectOptions` 0/0 means no range, and the end is inclusive, so `0-0` cannot be expressed.
- **INT:**
  - Serving:
    - `serve` now always generates the server key when it is absent and no state.json exists (WARN, §5.3).
    - It opens one TelegramSession, shared by the admin API and the runtime.
    - It always starts the admin listener and the control socket.
    - It starts the storage runtime only when linked. Storage routes answer `telegram_not_linked` or `server_not_ready` until a backend is published.
  - Security:
    - Core translates every claim, pairing, setup, session and adminrepo sentinel.
    - A core wrapper audits `auth.failure`, at most once per address per minute.
    - Random reads are guarded by a mutex.
    - The direct `admin-token` path uses a discarded logger, so the token is printed only to stdout.
  - S3 and the admin API share one `keys.Service`, through an optional `Keys` field on `S3Dependencies` and `RuntimeDependencies`, so an admin revoke reaches S3.
  - The S3 endpoint falls back to the S3 listen address when `s3.public_url` is unset.
  - SIGHUP reloads the files-mode certificate. The signal handling itself is untested.
  - Phase 08 cmd tests were adapted.
  - Minor findings:
    - **No hot activation (§7.1):** after a GUI link, storage stays down until `butterdbd setup` and a restart.
    - **Unauthenticated rate limit:** applied only to `server-info`, not to unknown routes.
    - **Pairing code TTL ignored:** `admin.pairing_code_ttl` has no effect, because session uses a constant 10 minutes.
    - **Missing `--key-only` hint:** a key-generation failure (`serverkey.go:21`) does not show the §5.3 `butterdbd setup --key-only` hint.
    - **Stale control socket:** `controlSocket.awaitReady` treats a stale socket file as ready (`admincomponents.go:69`). A later bind failure then surfaces only at Close.
    - **Coarse status:** component health is all-or-nothing, `telegram.connected` is coarse, `flood_wait_until` is always null and `jobs_in_flight` is always 0. This lasts until phase 11.

## Phase document defects

- §5.1, §5.2 and §5.4 use `adminv1.DeviceInfo`, `ClaimResult`, `DeviceTokenResult`, `adminrepo.Device` and `adminrepo.NewDevice`, but neither §16 nor schema.md defines them.
- L1's contract references the L2 type `adminrepo.NewDevice` while also saying "never import L2 code".
- L4 imports `setup.LoginRelay` from a lane that runs in parallel. The orchestrator fixed both with the second prelude commit.
- §16.1 gives `paths.go` "route constants" but names none.
- The §16.7 client needs `ReplicaStatus`, which §16.1 assigns to phase 12.
- `SetupEndpoints.ServerInfo` (L4) has no owner. INT built it in core.
- The L2 `NewAuthenticator`, `NewPairing`, `NewLimiter` and `NewTokens` signatures have no error return, which conflicts with the constructor rule in claude.md §6.2.
- `session.NewPairing` takes no TTL, while `admin.pairing_code_ttl` is configurable (§2.1).
- §6 says the integration lane cherry-picks and tidies. Protocol §9 and §10.3 give both to the orchestrator, and the protocol was followed.

## Open questions for the owner

Carried, still open:

- Put retry pacing.
- The `upload_jobs` CHECK gap.
- Sentinels belong in `internal/domain/errs`. Phase 09 adds more local admin sentinels.
- The clock timer port.
- The deleteChannel RPC.
- Warm-on-put and the invariant 7 generation gate.
- Wiring of the S3 auth-failure and anonymous rate limiters, `write_idle_timeout` and `trusted_proxies`.
- Degraded readiness.
- Ownership of object ACLs and tagging.
- Recording multipart part checksums.

New in this phase:

- **Hot activation after a GUI link (operations §7.1, steps 11–15):** should registry provisioning and storage runtime start run without a restart? Which phase should own this, 10 or 11?
- **Pairing code TTL:** should `session.NewPairing` take the TTL (a lane contract change), or should the config key be dropped?
- **Commit budget expiry:** should `context.DeadlineExceeded` on the admin upload commit budget map to `telegram_flood_wait` instead of `telegram_unavailable`?
- **Single-byte ranges:** should `GetObjectOptions` support a single-byte range or a suffix range (a DTO contract change)?
- **Layering exceptions:** should the L9 exceptions (`handlers` and `session` importing `adminrepo`, and `handlers` importing root `tgclient` for error mapping) be recorded in docs/decisions.md?

## Worktrees and branches left in place

None.
