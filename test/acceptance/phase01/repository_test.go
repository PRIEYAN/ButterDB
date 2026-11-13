package phase01

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func scriptEnv(workspace bool) []string {
	env := []string{"GOTOOLCHAIN=local", "CGO_ENABLED=0"}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOWORK=") || strings.HasPrefix(entry, "GOTOOLCHAIN=") || strings.HasPrefix(entry, "CGO_ENABLED=") {
			continue
		}
		env = append(env, entry)
	}
	if !workspace {
		env = append(env, "GOWORK=off")
	}
	return env
}

func runScript(t *testing.T, dir string, workspace bool, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", args...)
	cmd.Dir = dir
	cmd.Env = scriptEnv(workspace)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return 0, out.String()
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), out.String()
	}
	t.Fatalf("bash %v: %v", args, err)
	return -1, ""
}

func TestLayeringAndNoCommentChecksEnforceRepositoryRules(t *testing.T) {
	root := repoRoot(t)

	t.Run("script fixtures pass", func(t *testing.T) {
		code, out := runScript(t, root, true, "scripts/test/run.sh")
		if code != 0 || strings.Contains(out, "FAIL ") {
			t.Fatalf("scripts/test/run.sh exit %d:\n%s", code, out)
		}
		for _, name := range []string{
			"layering_rejects_gotd_outside_tgclient",
			"layering_rejects_sqlite_outside_storage",
			"layering_rejects_domain_thirdparty",
			"nocomments_rejects_two_lines",
			"nocomments_rejects_block",
			"nocomments_allows_directives",
			"worktree_create_remove_roundtrip",
		} {
			if !strings.Contains(out, "PASS "+name) {
				t.Errorf("fixture case %s did not pass:\n%s", name, out)
			}
		}
	})

	t.Run("repository satisfies both checks", func(t *testing.T) {
		if code, out := runScript(t, root, false, "scripts/layering.sh"); code != 0 {
			t.Fatalf("layering.sh exit %d:\n%s", code, out)
		}
		if code, out := runScript(t, root, false, "scripts/nocomments.sh"); code != 0 {
			t.Fatalf("nocomments.sh exit %d:\n%s", code, out)
		}
	})

	t.Run("layering rejects a domain package importing another package", func(t *testing.T) {
		tree := t.TempDir()
		write(t, filepath.Join(tree, "go.mod"), "module github.com/PRIEYAN/ButterDB\n\ngo 1.26.0\n")
		write(t, filepath.Join(tree, "internal", "platform", "clock", "clock.go"), "package clock\n\nconst Name = \"clock\"\n")
		write(t, filepath.Join(tree, "internal", "domain", "bad", "bad.go"), "package bad\n\nimport \"github.com/PRIEYAN/ButterDB/internal/platform/clock\"\n\nconst Name = clock.Name\n")
		code, out := runScript(t, root, false, "scripts/layering.sh", tree)
		if code != 1 || !strings.Contains(out, "L1 violation") {
			t.Fatalf("exit %d, output %q; want exit 1 with an L1 violation", code, out)
		}
	})

	t.Run("nocomments rejects a two line comment and a block comment", func(t *testing.T) {
		tree := t.TempDir()
		write(t, filepath.Join(tree, "two.go"), "package x\n\n// first line\n// second line\nconst A = 1\n")
		write(t, filepath.Join(tree, "block.go"), "package x\n\n/* block */\nconst B = 2\n")
		code, out := runScript(t, root, false, "scripts/nocomments.sh", tree)
		if code != 1 || !strings.Contains(out, "two consecutive comment lines") || !strings.Contains(out, "block comment is not allowed") {
			t.Fatalf("exit %d, output %q", code, out)
		}
	})

	t.Run("nocomments accepts one line comments and directives", func(t *testing.T) {
		tree := t.TempDir()
		write(t, filepath.Join(tree, "ok.go"), "//go:build linux\n\npackage x\n\n// One external constraint.\nconst A = 1\n")
		if code, out := runScript(t, root, false, "scripts/nocomments.sh", tree); code != 0 {
			t.Fatalf("exit %d, output %q", code, out)
		}
	})
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
