package pii

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/filter"
	filterv1 "github.com/af-corp/aegis-gateway/gen/filter/v1"
	"github.com/af-corp/aegis-gateway/internal/types"
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
		Messages:       []types.Message{{Role: "user", Content: "Hello world"}},
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
		Messages:       []types.Message{{Role: "user", Content: "John Doe lives at 123 Main St"}},
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
		Messages:       []types.Message{{Role: "user", Content: "Email: john@example.com"}},
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
		Messages:       []types.Message{{Role: "user", Content: "Contact me at john@example.com"}},
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
		Messages:       []types.Message{{Role: "user", Content: "test"}},
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
		Messages:       []types.Message{{Role: "user", Content: "test"}},
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
		Messages: []types.Message{{Role: "user", Content: "test"}},
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
		Messages: []types.Message{{Role: "user", Content: "test"}},
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
			{Role: "user", Content: "Hello there"},
			{Role: "user", Content: "Call 555-123-4567"},
		},
		Classification: "RESTRICTED",
	}
	result := c.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionBlock {
		t.Errorf("expected ActionBlock, got %s", result.Action)
	}
}
