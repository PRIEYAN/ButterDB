//go:build !unix

package keyring

import "io/fs"

// Windows file modes are synthetic, so the unix permission rules cannot be evaluated here.
func checkOwnership(info fs.FileInfo) error {
	return nil
}
