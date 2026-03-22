package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/af-corp/aegis-gateway/internal/httputil"
)

// AuditLogger defines the interface for audit logging (to avoid circular dependency).
type AuditLogger interface {
	LogAuthFailure(requestID, ip, userAgent, apiKey, reason string)
}

// Middleware returns a chi middleware that authenticates requests via Bearer token.
func Middleware(store KeyStore, auditLogger AuditLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := w.Header().Get("X-Request-ID")

			// Extract Bearer token
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				if auditLogger != nil {
					auditLogger.LogAuthFailure(reqID, r.RemoteAddr, r.UserAgent(), "", "missing authorization header")
				}
				httputil.WriteAuthError(w, reqID, "Missing Authorization header. Use: Authorization: Bearer <api-key>")
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token == authHeader {
				if auditLogger != nil {
					auditLogger.LogAuthFailure(reqID, r.RemoteAddr, r.UserAgent(), "", "invalid authorization format")
				}
				httputil.WriteAuthError(w, reqID, "Invalid Authorization format. Use: Authorization: Bearer <api-key>")
				return
			}
			if token == "" {
				if auditLogger != nil {
					auditLogger.LogAuthFailure(reqID, r.RemoteAddr, r.UserAgent(), "", "empty api key")
				}
				httputil.WriteAuthError(w, reqID, "Empty API key")
				return
			}

			// Hash and lookup
			keyHash := HashKey(token)
			meta, err := store.Lookup(r.Context(), keyHash)
			if err != nil {
				slog.Error("key lookup failed", "error", err, "key_prefix", safePrefix(token))
				if auditLogger != nil {
					auditLogger.LogAuthFailure(reqID, r.RemoteAddr, r.UserAgent(), token, "database lookup error")
				}
				httputil.WriteInternalError(w, reqID, "Internal error during authentication")
				return
			}
			if meta == nil {
				slog.Warn("auth failed: key not found", "key_prefix", safePrefix(token))
				if auditLogger != nil {
					auditLogger.LogAuthFailure(reqID, r.RemoteAddr, r.UserAgent(), token, "api key not found")
				}
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
