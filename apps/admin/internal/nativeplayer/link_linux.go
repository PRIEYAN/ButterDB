//go:build linux && cgo

package nativeplayer

// #cgo linux pkg-config: x11
import "C"
