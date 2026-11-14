//go:build linux

package main

import "golang.org/x/sys/unix"

func hideEcho(fd int) (restore func(), hidden bool) {
	if fd < 0 {
		return func() {}, false
	}
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}, false
	}
	silent := *original
	silent.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &silent); err != nil {
		return func() {}, false
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, original) }, true
}
