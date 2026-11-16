package session

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func ResolveClientAddr(r *http.Request, trusted []netip.Prefix) string {
	peer := peerAddr(r.RemoteAddr)
	if !peer.IsValid() || !isTrusted(peer, trusted) {
		return peerString(r.RemoteAddr)
	}
	entries := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(entries) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
		if err != nil {
			continue
		}
		if !isTrusted(addr.Unmap(), trusted) {
			return addr.Unmap().String()
		}
	}
	return peerString(r.RemoteAddr)
}

func clientAddr(r *http.Request) string {
	if addr, ok := ClientAddrFromContext(r.Context()); ok {
		return addr
	}
	return peerString(r.RemoteAddr)
}

func peerString(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return host
}

func peerAddr(remote string) netip.Addr {
	addr, err := netip.ParseAddr(peerString(remote))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
