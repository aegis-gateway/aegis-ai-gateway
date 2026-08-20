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

package filter

import (
	"context"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
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
