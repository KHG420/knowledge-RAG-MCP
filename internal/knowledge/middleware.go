package knowledge

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// CORSMiddleware wraps an http.Handler with permissive CORS headers suitable
// for a local management UI. In production, restrict origins as needed.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware validates a Bearer token against the configured APIToken.
// When APIToken is empty, all requests pass through (auth disabled).
// The health endpoint is always exempt from authentication.
func AuthMiddleware(apiToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always allow health endpoint.
			if r.URL.Path == "/health" || r.URL.Path == "/api/health" {
				next.ServeHTTP(w, r)
				return
			}

			// When no token is configured, auth is disabled.
			if apiToken == "" {
				next.ServeHTTP(w, r)
				return
			}

			auth := r.Header.Get("Authorization")
			if auth == "" {
				// Also check query parameter for SSE connections that can't set headers.
				auth = r.URL.Query().Get("token")
				if auth != "" {
					auth = "Bearer " + auth
				}
			}

			if !strings.HasPrefix(auth, "Bearer ") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"missing Authorization header"}`))
				return
			}

			token := strings.TrimPrefix(auth, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(apiToken)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"invalid token"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitMiddleware applies a simple token-bucket rate limit to API endpoints.
// maxRequests is the burst size; perSecond is the refill rate.
// When maxRequests is 0, rate limiting is disabled.
//
// NOTE: This is a placeholder implementation. For production use, deploy a
// reverse proxy (nginx, Caddy) with proper rate limiting, or implement
// per-IP token-bucket tracking with sync.Map.
func RateLimitMiddleware(maxRequests int, perSecond float64) func(http.Handler) http.Handler {
	if maxRequests <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// TODO: implement per-IP token-bucket rate limiting.
			// For now, pass through — deploy reverse-proxy rate limiting instead.
			next.ServeHTTP(w, r)
		})
	}
}
