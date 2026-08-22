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

package secrets

import (
	"context"
	"fmt"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// Detection represents a detected secret in text.
type Detection struct {
	PatternName string // e.g. "AWS Access Key"
	Start       int    // byte offset
	End         int    // byte offset
}

// Scanner scans text for secrets using pre-compiled regex patterns.
type Scanner struct {
	patterns []Pattern
}

// NewScanner creates a scanner with the default secret patterns.
func NewScanner() *Scanner {
	return &Scanner{patterns: DefaultPatterns()}
}

// Scan checks a single text string for secrets and returns all detections.
func (s *Scanner) Scan(text string) []Detection {
	var detections []Detection
	for _, p := range s.patterns {
		locs := p.Regex.FindAllStringIndex(text, -1)
		for _, loc := range locs {
			detections = append(detections, Detection{
				PatternName: p.Name,
				Start:       loc[0],
				End:         loc[1],
			})
		}
	}
	return detections
}

// ScanMessages scans all messages for secrets. Returns all detections found.
func (s *Scanner) ScanMessages(messages []types.Message) []Detection {
	var detections []Detection
	for _, m := range messages {
		detections = append(detections, s.Scan(m.Content)...)
	}
	return detections
}

// SecretsFilter wraps Scanner to implement filter.Filter.
type SecretsFilter struct {
	scanner *Scanner
	enabled func() bool
}

// NewFilter creates a SecretsFilter that implements the filter.Filter interface.
func NewFilter(enabled func() bool) *SecretsFilter {
	return &SecretsFilter{scanner: NewScanner(), enabled: enabled}
}

func (f *SecretsFilter) Name() string  { return "secrets" }
func (f *SecretsFilter) Enabled() bool { return f.enabled() }

func (f *SecretsFilter) ScanRequest(_ context.Context, req *types.AegisRequest) filter.Result {
	detections := f.scanner.ScanMessages(req.Messages)
	if len(detections) == 0 {
		return filter.Result{Action: filter.ActionPass, FilterName: "secrets"}
	}
	seen := map[string]bool{}
	for _, d := range detections {
		seen[d.PatternName] = true
	}
	secretTypes := ""
	for name := range seen {
		if secretTypes != "" {
			secretTypes += ", "
		}
		secretTypes += name
	}
	return filter.Result{
		Action:     filter.ActionBlock,
		FilterName: "secrets",
		Message:    fmt.Sprintf("Request blocked: detected %d secret(s) of type: %s", len(detections), secretTypes),
		Detections: len(detections),
	}
}
