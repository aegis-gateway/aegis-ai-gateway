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
	KeyID string

	// KeyPrefix is api_keys.key_prefix, the display-safe value the operator was
	// shown when the key was issued.
	//
	// Read from the row, NOT derived from the presented token. Deriving it
	// looked cheaper and was wrong: KeyPrefix() returned the whole input for a
	// key shorter than 16 bytes, so an imported or manually provisioned
	// credential could be sealed into audit_events.api_key_prefix and served by
	// the audit read API.
	//
	// It exists so an attested event can name the key in a form a human can
	// match against a key list without holding the key itself.
	KeyPrefix            string
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
