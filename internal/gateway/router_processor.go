package gateway

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/retry"
	"github.com/af-corp/aegis-gateway/internal/router"
	"github.com/af-corp/aegis-gateway/internal/router/adapters"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// RouterProcessor handles provider routing and request transformation.
type RouterProcessor struct {
	registry      *router.Registry
	healthTracker *router.HealthTracker
	modelsCfg     func() *config.ModelsConfig
}

// RouteResult contains the routing decision and transformed request.
type RouteResult struct {
	Adapter       adapters.ProviderAdapter
	ProviderModel string
	ProviderReq   *http.Request
}

// RouteToProvider resolves routing, transforms request, and returns the route result.
func (rp *RouterProcessor) RouteToProvider(
	ctx context.Context,
	aegisReq *types.AegisRequest,
) (*RouteResult, error) {
	modelsCfg := rp.modelsCfg()
	
	// Route to provider based on model and classification
	adapter, providerModel, err := router.ResolveRoute(
		modelsCfg,
		rp.registry,
		rp.healthTracker,
		aegisReq.Model,
		string(aegisReq.Classification),
	)
	if err != nil {
		return nil, httputil.NewHTTPError(
			http.StatusServiceUnavailable,
			"No provider available: "+err.Error(),
		)
	}

	// Override model with the provider-specific model name
	originalModel := aegisReq.Model
	aegisReq.Model = providerModel

	// Transform request for provider
	providerReq, err := adapter.TransformRequest(ctx, aegisReq)
	if err != nil {
		// Restore original model on error
		aegisReq.Model = originalModel
		slog.Error("failed to transform request",
			"error", err,
			"provider", adapter.Name(),
		)
		return nil, httputil.NewHTTPError(
			http.StatusInternalServerError,
			"Failed to prepare provider request",
		)
	}

	return &RouteResult{
		Adapter:       adapter,
		ProviderModel: providerModel,
		ProviderReq:   providerReq,
	}, nil
}

// ProviderExecutor handles provider request execution with retry logic.
type ProviderExecutor struct {
	healthTracker *router.HealthTracker
	retryExecutor interface {
		Execute(ctx context.Context, provider string, fn retry.RetryableFunc) (*http.Response, error)
	}
}

// ExecuteProviderRequest sends request to provider with retry logic.
func (pe *ProviderExecutor) ExecuteProviderRequest(
	ctx context.Context,
	adapter adapters.ProviderAdapter,
	aegisReq *types.AegisRequest,
	providerReq *http.Request,
) (*http.Response, error) {
	var providerResp *http.Response
	var err error

	if pe.retryExecutor != nil {
		// Use retry logic
		providerResp, err = pe.retryExecutor.Execute(
			ctx,
			adapter.Name(),
			func(retryCtx context.Context, attempt int) (*http.Response, error) {
				// Re-create request for each attempt with fresh context
				retryReq, transformErr := adapter.TransformRequest(retryCtx, aegisReq)
				if transformErr != nil {
					return nil, transformErr
				}
				return adapter.SendRequest(retryReq)
			},
		)
	} else {
		// Fallback to direct send if no retry executor
		providerResp, err = adapter.SendRequest(providerReq)
	}

	if err != nil {
		slog.Error("provider request failed",
			"error", err,
			"provider", adapter.Name(),
		)
		if pe.healthTracker != nil {
			pe.healthTracker.RecordFailure(adapter.Name())
		}
		return nil, httputil.NewHTTPError(
			http.StatusServiceUnavailable,
			"Provider request failed",
		)
	}

	if pe.healthTracker != nil {
		pe.healthTracker.RecordSuccess(adapter.Name())
	}

	return providerResp, nil
}

// TransformProviderResponse transforms provider response to AEGIS format.
func (pe *ProviderExecutor) TransformProviderResponse(
	ctx context.Context,
	adapter adapters.ProviderAdapter,
	providerResp *http.Response,
) (*types.AegisResponse, error) {
	aegisResp, err := adapter.TransformResponse(ctx, providerResp)
	if err != nil {
		slog.Error("failed to transform response",
			"error", err,
			"provider", adapter.Name(),
		)
		return nil, httputil.NewHTTPError(
			http.StatusInternalServerError,
			"Failed to process provider response",
		)
	}
	return aegisResp, nil
}
