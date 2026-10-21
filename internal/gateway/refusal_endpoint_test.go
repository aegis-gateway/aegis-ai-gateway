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
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// sourceSpy records the Source of every refusal.
type sourceSpy struct {
	messagesAuditSpy
	mu      sync.Mutex
	sources []string
	feature []string
}

func (s *sourceSpy) LogFilterBlock(_, _, _, _, filterType, _ string, src audit.Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources = append(s.sources, "filter_block "+filterType+" "+src.Method+" "+src.Endpoint)
}

func (s *sourceSpy) LogFeatureDenied(_, _, _, _, _, reason string, src audit.Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feature = append(s.feature, reason+" "+src.Method+" "+src.Endpoint)
}

// routedHandler mounts the handler the way cmd/gateway does, so the route
// pattern the audit record uses comes from a real router.
func routedHandler(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(g chi.Router) {
		g.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
					OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test", KeyPrefix: "aegis-dev-pfx",
					MaxClassification: types.ClassInternal,
				})))
			})
		})
		g.Post("/v1/chat/completions", h.ChatCompletions)
		g.Post("/v1/messages", h.Messages)
	})
	return r
}

// The defect: a refusal row did not say which endpoint refused it, so a block
// on /v1/messages could not be told from one on /v1/chat/completions.
func TestRefusalRowsRecordTheEndpoint(t *testing.T) {
	f := newFakeAnthropic(t)
	spy := &sourceSpy{}
	srv := routedHandler(newMessagesHandler(t, f, spy))

	post := func(path, body string, hdr map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	secret := "key AKIAIOSFODNN7EXAMPLE"
	if c := post("/v1/chat/completions", `{"model":"aegis-claude","messages":[{"role":"user","content":"`+secret+`"}]}`, nil); c != 451 {
		t.Fatalf("chat: %d", c)
	}
	if c := post("/v1/messages", `{"model":"aegis-claude","max_tokens":5,"messages":[{"role":"user","content":"`+secret+`"}]}`, nil); c != 451 {
		t.Fatalf("messages: %d", c)
	}
	want := []string{
		"filter_block secrets POST /v1/chat/completions",
		"filter_block secrets POST /v1/messages",
	}
	if strings.Join(spy.sources, "|") != strings.Join(want, "|") {
		t.Errorf("sealed sources %v, want %v", spy.sources, want)
	}
}

// A refused beta is a governance refusal and is sealed, with a fixed reason
// and never the flag. One case per refusal path: a denied family, and a
// malformed value carrying caller text.
func TestRefusedBetaIsSealedWithoutTheFlag(t *testing.T) {
	cases := []struct{ beta, reason string }{
		{"files-api-2025-04-14", audit.ReasonBetaDenied},
		{"SENTINEL-beta-2099-01-01", audit.ReasonBetaMalformed},
	}
	for _, c := range cases {
		f := newFakeAnthropic(t)
		spy := &sourceSpy{}
		srv := routedHandler(newMessagesHandler(t, f, spy))

		req := httptest.NewRequest(http.MethodPost, "/v1/messages",
			bytes.NewBufferString(`{"model":"aegis-claude","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("anthropic-beta", "claude-code-20250219,"+c.beta)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", c.beta, w.Code)
		}
		if len(spy.feature) != 1 || spy.feature[0] != c.reason+" POST /v1/messages" {
			t.Errorf("%s: sealed %v", c.beta, spy.feature)
		}
		if strings.Contains(strings.Join(spy.feature, ""), c.beta) {
			t.Errorf("%s: the refused flag reached the audit record", c.beta)
		}
		if f.calls() != 0 {
			t.Errorf("%s: provider called", c.beta)
		}
	}
}
