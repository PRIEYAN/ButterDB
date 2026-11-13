//go:build linux

package main

import "golang.org/x/sys/unix"

func (osVolumes) FilesystemType(path string) (uint32, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, false
	}
	return uint32(st.Type), true
}

func (osVolumes) FreeBytes(path string) (uint64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
