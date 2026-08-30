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
	"errors"
	"net/http"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/retry"
)

// providerFailureReason decides whether a failed request was the caller leaving
// or the provider failing.
//
// Three signals, and all three are needed. Two earlier versions used one each
// and both misclassified real traffic.
//
// providerStatus first, because an established fault wins. A non-200 means the
// provider has already failed, and every adapter calls io.ReadAll BEFORE it
// inspects the status, so a caller who leaves during that read produces a
// cancellation error and the status error is never built at all. Classifying on
// the error alone therefore erased genuine provider failures. Pass 0 where no
// response exists, as on a send failure.
func providerFailureReason(r *http.Request, err error, providerStatus int, providerReason string) string {
	if providerStatus != 0 && providerStatus != http.StatusOK {
		return providerReason
	}
	if !callerWentAway(r, err) {
		return providerReason
	}
	return audit.ReasonClientDisconnected
}

// callerWentAway reports whether a failure was the caller going away.
//
// The error is the evidence of WHAT failed. Consulting only r.Context().Err()
// classified on a snapshot taken when the event was written, so a provider fault
// that merely coincided with a later cancellation was recorded as a disconnect.
//
// The context is corroboration of WHO left, and rules out an unrelated
// cancellation deeper in the stack.
//
// retry.ErrContextCancelled is checked alongside context.Canceled because the
// buffered path always runs through retry.Executor, which wraps its own sentinel
// with %w and formats ctx.Err() with %v. context.Canceled is therefore not in
// the unwrap chain, and testing for it alone meant a real disconnect on the
// production path was sealed as a provider outage.
func callerWentAway(r *http.Request, err error) bool {
	if !errors.Is(r.Context().Err(), context.Canceled) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, retry.ErrContextCancelled)
}

// completedRequest builds the attested record of a request that passed every
// gate, for both the completion and the provider-failure event.
//
// One constructor, because the streaming and non-streaming paths are separate
// files with separate completion logic, and the pair of defects that motivated
// this work were both "the non-streaming path was corrected and the streaming
// path was not". A positional argument swapped between two call sites would
// compile and would silently attribute the wrong provider.
//
// providerKey is the configured provider name from the resolved route, never
// adapter.Name(): the adapter type is shared, so azure_openai and internal_vllm
// both report "openai". This is the same normalisation the pricing and usage
// records use, so the audit event and the spend record agree on who served the
// request.
func completedRequest(
	reqID string,
	authInfo *auth.AuthInfo,
	r *http.Request,
	requestedModel string,
	providerKey string,
	statusCode int,
	stream bool,
) audit.CompletedRequest {
	return audit.CompletedRequest{
		RequestID:      reqID,
		OrganizationID: authInfo.OrganizationID,
		TeamID:         authInfo.TeamID,
		UserID:         authInfo.UserID,
		KeyID:          authInfo.KeyID,
		KeyPrefix:      authInfo.KeyPrefix,
		Endpoint:       r.URL.Path,
		RequestedModel: requestedModel,
		ProviderKey:    providerKey,
		StatusCode:     statusCode,
		IPAddress:      r.RemoteAddr,
		Stream:         stream,
	}
}
