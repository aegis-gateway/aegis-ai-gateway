package injection

import "regexp"

// Rule defines a prompt injection detection pattern.
type Rule struct {
	Name     string
	Regex    *regexp.Regexp
	Severity float64 // 0.0 to 1.0
	Category string  // "instruction_bypass", "role_override", "encoding_trick", "output_steering"
}

// DefaultRules returns the built-in injection detection rules.
func DefaultRules() []Rule {
	return []Rule{
		{
			Name:     "ignore_previous",
			Regex:    regexp.MustCompile(`(?i)ignore\s+(all\s+)?previous\s+instructions`),
			Severity: 0.95,
			Category: "instruction_bypass",
		},
		{
			Name:     "disregard_prior",
			Regex:    regexp.MustCompile(`(?i)disregard\s+(all\s+)?prior\s+(instructions|context|rules)`),
			Severity: 0.95,
			Category: "instruction_bypass",
		},
		{
			Name:     "jailbreak",
			Regex:    regexp.MustCompile(`(?i)(DAN|do\s+anything\s+now|jailbreak|unrestricted\s+mode)`),
			Severity: 0.9,
			Category: "role_override",
		},
		{
			Name:     "code_block_system",
			Regex:    regexp.MustCompile("(?i)```system"),
			Severity: 0.9,
			Category: "role_override",
		},
		{
			Name:     "system_prefix",
			Regex:    regexp.MustCompile(`(?i)^\s*system\s*:\s*`),
			Severity: 0.85,
			Category: "role_override",
		},
		{
			Name:     "developer_mode",
			Regex:    regexp.MustCompile(`(?i)(developer|debug|admin|root)\s+mode\s+(enabled|activated|on)`),
			Severity: 0.85,
			Category: "role_override",
		},
		{
			Name:     "base64_instruction",
			Regex:    regexp.MustCompile(`(?i)(decode|execute|follow)\s+(the\s+)?base64`),
			Severity: 0.85,
			Category: "encoding_trick",
		},
		{
			Name:     "new_instructions",
			Regex:    regexp.MustCompile(`(?i)(new|updated|revised)\s+instructions?\s*:`),
			Severity: 0.8,
			Category: "instruction_bypass",
		},
		{
			Name:     "response_prefix",
			Regex:    regexp.MustCompile(`(?i)respond\s+with\s*:\s*(sure|absolutely|of course)`),
			Severity: 0.75,
			Category: "output_steering",
		},
		{
			Name:     "you_are_now",
			Regex:    regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|the)\s+`),
			Severity: 0.7,
			Category: "role_override",
		},
	}
}
