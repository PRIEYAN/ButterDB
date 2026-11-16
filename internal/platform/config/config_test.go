package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "butterdb.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

func loadFile(t *testing.T, content string, environ ...string) (Config, []Finding, error) {
	t.Helper()
	return Load(LoadOptions{Path: writeConfigFile(t, content), PathExplicit: true, Environ: environ})
}

func findingFor(findings []Finding, key string) (Finding, bool) {
	for _, f := range findings {
		if f.Key == key {
			return f, true
		}
	}
	return Finding{}, false
}

func TestConfigRejectsUnknownKeyAndEnv(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		environ  []string
		wantKey  string
		wantFail bool
	}{
		{name: "unknown top level key", file: "bogus = 1\n", wantKey: "bogus", wantFail: true},
		{name: "unknown key in known table", file: "[s3]\nnope = 1\n", wantKey: "s3.nope", wantFail: true},
		{name: "unknown nested table", file: "[telegram.send_rate]\nburst_all = 3\n", wantKey: "telegram.send_rate.burst_all", wantFail: true},
		{name: "inline api hash key", file: "[telegram]\napi_hash = \"abcdef\"\n", wantKey: "telegram.api_hash", wantFail: true},
		{name: "inline server key key", file: "server_key = \"abcdef\"\n", wantKey: "server_key", wantFail: true},
		{name: "unknown environment variable", environ: []string{"BUTTERDB_NOPE=1"}, wantKey: "BUTTERDB_NOPE", wantFail: true},
		{name: "inline api hash environment", environ: []string{"BUTTERDB_TELEGRAM_API_HASH=abcdef"}, wantKey: "BUTTERDB_TELEGRAM_API_HASH", wantFail: true},
		{name: "inline server key environment", environ: []string{"BUTTERDB_SERVER_KEY=abcdef"}, wantKey: "BUTTERDB_SERVER_KEY", wantFail: true},
		{name: "config path variable is reserved", environ: []string{"BUTTERDB_CONFIG=/etc/butterdb/butterdb.toml"}},
		{name: "telegram test harness variable is reserved", environ: []string{"BUTTERDB_TGTEST_API_ID=1"}},
		{name: "s3 compat harness variable is reserved", environ: []string{"BUTTERDB_S3COMPAT_ENDPOINT=http://x"}},
		{name: "foreign variables are ignored", environ: []string{"HOME=/root", "PATH=/bin"}},
		{name: "wrong type", file: "[ingest]\nmax_concurrent_puts = \"four\"\n", wantKey: "ingest.max_concurrent_puts", wantFail: true},
		{name: "syntax error", file: "data_dir = \n", wantKey: "data_dir", wantFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, findings, err := loadFile(t, tc.file, tc.environ...)
			if !tc.wantFail {
				if err != nil || len(findings) != 0 {
					t.Fatalf("unexpected failure: err=%v findings=%v", err, findings)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			finding, ok := findingFor(findings, tc.wantKey)
			if !ok || finding.Severity != SeverityError {
				t.Fatalf("findings = %v, want an error for %q", findings, tc.wantKey)
			}
			if strings.Contains(finding.Message, "abcdef") {
				t.Fatalf("finding echoes the inline value: %q", finding.Message)
			}
		})
	}
}

func TestPrecedenceFileThenEnv(t *testing.T) {
	file := "data_dir = \"/srv/butter\"\n[s3]\nregion = \"eu-west-1\"\nlisten = \"127.0.0.1:9100\"\nrequest_budget = \"45s\"\n[ingest]\nmax_concurrent_puts = 8\n"
	environ := []string{
		"BUTTERDB_S3_REGION=ap-south-1",
		"BUTTERDB_INGEST_MAX_CONCURRENT_PUTS_PER_KEY=3",
		"BUTTERDB_S3_BASE_DOMAINS=s3.example.com,s3.example.org",
	}
	cfg, findings, err := loadFile(t, file, environ...)
	if err != nil || len(findings) != 0 {
		t.Fatalf("load: err=%v findings=%v", err, findings)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"env beats file", cfg.S3.Region, "ap-south-1"},
		{"file beats default", cfg.S3.Listen, "127.0.0.1:9100"},
		{"file duration", cfg.S3.RequestBudget, Duration(45 * time.Second)},
		{"file int", cfg.Ingest.MaxConcurrentPuts, 8},
		{"env int without file", cfg.Ingest.MaxConcurrentPutsPerKey, 3},
		{"env list", cfg.S3.BaseDomains, []string{"s3.example.com", "s3.example.org"}},
		{"default untouched", cfg.S3.BodyIdleTimeout, Duration(60 * time.Second)},
		{"data dir from file", cfg.DataDir, "/srv/butter"},
		{"default path follows data dir", cfg.Cache.Dir, "/srv/butter/cache"},
		{"default socket follows data dir", cfg.Admin.ControlSocket, "/srv/butter/admin.sock"},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
}

func TestEnvironmentDataDirMovesDefaultPaths(t *testing.T) {
	cfg, findings, err := Load(LoadOptions{Environ: []string{"BUTTERDB_DATA_DIR=/data"}})
	if err != nil || len(findings) != 0 {
		t.Fatalf("load: err=%v findings=%v", err, findings)
	}
	if cfg.Snapshot.Dir != "/data/snapshots" || cfg.Telegram.SessionFile != "/data/secrets/session_primary.json" {
		t.Fatalf("defaults did not follow data_dir: %q %q", cfg.Snapshot.Dir, cfg.Telegram.SessionFile)
	}
}

func TestExplicitPathOverridesDefaultPathAfterEnvironment(t *testing.T) {
	cfg, _, err := Load(LoadOptions{Environ: []string{"BUTTERDB_CACHE_DIR=/mnt/cache"}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Cache.Dir != "/mnt/cache" {
		t.Fatalf("cache dir = %q", cfg.Cache.Dir)
	}
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.toml")
	cases := []struct {
		name     string
		explicit bool
		wantFail bool
	}{
		{name: "default path may be absent", explicit: false},
		{name: "explicit path must exist", explicit: true, wantFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, err := Load(LoadOptions{Path: missing, PathExplicit: tc.explicit})
			if tc.wantFail {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(cfg, Default()) {
				t.Fatalf("err=%v config differs from defaults", err)
			}
		})
	}
}

type keyCase struct {
	key  string
	def  string
	bad  []string
	good []string
}

func keyCases() []keyCase {
	proxies := "127.0.0.1/32,::1/128"
	badDuration := []string{"0s", "-1s", "30", "abc"}
	badSize := []string{"0", "-1", "20GB", "1.5GiB", "abc"}
	badBool := []string{"yes", "1", "TRUE"}
	badAddress := []string{"nocolon", "host:0", "host:70000", "host:http"}
	optionalBad := []string{"relative/path", "~/key"}
	badPath := append([]string{""}, optionalBad...)
	return []keyCase{
		{"data_dir", "/var/lib/butterdb", badPath, []string{"/srv/x"}},
		{"server_key_file", "/etc/butterdb/server.key", badPath, nil},
		{"log.level", "info", []string{"trace", "INFO"}, []string{"debug", "warn", "error"}},
		{"log.format", "json", []string{"yaml"}, []string{"text"}},
		{"log.access_log", "true", badBool, []string{"false"}},
		{"log.object_keys", "full", []string{"partial"}, []string{"hash", "none"}},
		{"s3.listen", "0.0.0.0:9000", badAddress, []string{"[::]:9000", ":9000"}},
		{"s3.public_url", "", nil, nil},
		{"s3.region", "us-east-1", nil, nil},
		{"s3.base_domains", "", []string{"a,,b"}, []string{"s3.example.com"}},
		{"s3.tls_mode", "none", []string{"auto"}, []string{"files"}},
		{"s3.tls_cert_file", "", optionalBad, []string{""}},
		{"s3.tls_key_file", "", optionalBad, []string{""}},
		{"s3.trusted_proxies", proxies, []string{"a,,b"}, nil},
		{"s3.request_budget", "30s", badDuration, []string{"1m"}},
		{"s3.body_idle_timeout", "60s", badDuration, nil},
		{"s3.write_idle_timeout", "60s", badDuration, nil},
		{"s3.keepalive_interval", "15s", badDuration, nil},
		{"s3.completed_upload_retention", "24h", badDuration, nil},
		{"s3.multipart_idle_expiry", "168h", badDuration, nil},
		{"s3.auth_failure_limit", "100", []string{"0", "-5", "x"}, []string{"1"}},
		{"s3.cors_allowed_origins", "", []string{"a,,b"}, nil},
		{"s3.cors_allowed_methods", "GET,HEAD,PUT,POST,DELETE", []string{","}, nil},
		{"s3.cors_allowed_headers", "*", []string{"a,,b"}, nil},
		{"s3.cors_expose_headers", strings.Join(defaultExposeHeaders(), ","), []string{"a,,b"}, nil},
		{"s3.cors_max_age", "3600", []string{"-1", "x"}, []string{"0"}},
		{"s3.anonymous_rate", "50", []string{"0", "10001"}, []string{"1", "10000"}},
		{"s3.anonymous_burst", "100", []string{"0", "10001"}, []string{"1", "10000"}},
		{"ingest.max_concurrent_puts", "4", []string{"0", "65"}, []string{"1", "64"}},
		{"ingest.max_concurrent_puts_per_key", "2", []string{"0", "65"}, []string{"1", "64"}},
		{"admin.listen", "0.0.0.0:9443", badAddress, nil},
		{"admin.tls_mode", "self-signed", []string{"none"}, []string{"files", "proxy"}},
		{"admin.tls_cert_file", "", optionalBad, []string{""}},
		{"admin.tls_key_file", "", optionalBad, []string{""}},
		{"admin.hostnames", "", []string{"a,,b"}, nil},
		{"admin.trusted_proxies", proxies, []string{"a,,b"}, nil},
		{"admin.control_socket", "/var/lib/butterdb/admin.sock", badPath, nil},
		{"admin.setup_token_ttl", "24h", badDuration, nil},
		{"admin.pairing_code_ttl", "10m", badDuration, nil},
		{"admin.device_idle_revoke", "4320h", badDuration, nil},
		{"admin.upload_commit_budget", "10m", badDuration, nil},
		{"media.enabled", "false", badBool, []string{"true"}},
		{"media.listen", "0.0.0.0:9080", badAddress, nil},
		{"media.path_prefix", "", nil, nil},
		{"media.public_url", "", nil, nil},
		{"media.hostnames", "", []string{"a,,b"}, nil},
		{"media.trusted_proxies", proxies, []string{"a,,b"}, nil},
		{"media.signing_key_file", "/var/lib/butterdb/secrets/media_signing.key", badPath, nil},
		{"media.signing_key_previous_file", "", optionalBad, []string{""}},
		{"media.private_max_expiry", "168h", badDuration, nil},
		{"media.width_ladder", strings.Join(ladderStrings(defaultWidthLadder()), ","), []string{"", "5,3", "0,4", "4,4", "a"}, []string{"16,32"}},
		{"media.worker_mode", "spawn", []string{"inline"}, []string{"external"}},
		{"media.worker_path", "butterdb-transformd", nil, nil},
		{"media.worker_socket", "/var/lib/butterdb/run/transformd.sock", badPath, nil},
		{"media.worker_tmp_dir", "/var/lib/butterdb/run/transformd-tmp", badPath, nil},
		{"media.ffmpeg_path", "/usr/bin/ffmpeg", badPath, nil},
		{"media.worker_concurrency", strconv.Itoa(min(runtimeCPUs(), 4)), []string{"0", "-1"}, []string{"1"}},
		{"media.worker_memory_limit", "1GiB", badSize, []string{"512MiB"}},
		{"media.worker_sandbox", "auto", []string{"maybe"}, []string{"require", "off"}},
		{"media.queue_depth", "64", []string{"0"}, []string{"1"}},
		{"media.queue_timeout", "10s", badDuration, nil},
		{"media.source_timeout", "120s", badDuration, nil},
		{"media.image_timeout", "20s", badDuration, nil},
		{"media.poster_timeout", "30s", badDuration, nil},
		{"media.poison_ttl", "1h", badDuration, nil},
		{"media.negative_ttl", "10m", badDuration, nil},
		{"media.cache_max_bytes", "10GiB", badSize, nil},
		{"media.cache_memory_max_bytes", "128MiB", badSize, nil},
		{"media.cache_sweep_interval", "1h", badDuration, nil},
		{"media.max_image_source_bytes", "64MiB", badSize, nil},
		{"media.max_video_source_bytes", "1GiB", badSize, nil},
		{"media.max_source_pixels", "50000000", []string{"0"}, nil},
		{"media.max_source_side", "32768", []string{"0"}, nil},
		{"media.max_output_side", "4096", []string{"0"}, nil},
		{"media.max_output_pixels", "16777216", []string{"0"}, nil},
		{"media.max_output_bytes", "32MiB", badSize, nil},
		{"media.max_frames", "200", []string{"0"}, nil},
		{"media.max_animated_pixels", "200000000", []string{"0"}, nil},
		{"media.avif_auto_max_pixels", "4194304", []string{"0"}, nil},
		{"ops.listen", "127.0.0.1:9090", badAddress, nil},
		{"ops.hostnames", "", []string{"a,,b"}, nil},
		{"ops.metrics_token_file", "", optionalBad, []string{""}},
		{"ops.allow_unauthenticated_metrics", "false", badBool, []string{"true"}},
		{"ops.ready_when_degraded", "true", badBool, []string{"false"}},
		{"telegram.app_file", "/var/lib/butterdb/secrets/telegram_app.json", badPath, nil},
		{"telegram.session_file", "/var/lib/butterdb/secrets/session_primary.json", badPath, nil},
		{"telegram.replica_session_file", "/var/lib/butterdb/secrets/session_replica.json", badPath, nil},
		{"telegram.connect_timeout", "30s", badDuration, nil},
		{"telegram.device_model", "butterDB server", nil, nil},
		{"telegram.test_dc", "false", badBool, []string{"true"}},
		{"telegram.send_rate.control_per_minute", "20", []string{"0", "601"}, []string{"1", "600"}},
		{"telegram.send_rate.control_burst", "5", []string{"0", "101"}, []string{"1", "100"}},
		{"telegram.send_rate.documents_per_minute", "6", []string{"0", "121"}, []string{"1", "120"}},
		{"telegram.send_rate.documents_burst", "1", []string{"0", "21"}, []string{"1", "20"}},
		{"cache.dir", "/var/lib/butterdb/cache", badPath, nil},
		{"cache.blocks_max_bytes", "0", []string{"-1", "20GB"}, []string{"20GiB"}},
		{"cache.warm_on_put_max_bytes", "0", []string{"-1", "64GB"}, []string{"64MiB"}},
		{"cache.min_free_bytes", "2GiB", []string{"-1"}, []string{"0"}},
		{"spool.max_bytes", "8GiB", badSize, nil},
		{"spool.multipart_max_bytes", "16GiB", badSize, nil},
		{"spool.min_free_bytes", "1GiB", []string{"-1"}, []string{"0"}},
		{"snapshot.dir", "/var/lib/butterdb/snapshots", badPath, nil},
		{"snapshot.interval", "24h", []string{"-1s", "30", "abc"}, []string{"0s"}},
		{"snapshot.retain", "7", []string{"0", "366"}, []string{"1", "365"}},
		{"snapshot.upload_to_telegram", "false", badBool, []string{"true"}},
		{"snapshot.telegram_retain", "7", []string{"0", "366"}, []string{"1", "365"}},
		{"snapshot.before_migrate", "true", badBool, []string{"false"}},
		{"shutdown.drain_timeout", "10s", badDuration, nil},
		{"shutdown.component_timeout", "5s", badDuration, nil},
		{"shutdown.total_timeout", "60s", badDuration, nil},
	}
}

func ladderStrings(ladder []int) []string {
	out := make([]string, 0, len(ladder))
	for _, v := range ladder {
		out = append(out, strconv.Itoa(v))
	}
	return out
}

func runtimeCPUs() int {
	return Default().Media.WorkerConcurrency
}

func TestEveryKeyHasDefaultAndRange(t *testing.T) {
	cases := keyCases()
	fields := walkFields(&Config{})
	if len(cases) != len(fields) {
		t.Fatalf("table covers %d keys, config has %d", len(cases), len(fields))
	}
	defaults := Default()
	byKey := make(map[string]field)
	for _, f := range walkFields(&defaults) {
		byKey[f.key] = f
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			f, ok := byKey[tc.key]
			if !ok {
				t.Fatalf("no such key")
			}
			if f.env != "BUTTERDB_"+strings.ToUpper(strings.ReplaceAll(tc.key, ".", "_")) {
				t.Fatalf("env name %q", f.env)
			}
			var expected Config
			expectedField := fieldByKey(t, &expected, tc.key)
			if err := setFromText(expectedField, tc.def); err != nil {
				t.Fatalf("parse documented default %q: %v", tc.def, err)
			}
			if !reflect.DeepEqual(f.value.Interface(), expectedField.value.Interface()) {
				t.Fatalf("default = %v, documented %q", f.value.Interface(), tc.def)
			}
			assertLoads(t, f.env, tc.def, tc.key, false)
			for _, bad := range tc.bad {
				assertLoads(t, f.env, bad, tc.key, true)
			}
			for _, good := range tc.good {
				assertLoads(t, f.env, good, tc.key, false)
			}
		})
	}
}

func fieldByKey(t *testing.T, cfg *Config, key string) field {
	t.Helper()
	for _, f := range walkFields(cfg) {
		if f.key == key {
			return f
		}
	}
	t.Fatalf("no field %q", key)
	return field{}
}

func assertLoads(t *testing.T, env, value, key string, wantError bool) {
	t.Helper()
	_, findings, err := Load(LoadOptions{Environ: []string{env + "=" + value}})
	if wantError {
		if err == nil {
			t.Fatalf("%s=%q accepted", env, value)
		}
		if _, ok := findingFor(findings, key); !ok {
			t.Fatalf("%s=%q: findings %v lack key %q", env, value, findings, key)
		}
		return
	}
	if err != nil || len(findings) != 0 {
		t.Fatalf("%s=%q rejected: err=%v findings=%v", env, value, err, findings)
	}
}

func TestSecretsOnlyFromFiles(t *testing.T) {
	secretWords := []string{"secret", "password", "token", "api_hash", "session", "signing_key", "server_key", "tls_key"}
	t.Run("secret named string keys are file paths", func(t *testing.T) {
		for _, f := range walkFields(&Config{}) {
			if f.value.Kind().String() != "string" {
				continue
			}
			for _, word := range secretWords {
				if strings.Contains(f.key, word) && !strings.HasSuffix(f.key, "_file") {
					t.Fatalf("key %q looks secret but is not a *_file path", f.key)
				}
			}
		}
	})
	t.Run("file keys are typed as paths", func(t *testing.T) {
		for _, f := range walkFields(&Config{}) {
			if strings.HasSuffix(f.key, "_file") && f.kind != kindPath {
				t.Fatalf("key %q ends in _file but is not a path", f.key)
			}
		}
	})
	inline := []struct {
		name    string
		environ []string
		file    string
		key     string
	}{
		{name: "server key inline in environment", environ: []string{"BUTTERDB_SERVER_KEY_FILE=hunter2-inline"}, key: "server_key_file"},
		{name: "metrics token inline in environment", environ: []string{"BUTTERDB_OPS_METRICS_TOKEN_FILE=hunter2-inline"}, key: "ops.metrics_token_file"},
		{name: "tls key inline in file", file: "[admin]\ntls_key_file = \"hunter2-inline\"\n", key: "admin.tls_key_file"},
	}
	for _, tc := range inline {
		t.Run(tc.name, func(t *testing.T) {
			_, findings, err := loadFile(t, tc.file, tc.environ...)
			if err == nil {
				t.Fatalf("inline value accepted")
			}
			finding, ok := findingFor(findings, tc.key)
			if !ok {
				t.Fatalf("findings = %v", findings)
			}
			for _, f := range findings {
				if strings.Contains(f.Message, "hunter2") {
					t.Fatalf("finding echoes the value: %q", f.Message)
				}
			}
			if finding.Severity != SeverityError {
				t.Fatalf("severity = %s", finding.Severity)
			}
		})
	}
	t.Run("credentials directory expands in path keys", func(t *testing.T) {
		cfg, findings, err := Load(LoadOptions{Environ: []string{
			"CREDENTIALS_DIRECTORY=/run/credentials/butterdbd.service",
			"BUTTERDB_SERVER_KEY_FILE=${CREDENTIALS_DIRECTORY}/server.key",
		}})
		if err != nil || len(findings) != 0 {
			t.Fatalf("err=%v findings=%v", err, findings)
		}
		if cfg.ServerKeyFile != "/run/credentials/butterdbd.service/server.key" {
			t.Fatalf("server_key_file = %q", cfg.ServerKeyFile)
		}
	})
	t.Run("credentials directory unset is an error", func(t *testing.T) {
		_, findings, err := Load(LoadOptions{Environ: []string{"BUTTERDB_SERVER_KEY_FILE=${CREDENTIALS_DIRECTORY}/server.key"}})
		if err == nil {
			t.Fatalf("unset credentials directory accepted")
		}
		if _, ok := findingFor(findings, "server_key_file"); !ok {
			t.Fatalf("findings = %v", findings)
		}
	})
}

const exampleFile = `data_dir = "/var/lib/butterdb"
server_key_file = "${CREDENTIALS_DIRECTORY}/server.key"

[log]
level = "info"

[s3]
listen = "127.0.0.1:9000"
public_url = "https://s3.example.com"
region = "us-east-1"
base_domains = ["s3.example.com"]

[admin]
listen = "0.0.0.0:9443"
tls_mode = "self-signed"
hostnames = ["admin.example.com"]

[ops]
listen = "127.0.0.1:9090"

[telegram.send_rate]
control_per_minute = 20
documents_per_minute = 6

[cache]
blocks_max_bytes = "100GiB"

[snapshot]
interval = "24h"
upload_to_telegram = true
`

func TestExampleFileParses(t *testing.T) {
	cfg, findings, err := loadFile(t, exampleFile, "CREDENTIALS_DIRECTORY=/run/credentials/butterdbd.service")
	if err != nil || len(findings) != 0 {
		t.Fatalf("err=%v findings=%v", err, findings)
	}
	want := Default()
	want.ServerKeyFile = "/run/credentials/butterdbd.service/server.key"
	want.S3.Listen = "127.0.0.1:9000"
	want.S3.PublicURL = "https://s3.example.com"
	want.S3.BaseDomains = []string{"s3.example.com"}
	want.Admin.Hostnames = []string{"admin.example.com"}
	want.Cache.BlocksMaxBytes = 100 << 30
	want.Snapshot.UploadToTelegram = true
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("example config differs from defaults plus the documented values:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestExampleFileNeedsCredentialsDirectory(t *testing.T) {
	_, findings, err := loadFile(t, exampleFile)
	if err == nil {
		t.Fatalf("example accepted without CREDENTIALS_DIRECTORY")
	}
	if _, ok := findingFor(findings, "server_key_file"); !ok {
		t.Fatalf("findings = %v", findings)
	}
}

func TestDurationAndSizeParsing(t *testing.T) {
	durations := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "seconds", input: "30s", want: 30 * time.Second},
		{name: "hours", input: "4320h", want: 4320 * time.Hour},
		{name: "compound", input: "1h30m", want: 90 * time.Minute},
		{name: "zero with unit", input: "0s", want: 0},
		{name: "bare number", input: "30", wantErr: true},
		{name: "empty", input: "", wantErr: true},
		{name: "days are not Go syntax", input: "7d", wantErr: true},
		{name: "garbage", input: "soon", wantErr: true},
	}
	for _, tc := range durations {
		t.Run("duration "+tc.name, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalText([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted %q as %v", tc.input, d)
				}
				return
			}
			if err != nil || d.Std() != tc.want {
				t.Fatalf("got %v err=%v, want %v", d, err, tc.want)
			}
		})
	}
	sizes := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{name: "plain bytes", input: "21474836480", want: 21474836480},
		{name: "zero", input: "0", want: 0},
		{name: "kibibytes", input: "4KiB", want: 4096},
		{name: "mebibytes", input: "64MiB", want: 64 << 20},
		{name: "gibibytes", input: "20GiB", want: 20 << 30},
		{name: "tebibytes", input: "2TiB", want: 2 << 40},
		{name: "decimal suffix rejected", input: "20GB", wantErr: true},
		{name: "kilobyte suffix rejected", input: "4KB", wantErr: true},
		{name: "fraction rejected", input: "1.5GiB", wantErr: true},
		{name: "negative rejected", input: "-1", wantErr: true},
		{name: "plus sign rejected", input: "+1GiB", wantErr: true},
		{name: "space rejected", input: "20 GiB", wantErr: true},
		{name: "lowercase suffix rejected", input: "20gib", wantErr: true},
		{name: "suffix only", input: "GiB", wantErr: true},
		{name: "overflow", input: "9000000TiB", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}
	for _, tc := range sizes {
		t.Run("size "+tc.name, func(t *testing.T) {
			var s ByteSize
			err := s.UnmarshalText([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted %q as %v", tc.input, s)
				}
				return
			}
			if err != nil || s.Int64() != tc.want {
				t.Fatalf("got %v err=%v, want %d", s, err, tc.want)
			}
		})
	}
	t.Run("size accepts a TOML integer and a TOML string", func(t *testing.T) {
		cfg, findings, err := loadFile(t, "[cache]\nblocks_max_bytes = 21474836480\nmin_free_bytes = \"3GiB\"\n")
		if err != nil || len(findings) != 0 {
			t.Fatalf("err=%v findings=%v", err, findings)
		}
		if cfg.Cache.BlocksMaxBytes != 20<<30 || cfg.Cache.MinFreeBytes != 3<<30 {
			t.Fatalf("sizes = %d %d", cfg.Cache.BlocksMaxBytes, cfg.Cache.MinFreeBytes)
		}
	})
	t.Run("duration rejects a TOML integer without unit", func(t *testing.T) {
		_, _, err := loadFile(t, "[shutdown]\ndrain_timeout = 10\n")
		if err == nil {
			t.Fatalf("bare integer duration accepted")
		}
	})
}

func TestCacheDisabledByDefault(t *testing.T) {
	cfg := Default()
	if cfg.Cache.BlocksMaxBytes != 0 {
		t.Fatalf("blocks_max_bytes default = %d, want 0", cfg.Cache.BlocksMaxBytes)
	}
	if cfg.Cache.WarmOnPutMaxBytes != 0 {
		t.Fatalf("warm_on_put_max_bytes default = %d, want 0", cfg.Cache.WarmOnPutMaxBytes)
	}
	loaded, findings, err := loadFile(t, "")
	if err != nil || len(findings) != 0 {
		t.Fatalf("err=%v findings=%v", err, findings)
	}
	if loaded.Cache.BlocksMaxBytes != 0 || loaded.Cache.WarmOnPutMaxBytes != 0 {
		t.Fatalf("loaded cache sizes = %d %d, want 0 0", loaded.Cache.BlocksMaxBytes, loaded.Cache.WarmOnPutMaxBytes)
	}
}

func TestCacheBlocksMaxBytesZeroIsValid(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		want      int64
		wantError bool
	}{
		{name: "zero integer", content: "[cache]\nblocks_max_bytes = 0\n", want: 0},
		{name: "zero string", content: "[cache]\nblocks_max_bytes = \"0\"\n", want: 0},
		{name: "positive opt in", content: "[cache]\nblocks_max_bytes = \"1GiB\"\n", want: 1 << 30},
		{name: "negative rejected", content: "[cache]\nblocks_max_bytes = -1\n", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, findings, err := loadFile(t, tc.content)
			if tc.wantError {
				if err == nil {
					t.Fatal("negative blocks_max_bytes accepted")
				}
				if _, ok := findingFor(findings, "cache.blocks_max_bytes"); !ok {
					t.Fatalf("findings %v lack cache.blocks_max_bytes", findings)
				}
				return
			}
			if err != nil || len(findings) != 0 {
				t.Fatalf("err=%v findings=%v", err, findings)
			}
			if cfg.Cache.BlocksMaxBytes.Int64() != tc.want {
				t.Fatalf("blocks_max_bytes = %d, want %d", cfg.Cache.BlocksMaxBytes, tc.want)
			}
		})
	}
}
