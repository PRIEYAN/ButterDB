//go:build linux

package nativeplayer

import "os"

const linuxNativePlayerFlag = "BUTTERDB_LINUX_NATIVE_PLAYER"

func linuxNativePlayerEnabled() bool {
	return nativePlayerEnabled(os.Getenv(linuxNativePlayerFlag))
}
