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

// ModelAllowed reports whether this key may use the given model alias.
//
// An empty AllowedModels means every configured alias is permitted. That is the
// semantics ListModels has always had, and it is the semantics the database
// carries: cmd/keygen writes an empty JSON array for a key with no restriction,
// so "unset" and "permits nothing" would otherwise be the same stored value.
// Reading an empty list as a deny-all would revoke every key issued to date.
//
// It is a method on AuthInfo rather than a helper beside either call site
// because the model listing and the model enforcement must agree: a key that is
// shown an alias by GET /v1/models and then refused it by
// POST /v1/chat/completions is a bug in whichever of the two moved last.
func (a *AuthInfo) ModelAllowed(model string) bool {
	if len(a.AllowedModels) == 0 {
		return true
	}
	for _, m := range a.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}
