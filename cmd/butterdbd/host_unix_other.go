//go:build unix && !linux

package main

func (osVolumes) FilesystemType(string) (uint32, bool) {
	return 0, false
}

func (osVolumes) FreeBytes(string) (uint64, bool) {
	return 0, false
}
