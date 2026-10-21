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

package gateway

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

// betaFlagPattern is the shape of every anthropic-beta flag: a lowercase name
// and a release date, written YYYY-MM-DD or, as claude-code-20250219 does,
// YYYYMMDD. A value outside it is refused whatever the
// configuration. That is also what makes it safe to log a flag by name and use
// it as a metric label: the pattern admits no uppercase, no underscore and
// nothing over 75 characters, which excludes the credential formats the
// secrets filter knows and any free text.
var betaFlagPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}-(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{8})$`)

// betaDecision is the outcome of applying the beta policy to a request.
type betaDecision struct {
	// Forward are the flags to send to the provider, sorted.
	Forward []string
	// Unlisted are the forwarded flags that were on neither list.
	Unlisted []string
	// Refused is the first refused value; empty if none was refused.
	Refused string
	// Reason is the sealed audit reason for a refusal.
	Reason string
}

// decideBetas applies anthropic_messages to the request's anthropic-beta
// headers. Per flag, in order: malformed is refused; a denied_betas match is
// refused; an allowed_betas match is forwarded; anything else is forwarded or
// refused by unlisted_betas, with any value but "forward" meaning refuse.
func decideBetas(headers []string, cfg config.AnthropicMessagesConfig) betaDecision {
	allowed := make(map[string]bool, len(cfg.AllowedBetas))
	for _, b := range cfg.AllowedBetas {
		allowed[b] = true
	}
	forwardUnlisted := cfg.UnlistedBetas == config.UnlistedBetasForward

	var d betaDecision
	for _, header := range headers {
		for _, b := range strings.Split(header, ",") {
			b = strings.TrimSpace(b)
			switch {
			case b == "":
				continue
			case !betaFlagPattern.MatchString(b):
				return betaDecision{Refused: b, Reason: audit.ReasonBetaMalformed}
			case betaDenied(b, cfg.DeniedBetas):
				return betaDecision{Refused: b, Reason: audit.ReasonBetaDenied}
			case allowed[b]:
				d.Forward = append(d.Forward, b)
			case forwardUnlisted:
				d.Forward = append(d.Forward, b)
				d.Unlisted = append(d.Unlisted, b)
			default:
				return betaDecision{Refused: b, Reason: audit.ReasonBetaNotAllowed}
			}
		}
	}
	sort.Strings(d.Forward)
	return d
}

// betaDenied reports whether flag matches a denied_betas entry, exactly or, for
// an entry ending in *, by prefix.
func betaDenied(flag string, denied []string) bool {
	for _, p := range denied {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(flag, prefix) {
				return true
			}
		} else if flag == p {
			return true
		}
	}
	return false
}

// maxBetaLabels caps the distinct flag labels on the unlisted-beta metric. A
// flag name is client input, so without a cap a caller could mint a series
// per request; past the cap every new name is counted as "other".
const maxBetaLabels = 32

var (
	betaLabelsMu sync.Mutex
	betaLabels   = map[string]bool{}
)

func betaLabel(flag string) string {
	betaLabelsMu.Lock()
	defer betaLabelsMu.Unlock()
	if betaLabels[flag] {
		return flag
	}
	if len(betaLabels) >= maxBetaLabels {
		return "other"
	}
	betaLabels[flag] = true
	return flag
}
