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
	"net"
	"net/http"
	"syscall"
	"time"

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

// clientGone reports whether an error is the network telling us the peer has
// gone: a write to a socket the other end has closed.
//
// A disconnect during a write to the CLIENT surfaces as EPIPE or ECONNRESET,
// not as anything wrapping context.Canceled, so a check for cancellation alone
// misses the commonest form of it.
//
// ONLY call this with an error from writing to the caller. The same errnos come
// back from a provider connection being reset, and this cannot tell the two
// apart: it names the shape of the failure, not which peer caused it. Using it
// on a provider error would seal a provider reset as a client disconnect, which
// is the mirror of the defect it was added to fix.
func clientGone(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, net.ErrClosed)
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
	// clientGone is deliberately NOT consulted here. This runs on provider send
	// and body-read failures, where EPIPE and ECONNRESET mean the PROVIDER
	// reset the connection, and treating that as a disconnect would discard the
	// provider fault these checks exist to preserve.
	if !errors.Is(err, context.Canceled) && !errors.Is(err, retry.ErrContextCancelled) {
		return false
	}
	// A cancellation that arrived on top of a provider failure is not a clean
	// disconnect. retry.Executor joins the last attempt's error when a caller
	// leaves during backoff, precisely so that fault is still visible here, and
	// treating the request as a disconnect would discard it.
	return !errors.Is(err, retry.ErrMaxRetriesExceeded) && !hasProviderFault(err)
}

// hasProviderFault reports whether a provider error is present alongside a
// cancellation. errors.Join produces a tree, so the whole chain is walked.
func hasProviderFault(err error) bool {
	var joined interface{ Unwrap() []error }
	if !errors.As(err, &joined) {
		return false
	}
	for _, e := range joined.Unwrap() {
		if !errors.Is(e, context.Canceled) && !errors.Is(e, retry.ErrContextCancelled) {
			return true
		}
	}
	return false
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
		// Known on every path, including the ones that never reach a provider:
		// it is the authority the request ran under, not an outcome of running
		// it. The outcome fields are attached separately by withOutcome.
		Classification: string(authInfo.MaxClassification),
	}
}

// withOutcome attaches what actually ran to a completion record.
//
// Separate from completedRequest because the two are known at different times
// and on different paths. Identity and authority exist from the moment the key
// is resolved; the served model, the token counts and the duration exist only
// once a provider has answered, and the failure paths that never got that far
// must not invent them. Leaving them zero there is what makes the logger write
// NULL rather than a measurement nobody took.
// withDuration attaches only the elapsed time.
//
// For the failure paths, which have no provider outcome to record but did
// measure how long the gateway spent before giving up. That measurement is the
// most useful thing a provider_failure row carries beyond the reason: a
// provider that refused in 3 ms and one that hung for 30 s produce the same
// event otherwise.
//
// The token counts stay absent there, because they are provider-reported and
// no provider reported any. Duration is different: the gateway measures it
// itself, so it exists on every path that got far enough to fail.
func withDuration(rec audit.CompletedRequest, d time.Duration) audit.CompletedRequest {
	ms := d.Milliseconds()
	rec.DurationMs = &ms
	return rec
}

func withOutcome(
	rec audit.CompletedRequest,
	modelServed string,
	promptTokens, completionTokens, totalTokens int,
	d time.Duration,
) audit.CompletedRequest {
	rec.ModelServed = modelServed
	rec.PromptTokens = int64(promptTokens)
	rec.CompletionTokens = int64(completionTokens)
	rec.TotalTokens = int64(totalTokens)
	ms := d.Milliseconds()
	rec.DurationMs = &ms
	return rec
}
