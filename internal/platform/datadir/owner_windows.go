//go:build windows

package datadir

import "io/fs"

func ownedByProcessUser(fs.FileInfo) bool {
	return true
}
