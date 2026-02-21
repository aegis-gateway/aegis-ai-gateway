package secrets

import (
	"github.com/af-corp/aegis-gateway/internal/types"
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

// ScanMessages scans all messages for secrets. Returns the first detection found
// (we only need to know if any secret is present to block the request).
func (s *Scanner) ScanMessages(messages []types.Message) []Detection {
	var detections []Detection
	for _, m := range messages {
		detections = append(detections, s.Scan(m.Content)...)
	}
	return detections
}
