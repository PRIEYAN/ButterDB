package phase01

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const inlineSecret = "0123456789abcdef0123456789abcdef"

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationRejectsUnknownAndUnsafeSecretValues(t *testing.T) {
	binary := buildDaemon(t)

	t.Run("unknown toml key", func(t *testing.T) {
		dir := privateDir(t)
		file := filepath.Join(t.TempDir(), "butterdb.toml")
		writeFile(t, file, "data_dir = \""+dir+"\"\nmystery_setting = 1\n", 0o600)
		got := runDaemon(t, binary, cleanEnv(), "check-config", "--config", file)
		if got.code != 2 || !strings.Contains(got.stdout, "mystery_setting") || !strings.Contains(got.stdout, "unknown key") {
			t.Fatalf("exit %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
		}
	})

	t.Run("unknown environment variable", func(t *testing.T) {
		got := runDaemon(t, binary, cleanEnv("BUTTERDB_MYSTERY_SETTING=1"), "check-config")
		if got.code != 2 || !strings.Contains(got.stdout, "BUTTERDB_MYSTERY_SETTING") {
			t.Fatalf("exit %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
		}
	})

	t.Run("inline secret in the file is refused and never echoed", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "butterdb.toml")
		writeFile(t, file, "server_key = \""+inlineSecret+"\"\n[telegram]\napi_hash = \""+inlineSecret+"\"\n", 0o600)
		got := runDaemon(t, binary, cleanEnv(), "check-config", "--config", file)
		if got.code != 2 {
			t.Fatalf("exit %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
		}
		if strings.Contains(got.stdout+got.stderr, inlineSecret) {
			t.Fatal("check-config echoed an inline secret value")
		}
	})

	t.Run("inline secret in the environment is refused and never echoed", func(t *testing.T) {
		got := runDaemon(t, binary, cleanEnv("BUTTERDB_SERVER_KEY="+inlineSecret, "BUTTERDB_TELEGRAM_API_HASH="+inlineSecret), "check-config", "--json")
		if got.code != 2 || !strings.Contains(got.stdout, `"ok":false`) {
			t.Fatalf("exit %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
		}
		if strings.Contains(got.stdout+got.stderr, inlineSecret) {
			t.Fatal("check-config echoed an inline secret value")
		}
	})

	cases := []struct {
		name     string
		mode     os.FileMode
		wantCode int
		wantText string
	}{
		{"private server key file passes", 0o600, 0, "0 errors"},
		{"other readable server key file fails", 0o644, 2, "mode 0644 allows group or other access"},
		{"group writable server key file fails", 0o660, 2, "mode 0660 allows group write or execute"},
		{"world accessible server key file fails", 0o666, 2, "mode 0666 allows group or other access"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := privateDir(t)
			key := filepath.Join(filepath.Dir(dir), "server.key")
			writeFile(t, key, strings.Repeat("k", 32), tc.mode)
			got := runDaemon(t, binary, cleanEnv("BUTTERDB_DATA_DIR="+dir, "BUTTERDB_SERVER_KEY_FILE="+key), "check-config")
			if got.code != tc.wantCode || !strings.Contains(got.stdout, tc.wantText) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want %d containing %q", got.code, got.stdout, got.stderr, tc.wantCode, tc.wantText)
			}
		})
	}

	t.Run("serve refuses an unsafe configuration with exit 2", func(t *testing.T) {
		got := runDaemon(t, binary, cleanEnv("BUTTERDB_MYSTERY_SETTING=1"), "serve")
		if got.code != 2 {
			t.Fatalf("exit %d, stderr %q", got.code, got.stderr)
		}
	})

	t.Run("loose data directory is refused", func(t *testing.T) {
		dir := privateDir(t)
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		got := runDaemon(t, binary, cleanEnv("BUTTERDB_DATA_DIR="+dir), "serve")
		if got.code != 2 {
			t.Fatalf("serve exit %d, stderr %q", got.code, got.stderr)
		}
	})
}
