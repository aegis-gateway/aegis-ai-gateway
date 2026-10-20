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

package audit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func sourceThrough(t *testing.T, register func(chi.Router, http.HandlerFunc), method, target string) (Source, bool) {
	t.Helper()
	var got Source
	called := false
	r := chi.NewRouter()
	r.Group(func(g chi.Router) {
		// A group middleware, as auth and rate limiting are, must already see
		// the matched pattern.
		g.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				got, called = SourceOf(req), true
				next.ServeHTTP(w, req)
			})
		})
		register(g, func(http.ResponseWriter, *http.Request) {})
	})
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "203.0.113.9:4321"
	r.ServeHTTP(httptest.NewRecorder(), req)
	return got, called
}

func TestSourceOf_RecordsTheRoutePatternSeenByGroupMiddleware(t *testing.T) {
	src, called := sourceThrough(t, func(r chi.Router, h http.HandlerFunc) { r.Post("/v1/messages", h) },
		http.MethodPost, "/v1/messages?beta=true")
	if !called {
		t.Fatal("middleware not reached")
	}
	if src.Endpoint != "/v1/messages" || src.Method != "POST" || src.IP != "203.0.113.9:4321" {
		t.Errorf("source %+v", src)
	}
}

// The sealed endpoint is the configured pattern, never the caller's spelling of
// the path.
func TestSourceOf_PatternNotCallerPath(t *testing.T) {
	src, called := sourceThrough(t, func(r chi.Router, h http.HandlerFunc) { r.Get("/aegis/v1/audit/{kind}", h) },
		http.MethodGet, "/aegis/v1/audit/SENTINEL_caller_text")
	if !called || src.Endpoint != "/aegis/v1/audit/{kind}" {
		t.Errorf("endpoint %q, want the pattern", src.Endpoint)
	}
}

func TestSourceOf_NoRouteContextAndOddMethod(t *testing.T) {
	req := httptest.NewRequest("PROPFIND", "/v1/SENTINEL", nil)
	src := SourceOf(req)
	if src.Endpoint != UnroutedEndpoint || src.Method != "OTHER" {
		t.Errorf("source %+v", src)
	}
}
