//go:build unix

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

type osFiles struct{}

func (osFiles) Lstat(path string) (fileMeta, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileMeta{}, err
	}
	meta := fileMeta{Mode: info.Mode(), Size: info.Size()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		meta.UID, meta.GID, meta.OwnerKnown = stat.Uid, stat.Gid, true
	}
	return meta, nil
}

func (osFiles) Readlink(path string) (string, error) {
	return os.Readlink(path)
}

func (osFiles) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osFiles) LookPath(name string) (string, error) {
	return lookPath(name)
}

type osVolumes struct{}

func (osVolumes) SameDevice(a, b string) (bool, bool) {
	first, err := os.Stat(a)
	if err != nil {
		return false, false
	}
	second, err := os.Stat(b)
	if err != nil {
		return false, false
	}
	x, okX := first.Sys().(*syscall.Stat_t)
	y, okY := second.Sys().(*syscall.Stat_t)
	if !okX || !okY {
		return false, false
	}
	return uint64(x.Dev) == uint64(y.Dev), true
}

func (osVolumes) Writable(dir string) bool {
	return unix.Access(dir, unix.W_OK) == nil
}

func processIdentity() identity {
	return identity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
}
