# TDX1 wire format for butterDB

TDX1 is the text format of every butterDB operation stored in Telegram: the
caption of a body document and the text of every control message. It is a
compatibility surface. A byte changed here can make every deployment's history
unreadable, so the grammar is frozen, the op catalogue is versioned, and every
op has a round-trip test.

The grammar is the legacy grammar byte for byte (legacy section 8,
[../butterDBCore.md](../butterDBCore.md); TDrive `backend/projection/wire.go`).
The op catalogue is new. Where ops are applied and why is in
[storage-model.md](storage-model.md).

Package: `internal/wire`, standard library only (E13). Owner: phase 02 lanes L1
(grammar, parse, format, errors) and L2 (op catalogue, round-trip tests).

---

## 1. Grammar

```
message text (or document caption) :=
    <header-line> [ "\n" <remainder> ]

header-line := "TDX1" ( "|" key "=" value )+
```

| Rule | Detail |
| --- | --- |
| Version prefix | Literal `TDX1`. Anything else: `ErrWireBadVersion` |
| Header extent | First line only. Cut at the first `\n`, then `strings.TrimSpace` |
| Empty header | `ErrWireMissingHeader` |
| Separator | `\|`. Fewer than two `\|`-separated parts: `ErrWireMalformed` |
| Pair | `key=value`, split at the first `=`. No `=`, or `=` at index 0: `ErrWireMalformed`. Values may be empty |
| Repeated key | The last occurrence wins (map assignment, as in TDrive). `Format` never emits a key twice |
| Required key | `t`. Missing: `ErrWireMalformed` |
| Text encoding | `url.QueryEscape` to format, `url.QueryUnescape` to parse |
| Binary encoding | `base64.RawURLEncoding` (no padding). Decoding to zero bytes is `ErrWireMalformed` |
| Unknown keys | Ignored. Parsing branches only on `t`, so new keys are additive |
| Timestamps | Unix seconds, `0 ≤ ts ≤ 253402300799` (SQLite's bound) |
| Remainder | Ignored by the parser. butterDB writes no remainder on any op |

Because every value is either `QueryEscape` output, base64url, a decimal
integer, or an identifier from a restricted ASCII set, a formatted header is
pure ASCII with no `\n`, no `|` inside values, and no leading or trailing
whitespace. Telegram's whitespace trimming and Unicode normalisation therefore
cannot alter it (plan.md risk R3). Its length in characters equals its length
in bytes.

---

## 2. Go contract

Committed by the phase 02 prelude. Field names are binding; the key each field
maps to is in section 4.

```go
package wire

type OpType string

const (
	OpBody         OpType = "body"
	OpPut          OpType = "put"
	OpDel          OpType = "del"
	OpRegistry     OpType = "reg"
	OpBucketCreate OpType = "bcreate"
	OpBucketConfig OpType = "bcfg"
	OpBucketDelete OpType = "bdel"
	OpEncConfig    OpType = "encfg"
)

const (
	Prefix              = "TDX1"
	FormatVersion       = 1
	MaxUnixTimestamp    = int64(253402300799)
	MaxTextChars        = 4096
	MaxCaptionChars     = 1024
	MaxMsgID            = int64(2147483647)
	MaxKeyBytes         = 1024
	MaxEncodedKeyChars  = 3 * MaxKeyBytes
	MaxContentTypeBytes = 1024
	MaxUserMetaBytes    = 2048
	MaxSysHeaderBytes   = 1024
	MaxPutFixedChars    = 512
	MaxStoredBytes      = int64(1900 * 1024 * 1024)
	MaxCacheMaxAge      = int64(31536000)
	DefaultContentType  = "application/octet-stream"
	EmptyMD5            = "d41d8cd98f00b204e9800998ecf8427e"
	EmptySHA256         = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var (
	ErrWireMissingHeader = errors.New("wire: header missing")
	ErrWireBadVersion    = errors.New("wire: unknown version")
	ErrWireMalformed     = errors.New("wire: malformed header")
	ErrWireUnknownOp     = errors.New("wire: unknown op type")
	ErrWireTooLong       = errors.New("wire: encoded op exceeds the telegram length limit")
)

type Op struct {
	Type              OpType
	JobID             string
	Key               string
	BodyMsgID         int64
	StoredSize        int64
	PlaintextSize     int64
	Encrypted         bool
	EncryptionVersion int
	KeyID             string
	ETag              string
	SHA256            string
	ContentType       string
	UserMeta          map[string]string
	SystemMeta        map[string]string
	Writer            string
	Timestamp         int64
	PutVersion        int64
	Bucket            string
	BucketChannelID   int64
	Policy            string
	DefaultEncryption string
	CacheMaxAge       int64
	WrappedKey        []byte
	KeyCheck          []byte
	KEKFingerprint    string
	ConfigVersion     int
	DeploymentID      string
	FormatVersion     int
	MinReaderVersion  int
	PrimaryUserID     int64
}

func ExtractHeaderLine(raw string) string
func Parse(raw string) (Op, error)
func Format(op Op) (string, error)
func Known(t OpType) bool
func OnDocument(t OpType) bool
func PutRecordLen(key, contentType string, userMeta, systemMeta map[string]string, writer string) (int, error)
func FitsPut(key, contentType string, userMeta, systemMeta map[string]string, writer string) error
func KeyOf(op Op) (string, bool)
func HasPrefix(raw string) bool
```

| Function | Contract |
| --- | --- |
| `ExtractHeaderLine` | The text before the first `\n`, untrimmed. The projector hashes `strings.TrimSpace` of it as `raw_header` |
| `Parse` | Section 8. For an unknown `t` it returns `Op{Type: OpType(t)}` and `ErrWireUnknownOp` |
| `Format` | Emits keys in the canonical order of section 5, validates every field with the setter rules of section 9, and returns `ErrWireTooLong` when the result exceeds the op's carrier limit (section 6). Never returns text that `Parse` rejects |
| `Known` | True for the eight types above |
| `OnDocument` | True only for `OpBody`. Every other op is a text message |
| `PutRecordLen`, `FitsPut` | Section 6.3 |
| `HasPrefix` | `strings.HasPrefix(strings.TrimSpace(ExtractHeaderLine(raw)), "TDX1")`. The projector's test for "this message claims to be butterDB's" (section 7.3) |
| `KeyOf` | The object key of a `put` or `del`, and `true`; `("", false)` for every other type. The projector stores it in `replay_log.object_key` for per-key repair (schema.md §4.2) |

The constants duplicate values in `internal/domain/limits` because `wire` may
import only the standard library. `test/acceptance/phase02` asserts that each
pair is equal.

---

## 3. Op catalogue overview

| `t` | Carrier | Channel | Commits | Section |
| --- | --- | --- | --- | --- |
| `body` | Document caption | Bucket | Nothing. A hidden body awaiting a put | 5.1 |
| `put` | Text | Bucket | One version of one key | 5.2 |
| `del` | Text | Bucket | Tombstone for one key | 5.3 |
| `reg` | Text | Registry | Deployment marker | 5.4 |
| `bcreate` | Text | Registry | Bucket creation | 5.5 |
| `bcfg` | Text | Registry | Full bucket configuration | 5.6 |
| `bdel` | Text | Registry | Bucket deletion | 5.7 |
| `encfg` | Text | Registry | One wrapped bucket key | 5.8 |

An op on the wrong carrier or in the wrong channel parses but is rejected by
the projector (`wrong_carrier`, `wrong_channel`; storage-model.md section 10).

---

## 4. Field vocabulary

| Key | `Op` field | Kind | Rule | Used by |
| --- | --- | --- | --- | --- |
| `t` | `Type` | identifier | Required on every op | all |
| `j` | `JobID` | identifier | Canonical lowercase UUIDv4: `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$` | body, put, del, bcreate, bcfg, bdel, encfg |
| `k` | `Key` | escaped text | After unescape: valid UTF-8, 1–1024 bytes, no U+0000 | put, del |
| `bm` | `BodyMsgID` | integer | `0 ≤ bm ≤ 2147483647` | put |
| `sz` | `StoredSize` | integer (shared) | `≥ 0` at grammar level; per-op limits in section 5 | body, put |
| `psz` | `PlaintextSize` | integer (shared) | `≥ 0` | put |
| `enc` | `Encrypted` | flag (shared) | `1` sets `Encrypted` and `EncryptionVersion = 1`; any other value is ignored | body, put |
| `ev` | `EncryptionVersion` | integer (shared) | Overrides the version. Format emits it only when not 1 | body, put |
| `kid` | `KeyID` | identifier | `^[0-9a-f]{16}$` | put, encfg |
| `et` | `ETag` | identifier | `^[0-9a-f]{32}$` or `^[0-9a-f]{32}-[1-9][0-9]{0,4}$` with suffix ≤ 10000. Unquoted | put |
| `h` | `SHA256` | identifier | `^[0-9a-f]{64}$`, SHA-256 of the plaintext | put |
| `ct` | `ContentType` | escaped text | After unescape: 1–1024 bytes of `0x20`–`0x7E`. Absent means `application/octet-stream` | put |
| `m.<name>` | `UserMeta[name]` | prefixed escaped text | One key per entry. Section 5.2.1 | put |
| `s.<name>` | `SystemMeta[name]` | prefixed escaped text | One key per entry. Section 5.2.2 | put |
| `w` | `Writer` | identifier | `^(key\|dev\|sys)\.[A-Za-z0-9_-]{1,32}$` | put, del (required); registry ops (optional) |
| `ts` | `Timestamp` | integer (shared) | `0 ≤ ts ≤ 253402300799`. Writer's clock at format time | all but body |
| `pv` | `PutVersion` | integer | `1 ≤ pv ≤ 2147483647` | del |
| `b` | `Bucket` | identifier | `^[a-z0-9.-]{3,63}$`. Full S3 naming rules are checked by the applier | bcreate, bcfg, bdel |
| `bc` | `BucketChannelID` | integer | `> 0` | bcreate, bcfg, bdel, encfg |
| `pol` | `Policy` | identifier | `private` \| `public-read` | bcreate, bcfg |
| `de` | `DefaultEncryption` | identifier | `none` \| `tde1` | bcreate, bcfg |
| `cma` | `CacheMaxAge` | integer | `0 ≤ cma ≤ 31536000` seconds | bcreate, bcfg |
| `wrap` | `WrappedKey` | base64url | Exactly 72 bytes decoded | encfg |
| `check` | `KeyCheck` | base64url | Exactly 61 bytes decoded | encfg |
| `kfp` | `KEKFingerprint` | identifier | `^bkf1_[0-9a-f]{32}$`, the server key fingerprint in the form operations.md §5.2 defines | encfg, reg |
| `cv` | `ConfigVersion` | integer | `≥ 1`; absent means 1; Format emits it only when > 1 | encfg |
| `dep` | `DeploymentID` | identifier | `^[a-z2-7]{26}$` | reg |
| `fv` | `FormatVersion` | integer | `≥ 1` | reg |
| `mr` | `MinReaderVersion` | integer | `1 ≤ mr ≤ fv` | reg |
| `uid` | `PrimaryUserID` | integer | `> 0` | reg |

Kinds:

| Kind | Format | Parse |
| --- | --- | --- |
| identifier | Written raw. Every identifier charset is a subset of the characters `QueryEscape` leaves unchanged, so raw equals escaped | Validated against its pattern on the raw value |
| escaped text | `url.QueryEscape` | `url.QueryUnescape`, then validated |
| prefixed escaped text | Key `<prefix>.` + `url.QueryEscape(name)`, value `url.QueryEscape(value)` | Every pair whose key starts with the prefix and `.`; name and value unescaped, then validated |
| integer | `strconv.FormatInt(v, 10)` | `strconv.ParseInt(s, 10, 64)` (a leading `+` or leading zeros parse, as in TDrive; the raw header hash still reflects the received bytes) |
| base64url | `base64.RawURLEncoding` | Must decode and be non-empty |
| flag | `enc=1` | As above |

The shared keys `sz`, `ts`, `enc`, `ev`, `psz` are parsed for **every** op type,
exactly as legacy section 8 does, and then checked per op. A shared key
present on an op that does not use it is ignored, except where section 5 says
otherwise.

---

## 5. Op definitions

Each table lists keys in canonical `Format` order. "Req" is required, "Opt"
optional, "Cond" required under the stated condition.

### 5.1 `t=body`

Caption of the body document. The document is sent `force_file`,
`application/octet-stream`, file name `<j>.bin` (storage-model.md section 2.2).

| Key | Req | Rule |
| --- | --- | --- |
| `j` | Req | Job ID; equals the `j` of the put that will reference this body |
| `sz` | Req | `1 ≤ sz ≤ 1992294400`. Equals the document size Telegram reports; the read path refuses a mismatch |
| `enc` | Opt | `1` when the body is a TDE1 stream |
| `ev` | Cond | Only with `enc=1`; omitted for version 1 |

`ev` without `enc=1`: `ErrWireMalformed`. The TDE1 file salt and nonce prefix
are not on the wire; they are in the body's own 50-byte TDE1 header. The key ID
is on the put, not the body.

### 5.2 `t=put`

| Key | Req | Rule |
| --- | --- | --- |
| `j` | Req | Job ID |
| `k` | Req | Object key, escaped |
| `bm` | Req | Body message ID. `0` only for a body-less empty object |
| `sz` | Req | Stored size, `0 ≤ sz ≤ 1992294400` |
| `psz` | Cond | Required iff `enc=1`; plaintext size. Forbidden without `enc=1` |
| `enc` | Opt | `1` when the body is TDE1 |
| `ev` | Cond | Only with `enc=1`; omitted for version 1 |
| `kid` | Cond | Required iff `enc=1`. Forbidden without it |
| `et` | Req | S3 ETag without quotes |
| `h` | Req | SHA-256 of the plaintext |
| `ct` | Opt | Omitted when `application/octet-stream` |
| `m.<name>` | Opt | One per user metadata entry, sorted by name in byte order; none when empty |
| `s.<name>` | Opt | One per stored system header, sorted by name in byte order; none when empty |
| `w` | Req | Writer |
| `ts` | Req | Format time |

Cross-field rules, all `ErrWireMalformed` on failure:

| Rule | Detail |
| --- | --- |
| Empty object | `bm = 0` requires `sz = 0`, no `enc`, `et = d41d8cd98f00b204e9800998ecf8427e`, `h = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| Non-empty body | `bm > 0` requires `sz > 0` |
| Plaintext size | Without `enc=1` the plaintext size is `sz`; `Parse` sets `PlaintextSize = StoredSize` |
| Content type | `Parse` sets `ContentType = "application/octet-stream"` when `ct` is absent; `Format` refuses an empty `ContentType` and omits the default |

Checks that need other packages are applier rejects, not parse errors:
`sz = CiphertextSize(psz)` for encrypted puts (`size_mismatch`, using
`internal/crypto/format`), and `bm` lower than the put's own message ID
(`bad_body_ref`).

ETag values: single PUT, `CopyObject` and admin upload: lowercase hex MD5 of
the plaintext. `CompleteMultipartUpload`: lowercase hex MD5 of the
concatenated 16-byte binary MD5s of the parts, then `-` and the part count.

#### 5.2.1 User metadata `m.<name>`

Each entry is its own pair: `|m.` + `url.QueryEscape(name)` + `=` +
`url.QueryEscape(value)`. The fixed cost of an entry is therefore exactly four
characters (`|`, `m`, `.`, `=`), and a name or value made of
`A-Z a-z 0-9 - _ . ~` costs one character per byte. This is the encoding that
s3-api.md §8.7's put-record guarantee assumes: a fixed part of at most 512
characters and at most four characters of overhead per metadata entry. A
base64url JSON blob would cost about 4/3 per byte plus six per entry and
break that guarantee.

| Rule | Detail |
| --- | --- |
| Names | Without the `x-amz-meta-` prefix; lowercase; an HTTP token: `^[a-z0-9!#$%&'*+.^_\x60\|~-]+$`, 1 byte or more |
| Values | Valid UTF-8 after unescape (S3 decodes RFC 2047 before storing); no byte below `0x20` except `0x09`, no `0x7F`. May be empty |
| Size | Sum of `len(name) + len(value)` over all entries ≤ 2048 bytes (`MaxUserMetaBytes`, S3's limit) |
| Order | `Format` emits entries sorted by name in byte order, after `ct` and before the `s.` entries |
| Duplicates | Two raw keys that unescape to the same name (`m.a` and `m.%61`) are `ErrWireMalformed` |
| Empty | No `m.` pair; `Parse` returns a nil map |

#### 5.2.2 System headers `s.<name>`

Same pair encoding with prefix `s.`. Names are limited to `cache-control`,
`content-disposition`, `content-encoding`, `content-language`, `expires`: the
S3 system headers a PUT may set and GET returns (s3-api.md §8.1). Each value is
1–1024 bytes (`MaxSysHeaderBytes`, s3-api.md §8.1) with the value rules above.
There is no separate total; the 4096-character message bounds the sum. An
`s.` name outside the list is `ErrWireMalformed`.

### 5.3 `t=del`

| Key | Req | Rule |
| --- | --- | --- |
| `j` | Req | Job ID (the op ID recorded in `projection_operations`) |
| `k` | Req | Object key, escaped |
| `pv` | Opt | The version the writer meant to delete. Informational and used by the sweeper's version guard; the applier removes any version below the tombstone's own message ID |
| `w` | Req | Writer |
| `ts` | Req | Format time |

### 5.4 `t=reg`

| Key | Req | Rule |
| --- | --- | --- |
| `dep` | Req | Deployment ID |
| `fv` | Req | Format version written by the deployment |
| `mr` | Req | Minimum reader format version, `1 ≤ mr ≤ fv` |
| `kfp` | Req | Server key fingerprint (`bkf1_` + 32 hex) |
| `uid` | Req | Primary Telegram user ID |
| `w` | Opt | Writer: `sys.setup` (first marker), `sys.upgrade` (format version raise), `sys.rekey` (server key rotation or lost-key replacement, security.md §5.6 and §5.7) |
| `ts` | Req | Format time |

### 5.5 `t=bcreate`

| Key | Req | Rule |
| --- | --- | --- |
| `j` | Req | Job ID |
| `b` | Req | Bucket name |
| `bc` | Req | Bucket channel ID |
| `pol` | Req | `private` or `public-read` |
| `de` | Req | `none` or `tde1` |
| `cma` | Req | Cache max-age seconds |
| `w` | Opt | Writer |
| `ts` | Req | Format time |

### 5.6 `t=bcfg`

Full configuration, never a delta. Same keys and order as `t=bcreate`. `b` must
equal the live bucket's name (`bucket_not_found` otherwise).

### 5.7 `t=bdel`

| Key | Req | Rule |
| --- | --- | --- |
| `j` | Req | Job ID |
| `b` | Req | Bucket name |
| `bc` | Req | Bucket channel ID |
| `w` | Opt | Writer |
| `ts` | Req | Format time |

### 5.8 `t=encfg`

Replaces the legacy vault `encfg` (`salt`, `kdf`, `wrap`, `check`, `hint`,
`cv`). butterDB has no password KDF; the KEK is the server key file
(storage-model.md section 8).

| Key | Req | Rule |
| --- | --- | --- |
| `j` | Req | Job ID |
| `bc` | Req | Bucket channel ID |
| `kid` | Req | Key ID |
| `wrap` | Req | Wrapped DEK, 72 bytes: nonce(24) ‖ ciphertext(32) ‖ tag(16) |
| `check` | Req | Key check, 61 bytes: nonce(24) ‖ ciphertext(21) ‖ tag(16) |
| `kfp` | Req | Fingerprint of the server key that wrapped it |
| `cv` | Opt | Config version of this `kid`: 1 when the key is minted, plus one for each re-wrap of the same DEK under a new server key (security.md §5.6). Emitted only when > 1 |
| `w` | Opt | Writer |
| `ts` | Req | Format time |

---

## 6. Length limits

### 6.1 Carrier limits

| Carrier | Limit | Constant | Ops |
| --- | --- | --- | --- |
| Text message | 4096 characters | `MaxTextChars` (`limits.MaxPutOpChars`) | put, del, reg, bcreate, bcfg, bdel, encfg |
| Document caption | 1024 characters | `MaxCaptionChars` (`limits.MaxCaptionChars`) | body |

Telegram counts characters after entity parsing; butterDB sends no entities and
every header is ASCII, so characters equal bytes. `Format` returns
`ErrWireTooLong` when its output exceeds the carrier limit. `Parse` returns
`ErrWireTooLong` for a header line longer than 4096 bytes.

### 6.2 Worst-case sizes

Computed with the widest legal value of every field: a 36-character `j`,
`bm` and `pv` of 2147483647, `sz` 1992294400, `psz` 1991808062, `enc=1`, a
16-character `kid`, a 38-character multipart `et`, a 36-character writer
(`sys.` plus 32), a 12-digit `ts`, 19-digit `bc` and `uid`, a 63-character
bucket name and 10-digit `fv`, `mr` and `cv`.

| Op | Fixed part | Variable part | Worst case |
| --- | ---: | --- | ---: |
| `body` | 75 (70 without `ev`) | none | 75 |
| `del` | 121 | key ≤ 3072 | 3193 |
| `encfg` | 397 | none | 397 |
| `bcreate`, `bcfg` | 234 | none | 234 |
| `bdel` | 194 | none | 194 |
| `reg` | 190 | none | 190 |
| `put` | 286 | key ≤ 3072, `ct`, `m.` and `s.` entries | can exceed 4096 |

The encoded key is at most `MaxEncodedKeyChars` = 3072 characters, because
`QueryEscape` turns each byte into at most three characters. A space becomes
`+`, `/` becomes `%2F`, and every byte of a multi-byte UTF-8 character becomes
`%XX`.

Every op except `put` fits its carrier whatever the input. With the worst-case
key, 738 characters remain for a put's `ct` and metadata entries.

The put fixed part (286) is below `MaxPutFixedChars` (512), the bound
s3-api.md §8.7 relies on. Its guarantee then holds: a 1024-byte key, 2048
bytes of user metadata in at most 16 entries and 384 bytes of system headers
in at most 5 entries, all of `A-Z a-z 0-9 - _ . ~`, take 1024 + 2112 + 404 + 286
= 3826 characters, leaving 270 for `|ct=` and the escaped content type.
`test/acceptance/phase02` asserts the 512 bound.

### 6.3 Overflow rule

`internal/s3api/objectops` (PutObject, CopyObject), `internal/s3api/multipart`
(at `CreateMultipartUpload`) and the admin upload handler check the record
before spooling a byte:

```go
func PutRecordLen(key, contentType string, userMeta, systemMeta map[string]string, writer string) (int, error)
func FitsPut(key, contentType string, userMeta, systemMeta map[string]string, writer string) error
```

`PutRecordLen` formats a provisional put with the request's key, content type,
metadata and writer, and the widest legal value of every other field (section
6.2), and returns its length. It returns `ErrWireMalformed` when a field
breaks a setter rule (section 9). `FitsPut` returns `ErrWireTooLong` when that
length exceeds `MaxTextChars`, else the `PutRecordLen` error or `nil`. Because
the provisional values are the widest legal ones, a request that passes can
never fail at `Format` time, whatever sizes and message IDs the job later
produces.

Classification, applied by callers in this order (s3-api.md §8.7 owns the
S3 wording):

| Check | S3 response | Admin API response |
| --- | --- | --- |
| Key invalid (empty, invalid UTF-8, U+0000; the S3 and admin APIs also refuse U+0001–U+001F and U+007F) | `400 InvalidArgument` | `422 object_key_invalid` |
| Key over 1024 bytes | `400 KeyTooLongError` | `422 object_key_invalid` |
| `PutRecordLen(key, contentType, nil, nil, writer)` > 4096 | `400 KeyTooLongError` | `422 object_key_invalid` |
| `PutRecordLen` with all metadata > 4096 | `400 MetadataTooLarge` with the length as `<Size>` | `422 metadata_too_large` |

The wire layer accepts control characters other than U+0000 in keys so that it
never rejects a header another writer produced; the stricter S3 rule is
enforced at the API edge.

Documented deviation from S3: a request whose key, content type and metadata
are each within S3's limits can still be refused when their encoded sum
exceeds the 4096-character message. Such a request never reaches Telegram.

---

## 7. Versioning and unknown ops

### 7.1 Three version layers

| Layer | Where | Changes when |
| --- | --- | --- |
| Grammar | `TDX1` prefix | Never. A new grammar would need a new prefix and is out of scope |
| Format version | `fv` in `t=reg`; `wire.FormatVersion` in the binary | An op type is added, or a key is added whose meaning an older reader may ignore safely |
| Minimum reader | `mr` in `t=reg` | A change that an older reader would project **wrongly** by ignoring it: a new op that removes or replaces objects, a new key that changes the meaning of an existing op, a new `ev` |

butterDB 0.1 writes `fv=1`, `mr=1`.

### 7.2 Rules

1. A binary refuses to start when the registry marker's `mr` exceeds its `wire.FormatVersion` (storage-model.md section 4.4, `format_too_new`).
2. A binary whose `wire.FormatVersion` exceeds the marker's `fv` sends a new `t=reg` with the higher `fv` (and `mr` if its new ops require it) **before** it writes its first new op. It then updates `state.json`.
3. A binary never writes an op type or key that its marker's `fv` does not cover.
4. Unknown keys in a known op are ignored (section 1). Adding an optional key whose absence keeps the old meaning needs only an `fv` bump.
5. An unknown `t` is the E5 case below.

### 7.3 Unknown op (E5)

`Parse` returns `(Op{Type: OpType(t)}, ErrWireUnknownOp)` when `t` is not
`Known`. The projector then:

1. Inserts the `replay_log` row with `op_type = t`, `op_payload_json = {"t":"<t>"}`, the raw header and its hash.
2. Inserts `replay_log_rejects` with code `unknown_op`.
3. Continues with the next message. The page transaction does not fail and the watermark advances.

A rebuild by a binary that knows `t` re-parses `raw_header`, applies the op and
clears the reject. An unknown op is never a parse failure that is skipped
without a record, and never fatal.

Classification of a message the projector observes:

| `HasPrefix(raw)` | `Parse` result | Recorded |
| --- | --- | --- |
| false | any error | Nothing. Not a butterDB message (service messages, stray posts) |
| true | `ErrWireMissingHeader` cannot occur; `ErrWireBadVersion` (`TDX10\|...`, `TDX1x\|...`) or `ErrWireMalformed` or `ErrWireTooLong` | `replay_log_rejects` with code `malformed` and the raw header; no `replay_log` row |
| true | `ErrWireUnknownOp` | `replay_log` plus `replay_log_rejects` `unknown_op` (above) |
| true | `nil` | `replay_log`, then the applier |

The grammar order is legacy section 8's, unchanged: a header without `|` is
`ErrWireMalformed` before its prefix is examined. `HasPrefix` exists so that
this ordering never turns a plain-text message into a recorded reject.

---

## 8. Parser skeleton

```
Parse(raw string) (Op, error):
    header := TrimSpace(ExtractHeaderLine(raw))
    if header == ""                    → ErrWireMissingHeader
    if len(header) > 4096              → ErrWireTooLong
    parts := Split(header, "|")
    if len(parts) < 2                  → ErrWireMalformed
    if parts[0] != "TDX1"              → ErrWireBadVersion

    kv := {}
    for each p in parts[1:]:
        eq := IndexByte(p, '=')
        if eq <= 0                     → ErrWireMalformed
        kv[p[:eq]] = p[eq+1:]

    t, ok := kv["t"]; if !ok || t == "" → ErrWireMalformed   (E16)
    op := Op{Type: OpType(t)}
    if !Known(op.Type)                 → op, ErrWireUnknownOp

    switch op.Type:
        per-type extraction of non-shared keys with the setters of section 9
        put only: for each key starting "m." or "s.": setMetaEntry

    # Shared attributes, applied to EVERY type, unchanged from legacy section 8
    if sz  present: ParseInt ≥ 0 else ErrWireMalformed
    if ts  present: ParseInt in [0, 253402300799] else ErrWireMalformed
    if enc == "1":  Encrypted = true; EncryptionVersion = 1
    if ev  present: Atoi else ErrWireMalformed; EncryptionVersion = ev
    if psz present: ParseInt ≥ 0 else ErrWireMalformed

    per-type requiredness and cross-field rules of section 5
    return op, nil
```

The only grammar change from legacy section 8 is the unknown-type branch: the
legacy `default → ErrWireBadOpType` becomes `ErrWireUnknownOp` returned with the
type filled in, so the caller can record it (E5). `rend` is not parsed; it was
valid only on the removed `t=part`.

---

## 9. Setters

| Setter | Rule | Error |
| --- | --- | --- |
| `setJobID` | Matches the UUIDv4 pattern | `ErrWireMalformed` |
| `setKey` | Non-empty raw; `QueryUnescape` succeeds; 1–1024 bytes; `utf8.ValidString`; no `0x00` | `ErrWireMalformed` |
| `setMsgID` | `ParseInt`; `0 ≤ v ≤ 2147483647` (`bm`) or `1 ≤ v ≤ 2147483647` (`pv`) | `ErrWireMalformed` |
| `setPositiveInt64` | `ParseInt`; `v > 0` (`bc`, `uid`) | `ErrWireMalformed` |
| `setBoundedInt` | `ParseInt`; within the stated range (`cma`, `fv`, `mr`, `cv`) | `ErrWireMalformed` |
| `setPattern` | Raw value matches the key's pattern (`kid`, `et`, `h`, `w`, `b`, `dep`, `kfp`) | `ErrWireMalformed` |
| `setEnum` | Raw value is one of the listed words (`pol`, `de`) | `ErrWireMalformed` |
| `setContentType` | `QueryUnescape` succeeds; 1–1024 bytes; every byte in `0x20`–`0x7E` | `ErrWireMalformed` |
| `setMetaEntry` | Name after the prefix is non-empty and `QueryUnescape`s; value `QueryUnescape`s; name, value and per-value size per section 5.2.1 or 5.2.2; no duplicate unescaped name; after all entries, the user metadata total is ≤ 2048 | `ErrWireMalformed` |
| `setB64Len` | Contains no CR or LF; canonical unpadded base64url (strict decoding) decodes to exactly the stated length (`wrap` 72, `check` 61) (E16) | `ErrWireMalformed` |

`Format` applies the same checks to the `Op` fields before writing, so an
invalid `Op` returns `ErrWireMalformed` rather than an unparseable header.

---

## 10. Worked examples

Field values are well-formed and cross-consistent (job IDs, sizes, key IDs,
fingerprints); hashes and message IDs are illustrative, not computed from real
files.

Registry marker (registry msg 2):

```text
TDX1|t=reg|dep=6pzyoxbfwcf6ubwcq5gpvjg5c4|fv=1|mr=1|kfp=bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6|uid=777000123|w=sys.setup|ts=1763000000
```

Bucket creation (registry msg 3), bucket channel 2045123456:

```text
TDX1|t=bcreate|j=0b6f3d2a-7c41-4e8a-9d15-2f6c8a4b1e07|b=photos|bc=2045123456|pol=private|de=tde1|cma=3600|w=dev.k3m9x2q7w4t1z8r6|ts=1763000060
```

Bucket key for it (registry msg 4):

```text
TDX1|t=encfg|j=5d2e8f10-3a6b-4c9d-8e7f-0a1b2c3d4e5f|bc=2045123456|kid=437ad4b129840534|wrap=rohtxlB3lex0XEw_yy6yxz4Uk0yGfuBXunJJm_oSHoNrKsFXJu59awr2qxPDjpLK4NFQV7FZmH-UzHQR1xfxRXmyqhAPu7NP|check=A3zUli5DSAEla4henJBR8yCw24PznqetvQ105t7H89-uzI9kZWZkGnuiZg8wEfw1cCkcV5kNGgCRJokZ8g|kfp=bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6|w=dev.k3m9x2q7w4t1z8r6|ts=1763000061
```

Configuration change to public-read with a one-day max-age (registry msg 5):

```text
TDX1|t=bcfg|j=9a7c5e3b-1d2f-4a6b-8c9d-7e5f3a1b2c4d|b=photos|bc=2045123456|pol=public-read|de=tde1|cma=86400|w=dev.k3m9x2q7w4t1z8r6|ts=1763000120
```

Unencrypted body (bucket msg 8811, document caption):

```text
TDX1|t=body|j=4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f|sz=482113
```

Its put (bucket msg 8812): key `2026/11/cat.jpg`, JPEG, two user metadata
entries (`x-amz-meta-camera: x100v`, `x-amz-meta-owner: ana`), written by an S3
access key:

```text
TDX1|t=put|j=4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f|k=2026%2F11%2Fcat.jpg|bm=8811|sz=482113|et=9b2cf535f27731c974343645a3985328|h=77af778b51abd4a3c51c5ddd97204a9c3ae614ebccb75a606c3b6865aed6744e|ct=image%2Fjpeg|m.camera=x100v|m.owner=ana|w=key.BDBK7Q2M9X4T1W8Z3R6Y|ts=1763000200
```

Encrypted body and put (734,003 plaintext bytes → 734,245 stored), with a
`Cache-Control` system header:

```text
TDX1|t=body|j=c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f|sz=734245|enc=1
```

```text
TDX1|t=put|j=c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f|k=2026%2F11%2Fdog.png|bm=8813|sz=734245|psz=734003|enc=1|kid=437ad4b129840534|et=1f3870be274f6c49b3e31a0c6728957f|h=2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae|ct=image%2Fpng|s.cache-control=max-age%3D86400|w=key.BDBK7Q2M9X4T1W8Z3R6Y|ts=1763000260
```

Multipart-completed object with a space and parentheses in the key, default
content type omitted:

```text
TDX1|t=put|j=7e6d5c4b-3a29-4180-9f7e-6d5c4b3a2918|k=reports%2FQ3+summary+%28final%29.pdf|bm=8820|sz=15728640|et=0befd8d67e1fa8a58b52371b0b3a021b-3|h=5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03|w=dev.k3m9x2q7w4t1z8r6|ts=1763000400
```

Zero-byte object, no body:

```text
TDX1|t=put|j=2a3b4c5d-6e7f-4a8b-9c0d-1e2f3a4b5c6d|k=2026%2F11%2F|bm=0|sz=0|et=d41d8cd98f00b204e9800998ecf8427e|h=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855|w=key.BDBK7Q2M9X4T1W8Z3R6Y|ts=1763000500
```

Tombstone (bucket msg 8830) deleting version 8812:

```text
TDX1|t=del|j=d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a|k=2026%2F11%2Fcat.jpg|pv=8812|w=key.BDBK7Q2M9X4T1W8Z3R6Y|ts=1763000600
```

Bucket deletion (registry msg 9):

```text
TDX1|t=bdel|j=e5f6a7b8-c9d0-4e1f-a2b3-c4d5e6f7a8b9|b=photos|bc=2045123456|w=dev.k3m9x2q7w4t1z8r6|ts=1763000700
```

Rejected examples and their errors:

| Text | Result |
| --- | --- |
| `TDX2\|t=put\|...` | `ErrWireBadVersion` |
| `TDX1` | `ErrWireMalformed` (one part) |
| `TDX1\|=x\|t=put` | `ErrWireMalformed` (`=` at index 0) |
| `TDX1\|t=put\|j=4f9c...\|k=` | `ErrWireMalformed` (empty key) |
| `TDX1\|t=put\|...\|sz=-1\|...` | `ErrWireMalformed` |
| `TDX1\|t=put\|...\|psz=10\|...` without `enc=1` | `ErrWireMalformed` |
| `TDX1\|t=put\|...\|enc=1\|...` without `kid` | `ErrWireMalformed` |
| `TDX1\|t=put\|...\|bm=0\|sz=5\|...` | `ErrWireMalformed` |
| `TDX1\|t=encfg\|...\|wrap=<71 bytes>\|...` | `ErrWireMalformed` |
| `TDX1\|t=reg\|...\|fv=1\|mr=2\|...` | `ErrWireMalformed` |
| `TDX1\|t=snapshot\|j=...` | `Op{Type: "snapshot"}`, `ErrWireUnknownOp` |
| `TDX1\|t=mkdir\|obj=d:...\|p=\|n=x` | `Op{Type: "mkdir"}`, `ErrWireUnknownOp` (legacy op, section 11) |
| `hello` | `ErrWireMalformed` (one part; the legacy order checks the part count before the prefix). `HasPrefix` is false, so the projector ignores it |
| `hello\|t=put` | `ErrWireBadVersion`; `HasPrefix` false, ignored |

---

## 11. Legacy ops not used

None of these is `Known`. Text carrying them parses to `ErrWireUnknownOp`.

| Legacy `t=` | Why not used |
| --- | --- |
| `f` | A single document carrying its own header would put the key in a 1024-character caption; keys are up to 1024 bytes and 3072 escaped characters (E2) |
| `meta` | Metadata travels on each put; there is no separate metadata op |
| `mkdir`, `rename`, `move`, `rmdir` | S3 has a flat key space; there are no folders, renames or moves. Prefixes and delimiters are computed from keys |
| `tomb` | Replaced by `t=del`, which addresses a key, not a message ID |
| `part`, `manifest` | TDX1 multipart is cut. One object is one document of at most 1900 MiB; S3 multipart is assembled on the server (D9) |
| `fcommit` | Its body-then-commit pattern is kept as `t=body` plus `t=put` (E2) |
| `freplace` | Overwrite is a new `t=put`; last writer wins by message ID (E3) |
| `dcommit` | No folders |
| `relocate` | No moves or renames; `CopyObject` writes a new put |
| `trash`, `untrash` | No trash (D10) |
| `harddel` | Replaced by `t=del` with batch message deletion (E4) |

The legacy writable envelope (`v`, `oid`, `rev`, `drev`, `dst`, `ow`, `del`,
`purge`, `retain`), the namespace keys (`obj`, `p`, `n`), the multipart keys
(`u`, `pix`, `pc`), the rendition key `rend`, the vault keys (`salt`, `kdf`,
`hint`) and the content reference keys (`cmid`, `hash`) are not part of the
butterDB vocabulary. The legacy object identifier forms `d:` and `f:` and the
errors `ErrWireBadObject` and `ErrWireBadParent` are dropped.

---

## 12. Round-trip test obligation

For every op type, `Parse(Format(op))` returns `op` exactly, for a table of ops
that covers each optional key present and absent. `Parse` must reject every
malformed case with its specific error. Write this table test first.

Normalisation the test relies on: `Parse` returns nil maps when there is no
`m.` or `s.` entry; `PlaintextSize = StoredSize` for unencrypted puts; `ContentType =
"application/octet-stream"` when `ct` is absent; `EncryptionVersion = 1` with
`enc=1`; `ConfigVersion = 1` when `cv` is absent. Test fixtures use these
normalised values.

Named tests in `internal/wire`:

| Test | Asserts |
| --- | --- |
| `TestRoundTripEveryOp` | Section 10 examples and generated variants round-trip |
| `TestFormatCanonicalOrder` | `Format` output equals the section 10 strings byte for byte |
| `TestParseRejectsMalformed` | Every row of the rejected-examples table |
| `TestUnknownOpReturnsType` | `ErrWireUnknownOp` with `Type` set, for `snapshot` and every legacy op of section 11 |
| `TestUnknownKeysIgnored` | A known op with an extra `zz=1` parses to the same `Op` |
| `TestRepeatedKeyLastWins` | `k=a\|k=b` yields key `b` |
| `TestRemainderIgnored` | Text after `\n` does not change the result |
| `TestSharedAttrsOnEveryType` | `sz=-1` on `t=reg` is `ErrWireMalformed` |
| `TestTimestampBounds` | `ts=253402300799` parses; `ts=253402300800` fails |
| `TestKeyEscapingHostile` | Keys with leading and trailing spaces, `\n`, `\|`, `=`, `%`, `+`, emoji and 1024 bytes of multi-byte characters round-trip |
| `TestKeyLimits` | 1024 bytes passes, 1025 fails, invalid UTF-8 fails, U+0000 fails |
| `TestMetaEncoding` | Entries emitted sorted by name, `m.` before `s.`; unreserved bytes cost one character and each entry four extra; 2048 bytes passes, 2049 fails; uppercase name fails; `m.a` with `m.%61` fails; empty `m.` name fails; CR or LF in a value fails; tab passes; an `s.` name outside the list fails; a 1025-byte `s.` value fails |
| `TestPutFixedPartBound` | The widest put with an empty key, no `ct` and no metadata is 286 characters, at most `MaxPutFixedChars` |
| `TestKeyOf` | `put` and `del` return their key; every other type returns false |
| `TestEmptyObjectRules` | `bm=0` with each forbidden companion fails |
| `TestFormatTooLong` | A put with a 1024-byte multi-byte key and 2048 bytes of metadata returns `ErrWireTooLong`; a body is always under 1024 characters |
| `TestFitsPutMatchesFormat` | For random inputs, `FitsPut == nil` implies the real put, with any legal sizes and message IDs, formats within 4096 characters, and `PutRecordLen` is never below the real length |
| `TestHasPrefixClassification` | `hello`, `hello\|t=put` and an empty caption are not candidates; `TDX1`, `TDX1x\|t=put` and `TDX1\|t=zz` are |
| `TestHeaderIsASCII` | Every formatted header is ASCII with no `\n` and no surrounding whitespace |

Phase 02 integration (`test/acceptance/phase02`) repeats the round trip for
every op through the projector and asserts that `wire` constants equal
`internal/domain/limits` values. Phase 04 integration sends hostile keys
through the Telegram test DC behind its environment gate and asserts the text
read back is byte-identical (plan.md risk R3).

---

## 13. TDrive sources

| Concern | TDrive file |
| --- | --- |
| Grammar, `Parse`, `Format`, setters, error values | `backend/projection/wire.go` |
| Round-trip and malformed-case tests to port as a pattern | `backend/projection/wire_test.go`, `backend/projection/writable_wire_test.go` |
| Op type constants | `backend/projection/types.go` |

Port the grammar functions (`ExtractHeaderLine`, the split and pair loop, the
shared-attribute block, `setB64`, `setRequiredRaw`) without their comment
blocks. Do not port the op switch, the writable envelope, the content
reference, renditions or the folder and file identifier setters.
