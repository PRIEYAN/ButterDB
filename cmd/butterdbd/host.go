package main

import (
	"io/fs"
	"os/exec"
)

type fileMeta struct {
	Mode       fs.FileMode
	Size       int64
	UID        uint32
	GID        uint32
	OwnerKnown bool
}

func (m fileMeta) isSymlink() bool {
	return m.Mode&fs.ModeSymlink != 0
}

type identity struct {
	UID uint32
	GID uint32
}

type hostFiles interface {
	Lstat(path string) (fileMeta, error)
	Readlink(path string) (string, error)
	ReadFile(path string) ([]byte, error)
	LookPath(name string) (string, error)
}

type hostVolumes interface {
	FilesystemType(path string) (uint32, bool)
	FreeBytes(path string) (uint64, bool)
	SameDevice(a, b string) (same bool, known bool)
	Writable(dir string) bool
}

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}
