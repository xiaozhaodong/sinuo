package utils

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestFindAndBind_LoopbackAndLANModes(t *testing.T) {
	port := freeTCPPort(t)
	listeners, actual, err := FindAndBind(port, 1, false)
	if err != nil {
		t.Fatalf("loopback bind failed: %v", err)
	}
	if actual != port || len(listeners) < 1 || len(listeners) > 2 {
		t.Fatalf("loopback bind returned listeners=%d port=%d", len(listeners), actual)
	}
	for _, listener := range listeners {
		t.Cleanup(func() { _ = listener.Close() })
		address := listener.Addr().String()
		if !strings.HasPrefix(address, "127.0.0.1:") && !strings.HasPrefix(address, "[::1]:") {
			t.Fatalf("unexpected loopback listener address %q", address)
		}
	}

	for _, listener := range listeners {
		_ = listener.Close()
	}
	listeners, actual, err = FindAndBind(port, 1, true)
	if err != nil {
		t.Fatalf("LAN bind failed: %v", err)
	}
	if actual != port || len(listeners) != 1 {
		t.Fatalf("LAN bind returned listeners=%d port=%d", len(listeners), actual)
	}
	t.Cleanup(func() { _ = listeners[0].Close() })
}

func TestFindAndBind_SkipsOccupiedWildcardAndCleansUp(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network listener unavailable in this environment: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port
	listeners, actual, err := FindAndBind(port, 2, false)
	if err != nil {
		t.Fatalf("expected fallback port, got %v", err)
	}
	if actual == port || len(listeners) < 1 || len(listeners) > 2 {
		t.Fatalf("expected fallback listener, got port=%d listeners=%d", actual, len(listeners))
	}
	_ = listeners[0].Close()

	wildcard, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skipf("wildcard listener unavailable in this environment: %v", err)
	}
	wildcardPort := wildcard.Addr().(*net.TCPAddr).Port
	defer wildcard.Close()
	if listeners, actual, err := FindAndBind(wildcardPort, 1, true); err == nil || listeners != nil || actual != 0 {
		t.Fatalf("expected occupied port error, listeners=%v port=%d err=%v", listeners, actual, err)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network listener unavailable in this environment: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if port <= 0 {
		t.Fatalf("invalid free port %s", strconv.Itoa(port))
	}
	return port
}
