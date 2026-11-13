package wire

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

const (
	exampleWrap  = "rohtxlB3lex0XEw_yy6yxz4Uk0yGfuBXunJJm_oSHoNrKsFXJu59awr2qxPDjpLK4NFQV7FZmH-UzHQR1xfxRXmyqhAPu7NP"
	exampleCheck = "A3zUli5DSAEla4henJBR8yCw24PznqetvQ105t7H89-uzI9kZWZkGnuiZg8wEfw1cCkcV5kNGgCRJokZ8g"

	exampleFingerprint = "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"
	exampleDevWriter   = "dev.k3m9x2q7w4t1z8r6"
	exampleKeyWriter   = "key.BDBK7Q2M9X4T1W8Z3R6Y"

	bodyHeader     = "TDX1|t=body|j=4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f|sz=482113"
	putHeader      = "TDX1|t=put|j=4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f|k=2026%2F11%2Fcat.jpg|bm=8811|sz=482113|et=9b2cf535f27731c974343645a3985328|h=77af778b51abd4a3c51c5ddd97204a9c3ae614ebccb75a606c3b6865aed6744e|ct=image%2Fjpeg|m.camera=x100v|m.owner=ana|w=key.BDBK7Q2M9X4T1W8Z3R6Y|ts=1763000200"
	encPutHeader   = "TDX1|t=put|j=c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f|k=2026%2F11%2Fdog.png|bm=8813|sz=734245|psz=734003|enc=1|kid=437ad4b129840534|et=1f3870be274f6c49b3e31a0c6728957f|h=2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae|ct=image%2Fpng|s.cache-control=max-age%3D86400|w=key.BDBK7Q2M9X4T1W8Z3R6Y|ts=1763000260"
	emptyPutHeader = "TDX1|t=put|j=2a3b4c5d-6e7f-4a8b-9c0d-1e2f3a4b5c6d|k=2026%2F11%2F|bm=0|sz=0|et=d41d8cd98f00b204e9800998ecf8427e|h=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855|w=key.BDBK7Q2M9X4T1W8Z3R6Y|ts=1763000500"
	delHeader      = "TDX1|t=del|j=d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a|k=2026%2F11%2Fcat.jpg|pv=8812|w=key.BDBK7Q2M9X4T1W8Z3R6Y|ts=1763000600"
	regHeader      = "TDX1|t=reg|dep=6pzyoxbfwcf6ubwcq5gpvjg5c4|fv=1|mr=1|kfp=bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6|uid=777000123|w=sys.setup|ts=1763000000"
	bcreateHeader  = "TDX1|t=bcreate|j=0b6f3d2a-7c41-4e8a-9d15-2f6c8a4b1e07|b=photos|bc=2045123456|pol=private|de=tde1|cma=3600|w=dev.k3m9x2q7w4t1z8r6|ts=1763000060"
	bcfgHeader     = "TDX1|t=bcfg|j=9a7c5e3b-1d2f-4a6b-8c9d-7e5f3a1b2c4d|b=photos|bc=2045123456|pol=public-read|de=tde1|cma=86400|w=dev.k3m9x2q7w4t1z8r6|ts=1763000120"
	bdelHeader     = "TDX1|t=bdel|j=e5f6a7b8-c9d0-4e1f-a2b3-c4d5e6f7a8b9|b=photos|bc=2045123456|w=dev.k3m9x2q7w4t1z8r6|ts=1763000700"
	encfgHeader    = "TDX1|t=encfg|j=5d2e8f10-3a6b-4c9d-8e7f-0a1b2c3d4e5f|bc=2045123456|kid=437ad4b129840534|wrap=" + exampleWrap + "|check=" + exampleCheck + "|kfp=bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6|w=dev.k3m9x2q7w4t1z8r6|ts=1763000061"
)

func decodeBase64(t testing.TB, raw string) []byte {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return decoded
}

func workedExampleOps(t testing.TB) []Op {
	t.Helper()
	return []Op{
		{Type: OpRegistry, DeploymentID: "6pzyoxbfwcf6ubwcq5gpvjg5c4", FormatVersion: 1, MinReaderVersion: 1,
			KEKFingerprint: exampleFingerprint, PrimaryUserID: 777000123, Writer: "sys.setup", Timestamp: 1763000000},
		{Type: OpBucketCreate, JobID: "0b6f3d2a-7c41-4e8a-9d15-2f6c8a4b1e07", Bucket: "photos", BucketChannelID: 2045123456,
			Policy: "private", DefaultEncryption: "tde1", CacheMaxAge: 3600, Writer: exampleDevWriter, Timestamp: 1763000060},
		{Type: OpEncConfig, JobID: "5d2e8f10-3a6b-4c9d-8e7f-0a1b2c3d4e5f", BucketChannelID: 2045123456, KeyID: "437ad4b129840534",
			WrappedKey: decodeBase64(t, exampleWrap), KeyCheck: decodeBase64(t, exampleCheck), KEKFingerprint: exampleFingerprint,
			ConfigVersion: 1, Writer: exampleDevWriter, Timestamp: 1763000061},
		{Type: OpBucketConfig, JobID: "9a7c5e3b-1d2f-4a6b-8c9d-7e5f3a1b2c4d", Bucket: "photos", BucketChannelID: 2045123456,
			Policy: "public-read", DefaultEncryption: "tde1", CacheMaxAge: 86400, Writer: exampleDevWriter, Timestamp: 1763000120},
		{Type: OpBody, JobID: "4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f", StoredSize: 482113},
		{Type: OpPut, JobID: "4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f", Key: "2026/11/cat.jpg", BodyMsgID: 8811,
			StoredSize: 482113, PlaintextSize: 482113, ETag: "9b2cf535f27731c974343645a3985328",
			SHA256:      "77af778b51abd4a3c51c5ddd97204a9c3ae614ebccb75a606c3b6865aed6744e",
			ContentType: "image/jpeg", UserMeta: map[string]string{"camera": "x100v", "owner": "ana"},
			Writer: exampleKeyWriter, Timestamp: 1763000200},
		{Type: OpBody, JobID: "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", StoredSize: 734245, Encrypted: true, EncryptionVersion: 1},
		{Type: OpPut, JobID: "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", Key: "2026/11/dog.png", BodyMsgID: 8813,
			StoredSize: 734245, PlaintextSize: 734003, Encrypted: true, EncryptionVersion: 1, KeyID: "437ad4b129840534",
			ETag:        "1f3870be274f6c49b3e31a0c6728957f",
			SHA256:      "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
			ContentType: "image/png", SystemMeta: map[string]string{"cache-control": "max-age=86400"},
			Writer: exampleKeyWriter, Timestamp: 1763000260},
		{Type: OpPut, JobID: "7e6d5c4b-3a29-4180-9f7e-6d5c4b3a2918", Key: "reports/Q3 summary (final).pdf", BodyMsgID: 8820,
			StoredSize: 15728640, PlaintextSize: 15728640, ETag: "0befd8d67e1fa8a58b52371b0b3a021b-3",
			SHA256:      "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
			ContentType: DefaultContentType, Writer: exampleDevWriter, Timestamp: 1763000400},
		{Type: OpPut, JobID: "2a3b4c5d-6e7f-4a8b-9c0d-1e2f3a4b5c6d", Key: "2026/11/", BodyMsgID: 0,
			ETag: EmptyMD5, SHA256: EmptySHA256, ContentType: DefaultContentType,
			Writer: exampleKeyWriter, Timestamp: 1763000500},
		{Type: OpDel, JobID: "d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a", Key: "2026/11/cat.jpg", PutVersion: 8812,
			Writer: exampleKeyWriter, Timestamp: 1763000600},
		{Type: OpBucketDelete, JobID: "e5f6a7b8-c9d0-4e1f-a2b3-c4d5e6f7a8b9", Bucket: "photos", BucketChannelID: 2045123456,
			Writer: exampleDevWriter, Timestamp: 1763000700},
	}
}

func workedExampleHeaders(t testing.TB) []string {
	t.Helper()
	data, err := os.ReadFile("testdata/worked_examples.txt")
	if err != nil {
		t.Fatalf("read worked examples: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func headerWith(header, key, value string) string {
	parts := strings.Split(header, "|")
	for i, part := range parts {
		if strings.HasPrefix(part, key+"=") {
			parts[i] = key + "=" + value
			return strings.Join(parts, "|")
		}
	}
	return header + "|" + key + "=" + value
}

func headerWithout(header, key string) string {
	parts := strings.Split(header, "|")
	kept := parts[:0:0]
	for _, part := range parts {
		if !strings.HasPrefix(part, key+"=") {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "|")
}
