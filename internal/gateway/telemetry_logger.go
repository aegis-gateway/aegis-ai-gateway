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

package gateway

import (
	"log/slog"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/storage"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/telemetry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// TelemetryLogger handles logging and metrics recording.
type TelemetryLogger struct {
	metrics       *telemetry.Metrics
	usageRecorder *storage.UsageRecorder
}

// LogCompletedRequest logs a completed request with all metrics.
func (tl *TelemetryLogger) LogCompletedRequest(
	reqID string,
	originalModel string,
	aegisResp *types.AegisResponse,
	authInfo *auth.AuthInfo,
	project string,
	stream bool,
	totalDuration time.Duration,
) {
	slog.Info("request completed",
		"request_id", reqID,
		"model_requested", originalModel,
		"model_served", aegisResp.Model,
		"provider", aegisResp.Provider,
		"prompt_tokens", aegisResp.Usage.PromptTokens,
		"completion_tokens", aegisResp.Usage.CompletionTokens,
		"total_tokens", aegisResp.Usage.TotalTokens,
		"estimated_cost_usd", aegisResp.EstimatedCostUSD,
		"duration_ms", totalDuration.Milliseconds(),
		"status_code", 200,
		"stream", stream,
		"classification", string(authInfo.MaxClassification),
		"org_id", authInfo.OrganizationID,
		"team_id", authInfo.TeamID,
	)

	// Record Prometheus metrics
	if tl.metrics != nil {
		tl.metrics.RecordRequest(telemetry.RequestLabels{
			Org:              authInfo.OrganizationID,
			Team:             authInfo.TeamID,
			Model:            originalModel,
			Provider:         aegisResp.Provider,
			Status:           "200",
			Classification:   string(authInfo.MaxClassification),
			DurationMs:       float64(totalDuration.Milliseconds()),
			OverheadMs:       float64(totalDuration.Milliseconds()),
			PromptTokens:     aegisResp.Usage.PromptTokens,
			CompletionTokens: aegisResp.Usage.CompletionTokens,
			CostUSD:          aegisResp.EstimatedCostUSD,
		})
	}

	// Record usage asynchronously (non-blocking)
	if tl.usageRecorder != nil {
		tl.usageRecorder.RecordUsage(storage.UsageRecord{
			RequestID:        reqID,
			OrganizationID:   authInfo.OrganizationID,
			TeamID:           authInfo.TeamID,
			UserID:           authInfo.UserID,
			APIKeyID:         authInfo.KeyID,
			ModelRequested:   originalModel,
			ModelServed:      aegisResp.Model,
			Provider:         aegisResp.Provider,
			Classification:   string(authInfo.MaxClassification),
			PromptTokens:     aegisResp.Usage.PromptTokens,
			CompletionTokens: aegisResp.Usage.CompletionTokens,
			TotalTokens:      aegisResp.Usage.TotalTokens,
			EstimatedCostUSD: aegisResp.EstimatedCostUSD,
			DurationMs:       totalDuration.Milliseconds(),
			StatusCode:       200,
			Project:          project,
			Stream:           stream,
		})
	}
}
