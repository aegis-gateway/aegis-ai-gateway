package filter

import (
	"context"

	"github.com/af-corp/aegis-gateway/internal/types"
)

// Action represents the filter decision.
type Action string

const (
	ActionPass   Action = "pass"
	ActionFlag   Action = "flag"
	ActionRedact Action = "redact"
	ActionBlock  Action = "block"
)

// Result is returned by each filter.
type Result struct {
	Action     Action
	FilterName string
	Message    string
	Detections int
	Score      float64
}

// Filter is the interface all content filters implement.
type Filter interface {
	Name() string
	Enabled() bool
	ScanRequest(ctx context.Context, req *types.AegisRequest) Result
}

// Chain runs filters in order, stopping on the first Block.
type Chain struct {
	filters []Filter
}

// NewChain creates a filter chain from the given filters.
func NewChain(filters ...Filter) *Chain {
	return &Chain{filters: filters}
}

// Run executes all enabled filters in order. Returns all results and a pointer
// to the first blocking result (nil if no filter blocked).
func (c *Chain) Run(ctx context.Context, req *types.AegisRequest) ([]Result, *Result) {
	var results []Result
	for _, f := range c.filters {
		if !f.Enabled() {
			continue
		}
		r := f.ScanRequest(ctx, req)
		results = append(results, r)
		if r.Action == ActionBlock {
			return results, &r
		}
	}
	return results, nil
}
