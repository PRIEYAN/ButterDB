package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
)

func TestCheckConfigSecretFilePermissions(t *testing.T) {
	const key = "server_key_file"
	const path = "/secrets/server.key"
	self := identity{UID: 1000, GID: 1000}
	regular := func(perm fs.FileMode, uid, gid uint32) fileMeta {
		return fileMeta{Mode: perm, Size: serverKeyBytes, UID: uid, GID: gid, OwnerKnown: true}
	}
	cases := []struct {
		name         string
		metas        map[string]fileMeta
		links        map[string]string
		wantSeverity config.Severity
		wantMessage  string
	}{
		{"private file is clean", map[string]fileMeta{path: regular(0o600, 1000, 1000)}, nil, "", ""},
		{"read only owner file is clean", map[string]fileMeta{path: regular(0o400, 1000, 1000)}, nil, "", ""},
		{"root owned private file is clean", map[string]fileMeta{path: regular(0o600, 0, 0)}, nil, "", ""},
		{"other readable is an error", map[string]fileMeta{path: regular(0o604, 1000, 1000)}, nil, config.SeverityError, "mode 0604 allows group or other access"},
		{"world readable and group readable is an error", map[string]fileMeta{path: regular(0o644, 1000, 1000)}, nil, config.SeverityError, "mode 0644 allows group or other access"},
		{"group writable is an error", map[string]fileMeta{path: regular(0o620, 1000, 1000)}, nil, config.SeverityError, "allows group write or execute"},
		{"group executable is an error", map[string]fileMeta{path: regular(0o610, 1000, 1000)}, nil, config.SeverityError, "allows group write or execute"},
		{"group read in another group is an error", map[string]fileMeta{path: regular(0o640, 0, 4242)}, nil, config.SeverityError, "group other than the process group"},
		{"group read owned by the process user is an error", map[string]fileMeta{path: regular(0o640, 1000, 1000)}, nil, config.SeverityError, "group other than the process group"},
		{"group read root owned in the process group is a warning", map[string]fileMeta{path: regular(0o640, 0, 1000)}, nil, config.SeverityWarning, "group readable"},
		{"foreign owner is an error", map[string]fileMeta{path: regular(0o600, 4242, 4242)}, nil, config.SeverityError, "neither the process user nor root"},
		{"directory is an error", map[string]fileMeta{path: {Mode: fs.ModeDir | 0o700, OwnerKnown: true, UID: 1000}}, nil, config.SeverityError, "not a regular file"},
		{"symlink target is checked", map[string]fileMeta{path: {Mode: fs.ModeSymlink | 0o777}, "/vault/key": regular(0o644, 1000, 1000)}, map[string]string{path: "/vault/key"}, config.SeverityError, "/vault/key: mode 0644"},
		{"symlink to a private target is clean", map[string]fileMeta{path: {Mode: fs.ModeSymlink | 0o777}, "/vault/key": regular(0o600, 1000, 1000)}, map[string]string{path: "/vault/key"}, "", ""},
		{"relative symlink resolves beside the link", map[string]fileMeta{path: {Mode: fs.ModeSymlink | 0o777}, "/secrets/real.key": regular(0o640, 0, 7)}, map[string]string{path: "real.key"}, config.SeverityError, "/secrets/real.key"},
		{"symlink chain is an error", map[string]fileMeta{path: {Mode: fs.ModeSymlink | 0o777}, "/a": {Mode: fs.ModeSymlink | 0o777}}, map[string]string{path: "/a", "/a": "/b"}, config.SeverityError, "symlink target is itself a symlink"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := secretPolicy{files: fakeFiles{metas: tc.metas, links: tc.links}, ids: self}
			file, found := policy.inspect(key, path)
			if tc.wantSeverity == "" {
				if len(found) != 0 {
					t.Fatalf("findings %+v, want none", found)
				}
				if !file.Exists {
					t.Fatal("file reported as missing")
				}
				return
			}
			if len(found) != 1 {
				t.Fatalf("findings %+v, want exactly one", found)
			}
			if found[0].Severity != tc.wantSeverity || found[0].Key != key || !strings.Contains(found[0].Message, tc.wantMessage) {
				t.Fatalf("finding %+v, want %s containing %q", found[0], tc.wantSeverity, tc.wantMessage)
			}
		})
	}

	t.Run("missing file reports nothing", func(t *testing.T) {
		policy := secretPolicy{files: fakeFiles{}, ids: self}
		file, found := policy.inspect(key, path)
		if file.Exists || len(found) != 0 {
			t.Fatalf("file %+v findings %+v", file, found)
		}
	})
}

func runCheck(t *testing.T, environ []string, args ...string) (int, testApp) {
	t.Helper()
	ta := newTestApp(environ)
	code := ta.app.run(context.Background(), append([]string{"check-config"}, args...))
	return code, ta
}

func TestCheckConfigRealSecretFileModes(t *testing.T) {
	cases := []struct {
		name     string
		mode     fs.FileMode
		wantCode int
		wantLine string
	}{
		{"private key passes", 0o600, exitOK, "0 errors"},
		{"other readable key fails", 0o644, exitUsage, "mode 0644 allows group or other access"},
		{"group writable key fails", 0o660, exitUsage, "allows group write or execute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := privateTempDir(t)
			keyPath := writeKeyFile(t, filepath.Dir(dir), tc.mode)
			code, ta := runCheck(t, []string{"BUTTERDB_DATA_DIR=" + dir, "BUTTERDB_SERVER_KEY_FILE=" + keyPath})
			if code != tc.wantCode || !strings.Contains(ta.stdout.String(), tc.wantLine) {
				t.Fatalf("exit %d, stdout %q; want %d containing %q", code, ta.stdout.String(), tc.wantCode, tc.wantLine)
			}
		})
	}
}

func TestCheckConfigKeyFileLength(t *testing.T) {
	dir := privateTempDir(t)
	keyPath := filepath.Join(filepath.Dir(dir), "short.key")
	if err := os.WriteFile(keyPath, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, ta := runCheck(t, []string{"BUTTERDB_DATA_DIR=" + dir, "BUTTERDB_SERVER_KEY_FILE=" + keyPath})
	if code != exitUsage || !strings.Contains(ta.stdout.String(), "must hold exactly 32") {
		t.Fatalf("exit %d stdout %q", code, ta.stdout.String())
	}
}

func TestCheckConfigMissingKeyFile(t *testing.T) {
	dir := privateTempDir(t)
	missing := filepath.Join(filepath.Dir(dir), "absent.key")
	environ := []string{"BUTTERDB_DATA_DIR=" + dir, "BUTTERDB_SERVER_KEY_FILE=" + missing}
	code, ta := runCheck(t, environ)
	if code != exitOK || !strings.Contains(ta.stdout.String(), "serve generates it on first start") {
		t.Fatalf("no state.json: exit %d stdout %q", code, ta.stdout.String())
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, ta = runCheck(t, environ)
	if code != exitUsage || !strings.Contains(ta.stdout.String(), "state.json shows an existing deployment") {
		t.Fatalf("with state.json: exit %d stdout %q", code, ta.stdout.String())
	}
}

func TestCheckConfigRejectsUnknownKeysAndEnvironment(t *testing.T) {
	dir := privateTempDir(t)
	configFile := filepath.Join(filepath.Dir(dir), "butterdb.toml")
	if err := os.WriteFile(configFile, []byte("data_dir = \""+dir+"\"\nmystery = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, ta := runCheck(t, nil, "--config", configFile)
	if code != exitUsage || !strings.Contains(ta.stdout.String(), "unknown key") {
		t.Fatalf("unknown key: exit %d stdout %q", code, ta.stdout.String())
	}
	code, ta = runCheck(t, []string{"BUTTERDB_MYSTERY=1"})
	if code != exitUsage || !strings.Contains(ta.stdout.String(), "unknown environment variable") {
		t.Fatalf("unknown variable: exit %d stdout %q", code, ta.stdout.String())
	}
	code, ta = runCheck(t, nil, "--config", filepath.Join(dir, "missing.toml"))
	if code != exitUsage || !strings.Contains(ta.stdout.String(), "cannot read configuration file") {
		t.Fatalf("missing explicit file: exit %d stdout %q", code, ta.stdout.String())
	}
}

func TestCheckConfigConfigPathFromEnvironment(t *testing.T) {
	dir := privateTempDir(t)
	configFile := filepath.Join(filepath.Dir(dir), "butterdb.toml")
	if err := os.WriteFile(configFile, []byte("surprise = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, ta := runCheck(t, []string{"BUTTERDB_CONFIG=" + configFile})
	if code != exitUsage || !strings.Contains(ta.stdout.String(), "surprise") {
		t.Fatalf("exit %d stdout %q", code, ta.stdout.String())
	}
}

func TestCheckConfigLooseDataDir(t *testing.T) {
	dir := privateTempDir(t)
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	keyPath := writeKeyFile(t, filepath.Dir(dir), 0o600)
	code, ta := runCheck(t, []string{"BUTTERDB_DATA_DIR=" + dir, "BUTTERDB_SERVER_KEY_FILE=" + keyPath})
	if code != exitUsage || !strings.Contains(ta.stdout.String(), "must be 0700") {
		t.Fatalf("exit %d stdout %q", code, ta.stdout.String())
	}
}

func TestCheckConfigNeverWrites(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "newdata")
	keyPath := writeKeyFile(t, parent, 0o600)
	before, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	code, ta := runCheck(t, []string{"BUTTERDB_DATA_DIR=" + dir, "BUTTERDB_SERVER_KEY_FILE=" + keyPath})
	if code != exitOK {
		t.Fatalf("exit %d stdout %q", code, ta.stdout.String())
	}
	after, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("check-config changed %q: %d entries before, %d after", parent, len(before), len(after))
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("check-config created the data directory: %v", err)
	}
}

func TestCheckConfigOutputFormats(t *testing.T) {
	dir := privateTempDir(t)
	keyPath := writeKeyFile(t, filepath.Dir(dir), 0o644)
	environ := []string{"BUTTERDB_DATA_DIR=" + dir, "BUTTERDB_SERVER_KEY_FILE=" + keyPath}

	code, ta := runCheck(t, environ)
	if code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(ta.stdout.String()), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "check-config: 1 error, ") || !strings.HasSuffix(last, " warnings") {
		t.Fatalf("summary line %q", last)
	}
	if !strings.HasPrefix(lines[0], "warning ") || !strings.Contains(ta.stdout.String(), "\nerror   server_key_file") {
		t.Fatalf("text output %q", ta.stdout.String())
	}

	code, ta = runCheck(t, environ, "--json")
	if code != exitUsage {
		t.Fatalf("json exit %d", code)
	}
	var report jsonReport
	if err := json.Unmarshal(ta.stdout.buf.Bytes(), &report); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if report.OK || len(report.Findings) == 0 {
		t.Fatalf("report %+v", report)
	}
	for _, f := range report.Findings {
		if f.Severity == "" || f.Key == "" || f.Message == "" {
			t.Fatalf("incomplete finding %+v", f)
		}
	}
}

func TestConfigRules(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(*config.Config)
		wantKey      string
		wantSeverity config.Severity
		wantMessage  string
	}{
		{"defaults warn about public url only", func(*config.Config) {}, "s3.public_url", config.SeverityWarning, "presigned URLs are unavailable"},
		{"s3 and admin share a port", func(c *config.Config) { c.Admin.Listen = "0.0.0.0:9000" }, "admin.listen", config.SeverityError, "overlaps s3.listen"},
		{"specific host overlaps wildcard", func(c *config.Config) { c.Ops.Listen = "127.0.0.1:9000" }, "ops.listen", config.SeverityError, "overlaps s3.listen"},
		{"different hosts do not overlap", func(c *config.Config) { c.S3.Listen = "127.0.0.1:9100"; c.Ops.Listen = "127.0.0.2:9100" }, "", "", ""},
		{"media listener only counts when enabled", func(c *config.Config) { c.Media.Listen = "0.0.0.0:9000" }, "", "", ""},
		{"enabled media listener overlaps", func(c *config.Config) { c.Media.Enabled = true; c.Media.Listen = "0.0.0.0:9000" }, "media.listen", config.SeverityError, "overlaps s3.listen"},
		{"public url without scheme", func(c *config.Config) { c.S3.PublicURL = "s3.example.com" }, "s3.public_url", config.SeverityError, "absolute http or https URL"},
		{"public url with query", func(c *config.Config) { c.S3.PublicURL = "https://s3.example.com/?a=b" }, "s3.public_url", config.SeverityError, "without query or fragment"},
		{"public url ftp", func(c *config.Config) { c.S3.PublicURL = "ftp://s3.example.com" }, "s3.public_url", config.SeverityError, "http or https"},
		{"bad base domain", func(c *config.Config) { c.S3.BaseDomains = []string{"bad_domain.example"} }, "s3.base_domains", config.SeverityError, "not a valid DNS name"},
		{"bad region", func(c *config.Config) { c.S3.Region = "US East" }, "s3.region", config.SeverityError, "^[a-z0-9-]{1,32}$"},
		{"short request budget", func(c *config.Config) { c.S3.RequestBudget = config.Duration(4 * time.Second) }, "s3.request_budget", config.SeverityError, "at least 5s"},
		{"bad cors origin", func(c *config.Config) { c.S3.CORSAllowedOrigins = []string{"example.com/path"} }, "s3.cors_allowed_origins", config.SeverityError, "scheme://host"},
		{"cors star is fine", func(c *config.Config) { c.S3.CORSAllowedOrigins = []string{"*", "https://app.example.com:8443"} }, "", "", ""},
		{"s3 files mode needs both files", func(c *config.Config) { c.S3.TLSMode = "files" }, "s3.tls_cert_file", config.SeverityError, "required when tls_mode is files"},
		{"admin files mode needs both files", func(c *config.Config) { c.Admin.TLSMode = "files" }, "admin.tls_key_file", config.SeverityError, "required when tls_mode is files"},
		{"admin proxy mode needs loopback", func(c *config.Config) { c.Admin.TLSMode = "proxy" }, "admin.listen", config.SeverityError, "loopback"},
		{"admin proxy mode on loopback is fine", func(c *config.Config) { c.Admin.TLSMode = "proxy"; c.Admin.Listen = "127.0.0.1:9443" }, "", "", ""},
		{"long control socket", func(c *config.Config) { c.Admin.ControlSocket = "/" + strings.Repeat("a", 110) }, "admin.control_socket", config.SeverityError, "limit is 103"},
		{"ops not loopback without token", func(c *config.Config) { c.Ops.Listen = "0.0.0.0:9090" }, "ops.listen", config.SeverityError, "metrics_token_file"},
		{"ops not loopback with token", func(c *config.Config) {
			c.Ops.Listen = "0.0.0.0:9090"
			c.Ops.MetricsTokenFile = "/etc/butterdb/metrics.token"
		}, "", "", ""},
		{"ops not loopback explicitly open", func(c *config.Config) { c.Ops.Listen = "0.0.0.0:9090"; c.Ops.AllowUnauthenticatedMetrics = true }, "", "", ""},
		{"test dc warns", func(c *config.Config) { c.Telegram.TestDC = true }, "telegram.test_dc", config.SeverityWarning, "development only"},
		{"media bad prefix", func(c *config.Config) { c.Media.Enabled = true; c.Media.PathPrefix = "Media/" }, "media.path_prefix", config.SeverityError, "^(/[a-z0-9-]+)+$"},
		{"media loopback without hostnames", func(c *config.Config) { c.Media.Enabled = true; c.Media.Listen = "127.0.0.1:9080" }, "media.listen", config.SeverityError, "media.hostnames"},
		{"media ladder not ascending", func(c *config.Config) {
			c.Media.Enabled = true
			c.Media.WidthLadder = []int{100, 100, c.Media.MaxOutputSide}
		}, "media.width_ladder", config.SeverityError, "strictly ascending"},
		{"media ladder end mismatch", func(c *config.Config) { c.Media.Enabled = true; c.Media.WidthLadder = []int{100, 200} }, "media.width_ladder", config.SeverityError, "max_output_side"},
		{"media sandbox off warns", func(c *config.Config) {
			c.Media.Enabled = true
			c.Media.WorkerMode = "spawn"
			c.Media.WorkerSandbox = "off"
		}, "media.worker_sandbox", config.SeverityWarning, "no sandbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.mutate(&cfg)
			found := configRules(cfg)
			if tc.wantKey == "" {
				for _, f := range found {
					if f.Severity == config.SeverityError {
						t.Fatalf("unexpected error finding %+v", f)
					}
				}
				return
			}
			for _, f := range found {
				if f.Key == tc.wantKey && f.Severity == tc.wantSeverity && strings.Contains(f.Message, tc.wantMessage) {
					return
				}
			}
			t.Fatalf("findings %+v lack %s %s containing %q", found, tc.wantSeverity, tc.wantKey, tc.wantMessage)
		})
	}
}
