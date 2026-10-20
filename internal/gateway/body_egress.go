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
	"context"
	"net/http/httptrace"
	"sync/atomic"
)

// bodyEgress records whether a request body was actually written to a
// provider.
//
// The unscanned-part columns of audit_events say what left the gateway. A
// provider failure can happen before anything leaves: DNS, connect and TLS
// failures all fail SendRequest with no byte of the body on the wire. Sealing
// the metadata on such a failure would attest an egress that did not happen,
// so a provider_failure record carries it only when this saw a write.
//
// It is fed by net/http/httptrace: WroteRequest fires once the transport has
// written the request, headers and body, and reports any write error. The
// adapters build every request with http.NewRequestWithContext and send it
// through a standard http.Transport (router.BuildFromConfig), both of which
// carry the trace, HTTP/2 included. Across retries it is sticky: once any
// attempt wrote the body, the body left, whatever later attempts did.
type bodyEgress struct {
	written atomic.Bool
}

// withBodyEgress returns ctx carrying a client trace that records a
// successful request write into eg. Requests built from the returned context,
// or from contexts derived from it, report to eg.
func withBodyEgress(ctx context.Context, eg *bodyEgress) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				eg.written.Store(true)
			}
		},
	})
}

// Written reports whether any traced attempt wrote the request body. A nil
// receiver reports false.
func (eg *bodyEgress) Written() bool {
	return eg != nil && eg.written.Load()
}
