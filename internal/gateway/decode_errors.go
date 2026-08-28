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
	"log/slog"
	"net/http"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// writeDecodeError turns a request decode failure into a client response.
//
// The three failure classes get distinct error codes because they need
// distinct actions from whoever reads the response. An unsupported field means
// remove the field or stop relying on it. A non-text content part means the
// gateway cannot inspect that data and will not forward it. Malformed JSON
// means fix the body.
func (h *Handler) writeDecodeError(w http.ResponseWriter, reqID, orgID string, err error) {
	var unsupported *types.UnsupportedFieldError
	if errors.As(err, &unsupported) {
		slog.Warn("request refused: unsupported field",
			"request_id", reqID,
			"field", unsupported.Field,
			"path", unsupported.Path,
			"org_id", orgID,
		)
		if h.metrics != nil {
			h.metrics.RecordUnsupportedField(types.MetricFieldLabel(unsupported.Field))
		}
		httputil.WriteError(w, reqID, http.StatusBadRequest,
			"invalid_request_error", "unsupported_field", err.Error())
		return
	}

	var nonText *types.NonTextPartError
	if errors.As(err, &nonText) {
		slog.Warn("request refused: non-text content part",
			"request_id", reqID,
			"part_type", nonText.Type,
			"org_id", orgID,
		)
		if h.metrics != nil {
			h.metrics.RecordUnsupportedField("content_part_type")
		}
		httputil.WriteError(w, reqID, http.StatusBadRequest,
			"invalid_request_error", "unsupported_content_part", err.Error())
		return
	}

	httputil.WriteBadRequestError(w, reqID, "Invalid JSON: "+err.Error())
}
