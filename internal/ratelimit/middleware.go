package ratelimit

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/telemetry"
)

const (
	defaultRPM = 60
	defaultTPM = 200_000

	headerRateLimitRequests          = "X-RateLimit-Limit-Requests"
	headerRateLimitRemainingRequests = "X-RateLimit-Remaining-Requests"
	headerRateLimitReset             = "X-RateLimit-Reset-Requests"
	headerRetryAfter                 = "Retry-After"
)

// Middleware returns chi middleware that enforces per-key rate limits and budget.
func Middleware(limiter *Limiter, budget *BudgetTracker, metrics *telemetry.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := w.Header().Get("X-Request-ID")

			authInfo, ok := auth.AuthFromContext(r.Context())
			if !ok {
				// No auth info — let request pass (auth middleware will catch it)
				next.ServeHTTP(w, r)
				return
			}

			// Determine RPM limit
			rpm := defaultRPM
			if authInfo.RPMLimit != nil {
				rpm = *authInfo.RPMLimit
			}

			// Check RPM
			rpmKey := fmt.Sprintf("rpm:%s", authInfo.KeyID)
			result, _ := limiter.Check(r.Context(), rpmKey, int64(rpm), time.Minute)

			// Always set rate limit headers
			w.Header().Set(headerRateLimitRequests, strconv.Itoa(rpm))
			w.Header().Set(headerRateLimitRemainingRequests, strconv.FormatInt(result.Remaining, 10))
			w.Header().Set(headerRateLimitReset, result.ResetAt.Format(time.RFC3339))

			if !result.Allowed {
				slog.Warn("rate limit exceeded",
					"request_id", reqID,
					"key_id", authInfo.KeyID,
					"org_id", authInfo.OrganizationID,
					"dimension", "rpm",
					"limit", rpm,
				)
				if metrics != nil {
					metrics.RecordRateLimitHit("rpm", authInfo.OrganizationID)
				}
				w.Header().Set(headerRetryAfter, strconv.Itoa(int(result.RetryAfter.Seconds())))
				httputil.WriteRateLimitError(w, reqID,
					fmt.Sprintf("Rate limit exceeded: %d requests per minute. Retry after %s", rpm, result.ResetAt.Format(time.RFC3339)))
				return
			}

			// Check daily budget
			if authInfo.DailySpendLimitCents != nil {
				budgetResult, _ := budget.CheckDailySpend(r.Context(), authInfo.TeamID, int64(*authInfo.DailySpendLimitCents))
				if !budgetResult.Allowed {
					slog.Warn("daily budget exceeded",
						"request_id", reqID,
						"key_id", authInfo.KeyID,
						"team_id", authInfo.TeamID,
						"spent_cents", budgetResult.SpentCents,
						"limit_cents", budgetResult.LimitCents,
					)
					if metrics != nil {
						metrics.RecordRateLimitHit("budget", authInfo.TeamID)
					}
					httputil.WriteBudgetExceededError(w, reqID,
						fmt.Sprintf("Daily budget exceeded: spent %d of %d cents", budgetResult.SpentCents, budgetResult.LimitCents))
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
