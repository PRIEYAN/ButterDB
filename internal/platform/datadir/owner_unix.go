//go:build !windows

package datadir

import (
	"io/fs"
	"os"
	"syscall"
)

func ownedByProcessUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return int(stat.Uid) == os.Geteuid()
}
