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

package adapters

import (
	"context"
	"net/http"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// ProviderAdapter transforms requests/responses between AEGIS canonical format
// and provider-specific API formats.
type ProviderAdapter interface {
	Name() string
	TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error)
	TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error)
	TransformStreamChunk(chunk []byte) ([]byte, error)
	SupportsStreaming() bool
	// SendRequest sends an HTTP request using the provider's configured client.
	SendRequest(req *http.Request) (*http.Response, error)
}
