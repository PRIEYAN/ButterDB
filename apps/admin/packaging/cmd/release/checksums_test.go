package main

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const metadataFixture = `Website = "https://example.invalid"

[Details]
Icon = "packaging/icon.png"
Name = "butterDB Admin"
ID = "io.github.prieyan.butterdb.admin"
Version = "0.1.0"
Build = 1
`

type fakeRunner struct {
	commands    []command
	described   string
	describeErr error
	onRun       func(cmd command) error
}

func (f *fakeRunner) Run(_ context.Context, cmd command) error {
	f.commands = append(f.commands, cmd)
	if f.onRun == nil {
		return nil
	}
	return f.onRun(cmd)
}

func (f *fakeRunner) Output(context.Context, command) (string, error) {
	return f.described, f.describeErr
}

func newAppDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fyneMetadataFile), []byte(metadataFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newReleaser(t *testing.T, runner commandRunner) (*Releaser, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	releaser, err := NewReleaser(Dependencies{Runner: runner, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	return releaser, &out
}

func nativeConfig(appDir string, goos string, goarch string) Config {
	return Config{AppDir: appDir, DistDir: defaultDistDir, Version: "0.1.0", OS: goos, Arch: goarch, HostOS: goos, HostArch: goarch}
}

func TestArtifactNames(t *testing.T) {
	cases := []struct {
		name    string
		version string
		goos    string
		goarch  string
		want    string
		wantErr bool
	}{
		{name: "linux amd64", version: "0.1.0", goos: "linux", goarch: "amd64", want: "butterdb-admin_0.1.0_linux_amd64.tar.xz"},
		{name: "linux arm64", version: "0.1.0", goos: "linux", goarch: "arm64", want: "butterdb-admin_0.1.0_linux_arm64.tar.xz"},
		{name: "windows amd64", version: "1.20.3", goos: "windows", goarch: "amd64", want: "butterdb-admin_1.20.3_windows_amd64.zip"},
		{name: "darwin arm64", version: "0.1.0", goos: "darwin", goarch: "arm64", want: "butterdb-admin_0.1.0_darwin_arm64.zip"},
		{name: "darwin amd64", version: "0.1.0", goos: "darwin", goarch: "amd64", want: "butterdb-admin_0.1.0_darwin_amd64.zip"},
		{name: "windows arm64 is not a target", version: "0.1.0", goos: "windows", goarch: "arm64", wantErr: true},
		{name: "unknown os", version: "0.1.0", goos: "plan9", goarch: "amd64", wantErr: true},
		{name: "unknown architecture", version: "0.1.0", goos: "linux", goarch: "386", wantErr: true},
		{name: "version with v prefix", version: "v0.1.0", goos: "linux", goarch: "amd64", wantErr: true},
		{name: "version with two parts", version: "0.1", goos: "linux", goarch: "amd64", wantErr: true},
		{name: "empty version", version: "", goos: "linux", goarch: "amd64", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := artifactName(tc.version, tc.goos, tc.goarch)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("name = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewReleaserRejectsNilDependencies(t *testing.T) {
	cases := []struct {
		name string
		deps Dependencies
	}{
		{name: "missing runner", deps: Dependencies{Out: &bytes.Buffer{}}},
		{name: "missing out", deps: Dependencies{Runner: &fakeRunner{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewReleaser(tc.deps); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		tag     string
		tagErr  error
		want    string
		wantErr bool
	}{
		{name: "flag wins", flag: "2.0.0", tag: "v1.0.0", want: "2.0.0"},
		{name: "git tag with v prefix", tag: "v1.2.3", want: "1.2.3"},
		{name: "no tag falls back to metadata", tagErr: errors.New("no tags"), want: "0.1.0"},
		{name: "unparsable tag falls back to metadata", tag: "v1.2", want: "0.1.0"},
		{name: "bad flag", flag: "1.2", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			releaser, _ := newReleaser(t, &fakeRunner{described: tc.tag, describeErr: tc.tagErr})
			got, err := releaser.resolveVersion(context.Background(), Config{Version: tc.flag}, t.TempDir(), appMetadata{Version: "0.1.0"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("version = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlanTargets(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		want    []target
		wantErr bool
	}{
		{name: "native host", cfg: Config{OS: "linux", HostOS: "linux", HostArch: "amd64"}, want: []target{{"linux", "amd64"}}},
		{name: "native other os", cfg: Config{OS: "windows", HostOS: "linux", HostArch: "amd64"}, wantErr: true},
		{name: "native other arch", cfg: Config{OS: "linux", Arch: "arm64", HostOS: "linux", HostArch: "amd64"}, wantErr: true},
		{name: "cross linux both", cfg: Config{OS: "linux", Cross: true}, want: []target{{"linux", "amd64"}, {"linux", "arm64"}}},
		{name: "cross linux one", cfg: Config{OS: "linux", Arch: "arm64", Cross: true}, want: []target{{"linux", "arm64"}}},
		{name: "cross windows", cfg: Config{OS: "windows", Cross: true}, want: []target{{"windows", "amd64"}}},
		{name: "cross windows arm64", cfg: Config{OS: "windows", Arch: "arm64", Cross: true}, wantErr: true},
		{name: "cross darwin", cfg: Config{OS: "darwin", Cross: true}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planTargets(tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("targets = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("targets = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestCommandSpelling(t *testing.T) {
	meta := appMetadata{ID: "io.github.prieyan.butterdb.admin"}
	cases := []struct {
		name string
		got  command
		want string
	}{
		{
			name: "native linux",
			got:  nativeCommand("/app", "0.1.0", target{"linux", "amd64"}),
			want: "go -C /app run fyne.io/tools/cmd/fyne package -os linux -release --app-version 0.1.0",
		},
		{
			name: "native windows",
			got:  nativeCommand("/app", "0.1.0", target{"windows", "amd64"}),
			want: "go -C /app run fyne.io/tools/cmd/fyne package -os windows -release --app-version 0.1.0",
		},
		{
			name: "native darwin",
			got:  nativeCommand("/app", "0.1.0", target{"darwin", "arm64"}),
			want: "go -C /app run fyne.io/tools/cmd/fyne package -os darwin -release --app-version 0.1.0",
		},
		{
			name: "cross linux",
			got:  crossCommand("/app", "0.1.0", meta, "linux", []string{"amd64", "arm64"}),
			want: "go -C /app run github.com/fyne-io/fyne-cross@v1.6.3 linux -arch=amd64,arm64 -app-id io.github.prieyan.butterdb.admin -app-version 0.1.0",
		},
		{
			name: "cross windows",
			got:  crossCommand("/app", "0.1.0", meta, "windows", []string{"amd64"}),
			want: "go -C /app run github.com/fyne-io/fyne-cross@v1.6.3 windows -arch=amd64 -app-id io.github.prieyan.butterdb.admin -app-version 0.1.0",
		},
		{
			name: "argument with space is quoted",
			got:  command{Name: "tool", Args: []string{"butterDB Admin"}},
			want: `tool "butterDB Admin"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.got.String(); got != tc.want {
				t.Fatalf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseMetadata(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    appMetadata
		wantErr bool
	}{
		{
			name:    "prelude file",
			content: metadataFixture,
			want:    appMetadata{Name: "butterDB Admin", ID: "io.github.prieyan.butterdb.admin", Version: "0.1.0", Icon: "packaging/icon.png", Build: 1},
		},
		{name: "keys outside details are ignored", content: "Name = \"top\"\n[Details]\nName = \"n\"\nID = \"i\"\n[Release]\nName = \"other\"\n", want: appMetadata{Name: "n", ID: "i"}},
		{name: "missing id", content: "[Details]\nName = \"n\"\n", wantErr: true},
		{name: "build not a number", content: "[Details]\nName = \"n\"\nID = \"i\"\nBuild = \"x\"\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, fyneMetadataFile), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := loadMetadata(dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("metadata = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMergeChecksumLines(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		file     string
		sum      string
		want     string
	}{
		{name: "first line", file: "b.zip", sum: "bb", want: "bb  b.zip\n"},
		{name: "sorted insert", existing: "bb  b.zip\n", file: "a.zip", sum: "aa", want: "aa  a.zip\nbb  b.zip\n"},
		{name: "replace same name", existing: "aa  a.zip\nbb  b.zip\n", file: "a.zip", sum: "cc", want: "cc  a.zip\nbb  b.zip\n"},
		{name: "garbage lines dropped", existing: "junk\n\n", file: "a.zip", sum: "aa", want: "aa  a.zip\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeChecksumLines(tc.existing, tc.file, tc.sum); got != tc.want {
				t.Fatalf("merged = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDryRunPlansWithoutTouchingDisk(t *testing.T) {
	appDir := newAppDir(t)
	runner := &fakeRunner{}
	releaser, out := newReleaser(t, runner)
	cfg := nativeConfig(appDir, "linux", "amd64")
	cfg.DryRun = true
	if err := releaser.Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("dry run executed %d commands", len(runner.commands))
	}
	if _, err := os.Stat(filepath.Join(appDir, defaultDistDir)); !os.IsNotExist(err) {
		t.Fatalf("dist exists after dry run: %v", err)
	}
	for _, want := range []string{
		"run: go -C " + appDir + " run fyne.io/tools/cmd/fyne package -os linux -release --app-version 0.1.0",
		"write: " + filepath.Join(appDir, defaultDistDir, "butterdb-admin_0.1.0_linux_amd64.tar.xz"),
		"write: " + filepath.Join(appDir, defaultDistDir, checksumsFile),
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry run output missing %q:\n%s", want, out.String())
		}
	}
}

func TestReleaseLinuxMovesArchiveAndKeepsMetadata(t *testing.T) {
	appDir := newAppDir(t)
	runner := &fakeRunner{onRun: func(cmd command) error {
		bumped := strings.Replace(metadataFixture, "Build = 1", "Build = 2", 1)
		if err := os.WriteFile(filepath.Join(appDir, fyneMetadataFile), []byte(bumped), 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(appDir, "butterDB Admin.tar.xz"), []byte("archive"), 0o644)
	}}
	releaser, _ := newReleaser(t, runner)
	if err := releaser.Run(context.Background(), nativeConfig(appDir, "linux", "amd64")); err != nil {
		t.Fatal(err)
	}
	name := "butterdb-admin_0.1.0_linux_amd64.tar.xz"
	got, err := os.ReadFile(filepath.Join(appDir, defaultDistDir, name))
	if err != nil || string(got) != "archive" {
		t.Fatalf("artifact = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "butterDB Admin.tar.xz")); !os.IsNotExist(err) {
		t.Fatalf("artifact left in source tree: %v", err)
	}
	metadata, _ := os.ReadFile(filepath.Join(appDir, fyneMetadataFile))
	if string(metadata) != metadataFixture {
		t.Fatalf("FyneApp.toml changed:\n%s", metadata)
	}
	sums, _ := os.ReadFile(filepath.Join(appDir, defaultDistDir, checksumsFile))
	wantSum, _ := fileSHA256(filepath.Join(appDir, defaultDistDir, name))
	if string(sums) != wantSum+"  "+name+"\n" {
		t.Fatalf("SHA256SUMS = %q", sums)
	}
}

func TestReleaseRestoresMetadataWhenToolFails(t *testing.T) {
	appDir := newAppDir(t)
	runner := &fakeRunner{onRun: func(command) error {
		bumped := strings.Replace(metadataFixture, "Build = 1", "Build = 9", 1)
		if err := os.WriteFile(filepath.Join(appDir, fyneMetadataFile), []byte(bumped), 0o644); err != nil {
			return err
		}
		return errors.New("tool failed")
	}}
	releaser, _ := newReleaser(t, runner)
	if err := releaser.Run(context.Background(), nativeConfig(appDir, "linux", "amd64")); err == nil {
		t.Fatal("expected the tool failure")
	}
	metadata, _ := os.ReadFile(filepath.Join(appDir, fyneMetadataFile))
	if string(metadata) != metadataFixture {
		t.Fatalf("FyneApp.toml changed:\n%s", metadata)
	}
}

func TestReleaseWindowsZipsExecutable(t *testing.T) {
	appDir := newAppDir(t)
	runner := &fakeRunner{onRun: func(command) error {
		return os.WriteFile(filepath.Join(appDir, "butterDB Admin.exe"), []byte("exe"), 0o755)
	}}
	releaser, _ := newReleaser(t, runner)
	if err := releaser.Run(context.Background(), nativeConfig(appDir, "windows", "amd64")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(appDir, defaultDistDir, "butterdb-admin_0.1.0_windows_amd64.zip")
	assertZipEntries(t, archive, map[string]string{"butterdb-admin.exe": "exe"})
	if _, err := os.Stat(filepath.Join(appDir, "butterDB Admin.exe")); !os.IsNotExist(err) {
		t.Fatalf("executable left in source tree: %v", err)
	}
}

func TestReleaseDarwinZipsAppBundle(t *testing.T) {
	appDir := newAppDir(t)
	runner := &fakeRunner{onRun: func(command) error {
		macOS := filepath.Join(appDir, "butterDB Admin.app", "Contents", "MacOS")
		if err := os.MkdirAll(macOS, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(macOS, "admin"), []byte("bin"), 0o755)
	}}
	releaser, _ := newReleaser(t, runner)
	if err := releaser.Run(context.Background(), nativeConfig(appDir, "darwin", "arm64")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(appDir, defaultDistDir, "butterdb-admin_0.1.0_darwin_arm64.zip")
	assertZipEntries(t, archive, map[string]string{"butterDB Admin.app/Contents/MacOS/admin": "bin"})
	if _, err := os.Stat(filepath.Join(appDir, "butterDB Admin.app")); !os.IsNotExist(err) {
		t.Fatalf("bundle left in source tree: %v", err)
	}
}

func TestReleaseCrossLinuxCollectsEveryArchitecture(t *testing.T) {
	appDir := newAppDir(t)
	runner := &fakeRunner{onRun: func(command) error {
		for _, arch := range []string{"amd64", "arm64"} {
			dir := filepath.Join(appDir, crossOutputDir, "dist", "linux-"+arch)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "butterDB Admin.tar.xz"), []byte(arch), 0o644); err != nil {
				return err
			}
		}
		return nil
	}}
	releaser, _ := newReleaser(t, runner)
	cfg := Config{AppDir: appDir, DistDir: defaultDistDir, Version: "0.1.0", OS: "linux", Cross: true, HostOS: "linux", HostArch: "amd64"}
	if err := releaser.Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		got, err := os.ReadFile(filepath.Join(appDir, defaultDistDir, "butterdb-admin_0.1.0_linux_"+arch+".tar.xz"))
		if err != nil || string(got) != arch {
			t.Fatalf("%s artifact = %q, %v", arch, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(appDir, crossOutputDir)); !os.IsNotExist(err) {
		t.Fatalf("fyne-cross output left in source tree: %v", err)
	}
	sums, _ := os.ReadFile(filepath.Join(appDir, defaultDistDir, checksumsFile))
	if strings.Count(string(sums), "\n") != 2 {
		t.Fatalf("SHA256SUMS = %q", sums)
	}
}

func TestReleaseRefusesToOverwriteSourceTreeFile(t *testing.T) {
	appDir := newAppDir(t)
	if err := os.WriteFile(filepath.Join(appDir, "butterDB Admin.tar.xz"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	releaser, _ := newReleaser(t, runner)
	if err := releaser.Run(context.Background(), nativeConfig(appDir, "linux", "amd64")); err == nil {
		t.Fatal("expected a refusal")
	}
	if len(runner.commands) != 0 {
		t.Fatal("the tool ran despite the stale file")
	}
}

func TestParseConfig(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    Config
		wantErr bool
	}{
		{name: "defaults", want: Config{AppDir: ".", DistDir: "dist", OS: "linux", HostOS: "linux", HostArch: "amd64"}},
		{
			name: "dry run linux",
			args: []string{"-os", "linux", "-dry-run", "-version", "0.1.0"},
			want: Config{AppDir: ".", DistDir: "dist", OS: "linux", Version: "0.1.0", DryRun: true, HostOS: "linux", HostArch: "amd64"},
		},
		{
			name: "cross windows",
			args: []string{"-os", "windows", "-cross", "-arch", "amd64"},
			want: Config{AppDir: ".", DistDir: "dist", OS: "windows", Arch: "amd64", Cross: true, HostOS: "linux", HostArch: "amd64"},
		},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: true},
		{name: "positional argument", args: []string{"extra"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConfig(tc.args, "linux", "amd64", &bytes.Buffer{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("config = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestIconIsSquare1024Png(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 1024 || config.Height != 1024 {
		t.Fatalf("icon is %dx%d, want 1024x1024", config.Width, config.Height)
	}
}
