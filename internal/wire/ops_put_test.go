package wire

import (
	"errors"
	"math/rand"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func putOpWithKey(t testing.TB, key string) Op {
	t.Helper()
	op := workedExampleOps(t)[5]
	op.Key = key
	return op
}

func TestKeyEscapingHostile(t *testing.T) {
	keys := map[string]string{
		"leading and trailing spaces": "  a b  ",
		"newline":                     "a\nb",
		"carriage return":             "a\rb",
		"pipe":                        "a|b",
		"equals":                      "a=b=c",
		"percent":                     "100%",
		"percent escape lookalike":    "a%2Fb",
		"plus":                        "a+b",
		"emoji":                       "\U0001F600/\U0001F680",
		"multi byte 1024":             strings.Repeat("é", MaxKeyBytes/2),
		"three byte runes":            strings.Repeat("€", MaxKeyBytes/3),
		"control character":           "a\x01b\x7f",
		"tdx1 lookalike":              "TDX1|t=put",
		"only dots":                   "..",
		"trailing slash":              "folder/",
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			op := putOpWithKey(t, key)
			op.UserMeta = nil
			header, err := Format(op)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			baselineOp := putOpWithKey(t, "k")
			baselineOp.UserMeta = nil
			baseline, err := Format(baselineOp)
			if err != nil {
				t.Fatalf("Format baseline: %v", err)
			}
			if strings.ContainsAny(header, "\n\r") || strings.Count(header, "|") != strings.Count(baseline, "|") {
				t.Fatalf("header leaks a separator: %q", header)
			}
			got, err := Parse(header)
			if err != nil {
				t.Fatalf("Parse(%q): %v", header, err)
			}
			if got.Key != key {
				t.Fatalf("key = %q, want %q", got.Key, key)
			}
			if !reflect.DeepEqual(got, op) {
				t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, op)
			}
		})
	}
}

func TestKeyLimits(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		valid bool
	}{
		{"1 byte", "a", true},
		{"1024 bytes", strings.Repeat("a", MaxKeyBytes), true},
		{"1025 bytes", strings.Repeat("a", MaxKeyBytes+1), false},
		{"empty", "", false},
		{"invalid utf8", "a\xffb", false},
		{"nul", "a\x00b", false},
		{"1024 bytes multi byte", strings.Repeat("é", MaxKeyBytes/2), true},
		{"1026 bytes multi byte", strings.Repeat("é", MaxKeyBytes/2+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := putOpWithKey(t, tc.key)
			op.UserMeta = nil
			header, err := Format(op)
			if tc.valid {
				if err != nil {
					t.Fatalf("Format: %v", err)
				}
				if _, err := Parse(header); err != nil {
					t.Fatalf("Parse: %v", err)
				}
				return
			}
			if !isMalformed(err) {
				t.Fatalf("Format error = %v, want ErrWireMalformed", err)
			}
			if tc.key == "" {
				return
			}
			raw := headerWith(putHeader, "k", url.QueryEscape(tc.key))
			if _, err := Parse(raw); !isMalformed(err) {
				t.Fatalf("Parse error = %v, want ErrWireMalformed", err)
			}
		})
	}
}

func TestMetaEncoding(t *testing.T) {
	t.Run("entries sorted with user before system", func(t *testing.T) {
		op := putOpWithKey(t, "k")
		op.UserMeta = map[string]string{"zeta": "1", "alpha": "2", "mid": "3"}
		op.SystemMeta = map[string]string{"expires": "e", "cache-control": "c"}
		header, err := Format(op)
		if err != nil {
			t.Fatalf("Format: %v", err)
		}
		want := "|m.alpha=2|m.mid=3|m.zeta=1|s.cache-control=c|s.expires=e|w="
		if !strings.Contains(header, want) {
			t.Fatalf("header %q does not contain %q", header, want)
		}
	})

	t.Run("each entry costs four extra characters", func(t *testing.T) {
		base := putOpWithKey(t, "k")
		base.UserMeta = nil
		baseHeader, err := Format(base)
		if err != nil {
			t.Fatal(err)
		}
		withEntry := base
		withEntry.UserMeta = map[string]string{"abc-_.~": "XYZ-_.~09"}
		entryHeader, err := Format(withEntry)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := len(entryHeader)-len(baseHeader), 4+len("abc-_.~")+len("XYZ-_.~09"); got != want {
			t.Fatalf("entry cost = %d, want %d", got, want)
		}
	})

	t.Run("metadata byte total", func(t *testing.T) {
		build := func(total int) map[string]string {
			entries := map[string]string{}
			remaining := total
			for i := 0; remaining > 0; i++ {
				name := "n" + strconv.Itoa(1000+i)
				valueLen := min(remaining-len(name), 100)
				entries[name] = strings.Repeat("v", valueLen)
				remaining -= len(name) + valueLen
			}
			return entries
		}
		at := putOpWithKey(t, "k")
		at.UserMeta = build(MaxUserMetaBytes)
		if _, err := Format(at); err != nil {
			t.Fatalf("2048 bytes: %v", err)
		}
		over := putOpWithKey(t, "k")
		over.UserMeta = build(MaxUserMetaBytes + 1)
		if _, err := Format(over); !isMalformed(err) {
			t.Fatalf("2049 bytes error = %v, want ErrWireMalformed", err)
		}
	})

	cases := []struct {
		name   string
		header string
		valid  bool
	}{
		{"uppercase name", headerWith(putHeader, "m.Camera", "x"), false},
		{"duplicate after unescape", headerWith(headerWith(putHeader, "m.a", "1"), "m.%61", "2"), false},
		{"empty name", headerWith(putHeader, "m.", "x"), false},
		{"carriage return in value", headerWith(putHeader, "m.a", "x%0Dy"), false},
		{"line feed in value", headerWith(putHeader, "m.a", "x%0Ay"), false},
		{"tab in value", headerWith(putHeader, "m.a", "x%09y"), true},
		{"empty value", headerWith(putHeader, "m.a", ""), true},
		{"escaped name round trips", headerWith(putHeader, "m.a%21b", "x"), true},
		{"space name", headerWith(putHeader, "m.a+b", "x"), false},
		{"system name outside list", headerWith(putHeader, "s.etag", "x"), false},
		{"system value 1024 bytes", headerWith(putHeader, "s.expires", strings.Repeat("a", MaxSysHeaderBytes)), true},
		{"system value 1025 bytes", headerWith(putHeader, "s.expires", strings.Repeat("a", MaxSysHeaderBytes+1)), false},
		{"system uppercase name", headerWith(putHeader, "s.Expires", "x"), false},
		{"unrelated prefix ignored", headerWith(putHeader, "mx.a", "x"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.header)
			if tc.valid && err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !tc.valid && !isMalformed(err) {
				t.Fatalf("Parse error = %v, want ErrWireMalformed", err)
			}
		})
	}
}

func widestPut() Op {
	return provisionalPut("", "", nil, nil, "sys."+strings.Repeat("w", 32))
}

func TestPutFixedPartBound(t *testing.T) {
	op := widestPut()
	op.Key = "k"
	header, err := Format(op)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	fixed := len(header) - len("k")
	if fixed != 286 || fixed > MaxPutFixedChars {
		t.Fatalf("fixed part = %d, want 286 and at most %d", fixed, MaxPutFixedChars)
	}
	length, err := PutRecordLen("k", "", nil, nil, op.Writer)
	if err != nil || length != len(header) {
		t.Fatalf("PutRecordLen = %d, %v; want %d", length, err, len(header))
	}
}

func TestFormatTooLong(t *testing.T) {
	op := putOpWithKey(t, strings.Repeat("é", MaxKeyBytes/2))
	op.UserMeta = map[string]string{}
	for i := 0; i < 16; i++ {
		op.UserMeta["n"+strconv.Itoa(10+i)] = strings.Repeat("v", 125)
	}
	if _, err := Format(op); !errors.Is(err, ErrWireTooLong) {
		t.Fatalf("Format error = %v, want ErrWireTooLong", err)
	}
	body := Op{Type: OpBody, JobID: widestJobID, StoredSize: MaxStoredBytes, Encrypted: true, EncryptionVersion: 2}
	header, err := Format(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) >= MaxCaptionChars {
		t.Fatalf("widest body is %d characters", len(header))
	}
}

func TestOverflowRule(t *testing.T) {
	writer := exampleKeyWriter
	longKey := strings.Repeat("é", MaxKeyBytes/2)
	cases := []struct {
		name        string
		key         string
		contentType string
		userMeta    map[string]string
		systemMeta  map[string]string
		want        error
	}{
		{"short key", "a", "text/plain", nil, nil, nil},
		{"default content type", "a", "", nil, nil, nil},
		{"ascii key at limit", strings.Repeat("a", MaxKeyBytes), "text/plain", nil, nil, nil},
		{"multi byte key alone overflows with metadata", longKey, "text/plain", bigMeta(), nil, ErrWireTooLong},
		{"multi byte key alone fits", longKey, "", nil, nil, nil},
		{"ascii key with maximum documented metadata fits", strings.Repeat("a", MaxKeyBytes), "", bigMeta(), documentedSystemMeta(), nil},
		{"key over limit", strings.Repeat("a", MaxKeyBytes+1), "", nil, nil, ErrWireMalformed},
		{"empty key", "", "", nil, nil, ErrWireMalformed},
		{"bad content type", "a", "text/\x01", nil, nil, ErrWireMalformed},
		{"bad metadata name", "a", "", map[string]string{"UPPER": "x"}, nil, ErrWireMalformed},
		{"bad system header", "a", "", nil, map[string]string{"x-other": "x"}, ErrWireMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := FitsPut(tc.key, tc.contentType, tc.userMeta, tc.systemMeta, writer)
			if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Fatalf("FitsPut error = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := PutRecordLen("a", "", nil, nil, "bad writer"); !isMalformed(err) {
		t.Fatalf("PutRecordLen with bad writer error = %v, want ErrWireMalformed", err)
	}
	length, err := PutRecordLen(longKey, "", bigMeta(), nil, writer)
	if err != nil || length <= MaxTextChars {
		t.Fatalf("PutRecordLen reports %d, %v; want a length above the limit", length, err)
	}
}

func bigMeta() map[string]string {
	entries := map[string]string{}
	for i := 0; i < 16; i++ {
		entries["n"+strconv.Itoa(10+i)] = strings.Repeat("v", 125)
	}
	return entries
}

func documentedSystemMeta() map[string]string {
	return map[string]string{
		"cache-control":       strings.Repeat("c", 62),
		"content-disposition": strings.Repeat("d", 62),
		"content-encoding":    strings.Repeat("e", 62),
		"content-language":    strings.Repeat("l", 62),
		"expires":             strings.Repeat("x", 62),
	}
}

func TestKeyOf(t *testing.T) {
	for _, op := range workedExampleOps(t) {
		key, ok := KeyOf(op)
		wantOK := op.Type == OpPut || op.Type == OpDel
		if ok != wantOK {
			t.Fatalf("KeyOf(%s) ok = %v, want %v", op.Type, ok, wantOK)
		}
		if ok && key != op.Key {
			t.Fatalf("KeyOf(%s) = %q, want %q", op.Type, key, op.Key)
		}
		if !ok && key != "" {
			t.Fatalf("KeyOf(%s) = %q, want empty", op.Type, key)
		}
	}
}

func randomKey(r *rand.Rand) string {
	alphabets := []string{"abcXYZ019-_.~", " /+%=|&?\n\t", "é€\U0001F600中"}
	length := 1 + r.Intn(300)
	var b strings.Builder
	for i := 0; i < length; i++ {
		alphabet := []rune(alphabets[r.Intn(len(alphabets))])
		next := alphabet[r.Intn(len(alphabet))]
		if b.Len()+utf8.RuneLen(next) > MaxKeyBytes {
			break
		}
		b.WriteRune(next)
	}
	return b.String()
}

func randomPrintable(r *rand.Rand, length int) string {
	var b strings.Builder
	for i := 0; i < length; i++ {
		b.WriteByte(byte(0x20 + r.Intn(0x5f)))
	}
	return b.String()
}

func randomMeta(r *rand.Rand, names []string) map[string]string {
	if r.Intn(3) == 0 {
		return nil
	}
	entries := map[string]string{}
	for _, name := range names {
		if r.Intn(2) == 0 {
			entries[name] = randomPrintable(r, 1+r.Intn(60))
		}
	}
	return entries
}

func TestFitsPutMatchesFormat(t *testing.T) {
	r := rand.New(rand.NewSource(20261113))
	userNames := []string{"a", "camera", "x-long-name-with-more-bytes", "tag.1", "q"}
	systemNames := []string{"cache-control", "content-disposition", "content-encoding", "content-language", "expires"}
	fitting := 0
	for i := 0; i < 3000; i++ {
		key := randomKey(r)
		contentType := ""
		if r.Intn(2) == 0 {
			contentType = randomPrintable(r, 1+r.Intn(400))
		}
		userMeta := randomMeta(r, userNames)
		systemMeta := randomMeta(r, systemNames)
		writer := "dev." + randomWriterName(r)
		recordLen, lenErr := PutRecordLen(key, contentType, userMeta, systemMeta, writer)
		fitsErr := FitsPut(key, contentType, userMeta, systemMeta, writer)
		if lenErr != nil {
			if !isMalformed(lenErr) || !isMalformed(fitsErr) {
				t.Fatalf("PutRecordLen error = %v, FitsPut error = %v", lenErr, fitsErr)
			}
			continue
		}
		if (recordLen <= MaxTextChars) != (fitsErr == nil) {
			t.Fatalf("FitsPut = %v but PutRecordLen = %d", fitsErr, recordLen)
		}
		if fitsErr != nil {
			continue
		}
		fitting++
		op := realPut(r, key, contentType, userMeta, systemMeta, writer)
		header, err := Format(op)
		if err != nil {
			t.Fatalf("Format failed after FitsPut passed: %v (key %q)", err, key)
		}
		if len(header) > MaxTextChars || len(header) > recordLen {
			t.Fatalf("real length %d, record length %d", len(header), recordLen)
		}
	}
	if fitting < 500 {
		t.Fatalf("only %d of 3000 random inputs fit; the generator is too wide", fitting)
	}
}

func randomWriterName(r *rand.Rand) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	length := 1 + r.Intn(32)
	var b strings.Builder
	for i := 0; i < length; i++ {
		b.WriteByte(alphabet[r.Intn(len(alphabet))])
	}
	return b.String()
}

func realPut(r *rand.Rand, key, contentType string, userMeta, systemMeta map[string]string, writer string) Op {
	if contentType == "" {
		contentType = DefaultContentType
	}
	stored := 1 + r.Int63n(MaxStoredBytes)
	op := Op{
		Type: OpPut, JobID: "4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f", Key: key,
		BodyMsgID: 1 + r.Int63n(MaxMsgID), StoredSize: stored, PlaintextSize: stored,
		ETag: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64),
		ContentType: contentType, UserMeta: userMeta, SystemMeta: systemMeta,
		Writer: writer, Timestamp: r.Int63n(MaxUnixTimestamp),
	}
	if r.Intn(2) == 0 {
		op.ETag += "-" + strconv.Itoa(1+r.Intn(10000))
	}
	if r.Intn(2) == 0 {
		op.Encrypted = true
		op.EncryptionVersion = 1
		op.KeyID = "437ad4b129840534"
		op.PlaintextSize = r.Int63n(1991808063)
	}
	return op
}
