# Phase 08 status — S3 API

Result: accepted
Date: 2026-11-16
Phase branch: phase/08-s3-api @ 3d24e85 (before this note)
Main before: ba26273
Main after: the commit that records this note
Prelude commit: 840e6cd
Lanes head: 4cb41b6
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 internal/access/keys + internal/storage/keyrepo | phase/08-s3-api-l1 | 1 | PASS | ACCEPT | 2 |
| L2 internal/access/sigv4 | phase/08-s3-api-l2 | 1 | PASS | ACCEPT | 3 |
| L3 internal/access/policy | phase/08-s3-api-l3 | 1 | PASS | ACCEPT | 1 |
| L4 internal/s3api/{router,xmlcodec,s3err,bucketops} | phase/08-s3-api-l4 | 1 | PASS | ACCEPT | 4 |
| L5 internal/s3api/{objectops,listing} | phase/08-s3-api-l5 | 2 | PASS | ACCEPT | 6 |
| L6 internal/s3api/multipart | phase/08-s3-api-l6 | 1 | PASS | ACCEPT | 3 |
| INT integration | phase/08-s3-api-int | 1 | PASS | ACCEPT | 5 |

- **Two waves:** L5 and L6 consume the wave-1 packages: router handlers, s3err, xmlcodec, the sigv4 payload reader and the policy evaluator. The prelude could hold only pure types (§15), so L1–L4 ran first. L1–L4 were picked onto the phase branch (head c3eeee4), and L5 and L6 then ran from that head. This is a deviation from one parallel wave, chosen so that wave-2 lanes compiled against real code.
- **API limit:** at about 03:00 IST the L1, L2 and L4 workers hit the 5-hour API limit. They were resumed in their own contexts at 04:31. The interruption is not counted as an attempt.
- **L5 attempt 2:** the reviewer rejected attempt 1 for one defect. In `isCommonPrefix`, a ListObjects v1 marker shorter than the delimiter after the prefix was treated as a rolled-up common prefix. A request such as `prefix=photos/&delimiter=/&marker=photos/` therefore returned an empty page. The test cases for this were also missing. Attempt 2 fixed both. It also fixed two minors: a CopyObject that could not start its long response ran the copy anyway, and two package-level `var` tables became functions.
- **Briefs:** as in phases 06 and 07, lane briefs were passed by line reference, and each lane was monitored and reviewed as soon as it returned.

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |
| 840e6cd | prieyan | define s3 api contracts |
| 3b234a8 | prieyan | add access key repository |
| 3785cb8 | prieyan | add access key service with sealed secrets |
| 2bf18a3 | prieyan | add sigv4 canonical request and header verification |
| 80f9073 | prieyan | add sigv4 presigned query verification |
| c73b7a2 | prieyan | add aws chunked payload and trailer verification |
| 47fb6e6 | prieyan | add s3 access policy evaluator |
| 63d22e8 | prieyan | add s3 error mapping and xml codec |
| c8ff709 | prieyan | add s3 router and dispatch |
| 3af29ab | prieyan | pick query error code for presigned wrong region requests |
| c3eeee4 | prieyan | add s3 bucket operations and configuration getters |
| f363e50 | prieyan | add s3 put get head and delete object handlers |
| 97a1963 | prieyan | add s3 delete objects and copy object |
| 113a06d | prieyan | add object handler constructor tests |
| 1ba4418 | prieyan | add s3 bucket and object listings |
| 8257b90 | prieyan | fix v1 marker common prefix check for short markers |
| c52208c | prieyan | fail copy when the long response cannot start and drop package tables |
| a194b41 | prieyan | add s3 multipart create upload part and abort handlers |
| 3504a2f | prieyan | add s3 complete multipart with keepalive responses |
| 4cb41b6 | prieyan | add s3 multipart listing handlers |
| 37ad9b5 | prieyan | wire s3 api in core |
| 7f4c4e1 | prieyan | assemble serving runtime with sync write read and s3 listener |
| fb5bcfd | prieyan | start s3 listener in serve |
| 5817583 | prieyan | add phase 08 acceptance tests |
| 0da2412 | prieyan | record phase 08 ported files in notices |
| 3d24e85 | prieyan | tidy module requirements for phase 08 |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |
| V2 static build | pass | |
| V3 server binary | pass | |
| V4 vet | pass | |
| V5 tests | pass | Whole module, including the acceptance suites of phases 01–08. |
| V6 race tests | pass | `./internal/...`. The INT reviewer also ran `./internal/core/...`, `./cmd/...` and `test/acceptance/phase08` three times under -race. |
| V7 module integrity | pass | Tidy promoted the four SDK modules from indirect to direct. The go line and go.sum are unchanged. |
| V8 layering | pass | |
| V9 comment policy | pass | 650 files. |
| V10 phase acceptance | pass | TestAwsSdkGoV2CanPUTGETHEADLISTAndDELETE, TestSigV4HeaderAndPresignedRequestsAuthenticate, TestMultipartCompletesToOneObject and TestXMLErrorsCarryStableS3Codes all reported `--- PASS`, as did the extra TestRuntimeServesS3OnItsListenerAfterInitialSync. |
| V11 apps modules | n/a | |
| V12 verify.sh | pass | |
| V13 commit hygiene | pass | 26 commits. Author and committer are prieyan on all. No bodies, no merges, no bad subjects. |
| V14 clean tree | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | Every new package. The s3api lanes test against consumer-port fakes, with real sigv4 payload readers and real httprange. |
| aws-sdk-go-v2 wire tests | yes | service/s3 with static credentials, path-style addressing and s3.PresignClient, against an httptest server and a real 127.0.0.1 listener serving the core-assembled handler. |
| faketg acceptance | yes | The phase 06 write path and phase 07 read path behind the S3 handler. Multipart Complete produces exactly one Telegram document. |
| SQLite and real crypto | yes | Real control.db and projection.db in t.TempDir(). TDE1 encrypted bucket subtests. Sealed access-key secrets. |
| Race detector | yes | |
| Real Telegram | no | The linked branch of `App.StartServing`, with the gotd transport, is compiled and vetted only. |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |
| none in section 8 | n/a | n/a | n/a |

The phase ran under the owner's standing instruction: "continue everything i going to sleep dont ask me anything do the best".

## Module changes

| Module | Package paths | Version | Added by |
| --- | --- | --- | --- |
| github.com/aws/aws-sdk-go-v2 | aws, aws/signer/v4 | v1.47.5 | prelude (test dependency) |
| github.com/aws/aws-sdk-go-v2/service/s3 | service/s3 | v1.114.6 | prelude (test dependency) |
| github.com/aws/aws-sdk-go-v2/credentials | credentials | v1.20.12 | prelude (test dependency) |
| github.com/aws/smithy-go | smithy-go (APIError in acceptance tests) | v1.28.7 | already in go.sum through service/s3; tidy made it direct |

The prelude added these modules as indirect requirements. Tidy after integration made all four direct, because test/acceptance/phase08 imports them. Production packages import none of them.

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |
| backend/mountdav/body_deadline.go | internal/s3api/objectops/idlebody.go | L5 |
| backend/mountdav/body_deadline_test.go | internal/s3api/objectops/idlebody_test.go | L5 |
| backend/mountdav/server.go (listener timeouts, shape only) | internal/core/s3server.go | INT (referenced) |

- **body_deadline_test.go:** four of its five tests were carried. `TestControlBodyUsesOneBoundedAbsoluteDeadline` and the PROPFIND case of `TestBodyDeadlineSetupFailsClosed` cover WebDAV-only behaviour and were dropped. Section 7 asked for all five.
- **Notices:** all rows are in `docs/legal/THIRD_PARTY_NOTICES.md`. The `internal/app/operation.go` and `upload_failures.go` rows are recorded as not used.

## Deviations and minor findings

- **Prelude:** pure contract types live in `contract.go` files for sigv4, policy, router, s3err and xmlcodec (§15). `sigv4.Auth` carries an unexported seed and signing key.
- **L1 (keys, keyrepo):**
  - Minor: `ErrDuplicateKey` is a local sentinel.
  - Minor: a test field name does not match.
- **L2 (sigv4):** minor: `NewVerifier` panics on nil dependencies, because the §15 contract has no error return.
- **L3 (policy):** `VisibleBuckets` uses an optional `ListLive` type assertion, so core passes the bucket repository directly.
- **L4 (router, s3err, xmlcodec, bucketops):**
  - Minor: an unknown-outcome bucket delete answers with `Retry-After: 5`, not 30. It is still never reported as a success.
  - Minor: an OPTIONS preflight to an invalid bucket name answers `400 InvalidBucketName` before the CORS check.
  - Spec conflict: an unknown commit outcome maps to `503 SlowDown` with `Retry-After: 30`, as s3-api §12.1 says. claude.md §8.4 says 500 InternalError; the higher-ranked spec wins.
- **L5 (objectops, listing):**
  - **Commits survive a disconnect:** PutObject, DeleteObject, DeleteObjects and CopyObject run under `context.WithoutCancel`, so a commit survives a disconnect after the body's EOF (§8.1).
  - **DeleteObjects SlowDown:** after the first key that maps to SlowDown, every later key in the same request also gets SlowDown without a call. `remove.Service` has no shared budget parameter.
  - **ListObjects v1 marker:** a marker equal to a rolled-up common prefix becomes an objectrepo continuation token in the §7.3 byte layout. This couples listing to that format.
  - **Put-record cap:** checked in the handler with `wire.PutRecordLen`, giving KeyTooLongError or MetadataTooLarge.
  - **Exported sentinel:** `listing.ErrInvalidContinuationToken` is exported from listing. Core maps `objectrepo.ErrInvalidContinuationToken` to it with `errors.Is`.
  - **GET failure after the first byte:** aborts the connection with `http.ErrAbortHandler`.
  - **Extra commit:** "add object handler constructor tests" was added; the commit plan was split.
- **L6 (multipart):**
  - **Extra dependencies:** Authorizer, UploadLookup (core adapter over the jobrepo index) and SourceReader (for UploadPartCopy).
  - **Keepalive:** the default is 15 s per spec §11; the brief said 10 s.
  - **Complete:** validates everything before the first response byte, then starts the long response. Outcomes go in the 200 body. The stage job runs under `context.WithoutCancel`.
  - **Duplicated header rules:** CreateMultipartUpload header rules are implemented locally in `attributes.go`, duplicating L5's PutObject rules, because the two lanes ran in parallel. A later phase should deduplicate them.
  - Minor: part checksums are never recorded end to end. `multipartstage.PutPart` takes no checksum, so Complete's listed-checksum check never fires against a real stage, and ListParts shows no part checksums. UploadPart still verifies x-amz-checksum and Content-MD5 against the stream.
  - Minor: UploadPart takes no admission or spool reservation (s3-api §9.2 and §12.1). Admission has no reservation API, and the stage has no hook.
  - Minor: `handlers.go:97` and `list.go:42` return `errs` sentinels without the package prefix.
  - Minor: the Complete body is not checked against Content-MD5.
  - Minor: ListMultipartUploads reports the deployment owner as Initiator. ListParts shows the key name only when the caller is the initiator.
- **INT:**
  - **Core assembly:** `core.NewS3` assembles keys, sigv4, policy and all handler sets onto the router. `core.Runtime` assembles Projection, Sync, Write, Read and S3 from injected parts, which faketg tests use. `App.StartServing` builds the Runtime from the primary Telegram session.
  - **serve:** starts the runtime when linked. When state.json is absent or the primary account is unlinked, it logs "s3 listener not started: deployment is not linked" and runs as before, opening nothing. This closes the open question carried since phase 05 (serve-time startup of sync, write and read).
  - **Readiness:** the listener answers `503 starting` until `EnsureAllAuthoritative` succeeds. That call is retried every 30 s through the injected Sleeper.
  - **Shutdown:** lifecycle order is read, write, livesync, multipart expiry, initial sync, then the s3 listener, closed in reverse so the listener stops first. All of it fits inside `shutdown.total_timeout`.
  - **Listener:** ReadHeaderTimeout 10 s, IdleTimeout 120 s, MaxHeaderBytes 64 KiB and no read or write timeouts (§2.2). TLS is used when `s3.tls_mode = "files"`.
  - **Added beyond the brief:** an hourly loop calls `multipartstage.Stage.SweepIdle` with `s3.multipart_idle_expiry`. Nothing else invoked the idle expiry.
  - **Owner ID:** `Ring.OwnerID()`, the HMAC owner id of §7.1.
  - **sigv4 limits:** MaxSkew and MaxPresign use the sigv4 defaults, because config has no key for them.
  - Minor: `internal/core/s3adapters.go:70` returns a non-sentinel list error without a `core:` wrap.

## Phase document defects

- §6 says the integration lane cherry-picks the lanes and runs `go mod tidy`. Protocol §9 and §10.3 give both to the orchestrator; the protocol was followed.
- §5.6 says the keepalive default is 10 s, but s3-api §11 and the config default say 15 s.
- The §5 lane briefs need wave-1 APIs in L5 and L6 that the prelude could not hold as pure types. The phase document does not plan for two waves.
- §7 says to carry all five `body_deadline_test.go` cases, but one and a half of them are WebDAV-only.
- No lane owns object ACL and tagging (GetObjectAcl, PutObjectAcl, Get/Put/DeleteObjectTagging; s3-api §6.1, §10.2 and §10.5). The router answers 501 NotImplemented for them. The AWS CLI's S3-to-S3 copy calls GetObjectTagging.

## Open questions for the owner

Carried, still open:

- Put retry pacing.
- The `upload_jobs` CHECK gap.
- Sentinels belong in `internal/domain/errs`.
- The clock timer port.
- The deleteChannel RPC.
- Whether to wire warm-on-put and the invariant 7 generation gate.

New in this phase:

- **Unwired S3 settings:** the auth-failure limiter (`s3.auth_failure_limit`), the anonymous rate limiter (`anonymous_rate`, `anonymous_burst`), `s3.write_idle_timeout` and `trusted_proxies` are not wired. No lane package has a hook for them. Which phase owns them, 09 or 11?
- **Degraded readiness:** should the listener move to `degraded` on a sync or transport failure after the first success? `core.S3.MarkDegraded` exists, but no spec section names its trigger.
- **Object ACL and tagging:** which phase owns these operations?
- **Multipart follow-ups:** should a phase 06 follow-up record part checksums in the multipart stage and add an UploadPart admission reservation?

## Worktrees and branches left in place

None.
