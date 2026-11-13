package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
)

func jsonConfig(level string) config.LogConfig {
	cfg := config.Default().Log
	cfg.Level = level
	return cfg
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("line is not JSON: %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestLogStandardFields(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, jsonConfig("info")).With(KeyComponent, "s3api")
	logger.Info("request",
		KeyListener, "s3",
		KeyRequestID, "9f2c41d07ab36e15",
		KeyMethod, "PUT",
		KeyOperation, "PutObject",
		KeyBucket, "photos",
		KeyObjectKey, "2026/11/cat.jpg",
		KeyStatus, 200,
		KeyBytesIn, 734003,
		KeyBytesOut, 0,
		KeyDurationMS, 8412,
		KeyRemoteAddr, "203.0.113.7",
		KeyAccessKeyID, "BDBK7Q2M9X4T1W8Z3R6Y",
	)
	lines := decodeLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	line := lines[0]
	timePattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	if text, _ := line["time"].(string); !timePattern.MatchString(text) {
		t.Fatalf("time = %v, want RFC 3339 UTC with milliseconds", line["time"])
	}
	wantText := map[string]string{
		"level": "INFO", "msg": "request", "component": "s3api", "listener": "s3",
		"request_id": "9f2c41d07ab36e15", "method": "PUT", "operation": "PutObject",
		"bucket": "photos", "key": "2026/11/cat.jpg", "remote_addr": "203.0.113.7",
		"access_key_id": "BDBK7Q2M9X4T1W8Z3R6Y",
	}
	for key, want := range wantText {
		if line[key] != want {
			t.Errorf("%s = %v, want %q", key, line[key], want)
		}
	}
	wantNumbers := map[string]float64{"status": 200, "bytes_in": 734003, "bytes_out": 0, "duration_ms": 8412}
	for key, want := range wantNumbers {
		if line[key] != want {
			t.Errorf("%s = %v (%T), want number %v", key, line[key], line[key], want)
		}
	}
}

func hasMessage(output, message string) bool {
	return strings.Contains(output, "msg="+message+"\n") || strings.Contains(output, `"msg":"`+message+`"`)
}

func TestLogLevelsAndFormat(t *testing.T) {
	cases := []struct {
		name      string
		level     string
		format    string
		debugSeen bool
		warnSeen  bool
	}{
		{name: "info hides debug", level: "info", format: "json", warnSeen: true},
		{name: "debug shows debug", level: "debug", format: "json", debugSeen: true, warnSeen: true},
		{name: "error hides warn", level: "error", format: "json"},
		{name: "unknown level falls back to info", level: "loud", format: "json", warnSeen: true},
		{name: "text format", level: "info", format: "text", warnSeen: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			cfg := jsonConfig(tc.level)
			cfg.Format = tc.format
			logger := New(&buf, cfg)
			logger.Debug("d")
			logger.Warn("w")
			if got := hasMessage(buf.String(), "d"); got != tc.debugSeen {
				t.Fatalf("debug seen = %v, want %v: %q", got, tc.debugSeen, buf.String())
			}
			if got := hasMessage(buf.String(), "w"); got != tc.warnSeen {
				t.Fatalf("warn seen = %v, want %v: %q", got, tc.warnSeen, buf.String())
			}
			if tc.format == "json" && buf.Len() > 0 {
				decodeLines(t, &buf)
			}
		})
	}
}

type sessionKey struct {
	material []byte
}

func (sessionKey) LogValue() slog.Value {
	return slog.StringValue("[redacted]")
}

type loginRequest struct {
	Phone    Phone
	APIHash  Secret
	Attempts int
}

func (r loginRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("phone", r.Phone),
		slog.Any("api_hash_value", r.APIHash),
		slog.Int("attempts", r.Attempts),
	)
}

func TestRedactionByType(t *testing.T) {
	const marker = "s3cr3t-marker-value"
	cases := []struct {
		name string
		log  func(*slog.Logger)
	}{
		{name: "secret attribute", log: func(l *slog.Logger) { l.Info("x", "server_key", Secret(marker)) }},
		{name: "secret in group", log: func(l *slog.Logger) { l.Info("x", slog.Group("auth", slog.Any("value", Secret(marker)))) }},
		{name: "secret on logger with", log: func(l *slog.Logger) { l.With("dek", Secret(marker)).Info("x") }},
		{name: "secret inside LogValuer struct", log: func(l *slog.Logger) { l.Info("x", "login", loginRequest{APIHash: Secret(marker)}) }},
		{name: "custom key type", log: func(l *slog.Logger) { l.Info("x", "k", sessionKey{material: []byte(marker)}) }},
		{name: "denylisted key password", log: func(l *slog.Logger) { l.Info("x", "password", marker) }},
		{name: "denylisted key mixed case", log: func(l *slog.Logger) { l.Info("x", "Authorization", marker) }},
		{name: "denylisted key api_hash", log: func(l *slog.Logger) { l.Info("x", "api_hash", marker) }},
		{name: "denylisted key session", log: func(l *slog.Logger) { l.Info("x", "session", marker) }},
		{name: "denylisted key in group", log: func(l *slog.Logger) { l.Info("x", slog.Group("tg", slog.String("code", marker))) }},
		{name: "denylisted key signature", log: func(l *slog.Logger) { l.Info("x", "signature", marker) }},
		{name: "denylisted key key_material", log: func(l *slog.Logger) { l.Info("x", "key_material", marker) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.log(New(&buf, jsonConfig("info")))
			if strings.Contains(buf.String(), marker) {
				t.Fatalf("secret leaked: %s", buf.String())
			}
			if !strings.Contains(buf.String(), "[redacted]") {
				t.Fatalf("no redaction marker: %s", buf.String())
			}
		})
	}
	t.Run("secret formats redacted under fmt verbs", func(t *testing.T) {
		secret := Secret(marker)
		for _, text := range []string{secret.String(), secret.GoString()} {
			if strings.Contains(text, marker) {
				t.Fatalf("leaked through formatting: %q", text)
			}
		}
	})
	t.Run("allowed keys pass untouched", func(t *testing.T) {
		var buf bytes.Buffer
		New(&buf, jsonConfig("info")).Info("x", "setup_token", "visible-setup", "error_code", "NoSuchKey", "bucket", "photos")
		out := buf.String()
		for _, want := range []string{"visible-setup", "NoSuchKey", "photos"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%q missing from %s", want, out)
			}
		}
	})
}

func TestPhoneMasked(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "united kingdom", input: "+447911123134", want: "+44*******134"},
		{name: "north america", input: "+14155552671", want: "+1*******671"},
		{name: "india", input: "+919876543210", want: "+91*******210"},
		{name: "three digit code", input: "+351912345678", want: "+351******678"},
		{name: "russia", input: "+79161234567", want: "+7*******567"},
		{name: "no plus sign", input: "447911123134", want: "+44*******134"},
		{name: "separators ignored", input: "+44 7911 123 134", want: "+44*******134"},
		{name: "too short", input: "+1234", want: "[redacted]"},
		{name: "empty", input: "", want: "[redacted]"},
		{name: "letters only", input: "abc", want: "[redacted]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaskPhone(tc.input); got != tc.want {
				t.Fatalf("MaskPhone(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
	t.Run("phone type masks in log output", func(t *testing.T) {
		var buf bytes.Buffer
		New(&buf, jsonConfig("info")).Info("login", "phone", Phone("+447911123134"), "request", loginRequest{Phone: "+14155552671"})
		out := buf.String()
		for _, leaked := range []string{"447911123134", "7911123", "4155552671", "+14155552671"} {
			if strings.Contains(out, leaked) {
				t.Fatalf("full phone number leaked (%q): %s", leaked, out)
			}
		}
		if !strings.Contains(out, "+44*******134") || !strings.Contains(out, "+1*******671") {
			t.Fatalf("masked forms missing: %s", out)
		}
	})
}
