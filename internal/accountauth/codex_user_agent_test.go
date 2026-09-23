package accountauth

import (
	"regexp"
	"testing"
)

func TestCodexBaseUserAgentShape(t *testing.T) {
	ua := CodexBaseUserAgent()
	// 形态：codex_cli_rs/0.155.1 (Mac OS 27.0.0; arm64) unknown
	pattern := regexp.MustCompile(`^codex_cli_rs/` + regexp.QuoteMeta(CodexCompatVersion) + ` \([A-Za-z ]+( [0-9]+\.[0-9]+\.[0-9]+)?; [a-z0-9_]+\) unknown$`)
	if !pattern.MatchString(ua) {
		t.Fatalf("unexpected user agent shape: %q", ua)
	}
	if CodexBaseUserAgent() != ua {
		t.Fatalf("user agent must be stable across calls")
	}
}

func TestNormalizeThreeComponentVersion(t *testing.T) {
	cases := map[string]string{
		"27.0":     "27.0.0",
		"26.6.2":   "26.6.2",
		"15":       "15.0.0",
		"27.0.1.5": "27.0.1",
	}
	for in, want := range cases {
		if got := normalizeThreeComponentVersion(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
