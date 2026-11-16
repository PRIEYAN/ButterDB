# S3 API specification

butterDB speaks the Amazon S3 REST protocol so that any AWS SDK, the AWS CLI,
`rclone` and `s3cmd` work by changing the endpoint (D5). This document is the
contract for the S3 listener: addressing, authentication, payload handling,
every operation and its v1 status, metadata, errors, timeouts and the
compatibility test plan. It is built in phase 08
([08-s3-api.md](../phases/08-s3-api.md)) on top of the write path (phase 06)
and the read path (phase 07).

| Item | Value |
| --- | --- |
| Default listen address | `0.0.0.0:9000` (plan §4.2) |
| Addressing | Path-style always; virtual-hosted-style when `s3.domains` is set |
| Region | `s3.region`, default `us-east-1` |
| Authentication | AWS Signature Version 4: `Authorization` header, presigned query, aws-chunked streaming |
| XML namespace | `http://s3.amazonaws.com/doc/2006-03-01/` |
| Maximum stored object size | `limits.MaxStoredObjectBytes` = `1900 * 1024 * 1024` = 1,992,294,400 bytes, ciphertext counts (D9) |
| Server packages | `internal/access/{keys,sigv4,policy}`, `internal/s3api/{router,xmlcodec,s3err,bucketops,objectops,listing,multipart}`, `internal/storage/keyrepo` |
| Compatibility suite | `test/s3compat` |

Bound by: D5, D6, D7, D8, D9, D10, D11, A4, A5, A6, A7, A8, E2, E3, E4, E8, E9, E10.
Related: [storage-model.md](storage-model.md) (put op fields, ETag storage),
[admin-api.md](admin-api.md) (buckets, access keys, presign),
[operations.md](operations.md) (configuration file, reverse proxy).

---

## 1. Principles

1. **AWS behaviour is the default.** Where butterDB deviates, the deviation is
   deliberate and listed in §17. Every deviation is either a capability
   butterDB does not have (versioning, KMS, ACL grants) or a limit Telegram
   imposes (object size, the put record cap).
2. **Fail before reading the body.** Authentication, authorisation, header
   validation, size limits and metadata limits are checked before the first
   body byte is read, so a client sending `Expect: 100-continue` never uploads
   a body that will be refused.
3. **A success means a Telegram commit (D8).** `200` on PutObject,
   CopyObject, CompleteMultipartUpload and UploadPartCopy is written only after
   the `t=put` commit is projected. UploadPart is acknowledged after the part
   is fsynced to the staging directory, because parts never reach Telegram on
   their own.
4. **Telegram pressure is `503 SlowDown`.** A flood wait or admission wait
   that does not fit the request budget is answered with `503 SlowDown` and
   `Retry-After`, which every AWS SDK retries (A7).
5. **Only `internal/s3api/s3err` writes S3 error responses** (claude.md §8.4).

---

## 2. Listener, endpoint and addressing

### 2.1 Configuration

Keys live in the `[s3]` table of the server TOML file and are overridable by
`BUTTERDB_S3_*` environment variables (`internal/platform/config`).
[operations.md](operations.md) §2.4 is authoritative for the configuration
file. The first group below repeats its `[s3]` keys with their meaning for this
listener; the second group is added by this document, and operations.md §2.4
must list it too. Every default is Default (owner may change).

Keys owned by [operations.md](operations.md) §2.4:

| Key | Env | Default | Meaning here |
| --- | --- | --- | --- |
| `s3.listen` | `BUTTERDB_S3_LISTEN` | `0.0.0.0:9000` | TCP address of the S3 listener, plain HTTP (§2.6) |
| `s3.public_url` | `BUTTERDB_S3_PUBLIC_URL` | empty | External base URL, for example `https://s3.example.com`. Reported as `endpoints.s3_public_url` by the admin API, used for presigned URLs and for `Location` (§9.4) |
| `s3.region` | `BUTTERDB_S3_REGION` | `us-east-1` | Region string clients sign with (§3) |
| `s3.domains` | `BUTTERDB_S3_DOMAINS` | `[]` | Base domains under which `<bucket>.<domain>` selects virtual-hosted-style (§2.4). Empty means path-style only |
| `s3.trusted_proxies` | `BUTTERDB_S3_TRUSTED_PROXIES` | `["127.0.0.1/32", "::1/128"]` | CIDRs whose `X-Forwarded-For` and `X-Forwarded-Proto` are honoured for rate limiting, logs and `Location` |
| `s3.request_budget` | `BUTTERDB_S3_REQUEST_BUDGET` | `30s` | Total time one request may spend waiting on admission, the E10 send-rate limiter and Telegram flood waits before `503 SlowDown` (§12.1). Transfer time does not count |
| `ingest.max_concurrent_puts` | `BUTTERDB_INGEST_MAX_CONCURRENT_PUTS` | `4` | Process-wide put slots in `ingest/admission`, shared with admin uploads (defined in [operations.md](operations.md) §2.4) |
| `ingest.max_concurrent_puts_per_key` | `BUTTERDB_INGEST_MAX_CONCURRENT_PUTS_PER_KEY` | `2` | Put slots one access key may hold at once (defined in [operations.md](operations.md) §2.4) |
| `s3.anonymous_rate` | `BUTTERDB_S3_ANONYMOUS_RATE` | `50` | Anonymous requests per second per client address (§4.11) |
| `s3.anonymous_burst` | `BUTTERDB_S3_ANONYMOUS_BURST` | `100` | Burst for the anonymous limiter |
| `s3.max_clock_skew` | `BUTTERDB_S3_MAX_CLOCK_SKEW` | `15m` | SigV4 request-time tolerance (§4.2) |
| `s3.multipart_idle_expiry` | `BUTTERDB_S3_MULTIPART_IDLE_EXPIRY` | `168h` | An upload with no activity this long is aborted by `ingest/sweeper` (plan §7.4 step 8) |

Keys added by this document:

| Key | Env | Default | Meaning |
| --- | --- | --- | --- |
| `s3.body_idle_timeout` | `BUTTERDB_S3_BODY_IDLE_TIMEOUT` | `60s` | Longest gap between request body reads before `RequestTimeout` |
| `s3.write_idle_timeout` | `BUTTERDB_S3_WRITE_IDLE_TIMEOUT` | `60s` | Longest blocked response write before the connection is closed |
| `s3.keepalive_interval` | `BUTTERDB_S3_KEEPALIVE_INTERVAL` | `15s` | Whitespace interval for long responses (§11) |
| `s3.completed_upload_retention` | `BUTTERDB_S3_COMPLETED_UPLOAD_RETENTION` | `24h` | How long a completed multipart upload answers a repeated Complete (§9.4) |
| `s3.auth_failure_limit` | `BUTTERDB_S3_AUTH_FAILURE_LIMIT` | `100` | Failed authentications per client address per 10 minutes before blocking (§4.12) |
| `s3.cors_allowed_origins` | `BUTTERDB_S3_CORS_ALLOWED_ORIGINS` | `[]` | CORS origins (§13). Empty disables CORS |
| `s3.cors_allowed_methods` | `BUTTERDB_S3_CORS_ALLOWED_METHODS` | `["GET", "HEAD", "PUT", "POST", "DELETE"]` | CORS methods |
| `s3.cors_allowed_headers` | `BUTTERDB_S3_CORS_ALLOWED_HEADERS` | `["*"]` | CORS request headers |
| `s3.cors_expose_headers` | `BUTTERDB_S3_CORS_EXPOSE_HEADERS` | §13.3 list | CORS exposed response headers |
| `s3.cors_max_age` | `BUTTERDB_S3_CORS_MAX_AGE` | `3600` | Preflight cache seconds |

`butterdbd check-config` fails when `s3.public_url` is set but is not an
absolute `http` or `https` URL without query or fragment, when a base domain is
not a valid DNS name, when `s3.region` does not match `^[a-z0-9-]{1,32}$`, when
`s3.request_budget` is under `5s`, or when a CORS origin is neither `*` nor
`scheme://host[:port]`.

### 2.2 HTTP server settings

| Setting | Value |
| --- | --- |
| `ReadHeaderTimeout` | 10 s |
| `ReadTimeout`, `WriteTimeout` | 0; per-request idle deadlines are set with `http.ResponseController` (§12.2) |
| `IdleTimeout` | 120 s |
| `MaxHeaderBytes` | 64 KiB; larger requests get Go's `431` |
| Protocol | Plain HTTP/1.1. No TLS and no h2c on the listener; TLS is terminated by a reverse proxy ([operations.md](operations.md) §11.3, §2.6 here) |
| `Server` response header | `butterdb` |
| Router | Built on the raw request URI. `http.ServeMux` is never used for S3 routes, because it cleans `//`, `/./` and `/../` and redirects, which corrupts S3 keys and signatures |

### 2.3 Path-style addressing (required)

Path-style is always available, on every `Host`:

| Request path | Target |
| --- | --- |
| `/` | Service (ListBuckets) |
| `/<bucket>` or `/<bucket>/` | Bucket |
| `/<bucket>/<key>` | Object; the key is everything after the first `/` following the bucket, percent-decoded once |

Rules:

- The path is taken from `r.URL.RawPath` when set, else `r.URL.Path`, and
  split before decoding, so `%2F` inside a key is a `/` in the key and never a
  separator ambiguity. Decoding is plain percent decoding; `+` is a literal
  plus in a path.
- No normalisation: `a//b`, `a/./b` and `a/../b` are distinct keys, exactly as
  in S3.
- A path segment that is not valid percent encoding returns `400 InvalidURI`.
- An empty key after `/<bucket>/` addresses the bucket.

### 2.4 Virtual-hosted-style addressing (optional)

Enabled when `s3.domains` is non-empty. For a request whose `Host`
(lowercased, port removed) ends with `.<base domain>` for a configured base
domain, the bucket is the part before that suffix and the whole path is the
key:

| `Host` | Path | Bucket | Key |
| --- | --- | --- | --- |
| `photos.s3.example.com` | `/2026/cat.jpg` | `photos` | `2026/cat.jpg` |
| `photos.s3.example.com:9000` | `/` | `photos` | none (bucket request) |
| `s3.example.com` | `/photos/2026/cat.jpg` | `photos` | `2026/cat.jpg` (path-style) |
| `my.dotted.bucket.s3.example.com` | `/a` | `my.dotted.bucket` | `a` |
| `203.0.113.10:9000` | `/photos/a` | `photos` | `a` (path-style) |

- The longest matching base domain wins.
- A `Host` equal to a base domain, or matching none, is path-style.
- Dotted bucket names work in virtual-hosted style only if the TLS
  certificate covers them; a wildcard certificate `*.s3.example.com` covers one
  label. Clients with dotted bucket names should use path-style.
- AWS SDKs default to virtual-hosted style. Clients must either set
  path-style (§12.4) or the operator must configure `s3.domains` and
  wildcard DNS and TLS.

### 2.5 Host validation

When `s3.listen` is a loopback address, `Host` (port removed) must be one of:
the host of `s3.public_url`, a configured base domain or any subdomain of one,
`localhost`, `127.0.0.1`, `::1`. Any other `Host` returns `400 InvalidRequest`
with message `Host header is not served by this endpoint` (claude.md §10
invariant 12, DNS rebinding).

### 2.6 Reverse proxy requirements

A TLS-terminating proxy in front of `s3.listen` must:

- forward the original `Host` header unchanged (nginx:
  `proxy_set_header Host $http_host;`), because `host` is always a signed
  header;
- forward the request URI byte for byte (nginx: `proxy_pass
  http://127.0.0.1:9000;` with no URI part), because the canonical URI is
  computed from the raw path;
- stream both directions (nginx: `proxy_request_buffering off`,
  `proxy_buffering off`, `client_max_body_size 1950m` as in
  [operations.md](operations.md) §11.3, or `0`; the aws-chunked encoded body is
  slightly larger than the object);
- allow a read timeout of at least `s3.request_budget` plus the slowest
  expected Telegram upload of the largest object plus 60 s. At the low end of
  the 2–4 MB/s planning rate ([operations.md](operations.md) §13.2),
  1,992,294,400 bytes take about 996 s, so the minimum is about 1,086 s.
  Recommended: `proxy_read_timeout 1200s` and `proxy_send_timeout 1200s`;
- set `X-Forwarded-Proto`, which `Location` (§9.4) uses when
  `s3.public_url` is empty;
- not strip `Content-MD5`, `x-amz-*`, `Expect` or `Transfer-Encoding`.

TLS for the S3 API is always the proxy's. Clients that send the default
`STREAMING-UNSIGNED-PAYLOAD-TRAILER` uploads (§4.4) do so only over HTTPS, so a
deployment reached over plain HTTP gets signed-hex uploads from the SDKs
instead; both work.

### 2.7 Readiness

The S3 listener follows the server states of [operations.md](operations.md)
§7.2 (readiness: plan §3.7). OPTIONS preflights are always answered (§13.2).

| State | Reads (GET, HEAD, listings, getters) | Writes (PUT, POST, DELETE) |
| --- | --- | --- |
| `starting`, `unlinked`, `syncing` | `503 ServiceUnavailable`, `Retry-After: 30` | `503 ServiceUnavailable`, `Retry-After: 30` |
| `ready` | Served | Served |
| `degraded` (Telegram disconnected, or in a flood wait longer than `s3.request_budget`) | Served from the projection, and from the block cache only when it is enabled (D21); a read that needs Telegram is `503 SlowDown` with `Retry-After` | `503 SlowDown` with `Retry-After` |

Independently of the server state, a bucket without write authority (scan in
progress or rebuild pending, [storage-model.md](storage-model.md)) refuses
writes to that bucket only with `503 SlowDown` and `Retry-After: 5`; reads and
other buckets are unaffected.

---

## 3. Region

- The deployment has one region string, `s3.region`, default `us-east-1`.
- A request's credential scope region must equal `s3.region` or be
  `us-east-1`. `us-east-1` is always accepted because tools such as `s3cmd`
  and the AWS CLI's `GetBucketLocation` probe sign with it. Default (owner may
  change).
- Any other region returns `400 AuthorizationHeaderMalformed` (header auth)
  or `400 AuthorizationQueryParametersError` (presigned), message `the region
  '<got>' is wrong; expecting '<s3.region>'`, with a `<Region>` element
  holding `s3.region`.
- `GetBucketLocation` returns an empty `LocationConstraint` when `s3.region`
  is `us-east-1` (AWS's representation of us-east-1) and the region string
  otherwise.
- `HeadBucket` responses carry `x-amz-bucket-region: <s3.region>`, as AWS
  does; other responses do not.

---

## 4. Authentication

### 4.1 Authentication kind

| Request carries | Kind | Result |
| --- | --- | --- |
| `Authorization: AWS4-HMAC-SHA256 ...` | Header SigV4 | §4.2 |
| Query `X-Amz-Algorithm=AWS4-HMAC-SHA256` | Presigned SigV4 | §4.3 |
| Both of the above | | `400 InvalidArgument`, `Only one auth mechanism allowed` |
| `Authorization: AWS <id>:<sig>` or query `AWSAccessKeyId` + `Signature` (SigV2) | | `400 InvalidRequest`, `The authorization mechanism you have provided is not supported. Please use AWS4-HMAC-SHA256.` |
| `Authorization: AWS4-ECDSA-P256-SHA256 ...` or `X-Amz-Algorithm=AWS4-ECDSA-P256-SHA256` (SigV4a) | | `501 NotImplemented` |
| Any other `Authorization` scheme | | `400 InvalidArgument`, `Unsupported Authorization Type` |
| `X-Amz-Security-Token` header or query parameter | | `400 InvalidToken`; butterDB has no temporary credentials |
| Nothing | Anonymous | §4.11 |

### 4.2 Header authentication (SigV4)

Header form:

```
Authorization: AWS4-HMAC-SHA256 Credential=<AKID>/<yyyymmdd>/<region>/s3/aws4_request, SignedHeaders=<h1;h2;...>, Signature=<64 hex>
```

Parsing: the three components may appear in any order, separated by `,` with
optional spaces. A missing component, a credential with other than five `/`
parts, a terminator other than `aws4_request` or a service other than `s3`
returns `400 AuthorizationHeaderMalformed`. A signature that is not 64
lowercase hex characters returns `403 SignatureDoesNotMatch`.

Required headers:

| Header | Rule |
| --- | --- |
| `x-amz-date` or `Date` | `x-amz-date` in ISO 8601 basic form `20261113T120000Z`; when absent, `Date` in any `http.ParseTime` form. Neither: `403 AccessDenied`, `AWS authentication requires a valid Date or x-amz-date header` |
| `x-amz-content-sha256` | Required for header auth. Absent: `400 InvalidRequest`, `Missing required header for this request: x-amz-content-sha256`. Allowed values in §4.4 |
| `host` | Must be in `SignedHeaders` |

The credential scope date (`yyyymmdd`) must equal the first eight characters
of the request date, else `400 AuthorizationHeaderMalformed`.

**Clock skew.** If the request time differs from the server clock by more than
`s3.max_clock_skew` (15 minutes, AWS's value) in either direction, the
response is `403 RequestTimeTooSkewed`
with `<RequestTime>`, `<ServerTime>` and `<MaxAllowedSkewMilliseconds>900000</MaxAllowedSkewMilliseconds>`.

**Canonical request.** Six lines joined by `\n`:

```
<HTTPMethod>
<CanonicalURI>
<CanonicalQueryString>
<CanonicalHeaders>
<SignedHeaders>
<HashedPayload>
```

| Part | Rule |
| --- | --- |
| `HTTPMethod` | Upper case, as sent |
| `CanonicalURI` | The raw request path (§2.3) percent-decoded once, then re-encoded: every byte except `A-Z a-z 0-9 - _ . ~ /` becomes `%XX` with upper-case hex. No path normalisation and no double encoding: S3 is the one AWS service whose SigV4 canonical URI is encoded exactly once. An empty path is `/` |
| `CanonicalQueryString` | Every query parameter, including unknown ones such as `x-id` that aws-sdk-go-v2 adds; for presigned requests every parameter except `X-Amz-Signature`. Names and values are decoded from the raw query, re-encoded with the same rule as the URI but with `/` also encoded (so space is `%20`, never `+`), sorted by encoded name then encoded value, joined as `name=value` with `&`. A parameter without `=` contributes `name=` |
| `CanonicalHeaders` | For each name in `SignedHeaders`: the lower-case name, `:`, the value with leading and trailing whitespace removed and internal runs of spaces collapsed to one, then `\n`. Repeated headers are joined with `,` in arrival order. `host` comes from `r.Host` because Go removes `Host` from `r.Header`. A signed header absent from the request fails the signature |
| `SignedHeaders` | The `SignedHeaders` value as sent; it must be lower case, sorted and contain `host`, else `400 AuthorizationHeaderMalformed` |
| `HashedPayload` | The literal value of `x-amz-content-sha256` (a hex digest, `UNSIGNED-PAYLOAD` or a `STREAMING-...` token) |

**String to sign.** Four lines joined by `\n`:

```
AWS4-HMAC-SHA256
<x-amz-date>
<yyyymmdd>/<region>/s3/aws4_request
<lowercase hex SHA-256 of the canonical request>
```

**Signing key.**

```
kDate    = HMAC-SHA256("AWS4" + secret, yyyymmdd)
kRegion  = HMAC-SHA256(kDate, region)
kService = HMAC-SHA256(kRegion, "s3")
kSigning = HMAC-SHA256(kService, "aws4_request")
signature = lowercase hex HMAC-SHA256(kSigning, stringToSign)
```

The region in the key derivation is the one from the request's credential
scope, not `s3.region`. Signatures are compared with `hmac.Equal`. The
signing key may be cached per `(access key ID, date, region)` for at most 24
hours; the cache entry is dropped when the key is revoked or its secret is
rotated.

**Signature failure.** `403 SignatureDoesNotMatch`, message `The request
signature we calculated does not match the signature you provided. Check your
key and signing method.`, with `<AWSAccessKeyId>`, `<StringToSign>` and
`<CanonicalRequest>` elements, as AWS returns them for debugging. The provided
signature is never echoed (claude.md §8.4).

**Unknown key.** An access key ID that does not exist, is malformed, or is
revoked returns `403 InvalidAccessKeyId`, message `The AWS Access Key Id you
provided does not exist in our records.`, with `<AWSAccessKeyId>`.

### 4.3 Presigned query authentication

Query parameters:

| Parameter | Rule |
| --- | --- |
| `X-Amz-Algorithm` | `AWS4-HMAC-SHA256` |
| `X-Amz-Credential` | `<AKID>/<yyyymmdd>/<region>/s3/aws4_request`, URL-encoded |
| `X-Amz-Date` | ISO 8601 basic, `20261113T120000Z` |
| `X-Amz-Expires` | Integer seconds, 1 to 604800 (7 days) |
| `X-Amz-SignedHeaders` | Must include `host` |
| `X-Amz-Signature` | 64 lowercase hex characters |
| `X-Amz-Content-Sha256` | Optional; when present it is the `HashedPayload` |
| `X-Amz-Security-Token` | Not supported, `400 InvalidToken` |

Any missing required parameter, an `X-Amz-Expires` outside 1 to 604800
(message `X-Amz-Expires must be less than a week (in seconds) that is 604800`
when too large) or a malformed credential returns
`400 AuthorizationQueryParametersError`.

Canonical request: as §4.2, with `X-Amz-Signature` excluded from the
canonical query string and `HashedPayload` equal to the `X-Amz-Content-Sha256`
query value when present, otherwise the literal `UNSIGNED-PAYLOAD`. String to
sign and signing key: as §4.2, with the date from `X-Amz-Date`.

Time rules, evaluated with the server clock `now`:

| Condition | Response |
| --- | --- |
| `now > X-Amz-Date + X-Amz-Expires` | `403 AccessDenied`, message `Request has expired`, with `<X-Amz-Expires>`, `<Expires>` and `<ServerTime>` |
| `X-Amz-Date > now + 15 min` | `403 AccessDenied`, message `Request is not valid yet` |
| Otherwise | Signature is checked |

The 15-minute skew rule of header auth does not apply to presigned requests
beyond the not-yet-valid check. Any HTTP method can be presigned, including
PUT and UploadPart; the admin API's presign endpoint issues GET only
([admin-api.md](admin-api.md) §12.6). A presigned URL is valid only while its
access key is active: revocation makes every URL it signed fail with
`403 InvalidAccessKeyId`.

`sigv4.Presign` (§15.1) is the only presigning implementation; the admin
API's `Presigner` port is an adapter over it.

### 4.4 Payload signing modes

The value of `x-amz-content-sha256` selects the payload mode.

| `x-amz-content-sha256` | v1 | Body framing | Integrity |
| --- | --- | --- | --- |
| 64 lowercase hex characters | Supported | Plain, `Content-Length` required | SHA-256 of the body must equal the value (§4.7) |
| `UNSIGNED-PAYLOAD` | Supported | Plain, `Content-Length` required | None from SigV4; `Content-MD5` and `x-amz-checksum-*` still verified |
| `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` | Supported | aws-chunked with chunk signatures (§4.5) | Every chunk signature |
| `STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER` | Supported | aws-chunked with chunk signatures and a signed trailer (§4.6) | Every chunk signature, trailer signature, trailing checksum |
| `STREAMING-UNSIGNED-PAYLOAD-TRAILER` | Supported | aws-chunked without signatures, with a trailer (§4.6) | Trailing checksum |
| `STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD` | `501 NotImplemented` | | SigV4a |
| `STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD-TRAILER` | `501 NotImplemented` | | SigV4a |
| Anything else | `400 InvalidArgument`, `x-amz-content-sha256 must be UNSIGNED-PAYLOAD, STREAMING-..., or a valid sha256 value.` | | |

The trailer variants are required in v1, not optional: aws-sdk-go-v2 (from
January 2025), boto3 (from 1.36) and the AWS JS SDK v3 compute a CRC32
checksum by default and send streaming uploads as
`STREAMING-UNSIGNED-PAYLOAD-TRAILER` with `x-amz-trailer:
x-amz-checksum-crc32` over HTTPS.

Streaming modes are accepted only on PutObject and UploadPart. Any other
operation with a `STREAMING-...` value returns `400 InvalidRequest`.

Streaming request headers:

| Header | Rule |
| --- | --- |
| `x-amz-decoded-content-length` | Required; the object or part length after decoding. Absent: `411 MissingContentLength` |
| `Content-Length` | The encoded length. It may be absent only with `Transfer-Encoding: chunked`; the decoded length is what limits are checked against |
| `Content-Encoding` | `aws-chunked`, optionally followed by other codings (`aws-chunked,gzip`). It may be absent; the `x-amz-content-sha256` value alone selects the framing. `aws-chunked` is removed before the stored `Content-Encoding` is computed |
| `x-amz-trailer` | Required with the `-TRAILER` modes and forbidden otherwise (`400 InvalidRequest`). Exactly one name from §4.8 |

A plain (non-streaming) PutObject or UploadPart without `Content-Length`
returns `411 MissingContentLength`, as S3 does, including when the client uses
`Transfer-Encoding: chunked`.

### 4.5 aws-chunked with chunk signatures

Encoded body:

```
<hex size>;chunk-signature=<64 hex>\r\n
<size bytes of data>\r\n
...
0;chunk-signature=<64 hex>\r\n
\r\n
```

The final chunk has size `0`. Each chunk signature is:

```
string to sign =
AWS4-HMAC-SHA256-PAYLOAD
<x-amz-date>
<yyyymmdd>/<region>/s3/aws4_request
<previous signature>
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
<lowercase hex SHA-256 of this chunk's data>

signature = lowercase hex HMAC-SHA256(kSigning, string to sign)
```

The previous signature for the first chunk is the seed signature from the
`Authorization` header. The fifth line is the SHA-256 of the empty string.

Decoder rules (`internal/access/sigv4` `PayloadReader`):

| Rule | Failure |
| --- | --- |
| Chunk header line at most 4 KiB, hex size at most 16 digits | `400 InvalidRequest` |
| Chunk data at most 16 MiB. AWS requires at least 8 KiB for every chunk but the last; butterDB does not enforce the minimum | `400 InvalidRequest` |
| Chunk signature equals the computed one, checked before the chunk's bytes are released to the reader | `403 SignatureDoesNotMatch` |
| Sum of chunk sizes equals `x-amz-decoded-content-length` | `400 IncompleteBody` |
| Body ends before the final chunk | `400 IncompleteBody` |
| Bytes after the terminating `\r\n` | `400 InvalidRequest` |

A failure detected after spooling started aborts the job before any Telegram
send, because spooling completes before the first send (plan §7.1 step 8).

### 4.6 Trailers

**Signed trailer** (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER`): after the
signed final chunk `0;chunk-signature=<sig>\r\n`, the body carries:

```
x-amz-checksum-crc32:<base64 value>\r\n
x-amz-trailer-signature:<64 hex>\r\n
\r\n
```

```
trailer string to sign =
AWS4-HMAC-SHA256-TRAILER
<x-amz-date>
<yyyymmdd>/<region>/s3/aws4_request
<signature of the final chunk>
<lowercase hex SHA-256 of "x-amz-checksum-crc32:<base64 value>\n">
```

The canonical trailer is each trailer header as `name:value\n` (lower-case
name, `\n` not `\r\n`), in the order sent, excluding
`x-amz-trailer-signature`.

**Unsigned trailer** (`STREAMING-UNSIGNED-PAYLOAD-TRAILER`):

```
<hex size>\r\n
<data>\r\n
...
0\r\n
x-amz-checksum-crc32:<base64 value>\r\n
\r\n
```

No chunk or trailer signatures; the request itself is still SigV4-signed with
`HashedPayload` = `STREAMING-UNSIGNED-PAYLOAD-TRAILER`. Chunk extensions after
`;` are ignored in this mode.

Trailer rules, both variants:

| Rule | Failure |
| --- | --- |
| The trailer section holds exactly the header named by `x-amz-trailer` (plus `x-amz-trailer-signature` in the signed variant) | `400 InvalidRequest` |
| Trailer section at most 16 KiB | `400 InvalidRequest` |
| Trailer signature correct | `403 SignatureDoesNotMatch` |
| Trailing checksum equals the checksum computed over the decoded body | `400 BadDigest`, message `The <ALG> you specified did not match the calculated checksum.` |

### 4.7 `x-amz-content-sha256` verification

| Mode | Verification |
| --- | --- |
| Hex digest | The decoded body's SHA-256 is compared at EOF. Mismatch: `400 XAmzContentSHA256Mismatch` with `<ClientComputedContentSHA256>` and `<S3ComputedContentSHA256>`. The spool already computes SHA-256 for E8; the same digest is used |
| Hex digest on a request with no body (GET, HEAD, DELETE) | Must be the empty-string digest `e3b0c442...b855`; a non-empty body on these methods is ignored and not read |
| `UNSIGNED-PAYLOAD` | No body hash check |
| Presigned with no `X-Amz-Content-Sha256` | `UNSIGNED-PAYLOAD`; when the request also carries an `x-amz-content-sha256` header holding a hex digest, that digest is verified as above |
| Streaming modes | Covered by chunk and trailer verification |

The value is a lowercase hex string; an upper-case hex value fails the
signature (it is part of the canonical request) and is not normalised.

### 4.8 `Content-MD5` and `x-amz-checksum-*`

| Header | Applies to | Rule |
| --- | --- | --- |
| `Content-MD5` | Every request with a body | Base64 of a 16-byte MD5. Malformed or wrong length: `400 InvalidDigest` before the body is read. Mismatch after the body: `400 BadDigest` |
| `x-amz-checksum-crc32` | PutObject, UploadPart, DeleteObjects, CompleteMultipartUpload body | Base64 of the 4-byte big-endian CRC-32 (IEEE) |
| `x-amz-checksum-crc32c` | same | Base64 of the 4-byte big-endian CRC-32C (Castagnoli) |
| `x-amz-checksum-crc64nvme` | same | Base64 of the 8-byte big-endian CRC-64/NVME (`crc64.MakeTable(0x9a6c9329ac4bc9b5)`, standard library) |
| `x-amz-checksum-sha1` | same | Base64 of the 20-byte SHA-1 |
| `x-amz-checksum-sha256` | same | Base64 of the 32-byte SHA-256 |
| `x-amz-sdk-checksum-algorithm` | any | Informational; ignored |
| `x-amz-checksum-algorithm` | CreateMultipartUpload, PutObject | Names the algorithm the client will use; recorded for multipart |
| `x-amz-checksum-type` | CreateMultipartUpload | `COMPOSITE` or `FULL_OBJECT`; accepted, recorded, and only affects which value Complete verifies (§9.4) |

- At most one `x-amz-checksum-<alg>` value per request, whether in a header or
  a trailer. Two or more: `400 InvalidRequest`.
- A malformed base64 value or wrong decoded length: `400 InvalidRequest`.
- A mismatch: `400 BadDigest`, message `The <ALG> you specified did not match
  the calculated checksum.`
- The verified checksum is echoed on the PutObject and UploadPart response as
  the same header.
- Object checksums are not stored in the `t=put` op in v1. GetObject and
  HeadObject with `x-amz-checksum-mode: ENABLED` return no checksum header;
  SDKs skip response validation when the header is absent.
- Part checksums are recorded in the multipart index and verified against the
  checksums listed in CompleteMultipartUpload.
- DeleteObjects: AWS requires `Content-MD5` or an `x-amz-checksum-*` header.
  butterDB verifies either when present and does not require one. Deviation
  (§17).

### 4.9 Access keys

| Property | Value |
| --- | --- |
| Access key ID | 20 characters `[A-Z0-9]`, prefix `BDBK`, 16 random characters from `crypto/rand` (about 82 bits). Example `BDBK7Q2M9X4T1W8Z3R6Y` |
| Secret access key | 40 characters: 30 random bytes encoded with standard base64 (alphabet `A-Z a-z 0-9 + /`, no padding needed at 30 bytes) |
| Issued by | The admin API only ([admin-api.md](admin-api.md) §11); the secret is shown once |
| At rest | `control.db` through `internal/storage/keyrepo`, the secret sealed with a key derived from the server key file (A6). SigV4 needs the secret itself, so it is encrypted, not hashed |
| Status | `active` or `revoked`; revocation is effective for the next request, presigned URLs included |
| `last_used_at` | Updated at most once per 60 s per key; Default (owner may change) |
| Lookup | One read of `keyrepo` per request, served from an in-memory cache that `keyrepo` writes invalidate. Scope and status are never cached past an invalidation |

### 4.10 Scopes and action mapping

A key's scope is `{buckets, actions}` ([admin-api.md](admin-api.md) §11):
`buckets` is a list of bucket names or exactly `["*"]`; `actions` is a
non-empty subset of `read`, `list`, `write`, `delete` (`internal/domain/access`
`Action`). `internal/access/policy` evaluates every request.

| Action | Operations |
| --- | --- |
| `read` | GetObject, HeadObject, GetObjectAcl, GetObjectTagging; the source of CopyObject and UploadPartCopy |
| `list` | ListObjectsV2, ListObjects, HeadBucket, GetBucketLocation, ListMultipartUploads, ListParts, GetBucketVersioning, GetBucketPolicy, GetBucketPolicyStatus, GetBucketAcl, GetBucketCors, GetBucketEncryption, every other bucket configuration getter in §10 |
| `write` | PutObject, the destination of CopyObject and UploadPartCopy, CreateMultipartUpload, UploadPart, CompleteMultipartUpload, AbortMultipartUpload, PutObjectAcl, PutBucketAcl, PutObjectTagging, DeleteObjectTagging |
| `delete` | DeleteObject, DeleteObjects (checked per key) |
| any action on the bucket | ListBuckets lists the buckets in scope; a key with `["*"]` sees every bucket |
| none (admin API only) | CreateBucket, DeleteBucket (§6.4) |

Evaluation order for a signed request:

1. The bucket is outside the key's scope: `403 AccessDenied`, whether or not
   it exists, so keys cannot probe bucket names.
2. The bucket is in scope but does not exist: `404 NoSuchBucket`.
3. The operation's action is missing from the key: `403 AccessDenied`.
4. Missing key on an object read: `404 NoSuchKey` for any key holding `read`.
   AWS returns `403` when the caller lacks `s3:ListBucket`; butterDB returns
   `404` so that existence checks with read-only keys work. Deviation (§17).

### 4.11 Anonymous access

A bucket's policy is `private` or `public-read` (D7). Anonymous requests are
allowed only for:

| Operation | Condition |
| --- | --- |
| GetObject, HeadObject | Bucket policy is `public-read` |
| OPTIONS preflight | Always (§13) |

Everything else anonymous returns `403 AccessDenied`, including listing a
public-read bucket, ACL and policy getters, and any request naming a bucket
that does not exist or is private (no `NoSuchBucket` leak). Default (owner may
change): AWS's canned `public-read` bucket ACL also grants anonymous listing;
butterDB's public-read grants object reads only, matching the common
`s3:GetObject`-only public bucket policy.

Anonymous specifics:

- A missing key on a public-read bucket returns `404 NoSuchKey`. AWS returns
  `403` without `s3:ListBucket`; existence is observable through GET anyway.
  Deviation (§17).
- `response-*` query overrides on an anonymous request return
  `400 InvalidRequest`, `Request specific response headers cannot be used for
  anonymous GET requests.` (AWS behaviour.)
- Anonymous requests are rate-limited per client address (`X-Forwarded-For`
  only from `s3.trusted_proxies`) with a token bucket of `s3.anonymous_rate`
  per second and burst `s3.anonymous_burst`. Over the limit: `503 SlowDown`,
  `Retry-After: 1`, checked before the bucket lookup. Signed requests are never
  counted here.
- Responses carry `Cache-Control` per §13.6 (A8).

### 4.12 Authentication failure limiting

Failed authentications (`InvalidAccessKeyId`, `SignatureDoesNotMatch`) are
counted per client address (`X-Forwarded-For` only from
`s3.trusted_proxies`). After `s3.auth_failure_limit` failures in 10 minutes,
further requests from that address are answered `503 SlowDown` with
`Retry-After` equal to the remaining block, 15 minutes from the last failure,
without evaluating the signature. Successful requests are never limited here.
Default (owner may change).

---

## 5. Request processing order

Every request runs these steps in order. The first failure ends it.

| Step | Package | Failure |
| --- | --- | --- |
| 1. Parse target (bucket, key, virtual-hosted or path-style) and host check | `s3api/router` | `400 InvalidURI`, `400 InvalidRequest` |
| 2. OPTIONS: CORS preflight, then return | `s3api/router` | §13 |
| 3. Readiness | `s3api/router` | `503 ServiceUnavailable` |
| 4. Resolve the operation from method, target and subresource (§6.2) | `s3api/router` | `405 MethodNotAllowed`, `501 NotImplemented` |
| 5. Authenticate (§4) | `access/sigv4` | §4 codes |
| 6. Authorise (§4.10, §4.11) | `access/policy` | `403 AccessDenied`, `404 NoSuchBucket` |
| 7. Validate headers, key, metadata, put record cap and declared size | `s3api/objectops`, `s3api/multipart` | `400` codes in §14 |
| 8. Conditional checks that need only metadata | `readpath/httprange`, `s3api/objectops` | `304`, `412` |
| 9. Admission (writes) | `ingest/admission` | `503 SlowDown` |
| 10. Read the body through the payload decoder | `access/sigv4`, `ingest/spool` | `400`, `403` codes in §4 |
| 11. Execute | operation handler | operation codes |

CORS response headers (§13.4) and `x-amz-request-id` are added to every
response, errors included.

---

## 6. Operations

### 6.1 Operation table

Status: **Yes** supported as AWS documents it, with the limits stated;
**Partial** supported with the stated restriction; **Read-only** the getter
answers from butterDB's own configuration and the setter returns
`501 NotImplemented`; **Denied** recognised and always `403 AccessDenied` on
the S3 listener; **No** `501 NotImplemented`.

| Operation | Request | Action | v1 | Notes |
| --- | --- | --- | --- | --- |
| ListBuckets | `GET /` | any on bucket | Yes | Lists buckets in scope (§7.1) |
| CreateBucket | `PUT /<b>` | none | Denied | Buckets are created through the admin API; existing in-scope bucket gives `409 BucketAlreadyOwnedByYou` (§6.4) |
| HeadBucket | `HEAD /<b>` | `list` | Yes | §7.2 |
| DeleteBucket | `DELETE /<b>` | none | Denied | Admin API only (§6.4) |
| GetBucketLocation | `GET /<b>?location` | `list` | Yes | §3 |
| ListObjectsV2 | `GET /<b>?list-type=2` | `list` | Yes | §7.3 |
| ListObjects (v1) | `GET /<b>` | `list` | Yes | §7.4 |
| ListObjectVersions | `GET /<b>?versions` | | No | Buckets are unversioned |
| PutObject | `PUT /<b>/<k>` | `write` | Yes | §8.1 |
| PostObject (browser form upload) | `POST /<b>` multipart/form-data | | No | Use presigned PUT |
| GetObject | `GET /<b>/<k>` | `read` | Yes | §8.2 |
| HeadObject | `HEAD /<b>/<k>` | `read` | Yes | §8.3 |
| GetObjectAttributes | `GET /<b>/<k>?attributes` | | No | |
| DeleteObject | `DELETE /<b>/<k>` | `delete` | Yes | §8.4 |
| DeleteObjects | `POST /<b>?delete` | `delete` per key | Yes | §8.5 |
| CopyObject | `PUT /<b>/<k>` + `x-amz-copy-source` | `read` source, `write` destination | Yes | Server-side read and put, stored size within 1900 MiB (§8.6) |
| CreateMultipartUpload | `POST /<b>/<k>?uploads` | `write` | Yes | §9.1 |
| UploadPart | `PUT /<b>/<k>?partNumber=N&uploadId=U` | `write` | Yes | §9.2 |
| UploadPartCopy | as UploadPart + `x-amz-copy-source` | `read` source, `write` destination | Yes | §9.3 |
| CompleteMultipartUpload | `POST /<b>/<k>?uploadId=U` | `write` | Yes | Whitespace keepalive, error in a `200` body (§9.4, §11) |
| AbortMultipartUpload | `DELETE /<b>/<k>?uploadId=U` | `write` | Yes | §9.5 |
| ListParts | `GET /<b>/<k>?uploadId=U` | `list` | Yes | §9.6 |
| ListMultipartUploads | `GET /<b>?uploads` | `list` | Yes | §9.7 |
| GetBucketPolicy | `GET /<b>?policy` | `list` | Read-only | Synthesised from the canned policy (§10.1) |
| PutBucketPolicy, DeleteBucketPolicy | `PUT`/`DELETE /<b>?policy` | | No | Set `private` or `public-read` through the admin API |
| GetBucketPolicyStatus | `GET /<b>?policyStatus` | `list` | Read-only | §10.1 |
| GetBucketCors | `GET /<b>?cors` | `list` | Read-only | Deployment CORS configuration (§13) |
| PutBucketCors, DeleteBucketCors | `PUT`/`DELETE /<b>?cors` | | No | CORS is configured in `[s3]` |
| GetObjectAcl | `GET /<b>/<k>?acl` | `read` | Partial | Synthesised (§10.2) |
| PutObjectAcl | `PUT /<b>/<k>?acl` | `write` | Partial | Accepted only when it matches the effective ACL (§10.2) |
| GetBucketAcl | `GET /<b>?acl` | `list` | Partial | Synthesised (§10.2) |
| PutBucketAcl | `PUT /<b>?acl` | `write` | Partial | Accepted only when it matches the effective ACL (§10.2) |
| GetBucketVersioning | `GET /<b>?versioning` | `list` | Read-only | Always never-enabled (§10.3) |
| PutBucketVersioning | `PUT /<b>?versioning` | | No | |
| GetBucketEncryption | `GET /<b>?encryption` | `list` | Read-only | From the bucket's encryption default (§10.4) |
| PutBucketEncryption, DeleteBucketEncryption | | | No | Admin API |
| GetObjectTagging | `GET /<b>/<k>?tagging` | `read` | Partial | Always an empty tag set (§10.5) |
| PutObjectTagging | `PUT /<b>/<k>?tagging` | `write` | Partial | Empty tag set accepted as a no-op; tags `501` |
| DeleteObjectTagging | `DELETE /<b>/<k>?tagging` | `write` | Partial | `204` no-op |
| Other bucket configuration getters | §10.6 | `list` | Read-only | Fixed answers |
| Every other S3 operation | | | No | `501 NotImplemented` |

### 6.2 Dispatch

`s3api/router` resolves the operation from the method, whether a key is
present and the first matching subresource parameter. Query parameters that
are not subresources (`x-id`, `response-*`, listing parameters, `X-Amz-*`)
never change the operation. Subresource matching is by parameter name,
with or without a value (`?acl` and `?acl=` are the same).

| Method | Target | Subresource or header | Operation |
| --- | --- | --- | --- |
| GET | service | | ListBuckets |
| other | service | | `405 MethodNotAllowed` |
| GET | bucket | `location` | GetBucketLocation |
| GET | bucket | `uploads` | ListMultipartUploads |
| GET | bucket | `versions` | `501` |
| GET | bucket | `versioning`, `acl`, `policy`, `policyStatus`, `cors`, `encryption`, `tagging`, `lifecycle`, `website`, `logging`, `requestPayment`, `object-lock`, `publicAccessBlock`, `ownershipControls`, `notification`, `accelerate` | the getter (§10) |
| GET | bucket | any other subresource (`replication`, `analytics`, `inventory`, `metrics`, `intelligent-tiering`, `metadataTable`, `session`) | `501` |
| GET | bucket | `list-type=2` | ListObjectsV2 |
| GET | bucket | `list-type` other than `2` | `400 InvalidArgument` |
| GET | bucket | none | ListObjects |
| HEAD | bucket | | HeadBucket |
| PUT | bucket | `acl` | PutBucketAcl |
| PUT | bucket | any other subresource | `501` |
| PUT | bucket | none | CreateBucket |
| DELETE | bucket | any subresource | `501` |
| DELETE | bucket | none | DeleteBucket |
| POST | bucket | `delete` | DeleteObjects |
| POST | bucket | none or other | `501` (PostObject) |
| GET | object | `uploadId` | ListParts |
| GET | object | `acl` | GetObjectAcl |
| GET | object | `tagging` | GetObjectTagging |
| GET | object | `attributes`, `torrent`, `retention`, `legal-hold` | `501` |
| GET | object | none | GetObject |
| HEAD | object | | HeadObject |
| PUT | object | `partNumber` and `uploadId`, with `x-amz-copy-source` | UploadPartCopy |
| PUT | object | `partNumber` and `uploadId` | UploadPart |
| PUT | object | `acl` | PutObjectAcl |
| PUT | object | `tagging` | PutObjectTagging |
| PUT | object | `retention`, `legal-hold` | `501` |
| PUT | object | `x-amz-copy-source` header | CopyObject |
| PUT | object | none | PutObject |
| DELETE | object | `uploadId` | AbortMultipartUpload |
| DELETE | object | `tagging` | DeleteObjectTagging |
| DELETE | object | none | DeleteObject |
| POST | object | `uploads` | CreateMultipartUpload |
| POST | object | `uploadId` | CompleteMultipartUpload |
| POST | object | `restore`, `select` | `501` |
| POST | object | none | `405 MethodNotAllowed` |
| OPTIONS | any | | CORS preflight |
| PATCH, other | any | | `405 MethodNotAllowed` |

Only one of `partNumber`/`uploadId` present on PUT returns
`400 InvalidArgument`.

### 6.3 Unsupported request features

These are refused on any operation that would otherwise accept them:

| Request feature | Response |
| --- | --- |
| `x-amz-server-side-encryption-customer-*` (SSE-C) | `501 NotImplemented` |
| `x-amz-server-side-encryption-aws-kms-key-id`, `x-amz-server-side-encryption-context`, `x-amz-server-side-encryption-bucket-key-enabled: true` | `501 NotImplemented` |
| `x-amz-server-side-encryption` other than `AES256` | `400 InvalidArgument`, `Server Side Encryption with <value> is not supported; use AES256` (plan §7.1 step 5) |
| `x-amz-object-lock-mode`, `x-amz-object-lock-retain-until-date`, `x-amz-object-lock-legal-hold` | `501 NotImplemented` |
| `x-amz-website-redirect-location` | `501 NotImplemented` |
| `x-amz-tagging` with a non-empty value | `501 NotImplemented` |
| `x-amz-grant-*` | `400 AccessControlListNotSupported` |
| `x-amz-acl` | §10.2 |
| `x-amz-storage-class` other than `STANDARD` | `400 InvalidStorageClass` |
| `versionId` query other than `null` | `400 InvalidArgument`, `Invalid version id specified` |
| `x-amz-expected-bucket-owner`, `x-amz-request-payer`, `x-amz-sdk-*` | Ignored |

`versionId=null` is accepted and treated as absent, matching S3 on an
unversioned bucket.

### 6.4 CreateBucket and DeleteBucket

Buckets are created and deleted only through the admin API, which owns the
registry channel writes and the bucket limit ([admin-api.md](admin-api.md)
§10, D3). On the S3 listener:

| Request | Response |
| --- | --- |
| CreateBucket for a bucket that exists and is in the key's scope | `409 BucketAlreadyOwnedByYou`. `rclone`, the AWS CLI and `s3cmd` treat this as success |
| CreateBucket for any other name | `403 AccessDenied`, `Bucket creation is available only through the butterDB admin API` |
| DeleteBucket | `403 AccessDenied`, `Bucket deletion is available only through the butterDB admin API` |

`BucketNotEmpty` is therefore never produced by the S3 listener in v1; the
admin API returns `bucket_not_empty` instead. AWS us-east-1 returns `200` for
a repeated CreateBucket by the owner; butterDB returns `409` as every other
AWS region does.

---

## 7. Bucket and listing operations

All XML responses: `Content-Type: application/xml`, body starting with
`<?xml version="1.0" encoding="UTF-8"?>`, root element in the S3 namespace.
Timestamps in XML are `2006-01-02T15:04:05.000Z` (UTC, milliseconds); HTTP
date headers are `http.TimeFormat`.

### 7.1 ListBuckets

Query parameters: `max-buckets` (1 to 10000, default all), `continuation-token`,
`prefix`, `bucket-region` (accepted when it equals `s3.region`, else the
result is empty).

```xml
<?xml version="1.0" encoding="UTF-8"?>
<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Owner><ID>5d0a…64 hex…</ID><DisplayName>butterdb</DisplayName></Owner>
  <Buckets>
    <Bucket><Name>photos</Name><CreationDate>2026-11-12T09:41:00.000Z</CreationDate><BucketRegion>us-east-1</BucketRegion></Bucket>
  </Buckets>
</ListAllMyBucketsResult>
```

- Sorted by name. `ContinuationToken` (when paginated) is base64url of the
  last name returned; `Prefix` is echoed when given.
- `CreationDate` is the date of the bucket's `t=bcreate` message.
- **Owner ID.** One per deployment: lowercase hex of
  HMAC-SHA256(server key, `butterdb/s3/owner-id/v1`), 64 characters, computed
  once at startup by `internal/crypto/keyring`. `DisplayName` is `butterdb`.
  The same owner appears in every `Owner`, `Initiator` (as owner) and ACL in
  this document.
- Anonymous: `403 AccessDenied`.

### 7.2 HeadBucket

`200` with `x-amz-bucket-region: <s3.region>` and no body. Errors carry no
body: `404` (NoSuchBucket), `403` (AccessDenied), `400`, `503`, as AWS does
for HEAD.

### 7.3 ListObjectsV2

| Parameter | Rule |
| --- | --- |
| `list-type` | `2` |
| `prefix` | Any UTF-8; empty means none |
| `delimiter` | Any non-empty UTF-8 string up to 1024 bytes; multi-character delimiters are supported, as in S3 |
| `max-keys` | 0 to 1000; larger values are clamped to 1000; default 1000; negative or non-integer: `400 InvalidArgument` |
| `start-after` | Return keys strictly after this key; ignored when `continuation-token` is present |
| `continuation-token` | Opaque token from `NextContinuationToken`; malformed: `400 InvalidArgument`, `The continuation token provided is incorrect` |
| `fetch-owner` | `true` adds `Owner` to each `Contents` |
| `encoding-type` | `url` or absent; other values `400 InvalidArgument` |

Semantics:

- Keys are ordered by UTF-8 byte order (SQLite `BINARY` collation in
  `internal/storage/objectrepo`).
- With a delimiter, every key that contains the delimiter after the prefix is
  rolled up into one `CommonPrefixes` entry: the key up to and including the
  first occurrence of the delimiter after the prefix. Rolled-up keys are not
  listed individually.
- `max-keys` counts `Contents` and `CommonPrefixes` together. `KeyCount` is
  their sum.
- `max-keys=0` returns `KeyCount` 0 and `IsTruncated` false.
- When truncated, `NextContinuationToken` resumes after the last emitted
  entry. If the last entry was a common prefix `P`, the next page starts at the
  first key that does not begin with `P`.
- `Size` is the plaintext size; `ETag` is quoted (§8.8); `StorageClass` is
  always `STANDARD`; `LastModified` is the put message date (§8.9).

Continuation token: `base64.RawURLEncoding` of the byte `0x01` (version), one
kind byte (`0x00` last key, `0x01` last common prefix), then the UTF-8 bytes
of that key or prefix. A token is not bound to `prefix` or `delimiter`;
changing them between pages gives well-defined but unspecified results, as in
S3.

`encoding-type=url`: `Key`, `Prefix`, `Delimiter`, `StartAfter` and each
`CommonPrefixes/Prefix` in the response are encoded with `url.QueryEscape`
(space is `+`, `+` is `%2B`), which is what S3 returns and what botocore and
the AWS CLI decode with `unquote_plus`. `EncodingType` is echoed.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>photos</Name>
  <Prefix>2026/</Prefix>
  <Delimiter>/</Delimiter>
  <MaxKeys>2</MaxKeys>
  <KeyCount>2</KeyCount>
  <IsTruncated>true</IsTruncated>
  <NextContinuationToken>AQEyMDI2LzExLw</NextContinuationToken>
  <Contents>
    <Key>2026/cover.jpg</Key>
    <LastModified>2026-11-12T09:41:00.000Z</LastModified>
    <ETag>"9b2cf535f27731c974343645a3985328"</ETag>
    <Size>482113</Size>
    <StorageClass>STANDARD</StorageClass>
  </Contents>
  <CommonPrefixes><Prefix>2026/11/</Prefix></CommonPrefixes>
</ListBucketResult>
```

Elements: `Name`, `Prefix`, `Delimiter` (when given), `MaxKeys`, `KeyCount`,
`IsTruncated`, `ContinuationToken` (when given), `NextContinuationToken`
(when truncated), `StartAfter` (when given), `EncodingType` (when given),
`Contents*`, `CommonPrefixes*`. `ETag` text is the quoted ETag; Go's
`encoding/xml` escapes the quotes as `&#34;`, which every XML parser reads
back as `"`.

### 7.4 ListObjects (v1)

Parameters `prefix`, `delimiter`, `max-keys`, `encoding-type` as §7.3, plus
`marker` (keys strictly after it). Response root `ListBucketResult` with
`Name`, `Prefix`, `Marker` (always present, empty when not given),
`Delimiter`, `MaxKeys`, `IsTruncated`, `NextMarker`, `EncodingType`,
`Contents*` and `CommonPrefixes*`.

- `Contents` always includes `Owner`, as in S3 v1.
- `NextMarker` is returned only when the response is truncated **and** a
  delimiter was given, exactly as S3 does. Without a delimiter, clients use the
  last `Key` as the next `marker`. When a delimiter was given and the last
  entry is a common prefix, `NextMarker` is that prefix, and a request with
  that marker skips every key beginning with it.
- With `encoding-type=url`, `Marker` and `NextMarker` are encoded too.

### 7.5 Key characters in XML

Keys containing U+0000 to U+001F or U+007F are refused at write time with
`400 InvalidArgument` (§8.7), so every stored key can be listed as XML 1.0
without `encoding-type=url`. AWS stores such keys and emits them as character
references that XML 1.0 parsers reject. Deviation (§17).

---

## 8. Object operations

### 8.1 PutObject

Request headers:

| Header | Rule |
| --- | --- |
| `Content-Length` or `x-amz-decoded-content-length` | Required (§4.4). Stored size (`format.CiphertextSize` when encrypting) above `limits.MaxStoredObjectBytes`: `400 EntityTooLarge` with `<ProposedSize>` and `<MaxSizeAllowed>1992294400</MaxSizeAllowed>`, before the body is read |
| `Content-Type` | Stored. Absent: `application/octet-stream` (AWS stores `binary/octet-stream`; deviation, §17). At most 1024 bytes |
| `Cache-Control`, `Content-Disposition`, `Content-Encoding` (minus `aws-chunked`), `Content-Language`, `Expires` | Stored verbatim and returned on GET and HEAD. Each at most 1024 bytes; over: `400 InvalidArgument` |
| `x-amz-meta-*` | User metadata (§8.7) |
| `Content-MD5`, `x-amz-checksum-*` | §4.8 |
| `x-amz-server-side-encryption` | §8.10 |
| `x-amz-acl` | §10.2 |
| `x-amz-storage-class` | `STANDARD` or absent |
| `If-None-Match` | `*` only: write only if the key does not exist; else `412 PreconditionFailed`. Any other value: `400 InvalidArgument` |
| `If-Match` | Write only if the current object's ETag matches; no object or another ETag: `412 PreconditionFailed` |
| `Expect: 100-continue` | Honoured; Go sends `100 Continue` on the first body read, which happens only after step 9 of §5 |

Flow: plan §7.1. Write preconditions are checked twice: once before
spooling, so an obvious failure costs no upload, and authoritatively inside
`ingest/put` under the per-key commit lock immediately before the `t=put`
send, which is the same mechanism the admin API's upload uses
([admin-api.md](admin-api.md) §12.4). A failure at the second check deletes
the already-sent body message and returns `412`.

Response `200`, empty body, headers: `ETag: "<md5 hex>"`,
`x-amz-server-side-encryption: AES256` when encrypted, the echoed
`x-amz-checksum-<alg>` when one was supplied. No `x-amz-version-id`.

A client that disconnects after the body is complete does not cancel the
commit; the object appears (plan §7.1 failure table). A client that
disconnects before the body is complete leaves nothing in Telegram.

Zero-byte objects and keys ending in `/` (folder markers) are ordinary
objects.

### 8.2 GetObject

Served by `internal/readpath/httprange` and `objectreader` (plan §7.2).

**Range.**

| `Range` header | Response |
| --- | --- |
| Absent | `200`, full object |
| `bytes=a-b`, `bytes=a-`, `bytes=-n`, satisfiable | `206` with `Content-Range: bytes a-b/size`; `b` past the end is clamped; `-n` larger than the object returns the whole object as `206` |
| Single range, unsatisfiable (`a >= size`, or any range on a zero-byte object) | `416 InvalidRange` with `Content-Range: bytes */size` and `<RangeRequested>` and `<ActualObjectSize>` elements |
| More than one range (`bytes=0-1,5-6`) | `200`, full object, as S3 does (S3 does not support multipart byteranges) |
| Syntactically invalid, or a unit other than `bytes` | Ignored; `200`, full object (RFC 9110) |

`Accept-Ranges: bytes` is always sent. Ranges are over plaintext.
`readpath/httprange` ports `parseRangeHeader` from TDrive
`backend/media/server.go` and changes two behaviours: a multi-range header
yields the full object instead of an error, and an invalid header is ignored
instead of answered with `416`.

**`partNumber`.** `partNumber=1` returns the whole object as `206` with
`Content-Range` covering it, without `x-amz-mp-parts-count`. Any other part
number returns `416 InvalidPartNumber`. Part boundaries of multipart uploads
are not kept after assembly. With no `x-amz-mp-parts-count`, the
aws-sdk-go-v2 transfer manager treats the object as one part. `partNumber`
together with `Range`: `400 InvalidRequest`.

**Conditionals**, evaluated before `Range`, in this order:

| Step | Header | Outcome |
| --- | --- | --- |
| 1 | `If-Match` | ETag not matched (strong comparison; `*` matches any existing object): `412 PreconditionFailed`. When present and matched, step 2 is skipped |
| 2 | `If-Unmodified-Since` | `Last-Modified` after the date: `412 PreconditionFailed` |
| 3 | `If-None-Match` | ETag matched (weak comparison; `*` matches): `304 Not Modified`. When present and not matched, step 4 is skipped |
| 4 | `If-Modified-Since` | `Last-Modified` not after the date: `304 Not Modified` |
| 5 | `If-Range` | ETag (strong) or HTTP-date equal to the object's: honour `Range`; otherwise ignore `Range` and return `200` |

This yields the two cases AWS documents: `If-Match` true with
`If-Unmodified-Since` false returns `200`; `If-None-Match` false with
`If-Modified-Since` true returns `304`.

- ETags in conditions are accepted with or without quotes; AWS accepts both.
  TDrive's `consumeEntityTag` (`backend/mountdav/conditions.go`) requires
  quotes and is adapted accordingly.
- Dates are parsed with `http.ParseTime`. An unparseable date, or a date in
  the future, makes that header ignored.
- Comparison is at one-second precision.
- `304` carries `ETag`, `Last-Modified`, `Cache-Control`, `Expires` and no
  body. `412` is an S3 error XML (`PreconditionFailed`, `<Condition>` naming
  the header).

**Response overrides.** Signed and presigned requests only (§4.11):

| Query | Overrides |
| --- | --- |
| `response-content-type` | `Content-Type` |
| `response-content-language` | `Content-Language` |
| `response-expires` | `Expires` |
| `response-cache-control` | `Cache-Control` |
| `response-content-disposition` | `Content-Disposition` |
| `response-content-encoding` | `Content-Encoding` |

Each value is at most 1024 bytes and must contain no control characters
(`400 InvalidArgument`). Overrides apply to `200` and `206` responses.

**Response headers.** `Content-Type`, `Content-Length` (plaintext bytes
sent), `Content-Range` (on `206`), `ETag`, `Last-Modified`, `Accept-Ranges`,
the stored system headers of §8.1, `x-amz-meta-*` (§8.7),
`x-amz-server-side-encryption: AES256` when encrypted, `Cache-Control` per
§13.6. Never sent: `x-amz-version-id`, `x-amz-storage-class` (S3 omits it for
`STANDARD`), `x-amz-tagging-count`, checksum headers.

**Read failures.** If no body byte can be produced within
`s3.request_budget` because of `getFile` slot waits or a Telegram flood wait,
the response is `503 SlowDown` with `Retry-After` (the remaining flood wait,
rounded up, at least 1 s). A non-transient Telegram failure before
headers is `500 InternalError` (from phase 12, the replica is tried first).
After headers are sent, any failure, including a TDE1 chunk authentication
failure, aborts the connection; the client sees a short body.

### 8.3 HeadObject

Same as GetObject without a body: same conditionals (`304`, `412`), same
`Range` and `partNumber` handling (a satisfiable range returns `206` with
`Content-Range` and the range's `Content-Length`), same headers. Errors have
no body: the status line and `x-amz-request-id` only. `response-*` overrides
are honoured.

### 8.4 DeleteObject

Flow: plan §7.3. `204 No Content`, also when the key does not exist (no
Telegram traffic then). `versionId=null` is accepted. A tombstone send whose
limiter or flood wait does not fit `s3.request_budget` returns
`503 SlowDown`; nothing was
committed and the object still exists. An unknown tombstone send outcome
returns `503 SlowDown`; the sweeper reconciles and the client's retry is
idempotent.

### 8.5 DeleteObjects

`POST /<b>?delete`, body:

```xml
<Delete xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Quiet>true</Quiet>
  <Object><Key>2026/a.jpg</Key></Object>
  <Object><Key>2026/b.jpg</Key><VersionId>null</VersionId></Object>
</Delete>
```

| Rule | Failure |
| --- | --- |
| Body at most 2 MiB | `400 MaxMessageLengthExceeded` |
| Empty body | `400 MissingRequestBodyError` |
| 1 to 1000 `Object` entries | `400 MalformedXML` |
| Namespace optional; DOCTYPE or unknown root | `400 MalformedXML` |
| `Content-MD5` or `x-amz-checksum-*` | Verified when present; not required (§4.8) |

Per-key handling, in body order, through the same path as DeleteObject,
batching Telegram message deletions across keys (plan §7.3):

| Per-key situation | Result entry |
| --- | --- |
| Deleted, or did not exist | `Deleted` (omitted in quiet mode) |
| Key fails validation (§8.7) | `Error` with `InvalidArgument` or `KeyTooLongError` |
| `VersionId` other than `null` | `Error` with `InvalidArgument` |
| Key lacks the `delete` action | `Error` with `AccessDenied` |
| Limiter or flood wait past the remaining `s3.request_budget` | `Error` with `SlowDown` for this and every later key |
| Unknown send outcome or internal failure | `Error` with `SlowDown` or `InternalError` |

```xml
<?xml version="1.0" encoding="UTF-8"?>
<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Deleted><Key>2026/a.jpg</Key></Deleted>
  <Error><Key>2026/b.jpg</Key><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>
</DeleteResult>
```

The status is `200` whenever the request itself is valid, whatever the
per-key results. Quiet mode lists only `Error` entries.

The whole request shares one `s3.request_budget`. Each key costs one
`t=del` tombstone, a `message`-class send under the E10 limiter (20 per minute,
burst 5, by default; [operations.md](operations.md) §13.3), so one request
deletes about 15 existing keys before the budget runs out at the defaults. Keys
that do not exist cost nothing and always report `Deleted`. Every key after the
budget is spent reports `SlowDown`; clients re-send those keys. The AWS CLI
(`aws s3 rm --recursive`) prints `delete failed` for them and exits `1`, and a
second run continues. Raising the limiter is an owner decision based on
`tgprobe` results.

### 8.6 CopyObject

Decision: CopyObject is supported as a server-side read of the source through
`readpath/objectreader` (decrypting when needed) streamed into a new put job
through `ingest/spool` and `ingest/put` (re-encrypting under the destination's
encryption), never as a Telegram forward of the source document. Every copy is
a full new object with its own body and put messages.

| Header | Rule |
| --- | --- |
| `x-amz-copy-source` | `<bucket>/<key>` or `/<bucket>/<key>`, URL-encoded, optional `?versionId=null`. Malformed: `400 InvalidArgument`. Source bucket outside scope or without `read`: `403 AccessDenied` |
| `x-amz-metadata-directive` | `COPY` (default): content type, system headers and user metadata come from the source. `REPLACE`: from this request. Other: `400 InvalidArgument` |
| `x-amz-tagging-directive` | `COPY` or `REPLACE` with an empty `x-amz-tagging`: accepted. Tags: `501` |
| `x-amz-copy-source-if-match`, `-if-none-match`, `-if-modified-since`, `-if-unmodified-since` | Evaluated against the source as §8.2; failure is `412 PreconditionFailed` (S3 uses `412` for all four on copy, not `304`) |
| `x-amz-server-side-encryption` | Destination encryption; absent means the destination bucket's default. The source's encryption is never inherited, as in S3 |
| `If-Match`, `If-None-Match` (destination) | `501 NotImplemented` in v1 |
| `x-amz-copy-source-server-side-encryption-customer-*` | `501 NotImplemented` |

Rules:

- Copying an object onto itself with `COPY` directive and no change to
  encryption returns `400 InvalidRequest`, `This copy request is illegal
  because it is trying to copy an object to itself without changing the
  object's metadata, storage class, website redirect location or encryption
  attributes.` (S3 behaviour.)
- The destination's stored size is computed before any read: source
  plaintext size, through `format.CiphertextSize` when the destination is
  encrypted. Over the limit: `400 EntityTooLarge`. A plaintext source just
  under 1900 MiB copied into an encrypted destination can fail this check
  (claude.md §10 invariant 3).
- The destination's put record cap (§8.7) is checked before any read.
- The new ETag is the MD5 of the content, computed by the spool, even when
  the source has a multipart ETag; S3 also returns a non-multipart ETag for a
  single CopyObject of a multipart source.

Response: once validation passes, the long-response protocol of §11 starts
(`200`, XML declaration, whitespace every `s3.keepalive_interval`). Success
body:

```xml
<CopyObjectResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <ETag>"3858f62230ac3c915f300c664312c11f"</ETag>
  <LastModified>2026-11-13T10:02:11.000Z</LastModified>
</CopyObjectResult>
```

Failure after the `200` status: an `Error` document in the body (§11).
`x-amz-server-side-encryption` is sent with the `200` headers, because the
destination encryption is decided during validation.

### 8.7 Keys and metadata

**Keys.**

| Rule | Failure |
| --- | --- |
| Valid UTF-8 | `400 InvalidArgument`, `Object key is not valid UTF-8` |
| 1 to 1024 bytes | Empty: bucket operation; longer: `400 KeyTooLongError`, `Your key is too long` with `<Size>` and `<MaxSizeAllowed>1024</MaxSizeAllowed>` |
| No U+0000 to U+001F, no U+007F | `400 InvalidArgument`, `Object key contains a control character` (§7.5) |

**User metadata.**

- Request headers `x-amz-meta-<name>`. Names are stored lower case, without
  the prefix. Repeated headers with the same name are joined with `,`.
- Values encoded as RFC 2047 (`=?UTF-8?B?...?=` or `=?UTF-8?Q?...?=`) are
  decoded with `mime.WordDecoder` before storing. On GET and HEAD, a value
  containing non-ASCII bytes is returned RFC 2047 B-encoded with
  `mime.BEncoding`, which is what S3 does.
- Size: the sum over entries of the UTF-8 byte length of the name (without
  `x-amz-meta-`) and of the value, after decoding, must be at most 2048 bytes
  (`limits.MaxUserMetadataBytes`). Over: `400 MetadataTooLarge` with `<Size>`
  and `<MaxSizeAllowed>2048</MaxSizeAllowed>`. System headers (§8.1) do not
  count toward the 2048 bytes, as in S3.
- A name must be a valid HTTP token; anything else is `400 InvalidArgument`.

**The put record cap.** An object is committed by one `t=put` Telegram text
message that carries the key, user metadata and system headers, all
URL-escaped (E2, [storage-model.md](storage-model.md)). Telegram limits a
message to 4096 characters, so the formatted message text, header line plus
any remainder, must satisfy `len(wire.Format(op)) <= limits.MaxPutOpChars`
(4096). The check runs in step 7 of §5, before admission and before any byte
is spooled, for PutObject, CopyObject and CreateMultipartUpload (so
CompleteMultipartUpload cannot fail on it). On failure:

| Condition | Response |
| --- | --- |
| The op formatted without user metadata and system headers already exceeds 4096 | `400 KeyTooLongError` |
| Otherwise | `400 MetadataTooLarge`, message `The encoded object record is <n> characters; the limit is 4096`, with `<Size>` and `<MaxSizeAllowed>4096</MaxSizeAllowed>` |

Escaping makes each byte outside `A-Z a-z 0-9 - _ . ~` cost three characters,
so a 1024-byte key of non-ASCII text alone uses 3072 characters and keys with
many `/` grow by two characters per slash. What clients can rely on: a key of
up to 1024 bytes and user metadata of up to 2048 bytes always fit when both,
plus the system headers, consist of characters from `A-Z a-z 0-9 - _ . ~`,
there are at most 16 metadata entries, and the system headers total at most
384 bytes. This guarantee requires that [storage-model.md](storage-model.md)
keeps the fixed part of the `t=put` op at most 512 characters and the
per-entry metadata overhead at most 4 characters. Deviation (§17, plan R4).

### 8.8 ETag rules

| Object origin | ETag |
| --- | --- |
| PutObject | `"<32 lowercase hex MD5 of the plaintext>"` |
| CompleteMultipartUpload | `"<32 lowercase hex MD5 of the concatenated 16-byte binary MD5s of the listed parts, in order>-<part count>"` |
| CopyObject | `"<MD5 of the plaintext>"` |
| UploadPart, UploadPartCopy | `"<MD5 of the part bytes>"` |

- Encrypted objects (TDE1) have the plaintext MD5 as ETag, as S3 does for
  SSE-S3. The ETag never depends on the ciphertext.
- The `t=put` op stores the ETag string without quotes, including any `-N`
  suffix ([storage-model.md](storage-model.md)); responses add the quotes.
- ETags in XML (`ETag` elements) and headers are quoted.

### 8.9 Last-Modified

`Last-Modified` is the date of the object's `t=put` message (Telegram's server
time, one-second precision, UTC), the moment of the commit (D8). Two
overwrites in the same second share `Last-Modified` and differ in ETag.

### 8.10 Server-side encryption (D11, A5)

| Request `x-amz-server-side-encryption` | Bucket encryption default | Stored as |
| --- | --- | --- |
| absent | `none` | Plaintext |
| absent | `tde1` | TDE1 |
| `AES256` | either | TDE1 |
| `aws:kms`, `aws:kms:dsse`, other | either | `400 InvalidArgument` (§6.3) |

- Encryption is resolved at PutObject, CopyObject (destination) and
  CreateMultipartUpload; UploadPart and Complete use the upload's decision.
- There is no way to request plaintext storage in a `tde1` bucket, as S3 has
  no such header.
- An `AES256` request in a bucket that has never had an encryption key
  requires the bucket's data encryption key to exist. `internal/buckets`
  creates it on first need with a single `t=encfg` registry commit under a
  per-bucket lock; its waits count against the request budget (§12.1).
- Every response describing an encrypted object (PutObject, GetObject,
  HeadObject, CopyObject, CreateMultipartUpload, UploadPart, UploadPartCopy,
  CompleteMultipartUpload) carries `x-amz-server-side-encryption: AES256`.
  Plaintext objects carry no such header.
- Encryption keys are held by the server (A6); an encrypted object is
  readable by any request authorised to read it.

---

## 9. Multipart upload

S3 multipart is a client-side upload protocol here: parts are staged on disk
by `internal/ingest/multipartstage` and assembled into one Telegram document
at Complete (D9, plan §7.4). The TDX1 `t=part`/`t=manifest` ops are not used.

| Limit | Value |
| --- | --- |
| Part numbers | 1 to 10000; other: `400 InvalidArgument`, `Part number must be an integer between 1 and 10000, inclusive` |
| Minimum part size | 5 MiB (5,242,880 bytes) for every listed part except the last, checked at Complete |
| Maximum part size | `limits.MaxStoredObjectBytes` |
| Total | Stored size of the sum of staged parts (each part number counted once at its latest size, through `format.CiphertextSize` when encrypted) at most `limits.MaxStoredObjectBytes`; checked at every UploadPart before its body is read and again at Complete for the listed parts |
| Maximum encrypted plaintext | 1,991,808,062 bytes (the largest plaintext whose TDE1 size is at most 1,992,294,400; `internal/crypto/format` is authoritative) |
| Upload ID | 32 lowercase hex characters from `crypto/rand` |
| Idle expiry | `s3.multipart_idle_expiry` (7 days) |

The multipart index in `control.db` (owned by `internal/ingest/multipartstage`)
holds, per upload: upload ID, bucket, key, initiating access key ID, initiated
time, content type, system headers, user metadata, encryption decision,
checksum algorithm and type, state (`open`, `committing`, `completed`,
`aborted`) and, once completed, the ETag, put message ID and a SHA-256 of the
completed part list; per part: number, size, MD5, checksum, staged time.

### 9.1 CreateMultipartUpload

Accepts the same headers as PutObject except the body headers. Validates key,
metadata, the put record cap (§8.7) and encryption up front. Response `200`:

```xml
<InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Bucket>photos</Bucket>
  <Key>video/clip.mp4</Key>
  <UploadId>4c1f0a9e7d2b6e8a3f5c1d0b9a7e6f2c</UploadId>
</InitiateMultipartUploadResult>
```

Headers: `x-amz-server-side-encryption` when encrypted,
`x-amz-checksum-algorithm` and `x-amz-checksum-type` when requested.

### 9.2 UploadPart

- Unknown upload ID, or an upload in another bucket or key: `404 NoSuchUpload`.
  Upload in state `committing`: `409 OperationAborted`. State `completed` or
  `aborted`: `404 NoSuchUpload`.
- Body framing, `Content-MD5` and checksums as PutObject (§4.4 to §4.8).
- The part is streamed to `multipart/<upload id>/<part>.part.tmp`, fsynced,
  renamed over `<part>.part`, and recorded; a re-upload of a part number
  replaces it.
- Spool space is reserved through `ingest/admission` before the body is read;
  no reservation within `s3.request_budget`: `503 SlowDown`.
- Response `200`, headers `ETag: "<part md5>"`, the echoed checksum header,
  `x-amz-server-side-encryption` when the upload is encrypted.

Any key holding `write` on the bucket may upload parts to, complete or abort
an upload, whichever key created it, as in S3.

### 9.3 UploadPartCopy

Decision: supported. The AWS CLI uses it for every S3-to-S3 copy above its
8 MiB multipart threshold.

| Header | Rule |
| --- | --- |
| `x-amz-copy-source` | As CopyObject (§8.6) |
| `x-amz-copy-source-range` | `bytes=first-last`, inclusive, within the source's plaintext size; absent copies the whole source. Malformed or out of range: `400 InvalidArgument`, `The x-amz-copy-source-range value must be of the form bytes=first-last where first and last are the zero-based offsets of the first and last bytes to copy` |
| `x-amz-copy-source-if-*` | As CopyObject; failure `412` |

The source range is read through `readpath/objectreader` into the part file.
After validation the long-response protocol of §11 runs. Success body:

```xml
<CopyPartResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <ETag>"b54357faf0632cce46e942fa68356b38"</ETag>
  <LastModified>2026-11-13T10:05:40.000Z</LastModified>
</CopyPartResult>
```

### 9.4 CompleteMultipartUpload

Body:

```xml
<CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Part><PartNumber>1</PartNumber><ETag>"6b0ad3b4fbd2f1c1f8e1a02d0b67b2a3"</ETag><ChecksumCRC32>u1x2Pw==</ChecksumCRC32></Part>
  <Part><PartNumber>2</PartNumber><ETag>"0e5a71c1f7d0a3b2c4e6f8091a2b3c4d"</ETag></Part>
</CompleteMultipartUpload>
```

Validation, all before any response byte (normal error statuses):

| Rule | Failure |
| --- | --- |
| Body at most 4 MiB | `400 MaxMessageLengthExceeded` |
| Empty body | `400 MissingRequestBodyError` |
| At least one `Part`; well-formed XML | `400 MalformedXML` |
| Part numbers strictly ascending, no duplicates | `400 InvalidPartOrder` |
| Each listed part staged, ETag equal (quotes optional, case-insensitive hex) | `400 InvalidPart` with `<PartNumber>` and `<ETag>` |
| Each listed checksum (`ChecksumCRC32`, `ChecksumCRC32C`, `ChecksumCRC64NVME`, `ChecksumSHA1`, `ChecksumSHA256`) equal to the recorded part checksum | `400 InvalidPart` |
| Every listed part except the last at least 5 MiB | `400 EntityTooSmall` with `<ProposedSize>`, `<MinSizeAllowed>5242880</MinSizeAllowed>`, `<PartNumber>`, `<ETag>` |
| Stored size of the listed parts at most `limits.MaxStoredObjectBytes` | `400 EntityTooLarge` |
| `If-None-Match: *` / `If-Match` | As PutObject; first check here, authoritative check in `ingest/put` |
| Upload unknown or aborted | `404 NoSuchUpload` |

Upload states on Complete:

| State | Behaviour |
| --- | --- |
| `open` | Validate, set `committing`, start the job |
| `committing` | Attach to the running job and wait for it within this request's budget; do not start a second job |
| `completed`, same part-list SHA-256, within `s3.completed_upload_retention` | Return the stored result immediately |
| `completed`, different part list, or after retention | `404 NoSuchUpload` |

After validation the long-response protocol of §11 runs while
`multipartstage` presents the part files as one reader and `ingest/put` runs
the write path (plan §7.4). Unlisted parts are discarded.

Success body:

```xml
<CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Location>https://s3.example.com/photos/video/clip.mp4</Location>
  <Bucket>photos</Bucket>
  <Key>video/clip.mp4</Key>
  <ETag>"d41d8cd98f00b204e9800998ecf8427e-2"</ETag>
</CompleteMultipartUploadResult>
```

`Location` is `s3.public_url` (or the request's scheme and `Host` when unset)
joined with the path-style object path. No composite checksum is returned in
v1.

Job outcomes after the `200` status line:

| Outcome | Body | Upload state |
| --- | --- | --- |
| Committed | `CompleteMultipartUploadResult` | `completed`; staging files removed |
| Flood wait past the budget before `put_attempted` | `Error`, `SlowDown` | back to `open`; parts kept; the SDK retries Complete |
| Unknown send outcome (`uncertain_body` or `uncertain_put`) | `Error`, `SlowDown` | stays `committing`; the sweeper finishes the job; a retried Complete attaches and returns the result |
| Write precondition failed at commit | `Error`, `PreconditionFailed` | back to `open` |
| Internal failure before `put_attempted` | `Error`, `InternalError` | back to `open` |

### 9.5 AbortMultipartUpload

`204 No Content`; staged files and index rows are removed at once. Unknown or
already aborted upload: `404 NoSuchUpload`. An upload in `committing`:
`409 OperationAborted`, `A conflicting conditional operation is currently in
progress against this resource. Please try again.` A completed upload:
`404 NoSuchUpload`.

### 9.6 ListParts

Parameters `max-parts` (1 to 1000, default 1000, larger clamped),
`part-number-marker`. Response `ListPartsResult` with `Bucket`, `Key`,
`UploadId`, `Initiator` (`ID` = initiating access key ID, `DisplayName` = the
key's name), `Owner` (§7.1), `StorageClass` `STANDARD`, `PartNumberMarker`,
`NextPartNumberMarker`, `MaxParts`, `IsTruncated`, `ChecksumAlgorithm` and
`ChecksumType` when recorded, and `Part*` with `PartNumber`, `LastModified`,
`ETag`, `Size` and the part's checksum element when recorded. Unknown upload:
`404 NoSuchUpload`.

### 9.7 ListMultipartUploads

Parameters `prefix`, `delimiter`, `key-marker`, `upload-id-marker` (used only
with `key-marker`), `max-uploads` (1 to 1000, default 1000, larger clamped),
`encoding-type`. Uploads in state `open` or `committing`, ordered by key then
initiation time. Response `ListMultipartUploadsResult` with `Bucket`,
`KeyMarker`, `UploadIdMarker`, `NextKeyMarker`, `NextUploadIdMarker`,
`Prefix`, `Delimiter`, `MaxUploads`, `IsTruncated`, `EncodingType`,
`Upload*` (`Key`, `UploadId`, `Initiator`, `Owner`, `StorageClass`,
`Initiated`) and `CommonPrefixes*`.

---

## 10. Bucket configuration, ACLs, versioning, tagging

### 10.1 Bucket policy (decision: canned only)

A bucket's policy is `private` or `public-read`, set through the admin API
(D7). There is no policy document engine.

GetBucketPolicy on a public-read bucket returns `200`,
`Content-Type: application/json`:

```json
{"Version":"2012-10-17","Statement":[{"Sid":"butterdb-public-read","Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::photos/*"}]}
```

On a private bucket: `404 NoSuchBucketPolicy`. PutBucketPolicy and
DeleteBucketPolicy: `501 NotImplemented`, message `Bucket policy is managed
through the butterDB admin API (private or public-read)`.

GetBucketPolicyStatus:

```xml
<PolicyStatus xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsPublic>true</IsPublic></PolicyStatus>
```

### 10.2 ACLs (decision: synthesised, ACLs-disabled semantics)

butterDB behaves like an S3 bucket with Object Ownership
`BucketOwnerEnforced` (ACLs disabled): ACLs can be read, and writes are
accepted only when they would not change access.

Effective ACL:

| Resource | Grants |
| --- | --- |
| Bucket (either policy) | Owner `FULL_CONTROL` |
| Object in a private bucket | Owner `FULL_CONTROL` |
| Object in a public-read bucket | Owner `FULL_CONTROL`, `AllUsers` `READ` |

The bucket ACL of a public-read bucket has no `AllUsers` grant, because
anonymous listing is not allowed (§4.11).

```xml
<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Owner><ID>5d0a…</ID><DisplayName>butterdb</DisplayName></Owner>
  <AccessControlList>
    <Grant>
      <Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>5d0a…</ID><DisplayName>butterdb</DisplayName></Grantee>
      <Permission>FULL_CONTROL</Permission>
    </Grant>
    <Grant>
      <Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee>
      <Permission>READ</Permission>
    </Grant>
  </AccessControlList>
</AccessControlPolicy>
```

Canned ACL values in `x-amz-acl` (PutObject, CopyObject,
CreateMultipartUpload, PutObjectAcl, PutBucketAcl):

| `x-amz-acl` | Accepted when | Otherwise |
| --- | --- | --- |
| `private`, `bucket-owner-full-control`, `bucket-owner-read` | Always on objects; `private` on buckets | `400 AccessControlListNotSupported` |
| `public-read` | Object in a public-read bucket | `400 AccessControlListNotSupported` |
| `public-read-write`, `authenticated-read`, `aws-exec-read`, `log-delivery-write` | Never | `400 AccessControlListNotSupported`, `The bucket does not allow ACLs` |

An accepted value changes nothing. PutObjectAcl and PutBucketAcl with an
`AccessControlPolicy` body are accepted (`200`) when the grant set equals the
effective ACL (order and `DisplayName` ignored), else
`400 AccessControlListNotSupported`. `x-amz-grant-*` headers always get
`400 AccessControlListNotSupported`.

### 10.3 Versioning

GetBucketVersioning always returns the never-enabled form:

```xml
<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>
```

PutBucketVersioning and ListObjectVersions: `501 NotImplemented` (D10).

### 10.4 Bucket encryption

GetBucketEncryption on a bucket whose encryption default is `tde1`:

```xml
<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault><BucketKeyEnabled>false</BucketKeyEnabled></Rule>
</ServerSideEncryptionConfiguration>
```

Default `none`: `404 ServerSideEncryptionConfigurationNotFoundError`. (AWS
now reports SSE-S3 on every bucket; butterDB reports what it does.)

### 10.5 Tagging

Objects have no tags. GetObjectTagging returns
`<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet/></Tagging>`;
PutObjectTagging with an empty `TagSet` returns `200`; with tags,
`501 NotImplemented`; DeleteObjectTagging returns `204`. The AWS CLI v2 calls
GetObjectTagging during S3-to-S3 copies, which is why these exist.

### 10.6 Other configuration getters

All require `list`. Setters and deleters for each are `501 NotImplemented`.

| Subresource | Answer |
| --- | --- |
| `lifecycle` | `404 NoSuchLifecycleConfiguration` |
| `tagging` (bucket) | `404 NoSuchTagSet` |
| `website` | `404 NoSuchWebsiteConfiguration` |
| `object-lock` | `404 ObjectLockConfigurationNotFoundError` |
| `publicAccessBlock` | `404 NoSuchPublicAccessBlockConfiguration` |
| `logging` | `200` `<BucketLoggingStatus xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>` |
| `requestPayment` | `200` `<RequestPaymentConfiguration xmlns="…"><Payer>BucketOwner</Payer></RequestPaymentConfiguration>` |
| `ownershipControls` | `200` `<OwnershipControls xmlns="…"><Rule><ObjectOwnership>BucketOwnerEnforced</ObjectOwnership></Rule></OwnershipControls>` |
| `notification` | `200` `<NotificationConfiguration xmlns="…"/>` |
| `accelerate` | `200` `<AccelerateConfiguration xmlns="…"/>` |
| `cors` | §13.5 |

`s3cmd info` reads several of these; each answer is what S3 returns for a
bucket with that feature unconfigured.

---

## 11. Long responses: keepalive and errors in a `200` body

CompleteMultipartUpload, CopyObject and UploadPartCopy can run for many
minutes because they wait for a Telegram commit or a Telegram read. They use
the protocol S3 uses for the same operations:

1. Every validation that can fail with a normal status (§9.4, §8.6, §9.3) runs
   first. Those failures are ordinary error responses.
2. The handler writes status `200`, headers `Content-Type: application/xml`,
   `x-amz-request-id`, `x-amz-id-2`, CORS headers and
   `x-amz-server-side-encryption` when applicable, with no `Content-Length`
   (chunked on HTTP/1.1), then the XML declaration
   `<?xml version="1.0" encoding="UTF-8"?>\n`, and flushes.
3. Every `s3.keepalive_interval` (15 s) while the job runs, it writes one
   space byte and flushes. XML allows whitespace between the declaration and
   the root element, so the final document is well-formed. The declaration
   must come first: whitespace before `<?xml` makes the document invalid.
4. On success it writes the result root element. On failure it writes an
   `Error` document (§14.1) without its own XML declaration.

SDK handling: aws-sdk-go-v2, botocore and the AWS JS SDK v3 inspect the body
of a `200` response to these three operations, raise the `Error` code as an
error, and retry it when the code is retryable (`SlowDown`, `InternalError`).
The retried request is safe: a repeated Complete attaches to the running job
or returns the stored result (§9.4); a repeated copy writes a new object
whose put wins by message ID (E3).

The keepalive writer stops before the final write, and both share one mutex
so no space byte can be written inside the root element. If the client
disconnects, the job continues (CopyObject and Complete commit; UploadPartCopy
stages the part) and the outcome is visible to a later request.

Every other operation answers with a complete status and body at the end;
PutObject cannot use this protocol because its response has no body.

---

## 12. Timeouts and budgets

### 12.1 Request budget (D8, A7)

A write is acknowledged only after its Telegram commit (D8), so its duration is
the client's transfer, then butterDB's transfer to Telegram, then the `t=put`
send and projection. Transfer time is never cut short. What is bounded is
**waiting**: `s3.request_budget` (default 30 s, [operations.md](operations.md)
§2.4) is the total time one request may spend waiting on

- a put slot (`ingest.max_concurrent_puts`, `ingest.max_concurrent_puts_per_key`) and
  a spool reservation in `ingest/admission`,
- the E10 send-rate limiter (`document` class for the body, `message` class
  for `t=put` and `t=del`),
- server-directed `FLOOD_WAIT`s, and `getFile` slots on reads,

summed across the request. The same budget applies to PutObject, CopyObject,
UploadPart (admission only), UploadPartCopy, CompleteMultipartUpload,
DeleteObject, DeleteObjects (§8.5) and GetObject (§8.2).

Behaviour (`ingest/admission`, `ingest/put`, `ingest/remove`):

| Event | Response |
| --- | --- |
| No put slot or spool reservation before the budget is spent | `503 SlowDown`, `Retry-After: 5`. The body has not been read |
| A limiter or `FLOOD_WAIT` wait longer than the remaining budget, before `put_attempted` | `503 SlowDown` at once, without waiting; `Retry-After` = that wait in whole seconds rounded up, at least 1. The job aborts and the sweeper removes any body by `j=<job id>` (plan §7.1) |
| Send outcome unknown (`uncertain_body`, `uncertain_put`, `uncertain_del`) | `503 SlowDown`, `Retry-After: 30`; the sweeper reconciles the job ([storage-model.md](storage-model.md)) |
| Telegram transfer fails after `tgclient`'s transient retries ([operations.md](operations.md) §13.1), before `put_attempted` | `503 SlowDown`, `Retry-After: 30`; job aborted |
| Server state `degraded`, or bucket without write authority | `503 SlowDown` (§2.7) |
| Server state `starting`, `unlinked` or `syncing` | `503 ServiceUnavailable`, `Retry-After: 30` (§2.7) |
| Non-transient failure before `put_attempted` | `500 InternalError` |

Waits that fit the remaining budget are waited out inside the request. For
long responses (§11) the `SlowDown` arrives as an `Error` body; `Retry-After`
cannot be sent because the headers are already written.

`Retry-After` is advisory. aws-sdk-go-v2 and botocore retry `SlowDown` with
their own exponential backoff rather than the header's value, which is why
§12.4 raises their attempt counts. Custom clients should honour it.

Worst-case server time after the last request byte, at the low end of the
2–4 MB/s planning rate ([operations.md](operations.md) §13.2):

| Stored size | Budget + transfer to Telegram + commit |
| --- | --- |
| 0 (DeleteObject) | 30 s + about 1 s |
| 8 MiB | 30 s + about 5 s |
| 100 MiB | 30 s + about 53 s |
| 1900 MiB | 30 s + about 997 s, about 17 min 10 s in all |

### 12.2 Connection timeouts

| Timeout | Value | Failure |
| --- | --- | --- |
| Request headers | `ReadHeaderTimeout` 10 s | Connection closed |
| Gap between body reads | `s3.body_idle_timeout` 60 s, reset before every read through `http.ResponseController.SetReadDeadline` | `400 RequestTimeout`, `Your socket connection to the server was not read from or written to within the timeout period. Idle connections will be closed.` |
| Blocked response write | `s3.write_idle_timeout` 60 s, reset before every write | Connection closed |
| Idle keep-alive connection | `IdleTimeout` 120 s | Connection closed |

There is no total request deadline: a 1.9 GB upload over a slow link may take
longer than any fixed timeout, and `s3.request_budget` bounds the
server-side waiting.

### 12.3 Client timeout requirements

A plain PutObject sends no byte back until the commit, so the client's wait
for response headers must exceed the worst case of §12.1 for the object's size.
The recommended setting below is 25 minutes (1,500 s), which covers the
17 min 10 s of the largest object plus margin, and exceeds the 1,200 s proxy
timeout of §2.6 so a proxy timeout is reported as such. Multipart Complete,
CopyObject and UploadPartCopy receive a byte every 15 s, so any idle-read
timeout above 30 s works for them.

Recommendations for every client:

- Use the SDK's multipart transfer manager for objects above 16 MiB. Behind
  Cloudflare's proxy use 64 MiB parts instead ([operations.md](operations.md)
  §11.3).
- Use path-style addressing unless virtual-hosted DNS and TLS are set up.
- Use region `us-east-1` or the configured `s3.region`.
- Allow 10 attempts, so `SlowDown` is retried through a flood wait or a full
  admission queue ([operations.md](operations.md) §11.4 uses the same count).

### 12.4 SDK and tool settings

**aws-sdk-go-v2**

```go
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func newClient(ctx context.Context, endpoint, accessKeyID, secret string) (*s3.Client, error) {
	httpClient := awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
		t.ResponseHeaderTimeout = 25 * time.Minute
	})
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secret, "")),
		config.WithHTTPClient(httpClient),
		config.WithRetryer(func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = 10
				o.MaxBackoff = 60 * time.Second
				o.RateLimiter = ratelimit.None
			})
		}),
	)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	}), nil
}
```

- `ResponseHeaderTimeout` starts after the request body is fully written,
  which is exactly the commit wait. Do not set `http.Client.Timeout`: it
  bounds the whole request including the upload.
- `ratelimit.None` disables the standard retryer's client-side retry quota,
  which otherwise stops retrying after a burst of `SlowDown` responses.
- Default checksum behaviour (`RequestChecksumCalculation` `WhenSupported`)
  works; `WhenRequired` also works.
- Transfer manager (`feature/s3/manager`): `PartSize` 16 MiB, `Concurrency` 4.
  Larger concurrency only queues behind Telegram's send rate.

**boto3**

```python
import boto3
from botocore.config import Config

s3 = boto3.client(
    "s3",
    endpoint_url="https://s3.example.com",
    aws_access_key_id=ACCESS_KEY_ID,
    aws_secret_access_key=SECRET_ACCESS_KEY,
    config=Config(
        region_name="us-east-1",
        signature_version="s3v4",
        s3={"addressing_style": "path"},
        connect_timeout=10,
        read_timeout=1500,
        retries={"mode": "adaptive", "max_attempts": 10},
    ),
)
```

`read_timeout` defaults to 60 s, which a commit wait regularly exceeds; it is
the setting that matters. `TransferConfig(multipart_threshold=16 * 1024 * 1024,
multipart_chunksize=16 * 1024 * 1024, max_concurrency=4)` for `upload_file`.

**AWS SDK for JavaScript v3 (Node.js)**

```js
import { S3Client } from "@aws-sdk/client-s3";
import { NodeHttpHandler } from "@smithy/node-http-handler";

const s3 = new S3Client({
  region: "us-east-1",
  endpoint: "https://s3.example.com",
  forcePathStyle: true,
  credentials: { accessKeyId: ACCESS_KEY_ID, secretAccessKey: SECRET_ACCESS_KEY },
  maxAttempts: 10,
  requestHandler: new NodeHttpHandler({ connectionTimeout: 10_000, requestTimeout: 1_500_000 }),
});
```

`requestTimeout` is a socket idle timeout in milliseconds. In browsers,
`FetchHttpHandler({ requestTimeout: 0 })` (no timeout) is recommended and
uploads should use presigned PUT URLs; browser use needs CORS (§13).
`@aws-sdk/lib-storage` `Upload`: `partSize: 16 * 1024 * 1024`,
`queueSize: 4`.

**AWS CLI v2**

```
aws configure set default.s3.addressing_style path
aws configure set default.retry_mode adaptive
aws configure set default.max_attempts 10
aws --endpoint-url https://s3.example.com --cli-read-timeout 1500 --cli-connect-timeout 10 s3 cp ./big.mp4 s3://photos/big.mp4
```

The CLI's default multipart threshold and chunk size (8 MiB) satisfy the
5 MiB part minimum.

**rclone**

```
[butterdb]
type = s3
provider = Other
access_key_id = BDBK7Q2M9X4T1W8Z3R6Y
secret_access_key = <secret>
endpoint = https://s3.example.com
region = us-east-1
force_path_style = true
no_check_bucket = true
list_version = 2
chunk_size = 16Mi
upload_cutoff = 16Mi
upload_concurrency = 4
```

Run with `--timeout 25m --contimeout 10s --low-level-retries 10`.
`no_check_bucket` stops rclone from attempting CreateBucket; without it,
rclone gets `409 BucketAlreadyOwnedByYou` and continues.

**s3cmd** (`~/.s3cfg`)

```
host_base = s3.example.com
host_bucket = s3.example.com
bucket_location = us-east-1
use_https = True
signature_v2 = False
multipart_chunk_size_mb = 16
socket_timeout = 1500
```

`host_bucket` without `%(bucket)s` selects path-style. `s3cmd` stores its
`x-amz-meta-s3cmd-attrs` metadata (about 200 bytes), which fits the limits.
`--acl-public` only succeeds on public-read buckets (§10.2).

---

## 13. CORS

### 13.1 Decision

CORS is configured once per deployment in the `[s3]` table (§2.1) and applies
to every bucket. GetBucketCors reports it; PutBucketCors and DeleteBucketCors
return `501 NotImplemented`. Per-bucket CORS would need a new bucket
configuration field in `t=bcfg` and the admin API. Default (owner may change).
With `s3.cors_allowed_origins` empty, no CORS headers are ever sent and every
preflight is refused.

### 13.2 Preflight (`OPTIONS`)

Unauthenticated, answered before readiness and authentication, and never
reveals whether the bucket or key exists.

| Request | Response |
| --- | --- |
| No `Origin` or no `Access-Control-Request-Method` | `400 BadRequest`, `Insufficient information. Origin request header needed.` |
| Origin, method or any `Access-Control-Request-Headers` entry not allowed | `403 AccessForbidden`, `CORSResponse: This CORS request is not allowed. This is usually because the evalution of Origin, request method / Access-Control-Request-Method or Access-Control-Request-Headers are not whitelisted by the resource's CORS spec.` |
| Allowed | `200`, empty body, headers below |

Allowed preflight headers: `Access-Control-Allow-Origin` (the request's
`Origin`, or `*` when `*` is configured), `Access-Control-Allow-Methods` (the
configured list), `Access-Control-Allow-Headers` (the requested headers,
echoed), `Access-Control-Max-Age` (`s3.cors_max_age`),
`Access-Control-Expose-Headers`, and `Vary: Origin,
Access-Control-Request-Headers, Access-Control-Request-Method`.

Origin matching is exact on `scheme://host[:port]`, case-insensitive on the
host. A configured origin may contain one `*` wildcard
(`https://*.example.com`), as S3 allows.

### 13.3 Default exposed headers

`ETag`, `Content-Length`, `Content-Range`, `Accept-Ranges`, `Last-Modified`,
`Content-Type`, `Content-Disposition`, `Content-Encoding`, `Cache-Control`,
`Expires`, `x-amz-request-id`, `x-amz-id-2`, `x-amz-server-side-encryption`,
`x-amz-checksum-crc32`, `x-amz-checksum-crc32c`, `x-amz-checksum-crc64nvme`,
`x-amz-checksum-sha1`, `x-amz-checksum-sha256`. Browser code that needs user
metadata must list each `x-amz-meta-<name>` in `s3.cors_expose_headers`.

### 13.4 Actual requests

Any request carrying an allowed `Origin` gets `Access-Control-Allow-Origin`,
`Access-Control-Expose-Headers` and `Vary: Origin`, on success and on error,
so browser code can read error bodies. No
`Access-Control-Allow-Credentials` is sent; S3 credentials travel in the
signature, not in cookies.

### 13.5 GetBucketCors

Returns the deployment configuration as one rule:

```xml
<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <CORSRule>
    <AllowedOrigin>https://app.example.com</AllowedOrigin>
    <AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod>
    <AllowedHeader>*</AllowedHeader>
    <ExposeHeader>ETag</ExposeHeader>
    <MaxAgeSeconds>3600</MaxAgeSeconds>
  </CORSRule>
</CORSConfiguration>
```

With no origins configured: `404 NoSuchCORSConfiguration`.

### 13.6 Cache-Control for reads (A8)

Precedence for the `Cache-Control` response header on GetObject and
HeadObject:

1. `response-cache-control` (signed requests only).
2. The object's stored `Cache-Control`.
3. Public-read bucket: `public, max-age=<cache_max_age_seconds>`, or
   `no-cache` when it is 0.
4. Otherwise no header.

`immutable` is never added, because keys can be overwritten (D6, A8).
Revalidation uses `ETag` and `If-None-Match` (§8.2).

---

## 14. Errors

### 14.1 Error response format

```
HTTP/1.1 404 Not Found
Content-Type: application/xml
x-amz-request-id: 4F2A9C1D7E3B5A60
x-amz-id-2: 9Kp3m1sQ0n2bW8vJcX4yZ6tR5uA7eD0fG1hI2jK3lM4=
Server: butterdb
```

```xml
<?xml version="1.0" encoding="UTF-8"?>
<Error>
  <Code>NoSuchKey</Code>
  <Message>The specified key does not exist.</Message>
  <Key>2026/cat.jpg</Key>
  <BucketName>photos</BucketName>
  <Resource>/photos/2026/cat.jpg</Resource>
  <RequestId>4F2A9C1D7E3B5A60</RequestId>
  <HostId>9Kp3m1sQ0n2bW8vJcX4yZ6tR5uA7eD0fG1hI2jK3lM4=</HostId>
</Error>
```

| Element | Rule |
| --- | --- |
| `Code` | From §14.2; clients branch on it |
| `Message` | Human text; S3's wording where S3 has one; never secrets, signatures or object bytes |
| `Resource` | The path-style resource, `/<bucket>/<key>` |
| `BucketName`, `Key` | When the error concerns them (`NoSuchBucket`, `NoSuchKey`, `NoSuchUpload`) |
| `RequestId` | Equal to `x-amz-request-id`: 16 upper-case hex characters from `crypto/rand` |
| `HostId` | Equal to `x-amz-id-2`: standard base64 of 32 random bytes |
| Extra elements | As listed per code (`<Region>`, `<StringToSign>`, `<ProposedSize>` ...) |

- The `Error` root has no namespace, as in S3.
- HEAD requests and `304` responses carry no body.
- `SlowDown` and `ServiceUnavailable` set `Retry-After` (whole seconds).
- When the request body was not read and is larger than 256 KiB, Go closes the
  connection after the error; this is expected.
- Every error is logged with the request ID, operation, code and access key
  ID, never the signature.

### 14.2 Error code table

Every code the S3 listener produces.

| Code | HTTP | Produced when | `errs` sentinel |
| --- | --- | --- | --- |
| `AccessControlListNotSupported` | 400 | ACL write that would change access, `x-amz-grant-*` (§10.2) | local |
| `AccessDenied` | 403 | Scope or action missing, anonymous request not allowed, CreateBucket/DeleteBucket, presigned URL expired or not yet valid, no date header | `ErrAccessDenied`, `ErrRequestExpired` |
| `AccessForbidden` | 403 | CORS preflight refused | local |
| `AuthorizationHeaderMalformed` | 400 | Malformed `Authorization`, wrong service, wrong region, scope date mismatch | `ErrSignatureMismatch` (reason) |
| `AuthorizationQueryParametersError` | 400 | Malformed presigned parameters, `X-Amz-Expires` out of range, wrong region | `ErrSignatureMismatch` (reason) |
| `BadDigest` | 400 | `Content-MD5` or `x-amz-checksum-*` mismatch | `ErrBadDigest` |
| `BadRequest` | 400 | Preflight without `Origin` | local |
| `BucketAlreadyOwnedByYou` | 409 | CreateBucket on an existing in-scope bucket | `ErrBucketExists` |
| `BucketNotEmpty` | 409 | Not produced by the S3 listener in v1 (§6.4); listed because the catalogue maps it | `ErrBucketNotEmpty` |
| `EntityTooLarge` | 400 | Stored size over `limits.MaxStoredObjectBytes` | `ErrObjectTooLarge` |
| `EntityTooSmall` | 400 | A non-last part under 5 MiB at Complete | `ErrEntityTooSmall` (new) |
| `IncompleteBody` | 400 | Body shorter than `Content-Length` or `x-amz-decoded-content-length` | local |
| `InternalError` | 500 | Any unmapped error; uncertain outcomes never surface as success | `ErrMissingDependency`, unmapped |
| `InvalidAccessKeyId` | 403 | Unknown, malformed or revoked access key | `ErrUnknownAccessKey` |
| `InvalidArgument` | 400 | Bad parameter or header value (each case named in this document) | `ErrInvalidKey` (control characters, invalid UTF-8), local |
| `InvalidBucketName` | 400 | Bucket name in the request breaks the S3 naming rules | `ErrInvalidBucketName` |
| `InvalidDigest` | 400 | Malformed `Content-MD5` | local |
| `InvalidPart` | 400 | Listed part not staged, or ETag or checksum mismatch | `ErrInvalidPart` |
| `InvalidPartNumber` | 416 | GetObject/HeadObject `partNumber` other than 1 | local |
| `InvalidPartOrder` | 400 | Listed parts not strictly ascending | `ErrInvalidPartOrder` (new) |
| `InvalidRange` | 416 | Unsatisfiable single range | `ErrInvalidRange` |
| `InvalidRequest` | 400 | Request-level misuse named in this document (SigV2, missing `x-amz-content-sha256`, bad aws-chunked framing, illegal self-copy, host not served) | local |
| `InvalidStorageClass` | 400 | `x-amz-storage-class` other than `STANDARD` | local |
| `InvalidToken` | 400 | `X-Amz-Security-Token` present | local |
| `InvalidURI` | 400 | Path is not valid percent encoding | local |
| `KeyTooLongError` | 400 | Key over 1024 bytes, or key alone overflows the put record cap | `ErrInvalidKey` |
| `MalformedXML` | 400 | Request XML not well-formed or not the expected shape | local |
| `MaxMessageLengthExceeded` | 400 | XML body over its limit (DeleteObjects 2 MiB, Complete 4 MiB, others 64 KiB) | local |
| `MetadataTooLarge` | 400 | User metadata over 2048 bytes, or the put record cap exceeded | `ErrMetadataTooLarge` (new) |
| `MethodNotAllowed` | 405 | Method not valid for the resource (§6.2) | local |
| `MissingContentLength` | 411 | PutObject/UploadPart without a length | local |
| `MissingRequestBodyError` | 400 | Empty body where XML is required | local |
| `NoSuchBucket` | 404 | In-scope bucket does not exist | `ErrNoSuchBucket` |
| `NoSuchBucketPolicy` | 404 | GetBucketPolicy on a private bucket | local |
| `NoSuchCORSConfiguration` | 404 | GetBucketCors with CORS disabled | local |
| `NoSuchKey` | 404 | Object does not exist | `ErrNoSuchKey` |
| `NoSuchLifecycleConfiguration` | 404 | §10.6 | local |
| `NoSuchPublicAccessBlockConfiguration` | 404 | §10.6 | local |
| `NoSuchTagSet` | 404 | §10.6 | local |
| `NoSuchUpload` | 404 | Unknown, aborted or expired upload | `ErrNoSuchUpload` |
| `NoSuchWebsiteConfiguration` | 404 | §10.6 | local |
| `NotImplemented` | 501 | Operation or feature not built (§6.1, §6.3, §4.4) | `ErrNotImplemented` |
| `ObjectLockConfigurationNotFoundError` | 404 | §10.6 | local |
| `OperationAborted` | 409 | UploadPart or Abort while the upload is `committing` | `ErrUploadBusy` (new) |
| `PreconditionFailed` | 412 | `If-Match`, `If-Unmodified-Since`, `If-None-Match: *` on write, copy-source conditions | `ErrPreconditionFailed` |
| `RequestTimeTooSkewed` | 403 | Header-auth request time more than 15 minutes from server time | `ErrRequestExpired` (reason) |
| `RequestTimeout` | 400 | Request body idle longer than `s3.body_idle_timeout` | local |
| `ServerSideEncryptionConfigurationNotFoundError` | 404 | GetBucketEncryption on a `none` bucket | local |
| `ServiceUnavailable` | 503 | Server state `starting`, `unlinked` or `syncing` (§2.7); `Retry-After: 30` | `ErrServiceUnavailable` (new) |
| `SignatureDoesNotMatch` | 403 | Header, presigned, chunk or trailer signature wrong | `ErrSignatureMismatch` |
| `SlowDown` | 503 | Waits past `s3.request_budget` (admission, E10 limiter, flood wait), uncertain send, state `degraded`, bucket without authority, anonymous rate limit, auth-failure block; `Retry-After` set | `ErrSlowDown` |
| `XAmzContentSHA256Mismatch` | 400 | Signed hex payload hash differs from the body | `ErrBadDigest` (reason) |

`304 Not Modified` is a status, not an error code, and has no body.

"local" codes are produced inside `internal/s3api` or `internal/access/sigv4`
and have no domain sentinel. "(new)" sentinels extend the starting catalogue
in claude.md §8.4 with codes `object.metadata_too_large`,
`multipart.entity_too_small`, `multipart.invalid_part_order`,
`multipart.upload_busy` and `server.unavailable`. "(reason)" means the
sentinel is shared and `sigv4.Error.Reason` (§15.1) selects the S3 code.

### 14.3 Messages for the common codes

| Code | Message |
| --- | --- |
| `NoSuchKey` | `The specified key does not exist.` |
| `NoSuchBucket` | `The specified bucket does not exist` |
| `NoSuchUpload` | `The specified upload does not exist. The upload ID may be invalid, or the upload may have been aborted or completed.` |
| `AccessDenied` | `Access Denied` |
| `SlowDown` | `Please reduce your request rate.` |
| `ServiceUnavailable` | `butterDB is starting or is not linked to Telegram. Please retry.` |
| `EntityTooLarge` | `Your proposed upload exceeds the maximum allowed size` |
| `EntityTooSmall` | `Your proposed upload is smaller than the minimum allowed size` |
| `MetadataTooLarge` | `Your metadata headers exceed the maximum allowed metadata size.` |
| `KeyTooLongError` | `Your key is too long` |
| `NotImplemented` | `A header or operation you provided implies functionality that is not implemented` |
| `InternalError` | `We encountered an internal error. Please try again.` |

---

## 15. Go contracts

Exact exported signatures that phase 08 lanes and the integration lane share.
No comments, per the code policy.

### 15.1 `internal/access/sigv4`

```go
package sigv4

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
)

type AuthKind int

const (
	AuthAnonymous AuthKind = iota
	AuthHeader
	AuthPresigned
)

type PayloadMode int

const (
	PayloadNone PayloadMode = iota
	PayloadSignedHex
	PayloadUnsigned
	PayloadStreamingSigned
	PayloadStreamingSignedTrailer
	PayloadStreamingUnsignedTrailer
)

type Reason int

const (
	ReasonSignature Reason = iota + 1
	ReasonUnknownKey
	ReasonHeaderMalformed
	ReasonQueryMalformed
	ReasonWrongRegion
	ReasonSkew
	ReasonExpired
	ReasonNotYetValid
	ReasonMissingDate
	ReasonMissingContentSHA256
	ReasonBadContentSHA256
	ReasonContentSHA256Mismatch
	ReasonSecurityToken
	ReasonSigV2
	ReasonSigV4A
	ReasonBothMechanisms
	ReasonUnsupportedScheme
	ReasonChunkFraming
	ReasonIncompleteBody
	ReasonChecksumMismatch
	ReasonChecksumMalformed
)

type Error struct {
	Reason           Reason
	Err              error
	AccessKeyID      string
	StringToSign     string
	CanonicalRequest string
	Region           string
	RequestTime      time.Time
	ServerTime       time.Time
	Expires          time.Time
	Algorithm        string
	ClientSHA256     string
	ServerSHA256     string
}

func (e *Error) Error() string
func (e *Error) Unwrap() error

type Config struct {
	Region     string
	MaxSkew    time.Duration
	MaxPresign time.Duration
	Now        func() time.Time
}

type SecretSource interface {
	Secret(ctx context.Context, id access.AccessKeyID) ([]byte, access.KeyInfo, error)
}

type Auth struct {
	Kind          AuthKind
	Key           access.KeyInfo
	Mode          PayloadMode
	ContentSHA256 string
	DecodedLength int64
	Trailer       string
	RequestTime   time.Time
	Region        string
	seed          string
	signingKey    []byte
}

type Verifier struct {
}

func NewVerifier(cfg Config, secrets SecretSource) *Verifier
func (v *Verifier) Authenticate(ctx context.Context, r *http.Request) (Auth, error)
func (v *Verifier) Body(r *http.Request, a Auth) (*PayloadReader, error)

type PayloadReader struct {
}

func (p *PayloadReader) Read(b []byte) (int, error)
func (p *PayloadReader) Close() error
func (p *PayloadReader) Checksum() (algorithm string, value string)

type PresignInput struct {
	Method      string
	URL         *url.URL
	AccessKeyID access.AccessKeyID
	Secret      []byte
	Region      string
	Time        time.Time
	Expires     time.Duration
}

func Presign(in PresignInput) (string, error)

var _ io.ReadCloser = (*PayloadReader)(nil)
```

- `Config.MaxSkew` is 15 minutes and `Config.MaxPresign` is 7 days; `Region`
  is `s3.region` (`us-east-1` is always accepted, §3).
- `Authenticate` never reads the body. It returns `AuthAnonymous` with a nil
  error when no credentials are present.
- `Body` returns a reader yielding decoded object bytes. Signature, hash and
  checksum failures are returned from `Read` as `*Error` no later than at EOF,
  and always before `Read` returns `io.EOF`. `Checksum` reports the verified
  `x-amz-checksum-*` after EOF.
- `Error.Err` is the `internal/domain/errs` sentinel; `s3err` maps
  `Reason` to the S3 code (§14.2).

### 15.2 `internal/access/policy`

```go
package policy

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
)

type Principal struct {
	Anonymous bool
	Key       access.KeyInfo
}

type BucketSource interface {
	Lookup(ctx context.Context, name bucket.BucketName) (bucket.Info, bool, error)
}

type Evaluator struct {
}

func NewEvaluator(buckets BucketSource) *Evaluator
func (e *Evaluator) Authorize(ctx context.Context, p Principal, b bucket.BucketName, a access.Action) (bucket.Info, error)
func (e *Evaluator) AuthorizeAnonymousRead(ctx context.Context, b bucket.BucketName) (bucket.Info, error)
func (e *Evaluator) VisibleBuckets(ctx context.Context, p Principal) ([]bucket.Info, error)
```

`Authorize` implements §4.10 steps 1 to 3, returning `errs.ErrAccessDenied` or
`errs.ErrNoSuchBucket`. `AuthorizeAnonymousRead` returns
`errs.ErrAccessDenied` for a missing or private bucket.

### 15.3 `internal/s3api/router`

```go
package router

import "net/http"

type Operation string

const (
	OpListBuckets             Operation = "ListBuckets"
	OpCreateBucket            Operation = "CreateBucket"
	OpHeadBucket              Operation = "HeadBucket"
	OpDeleteBucket            Operation = "DeleteBucket"
	OpGetBucketLocation       Operation = "GetBucketLocation"
	OpListObjectsV2           Operation = "ListObjectsV2"
	OpListObjects             Operation = "ListObjects"
	OpPutObject               Operation = "PutObject"
	OpGetObject               Operation = "GetObject"
	OpHeadObject              Operation = "HeadObject"
	OpDeleteObject            Operation = "DeleteObject"
	OpDeleteObjects           Operation = "DeleteObjects"
	OpCopyObject              Operation = "CopyObject"
	OpCreateMultipartUpload   Operation = "CreateMultipartUpload"
	OpUploadPart              Operation = "UploadPart"
	OpUploadPartCopy          Operation = "UploadPartCopy"
	OpCompleteMultipartUpload Operation = "CompleteMultipartUpload"
	OpAbortMultipartUpload    Operation = "AbortMultipartUpload"
	OpListParts               Operation = "ListParts"
	OpListMultipartUploads    Operation = "ListMultipartUploads"
	OpGetBucketPolicy         Operation = "GetBucketPolicy"
	OpGetBucketPolicyStatus   Operation = "GetBucketPolicyStatus"
	OpGetBucketCors           Operation = "GetBucketCors"
	OpGetBucketAcl            Operation = "GetBucketAcl"
	OpPutBucketAcl            Operation = "PutBucketAcl"
	OpGetObjectAcl            Operation = "GetObjectAcl"
	OpPutObjectAcl            Operation = "PutObjectAcl"
	OpGetBucketVersioning     Operation = "GetBucketVersioning"
	OpGetBucketEncryption     Operation = "GetBucketEncryption"
	OpGetObjectTagging        Operation = "GetObjectTagging"
	OpPutObjectTagging        Operation = "PutObjectTagging"
	OpDeleteObjectTagging     Operation = "DeleteObjectTagging"
	OpGetBucketConfig         Operation = "GetBucketConfig"
	OpPreflight               Operation = "Preflight"
)

type Target struct {
	Bucket        string
	Key           string
	HasKey        bool
	VirtualHosted bool
}

type Route struct {
	Target      Target
	Operation   Operation
	Subresource string
}

type Addressing struct {
	BaseDomains  []string
	AllowedHosts []string
	LoopbackOnly bool
}

func ParseTarget(r *http.Request, a Addressing) (Target, error)
func Resolve(r *http.Request, t Target) (Route, error)
```

`OpGetBucketConfig` covers the fixed getters of §10.6 with `Subresource`
naming which. `Resolve` returns an error mapping to `405 MethodNotAllowed` or
`501 NotImplemented` per §6.2. The action for each operation is the table in
§4.10, held as a `map[Operation]access.Action` in `router`.

### 15.4 `internal/s3api/s3err`

```go
package s3err

import (
	"net/http"
	"time"
)

type Code string

type Field struct {
	Name  string
	Value string
}

type APIError struct {
	Code       Code
	Status     int
	Message    string
	BucketName string
	Key        string
	Resource   string
	RetryAfter time.Duration
	Extra      []Field
}

func Map(err error) APIError
func Encode(e APIError, requestID, hostID string) []byte
func Write(w http.ResponseWriter, r *http.Request, e APIError)
```

`Code` constants are exactly the `Code` column of §14.2, named
`Code<CodeValue>` (`CodeNoSuchKey`, `CodeSlowDown` ...). `Encode` returns the
`Error` element without an XML declaration; `Write` adds the declaration,
headers and `Retry-After`, and writes no body for HEAD.

### 15.5 `internal/s3api/xmlcodec`

```go
package xmlcodec

import (
	"io"
	"net/http"
	"time"
)

const Namespace = "http://s3.amazonaws.com/doc/2006-03-01/"

const TimeFormat = "2006-01-02T15:04:05.000Z"

func Decode(r io.Reader, limit int64, v any) error
func WriteResult(w http.ResponseWriter, status int, v any) error

type LongResponse struct {
}

func StartLongResponse(w http.ResponseWriter, interval time.Duration) (*LongResponse, error)
func (l *LongResponse) Succeed(v any) error
func (l *LongResponse) Fail(errorXML []byte) error
```

`Decode` rejects DOCTYPE, bodies over `limit` (mapped to
`MaxMessageLengthExceeded`) and empty bodies (`MissingRequestBodyError`), and
accepts elements with or without the S3 namespace. `StartLongResponse`
implements §11 steps 2 and 3; `Succeed` and `Fail` stop the keepalive and
write step 4. `Fail` takes `s3err.Encode` output so `xmlcodec` does not import
`s3err`.

---

## 16. Compatibility test plan

### 16.1 Unit tests (phase 08 lanes)

| Package | Required tests |
| --- | --- |
| `access/sigv4` | `TestAWSDocExampleGetObject`, `TestAWSDocExamplePutObject`, `TestAWSDocExampleListObjects`, `TestAWSDocExamplePresignedURL`, `TestAWSDocExampleChunkedUpload` (the worked examples from the AWS S3 API Reference "Signature Calculations for the Authorization Header", "Authenticating Requests: Using Query Parameters" and "Signature Calculation: Transfer Payload in Multiple Chunks", copied verbatim with their published signatures into `testdata/`), `TestCrossCheckWithSDKSigner` (requests signed by `github.com/aws/aws-sdk-go-v2/aws/signer/v4` verify), `TestCanonicalURIEncodesOnce`, `TestCanonicalURINoNormalisation`, `TestCanonicalQueryUnknownParamsIncluded`, `TestCanonicalQuerySpaceIsPercent20`, `TestHostFromRequestHost`, `TestSignedHeaderMissing`, `TestHeaderValueWhitespaceCollapsed`, `TestClockSkewBoundary` (14:59 ok, 15:01 fails), `TestPresignExpiresMax`, `TestPresignExpired`, `TestPresignNotYetValid`, `TestRegionUsEast1AlwaysAccepted`, `TestWrongRegion`, `TestSigV2Rejected`, `TestSigV4ARejected`, `TestSecurityTokenRejected`, `TestBothMechanismsRejected`, `TestUnsignedPayload`, `TestSignedHexMismatch`, `TestStreamingSignedChunkTampered`, `TestStreamingSignedTrailer`, `TestStreamingUnsignedTrailerCRC32`, `TestTrailerChecksumAlgorithms` (crc32, crc32c, crc64nvme, sha1, sha256), `TestTrailerMismatchBadDigest`, `TestDecodedLengthMismatch`, `TestChunkHeaderTooLong`, `TestTruncatedChunkedBody`, `TestRevokedKeyRejected`, `FuzzChunkDecoder` |
| `access/policy` | `TestScopeBucketOutsideDenied`, `TestScopeWildcard`, `TestMissingInScopeBucketNotFound`, `TestActionMatrix` (every row of §4.10), `TestAnonymousPublicReadGetOnly`, `TestAnonymousPrivateDenied`, `TestAnonymousMissingBucketDenied` |
| `s3api/router` | `TestPathStyleTargets`, `TestVirtualHostedTargets`, `TestLongestBaseDomainWins`, `TestEncodedSlashInKey`, `TestNoPathCleaning`, `TestDispatchTable` (every row of §6.2), `TestXIDIgnored`, `TestLoopbackHostCheck` |
| `s3api/xmlcodec` | `TestDecodeWithoutNamespace`, `TestDecodeRejectsDoctype`, `TestDecodeLimit`, `TestLongResponseDeclarationFirst`, `TestLongResponseKeepalive`, `TestLongResponseFailIsWellFormed`, `TestTimeFormat` |
| `s3api/s3err` | `TestEveryCodeHasStatus`, `TestMapEverySentinel`, `TestErrorXMLShape`, `TestHeadHasNoBody`, `TestRetryAfterSet`, `TestSignatureNotEchoed` |
| `s3api/listing` | `TestV2Delimiter`, `TestV2MultiCharDelimiter`, `TestV2MaxKeysCountsPrefixes`, `TestV2ContinuationAfterCommonPrefix`, `TestV2StartAfterIgnoredWithToken`, `TestV2EncodingTypeURL`, `TestV2MaxKeysZero`, `TestV2MaxKeysClamped`, `TestV1NextMarkerOnlyWithDelimiter`, `TestV1OwnerAlwaysPresent`, `TestBadContinuationToken` |
| `s3api/objectops` | `TestPutOpCapKeyTooLong`, `TestPutOpCapMetadataTooLarge`, `TestUserMetadata2048`, `TestRFC2047RoundTrip`, `TestEntityTooLargeBeforeBody`, `TestEncryptedSizeLimit`, `TestSSEValues`, `TestCannedACLMatrix`, `TestRangeMatrix` (every row of §8.2), `TestConditionalMatrix`, `TestIfRange`, `TestResponseOverridesSignedOnly`, `TestPartNumberOne`, `TestCopySelfIllegal`, `TestCopyEncryptionNotInherited`, `TestDeleteObjectsQuiet`, `TestDeleteObjects1001Keys`, `TestControlCharacterKeyRejected` |
| `s3api/multipart` | `TestPartNumberRange`, `TestEntityTooSmall`, `TestInvalidPartOrder`, `TestInvalidPartETag`, `TestPartChecksumVerified`, `TestRunningTotalLimit`, `TestCompleteIdempotentRetry`, `TestCompleteAttachesToCommitting`, `TestAbortWhileCommitting`, `TestMultipartETag` |

### 16.2 `test/s3compat` (aws-sdk-go-v2, phase 11 L4)

Runs against an in-process `butterdbd` backed by `internal/tgclient/faketg`
in CI, and against a real deployment when `BUTTERDB_S3COMPAT_ENDPOINT`,
`BUTTERDB_S3COMPAT_ACCESS_KEY_ID`, `BUTTERDB_S3COMPAT_SECRET_FILE` (a file holding the secret),
`BUTTERDB_S3COMPAT_BUCKET` and (for anonymous tests)
`BUTTERDB_S3COMPAT_PUBLIC_BUCKET` are set (plan §9.5). Uses
`github.com/aws/aws-sdk-go-v2/service/s3` and `feature/s3/manager`, each test
run both path-style and virtual-hosted-style (the latter with a base domain
resolved through a custom dialer).

| Test | Proves |
| --- | --- |
| `TestPutGetHeadDelete` | Round trip, ETag, `Last-Modified`, `204` on missing delete |
| `TestOverwriteLastWriterWins` | Second PUT replaces the first (D6, E3) |
| `TestDefaultChecksumTrailer` | SDK default CRC32 `STREAMING-UNSIGNED-PAYLOAD-TRAILER` upload over TLS |
| `TestChecksumWhenRequired` | `RequestChecksumCalculationWhenRequired` uploads |
| `TestChecksumAlgorithms` | CRC32C, CRC64NVME, SHA1, SHA256 |
| `TestUnsignedPayloadMiddleware` | `v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware` |
| `TestStreamingSignedPayload` | aws-chunked with chunk signatures and with a signed trailer. aws-sdk-go-v2 never emits these modes, so the test builds them with its own encoder, itself checked against the AWS chunked-upload documentation example |
| `TestPresignedGetPut` | `s3.NewPresignClient` GET and PUT; expiry; revoked key |
| `TestAuthErrors` | `InvalidAccessKeyId`, `SignatureDoesNotMatch`, `RequestTimeTooSkewed` (request signed 16 minutes in the past with `v4.Signer.SignHTTP`) |
| `TestScopeAndAnonymous` | Scoped keys, public-read anonymous GET via plain `net/http`, private anonymous `403` |
| `TestListObjectsV2Paginator` | 2,500 keys, prefixes, delimiter, `ListObjectsV2Paginator` |
| `TestListObjectsV1` | Marker paging |
| `TestRangeAndConditionals` | `Range`, `If-Match`, `If-None-Match`, `304`, `412`, `416` |
| `TestResponseOverrides` | `ResponseContentDisposition`, `ResponseContentType` |
| `TestMetadataLimits` | 2048 bytes accepted, 2049 rejected, put record cap, `KeyTooLongError` |
| `TestEntityTooLarge` | Declared length over 1900 MiB refused before upload |
| `TestDeleteObjectsQuiet` | Quiet and verbose modes |
| `TestCopyObject` | Same bucket, cross bucket, `REPLACE`, encrypted destination |
| `TestTransferManagerUpload` | `manager.Uploader` 64 MiB in 5 MiB parts |
| `TestUploadPartCopy` | Ranged part copies, then Complete |
| `TestMultipartErrors` | `EntityTooSmall`, `InvalidPartOrder`, `InvalidPart`, `NoSuchUpload` |
| `TestCompleteKeepalive` | faketg latency of 40 s on the body send; the client with a 30 s idle timeout succeeds |
| `TestCompleteErrorIn200` | faketg flood wait past the budget during Complete; SDK surfaces and retries `SlowDown`; the retry succeeds |
| `TestPutSlowDown` | faketg flood wait past the budget on PutObject; `503 SlowDown` with `Retry-After` |
| `TestSSEAES256` | Header and bucket default; `x-amz-server-side-encryption` echoed; range read of an encrypted object |
| `TestUnsupportedReturnNotImplemented` | SSE-C, KMS, versioning PUT, policy PUT, tags |
| `TestBucketGetters` | Location, versioning, policy, ACL, CORS, encryption, tagging, lifecycle |
| `TestCreateBucketDenied` | `409` on existing, `403` on new |
| `TestCORSPreflight` | Allowed and refused preflights, exposed headers |

### 16.3 AWS CLI smoke (manual, release checklist)

Run from [docs/guide/](../guide/) against a real deployment, with a key
scoped to `smoke` (`read`, `list`, `write`, `delete`):

```
export AWS_ENDPOINT_URL=https://s3.example.com AWS_REGION=us-east-1
aws s3 ls
aws s3 cp ./10MiB.bin s3://smoke/a/10MiB.bin
aws s3 cp ./300MiB.bin s3://smoke/a/300MiB.bin
aws s3 ls s3://smoke/a/
aws s3 cp s3://smoke/a/300MiB.bin ./back.bin && cmp 300MiB.bin back.bin
aws s3 cp s3://smoke/a/300MiB.bin s3://smoke/b/copy.bin
aws s3 sync ./dir5 s3://smoke/sync/
aws s3api head-object --bucket smoke --key a/10MiB.bin
aws s3api get-object --bucket smoke --key a/10MiB.bin --range bytes=0-99 part.bin
aws s3api put-object --bucket smoke --key enc.bin --body ./10MiB.bin --server-side-encryption AES256
aws s3 presign s3://smoke/a/10MiB.bin --expires-in 300
aws s3 rm s3://smoke/ --recursive
aws s3 mb s3://smoke
aws s3 mb s3://new-bucket-name
```

Pass: every command succeeds except the last two: `mb s3://smoke` reports
`BucketAlreadyOwnedByYou` and `mb s3://new-bucket-name` reports
`AccessDenied`. The presigned URL downloads with `curl`. The `b/copy.bin`
copy exercises UploadPartCopy and GetObjectTagging. `./dir5` holds five small
files, so the run leaves 9 objects and the final `rm --recursive` fits one
request budget under the default E10 limiter (§8.5). Smoke runs with more
objects must expect `SlowDown` entries from DeleteObjects and re-run `rm`.

### 16.4 rclone smoke (manual)

With the remote of §12.4:

```
rclone lsd butterdb:
rclone copy ./dir butterdb:smoke/rc --progress
rclone check ./dir butterdb:smoke/rc
rclone copyto ./1GiB.bin butterdb:smoke/rc/1GiB.bin
rclone copyto butterdb:smoke/rc/1GiB.bin butterdb:smoke/rc/1GiB-copy.bin
rclone cat butterdb:smoke/rc/1GiB.bin --offset 1000 --count 10
rclone delete butterdb:smoke/rc
```

Pass: `rclone check` reports 0 differences; the server-side copy uses
CopyObject; the 1 GiB upload uses multipart with 16 MiB chunks.

### 16.5 s3cmd smoke (manual)

With the `~/.s3cfg` of §12.4:

```
s3cmd ls
s3cmd put ./10MiB.bin s3://smoke/s3cmd/10MiB.bin
s3cmd put --multipart-chunk-size-mb=16 ./100MiB.bin s3://smoke/s3cmd/100MiB.bin
s3cmd ls s3://smoke/s3cmd/
s3cmd info s3://smoke/s3cmd/10MiB.bin
s3cmd info s3://smoke
s3cmd get s3://smoke/s3cmd/100MiB.bin ./back.bin
s3cmd del --recursive --force s3://smoke/s3cmd/
```

Pass: every command succeeds; `s3cmd info s3://smoke` prints location,
policy, CORS and ACL without errors; `s3cmd get` verifies its stored MD5.

### 16.6 Exit criteria for S3 compatibility

Phase 08 is complete when §16.1 passes and the `test/acceptance/phase08` run
of the aws-sdk-go-v2 calls in plan §9.4 passes against faketg. The v0.1.0
release (phase 11) additionally requires §16.2 in CI and §16.3 to §16.5 run by
the owner against a real Telegram account.

---

## 17. Deviations from AWS S3

| Area | AWS | butterDB | Reason |
| --- | --- | --- | --- |
| Object size | 5 TiB, 5 GiB single PUT | 1900 MiB stored, ciphertext counts | One Telegram document per object (D9) |
| Key and metadata | 1024-byte key, 2 KB metadata | Same, plus the 4096-character put record cap | One Telegram message per commit (E2, plan R4) |
| Control characters in keys | Allowed | Refused | XML 1.0 listing safety |
| Missing key without list permission | `403 AccessDenied` | `404 NoSuchKey` | Existence checks with read-only keys and public reads |
| Anonymous listing of public-read bucket | Allowed by the canned ACL | Denied | Public-read means object reads (D7) |
| CreateBucket, DeleteBucket | S3 API | Admin API only | Bucket lifecycle is owner-only (D3) |
| Repeated CreateBucket in us-east-1 | `200` | `409 BucketAlreadyOwnedByYou` | Matches every other region |
| Versioning | Optional | Never | D10 |
| Bucket policy | JSON policy language | Canned `private` / `public-read` | D7 |
| ACLs | Grants (when enabled) | ACLs-disabled semantics | D7 |
| CORS | Per bucket | Per deployment | No bucket configuration field |
| SSE | SSE-S3, SSE-KMS, SSE-C | `AES256` maps to TDE1 | D11, A5 |
| Default content type | `binary/octet-stream` | `application/octet-stream` | Standard media type |
| Object checksums on GET | Returned when stored | Not returned | Not stored in `t=put` in v1 |
| `partNumber` on GET | Any part | `1` only | Part boundaries are not kept |
| DeleteObjects integrity header | Required | Optional | Compatibility with minimal clients |
| Write conditionals on CopyObject | Supported | `501` | Not built in v1 |
| Region | Many | One, plus `us-east-1` accepted | Single deployment |
| `Last-Modified` precision | Seconds | Seconds (Telegram message date) | Same |

---

## 18. Defaults chosen here (owner may change)

| Item | Default |
| --- | --- |
| S3 listen address | `0.0.0.0:9000` |
| Region | `us-east-1`; `us-east-1` always accepted |
| TLS | Terminated by a reverse proxy; the listener is plain HTTP/1.1 |
| Request budget | `s3.request_budget` 30 s ([operations.md](operations.md)); admission refusal `Retry-After: 5` |
| Proxy read timeout | 1,200 s |
| Client response timeout | 1,500 s, 10 attempts |
| Configuration keys added to `[s3]` | `body_idle_timeout`, `write_idle_timeout`, `keepalive_interval`, `completed_upload_retention`, `auth_failure_limit`, `cors_*` (§2.1) |
| Body and write idle timeouts | 60 s |
| Keepalive interval | 15 s |
| Completed upload retention | 24 h |
| Multipart idle expiry | 7 days |
| Auth failure limit | 100 per 10 min per address, 15 min block |
| CORS | Deployment-wide, disabled until origins are configured |
| Anonymous access | GetObject and HeadObject on public-read buckets only |
| Missing key with `read` | `404 NoSuchKey` |
| Owner ID | HMAC-SHA256(server key, `butterdb/s3/owner-id/v1`), hex |
| Continuation token | `base64url(0x01, kind, key bytes)` |
| XML body limits | DeleteObjects 2 MiB, Complete 4 MiB, others 64 KiB |
| aws-chunked limits | Chunk 16 MiB, chunk header 4 KiB, trailer 16 KiB |
| System header limit | 1024 bytes each |
| `last_used_at` update | At most once per 60 s per key |
