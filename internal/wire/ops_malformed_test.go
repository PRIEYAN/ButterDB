package wire

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEveryMalformedCase(t *testing.T) {
	validJob := "4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f"
	encPut := func(key, value string) string { return headerWith(encPutHeader, key, value) }
	cases := []struct {
		name   string
		header string
	}{
		{"body missing size", headerWithout(bodyHeader, "sz")},
		{"body zero size", headerWith(bodyHeader, "sz", "0")},
		{"body size above stored maximum", headerWith(bodyHeader, "sz", "1992294401")},
		{"body negative size", headerWith(bodyHeader, "sz", "-1")},
		{"body size not a number", headerWith(bodyHeader, "sz", "abc")},
		{"body encryption version without enc", headerWith(bodyHeader, "ev", "2")},
		{"body encryption version not a number", headerWith(headerWith(bodyHeader, "enc", "1"), "ev", "x")},
		{"body enc zero with encryption version", headerWith(headerWith(bodyHeader, "enc", "0"), "ev", "1")},
		{"body bad timestamp", headerWith(bodyHeader, "ts", "253402300800")},
		{"put missing key", headerWithout(putHeader, "k")},
		{"put empty key", headerWith(putHeader, "k", "")},
		{"put bad key escape", headerWith(putHeader, "k", "a%zz")},
		{"put key with nul", headerWith(putHeader, "k", "a%00b")},
		{"put key invalid utf8", headerWith(putHeader, "k", "%ff%fe")},
		{"put key too long", headerWith(putHeader, "k", strings.Repeat("a", MaxKeyBytes+1))},
		{"put missing body message id", headerWithout(putHeader, "bm")},
		{"put negative body message id", headerWith(putHeader, "bm", "-1")},
		{"put body message id too large", headerWith(putHeader, "bm", "2147483648")},
		{"put missing size", headerWithout(putHeader, "sz")},
		{"put negative size", headerWith(putHeader, "sz", "-1")},
		{"put size above stored maximum", headerWith(putHeader, "sz", "1992294401")},
		{"put body reference with zero size", headerWith(putHeader, "sz", "0")},
		{"put plaintext size without enc", headerWith(putHeader, "psz", "10")},
		{"put key id without enc", headerWith(putHeader, "kid", "437ad4b129840534")},
		{"put encryption version without enc", headerWith(putHeader, "ev", "2")},
		{"put enc without key id", headerWithout(encPutHeader, "kid")},
		{"put enc without plaintext size", headerWithout(encPutHeader, "psz")},
		{"put negative plaintext size", encPut("psz", "-1")},
		{"put short key id", encPut("kid", "437ad4b12984053")},
		{"put uppercase key id", encPut("kid", "437AD4B129840534")},
		{"put missing etag", headerWithout(putHeader, "et")},
		{"put short etag", headerWith(putHeader, "et", "9b2cf535f27731c974343645a398532")},
		{"put uppercase etag", headerWith(putHeader, "et", "9B2CF535F27731C974343645A3985328")},
		{"put quoted etag", headerWith(putHeader, "et", "%229b2cf535f27731c974343645a3985328%22")},
		{"put etag part count zero", headerWith(putHeader, "et", "9b2cf535f27731c974343645a3985328-0")},
		{"put etag part count leading zero", headerWith(putHeader, "et", "9b2cf535f27731c974343645a3985328-01")},
		{"put etag part count above limit", headerWith(putHeader, "et", "9b2cf535f27731c974343645a3985328-10001")},
		{"put etag part count not a number", headerWith(putHeader, "et", "9b2cf535f27731c974343645a3985328-x")},
		{"put etag two suffixes", headerWith(putHeader, "et", "9b2cf535f27731c974343645a3985328-1-2")},
		{"put missing sha256", headerWithout(putHeader, "h")},
		{"put short sha256", headerWith(putHeader, "h", strings.Repeat("a", 63))},
		{"put uppercase sha256", headerWith(putHeader, "h", strings.Repeat("A", 64))},
		{"put empty content type", headerWith(putHeader, "ct", "")},
		{"put content type with control byte", headerWith(putHeader, "ct", "text%0Aplain")},
		{"put content type non ascii", headerWith(putHeader, "ct", "text%2F%C3%A9")},
		{"put content type too long", headerWith(putHeader, "ct", strings.Repeat("a", MaxContentTypeBytes+1))},
		{"put bad content type escape", headerWith(putHeader, "ct", "a%zz")},
		{"put user meta uppercase name", headerWith(putHeader, "m.Camera", "x")},
		{"put user meta empty name", headerWith(putHeader, "m.", "x")},
		{"put user meta duplicate name", headerWith(headerWith(putHeader, "m.a", "1"), "m.%61", "2")},
		{"put user meta bad value escape", headerWith(putHeader, "m.a", "%zz")},
		{"put user meta carriage return", headerWith(putHeader, "m.a", "x%0Dy")},
		{"put user meta delete byte", headerWith(putHeader, "m.a", "x%7Fy")},
		{"put user meta invalid utf8", headerWith(putHeader, "m.a", "%ff")},
		{"put system meta unknown name", headerWith(putHeader, "s.x-custom", "v")},
		{"put system meta empty value", headerWith(putHeader, "s.expires", "")},
		{"put system meta too long", headerWith(putHeader, "s.expires", strings.Repeat("a", MaxSysHeaderBytes+1))},
		{"put missing writer", headerWithout(putHeader, "w")},
		{"put bad writer kind", headerWith(putHeader, "w", "usr.abc")},
		{"put writer without name", headerWith(putHeader, "w", "key.")},
		{"put writer name too long", headerWith(putHeader, "w", "key."+strings.Repeat("a", 33))},
		{"put missing timestamp", headerWithout(putHeader, "ts")},
		{"put negative timestamp", headerWith(putHeader, "ts", "-1")},
		{"put timestamp above bound", headerWith(putHeader, "ts", "253402300800")},
		{"put timestamp not a number", headerWith(putHeader, "ts", "now")},
		{"del missing key", headerWithout(delHeader, "k")},
		{"del missing job", headerWithout(delHeader, "j")},
		{"del put version zero", headerWith(delHeader, "pv", "0")},
		{"del put version too large", headerWith(delHeader, "pv", "2147483648")},
		{"del missing writer", headerWithout(delHeader, "w")},
		{"del missing timestamp", headerWithout(delHeader, "ts")},
		{"reg missing deployment", headerWithout(regHeader, "dep")},
		{"reg short deployment", headerWith(regHeader, "dep", "6pzyoxbfwcf6ubwcq5gpvjg5c")},
		{"reg deployment outside alphabet", headerWith(regHeader, "dep", "1pzyoxbfwcf6ubwcq5gpvjg5c4")},
		{"reg format version zero", headerWith(regHeader, "fv", "0")},
		{"reg min reader above format version", headerWith(regHeader, "mr", "2")},
		{"reg min reader zero", headerWith(regHeader, "mr", "0")},
		{"reg bad fingerprint prefix", headerWith(regHeader, "kfp", "bkf2_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6")},
		{"reg short fingerprint", headerWith(regHeader, "kfp", "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a")},
		{"reg missing user id", headerWithout(regHeader, "uid")},
		{"reg zero user id", headerWith(regHeader, "uid", "0")},
		{"reg bad writer", headerWith(regHeader, "w", "setup")},
		{"reg missing timestamp", headerWithout(regHeader, "ts")},
		{"bcreate missing job", headerWithout(bcreateHeader, "j")},
		{"bcreate short bucket", headerWith(bcreateHeader, "b", "ab")},
		{"bcreate uppercase bucket", headerWith(bcreateHeader, "b", "Photos")},
		{"bcreate zero channel", headerWith(bcreateHeader, "bc", "0")},
		{"bcreate bad policy", headerWith(bcreateHeader, "pol", "public")},
		{"bcreate bad default encryption", headerWith(bcreateHeader, "de", "aes")},
		{"bcreate max age above limit", headerWith(bcreateHeader, "cma", "31536001")},
		{"bcreate negative max age", headerWith(bcreateHeader, "cma", "-1")},
		{"bcreate missing max age", headerWithout(bcreateHeader, "cma")},
		{"bcreate missing timestamp", headerWithout(bcreateHeader, "ts")},
		{"bcfg bad policy", headerWith(bcfgHeader, "pol", "")},
		{"bcfg missing bucket", headerWithout(bcfgHeader, "b")},
		{"bdel missing channel", headerWithout(bdelHeader, "bc")},
		{"bdel missing job", headerWithout(bdelHeader, "j")},
		{"bdel bad writer", headerWith(bdelHeader, "w", "dev.")},
		{"encfg missing job", headerWithout(encfgHeader, "j")},
		{"encfg missing channel", headerWithout(encfgHeader, "bc")},
		{"encfg missing key id", headerWithout(encfgHeader, "kid")},
		{"encfg wrapped key 71 bytes", headerWith(encfgHeader, "wrap", base64.RawURLEncoding.EncodeToString(make([]byte, 71)))},
		{"encfg wrapped key 73 bytes", headerWith(encfgHeader, "wrap", base64.RawURLEncoding.EncodeToString(make([]byte, 73)))},
		{"encfg wrapped key not base64", headerWith(encfgHeader, "wrap", "!!!")},
		{"encfg wrapped key padded", headerWith(encfgHeader, "wrap", base64.URLEncoding.EncodeToString(make([]byte, 71)))},
		{"encfg missing wrapped key", headerWithout(encfgHeader, "wrap")},
		{"encfg key check 60 bytes", headerWith(encfgHeader, "check", base64.RawURLEncoding.EncodeToString(make([]byte, 60)))},
		{"encfg missing key check", headerWithout(encfgHeader, "check")},
		{"encfg missing fingerprint", headerWithout(encfgHeader, "kfp")},
		{"encfg config version zero", headerWith(encfgHeader, "cv", "0")},
		{"encfg config version not a number", headerWith(encfgHeader, "cv", "x")},
		{"encfg missing timestamp", headerWithout(encfgHeader, "ts")},
		{"encfg job id uppercase", headerWith(encfgHeader, "j", strings.ToUpper(validJob))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, err := Parse(tc.header)
			if !isMalformed(err) {
				t.Fatalf("Parse error = %v, want ErrWireMalformed", err)
			}
			if op.Type != "" {
				t.Fatalf("Parse returned a populated op on error: %#v", op)
			}
		})
	}
}
