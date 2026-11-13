# Media API specification (Cloudinary-style transforms)

The media API serves resized, cropped and re-encoded **derivatives** of stored
objects from URLs, in the style of Cloudinary delivery URLs (D5, D16). It runs
on its own listener in `butterdbd`. All image and video decoding happens in the
separate worker process `butterdb-transformd` (module `apps/transformd`), so the
server stays a pure-Go `CGO_ENABLED=0` binary (E14). It is built in phase 13 and
ships in v0.3.0 (D17).

| Item | Value |
| --- | --- |
| Listener | Plain HTTP, its own listener, default `0.0.0.0:9080` — Default (owner may change) |
| URL shape | `GET {prefix}/{bucket}/{resource}/[s--{sig}--/]{transformation}/{key}` |
| Methods | `GET`, `HEAD`, `OPTIONS` |
| Resources | `image` (still or animated image source), `video` (poster frame of a video source) |
| Authentication | Public presets, or an HMAC-SHA256 signature segment; never SigV4 |
| Server packages | `internal/mediaapi/{grammar,presets,signing,derivcache,workerclient,handlers}` |
| Worker protocol | `github.com/PRIEYAN/ButterDB/api/transformv1` over a unix socket |
| Worker | `apps/transformd`, binary `butterdb-transformd`; libvips through govips, libheif, ffmpeg subprocess |
| Derivative cache | `<data_dir>/cache/derived/` plus an in-memory tier for encrypted sources |

Bound by: D5, D7, D11, D16, D17, A4, A8, E13, E14, and invariants 2, 11, 12 and
19 of [docs/claude.md](../claude.md) §10.

---

## 1. Principles

1. **Originals are served by the S3 API, byte for byte.** `GetObject` on the S3
   listener returns exactly the bytes that were uploaded, EXIF and GPS
   included (A4, invariant 19). The media API never serves an original. Every
   media response is a freshly encoded derivative, even for a transformation
   that changes no pixels.
2. **Transforms never modify stored objects.** The media API has no write path.
   It never calls `internal/ingest`, never sends a Telegram message, never
   changes `ObjectMeta`, `projection.db` or `control.db`. Its only persistent
   state is the derivative cache, which is disposable: deleting
   `cache/derived/` loses nothing but warm derivatives, and snapshots exclude
   it.
3. **Verify before work.** A signature is verified, and the request is
   authorised, before any object is read, any cache entry is opened, or any
   worker call is made (invariant 12).
4. **Untrusted bytes never meet cgo in the server.** Decoders run only in
   `butterdb-transformd`, which is a separate, sandboxed, restartable process.
   A crash, hang or exploit in libvips, libheif, libde265, libaom, dav1d or
   ffmpeg cannot take down the S3 or admin listeners.
5. **Bounded everything.** Source bytes, source pixels, frames, output
   dimensions, output bytes, processing time, worker memory, worker
   concurrency, queue depth and cache size all have explicit ceilings (§9).
6. **One canonical form per meaning.** Parameter order, number formatting and
   aliases never create distinct cache entries (§6). Output dimensions snap to
   a width ladder (§8).

---

## 2. Configuration

### 2.1 The `[media]` table

Keys live in the `[media]` table of the server TOML file and are overridable by
`BUTTERDB_MEDIA_*` environment variables (`internal/platform/config`). The phase
13 prelude adds the `config.Media` struct. Every default below is Default
(owner may change).

| Key | Env | Default | Meaning |
| --- | --- | --- | --- |
| `media.enabled` | `BUTTERDB_MEDIA_ENABLED` | `false` | Bind the media listener and supervise the worker |
| `media.listen` | `BUTTERDB_MEDIA_LISTEN` | `0.0.0.0:9080` | TCP address of the media listener |
| `media.path_prefix` | `BUTTERDB_MEDIA_PATH_PREFIX` | `""` | Optional prefix such as `/media`; must start with `/` and must not end with `/` |
| `media.public_url` | `BUTTERDB_MEDIA_PUBLIC_URL` | `""` | External base URL, reported as `endpoints.media_public_url` by the admin API |
| `media.hostnames` | `BUTTERDB_MEDIA_HOSTNAMES` | `[]` | Allowed `Host` values when the listener is bound to loopback |
| `media.trusted_proxies` | `BUTTERDB_MEDIA_TRUSTED_PROXIES` | `["127.0.0.1/32", "::1/128"]` | CIDRs whose `X-Forwarded-For` is honoured in logs |
| `media.signing_key_file` | `BUTTERDB_MEDIA_SIGNING_KEY_FILE` | `<data_dir>/secrets/media_signing.key` | Current media signing key (§7.1) |
| `media.signing_key_previous_file` | `BUTTERDB_MEDIA_SIGNING_KEY_PREVIOUS_FILE` | `""` | Previous key, still accepted for verification during rotation |
| `media.private_max_expiry` | `BUTTERDB_MEDIA_PRIVATE_MAX_EXPIRY` | `168h` | Furthest `exp_` a private-bucket URL may carry |
| `media.width_ladder` | `BUTTERDB_MEDIA_WIDTH_LADDER` | §8 ladder | Allowed output sizes in pixels, ascending |
| `media.worker_mode` | `BUTTERDB_MEDIA_WORKER_MODE` | `spawn` | `spawn`: `butterdbd` runs the worker as a child. `external`: systemd or a container runs it |
| `media.worker_path` | `BUTTERDB_MEDIA_WORKER_PATH` | `butterdb-transformd` | Worker binary, resolved once with `exec.LookPath` at start; spawn mode only |
| `media.worker_socket` | `BUTTERDB_MEDIA_WORKER_SOCKET` | `<data_dir>/run/transformd.sock` | Worker unix socket |
| `media.worker_tmp_dir` | `BUTTERDB_MEDIA_WORKER_TMP_DIR` | `<data_dir>/run/transformd-tmp` | Worker scratch directory for video sources; spawn mode only |
| `media.ffmpeg_path` | `BUTTERDB_MEDIA_FFMPEG_PATH` | `/usr/bin/ffmpeg` | Passed to the worker as `--ffmpeg`; spawn mode only |
| `media.worker_concurrency` | `BUTTERDB_MEDIA_WORKER_CONCURRENCY` | `min(NumCPU, 4)` | Concurrent jobs in the worker |
| `media.worker_memory_limit` | `BUTTERDB_MEDIA_WORKER_MEMORY_LIMIT` | `1GiB` | Worker address-space limit; spawn mode only (§15.2) |
| `media.worker_sandbox` | `BUTTERDB_MEDIA_WORKER_SANDBOX` | `auto` | `auto`, `require` or `off` (§18.2); spawn mode only |
| `media.queue_depth` | `BUTTERDB_MEDIA_QUEUE_DEPTH` | `64` | Requests waiting for a worker slot before `503` |
| `media.queue_timeout` | `BUTTERDB_MEDIA_QUEUE_TIMEOUT` | `10s` | Longest wait for a worker slot |
| `media.source_timeout` | `BUTTERDB_MEDIA_SOURCE_TIMEOUT` | `120s` | Longest time to read the original (cache warm plus streaming to the worker) |
| `media.image_timeout` | `BUTTERDB_MEDIA_IMAGE_TIMEOUT` | `20s` | Worker processing budget for an image job, after its source is received |
| `media.poster_timeout` | `BUTTERDB_MEDIA_POSTER_TIMEOUT` | `30s` | Worker processing budget for a poster job, ffmpeg included |
| `media.poison_ttl` | `BUTTERDB_MEDIA_POISON_TTL` | `1h` | Quarantine time for a source that crashed the worker twice (§15.5) |
| `media.negative_ttl` | `BUTTERDB_MEDIA_NEGATIVE_TTL` | `10m` | Memory of a deterministic transform failure (§11.7) |
| `media.cache_max_bytes` | `BUTTERDB_MEDIA_CACHE_MAX_BYTES` | `10GiB` | Disk tier size ceiling |
| `media.cache_memory_max_bytes` | `BUTTERDB_MEDIA_CACHE_MEMORY_MAX_BYTES` | `128MiB` | Memory tier ceiling (encrypted sources) |
| `media.cache_sweep_interval` | `BUTTERDB_MEDIA_CACHE_SWEEP_INTERVAL` | `1h` | Stale-version sweep period (§11.5) |
| `media.max_image_source_bytes` | `BUTTERDB_MEDIA_MAX_IMAGE_SOURCE_BYTES` | `64MiB` | Largest plaintext image source |
| `media.max_video_source_bytes` | `BUTTERDB_MEDIA_MAX_VIDEO_SOURCE_BYTES` | `1GiB` | Largest plaintext video source for a poster |
| `media.max_source_pixels` | `BUTTERDB_MEDIA_MAX_SOURCE_PIXELS` | `50000000` | Largest width × height of one source frame |
| `media.max_source_side` | `BUTTERDB_MEDIA_MAX_SOURCE_SIDE` | `32768` | Largest source width or height |
| `media.max_output_side` | `BUTTERDB_MEDIA_MAX_OUTPUT_SIDE` | `4096` | Largest output width or height |
| `media.max_output_pixels` | `BUTTERDB_MEDIA_MAX_OUTPUT_PIXELS` | `16777216` | Largest output width × height |
| `media.max_output_bytes` | `BUTTERDB_MEDIA_MAX_OUTPUT_BYTES` | `32MiB` | Largest encoded derivative |
| `media.max_frames` | `BUTTERDB_MEDIA_MAX_FRAMES` | `200` | Largest frame count of an animated source kept animated |
| `media.max_animated_pixels` | `BUTTERDB_MEDIA_MAX_ANIMATED_PIXELS` | `200000000` | Largest width × height × frames of an animated source kept animated |
| `media.avif_auto_max_pixels` | `BUTTERDB_MEDIA_AVIF_AUTO_MAX_PIXELS` | `4194304` | `f_auto` chooses AVIF only at or below this output size |

Hard ceilings that no configuration may exceed live in `internal/domain/limits`
(added by the phase 13 prelude):

| Constant | Value |
| --- | --- |
| `MediaMaxOutputSideCeiling` | `8192` |
| `MediaMaxSourcePixelsCeiling` | `268402689` (16383 × 16383) |
| `MediaMaxSourceSideCeiling` | `65535` |
| `MediaMaxImageSourceBytesCeiling` | `256 << 20` |
| `MediaMaxVideoSourceBytesCeiling` | `1900 << 20` (the stored-object ceiling) |
| `MediaMaxTransformationBytes` | `512` |
| `MediaMaxParams` | `16` |
| `MediaMaxPathBytes` | `8192` |
| `MediaMaxPresetsPerBucket` | `64` |
| `MediaMaxPosterMillis` | `86400000` |
| `MediaMaxPrivateExpiry` | `604800 * time.Second` (the SigV4 presign maximum) |

### 2.2 Per-bucket settings and presets

Presets and per-bucket media settings are configured in the TOML file, not in
Telegram. Default (owner may change): they are local configuration, reloaded on
`SIGHUP`, and are not part of the bucket's registry ops; a rebuild from Telegram
does not need them.

```toml
[media]
enabled = true
public_url = "https://img.example.com"

[media.presets]
thumb = "c_thumb,g_auto,h_200,w_200"
card = "c_fill,f_auto,g_auto,h_360,q_auto,w_640"

[media.buckets.photos]
allow_unsigned = false

[media.buckets.photos.presets]
hero = "c_fill,f_auto,g_auto,h_720,w_1920"
poster = "f_jpg,t_2,w_1280"

[media.buckets.internal-scans]
enabled = false
```

| Key | Default | Meaning |
| --- | --- | --- |
| `media.presets.<name>` | none | A preset available in every bucket |
| `media.buckets.<bucket>.enabled` | `true` | `false` answers every media request for the bucket with `403 media.disabled` |
| `media.buckets.<bucket>.allow_unsigned` | `false` | `true` lets a **public-read** bucket accept arbitrary unsigned transformations. Ignored for a private bucket |
| `media.buckets.<bucket>.presets.<name>` | none | A bucket preset; overrides a global preset of the same name |

Settings for a bucket that does not exist are kept and apply if the bucket is
created later. The bucket's policy (`private` or `public-read`, D7) and its
`cache_max_age_seconds` (A8) come from the bucket configuration managed through
the admin API, not from this table.

### 2.3 `check-config` failures

`butterdbd check-config` fails when `media.enabled = true` and any of these hold:

- a preset name does not match `^[a-z][a-z0-9_-]{0,31}$`, a preset value fails
  `grammar.Parse`, a preset contains `t_<name>` (no nesting), `exp_`, or a
  signature, or a bucket has more than `MediaMaxPresetsPerBucket` presets;
- a preset's dimensions exceed `media.max_output_side` after its own `dpr_`;
- `media.path_prefix` is not empty and does not match `^(/[a-z0-9-]+)+$`;
- `media.listen` is a loopback address and both `media.hostnames` and the host
  of `media.public_url` are empty;
- `media.width_ladder` is empty, not strictly ascending, contains a value below
  `1`, or its last value differs from `media.max_output_side`;
- any limit exceeds its ceiling in §2.1, or `media.private_max_expiry` exceeds
  `MediaMaxPrivateExpiry`;
- `media.signing_key_file` exists and is not mode `0600`, or does not decode to
  exactly 32 bytes (§7.1); the same for a non-empty previous key file;
- `media.worker_mode = "spawn"` and the worker binary or `media.ffmpeg_path`
  is not an executable regular file;
- the resolved `media.worker_socket` path is longer than 103 bytes.

---

## 3. URL grammar

### 3.1 Shape

```
{prefix}/{bucket}/{resource}/{transformation}/{key}
{prefix}/{bucket}/{resource}/s--{sig}--/{transformation}/{key}
```

| Segment | Rule |
| --- | --- |
| `prefix` | `media.path_prefix`, matched literally; empty by default |
| `bucket` | A bucket name valid under `internal/domain/bucket` (S3 naming rules) |
| `resource` | `image` or `video` |
| `s--{sig}--` | Optional. Exactly `^s--[A-Za-z0-9_-]{22}--$`: 16 bytes of HMAC, base64url without padding (§7.2) |
| `transformation` | One segment of comma-separated parameters, charset `[a-z0-9_.:,-]`, 1 to 512 bytes, 1 to 16 parameters. No percent-encoding |
| `key` | Everything after the slash that ends `transformation`, percent-decoded once, valid under `internal/domain/object` |

Examples on a deployment whose `media.public_url` is `https://img.example.com`:

```
https://img.example.com/photos/image/t_thumb/2026/cat.jpg
https://img.example.com/photos/image/s--Dl3vXL7iHC1kGZu47mkoRg--/c_fill,exp_1893456000,f_webp,h_360,w_640/2026/cat.jpg
https://img.example.com/photos/image/w_640,h_360,c_fill,q_80,f_webp/2026/cat.jpg
https://img.example.com/clips/video/s--xfILzBFvNlbn9uhw8idqMg--/f_jpg,t_2.5,w_640/reel/intro.mp4
```

The third URL is the example of [plan.md](../plan.md) §6.6; it is accepted only
when the bucket allows unsigned transformations.

### 3.2 Path splitting

`internal/mediaapi/handlers` splits the path itself. The media listener's
handler is a plain `http.Handler`, never an `http.ServeMux`, because `ServeMux`
cleans paths and would redirect keys containing `//`, `/./` or `/../`.

1. Reject a raw path (`r.URL.EscapedPath()`) longer than `MediaMaxPathBytes`
   with `400 media.bad_path`.
2. Strip `media.path_prefix`; a mismatch is `404 object.not_found`.
3. The raw path must start with `/`. Take segments by splitting on `/` from
   the left: `bucket`, `resource`, then either a signature segment followed by
   `transformation`, or `transformation` directly. A segment is a signature
   segment only when it matches the regular expression in §3.1 exactly.
4. The key is the raw remainder after the `/` that ends `transformation`,
   unchanged (it may itself contain `/`, empty segments, or a leading `/`).
   It is decoded once with `url.PathUnescape`. A decoding error, an empty key,
   or a key that `internal/domain/object` rejects is `400 media.bad_path`.
5. `bucket` and `transformation` must not contain `%`; `resource` must be
   `image` or `video`. Otherwise `400 media.bad_path`.
6. The query string is ignored and is never part of the cache key or the
   signature. Default (owner may change).

---

## 4. Transformation parameters

A parameter is `<key>_<value>`. The key is the text before the **first** `_`,
so values may contain `_` (`g_north_east`, `t_hero_wide`). Each key may appear
once, except `t`, which may appear once as a preset and once as a poster time.
A repeated key is `422 media.invalid_transformation`.

### 4.1 Parameter table

| Param | Syntax | Range | Default (effective) | Canonical value |
| --- | --- | --- | --- | --- |
| `w_` | `[1-9][0-9]*` | 1 to `max_output_side` | source width, capped | unchanged |
| `h_` | `[1-9][0-9]*` | 1 to `max_output_side` | source height, capped | unchanged |
| `c_` | `fill`, `fit`, `limit`, `pad`, `thumb` | | `limit` | unchanged |
| `g_` | `center`, `centre`, `north`, `south`, `east`, `west`, `north_east`, `north_west`, `south_east`, `south_west`, `auto` | | `center`; `auto` for `c_thumb` | `centre` becomes `center` |
| `q_` | `[1-9][0-9]{0,2}` or `auto` | 1 to 100 | `auto` | unchanged |
| `f_` | `jpg`, `jpeg`, `png`, `webp`, `avif`, `auto` | | as source (§4.5) | `jpeg` becomes `jpg` |
| `dpr_` | decimal, at most 2 fraction digits | 1.0 to 4.0 | `1` | trailing zeros and a trailing `.` removed (`2.50` becomes `2.5`, `2.0` becomes `2`) |
| `ar_` | `<a>:<b>` integers, or decimal with at most 3 fraction digits | 0.01 to 100 | none | `a:b` reduced by GCD (`32:18` becomes `16:9`); decimal trimmed |
| `b_` | `rgb:RRGGBB`, `rgb:RRGGBBAA`, `white`, `black`, `transparent` | | white for JPEG flattening, transparent padding otherwise | `rgb:` plus lowercase hex; alpha `ff` dropped; names become hex (`transparent` is `rgb:00000000`) |
| `fl_` | flags joined by `.` | §4.7 | `strip` | flags deduplicated and sorted |
| `t_` | preset name `[a-z][a-z0-9_-]{0,31}` | §5 | none | unchanged |
| `t_` | poster seconds `0` or `[1-9][0-9]*`, optionally `.` and 1 to 3 digits | 0 to 86400 | `1` | trailing zeros removed |
| `exp_` | unix seconds `[1-9][0-9]*` | | none | unchanged |

A preset name always starts with a letter and a poster time always starts with
a digit, so the two forms of `t_` never collide.

### 4.2 Crop modes

| Mode | Needs | Upscales | Output size | Behaviour |
| --- | --- | --- | --- | --- |
| `fill` | both dimensions | yes | exactly W × H | Scale to cover the box, then crop by gravity |
| `fit` | at least one dimension | yes | within W × H | Scale to fit inside the box, aspect kept |
| `limit` | at least one dimension | no | within W × H and within the source | As `fit`, but never larger than the source |
| `pad` | both dimensions | yes | exactly W × H | Fit inside the box, then pad with `b_` placed by gravity |
| `thumb` | both dimensions | no | W × H, or smaller with the box's aspect when the source is smaller | Cover-crop with gravity (default `auto`) without upscaling |

"Both dimensions" means `w_` and `h_`, or one of them plus `ar_`. With no
dimension at all, the output keeps the source size, capped to
`max_output_side` and `max_output_pixels` as `c_limit` would.

### 4.3 Gravity

| Value | Meaning |
| --- | --- |
| `center` | Crop or pad around the centre |
| `north`, `south`, `east`, `west` | Anchor to that edge, centred on the other axis |
| `north_east`, `north_west`, `south_east`, `south_west` | Anchor to that corner |
| `auto` | libvips smart crop, `interesting=attention`. Not valid with `c_pad` |

Gravity is valid only with `c_fill`, `c_thumb` and `c_pad`.

### 4.4 Quality

`q_<n>` sets encoder quality. In the effective form an explicit value is snapped
to the nearest multiple of 5 (minimum 5), so `q_83` and `q_85` share one
derivative. `q_auto` resolves per output format:

| Output | `q_auto` | Encoder option |
| --- | --- | --- |
| JPEG | `80` | `Q` |
| WebP | `78` | `Q` |
| AVIF | `55` | `Q` |
| PNG | ignored | `compression=6` |

`q_` together with `fl_lossless` is a conflict.

### 4.5 Format and Accept negotiation

| Request | Output |
| --- | --- |
| `f_jpg`, `f_png`, `f_webp`, `f_avif` | That format. An alpha channel is flattened onto `b_` (default white) for JPEG |
| No `f_` | The source format when it is JPEG, PNG, WebP or AVIF. GIF becomes PNG, or WebP with `fl_animated`. HEIC/HEIF becomes JPEG. A video poster becomes JPEG |
| `f_auto` | Resolved from `Accept` by the server, before the cache lookup (below) |

`f_auto` resolution in `grammar.ClassifyAccept` and `grammar.Resolve`:

1. Parse `Accept` as a comma-separated list of media ranges with optional `q`.
   Only the exact types `image/avif` and `image/webp` count; `image/*` and
   `*/*` never imply them. A type with `q=0` is excluded.
2. Choose **AVIF** when `image/avif` is accepted, the worker reports AVIF
   encoding (§14.6), the output is not animated, and the planned output is at
   most `media.avif_auto_max_pixels`.
3. Otherwise choose **WebP** when `image/webp` is accepted.
4. Otherwise choose the **legacy** class: the worker encodes PNG when the
   output has an alpha channel and JPEG when it does not. The server cannot
   know the source's alpha without decoding, so the cache key records
   `f_legacy`, which the worker resolves deterministically for a given source.

Every `f_auto` response carries `Vary: Accept`, including `304` responses. A
CDN that ignores `Vary` must not front `f_auto` URLs; such deployments use
explicit `f_` values and an HTML `<picture>` element instead.

`f_avif` when the worker cannot encode AVIF is `415 media.unsupported_output_format`.

### 4.6 Background

`b_` colours `c_pad` padding and the matte used when an image with alpha is
encoded as JPEG. It has no other effect.

### 4.7 Flags

| Flag | Effect | Constraints |
| --- | --- | --- |
| `strip` | Remove all metadata (the default; listing it changes nothing) | none |
| `keep_icc` | Keep the embedded ICC profile and skip conversion to sRGB. EXIF, XMP and IPTC are still removed | none |
| `progressive` | Progressive JPEG (`interlace=true`) | applied only when the output is JPEG |
| `lossless` | Lossless WebP or AVIF | output must be `webp`, `avif` or `auto`; no `q_` |
| `animated` | Keep animation of an animated GIF or WebP source (§16.6) | output must be `webp`, `auto` or absent; `g_` must be absent or `center`; resource `image` only |

There is no flag that keeps EXIF, XMP, IPTC or GPS data (A4).

### 4.8 Poster time

`t_<seconds>` selects the poster frame of a `video` resource. It is valid only
with resource `video`. The effective default is `1`. When the video is shorter
than the requested time, the worker retries once at `0` and returns that frame
(§17.3).

### 4.9 Expiry

`exp_<unix seconds>` makes a signed URL stop working after that instant (§7.4).
It is part of the signed string, but it is removed from the effective form, so
URLs that differ only in expiry share one cached derivative.

### 4.10 Conflicts

A transformation that parses but cannot be satisfied is
`422 media.transformation_conflict` with the offending parameter in `param`.

| Rule | `param` |
| --- | --- |
| `c_fill`, `c_thumb` or `c_pad` without both dimensions | `c` |
| `c_fit` or `c_limit` written explicitly without any dimension | `c` |
| `ar_` with both `w_` and `h_`, or with neither | `ar` |
| `g_` with `c_fit`, `c_limit` or no `c_` | `g` |
| `g_auto` with `c_pad` | `g` |
| `dpr_` without `w_` or `h_` | `dpr` |
| `t_<seconds>` with resource `image` | `t` |
| `fl_animated` with resource `video`, with `f_jpg`, `f_png` or `f_avif`, or with `g_` other than `center` | `fl` |
| `fl_lossless` with `f_jpg` or `f_png`, or with `q_` | `fl` |
| `w_` or `h_` above `max_output_side` | `w` or `h` |
| `exp_` in an unsigned request | answered `403 media.signature_required` instead |

---

## 5. Named presets

A preset is a named transformation stored in configuration (§2.2) and used as
`t_<name>`.

- Lookup order: the bucket's presets, then the global presets. An unknown name
  is `422 media.unknown_preset`.
- Expansion: the preset's parsed parameters form the base; every other
  parameter in the request overrides the preset's value for the same key.
  Flags are the union of both sets.
- A preset cannot contain `t_<name>`, `exp_` or a signature (§2.3). It may
  contain `t_<seconds>`.
- Validation (§4.10) runs on the merged result.
- The signature covers the request as written, with the preset **name**, not
  its expansion. The cache key covers the expansion, so editing a preset and
  reloading changes the derivative without breaking any signed URL.

An unsigned request may use presets only (§7.5): its transformation must
consist of exactly one parameter, `t_<name>`.

---

## 6. Canonicalization

Every transformation has three forms.

| Form | Produced by | Used for |
| --- | --- | --- |
| Written | the client | the URL |
| Canonical | `grammar.Transformation.Canonical()` | the signed string (§7.2) |
| Effective | `grammar.Spec.Key()` after `grammar.Resolve` | the cache key (§11.2) and the worker request |

### 6.1 Canonical form

1. Parse each parameter and validate its syntax and range (§4.1). Any failure
   is `422 media.invalid_transformation` with `param` set.
2. Normalise each value as the "Canonical value" column of §4.1 states.
3. Sort parameters by key in byte order: `ar`, `b`, `c`, `dpr`, `exp`, `f`,
   `fl`, `g`, `h`, `q`, `t`, `w`. When `t` appears twice, the preset comes
   before the poster time.
4. Join with `,`.

Defaults are **not** dropped: `q_auto` written explicitly stays in the
canonical form. The canonical form is a pure function of the parameters the
client wrote. Requests in a different order or spelling are accepted and served
without a redirect.

| Written | Canonical |
| --- | --- |
| `w_640,h_360,c_fill,q_80,f_webp` | `c_fill,f_webp,h_360,q_80,w_640` |
| `f_jpeg,g_centre,c_pad,w_300,h_300,b_WHITE` | rejected: `b_WHITE` fails the charset |
| `f_jpeg,g_centre,c_pad,w_300,h_300,b_white` | `b_rgb:ffffff,c_pad,f_jpg,g_center,h_300,w_300` |
| `ar_32:18,w_1280,dpr_2.0` | `ar_16:9,dpr_2,w_1280` |
| `fl_progressive.strip.progressive,q_auto` | `fl_progressive.strip,q_auto` |
| `t_2.500,f_jpg` | `f_jpg,t_2.5` |

### 6.2 Effective form

`grammar.Resolve(t, resource, acceptClass, ladder, limits)` produces a
`grammar.Spec`:

1. Expand the preset (§5).
2. Apply defaults: `c_limit`; `g_center` (or `g_auto` for `c_thumb`) where
   gravity applies; `q_auto`; `t_1` for `video`.
3. Fold `ar_` into the missing dimension: `H = round(W / ar)` or
   `W = round(H × ar)`.
4. Fold `dpr_`: `W = ceil(w × dpr)`, `H = ceil(h × dpr)`. When the result
   exceeds `max_output_side` only because of `dpr_`, scale both dimensions down
   proportionally to fit; when `w_` or `h_` alone exceeds it, the request was
   already rejected in §4.10.
5. Snap to the ladder (§8).
6. Check `W × H ≤ max_output_pixels`, else `422 media.dimensions_too_large`.
7. Resolve `f_auto` to `avif`, `webp` or `legacy` (§4.5); absent `f_` becomes
   `src`.
8. Resolve `q_auto` and snap explicit quality (§4.4); drop quality for PNG.
9. Drop `exp_`, `dpr_` and `ar_`.

`Spec.Key()` serialises the result in the canonical order with every value
explicit, for example
`b_rgb:ffffff,c_fill,f_webp,g_auto,h_405,q_80,w_720`. Two requests whose
effective keys are equal are served by the same derivative.

---

## 7. Signed transformations

### 7.1 The media signing key

One key per deployment signs media URLs. It is a separate secret from the
server key file (A6) and from every S3 access key.

| Item | Value |
| --- | --- |
| File | `media.signing_key_file`, default `<data_dir>/secrets/media_signing.key` |
| Content | 32 random bytes, base64url without padding (43 characters), then `\n` |
| Mode | `0600`, read through `internal/platform/secretfile` |
| Creation | `butterdbd serve` creates it with `crypto/rand` when `media.enabled` and the file is absent, and logs one `WARN` line naming the path (never the key) |
| Distribution | The owner copies it from the host into the applications that build media URLs. Default (owner may change): the admin API does not reveal it |
| Rotation | Move the current file to `media.signing_key_previous_file`, create a new current file, reload with `SIGHUP`. Both keys verify; only the current key signs. Remove the previous file once old URLs have expired |

Example key used by the test vectors: bytes `0x00` to `0x1f`, file content
`AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8`.

Anyone holding this key can request any transformation of any object in any
bucket with media enabled, private buckets included. It is as sensitive as an
access key with read on every bucket. A bucket that must never be reachable
through media URLs sets `enabled = false` (§2.2).

### 7.2 Algorithm

```
string_to_sign = "BDBMEDIA1" "\n" bucket "\n" resource "\n" canonical "\n" key
mac            = HMAC-SHA256(media_signing_key, string_to_sign)
signature      = base64url_nopad(mac[0:16])
segment        = "s--" signature "--"
```

- `canonical` is the canonical form of §6.1, including `exp_` when present.
- `key` is the decoded object key, exactly the bytes stored in S3, never the
  percent-encoded form.
- Verification recomputes the MAC with the current key, then the previous key,
  and compares with `hmac.Equal` on the 16 decoded bytes.

Reference implementation for client applications:

```go
func MediaSignature(secret []byte, bucket, resource, canonical, key string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("BDBMEDIA1\n" + bucket + "\n" + resource + "\n" + canonical + "\n" + key))
	return "s--" + base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16]) + "--"
}
```

Test vectors with the example key of §7.1 (exercised by
`TestSignVectors` in `internal/mediaapi/signing`):

| bucket | resource | canonical | key | segment |
| --- | --- | --- | --- | --- |
| `photos` | `image` | `c_fill,exp_1893456000,f_webp,h_360,w_640` | `2026/cat.jpg` | `s--Dl3vXL7iHC1kGZu47mkoRg--` |
| `photos` | `image` | `t_thumb` | `2026/cat.jpg` | `s--1bPHGTznlZg6hQ9C0FSgdg--` |
| `clips` | `video` | `f_jpg,t_2.5,w_640` | `reel/intro.mp4` | `s--xfILzBFvNlbn9uhw8idqMg--` |

### 7.3 When a signature is required

| Bucket policy | `allow_unsigned` | Transformation | Unsigned request | Signed request |
| --- | --- | --- | --- | --- |
| `public-read` | `false` (default) | exactly `t_<name>` | allowed | allowed |
| `public-read` | `false` | anything else | `403 media.unsigned_not_allowed` | allowed |
| `public-read` | `true` | anything without `exp_` | allowed | allowed |
| `private` | ignored | anything | `403 media.signature_required` | allowed only with a valid `exp_` (§7.4) |

A signature that is present is always verified, even where an unsigned request
would be allowed; an invalid one is `403 media.signature_invalid`.

### 7.4 Expiry

- When `exp_` is present, a request at or after that unix second is
  `403 media.signature_expired`.
- For a private bucket `exp_` is mandatory (`403 media.signature_required`
  when missing) and must be at most `media.private_max_expiry` after the
  server's current time (`403 media.expiry_too_far`). This mirrors SigV4
  presigned URLs for private objects (D7).
- For a public-read bucket `exp_` is optional and has no upper bound.
- Time comes from the injected `clock.Clock`; no clock-skew allowance is added.

### 7.5 Verification order

1. Parse the path and the transformation's syntax (§3.2, §6.1 step 1).
2. Look up the bucket. Unknown: `404 bucket.not_found`. Media disabled:
   `403 media.disabled`.
3. Apply §7.3 and §7.4 (`signing.Signer.Authorize`).
4. Only now: preset expansion, conflict validation, object lookup, cache and
   worker.

A bucket's existence is observable through 404 versus 403, as it is on the S3
API (`NoSuchBucket` versus `AccessDenied`). Object existence in a private
bucket is never observable without a valid signature.

---

## 8. Width ladder

Effective output dimensions snap **up** to the next rung, which bounds the
number of distinct derivatives per object and lets nearby sizes share a cache
entry.

Default ladder (Default (owner may change)):

```
16 32 48 64 80 96 128 160 192 240 256 320 384 400 480 512 576 640 720 768
800 960 1024 1080 1200 1280 1440 1536 1600 1920 2048 2400 2560 2880 3072
3200 3840 4096
```

Rules applied in §6.2 step 5:

| Case | Rule |
| --- | --- |
| `W` set | `W' = smallest rung ≥ W`. When `H` is also set, `H' = round(H × W' / W)` |
| Only `H` set | `H' = smallest rung ≥ H` |
| Neither | Source size, capped (no snapping) |

`c_limit` and `c_thumb` still never upscale, so a snapped size larger than the
source produces the source size. Snapping can make the output up to one rung
larger than requested; it never makes it smaller. Responsive markup that wants
exact sizes should use ladder values.

---

## 9. Limits

| Limit | Default | Enforced by | Failure |
| --- | --- | --- | --- |
| Path length | 8192 bytes | handlers | `400 media.bad_path` |
| Transformation length / params | 512 bytes / 16 | grammar | `422 media.invalid_transformation` |
| Output side | `max_output_side` 4096 | grammar, worker | `422 media.dimensions_too_large` |
| Output pixels | `max_output_pixels` 16777216 | grammar, worker | `422 media.dimensions_too_large` |
| Image source size | `max_image_source_bytes` 64 MiB, from `ObjectMeta` plaintext size | handlers | `422 media.source_too_large` |
| Video source size | `max_video_source_bytes` 1 GiB | handlers | `422 media.source_too_large` |
| Source pixels per frame | `max_source_pixels` 50 MP | worker, from the header, before decoding pixels | `422 media.source_too_many_pixels` |
| Source side | `max_source_side` 32768 | worker | `422 media.source_too_many_pixels` |
| Animated frames | `max_frames` 200 | worker | `422 media.too_many_frames` |
| Animated pixels | `max_animated_pixels` 200 MP (width × height × frames) | worker | `422 media.too_many_frames` |
| Encoded output | `max_output_bytes` 32 MiB | worker | `422 media.output_too_large` |
| Poster time | 86400 s | grammar | `422 media.invalid_transformation` |
| Source read | `source_timeout` 120 s | handlers, workerclient | `503 media.timeout` |
| Processing | `image_timeout` 20 s, `poster_timeout` 30 s | worker watchdog, workerclient | `503 media.timeout` |
| Queue | `queue_depth` 64, `queue_timeout` 10 s | workerclient | `503 media.worker_busy` |
| Worker memory | `worker_memory_limit` 1 GiB | worker rlimit, cgroup in external mode | worker crash, `503 media.worker_unavailable` |

**Animated images policy.** An animated GIF, WebP or AVIF sequence source is
rendered as its first frame unless `fl_animated` is set. With `fl_animated` the
output is animated WebP, the frame and animated-pixel limits apply, and gravity
is fixed to `center`. AVIF and PNG output are never animated, and the media API
never encodes GIF.

---

## 10. Request processing

`internal/mediaapi/handlers` runs these steps for `GET` and `HEAD`.

1. Method: `OPTIONS` returns `204` with `Allow: GET, HEAD, OPTIONS`; anything
   else but `GET` and `HEAD` is `405 media.method_not_allowed`.
2. Host check when the listener is loopback (invariant 12).
3. Split the path (§3.2) and parse the transformation (§6.1 step 1).
4. Bucket lookup and authorisation (§7.5).
5. Preset expansion and conflict validation (§5, §4.10).
6. `Objects.Stat(bucket, key)`: the current version (the put message ID),
   plaintext size, encryption flag and `Last-Modified`. Missing:
   `404 object.not_found`. Size above the limit for the resource:
   `422 media.source_too_large`.
7. Accept classification and `grammar.Resolve` (§6.2); build the cache key
   (§11.2).
8. Quarantine and negative-cache check (§15.5, §11.7).
9. Cache lookup. A hit is served (§12.1) with conditional request handling.
10. Miss: join or start the single flight for the cache key (§11.6). The
    flight:
    1. reads the original once through `readpath/objectreader` into
       `io.Discard` to warm the block cache, under `source_timeout`;
    2. reads the first 64 bytes and classifies the magic number (§16.2); a
       class that does not match the resource is
       `415 media.unsupported_source_format`, before any worker slot is taken;
    3. takes a worker slot (§15.4);
    4. streams the original from the block cache to the worker with a
       `transformv1.Request` (§14);
    5. writes the output to the cache (§11.3) and returns its entry.
11. Serve the entry (§12.1).

The flight runs under a context detached from the client request, bounded by
`source_timeout + image_timeout` (or `poster_timeout`). A client that
disconnects does not cancel a flight that others may be waiting on, and a
client that times out on a slow first request finds the derivative cached on
retry.

No step holds a SQLite transaction across the worker call or the read
(invariant 2). `Objects.Stat` is one read on the read-only WAL pool (E7).

---

## 11. Derivative cache (`internal/mediaapi/derivcache`)

### 11.1 Tiers

| Tier | Used for | Location | Ceiling |
| --- | --- | --- | --- |
| Disk | Derivatives of objects stored **unencrypted** | `<data_dir>/cache/derived/` | `media.cache_max_bytes` |
| Memory | Derivatives of objects stored **encrypted** (D11) | process memory, LRU | `media.cache_memory_max_bytes` |

Encrypted objects are kept encrypted at rest in Telegram and in the block cache.
Writing their decoded derivatives to disk would leave plaintext at rest, so
they live only in memory. Default (owner may change).

### 11.2 Cache key

```
cache_key = SHA-256(
  "BDBDERIV1" "\n" bucket "\n" key "\n" version "\n" resource "\n"
  effective "\n" protocol_version "\n" pipeline_version)
```

| Field | Source |
| --- | --- |
| `version` | Decimal put message ID of the object's current `t=put` (the object version identity) |
| `effective` | `grammar.Spec.Key()` (§6.2) |
| `protocol_version` | `transformv1.ProtocolVersion` |
| `pipeline_version` | `Health.PipelineVersion` reported by the running worker (§14.6) |

An overwrite produces a new put message ID, so it can never be served a stale
derivative, with no purge needed for correctness (E3). A worker upgrade that
changes encoder output changes `pipeline_version` and therefore every key.

### 11.3 Disk layout

```
cache/derived/
├── tmp/                                   in-progress writes, cleared at start
└── <oo>/<object hash>/
    ├── object.json                        {"bucket": "...", "key": "..."}
    └── <version>/
        └── <cache key hex>.bdv
```

- `<object hash>` is the hex SHA-256 of `bucket "\x00" key`; `<oo>` is its
  first two characters.
- A `.bdv` file is `BDV1` (4 bytes), a big-endian `uint32` metadata length, a
  JSON `derivcache.Entry`, then the encoded bytes.
- Writes go to `tmp/` and are renamed into place. No `fsync`: a torn or short
  file is detected on read by comparing `Entry.Size` with the remaining
  length, deleted, and treated as a miss.

### 11.4 Eviction

- At start, `derivcache.Open` scans the tree into an in-memory index of path,
  size and modification time, and removes everything under `tmp/`.
- A hit refreshes the file's modification time at most once per hour
  (`os.Chtimes`), because access times are unreliable under `noatime` and
  `relatime`.
- When the indexed total exceeds `cache_max_bytes`, a background evictor
  deletes the least recently modified files until the total is at or below
  90 % of the ceiling, then removes empty directories.
- The memory tier evicts least recently used entries on insert.

### 11.5 Stale-version reclamation

Correctness never depends on purging. Space is reclaimed three ways:

1. **Lazy:** on a disk write under `<object hash>/<version>/`, sibling version
   directories of the same object are deleted asynchronously.
2. **Sweep:** every `cache_sweep_interval`, `Cache.Sweep` reads each
   `object.json`, asks the `VersionLookup` port for the current version, and
   deletes version directories that are not current, or the whole object
   directory when the object no longer exists.
3. **Eviction** (§11.4).

`Cache.PurgeObject(bucket, key)` deletes an object's directory and memory
entries at once; `handlers` calls it asynchronously whenever `Objects.Stat`
returns `errs.ErrNoSuchKey` (a no-op when nothing is cached). Delete flow
[plan.md](../plan.md) §6.3 step 7 is satisfied by the sweep.

### 11.6 Single flight

At most one worker job runs per cache key. Concurrent requests for the same key
wait on the first flight and share its result or its error. The flight table
lives in `internal/mediaapi/handlers` and is keyed by the cache key hex.

### 11.7 Negative cache

A deterministic failure (`415`, or a `422` from the worker) is remembered per
cache key for `media.negative_ttl` in a bounded in-memory map (10000 entries,
LRU), so a client retrying a broken source does not re-run the worker.
Timeouts, busy and crash failures are never remembered here.

### 11.8 Startup and snapshots

The cache directory is created mode `0700`. `butterdbd snapshot` excludes
`cache/derived/`, as it excludes `cache/blocks/`.

---

## 12. Responses

### 12.1 Success headers

| Header | Value |
| --- | --- |
| `Content-Type` | `image/jpeg`, `image/png`, `image/webp` or `image/avif`, from the worker response |
| `Content-Length` | Entry size |
| `ETag` | `"` + first 32 hex characters of the cache key + `"` (strong) |
| `Last-Modified` | The object's `Last-Modified` |
| `Cache-Control` | §12.2 |
| `Vary` | `Accept` when the written or preset-expanded transformation uses `f_auto` |
| `Accept-Ranges` | `bytes` |
| `X-Content-Type-Options` | `nosniff` |
| `Content-Disposition` | `inline` |
| `Access-Control-Allow-Origin` | `*`, for public-read buckets only |

The body is served with `http.ServeContent`, which handles `If-None-Match`,
`If-Modified-Since` (`304`), `If-Match` and `If-Unmodified-Since` (`412`), and
single `Range` requests (`206`, `416`). `HEAD` runs the full pipeline, cache
fill included, and omits the body. Media responses never carry
`x-amz-server-side-encryption` or `x-amz-meta-*`.

### 12.2 Cache-Control

| Case | `Cache-Control` |
| --- | --- |
| Public-read bucket | `public, max-age=<cache_max_age_seconds>` (never `immutable`, A8) |
| Public-read bucket, `exp_` present | `public, max-age=<min(cache_max_age_seconds, exp - now)>` |
| Private bucket | `private, max-age=<min(cache_max_age_seconds, exp - now)>` |
| Any error, `3xx` excluded | `no-store` |

### 12.3 Error body

Errors are JSON, with the stable code also in a header so image tags and CDNs
can log it.

```
HTTP/1.1 422 Unprocessable Entity
Content-Type: application/json; charset=utf-8
Cache-Control: no-store
X-Butterdb-Error-Code: media.transformation_conflict

{"error":{"code":"media.transformation_conflict","message":"g_ requires c_fill, c_thumb or c_pad","param":"g"}}
```

`param` is present only for transformation errors. Messages never contain the
signature, the signing key, object bytes or worker stderr. `503` responses
carry `Retry-After` (seconds).

### 12.4 Error table

The phase 13 prelude adds the `ErrMedia*` sentinels to `internal/domain/errs`.

| HTTP | Code | Sentinel | When |
| --- | --- | --- | --- |
| 400 | `media.bad_path` | `ErrMediaBadPath` | Path too long, bad segment, bad percent-encoding, invalid bucket name or key |
| 403 | `media.signature_required` | `ErrMediaSignatureRequired` | Private bucket without signature or without `exp_`; `exp_` in an unsigned request |
| 403 | `media.signature_invalid` | `ErrMediaSignatureInvalid` | Signature does not verify under the current or previous key |
| 403 | `media.signature_expired` | `ErrMediaSignatureExpired` | Now ≥ `exp_` |
| 403 | `media.expiry_too_far` | `ErrMediaExpiryTooFar` | Private bucket `exp_` beyond `private_max_expiry` |
| 403 | `media.unsigned_not_allowed` | `ErrMediaUnsignedNotAllowed` | Unsigned non-preset transformation on a strict bucket |
| 403 | `media.disabled` | `ErrMediaDisabled` | `media.buckets.<bucket>.enabled = false` |
| 404 | `bucket.not_found` | `ErrNoSuchBucket` | Unknown bucket |
| 404 | `object.not_found` | `ErrNoSuchKey` | Unknown key, or prefix mismatch |
| 405 | `media.method_not_allowed` | `ErrMediaMethodNotAllowed` | Method other than GET, HEAD, OPTIONS |
| 415 | `media.unsupported_source_format` | `ErrMediaUnsupportedSource` | Source magic not in the allowlist for the resource (§16.2) |
| 415 | `media.unsupported_output_format` | `ErrMediaUnsupportedOutput` | `f_avif` while the worker lacks an AV1 encoder |
| 422 | `media.invalid_transformation` | `ErrMediaInvalidTransformation` | Unknown key, bad value, duplicate, too long, too many params |
| 422 | `media.transformation_conflict` | `ErrMediaTransformationConflict` | §4.10 |
| 422 | `media.unknown_preset` | `ErrMediaUnknownPreset` | `t_<name>` not configured for the bucket |
| 422 | `media.dimensions_too_large` | `ErrMediaDimensionsTooLarge` | Output side or pixels above the limit |
| 422 | `media.source_too_large` | `ErrMediaSourceTooLarge` | Plaintext size above the source-size limit |
| 422 | `media.source_too_many_pixels` | `ErrMediaSourceTooManyPixels` | Source dimensions above the limits |
| 422 | `media.too_many_frames` | `ErrMediaTooManyFrames` | Animated limits exceeded with `fl_animated` |
| 422 | `media.source_undecodable` | `ErrMediaSourceUndecodable` | Truncated or corrupt source |
| 422 | `media.output_too_large` | `ErrMediaOutputTooLarge` | Encoded output above `max_output_bytes` |
| 422 | `media.poster_failed` | `ErrMediaPosterFailed` | ffmpeg produced no frame at `t` or at `0` |
| 422 | `media.source_rejected` | `ErrMediaSourceRejected` | Source quarantined after crashing the worker (§15.5) |
| 503 | `media.worker_unavailable` | `ErrMediaWorkerUnavailable` | Worker down, restarting, or crashed during this job; `Retry-After: 2` |
| 503 | `media.worker_busy` | `ErrMediaWorkerBusy` | Queue full or queue timeout; `Retry-After: 2` |
| 503 | `media.timeout` | `ErrMediaTimeout` | Source read or processing budget exceeded; `Retry-After: 5` |
| 503 | `telegram.slow_down` | `ErrSlowDown` | Reading the original hit a flood wait beyond the request budget (A7) |
| 500 | `internal.error` | any unmapped error | |

Error-to-status mapping is a table in `internal/mediaapi/handlers/errors.go`
branching with `errors.Is` (invariant 11).

---

## 13. Metadata stripping and orientation (A4)

Every derivative goes through the same finishing steps in the worker,
regardless of transformation:

1. **Auto-orient.** EXIF `Orientation` 2 to 8 is applied to the pixels (libvips
   `autorot`, done by the thumbnail operation by default). HEIF `irot` and
   `imir` transforms are applied by libheif at decode. The orientation tag is
   then removed, so viewers never rotate twice.
2. **Colour.** An embedded ICC profile is used to convert to sRGB, and the
   profile is dropped. With `fl_keep_icc`, the profile is kept and no
   conversion runs.
3. **Strip.** The encoder writes no EXIF, XMP, IPTC, GPS, maker notes,
   comments or thumbnails (libvips ≥ 8.15 save option `keep=none`, or
   `keep=icc` with `fl_keep_icc`).
4. **Verify.** In tests, every fixture's output is parsed and asserted to
   contain no `APP1`/`APP13` JPEG segments, no PNG `eXIf`, `tEXt`, `iTXt` or
   `zTXt` chunks, no WebP `EXIF` or `XMP ` chunks, and no HEIF `Exif` or `mime`
   items (§22.3).

Originals are untouched: the S3 API keeps serving the uploaded bytes with their
metadata (invariant 19). Stripping is a property of the derivative, not a
change to the object.

---

## 14. Worker protocol (`api/transformv1`)

`api/transformv1` is a public contract package (standard library only),
committed by the phase 13 prelude. The server's `workerclient` and the worker's
`internal/server` both use it.

### 14.1 Transport

- One unix stream socket at `media.worker_socket`. The worker listens; the
  server connects.
- **One request per connection.** The server dials, writes one request frame
  with its payload, reads one response frame with its payload, and closes.
  Dialing a unix socket costs microseconds, and a broken connection affects
  exactly one job.
- Spawn mode: the worker creates the socket with mode `0600` inside
  `<data_dir>/run/` (mode `0700`), as invariant 12 requires. External mode: the
  worker is started with `--socket-mode 0660` and both processes share a group
  (§21.2). Default (owner may change).
- The server verifies after connect, with `SO_PEERCRED`, that the peer's uid is
  its own uid (spawn) or the configured worker uid (external); a mismatch
  closes the connection and logs `ERROR`.

### 14.2 Framing

```
frame   = magic header_len header payload
magic   = "BTX1"                       4 bytes
header_len = uint32 big-endian         1 .. 65536
header  = UTF-8 JSON                   Request or Response
payload = exactly SourceSize (request) or OutputSize (response) bytes
```

The reader rejects a wrong magic (`ErrBadMagic`), a header length of `0` or
above `MaxHeaderBytes` (`ErrHeaderTooLarge`), JSON with unknown fields
(`ErrBadHeader`), a version other than `ProtocolVersion` (`ErrVersion`), and a
payload that ends early (`io.ErrUnexpectedEOF`).

### 14.3 Contract

```go
package transformv1

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const (
	ProtocolVersion = 1
	Magic           = "BTX1"
	MaxHeaderBytes  = 64 << 10
)

var (
	ErrBadMagic       = errors.New("transformv1: bad magic")
	ErrHeaderTooLarge = errors.New("transformv1: header too large")
	ErrBadHeader      = errors.New("transformv1: bad header")
	ErrVersion        = errors.New("transformv1: unsupported protocol version")
)

type Op string

const (
	OpTransform Op = "transform"
	OpHealth    Op = "health"
)

type Resource string

const (
	ResourceImage Resource = "image"
	ResourceVideo Resource = "video"
)

type Crop string

const (
	CropFill  Crop = "fill"
	CropFit   Crop = "fit"
	CropLimit Crop = "limit"
	CropPad   Crop = "pad"
	CropThumb Crop = "thumb"
)

type Gravity string

const (
	GravityCenter    Gravity = "center"
	GravityNorth     Gravity = "north"
	GravitySouth     Gravity = "south"
	GravityEast      Gravity = "east"
	GravityWest      Gravity = "west"
	GravityNorthEast Gravity = "north_east"
	GravityNorthWest Gravity = "north_west"
	GravitySouthEast Gravity = "south_east"
	GravitySouthWest Gravity = "south_west"
	GravityAuto      Gravity = "auto"
)

type Format string

const (
	FormatJPEG   Format = "jpg"
	FormatPNG    Format = "png"
	FormatWebP   Format = "webp"
	FormatAVIF   Format = "avif"
	FormatSource Format = "src"
	FormatLegacy Format = "legacy"
)

type Spec struct {
	Width         int     `json:"w"`
	Height        int     `json:"h"`
	Crop          Crop    `json:"c"`
	Gravity       Gravity `json:"g"`
	Format        Format  `json:"f"`
	Quality       int     `json:"q"`
	Background    [4]byte `json:"b"`
	HasBackground bool    `json:"has_b"`
	KeepICC       bool    `json:"keep_icc"`
	Progressive   bool    `json:"progressive"`
	Lossless      bool    `json:"lossless"`
	Animated      bool    `json:"animated"`
	PosterMillis  int64   `json:"poster_ms"`
}

type Limits struct {
	MaxSourcePixels   int64 `json:"max_source_pixels"`
	MaxSourceSide     int   `json:"max_source_side"`
	MaxOutputSide     int   `json:"max_output_side"`
	MaxOutputPixels   int64 `json:"max_output_pixels"`
	MaxOutputBytes    int64 `json:"max_output_bytes"`
	MaxFrames         int   `json:"max_frames"`
	MaxAnimatedPixels int64 `json:"max_animated_pixels"`
}

type Request struct {
	Version       int      `json:"v"`
	Op            Op       `json:"op"`
	ID            string   `json:"id"`
	Resource      Resource `json:"resource"`
	Spec          Spec     `json:"spec"`
	Limits        Limits   `json:"limits"`
	ProcessMillis int64    `json:"process_ms"`
	SourceSize    int64    `json:"source_size"`
}

type Status string

const (
	StatusOK    Status = "ok"
	StatusError Status = "error"
)

type Code string

const (
	CodeUnsupportedSource Code = "unsupported_source_format"
	CodeUnsupportedOutput Code = "unsupported_output_format"
	CodeUndecodable       Code = "source_undecodable"
	CodeTooManyPixels     Code = "source_too_many_pixels"
	CodeTooManyFrames     Code = "too_many_frames"
	CodeOutputTooLarge    Code = "output_too_large"
	CodePosterFailed      Code = "poster_failed"
	CodeInvalidSpec       Code = "invalid_spec"
	CodeTimeout           Code = "timeout"
	CodeBusy              Code = "busy"
	CodeInternal          Code = "internal"
)

type Health struct {
	PipelineVersion string   `json:"pipeline_version"`
	Libvips         string   `json:"libvips"`
	Libheif         string   `json:"libheif"`
	FFmpeg          string   `json:"ffmpeg"`
	InputFormats    []string `json:"input_formats"`
	OutputFormats   []string `json:"output_formats"`
	Concurrency     int      `json:"concurrency"`
	InFlight        int      `json:"in_flight"`
	Sandbox         string   `json:"sandbox"`
	StartedUnix     int64    `json:"started_unix"`
}

type Response struct {
	Version     int     `json:"v"`
	ID          string  `json:"id"`
	Status      Status  `json:"status"`
	Code        Code    `json:"code,omitempty"`
	Message     string  `json:"message,omitempty"`
	ContentType string  `json:"content_type,omitempty"`
	Format      Format  `json:"format,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	OutputSize  int64   `json:"output_size"`
	Health      *Health `json:"health,omitempty"`
}

func WriteRequest(w io.Writer, req Request, src io.Reader) error
func ReadRequest(r io.Reader) (Request, io.Reader, error)
func WriteResponse(w io.Writer, resp Response, out io.Reader) error
func ReadResponse(r io.Reader) (Response, io.Reader, error)
func (s Spec) Validate(l Limits) error
```

`ReadRequest` and `ReadResponse` return the payload as an `io.LimitReader` of
exactly the declared size. The files are `api/transformv1/{protocol.go,
frame.go,spec.go}` with `frame_test.go` beside them.

### 14.4 Transform exchange

| Field | Set by the server to |
| --- | --- |
| `Version` | `ProtocolVersion` |
| `Op` | `transform` |
| `ID` | The cache key hex (64 characters); used for quarantine and log correlation |
| `Resource` | `image` or `video` |
| `Spec` | The resolved `grammar.Spec`, converted in `handlers` |
| `Limits` | From `[media]` |
| `ProcessMillis` | `image_timeout` or `poster_timeout` in milliseconds; the budget starts when the worker has received the whole payload |
| `SourceSize` | The object's plaintext size; the payload is exactly this many bytes |

The worker answers `StatusOK` with `ContentType`, the concrete `Format` (`src`
and `legacy` never appear in a response), `Width`, `Height`, `OutputSize` and
the encoded bytes; or `StatusError` with a `Code`, a short `Message` and
`OutputSize: 0`.

### 14.5 Worker code to server error

| Worker `Code` | Server sentinel | Negative-cached |
| --- | --- | --- |
| `unsupported_source_format` | `ErrMediaUnsupportedSource` (415) | yes |
| `unsupported_output_format` | `ErrMediaUnsupportedOutput` (415) | yes |
| `source_undecodable` | `ErrMediaSourceUndecodable` (422) | yes |
| `source_too_many_pixels` | `ErrMediaSourceTooManyPixels` (422) | yes |
| `too_many_frames` | `ErrMediaTooManyFrames` (422) | yes |
| `output_too_large` | `ErrMediaOutputTooLarge` (422) | yes |
| `poster_failed` | `ErrMediaPosterFailed` (422) | yes |
| `invalid_spec` | `internal.error` (500); the server and worker disagree, which is a bug | no |
| `timeout` | `ErrMediaTimeout` (503) | no |
| `busy` | `ErrMediaWorkerBusy` (503) | no |
| `internal` | `internal.error` (500) | no |

### 14.6 Health exchange

`Op: health`, `SourceSize: 0`, `ID` a random 16-hex-character string. The
response carries `Health` and `OutputSize: 0`.

- `PipelineVersion` is `"<worker semver>+vips<libvips version>+heif<libheif version>"`,
  for example `0.3.0+vips8.16.1+heif1.19.8`. It enters every cache key (§11.2).
- `OutputFormats` contains `avif` only when libheif reports an AV1 encoder at
  start; `InputFormats` contains `heic` only when it reports an HEVC decoder.
- `Sandbox` is `landlock-v<abi>`, `external` or `none` (§18.2).

---

## 15. Worker supervision (`internal/mediaapi/workerclient`)

`workerclient.Supervisor` is a `lifecycle.Component`. It is an adapter: it
imports `api/transformv1` and the standard library, never an application
package.

### 15.1 Modes

| Mode | Who starts the worker | Restart | Use |
| --- | --- | --- | --- |
| `spawn` | `Supervisor.Start` executes `media.worker_path` as a child | The supervisor, with backoff (§15.3) | Bare metal and single-host systemd |
| `external` | systemd unit or container (§21) | systemd `Restart=always` or the container runtime; the supervisor only probes | Separate user, cgroup and network namespace |

### 15.2 Spawn contract

The child is started with `exec.Cmd`, never through a shell:

| Field | Value |
| --- | --- |
| `Path` | Absolute path from `exec.LookPath(media.worker_path)`, resolved once at start |
| `Args` | `butterdb-transformd --socket <socket> --tmp-dir <tmp dir> --ffmpeg <ffmpeg path> --concurrency <n> --memory-limit <bytes> --sandbox <mode> --socket-mode 0600`, where `<mode>` is `media.worker_sandbox` |
| `Env` | Exactly `PATH=/usr/bin:/bin`, `HOME=<tmp dir>`, `VIPS_BLOCK_UNTRUSTED=1`, `VIPS_CONCURRENCY=2`, `MALLOC_ARENA_MAX=2`, `GOMEMLIMIT=<memory limit / 4>`. Nothing is inherited from the server's environment |
| `Dir` | `<tmp dir>` |
| `Stdin` | `nil` |
| `Stdout` | discarded |
| `Stderr` | Pipe; each JSON line is re-logged at its own level with `component=transformd`, lines cut at 8 KiB |
| `ExtraFiles` | none |
| `SysProcAttr` | `Setpgid: true`, `Pdeathsig: syscall.SIGKILL` |
| Stop | `SIGTERM` to the process group, then `SIGKILL` after 5 s |

The supervisor creates `<data_dir>/run/` (mode `0700`) and the tmp dir (mode
`0700`), removes a stale socket file, starts the child, and waits up to 10 s
for a successful health exchange. Startup failure leaves the media listener
answering `503 media.worker_unavailable` and the supervisor retrying with
backoff; it never blocks the S3 listener or `/readyz` (D16).

### 15.3 Restart with backoff

- A child exit, a failed health check (3 consecutive failures, probed every
  15 s with a 2 s timeout), or a job overrunning its budget by more than 5 s
  (the server-side watchdog, which sends `SIGKILL` to the process group in
  spawn mode) marks the worker down.
- Restart delay starts at 250 ms, doubles to a maximum of 30 s, with ±20 %
  jitter, and resets after 60 s of healthy uptime.
- While down, new requests fail fast with `503 media.worker_unavailable` and
  `Retry-After: 2`; they are not queued.
- In external mode the same probing applies, but the supervisor never starts or
  kills a process; it reconnects with the same backoff.
- `butterdb_media_worker_restarts_total` counts restarts; every restart logs
  `ERROR` with the exit status or signal.

### 15.4 Concurrency and queueing

- `media.worker_concurrency` slots, held from dial to close of a job's
  connection. The worker enforces the same number and answers `busy` past it.
- Up to `media.queue_depth` waiters wait for a slot for at most
  `media.queue_timeout`; beyond either bound the request gets
  `503 media.worker_busy`.
- Slots are an injected value constructed in `internal/core`, never a package
  global (docs/claude.md §8.5).
- The block-cache warm read of §10 step 10.1 happens **before** a slot is
  taken, so a slow Telegram read never holds a worker slot.

### 15.5 Crash isolation and quarantine

- A worker crash ends every in-flight job with `ErrMediaWorkerUnavailable`.
- Each in-flight job ID at crash time gets a crash count in an in-memory map.
  A job whose count is `1` is retried once, automatically and transparently,
  after the worker is back (within its remaining budget).
- A job ID that reaches a count of `2` is quarantined for `media.poison_ttl`:
  requests for it get `422 media.source_rejected` without contacting the
  worker. Quarantine is logged at `WARN` with bucket, key and version.
- The map holds at most 10000 IDs, LRU.

### 15.6 Contract

```go
package workerclient

type Mode string

const (
	ModeSpawn    Mode = "spawn"
	ModeExternal Mode = "external"
)

type Config struct {
	Mode           Mode
	BinaryPath     string
	SocketPath     string
	TmpDir         string
	FFmpegPath     string
	Concurrency    int
	QueueDepth     int
	QueueTimeout   time.Duration
	MemoryLimit    int64
	Sandbox        string
	PeerUID        int
	HealthInterval time.Duration
	StartTimeout   time.Duration
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	PoisonTTL      time.Duration
}

type State struct {
	Status    string
	Since     time.Time
	Restarts  int
	LastError string
	Health    transformv1.Health
}

var (
	ErrUnavailable = errors.New("workerclient: worker unavailable")
	ErrBusy        = errors.New("workerclient: worker busy")
	ErrTimeout     = errors.New("workerclient: job timed out")
	ErrQuarantined = errors.New("workerclient: source quarantined")
)

type WorkerError struct {
	Code    transformv1.Code
	Message string
}

func (e *WorkerError) Error() string

func New(cfg Config, logger *slog.Logger, clk clock.Clock) (*Supervisor, error)
func (s *Supervisor) Start(ctx context.Context) error
func (s *Supervisor) Close() error
func (s *Supervisor) Transform(ctx context.Context, req transformv1.Request, src io.Reader, out io.Writer) (transformv1.Response, error)
func (s *Supervisor) State() State
func (s *Supervisor) Health() (transformv1.Health, bool)
```

`Health` returns the last successful health response and whether the worker is
currently up. `handlers` reads `PipelineVersion` for the cache key and
`OutputFormats` for AVIF availability from it, so `handlers` depends only on
`api/transformv1`, never on the `workerclient` package.

`State.Status` is `disabled`, `starting`, `ok`, `degraded` (restarting within
backoff) or `down`. The admin API status component `transformd` reports it
([admin-api.md](admin-api.md)). `ErrUnavailable`, `ErrBusy`, `ErrTimeout` and
`ErrQuarantined` wrap `errs.ErrMediaWorkerUnavailable`, `errs.ErrMediaWorkerBusy`,
`errs.ErrMediaTimeout` and `errs.ErrMediaSourceRejected`, and a `*WorkerError`
unwraps to the sentinel of §14.5, so `handlers` branches with `errors.Is` on
`internal/domain/errs` alone.

---

## 16. The worker (`apps/transformd`)

### 16.1 Module and layout

| Path | Holds |
| --- | --- |
| `apps/transformd/go.mod` | `module github.com/PRIEYAN/ButterDB/apps/transformd`; `require github.com/PRIEYAN/ButterDB` with `replace github.com/PRIEYAN/ButterDB => ../..` for non-workspace builds |
| `apps/transformd/main.go` | Flag parsing, startup sequence (§16.3), signal handling |
| `apps/transformd/internal/server` | Socket accept loop, framing via `transformv1`, concurrency gate, per-job budget, self-watchdog |
| `apps/transformd/internal/sniff` | Magic-number classification (§16.2) |
| `apps/transformd/internal/pipeline` | libvips decode, resize, crop, pad, orient, colour, strip, encode |
| `apps/transformd/internal/vipsguard` | cgo shim that blocks every libvips loader except the allowlist |
| `apps/transformd/internal/poster` | ffmpeg argument construction and execution (§17) |
| `apps/transformd/internal/sandbox` | rlimits and Landlock (§18.2) |
| `apps/transformd/testdata/` | Fixtures (§22.1) and `generate.sh`, which rebuilds them |

Third-party packages, added by exact package path in the phase 13 prelude
(E12): `github.com/davidbyttow/govips/v2/vips`,
`github.com/landlock-lsm/go-landlock/landlock`, `golang.org/x/sys/unix`. The
exact versions are pinned in `apps/transformd/go.mod` by the prelude. The
module needs libvips ≥ 8.15 headers (`pkg-config vips`); the root module is
untouched (E14).

The worker imports only `github.com/PRIEYAN/ButterDB/api/transformv1` from the
root module (L8 of docs/claude.md).

### 16.2 Accepted sources

`sniff.Classify(prefix []byte)` decides from the first 64 bytes; the file
extension and the stored `Content-Type` are never trusted. The server runs the
same table (a pure-Go copy in `internal/mediaapi/handlers/sniff.go`) for the
early `415` of §10.

| Class | Magic | Resource | Decoder |
| --- | --- | --- | --- |
| JPEG | `FF D8 FF` | image | libvips `jpegload` |
| PNG | `89 50 4E 47 0D 0A 1A 0A` | image | libvips `pngload` (spng) |
| GIF | `GIF87a`, `GIF89a` | image | libvips `gifload` |
| WebP | `RIFF` …. `WEBP` | image | libvips `webpload` |
| AVIF | `ftyp` at offset 4 with major or compatible brand `avif`, `avis` | image | libvips `heifload` (libheif + dav1d) |
| HEIC/HEIF | `ftyp` brand `heic`, `heix`, `heim`, `heis`, `hevc`, `hevx`, `mif1`, `msf1` (and not `avif`) | image | libvips `heifload` (libheif + libde265) |
| MP4/MOV | `ftyp` brand `isom`, `iso2`, `iso4`, `iso5`, `iso6`, `mp41`, `mp42`, `avc1`, `M4V `, `qt  `, `3gp4`, `3gp5`, `3g2a`, `dash` | video | ffmpeg `mov` demuxer |
| WebM/Matroska | `1A 45 DF A3` | video | ffmpeg `matroska` demuxer |
| AVI | `RIFF` …. `AVI ` | video | ffmpeg `avi` demuxer |

Everything else is `unsupported_source_format`: SVG, PDF, TIFF, BMP, ICO, PSD,
JPEG XL, RAW camera formats, MPEG-TS, playlists (`#EXTM3U`), `ffconcat`
scripts, archives and text. Default (owner may change): TIFF and JPEG XL may be
added later behind the same allowlist.

### 16.3 Startup sequence

1. Parse flags (§15.2). Unknown flags exit with status 2.
2. `sandbox.ApplyLimits`: `RLIMIT_AS` = `--memory-limit`, `RLIMIT_CORE` = 0,
   `RLIMIT_NOFILE` = 256, `RLIMIT_FSIZE` = `max_video_source_bytes + 64 MiB`
   (default 1088 MiB), `RLIMIT_NPROC` unchanged.
3. Set `VIPS_BLOCK_UNTRUSTED=1` if absent, then `vips.Startup` with the
   operation cache disabled (max cache size, memory and files all `0`) so no
   pixel data survives between jobs.
4. `vipsguard.Apply`: `vips_operation_block_set("VipsForeignLoad", TRUE)`,
   then unblock exactly `VipsForeignLoadJpeg`, `VipsForeignLoadPng`,
   `VipsForeignLoadGif`, `VipsForeignLoadWebp` and `VipsForeignLoadHeif`
   (with their `Buffer` and `Source` subclasses). This is the only cgo the
   module writes itself.
5. Probe capabilities: libvips, libheif and `ffmpeg -version` (§17.2 rules);
   build `Health`.
6. Bind the socket with `umask(0177)` (or the `--socket-mode`), after removing
   a stale socket file.
7. `sandbox.ApplyLandlock` (§18.2).
8. Serve. `SIGTERM` stops accepting, waits up to 5 s for in-flight jobs, then
   exits 0.

### 16.4 Per-job handling

1. Read the request header with a 5 s deadline.
2. `OpHealth`: answer at once, even when all slots are busy.
3. `OpTransform`: take a slot or answer `busy`. Validate `Spec` against
   `Limits` (`invalid_spec`).
4. Read the payload. Images are read into memory (`SourceSize` is bounded by
   the server's limit). Videos are written to a file in the tmp dir that is
   unlinked immediately after creation (§17.1).
5. Start the processing budget of `ProcessMillis`. The self-watchdog exits the
   process with status 70 if any job exceeds its budget by 5 s, because a
   running libvips operation cannot be cancelled safely; the supervisor
   restarts it.
6. Sniff, check the class against the resource, run the pipeline, write the
   response.

### 16.5 Image pipeline

| Step | libvips operation |
| --- | --- |
| Header | `new_from_buffer` (lazy); read `width`, `height`, `n-pages`, `page-height`; enforce source limits before any pixel is decoded |
| Frames | `n=1` unless `Animated` and the source is animated (`n=-1`) |
| Resize | `thumbnail_buffer` with shrink-on-load: `fit`/`pad` use `size=both`; `limit`/`thumb` use `size=down`; `fill`/`thumb` with gravity `center` or `auto` use `crop=centre` or `crop=attention` |
| Directional gravity | `thumbnail_buffer` to cover the box without crop, then `extract_area` at the gravity offset |
| Pad | `gravity` (embed) to W × H with the background; alpha added for transparent backgrounds on PNG, WebP or AVIF |
| Orient | `autorot` (implicit in `thumbnail`; explicit `autorot` for the no-resize path) |
| Colour | Convert to sRGB using the embedded profile unless `KeepICC` |
| Flatten | `flatten` onto the background when the output is JPEG and the image has alpha |
| Legacy format | `FormatLegacy` becomes PNG if the final image has alpha, else JPEG |
| Encode | §16.7 |
| Check | Output length ≤ `MaxOutputBytes`, output magic equals the format |

### 16.6 Animated images

With `Animated` and an animated GIF or WebP source: load all pages, check
`n-pages ≤ MaxFrames` and `width × page-height × n-pages ≤ MaxAnimatedPixels`,
resize every page with `thumbnail` (gravity `center` only), keep the `delay`
and `loop` metadata required for playback (not EXIF), and encode with
`webpsave`. An animated AVIF sequence is always rendered as its first frame.

### 16.7 Encoders

| Format | Saver | Options |
| --- | --- | --- |
| JPEG | `jpegsave` | `Q=<q>`, `optimize_coding=true`, `interlace=<Progressive>`, `subsample_mode=auto`, `keep=none` |
| PNG | `pngsave` | `compression=6`, `interlace=false`, `keep=none` |
| WebP | `webpsave` | `Q=<q>` or `lossless=true`, `effort=4`, `smart_subsample=true`, `keep=none` |
| AVIF | `heifsave` | `compression=av1`, `Q=<q>` or `lossless=true`, `effort=2`, `bitdepth=8`, `keep=none` |

`keep=icc` replaces `keep=none` with `KeepICC`. AVIF encoding uses libheif's
libaom encoder plugin; HEIC input uses libheif's libde265 decoder plugin; AVIF
input uses the dav1d plugin. The worker never encodes HEIC.

### 16.8 TDrive reference

TDrive has no server-side transform pipeline to port; the worker is new code.
Three TDrive files inform it and are read, not ported:

| TDrive file | Idea reused |
| --- | --- |
| `backend/media/image_admission.go` | Admission by byte size and header-decoded pixel count before any decode, and animated WebP detection from the `VP8X` flags byte; the same checks run in `internal/sniff` and `internal/pipeline` |
| `backend/thumbnail/orientation.go` | A bounds-checked EXIF `Orientation` parser; a stdlib-only copy lives in the worker's test helpers to assert auto-orient (§22.3) |
| `backend/thumbnail/poster_desktop.go` | Poster extraction through a fixed argument list and `exec.CommandContext`; butterDB replaces mpv with ffmpeg and adds whitelists (§17.2) |

---

## 17. Video posters with ffmpeg

### 17.1 Source handling

The worker creates the source file with `os.CreateTemp(tmpDir, "src-*")`,
unlinks it at once, writes the payload to it, and passes the open descriptor to
ffmpeg as `ExtraFiles[0]` (descriptor 3). The file has no name on disk while
ffmpeg runs, and the kernel frees it when the job ends or the worker dies. For
an encrypted object this means plaintext exists only in an unlinked file; an
owner who wants no plaintext on disk at all points `media.worker_tmp_dir` at a
tmpfs. Default (owner may change).

### 17.2 Argument construction

`poster.Args(seek string) []string` returns a fixed slice. The only
variable element is the seek time, formatted by the worker from
`Spec.PosterMillis` as `<s>.<mmm>` with `strconv`, never copied from the URL.

```go
func Args(seek string) []string {
	return []string{
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-protocol_whitelist", "file",
		"-format_whitelist", "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,avi",
		"-codec_whitelist", "h264,hevc,vp8,vp9,av1,mpeg4,mjpeg,prores",
		"-ss", seek,
		"-i", "/dev/fd/3",
		"-map", "0:v:0",
		"-frames:v", "1",
		"-an", "-sn", "-dn",
		"-threads", "1",
		"-f", "image2pipe", "-c:v", "png",
		"pipe:1",
	}
}
```

| Rule | Reason |
| --- | --- |
| `exec.CommandContext(ctx, ffmpegPath, Args(seek)...)`, absolute path, no shell | No shell parsing, no `PATH` lookup at job time |
| `-protocol_whitelist file` | ffmpeg cannot open network, `concat`, `subfile` or `data` protocols, which blocks SSRF and local-file reads through crafted playlists |
| `-format_whitelist` and `-codec_whitelist` | Only the demuxers and decoders of §16.2 are reachable |
| `-ss` before `-i` | Fast input seek to the nearest keyframe |
| `-threads 1` | Predictable memory and CPU per job |
| Environment | Exactly `PATH=/usr/bin:/bin` |
| Stdout cap | At most `MaxOutputBytes` read; more is `poster_failed` |
| Stderr cap | First 4 KiB kept for the worker log only, never returned to a client |
| Process group | `Setpgid: true`; on budget expiry the whole group gets `SIGKILL` |
| Version probe | At start, `ffmpeg -version` with the same environment; the first line is reported as `Health.FFmpeg` |

No part of a URL, object key, bucket name or stored metadata ever reaches
ffmpeg's argument list or environment.

### 17.3 Poster flow

1. Run ffmpeg with the requested time. An empty stdout with exit status 0 means
   the time is beyond the stream; run once more with seek `0.000`.
2. No frame after the retry, or a non-zero exit: `poster_failed`.
3. The PNG frame enters the image pipeline of §16.5 as a PNG source, with
   source-pixel limits applied, so posters are resized, oriented (rotation side
   data is applied by ffmpeg's default autorotate), stripped and encoded like
   any image.

---

## 18. Security model

### 18.1 Threats and controls

| Threat | Control |
| --- | --- |
| Crafted image or video exploits a decoder | Decoding only in `butterdb-transformd`; loader allowlist (§16.2, `vipsguard`); `VIPS_BLOCK_UNTRUSTED`; Landlock or systemd sandbox; no network; crash restarts; quarantine of repeat crashers |
| Decompression bomb (huge dimensions, many frames) | Header-only dimension checks before decode; source pixel, side, frame and animated-pixel limits; libvips shrink-on-load; `RLIMIT_AS` |
| Large-file exhaustion | Source byte limits from `ObjectMeta` before any read; `RLIMIT_FSIZE`; output byte limit |
| CPU exhaustion | Concurrency slots, queue bounds, per-job budgets, watchdog, ladder snapping, strict signed transformations by default |
| Cache-filling attack | Unsigned requests are preset-only by default; ladder and quality snapping; LRU disk ceiling |
| Command injection into ffmpeg | Fixed argument slice; seek time formatted from an integer; no shell; absolute binary path; clean environment |
| SSRF or local-file read through ffmpeg playlists | `-protocol_whitelist file`, demuxer whitelist without `hls`, `concat` or `image2`; input is descriptor 3 only |
| Worker steals server secrets | Spawn mode: Landlock confines the worker to its tmp dir and the libraries it needs; clean environment; no inherited descriptors. External mode: a separate user, `ProtectSystem=strict`, no read access to the data directory |
| Rogue local process speaks to the worker or impersonates it | Socket mode `0600` in a `0700` directory; `SO_PEERCRED` uid check by the server |
| Signature forgery or downgrade | HMAC-SHA256, 128-bit truncated tag, constant-time compare, versioned string to sign, the key bound into the signed string |
| Replay of a private URL | Mandatory `exp_` with at most `private_max_expiry` on private buckets |
| Metadata leakage (GPS, serial numbers) | Unconditional strip of every derivative (§13) |
| Plaintext of encrypted objects at rest | Memory-only derivative tier; unlinked worker temp files |
| Path confusion | Own path splitter, no `ServeMux` cleaning; one percent-decoding pass; domain key validation |

### 18.2 Worker sandbox

| Mode | `--sandbox` | Behaviour |
| --- | --- | --- |
| Spawn, Linux ≥ 5.13 | `auto` (default) | `PR_SET_NO_NEW_PRIVS`, then a Landlock ruleset at the best ABI the kernel supports: read and execute under `/usr`, `/lib`, `/lib64`; read of `/etc/ld.so.cache`; read and write under the tmp dir; nothing else. ABI v4 and later also deny TCP bind and connect. `Health.Sandbox` is `landlock-v<abi>` |
| Spawn, Landlock unavailable | `auto` | Logs `WARN` and runs with rlimits only. `Health.Sandbox` is `none` |
| Spawn | `require` | Exits with status 78 when Landlock is unavailable |
| Spawn | `off` | rlimits only; for development |
| External | `off` | The systemd unit or container supplies the sandbox (§21); `Health.Sandbox` is `external` |

The admin status component `transformd` carries `Health.Sandbox` in its detail; `none` is reported with status `degraded`.

---

## 19. Server package contracts

Ownership follows the phase 13 lanes of the design brief. Every constructor
validates its dependencies and returns `errs.ErrMissingDependency` for a nil
one (docs/claude.md §6.2).

### 19.1 `internal/mediaapi/grammar` (application, standard library and `internal/domain` only)

```go
package grammar

type Resource string

const (
	ResourceImage Resource = "image"
	ResourceVideo Resource = "video"
)

type Crop string
type Gravity string
type Format string
type Flag string

const (
	FlagStrip       Flag = "strip"
	FlagKeepICC     Flag = "keep_icc"
	FlagProgressive Flag = "progressive"
	FlagLossless    Flag = "lossless"
	FlagAnimated    Flag = "animated"
)

type Aspect struct {
	Num     int64
	Den     int64
	Decimal bool
}

type RGBA struct {
	R, G, B, A uint8
}

type Transformation struct {
	Preset        string
	Width         int
	Height        int
	Aspect        Aspect
	Crop          Crop
	Gravity       Gravity
	DPRCenti      int
	Quality       int
	QualityAuto   bool
	Format        Format
	Background    RGBA
	HasBackground bool
	Flags         []Flag
	PosterMillis  int64
	HasPoster     bool
	ExpiresUnix   int64
}

type Limits struct {
	MaxOutputSide   int
	MaxOutputPixels int64
	MaxParams       int
	MaxBytes        int
}

type AcceptClass string

const (
	AcceptAVIF   AcceptClass = "avif"
	AcceptWebP   AcceptClass = "webp"
	AcceptLegacy AcceptClass = "legacy"
)

type Spec struct {
	Resource      Resource
	Width         int
	Height        int
	Crop          Crop
	Gravity       Gravity
	Format        string
	Quality       int
	Background    RGBA
	HasBackground bool
	KeepICC       bool
	Progressive   bool
	Lossless      bool
	Animated      bool
	PosterMillis  int64
	UsedAuto      bool
}

type Error struct {
	Sentinel error
	Param    string
	Message  string
}

func (e *Error) Error() string
func (e *Error) Unwrap() error

func Parse(segment string, l Limits) (Transformation, error)
func (t Transformation) Canonical() string
func (t Transformation) IsPresetOnly() bool
func (t Transformation) UsesAuto() bool
func Merge(preset, request Transformation) Transformation
func Validate(t Transformation, r Resource, l Limits) error
func ClassifyAccept(accept string) []AcceptClass
func Resolve(t Transformation, r Resource, accepted []AcceptClass, avif AVIFPolicy, ladder []int, l Limits) (Spec, error)
func (s Spec) Key() string

type AVIFPolicy struct {
	Available bool
	MaxPixels int64
}
```

Files: `parse.go`, `canonical.go`, `validate.go`, `resolve.go`, `accept.go`,
`ladder.go`, `errors.go`, each with a `_test.go`, plus `fuzz_test.go`.

### 19.2 `internal/mediaapi/presets` (application)

```go
package presets

type BucketSettings struct {
	Enabled       *bool
	AllowUnsigned bool
	Presets       map[string]string
}

type Config struct {
	Global  map[string]string
	Buckets map[string]BucketSettings
}

type Access struct {
	Enabled       bool
	AllowUnsigned bool
}

func New(cfg Config, l grammar.Limits) (*Store, error)
func (s *Store) Preset(bucket, name string) (grammar.Transformation, bool)
func (s *Store) Access(bucket string) Access
func (s *Store) Replace(cfg Config) error
```

`Replace` builds a new snapshot and swaps it atomically; an invalid
configuration returns an error and keeps the old snapshot. `internal/core`
calls it on `SIGHUP`.

### 19.3 `internal/mediaapi/signing` (application)

```go
package signing

type Input struct {
	Bucket    string
	Resource  grammar.Resource
	Canonical string
	Key       string
}

type Policy struct {
	Private       bool
	AllowUnsigned bool
}

func ParseKeyFile(data []byte) ([]byte, error)
func StringToSign(in Input) []byte
func NewSigner(current, previous []byte, maxPrivateExpiry time.Duration) (*Signer, error)
func (s *Signer) Sign(in Input) string
func (s *Signer) Verify(in Input, segment string) error
func (s *Signer) Authorize(p Policy, t grammar.Transformation, in Input, segment string, now time.Time) error
func (s *Signer) Rotate(current, previous []byte) error
```

`Authorize` implements §7.3 and §7.4 and returns the `ErrMedia*` sentinels.
`NewSigner` copies the keys; `Rotate` replaces them on `SIGHUP`; the old copies
are zeroed (invariant 6 applies to every key copy). `segment` is empty for an
unsigned request.

### 19.4 `internal/mediaapi/derivcache` (application)

```go
package derivcache

type Key struct {
	Bucket    string
	ObjectKey string
	Version   int64
	Resource  string
	Effective string
	Protocol  int
	Pipeline  string
}

func (k Key) Sum() [32]byte
func (k Key) Hex() string
func (k Key) ETag() string

type Entry struct {
	ContentType string    `json:"content_type"`
	Format      string    `json:"format"`
	Size        int64     `json:"size"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	ETag        string    `json:"etag"`
	Created     time.Time `json:"created"`
}

type Config struct {
	Root           string
	MaxBytes       int64
	MemoryMaxBytes int64
	TouchInterval  time.Duration
}

type VersionLookup interface {
	CurrentVersion(ctx context.Context, bucket, key string) (int64, bool, error)
}

type Stats struct {
	DiskBytes   int64
	DiskEntries int
	MemBytes    int64
	MemEntries  int
}

var ErrMiss = errors.New("derivcache: miss")

func Open(cfg Config, clk clock.Clock, logger *slog.Logger) (*Cache, error)
func (c *Cache) Get(k Key) (Entry, io.ReadSeekCloser, error)
func (c *Cache) Put(k Key, e Entry, body io.Reader, persist bool) (Entry, error)
func (c *Cache) PurgeObject(bucket, objectKey string) error
func (c *Cache) Sweep(ctx context.Context, lookup VersionLookup) error
func (c *Cache) Stats() Stats
func (c *Cache) Close() error
```

`persist` is `false` for encrypted sources (memory tier). `Put` returns the
stored entry with `ETag` set from `k.ETag()`.

### 19.5 `internal/mediaapi/handlers` (interface)

```go
package handlers

type BucketInfo struct {
	Name        string
	Private     bool
	CacheMaxAge time.Duration
}

type SourceInfo struct {
	Version      int64
	PlainSize    int64
	Encrypted    bool
	LastModified time.Time
}

type Buckets interface {
	MediaBucket(ctx context.Context, name string) (BucketInfo, error)
}

type Objects interface {
	Stat(ctx context.Context, bucket, key string) (SourceInfo, error)
	Open(ctx context.Context, bucket, key string, version int64) (io.ReadCloser, error)
}

type Transformer interface {
	Transform(ctx context.Context, req transformv1.Request, src io.Reader, out io.Writer) (transformv1.Response, error)
	Health() (transformv1.Health, bool)
}

type Cache interface {
	Get(k derivcache.Key) (derivcache.Entry, io.ReadSeekCloser, error)
	Put(k derivcache.Key, e derivcache.Entry, body io.Reader, persist bool) (derivcache.Entry, error)
	PurgeObject(bucket, objectKey string) error
}

type Presets interface {
	Preset(bucket, name string) (grammar.Transformation, bool)
	Access(bucket string) presets.Access
}

type Authorizer interface {
	Authorize(p signing.Policy, t grammar.Transformation, in signing.Input, segment string, now time.Time) error
}

type Config struct {
	PathPrefix    string
	Hostnames     []string
	Limits        grammar.Limits
	Ladder        []int
	AVIFMaxAuto   int64
	MaxImageSrc   int64
	MaxVideoSrc   int64
	SourceTimeout time.Duration
	ImageTimeout  time.Duration
	PosterTimeout time.Duration
	NegativeTTL   time.Duration
	WorkerLimits  transformv1.Limits
}

type Dependencies struct {
	Buckets     Buckets
	Objects     Objects
	Transformer Transformer
	Cache       Cache
	Presets     Presets
	Authorizer  Authorizer
	Clock       clock.Clock
	Logger      *slog.Logger
}

func New(deps Dependencies, cfg Config) (http.Handler, error)
```

`Objects.Stat` returns `errs.ErrNoSuchBucket` or `errs.ErrNoSuchKey`;
`Objects.Open` returns a reader of exactly `PlainSize` plaintext bytes, through
`readpath/objectreader` and the block cache. `internal/core` adapts
`ObjectMeta` to `SourceInfo` and the bucket configuration to `BucketInfo`; the
handlers never see a `DocumentRef` or a DEK.

Files: `router.go` (path splitting), `serve.go`, `flight.go`, `negative.go`,
`sniff.go`, `errors.go`, `headers.go`, each with tests.

### 19.6 Wiring in `internal/core` (phase 13 integration lane)

1. When `media.enabled`, read or create the signing key, build `presets.Store`,
   `signing.Signer`, `derivcache.Cache`, `workerclient.Supervisor` and the
   handler.
2. Register the supervisor and the cache with the lifecycle group; the
   supervisor starts at step 7 of the startup order and stops at step 2 of
   shutdown ([plan.md](../plan.md) §3, docs/claude.md §7).
3. Bind the media listener at step 8 with `ReadHeaderTimeout 10s`,
   `IdleTimeout 120s`, `MaxHeaderBytes 16 KiB` and no `WriteTimeout` (ranges of
   large derivatives to slow clients).
4. Run `derivcache.Sweep` every `cache_sweep_interval` with a `VersionLookup`
   adapter over `objectrepo`.
5. On `SIGHUP`, re-read `[media]` and call `presets.Store.Replace` and
   `signing.Signer.Rotate`.
6. Report the supervisor `State` as the admin status component `transformd`.

---

## 20. Observability

### 20.1 Metrics (exported on the ops listener)

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

### 20.2 Logs

One `INFO` access line per request with method, bucket, resource, canonical
transformation, status, cache outcome, bytes and duration. The key is logged;
the signature segment is logged as `s--…--`. Worker stderr lines are re-logged
with `component=transformd`.

### 20.3 Readiness

The media listener and the worker never gate `/readyz`. A down worker is
visible through `butterdb_media_worker_up`, the admin status component
`transformd`, and `503` responses on the media listener only.

---

## 21. Deployment (`deploy/transformd/`, phase 13 L6)

### 21.1 Worker image

`deploy/transformd/Dockerfile`, built with the repository root as context:

| Stage | Base | Packages |
| --- | --- | --- |
| build | `golang:1.26-trixie` | `libvips-dev`, `pkg-config` |
| runtime | `debian:trixie-slim` | `libvips42t64`, `libheif1`, `libheif-plugin-libde265`, `libheif-plugin-aomenc`, `libheif-plugin-dav1d`, `ffmpeg` |

- `CGO_ENABLED=1 go -C apps/transformd build -trimpath -o /out/butterdb-transformd .`
- Runs as uid `10002` (`butterdb-transformd`), group `10001` (`butterdb`),
  read-only root filesystem, a tmpfs at `/tmp/transformd`.
- Package versions are pinned in the Dockerfile by the L6 lane after checking
  them with `apt-cache policy` in the build stage.
- HEVC decoding (libde265) and AV1 encoding (libaom) are distributed as Debian
  packages; patent licensing for HEVC in the deployer's jurisdiction is the
  deployer's responsibility, noted in docs/guide.

### 21.2 systemd (external mode)

`deploy/transformd/butterdb-transformd.service`:

| Directive | Value |
| --- | --- |
| `ExecStart` | `/usr/bin/butterdb-transformd --socket /run/butterdb-transformd/transformd.sock --socket-mode 0660 --tmp-dir /var/tmp/butterdb-transformd --ffmpeg /usr/bin/ffmpeg --concurrency 4 --memory-limit 0 --sandbox off` |
| `User`, `Group` | `butterdb-transformd`, `butterdb` |
| `RuntimeDirectory` | `butterdb-transformd` (mode `0750`) |
| `Restart`, `RestartSec` | `always`, `1s` |
| `MemoryMax`, `TasksMax`, `CPUQuota` | `1G`, `64`, `200%` |
| Hardening | `PrivateNetwork=yes`, `PrivateTmp=yes`, `ProtectSystem=strict`, `ProtectHome=yes`, `NoNewPrivileges=yes`, `ReadWritePaths=/var/tmp/butterdb-transformd`, `InaccessiblePaths=/var/lib/butterdb /etc/butterdb`, `SystemCallFilter=@system-service`, `SystemCallArchitectures=native`, `RestrictAddressFamilies=AF_UNIX`, `LockPersonality=yes`, `MemoryDenyWriteExecute=yes`, `LimitCORE=0` |

`--memory-limit 0` disables the worker's own `RLIMIT_AS`; the cgroup's
`MemoryMax` bounds it instead. `butterdbd` then runs with
`media.worker_mode = "external"`,
`media.worker_socket = "/run/butterdb-transformd/transformd.sock"`, and is a
member of group `butterdb`.

### 21.3 Compose

`deploy/compose.yaml` (phase 11) gains a `transformd` service using the worker
image with `network_mode: none`, `read_only: true`, a tmpfs, and a named volume
for the socket directory shared with `butterdbd`, which runs in external mode.
The `butterdbd` distroless image is unchanged and contains no libvips.

---

## 22. Test plan

### 22.1 Fixtures

Fixtures live in `apps/transformd/testdata/` and are regenerated by
`apps/transformd/testdata/generate.sh` from a synthetic 64 × 48 gradient with a
red marker in the top-left corner, using `vips`, `heif-enc`, `exiftool` and
`ffmpeg`. Generated files are committed so tests do not need those tools. Root
module tests needing a source use small byte fixtures under
`test/acceptance/phase13/testdata/` (JPEG, PNG and the bombs only).

| Fixture | Content | Expected |
| --- | --- | --- |
| `jpeg_orient6_gps.jpg` | 64 × 48 stored, EXIF `Orientation=6`, GPS, `Make`, `Model`, XMP | Output 48 × 64, marker top-left after rotation, no EXIF/XMP |
| `jpeg_cmyk.jpg` | CMYK JPEG with an ICC profile | sRGB output, no ICC (or ICC kept with `fl_keep_icc`) |
| `jpeg_progressive.jpg` | Progressive JPEG | Decodes; `fl_progressive` output is progressive |
| `png_rgba.png` | RGBA with partial transparency, `tEXt` chunk | `f_jpg` flattens onto white or `b_`; `f_auto` legacy picks PNG |
| `png_16bit.png` | 16-bit RGB | 8-bit output |
| `png_palette.png` | Palette with `tRNS` | Alpha preserved for PNG and WebP |
| `gif_static.gif` | One frame | PNG by default |
| `gif_animated_3f.gif` | 3 frames | First frame by default; animated WebP with `fl_animated` |
| `gif_animated_300f.gif` | 300 frames, 16 × 16 | `fl_animated`: `too_many_frames`; default: first frame |
| `webp_lossy.webp` | Lossy WebP with EXIF chunk | No `EXIF` chunk in output |
| `webp_lossless_alpha.webp` | Lossless with alpha | `fl_lossless` round trip |
| `webp_animated.webp` | 3 frames | As the animated GIF |
| `avif_8bit.avif` | 8-bit AVIF | Decodes through dav1d |
| `avif_10bit_alpha.avif` | 10-bit with alpha | 8-bit output with alpha |
| `heic_orient_gps.heic` | HEIC with `irot` and EXIF GPS | Rotated output, no metadata, JPEG by default |
| `mp4_h264_5s.mp4` | 5 s H.264, `moov` first, frame number burned in | `t_2` gives frame at 2 s; `t_9` falls back to frame 0 |
| `mov_moov_end.mov` | `moov` at the end | Poster works (seekable file) |
| `mp4_rotate90.mp4` | Rotation side data 90° | Poster is rotated |
| `webm_vp9.webm` | VP9 | Poster works |
| `mkv_hevc.mkv` | HEVC in Matroska | Poster works |
| `bomb_png_dims.png` | Valid header claiming 100000 × 100000, tiny body | `source_too_many_pixels` before decode |
| `bomb_jpeg_dims.jpg` | SOF claiming 65000 × 65000 | `source_too_many_pixels` |
| `bomb_gif_frames.gif` | 10000 frames of 1024 × 1024 claimed | `too_many_frames` with `fl_animated` |
| `truncated.jpg` | First 40 % of a JPEG | `source_undecodable` |
| `svg_script.svg` | SVG with a script element | `unsupported_source_format` |
| `pdf_min.pdf` | Minimal PDF | `unsupported_source_format` |
| `tiff_min.tif` | Minimal TIFF | `unsupported_source_format` |
| `hls_ssrf.mp4` | `#EXTM3U` playlist pointing at `http://169.254.169.254/` with an `.mp4` name | `unsupported_source_format`; no network attempt |
| `ffconcat_file.mp4` | `ffconcat version 1.0` listing `/etc/passwd` | `unsupported_source_format` |
| `polyglot_jpeg_zip.jpg` | JPEG followed by a ZIP | Decodes as JPEG; output carries no trailing data |
| `empty.bin` | 0 bytes | `unsupported_source_format` |

### 22.2 Root module unit tests (CGO_ENABLED=0)

| Package | Tests |
| --- | --- |
| `api/transformv1` | `TestFrameRoundTrip`, `TestFrameBadMagic`, `TestFrameHeaderTooLarge`, `TestFrameZeroHeader`, `TestFrameUnknownField`, `TestFrameVersionMismatch`, `TestFrameShortPayload`, `TestSpecValidate`, `FuzzReadRequest` |
| `grammar` | `TestParseValid` (every row of §4.1), `TestParseInvalid` (bad charset, leading zeros, duplicate key, unknown key, empty segment, 17 params, 513 bytes), `TestCanonicalExamples` (every row of §6.1), `TestCanonicalIdempotent`, `TestPresetVersusPosterT`, `TestValidateConflicts` (every row of §4.10), `TestMergePresetOverride`, `TestClassifyAccept` (Chrome, Safari 16, Firefox, `*/*`, `q=0`), `TestResolveAutoFormat`, `TestResolveDPRClamp`, `TestResolveAspect`, `TestLadderSnap` (exact rung, between rungs, above max, height only, ratio kept), `TestQualitySnap`, `TestSpecKeyStable`, `FuzzParse` (no panic; `Parse(Canonical(x))` equals `x`) |
| `presets` | `TestBucketOverridesGlobal`, `TestRejectNestedPreset`, `TestRejectExpInPreset`, `TestPresetNameRules`, `TestTooManyPresets`, `TestReplaceKeepsOldOnError`, `TestAccessDefaults` |
| `signing` | `TestSignVectors` (§7.2 table), `TestVerifyPreviousKey`, `TestVerifyRejectsTampered` (bucket, resource, canonical, key, one bit of the tag), `TestAuthorizeMatrix` (every row of §7.3), `TestExpiry` (expired, at the second, private missing, private too far, public unbounded), `TestParseKeyFile` (43 chars, padding, wrong length, whitespace), `TestRotateZeroesOldKeys` |
| `derivcache` | `TestKeyIncludesVersion`, `TestKeyIncludesPipeline`, `TestPutGetDisk`, `TestPutGetMemoryForEncrypted` (asserts no file written), `TestTornFileIsMiss`, `TestEvictionLRU`, `TestTouchThrottled`, `TestLazySiblingPurge`, `TestSweepRemovesStaleAndDeleted`, `TestPurgeObject`, `TestOpenClearsTmp` |
| `workerclient` | Using a fake worker started from the test binary (`TestMain` helper-process pattern) that speaks `transformv1` and can be scripted to crash, hang, answer busy or answer slowly: `TestSpawnArgsAndEnv`, `TestHealthHandshake`, `TestRestartBackoffSequence` (fake clock), `TestBackoffResetsAfterHealthyUptime`, `TestCrashRetriesOnce`, `TestQuarantineAfterTwoCrashes`, `TestQueueDepthAndTimeout`, `TestWatchdogKillsGroup`, `TestExternalModeNeverSpawns`, `TestPeerCredMismatch`, `TestFailFastWhileDown` |
| `handlers` | With fakes for every port: `TestPathSplitting` (prefix, `//` in key, percent-encoded key, signature segment detection, bad bucket), `TestNoServeMuxCleaning`, `TestVerifyBeforeWork` (fakes assert no `Stat`, cache or worker call on any `403`), `TestUnsignedPresetOnly`, `TestPrivateRequiresExp`, `TestErrorMapping` (every row of §12.4), `TestCacheControlMatrix` (§12.2), `TestVaryAccept`, `TestConditional304`, `TestRange206`, `TestHeadFillsCache`, `TestSingleFlight` (50 concurrent requests, one worker call), `TestFlightSurvivesClientCancel`, `TestNegativeCache`, `TestEarlySniff415`, `TestSourceTooLarge`, `TestEncryptedUsesMemoryTier` |

### 22.3 Worker tests (`go -C apps/transformd test ./...`, cgo, libvips installed)

| Package | Tests |
| --- | --- |
| `internal/sniff` | `TestClassifyFixtures` (every fixture to its class), `TestClassifyShortInput`, `FuzzClassify` |
| `internal/pipeline` | `TestCropModes` (each mode × source smaller and larger than the box, asserting exact output sizes), `TestGravityOffsets` (marker position for each directional gravity), `TestSmartCrop`, `TestPadBackground`, `TestFlattenJPEG`, `TestLegacyPicksPNGForAlpha`, `TestEncodersMagic` (each format), `TestQualityChangesSize`, `TestLossless`, `TestProgressive`, `TestAutoOrientJPEG`, `TestAutoOrientHEIC`, `TestStripAllFormats` (§13 step 4 parsers over every fixture × output format), `TestKeepICC`, `TestAnimatedWebPOutput`, `TestAnimatedFirstFrameDefault`, `TestSourcePixelLimitBeforeDecode` (bomb fixtures finish in under 100 ms and under 32 MiB RSS growth), `TestOutputByteLimit`, `TestDeterministicOutput` (same input and spec give identical bytes) |
| `internal/vipsguard` | `TestBlockedLoaders` (SVG, PDF, TIFF and `magickload` fail even when handed bytes directly) |
| `internal/poster` | `TestArgsExact` (slice equality with §17.2), `TestSeekFormatting` (`0` → `0.000`, `2500` → `2.500`), `TestPosterAtTime`, `TestPosterFallbackToZero`, `TestPosterRotation`, `TestPosterTimeoutKillsGroup` (a fake ffmpeg script that sleeps), `TestNoNetworkProtocols` (playlist fixtures), `TestEnvIsClean` |
| `internal/sandbox` | `TestRlimitsApplied`, `TestLandlockDeniesDataDir` (skipped when the kernel lacks Landlock), `TestRequireExitsWithoutLandlock` |
| `internal/server` | `TestBusyBeyondConcurrency`, `TestHealthWhileBusy`, `TestBudgetWatchdogExits`, `TestSocketMode0600`, `TestGracefulShutdown` |

### 22.4 Acceptance (`test/acceptance/phase13`)

Runs in the root module with `CGO_ENABLED=0` against a `faketg`-backed server
and a pure-Go fake worker (`test/acceptance/phase13/fakeworker`), plus the same
scenarios against the real worker when `BUTTERDB_TEST_TRANSFORMD` names a built
`butterdb-transformd` binary.

| Test | Asserts |
| --- | --- |
| `TestOriginalsByteForByte` | Upload a JPEG with GPS through the S3 API; request a transform; `GetObject` returns bytes whose SHA-256 equals the upload's |
| `TestTransformsNeverWrite` | Across 100 media requests, `faketg` records zero sends and zero deletes; `projection.db` and `control.db` are unchanged |
| `TestOverwriteInvalidatesDerivative` | Overwrite the key through S3; the next media response has a new `ETag` and reflects the new object |
| `TestDeleteGives404` | Delete through S3; media request returns `404 object.not_found`; the sweep removes the directory |
| `TestEncryptedObjectTransform` | TDE1 object transforms correctly; nothing is written under `cache/derived/` |
| `TestPublicPresetUnsigned` | `t_thumb` works unsigned on a public-read bucket; `w_100` unsigned is `403 media.unsigned_not_allowed` |
| `TestPrivateSignedWithExpiry` | Signed private URL works; expired is `403`; missing `exp_` is `403` |
| `TestFAutoNegotiation` | AVIF, WebP and legacy `Accept` headers produce three entries and `Vary: Accept` |
| `TestWorkerCrashRecovery` | Kill the worker; the next request is `503`; within backoff the worker is back and requests succeed; the S3 API answers throughout |
| `TestPoisonQuarantine` | A fixture scripted to crash the fake worker is quarantined after two crashes |
| `TestMediaDisabledBucket` | `enabled = false` gives `403 media.disabled` |
| `TestReadyzIndependentOfWorker` | With no worker binary, `/readyz` is `200` and media answers `503` |

### 22.5 Verification commands

```
GOWORK=off CGO_ENABLED=0 go test ./api/transformv1/... ./internal/mediaapi/... ./test/acceptance/phase13/...
CGO_ENABLED=1 go -C apps/transformd test ./...
CGO_ENABLED=1 go -C apps/transformd test -run 'Fuzz' -fuzz FuzzClassify -fuzztime 60s ./internal/sniff
GOWORK=off go test -fuzz FuzzParse -fuzztime 60s ./internal/mediaapi/grammar
docker build -f deploy/transformd/Dockerfile .
scripts/layering.sh
scripts/nocomments.sh
```

---

## 23. Phase 13 ownership map

| Section | Owner |
| --- | --- |
| §14 `api/transformv1`, `apps/transformd/go.mod`, `go.work` update, `ErrMedia*` sentinels, `Media*` limits, `config.Media` | Prelude |
| §16, §17, §18.2, §22.1, §22.3 | L1 `apps/transformd` |
| §3 to §8, §19.1 to §19.3 | L2 `internal/mediaapi/{grammar,presets,signing}` |
| §11, §19.4 | L3 `internal/mediaapi/derivcache` |
| §15, §19 `workerclient` | L4 `internal/mediaapi/workerclient` |
| §10, §12, §19.5 | L5 `internal/mediaapi/handlers` |
| §21 | L6 `deploy/transformd/` |
| §19.6, §20, §22.4 | Integration lane |

---

## 24. Defaults the owner may change

| Default | Value |
| --- | --- |
| Media disabled until configured | `media.enabled = false` |
| Listener | `0.0.0.0:9080`, no prefix |
| Strict transformations | Unsigned requests are preset-only on every bucket unless `allow_unsigned` |
| Private expiry ceiling | 7 days |
| One deployment-wide signing key, not exposed by the admin API | §7.1 |
| Presets in TOML, reloaded on `SIGHUP`, not in the registry channel | §2.2 |
| Width ladder | §8 |
| Quality snapping to multiples of 5 | §4.4 |
| Encrypted derivatives memory-only | §11.1 |
| Worker spawned by `butterdbd` | `media.worker_mode = "spawn"` |
| Limits | §9 |
| Accepted source formats | §16.2 (no TIFF, SVG, PDF, JPEG XL) |
| Poster default time and fallback | `t_1`, fallback to `0` |
| Query string ignored | §3.2 |
