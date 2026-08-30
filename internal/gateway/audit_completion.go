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
)

// providerFailureReason decides whether a failed request was the caller leaving
// or the provider failing.
//
// The ERROR must say cancellation. An earlier version consulted only
// r.Context().Err(), which is a snapshot at the moment of classification, so a
// genuine provider failure followed a moment later by the caller hanging up was
// sealed as client_disconnected and a real fault vanished from the record an
// operator uses to judge provider reliability. The error is the evidence of what
// actually went wrong; the context is corroboration that the caller is the one
// who went away.
//
// This also removes the need for callers to special-case a non-200. A provider
// that answers with an error status produces an ordinary error from the decode,
// not a cancellation, so its fault is preserved without anyone having to
// remember to check the status first.
func providerFailureReason(r *http.Request, err error, providerReason string) string {
	if errors.Is(err, context.Canceled) && errors.Is(r.Context().Err(), context.Canceled) {
		return audit.ReasonClientDisconnected
	}
	return providerReason
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
