package storage

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UsageRecord represents a single request usage record.
type UsageRecord struct {
	RequestID        string
	OrganizationID   string
	TeamID           string
	UserID           string
	APIKeyID         string
	ModelRequested   string
	ModelServed      string
	Provider         string
	Classification   string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	EstimatedCostUSD float64
	DurationMs       int64
	StatusCode       int
	Project          string
	Stream           bool
}

// UsageRecorder handles writing usage records to the database.
type UsageRecorder struct {
	pool *pgxpool.Pool
}

// NewUsageRecorder creates a new usage recorder.
func NewUsageRecorder(pool *pgxpool.Pool) *UsageRecorder {
	return &UsageRecorder{
		pool: pool,
	}
}

// RecordUsage asynchronously writes a usage record to the database.
// It does not block the request response.
func (r *UsageRecorder) RecordUsage(record UsageRecord) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := r.recordSync(ctx, record); err != nil {
			slog.Error("failed to record usage",
				"error", err,
				"request_id", record.RequestID,
				"org_id", record.OrganizationID,
			)
		}
	}()
}

// recordSync performs the actual database write.
func (r *UsageRecorder) recordSync(ctx context.Context, record UsageRecord) error {
	query := `
		INSERT INTO usage_records (
			request_id, organization_id, team_id, user_id, api_key_id,
			model_requested, model_served, provider, classification,
			prompt_tokens, completion_tokens, total_tokens,
			estimated_cost_usd, duration_ms, status_code,
			project, stream
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12,
			$13, $14, $15,
			$16, $17
		)
	`

	_, err := r.pool.Exec(ctx, query,
		record.RequestID, record.OrganizationID, record.TeamID, record.UserID, record.APIKeyID,
		record.ModelRequested, record.ModelServed, record.Provider, record.Classification,
		record.PromptTokens, record.CompletionTokens, record.TotalTokens,
		record.EstimatedCostUSD, record.DurationMs, record.StatusCode,
		record.Project, record.Stream,
	)

	if err != nil {
		return err
	}

	slog.Debug("usage record saved",
		"request_id", record.RequestID,
		"org_id", record.OrganizationID,
		"model", record.ModelServed,
		"cost_usd", record.EstimatedCostUSD,
	)

	return nil
}

// GetUsageByOrg retrieves usage records for an organization within a date range.
func (r *UsageRecorder) GetUsageByOrg(ctx context.Context, orgID string, startTime, endTime time.Time, limit int) ([]UsageRecord, error) {
	query := `
		SELECT 
			request_id, organization_id, team_id, user_id, api_key_id,
			model_requested, model_served, provider, classification,
			prompt_tokens, completion_tokens, total_tokens,
			estimated_cost_usd, duration_ms, status_code,
			project, stream
		FROM usage_records
		WHERE organization_id = $1
		  AND created_at >= $2
		  AND created_at < $3
		ORDER BY created_at DESC
		LIMIT $4
	`

	rows, err := r.pool.Query(ctx, query, orgID, startTime, endTime, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []UsageRecord
	for rows.Next() {
		var rec UsageRecord
		err := rows.Scan(
			&rec.RequestID, &rec.OrganizationID, &rec.TeamID, &rec.UserID, &rec.APIKeyID,
			&rec.ModelRequested, &rec.ModelServed, &rec.Provider, &rec.Classification,
			&rec.PromptTokens, &rec.CompletionTokens, &rec.TotalTokens,
			&rec.EstimatedCostUSD, &rec.DurationMs, &rec.StatusCode,
			&rec.Project, &rec.Stream,
		)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}

	return records, rows.Err()
}

// GetUsageByTeam retrieves usage records for a team within a date range.
func (r *UsageRecorder) GetUsageByTeam(ctx context.Context, teamID string, startTime, endTime time.Time, limit int) ([]UsageRecord, error) {
	query := `
		SELECT 
			request_id, organization_id, team_id, user_id, api_key_id,
			model_requested, model_served, provider, classification,
			prompt_tokens, completion_tokens, total_tokens,
			estimated_cost_usd, duration_ms, status_code,
			project, stream
		FROM usage_records
		WHERE team_id = $1
		  AND created_at >= $2
		  AND created_at < $3
		ORDER BY created_at DESC
		LIMIT $4
	`

	rows, err := r.pool.Query(ctx, query, teamID, startTime, endTime, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []UsageRecord
	for rows.Next() {
		var rec UsageRecord
		err := rows.Scan(
			&rec.RequestID, &rec.OrganizationID, &rec.TeamID, &rec.UserID, &rec.APIKeyID,
			&rec.ModelRequested, &rec.ModelServed, &rec.Provider, &rec.Classification,
			&rec.PromptTokens, &rec.CompletionTokens, &rec.TotalTokens,
			&rec.EstimatedCostUSD, &rec.DurationMs, &rec.StatusCode,
			&rec.Project, &rec.Stream,
		)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}

	return records, rows.Err()
}

// GetUsageSummary returns aggregated usage statistics for an organization.
type UsageSummary struct {
	TotalRequests     int
	TotalCostUSD      float64
	TotalTokens       int64
	PromptTokens      int64
	CompletionTokens  int64
	AverageDurationMs float64
}

func (r *UsageRecorder) GetUsageSummary(ctx context.Context, orgID string, startTime, endTime time.Time) (*UsageSummary, error) {
	query := `
		SELECT 
			COUNT(*) as total_requests,
			COALESCE(SUM(estimated_cost_usd), 0) as total_cost,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(AVG(duration_ms), 0) as avg_duration
		FROM usage_records
		WHERE organization_id = $1
		  AND created_at >= $2
		  AND created_at < $3
	`

	var summary UsageSummary
	err := r.pool.QueryRow(ctx, query, orgID, startTime, endTime).Scan(
		&summary.TotalRequests,
		&summary.TotalCostUSD,
		&summary.TotalTokens,
		&summary.PromptTokens,
		&summary.CompletionTokens,
		&summary.AverageDurationMs,
	)

	if err != nil {
		return nil, err
	}

	return &summary, nil
}
