//go:build unix

package keyring

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func checkOwnership(info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("keyring: key file owner is unavailable: %w", ErrKeyFilePermissions)
	}
	return evaluateKeyFileAccess(info.Mode(), st.Uid, st.Gid, uint32(os.Geteuid()), uint32(os.Getegid()))
}

func evaluateKeyFileAccess(mode fs.FileMode, uid, gid, euid, egid uint32) error {
	if mode.Perm()&0o007 != 0 {
		return fmt.Errorf("keyring: key file is accessible to others: %w", ErrKeyFilePermissions)
	}
	if mode.Perm()&0o030 != 0 {
		return fmt.Errorf("keyring: key file group has write or execute: %w", ErrKeyFilePermissions)
	}
	if mode.Perm()&0o040 != 0 && !(uid == 0 && gid == egid) {
		return fmt.Errorf("keyring: key file is group readable: %w", ErrKeyFilePermissions)
	}
	if uid != euid && uid != 0 {
		return fmt.Errorf("keyring: key file owner is neither the process user nor root: %w", ErrKeyFilePermissions)
	}
	return nil
}
