package app

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	adminModule = "github.com/PRIEYAN/ButterDB/apps/admin"
	rootModule  = "github.com/PRIEYAN/ButterDB"
	adminAPI    = rootModule + "/api/adminv1"
	embeddedPkg = rootModule + "/internal/embedded"
	fyneModule  = "fyne.io/fyne/v2"
	imageX      = "golang.org/x/image"
)

func isStandardLibrary(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

func isFyne(importPath string) bool {
	return importPath == fyneModule || strings.HasPrefix(importPath, fyneModule+"/")
}

func inModule(importPath string) (string, bool) {
	if importPath == adminModule {
		return "", true
	}
	rel, ok := strings.CutPrefix(importPath, adminModule+"/")
	return rel, ok
}

func screenOf(pkg string) (string, bool) {
	parts := strings.Split(pkg, "/")
	if len(parts) >= 3 && parts[0] == "internal" && parts[1] == "ui" {
		return parts[2], true
	}
	return "", false
}

func isViewModelPackage(pkg string) bool {
	parts := strings.Split(pkg, "/")
	return len(parts) == 4 && parts[0] == "internal" && parts[1] == "ui" && parts[3] == "vm"
}

func importAllowed(pkg, importPath string) bool {
	if isStandardLibrary(importPath) {
		return true
	}
	rel, internal := inModule(importPath)
	if !internal && strings.HasPrefix(importPath, rootModule+"/") && importPath != adminAPI && importPath != embeddedPkg {
		return false
	}
	if pkg == "internal/app" || pkg == "." {
		return true
	}
	switch {
	case pkg == "internal/model":
		return importPath == adminAPI || importPath == embeddedPkg
	case isViewModelPackage(pkg):
		return importPath == adminAPI || importPath == embeddedPkg || rel == "internal/model"
	case pkg == "internal/shell":
		return rel == "internal/model" || importPath == adminAPI || isFyne(importPath)
	case pkg == "internal/media":
		return importPath == adminAPI
	case pkg == "internal/mediaservice":
		return rel == "internal/media" || rel == "internal/nativeplayer"
	case pkg == "internal/mediaqueue":
		return importPath == adminAPI || rel == "internal/mediaservice" || rel == "internal/nativeplayer"
	}
	if screen, ok := screenOf(pkg); ok && !strings.Contains(strings.TrimPrefix(pkg, "internal/ui/"+screen), "/") {
		return screenImportAllowed(pkg, screen, importPath, rel)
	}
	return false
}

func screenImportAllowed(pkg, screen, importPath, rel string) bool {
	switch {
	case importPath == adminAPI, isFyne(importPath):
		return true
	case rel == "internal/model", rel == "internal/shell", rel == "internal/ui/"+screen+"/vm":
		return true
	case screen == "objects" && (importPath == imageX || strings.HasPrefix(importPath, imageX+"/")):
		return true
	case screen == "replica" && rel == "internal/ui/telegramlink":
		return true
	}
	return false
}

func collectImports(t *testing.T, root string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(file, ".go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(file))
		if err != nil {
			return err
		}
		pkg := path.Clean(filepath.ToSlash(rel))
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			found[pkg] = append(found[pkg], importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	return found
}

func TestImportRules(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("module root: %v", err)
	}
	imports := collectImports(t, root)
	if len(imports) < 15 {
		t.Fatalf("only %d packages found under %s", len(imports), root)
	}
	for pkg, list := range imports {
		if strings.HasPrefix(pkg, "packaging") {
			continue
		}
		for _, importPath := range list {
			if !importAllowed(pkg, importPath) {
				t.Errorf("%s imports %s, which the module import table does not allow", pkg, importPath)
			}
		}
	}
}

func TestImportRulesRejectViolations(t *testing.T) {
	cases := []struct {
		name       string
		pkg        string
		importPath string
		want       bool
	}{
		{"model imports adminv1", "internal/model", adminAPI, true},
		{"model imports fyne", "internal/model", "fyne.io/fyne/v2/widget", false},
		{"vm imports fyne", "internal/ui/buckets/vm", "fyne.io/fyne/v2", false},
		{"vm imports model", "internal/ui/buckets/vm", adminModule + "/internal/model", true},
		{"vm imports shell", "internal/ui/buckets/vm", adminModule + "/internal/shell", false},
		{"screen imports own vm", "internal/ui/keys", adminModule + "/internal/ui/keys/vm", true},
		{"screen imports other vm", "internal/ui/keys", adminModule + "/internal/ui/buckets/vm", false},
		{"screen imports other screen", "internal/ui/keys", adminModule + "/internal/ui/buckets", false},
		{"replica imports telegramlink", "internal/ui/replica", adminModule + "/internal/ui/telegramlink", true},
		{"settings imports telegramlink", "internal/ui/settings", adminModule + "/internal/ui/telegramlink", false},
		{"screen imports root internal", "internal/ui/keys", rootModule + "/internal/core", false},
		{"app imports root internal", "internal/app", rootModule + "/internal/core", false},
		{"app imports every screen", "internal/app", adminModule + "/internal/ui/objects", true},
		{"shell imports fyne", "internal/shell", "fyne.io/fyne/v2/dialog", true},
		{"shell imports a screen", "internal/shell", adminModule + "/internal/ui/keys", false},
		{"vm imports embedded", "internal/ui/projects/vm", embeddedPkg, true},
		{"screen imports embedded", "internal/ui/projects", embeddedPkg, false},
		{"shell imports embedded", "internal/shell", embeddedPkg, false},
		{"vm imports keyring", "internal/ui/projects/vm", "github.com/zalando/go-keyring", false},
		{"objects imports image decoders", "internal/ui/objects", "golang.org/x/image/webp", true},
		{"keys imports image decoders", "internal/ui/keys", "golang.org/x/image/webp", false},
		{"anything imports stdlib", "internal/model", "encoding/json", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := importAllowed(tc.pkg, tc.importPath); got != tc.want {
				t.Fatalf("importAllowed(%s, %s) = %v, want %v", tc.pkg, tc.importPath, got, tc.want)
			}
		})
	}
}
