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
