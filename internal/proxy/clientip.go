package proxy

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// parseTrustedNetworks parses a list of CIDR strings. It returns an error if
// any entry is not a valid CIDR.
func parseTrustedNetworks(cidrs []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted_proxy cidr %q: %w", c, err)
		}
		out = append(out, ipnet)
	}
	return out, nil
}

// remoteIP extracts the IP portion of r.RemoteAddr. It returns nil if the
// address cannot be parsed.
func remoteIP(r *http.Request) net.IP {
	host := r.RemoteAddr
	if host == "" {
		return nil
	}
	// RemoteAddr is "ip:port" for TCP; try to split it first.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return net.ParseIP(host)
}

// ipInNetworks reports whether ip is contained in any of the networks.
func ipInNetworks(ip net.IP, networks []*net.IPNet) bool {
	for _, n := range networks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveClientIP returns the client IP for logging/upstream forwarding.
// If the immediate peer (r.RemoteAddr) is not in the trusted proxy list, the
// peer address is used and any X-Forwarded-For header is ignored.  If the peer
// is trusted, the right-most X-Forwarded-For entry that does not belong to a
// trusted proxy is returned (this is the closest untrusted client).  If there
// is no usable X-Forwarded-For, the peer address is used.
func resolveClientIP(r *http.Request, trusted []*net.IPNet) string {
	peer := remoteIP(r)
	peerStr := ""
	if peer != nil {
		peerStr = peer.String()
	}
	if !ipInNetworks(peer, trusted) {
		return peerStr
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			s := strings.TrimSpace(parts[i])
			if s == "" {
				continue
			}
			ip := net.ParseIP(s)
			if ip == nil {
				continue
			}
			if !ipInNetworks(ip, trusted) {
				return ip.String()
			}
		}
	}

	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		if ip := net.ParseIP(strings.TrimSpace(xri)); ip != nil && !ipInNetworks(ip, trusted) {
			return ip.String()
		}
	}

	return peerStr
}
