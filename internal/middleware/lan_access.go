package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// LANAccessGate protects the main port when it is reachable from other
// devices. Its state is updated from the settings store and is independent of
// config.yaml hot reloads.
type LANAccessGate struct {
	mu      sync.RWMutex
	enabled bool
	token   string
	logger  *slog.Logger
}

func NewLANAccessGate(logger *slog.Logger) *LANAccessGate {
	if logger == nil {
		logger = slog.Default()
	}
	return &LANAccessGate{logger: logger}
}

// Update replaces the runtime gate state. Empty or whitespace-only tokens are
// deliberately retained as unconfigured so LAN requests fail closed.
func (g *LANAccessGate) Update(enabled bool, token string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.enabled = enabled
	g.token = strings.TrimSpace(token)
	g.mu.Unlock()
}

// State reports the effective setting and whether a non-empty token is
// configured, without exposing the token itself.
func (g *LANAccessGate) State() (enabled, tokenConfigured bool) {
	if g == nil {
		return false, false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.enabled, g.token != ""
}

func (g *LANAccessGate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteIP := remoteIP(r.RemoteAddr)
		if remoteIP.IsValid() && remoteIP.Unmap().IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}

		g.mu.RLock()
		enabled, configured, configuredToken := g.enabled, g.token != "", g.token
		g.mu.RUnlock()

		if !enabled {
			g.reject(w, r, remoteIP, http.StatusForbidden, "LAN access is disabled")
			return
		}
		if !configured {
			g.reject(w, r, remoteIP, http.StatusForbidden, "LAN access token is not configured")
			return
		}

		tokens, present, _ := accessTokenCandidates(r)
		if !present || !accessTokenMatches(configuredToken, tokens) {
			g.reject(w, r, remoteIP, http.StatusUnauthorized, "Missing or invalid access token")
			return
		}

		next.ServeHTTP(w, withLANAccessAuthenticated(r))
	})
}

func (g *LANAccessGate) reject(w http.ResponseWriter, r *http.Request, remote netip.Addr, status int, reason string) {
	ip := remote.String()
	if !remote.IsValid() {
		ip = r.RemoteAddr
	}
	g.logger.Warn("LAN request rejected", "remote_ip", ip, "method", r.Method, "path", r.URL.Path, "reason", reason)
	http.Error(w, reason, status)
}

func remoteIP(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}
