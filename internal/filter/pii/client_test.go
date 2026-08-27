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
	"strings"
	"testing"
	"time"

	filterv1 "github.com/aegis-gateway/aegis-ai-gateway/gen/filter/v1"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
	"google.golang.org/grpc"
)

// mockFilterClient directly implements FilterServiceClient for unit testing
// without needing real gRPC transport (which requires proto.Message).
type mockFilterClient struct {
	scanFunc func(ctx context.Context, req *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error)
}

func (m *mockFilterClient) ScanPII(ctx context.Context, in *filterv1.ScanPIIRequest, _ ...grpc.CallOption) (*filterv1.ScanPIIResponse, error) {
	if m.scanFunc != nil {
		return m.scanFunc(ctx, in)
	}
	return &filterv1.ScanPIIResponse{Detected: false}, nil
}

func clientWithMock(mock *mockFilterClient, failOpen bool) *Client {
	return &Client{
		grpcClient: mock,
		cfg: func() config.PIIServiceConfig {
			return config.PIIServiceConfig{
				Enabled:  true,
				Timeout:  5 * time.Second,
				FailOpen: failOpen,
			}
		},
	}
}

func TestClient_NoPII_Pass(t *testing.T) {
	mock := &mockFilterClient{
		scanFunc: func(_ context.Context, _ *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error) {
			return &filterv1.ScanPIIResponse{Detected: false}, nil
		},
	}
	c := clientWithMock(mock, false)
	req := &types.AegisRequest{
		Messages:       []types.Message{{Role: "user", Content: types.TextContent("Hello world")}},
		Classification: "INTERNAL",
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionPass {
		t.Errorf("expected ActionPass, got %s", result.Action)
	}
}

func TestClient_PIIDetected_Confidential_Block(t *testing.T) {
	mock := &mockFilterClient{
		scanFunc: func(_ context.Context, _ *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error) {
			return &filterv1.ScanPIIResponse{
				Detected: true,
				Detections: []*filterv1.PIIDetection{
					{EntityType: "PERSON", Start: 0, End: 8, Score: 0.95},
				},
			}, nil
		},
	}
	c := clientWithMock(mock, false)
	req := &types.AegisRequest{
		Messages:       []types.Message{{Role: "user", Content: types.TextContent("John Doe lives at 123 Main St")}},
		Classification: "CONFIDENTIAL",
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionBlock {
		t.Errorf("expected ActionBlock for CONFIDENTIAL, got %s", result.Action)
	}
	if result.Detections != 1 {
		t.Errorf("expected 1 detection, got %d", result.Detections)
	}
}

func TestClient_PIIDetected_Restricted_Block(t *testing.T) {
	mock := &mockFilterClient{
		scanFunc: func(_ context.Context, _ *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error) {
			return &filterv1.ScanPIIResponse{
				Detected: true,
				Detections: []*filterv1.PIIDetection{
					{EntityType: "EMAIL_ADDRESS", Start: 10, End: 30, Score: 0.99},
				},
			}, nil
		},
	}
	c := clientWithMock(mock, false)
	req := &types.AegisRequest{
		Messages:       []types.Message{{Role: "user", Content: types.TextContent("Email: john@example.com")}},
		Classification: "RESTRICTED",
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionBlock {
		t.Errorf("expected ActionBlock for RESTRICTED, got %s", result.Action)
	}
}

func TestClient_PIIDetected_Internal_Flag(t *testing.T) {
	mock := &mockFilterClient{
		scanFunc: func(_ context.Context, _ *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error) {
			return &filterv1.ScanPIIResponse{
				Detected: true,
				Detections: []*filterv1.PIIDetection{
					{EntityType: "EMAIL_ADDRESS", Start: 10, End: 30, Score: 0.99},
				},
			}, nil
		},
	}
	c := clientWithMock(mock, false)
	req := &types.AegisRequest{
		Messages:       []types.Message{{Role: "user", Content: types.TextContent("Contact me at john@example.com")}},
		Classification: "INTERNAL",
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionFlag {
		t.Errorf("expected ActionFlag for INTERNAL, got %s", result.Action)
	}
}

func TestClient_GRPCError_FailClosed(t *testing.T) {
	mock := &mockFilterClient{
		scanFunc: func(_ context.Context, _ *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	c := clientWithMock(mock, false)
	req := &types.AegisRequest{
		Messages:       []types.Message{{Role: "user", Content: types.TextContent("test")}},
		Classification: "INTERNAL",
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionBlock {
		t.Errorf("expected ActionBlock on error (fail closed), got %s", result.Action)
	}
}

func TestClient_GRPCError_FailOpen(t *testing.T) {
	mock := &mockFilterClient{
		scanFunc: func(_ context.Context, _ *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	c := clientWithMock(mock, true)
	req := &types.AegisRequest{
		Messages:       []types.Message{{Role: "user", Content: types.TextContent("test")}},
		Classification: "INTERNAL",
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionPass {
		t.Errorf("expected ActionPass on error (fail open), got %s", result.Action)
	}
}

func TestClient_NotConnected_FailClosed(t *testing.T) {
	c := NewClient(func() config.PIIServiceConfig {
		return config.PIIServiceConfig{
			Enabled:  true,
			FailOpen: false,
		}
	})
	req := &types.AegisRequest{
		Messages: []types.Message{{Role: "user", Content: types.TextContent("test")}},
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionBlock {
		t.Errorf("expected ActionBlock when not connected (fail closed), got %s", result.Action)
	}
}

func TestClient_NotConnected_FailOpen(t *testing.T) {
	c := NewClient(func() config.PIIServiceConfig {
		return config.PIIServiceConfig{
			Enabled:  true,
			FailOpen: true,
		}
	})
	req := &types.AegisRequest{
		Messages: []types.Message{{Role: "user", Content: types.TextContent("test")}},
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionPass {
		t.Errorf("expected ActionPass when not connected (fail open), got %s", result.Action)
	}
}

func TestClient_Disabled(t *testing.T) {
	c := NewClient(func() config.PIIServiceConfig {
		return config.PIIServiceConfig{Enabled: false}
	})
	if c.Enabled() {
		t.Error("expected client to be disabled")
	}
}

func TestClassificationAction(t *testing.T) {
	tests := []struct {
		classification string
		detections     int
		want           filter.Action
	}{
		{"RESTRICTED", 1, filter.ActionBlock},
		{"CONFIDENTIAL", 2, filter.ActionBlock},
		{"INTERNAL", 1, filter.ActionFlag},
		{"PUBLIC", 1, filter.ActionFlag},
		{"INTERNAL", 0, filter.ActionPass},
	}
	for _, tt := range tests {
		got := classificationAction(tt.classification, tt.detections)
		if got != tt.want {
			t.Errorf("classificationAction(%s, %d) = %s, want %s", tt.classification, tt.detections, got, tt.want)
		}
	}
}

func TestClient_MultipleMessages_FirstPIIBlocks(t *testing.T) {
	callCount := 0
	mock := &mockFilterClient{
		scanFunc: func(_ context.Context, req *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error) {
			callCount++
			if callCount == 2 {
				return &filterv1.ScanPIIResponse{
					Detected: true,
					Detections: []*filterv1.PIIDetection{
						{EntityType: "PHONE_NUMBER", Start: 5, End: 17, Score: 0.9},
					},
				}, nil
			}
			return &filterv1.ScanPIIResponse{Detected: false}, nil
		},
	}
	c := clientWithMock(mock, false)
	req := &types.AegisRequest{
		Messages: []types.Message{
			{Role: "user", Content: types.TextContent("Hello there")},
			{Role: "user", Content: types.TextContent("Call 555-123-4567")},
		},
		Classification: "RESTRICTED",
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionBlock {
		t.Errorf("expected ActionBlock, got %s", result.Action)
	}
}

// TestClient_ScansWidenedToolSurface asserts the PII service is called with
// every text-bearing element of a request, not only with Message.Content.
//
// The PII filter is the one filter in the chain that cannot be exercised
// through the shared conformance tests in internal/filter, because it is a gRPC
// call out to the Presidio service. It therefore needs its own proof that
// widening the request shape did not leave it scanning a subset: a structured
// content part, a tool call's arguments and a tool result are all data an agent
// routinely carries personal information in.
func TestClient_ScansWidenedToolSurface(t *testing.T) {
	var scanned []string
	mock := &mockFilterClient{
		scanFunc: func(_ context.Context, in *filterv1.ScanPIIRequest) (*filterv1.ScanPIIResponse, error) {
			scanned = append(scanned, in.Text)
			return &filterv1.ScanPIIResponse{Detected: false}, nil
		},
	}
	c := clientWithMock(mock, false)

	req := &types.AegisRequest{
		Classification: "INTERNAL",
		Messages: []types.Message{
			{Role: types.RoleUser, Content: types.PartsContent("part-one", "part-two")},
			{
				Role: types.RoleAssistant,
				ToolCalls: []types.ToolCall{{
					ID:       "call_1",
					Type:     types.ToolTypeFunction,
					Function: types.FunctionCallSpec{Name: "lookup", Arguments: `{"q":"tool-args"}`},
				}},
			},
			{Role: types.RoleTool, ToolCallID: "call_1", Content: types.TextContent("tool-result")},
		},
		Tools: []types.Tool{{
			Type:     types.ToolTypeFunction,
			Function: types.FunctionDef{Name: "lookup", Description: "tool-description"},
		}},
	}

	if result := c.ScanRequest(context.Background(), req); result.Action != filter.ActionPass {
		t.Fatalf("expected ActionPass on a clean request, got %s", result.Action)
	}

	// Each marker sits in exactly one surface, so a missing marker names the
	// surface that went unscanned.
	for _, want := range []struct{ marker, surface string }{
		{"part-one", "first structured content part"},
		{"part-two", "second structured content part"},
		{"tool-args", "tool call arguments"},
		{"tool-result", "tool result content"},
		{"tool-description", "tool definition description"},
	} {
		found := false
		for _, got := range scanned {
			if strings.Contains(got, want.marker) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the PII service was never called with the %s — that surface reaches "+
				"the provider without being scanned for personal information", want.surface)
		}
	}
}
