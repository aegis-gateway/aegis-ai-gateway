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
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
)

// AuditHandler serves the audit read API.
//
// The API exists so that the decision record is something an operator can
// actually get out of the system, in a form they can hand to an auditor,
// without reaching into Postgres. It is read-only by construction: the Reader
// exposes no write path.
type AuditHandler struct {
	reader *audit.Reader
}

// NewAuditHandler creates an AuditHandler. A nil reader disables the endpoints,
// which is what happens when the gateway runs without a database.
func NewAuditHandler(reader *audit.Reader) *AuditHandler {
	return &AuditHandler{reader: reader}
}

// auditListResponse is the JSON envelope. NextBefore is the cursor to pass back
// as ?before_id= to page further; it is absent on the last page.
type auditListResponse struct {
	Object     string `json:"object"`
	Data       any    `json:"data"`
	NextBefore *int64 `json:"next_before_id,omitempty"`
}

// Events serves GET /aegis/v1/audit/events.
func (h *AuditHandler) Events(w http.ResponseWriter, r *http.Request) {
	reqID := w.Header().Get("X-Request-ID")
	authInfo, filter, format, ok := h.parse(w, r, reqID)
	if !ok {
		return
	}

	rows, err := h.reader.QueryEvents(r.Context(), authInfo.OrganizationID, filter)
	if err != nil {
		httputil.WriteInternalError(w, reqID, "Failed to read audit events")
		return
	}

	if format == "csv" {
		writeCSV(w, reqID, "audit_events",
			[]string{"id", "request_id", "timestamp", "event_type", "organization_id",
				"team_id", "user_id", "api_key_id", "ip_address", "endpoint", "method",
				"status_code", "error_message",
				"api_key_prefix", "limit_dimension", "limit_value", "spent_cents",
				"limit_cents", "filter_type", "reason", "provider", "model", "mode",
				"operation", "error_detail"},
			func(yield func([]string) error) error {
				for _, e := range rows {
					if err := yield([]string{
						strconv.FormatInt(e.ID, 10), e.RequestID,
						e.Timestamp.UTC().Format(time.RFC3339Nano), e.EventType,
						deref(e.OrganizationID), deref(e.TeamID), deref(e.UserID),
						deref(e.APIKeyID), deref(e.IPAddress), deref(e.Endpoint),
						deref(e.Method), derefInt(e.StatusCode), deref(e.ErrorMessage),
						deref(e.APIKeyPrefix), deref(e.LimitDimension), derefInt64(e.LimitValue),
						derefInt64(e.SpentCents), derefInt64(e.LimitCents), deref(e.FilterType),
						deref(e.Reason), deref(e.Provider), deref(e.Model), deref(e.Mode),
						deref(e.Operation), deref(e.ErrorDetail),
					}); err != nil {
						return err
					}
				}
				return nil
			})
		return
	}

	var next *int64
	if len(rows) == filter.Limit {
		next = &rows[len(rows)-1].ID
	}
	writeJSON(w, auditListResponse{Object: "list", Data: rows, NextBefore: next})
}

// Logs serves GET /aegis/v1/audit/logs, which is retired.
//
// The endpoint returned an empty list for its whole life. Nothing has ever
// written audit_logs: the table is created by migration 002 and a
// repository-wide search finds it only in the migration, the reader, and purge.
// Because the handler emitted a full 21-column CSV header before the empty
// body, an operator exporting the decision record got a well-formed file with
// no rows, which reads as "no activity" rather than "this endpoint does not
// work". That is worse than an error.
//
// 410 rather than 404: the route existed, was documented, and is deliberately
// gone. A 404 would suggest a typo and invite a retry.
//
// The access gate is kept. Nothing here reads a row, so scoping has nothing to
// protect, but weakening an access check as a side effect of a deprecation is
// not a trade worth making for one line: TestAuditHandler_RefusesSentinelOrg
// asserts both audit routes refuse the unattributed-org sentinel, and it still
// does. Query parameters are no longer validated, because validating the
// arguments of a retired endpoint tells the caller nothing useful.
// GET /aegis/v1/audit/events is unchanged.
//
// The audit_logs table is deliberately left in place. Purge, the schema guard
// and migration history all reference it, and dropping it is a separate and
// riskier change. See docs/evidence/known-limitations.md section 2.11.
func (h *AuditHandler) Logs(w http.ResponseWriter, r *http.Request) {
	reqID := w.Header().Get("X-Request-ID")
	if _, ok := h.authorize(w, r, reqID); !ok {
		return
	}
	httputil.WriteGoneError(w, reqID,
		"GET /aegis/v1/audit/logs is retired: audit_logs was never written and this "+
			"endpoint always returned an empty list. Use GET /aegis/v1/audit/events, "+
			"which carries the decision record")
}

// authorize is the access gate every audit read shares, including the retired
// one. It writes the error response itself and reports false when the caller
// must not be served.
func (h *AuditHandler) authorize(w http.ResponseWriter, r *http.Request, reqID string) (*auth.AuthInfo, bool) {
	authInfo, ok := auth.AuthFromContext(r.Context())
	if !ok {
		httputil.WriteAuthError(w, reqID, "Not authenticated")
		return nil, false
	}
	// An organization-less key cannot be scoped, so it is refused rather than
	// served an unscoped query.
	if authInfo.OrganizationID == "" {
		httputil.WriteAuthError(w, reqID, "API key carries no organization; cannot scope an audit query")
		return nil, false
	}
	// audit.UnattributedOrg is the sentinel recorded for events that happen
	// before a caller is identified. It is not a tenant, and a key carrying it
	// would otherwise scope to every authentication failure in the deployment.
	// cmd/keygen takes -org as a free string, so such a key is one typo away.
	if authInfo.OrganizationID == audit.UnattributedOrg {
		httputil.WriteAuthError(w, reqID,
			"API key organization is the reserved sentinel for unattributed events; cannot scope an audit query")
		return nil, false
	}
	return authInfo, true
}

// parse pulls auth, filter and format off the request, writing the error
// response itself and reporting false when the request cannot be served.
func (h *AuditHandler) parse(w http.ResponseWriter, r *http.Request, reqID string) (*auth.AuthInfo, audit.ReadFilter, string, bool) {
	var zero audit.ReadFilter

	authInfo, ok := h.authorize(w, r, reqID)
	if !ok {
		return nil, zero, "", false
	}

	q := r.URL.Query()

	format := strings.ToLower(q.Get("format"))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		httputil.WriteBadRequestError(w, reqID, "format must be json or csv")
		return nil, zero, "", false
	}

	filter := audit.ReadFilter{
		EventType: q.Get("event_type"),
		RequestID: q.Get("request_id"),
	}

	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				httputil.WriteBadRequestError(w, reqID,
					fmt.Sprintf("%s must be an RFC3339 timestamp", p.name))
				return nil, zero, "", false
			}
			*p.dst = t
		}
	}

	if v := q.Get("before_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			httputil.WriteBadRequestError(w, reqID, "before_id must be a non-negative integer")
			return nil, zero, "", false
		}
		filter.BeforeID = n
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httputil.WriteBadRequestError(w, reqID, "limit must be a positive integer")
			return nil, zero, "", false
		}
		if n > audit.MaxReadLimit {
			httputil.WriteBadRequestError(w, reqID,
				fmt.Sprintf("limit must not exceed %d", audit.MaxReadLimit))
			return nil, zero, "", false
		}
		filter.Limit = n
	}
	// Resolve the effective limit here so the caller can compare it against the
	// row count to decide whether another page exists.
	if filter.Limit == 0 {
		filter.Limit = audit.DefaultReadLimit
	}

	// Availability is checked last, once the request is known to be well formed.
	// A malformed query is a 400 whether or not the database happens to be up,
	// and reporting 503 for it would send the caller chasing an outage that is
	// not there.
	if h.reader == nil {
		httputil.WriteServiceUnavailableError(w, reqID,
			"Audit read API unavailable: the gateway is running without a database")
		return nil, zero, "", false
	}

	return authInfo, filter, format, true
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// writeCSV streams rows with a header. Headers are set before the first write,
// because once a row is flushed the status code can no longer change.
func writeCSV(w http.ResponseWriter, reqID, name string, header []string, rows func(func([]string) error) error) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", name+"-"+time.Now().UTC().Format("20060102")+".csv"))

	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return
	}
	if err := rows(cw.Write); err != nil {
		return
	}
	cw.Flush()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefInt64 renders a nullable counter column for CSV. An absent value becomes
// an empty cell rather than "0", because a rate-limit event that carries no
// limit is not an event whose limit was zero.
func derefInt64(i *int64) string {
	if i == nil {
		return ""
	}
	return strconv.FormatInt(*i, 10)
}

func derefInt(i *int) string {
	if i == nil {
		return ""
	}
	return strconv.Itoa(*i)
}
