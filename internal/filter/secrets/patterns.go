package secrets

import "regexp"

// Pattern defines a secret detection pattern.
type Pattern struct {
	Name  string
	Regex *regexp.Regexp
}

// DefaultPatterns returns the built-in secret detection patterns.
func DefaultPatterns() []Pattern {
	return []Pattern{
		{
			Name:  "AWS Access Key",
			Regex: regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		},
		{
			Name:  "GCP Service Account Key",
			Regex: regexp.MustCompile(`"private_key":\s*"-----BEGIN`),
		},
		{
			Name:  "GitHub Token",
			Regex: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{36,}`),
		},
		{
			Name:  "Stripe Secret Key",
			Regex: regexp.MustCompile(`sk_live_[A-Za-z0-9]{24,}`),
		},
		{
			Name:  "Private Key",
			Regex: regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA )?PRIVATE KEY-----`),
		},
		{
			Name:  "Connection String",
			Regex: regexp.MustCompile(`(?:postgres|mysql|mongodb|redis)://[^\s]+`),
		},
		{
			Name:  "JWT Token",
			Regex: regexp.MustCompile(`eyJ[A-Za-z0-9\-_]+\.eyJ[A-Za-z0-9\-_]+\.[A-Za-z0-9\-_]+`),
		},
	}
}
