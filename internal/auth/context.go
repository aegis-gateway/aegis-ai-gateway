// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"context"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
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
