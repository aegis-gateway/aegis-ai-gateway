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
// That matters here because the audit record claims what the caller received.
// http.NewResponseController surfaces the flush error, which is the last point
// at which the gateway can still tell.
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
