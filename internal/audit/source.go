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

	"github.com/go-chi/chi/v5"
)

// UnroutedEndpoint is recorded when a request reached the audit trail without a
// matched route, which the router does not do for any audited handler.
const UnroutedEndpoint = "(unrouted)"

// Source is where a refused request came in: the client address, and the
// endpoint and method it was refused on. Every refusal row carries it, so a
// refusal on /v1/messages can be told from one on /v1/chat/completions.
type Source struct {
	IP       string
	Endpoint string
	Method   string
}

// SourceOf returns the Source of r.
//
// Endpoint is the matched route PATTERN, not r.URL.Path. The path is what the
// caller sent and the pattern is what the gateway configured: for every audited
// route the two name the same endpoint, but only the pattern is guaranteed to
// be a literal from cmd/gateway/main.go, and audit_events.endpoint is sealed.
// An encoded or unusual path must not become caller text in the record.
func SourceOf(r *http.Request) Source {
	endpoint := UnroutedEndpoint
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			endpoint = p
		}
	}
	method := r.Method
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodHead, http.MethodOptions:
	default:
		// The method token is client-supplied too. Audited routes are GET and
		// POST; anything else is recorded as a fixed value.
		method = "OTHER"
	}
	return Source{IP: r.RemoteAddr, Endpoint: endpoint, Method: method}
}
