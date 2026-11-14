//go:build !linux

package main

// Echo suppression is only implemented for Linux terminals; elsewhere secrets are read as typed.
func hideEcho(int) (restore func(), hidden bool) {
	return func() {}, false
}
