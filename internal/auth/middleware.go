package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/af-corp/aegis-gateway/internal/httputil"
)

// Middleware returns a chi middleware that authenticates requests via Bearer token.
func Middleware(store KeyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := w.Header().Get("X-Request-ID")

			// Extract Bearer token
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				httputil.WriteAuthError(w, reqID, "Missing Authorization header. Use: Authorization: Bearer <api-key>")
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token == authHeader {
				httputil.WriteAuthError(w, reqID, "Invalid Authorization format. Use: Authorization: Bearer <api-key>")
				return
			}
			if token == "" {
				httputil.WriteAuthError(w, reqID, "Empty API key")
				return
			}

			// Hash and lookup
			keyHash := HashKey(token)
			meta, err := store.Lookup(r.Context(), keyHash)
			if err != nil {
				slog.Error("key lookup failed", "error", err, "key_prefix", safePrefix(token))
				httputil.WriteInternalError(w, reqID, "Internal error during authentication")
				return
			}
			if meta == nil {
				slog.Warn("auth failed: key not found", "key_prefix", safePrefix(token))
				httputil.WriteAuthError(w, reqID, "Invalid API key")
				return
			}

			// Enrich context
			info := &AuthInfo{
				KeyID:                meta.ID,
				OrganizationID:       meta.OrganizationID,
				TeamID:               meta.TeamID,
				UserID:               meta.UserID,
				MaxClassification:    meta.MaxClassification,
				AllowedModels:        meta.AllowedModels,
				RPMLimit:             meta.RPMLimit,
				TPMLimit:             meta.TPMLimit,
				DailySpendLimitCents: meta.DailySpendLimitCents,
			}

			ctx := ContextWithAuth(r.Context(), info)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// safePrefix returns a safe-to-log prefix of an API key (never the full key).
func safePrefix(key string) string {
	if len(key) > 20 {
		return key[:20] + "..."
	}
	return key
}
