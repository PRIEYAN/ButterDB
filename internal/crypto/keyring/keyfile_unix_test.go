//go:build unix

package keyring

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyFileAccessRules(t *testing.T) {
	const euid, egid = 1000, 1000
	tests := []struct {
		name    string
		mode    fs.FileMode
		uid     uint32
		gid     uint32
		wantErr bool
	}{
		{name: "0600 own", mode: 0o600, uid: euid, gid: egid},
		{name: "0400 own", mode: 0o400, uid: euid, gid: egid},
		{name: "0600 root owned", mode: 0o600, uid: 0, gid: 0},
		{name: "other read", mode: 0o604, uid: euid, gid: egid, wantErr: true},
		{name: "other write", mode: 0o602, uid: euid, gid: egid, wantErr: true},
		{name: "other execute", mode: 0o601, uid: euid, gid: egid, wantErr: true},
		{name: "group write", mode: 0o620, uid: euid, gid: egid, wantErr: true},
		{name: "group execute", mode: 0o610, uid: euid, gid: egid, wantErr: true},
		{name: "group read owned by process user", mode: 0o640, uid: euid, gid: egid, wantErr: true},
		{name: "group read root owned same group", mode: 0o640, uid: 0, gid: egid},
		{name: "group read root owned other group", mode: 0o640, uid: 0, gid: 42, wantErr: true},
		{name: "group read other owner", mode: 0o640, uid: 4242, gid: egid, wantErr: true},
		{name: "owned by another user", mode: 0o600, uid: 4242, gid: egid, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateKeyFileAccess(tc.mode, tc.uid, tc.gid, euid, egid)
			if tc.wantErr {
				if !errors.Is(err, ErrKeyFilePermissions) {
					t.Fatalf("error = %v, want ErrKeyFilePermissions", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadChecksFilePermissions(t *testing.T) {
	path := writeKeyFile(t, testRootKey())
	for _, mode := range []fs.FileMode{0o644, 0o640, 0o660, 0o666, 0o604} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if _, err := Load(path); !errors.Is(err, ErrKeyFilePermissions) {
			t.Fatalf("Load with mode %v = %v, want ErrKeyFilePermissions", mode, err)
		}
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	ring, err := Load(path)
	if err != nil {
		t.Fatalf("Load with mode 0400: %v", err)
	}
	ring.Close()
}

func TestLoadFollowsSymlinkOnceAndChecksTarget(t *testing.T) {
	target := writeKeyFile(t, testRootKey())
	link := filepath.Join(t.TempDir(), "link.key")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	ring, err := Load(link)
	if err != nil {
		t.Fatalf("Load through symlink: %v", err)
	}
	ring.Close()
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := Load(link); !errors.Is(err, ErrKeyFilePermissions) {
		t.Fatalf("Load through symlink with open target = %v, want ErrKeyFilePermissions", err)
	}
}
