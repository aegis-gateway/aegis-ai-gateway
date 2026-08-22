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

package pii

import (
	"context"
	"fmt"
	"log/slog"

	filterv1 "github.com/aegis-gateway/aegis-ai-gateway/gen/filter/v1"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the gRPC FilterServiceClient and implements filter.Filter.
type Client struct {
	grpcClient filterv1.FilterServiceClient
	conn       *grpc.ClientConn
	cfg        func() config.PIIServiceConfig
}

// NewClient creates a PII filter client. Call Connect() to establish the gRPC connection.
func NewClient(cfg func() config.PIIServiceConfig) *Client {
	return &Client{cfg: cfg}
}

// Connect establishes the gRPC connection to the PII service.
func (c *Client) Connect() error {
	cfg := c.cfg()
	conn, err := grpc.NewClient(cfg.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("pii service dial: %w", err)
	}
	c.conn = conn
	c.grpcClient = filterv1.NewFilterServiceClient(conn)
	slog.Info("pii service connected", "address", cfg.Address)
	return nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *Client) Name() string  { return "pii" }
func (c *Client) Enabled() bool { return c.cfg().Enabled }

// ScanRequest implements filter.Filter.
func (c *Client) ScanRequest(ctx context.Context, req *types.AegisRequest) filter.Result {
	if c.grpcClient == nil {
		if c.cfg().FailOpen {
			return filter.Result{Action: filter.ActionPass, FilterName: "pii"}
		}
		return filter.Result{
			Action:     filter.ActionBlock,
			FilterName: "pii",
			Message:    "PII service not connected",
		}
	}

	cfg := c.cfg()
	scanCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	classification := string(req.Classification)

	for _, msg := range req.Messages {
		resp, err := c.grpcClient.ScanPII(scanCtx, &filterv1.ScanPIIRequest{
			Text:           msg.Content,
			Classification: classification,
		})
		if err != nil {
			slog.Error("pii service error", "error", err)
			if cfg.FailOpen {
				return filter.Result{Action: filter.ActionPass, FilterName: "pii"}
			}
			return filter.Result{
				Action:     filter.ActionBlock,
				FilterName: "pii",
				Message:    "PII service unavailable",
			}
		}

		if resp.Detected {
			action := classificationAction(classification, len(resp.Detections))
			if action == filter.ActionBlock {
				return filter.Result{
					Action:     filter.ActionBlock,
					FilterName: "pii",
					Message:    fmt.Sprintf("PII detected: %d entities found", len(resp.Detections)),
					Detections: len(resp.Detections),
				}
			}
			if action == filter.ActionFlag {
				return filter.Result{
					Action:     filter.ActionFlag,
					FilterName: "pii",
					Detections: len(resp.Detections),
				}
			}
		}
	}

	return filter.Result{Action: filter.ActionPass, FilterName: "pii"}
}

// classificationAction determines the action based on classification level.
func classificationAction(classification string, detections int) filter.Action {
	if detections == 0 {
		return filter.ActionPass
	}
	switch classification {
	case "RESTRICTED", "CONFIDENTIAL":
		return filter.ActionBlock
	case "INTERNAL":
		return filter.ActionFlag
	default: // PUBLIC
		return filter.ActionFlag
	}
}
