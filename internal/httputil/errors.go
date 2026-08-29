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

package httputil

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// HTTPError represents an HTTP error with status code and message.
type HTTPError struct {
	StatusCode int
	Message    string
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// NewHTTPError creates a new HTTP error.
func NewHTTPError(statusCode int, message string) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Message:    message,
	}
}

// APIError matches the OpenAI error response format.
type APIError struct {
	Error APIErrorBody `json:"error"`
}

type APIErrorBody struct {
	Message    string `json:"message"`
	Type       string `json:"type"`
	Code       string `json:"code"`
	AegisReqID string `json:"aegis_request_id,omitempty"`
}

func WriteError(w http.ResponseWriter, requestID string, statusCode int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(APIError{
		Error: APIErrorBody{
			Message:    message,
			Type:       errType,
			Code:       code,
			AegisReqID: requestID,
		},
	})
}

func WriteAuthError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, http.StatusUnauthorized, "authentication_error", "invalid_api_key", message)
}

func WriteRateLimitError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", message)
}

func WriteBadRequestError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, http.StatusBadRequest, "invalid_request_error", "invalid_request", message)
}

func WriteInternalError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, http.StatusInternalServerError, "server_error", "internal_error", message)
}

func WriteServiceUnavailableError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, http.StatusServiceUnavailable, "server_error", "service_unavailable", message)
}

func WriteContentBlockedError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, 451, "content_filter_error", "content_blocked", message)
}

func WriteBudgetExceededError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, http.StatusPaymentRequired, "budget_error", "budget_exceeded", message)
}

// WriteModelNotAllowedError refuses a request whose model is outside the
// presenting key's allowlist.
//
// 403 permission_error rather than the 503 server_error that a
// classification-ceiling refusal produces. That refusal reports "no provider
// available", which is retryable by definition, and this gateway's own retry
// executor treats 503 that way. An allowlist denial is permanent and
// deterministic: retrying it burns provider budget and buries the real cause.
// It also discloses nothing new, because ListModels already tells this same
// caller exactly which models its key may use.
func WriteModelNotAllowedError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, http.StatusForbidden, "permission_error", "model_not_allowed", message)
}

// WriteGoneError refuses a route that existed, was documented, and has been
// deliberately retired. 404 would read as a typo and invite a retry.
func WriteGoneError(w http.ResponseWriter, requestID, message string) {
	WriteError(w, requestID, http.StatusGone, "invalid_request_error", "endpoint_retired", message)
}
