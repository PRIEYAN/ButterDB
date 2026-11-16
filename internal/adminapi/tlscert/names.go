package tlscert

import (
	"net"
	"slices"
	"strings"
)

type nameSet struct {
	dns []string
	ips []net.IP
}

func buildNameSet(hostname string, configured []string) nameSet {
	candidates := []string{"localhost", "127.0.0.1", "::1", hostname}
	candidates = append(candidates, configured...)
	dnsSeen := map[string]bool{}
	ipSeen := map[string]bool{}
	var set nameSet
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate))
		if name == "" {
			continue
		}
		if ip := net.ParseIP(name); ip != nil {
			if !ipSeen[ip.String()] {
				ipSeen[ip.String()] = true
				set.ips = append(set.ips, ip)
			}
			continue
		}
		if !dnsSeen[name] {
			dnsSeen[name] = true
			set.dns = append(set.dns, name)
		}
	}
	slices.Sort(set.dns)
	slices.SortFunc(set.ips, func(a, b net.IP) int { return strings.Compare(a.String(), b.String()) })
	return set
}

func (s nameSet) equal(other nameSet) bool {
	if !slices.Equal(s.dns, other.dns) || len(s.ips) != len(other.ips) {
		return false
	}
	for i := range s.ips {
		if !s.ips[i].Equal(other.ips[i]) {
			return false
		}
	}
	return true
}
