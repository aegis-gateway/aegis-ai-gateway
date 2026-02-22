package injection

import (
	"context"
	"fmt"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/filter"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// Detection records a matched injection pattern.
type Detection struct {
	RuleName string
	Severity float64
	Category string
	Start    int
	End      int
}

// Scanner scans text for prompt injection patterns.
type Scanner struct {
	rules []Rule
	cfg   func() config.InjectionFilterConfig
}

// NewScanner creates a prompt injection scanner.
func NewScanner(cfg func() config.InjectionFilterConfig) *Scanner {
	return &Scanner{rules: DefaultRules(), cfg: cfg}
}

func (s *Scanner) Name() string  { return "injection" }
func (s *Scanner) Enabled() bool { return s.cfg().Enabled }

// Scan checks a single text string and returns all detections.
func (s *Scanner) Scan(text string) []Detection {
	var detections []Detection
	for _, r := range s.rules {
		locs := r.Regex.FindAllStringIndex(text, -1)
		for _, loc := range locs {
			detections = append(detections, Detection{
				RuleName: r.Name,
				Severity: r.Severity,
				Category: r.Category,
				Start:    loc[0],
				End:      loc[1],
			})
		}
	}
	return detections
}

// ScanMessages scans all messages and returns detections and the max severity score.
func (s *Scanner) ScanMessages(messages []types.Message) ([]Detection, float64) {
	var allDetections []Detection
	maxScore := 0.0
	for _, m := range messages {
		detections := s.Scan(m.Content)
		allDetections = append(allDetections, detections...)
		for _, d := range detections {
			if d.Severity > maxScore {
				maxScore = d.Severity
			}
		}
	}
	return allDetections, maxScore
}

// ScanRequest implements filter.Filter.
func (s *Scanner) ScanRequest(_ context.Context, req *types.AegisRequest) filter.Result {
	detections, score := s.ScanMessages(req.Messages)
	cfg := s.cfg()

	if score >= cfg.BlockThreshold {
		return filter.Result{
			Action:     filter.ActionBlock,
			FilterName: "injection",
			Message:    fmt.Sprintf("Request blocked: prompt injection detected (score %.2f)", score),
			Detections: len(detections),
			Score:      score,
		}
	}
	if score >= cfg.FlagThreshold {
		return filter.Result{
			Action:     filter.ActionFlag,
			FilterName: "injection",
			Detections: len(detections),
			Score:      score,
		}
	}
	return filter.Result{Action: filter.ActionPass, FilterName: "injection", Score: score}
}

// InjectionClassifier is the ML classifier interface for Phase 2.
type InjectionClassifier interface {
	Score(text string) (float64, error)
}

// NoOpClassifier always returns 0.0.
type NoOpClassifier struct{}

func (n *NoOpClassifier) Score(_ string) (float64, error) { return 0.0, nil }
