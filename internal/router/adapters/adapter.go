package adapters

import (
	"context"
	"net/http"

	"github.com/af-corp/aegis-gateway/internal/types"
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
