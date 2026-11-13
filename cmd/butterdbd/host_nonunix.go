//go:build !unix

package main

import "os"

type osFiles struct{}

func (osFiles) Lstat(path string) (fileMeta, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileMeta{}, err
	}
	return fileMeta{Mode: info.Mode(), Size: info.Size()}, nil
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

func (osVolumes) FilesystemType(string) (uint32, bool) { return 0, false }

func (osVolumes) FreeBytes(string) (uint64, bool) { return 0, false }

func (osVolumes) SameDevice(string, string) (bool, bool) { return false, false }

func (osVolumes) Writable(string) bool { return true }

func processIdentity() identity {
	return identity{}
}
