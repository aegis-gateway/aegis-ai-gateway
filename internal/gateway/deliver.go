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
	"errors"
	"net/http"
)

// flushToClient pushes whatever is buffered in the ResponseWriter out to the
// network and reports whether that succeeded.
//
// A successful Write is not delivery. net/http may hold a small response in its
// server-side buffer until the handler returns, so an encode that reports no
// error can still be followed by a failed socket write, and http.Flusher.Flush
// discards the error entirely. Both are invisible to a caller that only checks
// the write.
//
// WHAT A SUCCESSFUL FLUSH DOES AND DOES NOT PROVE. It proves the gateway handed
// the bytes to the local kernel without error. It does NOT prove the peer
// received them: a client that disconnects after the kernel accepts the write
// but before the data is delivered or read produces a TCP reset that surfaces
// later or not at all, and Flush returns nil in that window.
//
// Remote receipt is not knowable from an HTTP handler. Establishing it would
// need an application-level acknowledgement, which an OpenAI-compatible client
// does not send. So the attested claim is the supportable one, that the gateway
// wrote and flushed the response without error, and the audit documentation
// says exactly that rather than implying receipt.
//
// This is still worth checking. It is the strongest signal available at this
// layer and it catches the common cases: a peer that has already gone, and a
// writer that fails outright.
//
// ErrNotSupported is deliberately NOT a delivery failure. It means this writer
// cannot be flushed on demand, so nothing has been learned either way, and
// treating "cannot confirm" as "failed" would attest a failure that may not
// have happened. Every wrapper in this codebase supports flushing; a future one
// that does not should degrade to the old best-effort behaviour rather than
// start recording false failures.
func flushToClient(w http.ResponseWriter) error {
	if err := http.NewResponseController(w).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}
