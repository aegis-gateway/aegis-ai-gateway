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
	"net/http"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

func defaultMessagesCfg() config.AnthropicMessagesConfig {
	return config.DefaultConfig().AnthropicMessages
}

// Every flag set Claude Code 2.1.150 was observed sending passes the defaults
// silently: print mode, interactive mode, the session-title request, /init,
// and an install with the advisor flag. Interactive Claude Code failed on
// every turn under the previous six-flag list.
func TestDecideBetas_ObservedClaudeCodeSetsForwardSilently(t *testing.T) {
	observed := []string{
		"claude-code-20250219,context-1m-2025-08-07,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24",
		"interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,claude-code-20250219,advanced-tool-use-2025-11-20,thinking-display-updates-2026-08-18",
		"interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,structured-outputs-2025-12-15",
		"redact-thinking-2026-02-12,claude-code-20250219",
		"interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,claude-code-20250219,advisor-tool-2026-03-01",
	}
	for _, h := range observed {
		d := decideBetas([]string{h}, defaultMessagesCfg())
		if d.Refused != "" || len(d.Unlisted) != 0 || len(d.Forward) != len(strings.Split(h, ",")) {
			t.Errorf("%s: %+v", h, d)
		}
	}
}

func TestDecideBetas_Paths(t *testing.T) {
	refuse := defaultMessagesCfg()
	refuse.UnlistedBetas = config.UnlistedBetasRefuse
	typo := defaultMessagesCfg()
	typo.UnlistedBetas = "foward"

	cases := []struct {
		name     string
		header   string
		cfg      config.AnthropicMessagesConfig
		refused  string
		reason   string
		unlisted []string
	}{
		{"unlisted forwarded by default", "claude-code-20250219,new-thing-2027-01-01", defaultMessagesCfg(), "", "", []string{"new-thing-2027-01-01"}},
		{"unlisted refused in refuse mode", "new-thing-2027-01-01", refuse, "new-thing-2027-01-01", audit.ReasonBetaNotAllowed, nil},
		{"unknown mode fails closed", "new-thing-2027-01-01", typo, "new-thing-2027-01-01", audit.ReasonBetaNotAllowed, nil},
		{"denied family by prefix", "files-api-2026-09-01", defaultMessagesCfg(), "files-api-2026-09-01", audit.ReasonBetaDenied, nil},
		{"denied beats allowed", "web-search-2025-03-05", func() config.AnthropicMessagesConfig {
			c := defaultMessagesCfg()
			c.AllowedBetas = append(c.AllowedBetas, "web-search-2025-03-05")
			return c
		}(), "web-search-2025-03-05", audit.ReasonBetaDenied, nil},
		{"malformed: uppercase key", "AKIAIOSFODNN7EXAMPLE", defaultMessagesCfg(), "AKIAIOSFODNN7EXAMPLE", audit.ReasonBetaMalformed, nil},
		{"malformed: no date", "some-flag", defaultMessagesCfg(), "some-flag", audit.ReasonBetaMalformed, nil},
		{"malformed even in forward mode", "free text-2025-01-01", defaultMessagesCfg(), "free text-2025-01-01", audit.ReasonBetaMalformed, nil},
		{"malformed: underscore", "sk_live_x-2025-01-01", defaultMessagesCfg(), "sk_live_x-2025-01-01", audit.ReasonBetaMalformed, nil},
		{"empty entries ignored", " , claude-code-20250219 ,", defaultMessagesCfg(), "", "", nil},
	}
	for _, c := range cases {
		d := decideBetas([]string{c.header}, c.cfg)
		if d.Refused != c.refused || d.Reason != c.reason || strings.Join(d.Unlisted, ",") != strings.Join(c.unlisted, ",") {
			t.Errorf("%s: got %+v", c.name, d)
		}
	}
}

func TestBetaLabel_Capped(t *testing.T) {
	betaLabelsMu.Lock()
	saved := betaLabels
	betaLabels = map[string]bool{}
	betaLabelsMu.Unlock()
	defer func() { betaLabelsMu.Lock(); betaLabels = saved; betaLabelsMu.Unlock() }()

	for i := 0; i < maxBetaLabels; i++ {
		if l := betaLabel("f" + strings.Repeat("x", i) + "-2027-01-01"); l == "other" {
			t.Fatalf("label %d collapsed early", i)
		}
	}
	if betaLabel("one-more-2027-01-01") != "other" {
		t.Error("cap not applied")
	}
	if betaLabel("f-2027-01-01") != "f-2027-01-01" {
		t.Error("a label already counted must keep its name")
	}
}

// End to end: an unlisted, well-formed flag reaches the provider, nothing is
// sealed for it, and the request completes.
func TestMessages_UnlistedBetaForwardedAndNotSealed(t *testing.T) {
	f := newFakeAnthropic(t)
	f.body = `{"type":"message","model":"claude-haiku-4-5-20251001","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`
	spy := &messagesAuditSpy{}
	h := newMessagesHandler(t, f, spy)
	w := postMessages(t, h, "/v1/messages", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"anthropic-beta": "claude-code-20250219,brand-new-2027-01-01"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.headers[0].Get("anthropic-beta"); got != "brand-new-2027-01-01,claude-code-20250219" {
		t.Errorf("provider got betas %q", got)
	}
	if len(spy.completes) != 1 || len(spy.failures) != 0 {
		t.Errorf("completes %d failures %v", len(spy.completes), spy.failures)
	}
}

// The forward path is visible: the unlisted flag is counted by name.
func TestMessages_UnlistedBetaCounted(t *testing.T) {
	f := newFakeAnthropic(t)
	f.body = `{"type":"message","model":"claude-haiku-4-5-20251001","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`
	h := newMessagesHandler(t, f, &messagesAuditSpy{})
	h.metrics = getTestMetrics()
	const flag = "counted-flag-2027-02-02"
	before := testutil.ToFloat64(h.metrics.AnthropicBetaUnlistedTotal.WithLabelValues(betaLabel(flag)))
	w := postMessages(t, h, "/v1/messages", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"anthropic-beta": flag})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if got := testutil.ToFloat64(h.metrics.AnthropicBetaUnlistedTotal.WithLabelValues(betaLabel(flag))); got != before+1 {
		t.Errorf("counter %v, want %v", got, before+1)
	}
}
