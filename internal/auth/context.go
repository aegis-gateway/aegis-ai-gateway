package auth

import (
	"context"

	"github.com/af-corp/aegis-gateway/internal/types"
)

type contextKey string

const authContextKey contextKey = "aegis_auth"

// AuthInfo holds authenticated identity information extracted from an API key.
type AuthInfo struct {
	KeyID                string
	OrganizationID       string
	TeamID               string
	UserID               string
	MaxClassification    types.Classification
	AllowedModels        []string
	RPMLimit             *int
	TPMLimit             *int
	DailySpendLimitCents *int
}

func ContextWithAuth(ctx context.Context, info *AuthInfo) context.Context {
	return context.WithValue(ctx, authContextKey, info)
}

func AuthFromContext(ctx context.Context) (*AuthInfo, bool) {
	info, ok := ctx.Value(authContextKey).(*AuthInfo)
	return info, ok
}
