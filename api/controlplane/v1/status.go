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

package controlplanev1

import "fmt"

// SealState describes why a gateway's chain is or is not advancing.
//
// Without it, a gateway that has deliberately stopped sealing and a gateway
// that has fallen off the network look identical to anything aggregating
// checkpoints: both simply stop submitting. They are not the same, and an
// evidence bundle has to say which one it is. One is a known state with a named
// cause and a bounded set of unattested events; the other is unknown, and an
// unknown is the thing an auditor is entitled to be told about.
type SealState string

const (
	// SealStateCurrent means every event old enough to seal has been sealed.
	SealStateCurrent SealState = "current"

	// SealStatePausedAtGap means sealing has stopped because a gap in audit
	// event IDs separates the last checkpoint from the next visible event.
	//
	// The gateway's sealer stops rather than sealing past the gap, because an
	// in-flight transaction may still commit into it and sealing past it would
	// exclude that event from the chain permanently. A gap left by a
	// rolled-back insert never fills, so this state persists until an operator
	// resolves it. It is a deliberate stall, and reporting it is what stops it
	// being mistaken for a gateway that has gone quiet.
	SealStatePausedAtGap SealState = "paused_at_gap"

	// SealStateNeverSealed means the gateway holds audit events but no
	// checkpoint. Its chain attests nothing yet.
	SealStateNeverSealed SealState = "never_sealed"

	// SealStateEmpty means the gateway holds no audit events at all, so there
	// is nothing to seal. Distinct from Current: one has attested everything,
	// the other has had nothing to attest.
	SealStateEmpty SealState = "empty"
)

// GatewayStatusReport is a gateway's account of its own sealing.
//
// It is reported independently of checkpoint submission, because the case it
// exists for is precisely the one where no checkpoint is forthcoming.
//
// It carries no prompt content, no response content, and no audit event: counts
// and event IDs only.
type GatewayStatusReport struct {
	// GatewayID is the identifier returned by registration.
	GatewayID string `json:"gateway_id"`

	// ReportedAt is when the gateway assembled this report.
	ReportedAt Timestamp `json:"reported_at"`

	// SealState is why the chain is or is not advancing.
	SealState SealState `json:"seal_state"`

	// LastCheckpointID is the highest checkpoint the gateway has sealed, or
	// nil when it has sealed none.
	LastCheckpointID *int64 `json:"last_checkpoint_id"`

	// LastSealedAt is when that checkpoint was sealed. Nil alongside a nil
	// LastCheckpointID.
	LastSealedAt *Timestamp `json:"last_sealed_at"`

	// UnsealedEventCount is how many audit events lie beyond the last
	// checkpoint. It bounds what is unattested: a gateway paused at a gap with
	// four unsealed events and one with four million are very different
	// situations wearing the same label.
	UnsealedEventCount int64 `json:"unsealed_event_count"`

	// GapAfterEventID and NextVisibleEventID locate a gap. Both are set only
	// for [SealStatePausedAtGap], and they are the two numbers an operator
	// needs in order to go and look at what is missing.
	GapAfterEventID    *int64 `json:"gap_after_event_id,omitempty"`
	NextVisibleEventID *int64 `json:"next_visible_event_id,omitempty"`

	// GatewayVersion is the build version reporting this.
	GatewayVersion string `json:"gateway_version"`
}

// Validate checks the report against the wire contract.
func (r *GatewayStatusReport) Validate() error {
	if r.GatewayID == "" {
		return fmt.Errorf("gateway_id is required")
	}
	if r.ReportedAt.IsZero() {
		return fmt.Errorf("reported_at is required")
	}
	switch r.SealState {
	case SealStateCurrent, SealStatePausedAtGap, SealStateNeverSealed, SealStateEmpty:
	default:
		return fmt.Errorf("seal_state %q is not a known state", r.SealState)
	}
	if r.UnsealedEventCount < 0 {
		return fmt.Errorf("unsealed_event_count must not be negative, got %d", r.UnsealedEventCount)
	}
	if (r.LastCheckpointID == nil) != (r.LastSealedAt == nil) {
		return fmt.Errorf("last_checkpoint_id and last_sealed_at must be sent together or not at all")
	}
	if r.LastCheckpointID != nil && *r.LastCheckpointID <= 0 {
		return fmt.Errorf("last_checkpoint_id must be positive when present, got %d", *r.LastCheckpointID)
	}

	// A gap is only meaningful with both of its bounds, and only in the state
	// that has one. Accepting a stray bound would let a report describe a gap
	// while claiming to be current.
	hasGapBounds := r.GapAfterEventID != nil || r.NextVisibleEventID != nil
	if r.SealState == SealStatePausedAtGap {
		if r.GapAfterEventID == nil || r.NextVisibleEventID == nil {
			return fmt.Errorf(
				"seal_state is %q, so gap_after_event_id and next_visible_event_id are both required",
				SealStatePausedAtGap)
		}
		if *r.NextVisibleEventID <= *r.GapAfterEventID {
			return fmt.Errorf("next_visible_event_id %d does not follow gap_after_event_id %d",
				*r.NextVisibleEventID, *r.GapAfterEventID)
		}
	} else if hasGapBounds {
		return fmt.Errorf("gap bounds were sent with seal_state %q, which describes no gap",
			r.SealState)
	}

	if r.SealState == SealStateEmpty && r.UnsealedEventCount != 0 {
		return fmt.Errorf("seal_state is %q but %d events are unsealed",
			SealStateEmpty, r.UnsealedEventCount)
	}
	return validateLabel("gateway_version", r.GatewayVersion, MaxVersionLen)
}

// GatewayStatusResponse acknowledges a status report.
type GatewayStatusResponse struct {
	GatewayID  string    `json:"gateway_id"`
	ReceivedAt Timestamp `json:"received_at"`
	SealState  SealState `json:"seal_state"`
}
