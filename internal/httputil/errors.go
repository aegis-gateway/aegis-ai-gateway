package httputil

import (
	"encoding/json"
	"net/http"
)

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
	json.NewEncoder(w).Encode(APIError{
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
