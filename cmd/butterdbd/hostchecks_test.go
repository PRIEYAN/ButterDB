package main

import (
	"encoding/base64"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

type fakeVolumes struct {
	fsType   uint32
	fsKnown  bool
	free     uint64
	freeKnow bool
	same     bool
	sameKnow bool
	writable bool
}

func (v fakeVolumes) FilesystemType(string) (uint32, bool) { return v.fsType, v.fsKnown }
func (v fakeVolumes) FreeBytes(string) (uint64, bool)      { return v.free, v.freeKnow }
func (v fakeVolumes) SameDevice(string, string) (bool, bool) {
	return v.same, v.sameKnow
}
func (v fakeVolumes) Writable(string) bool { return v.writable }

func checkerWith(files fakeFiles, volumes fakeVolumes) hostChecker {
	ids := identity{UID: 1000, GID: 1000}
	return hostChecker{files: files, volumes: volumes, fsys: fsx.OS{}, policy: secretPolicy{files: files, ids: ids}}
}

func findingContaining(found []config.Finding, key string, severity config.Severity, text string) bool {
	for _, f := range found {
		if f.Key == key && f.Severity == severity && strings.Contains(f.Message, text) {
			return true
		}
	}
	return false
}

func TestHostLocalFilesystem(t *testing.T) {
	cases := []struct {
		name    string
		magic   uint32
		known   bool
		wantErr string
	}{
		{"ext4 passes", 0xEF53, true, ""},
		{"nfs fails", magicNFS, true, "NFS"},
		{"smb fails", magicSMB, true, "SMB or CIFS"},
		{"cifs fails", magicCIFS, true, "SMB or CIFS"},
		{"fuse fails", magicFUSE, true, "FUSE"},
		{"unknown type passes", magicNFS, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := checkerWith(fakeFiles{}, fakeVolumes{fsType: tc.magic, fsKnown: tc.known})
			found := h.localFilesystem("data_dir", t.TempDir())
			if tc.wantErr == "" {
				if len(found) != 0 {
					t.Fatalf("findings %+v", found)
				}
				return
			}
			if !findingContaining(found, "data_dir", config.SeverityError, tc.wantErr) {
				t.Fatalf("findings %+v lack %q", found, tc.wantErr)
			}
		})
	}
}

func TestHostSameFilesystemAndFreeSpace(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Snapshot.Dir = filepath.Join(cfg.DataDir, "snapshots")

	h := checkerWith(fakeFiles{}, fakeVolumes{same: false, sameKnow: true})
	found := h.sameFilesystem(cfg)
	if !findingContaining(found, "data_dir", config.SeverityError, "different filesystem") {
		t.Fatalf("findings %+v", found)
	}
	if findingContaining(found, "snapshot.dir", config.SeverityWarning, "off-host") {
		t.Fatalf("snapshot warning on different devices: %+v", found)
	}

	h = checkerWith(fakeFiles{}, fakeVolumes{same: true, sameKnow: true})
	found = h.sameFilesystem(cfg)
	if !findingContaining(found, "snapshot.dir", config.SeverityWarning, "off-host") {
		t.Fatalf("findings %+v", found)
	}
	cfg.Snapshot.UploadToTelegram = true
	if found = h.sameFilesystem(cfg); len(found) != 0 {
		t.Fatalf("telegram upload still warns: %+v", found)
	}

	need := uint64(cfg.Spool.MaxBytes) + uint64(cfg.Spool.MinFreeBytes)
	h = checkerWith(fakeFiles{}, fakeVolumes{free: need - 1, freeKnow: true})
	if !findingContaining(h.freeSpace(cfg), "spool.max_bytes", config.SeverityWarning, "free") {
		t.Fatal("no low space warning")
	}
	h = checkerWith(fakeFiles{}, fakeVolumes{free: need, freeKnow: true})
	if found = h.freeSpace(cfg); len(found) != 0 {
		t.Fatalf("enough space still warns: %+v", found)
	}
}

func TestHostDataDirMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent", "data")
	h := checkerWith(fakeFiles{}, fakeVolumes{writable: false})
	if !findingContaining(h.dataDir(missing), "data_dir", config.SeverityError, "parent is not writable") {
		t.Fatal("unwritable parent not reported")
	}
	h = checkerWith(fakeFiles{}, fakeVolumes{writable: true})
	if found := h.dataDir(missing); len(found) != 0 {
		t.Fatalf("writable parent reported %+v", found)
	}
}

func TestHostMediaSigningKey(t *testing.T) {
	const path = "/keys/media.key"
	good := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("m", 32))) + "\n"
	meta := fileMeta{Mode: 0o600, Size: int64(len(good)), UID: 1000, GID: 1000, OwnerKnown: true}
	cases := []struct {
		name    string
		content string
		mode    fs.FileMode
		want    string
	}{
		{"valid key", good, 0o600, ""},
		{"short key", base64.RawURLEncoding.EncodeToString([]byte("short")), 0o600, "exactly 32 bytes"},
		{"not base64", "!!!not base64!!!", 0o600, "exactly 32 bytes"},
		{"loose mode", good, 0o644, "mode 0644"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := meta
			m.Mode = tc.mode
			files := fakeFiles{metas: map[string]fileMeta{path: m}, contents: map[string][]byte{path: []byte(tc.content)}}
			found := checkerWith(files, fakeVolumes{}).signingKey("media.signing_key_file", path)
			if tc.want == "" {
				if len(found) != 0 {
					t.Fatalf("findings %+v", found)
				}
				return
			}
			if !findingContaining(found, "media.signing_key_file", config.SeverityError, tc.want) {
				t.Fatalf("findings %+v lack %q", found, tc.want)
			}
		})
	}
	if found := checkerWith(fakeFiles{}, fakeVolumes{}).signingKey("media.signing_key_file", path); len(found) != 0 {
		t.Fatalf("absent key reported %+v", found)
	}
}

func TestHostExecutable(t *testing.T) {
	files := fakeFiles{metas: map[string]fileMeta{
		"/usr/bin/worker": {Mode: 0o755},
		"/usr/bin/noexec": {Mode: 0o644},
	}}
	h := checkerWith(files, fakeVolumes{})
	if found := h.executable("media.worker_path", "/usr/bin/worker"); len(found) != 0 {
		t.Fatalf("executable reported %+v", found)
	}
	if !findingContaining(h.executable("media.worker_path", "/usr/bin/noexec"), "media.worker_path", config.SeverityError, "not an executable") {
		t.Fatal("non-executable accepted")
	}
	if !findingContaining(h.executable("media.worker_path", "/usr/bin/absent"), "media.worker_path", config.SeverityError, "cannot inspect") {
		t.Fatal("absent file accepted")
	}
	if !findingContaining(h.executable("media.ffmpeg_path", "ffmpeg"), "media.ffmpeg_path", config.SeverityError, "not found") {
		t.Fatal("unresolvable name accepted")
	}
}
