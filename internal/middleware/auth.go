package middleware

import (
	"context"
	"crypto/subtle"
	"net/http"
	"sinuo/config"
	"strings"
	"sync"
)

type AuthMiddleware struct {
	mu     sync.RWMutex
	config config.AuthConfig
}

func NewAuthMiddleware(cfg config.AuthConfig) *AuthMiddleware {
	return &AuthMiddleware{
		config: cfg,
	}
}

func (am *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		am.mu.RLock()
		cfg := am.config
		am.mu.RUnlock()

		if !cfg.Enabled || lanAccessAuthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}

		tokens, present, malformed := accessTokenCandidates(r)
		if !present {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		if accessTokenMatches(cfg.Token, tokens) {
			next.ServeHTTP(w, r)
			return
		}

		if malformed {
			http.Error(w, "Invalid authorization format. Expected 'Bearer <token>'", http.StatusUnauthorized)
			return
		}

		http.Error(w, "Invalid token", http.StatusUnauthorized)
	})
}

// UpdateConfig updates the auth middleware configuration
func (am *AuthMiddleware) UpdateConfig(cfg config.AuthConfig) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.config = cfg
}

type lanAccessContextKey struct{}

func withLANAccessAuthenticated(r *http.Request) *http.Request {
	return r.WithContext(contextWithLANAccessAuthenticated(r.Context()))
}

func lanAccessAuthenticated(r *http.Request) bool {
	if r == nil {
		return false
	}
	value, _ := r.Context().Value(lanAccessContextKey{}).(bool)
	return value
}

func contextWithLANAccessAuthenticated(ctx context.Context) context.Context {
	return context.WithValue(ctx, lanAccessContextKey{}, true)
}

// accessTokenCandidates accepts both the Bearer form used by the web API and
// x-api-key used by Claude clients. Both headers are retained: either one may
// match the configured token when clients send both forms at once.
func accessTokenCandidates(r *http.Request) (tokens []string, present, malformedAuthorization bool) {
	if apiKey := strings.TrimSpace(r.Header.Get("x-api-key")); apiKey != "" {
		tokens = append(tokens, apiKey)
		present = true
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		return tokens, present, false
	}
	present = true
	if !strings.HasPrefix(auth, "Bearer ") {
		return tokens, present, true
	}
	tokens = append(tokens, strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
	return tokens, present, false
}

func accessTokenMatches(expected string, candidates []string) bool {
	matched := false
	for _, candidate := range candidates {
		if constantTimeTokenEqual(expected, candidate) {
			matched = true
		}
	}
	return matched
}

func constantTimeTokenEqual(expected, actual string) bool {
	expected = strings.TrimSpace(expected)
	actual = strings.TrimSpace(actual)
	if expected == "" || actual == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
