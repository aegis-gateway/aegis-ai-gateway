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
	// SealStateAdvancing means nothing is blocking the sealer. Either
	// everything old enough to seal has been sealed, or what remains is
	// contiguous with the last checkpoint and will be sealed on the next run.
	SealStateAdvancing SealState = "advancing"

	// SealStateWaitingOnGap means a gap in audit event IDs exists, but the
	// events beyond it are still inside the seal lag window, so the sealer has
	// not yet attempted to seal past it and the gap may still fill on its own.
	//
	// This is the state that must not be confused with a stall. BIGSERIAL hands
	// out an ID at insert and the row becomes visible at commit, so a
	// transaction in flight leaves exactly this shape and then resolves it. The
	// lag window exists to let that happen. Reporting a healthy gateway as
	// paused during it would train an operator to ignore the signal, which is
	// worse than not having one.
	SealStateWaitingOnGap SealState = "waiting_on_gap"

	// SealStatePausedAtGap means the gap persisted past the lag window. The
	// sealer has attempted to seal and stopped, and will make no further
	// progress until an operator resolves it: a gap left by a rolled-back
	// insert never fills.
	SealStatePausedAtGap SealState = "paused_at_gap"

	// SealStateNeverSealed means the gateway holds audit events but no
	// checkpoint. Its chain attests nothing yet.
	SealStateNeverSealed SealState = "never_sealed"

	// SealStateEmpty means the gateway holds no audit events at all, so there
	// is nothing to seal. Distinct from Advancing: one has attested
	// everything, the other has had nothing to attest.
	SealStateEmpty SealState = "empty"
)

// DefaultSealLagSeconds is the lag window a gateway uses unless configured
// otherwise, and the value this protocol documents as the reference.
//
// It is 300 because that is what the sealer's own default is: aegis-migrate
// seal takes -lag-seconds 300, and docs/AUDIT-INTEGRITY.md section 6 specifies
// audit.seal_lag_seconds with a five minute default. The number is not chosen
// here; it is read from the component whose behaviour it describes, which is
// the only way the two can be relied on to agree.
const DefaultSealLagSeconds int64 = 300

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

	// LastSealedEventID is the highest audit event ID covered by a checkpoint,
	// or zero when none has been sealed. Required.
	LastSealedEventID int64 `json:"last_sealed_event_id"`

	// FirstUnsealedEventID is the lowest audit event ID beyond the last
	// checkpoint, nil when nothing is unsealed. Required whenever
	// UnsealedEventCount is non-zero.
	//
	// Together with LastSealedEventID it locates a gap exactly: the missing IDs
	// are the interval between them. These are the two numbers an operator
	// needs in order to go and look, and they are reported in every state
	// rather than only when paused, because "how far behind is this gateway"
	// is a question worth answering while it is still healthy.
	FirstUnsealedEventID *int64 `json:"first_unsealed_event_id"`

	// GapAgeSeconds is how long the gap has existed, nil when there is no gap.
	//
	// Required whenever a gap exists, and deliberately independent of any
	// threshold. A consumer can reason about an age without agreeing with the
	// gateway about what counts as too long, which matters because
	// SealLagSeconds is the gateway's own declaration and may itself be
	// misconfigured. An age is objective; a state is a judgement made against
	// a number someone chose.
	GapAgeSeconds *int64 `json:"gap_age_seconds"`

	// SealLagSeconds is the lag window this gateway is running with: the age an
	// event must reach before the sealer will seal it.
	//
	// It is declared by the gateway and stored as declared. A control plane
	// must not hold a threshold describing a system it does not operate, and
	// must not synthesise a state it was not told: SealState is the gateway's
	// judgement, this is the number it judged against, and both travel
	// together so a reader can check one against the other.
	SealLagSeconds int64 `json:"seal_lag_seconds"`

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
	case SealStateAdvancing, SealStateWaitingOnGap, SealStatePausedAtGap,
		SealStateNeverSealed, SealStateEmpty:
	default:
		return fmt.Errorf("seal_state %q is not a known state", r.SealState)
	}
	if r.SealLagSeconds < 0 {
		return fmt.Errorf("seal_lag_seconds must not be negative, got %d", r.SealLagSeconds)
	}
	if r.LastSealedEventID < 0 {
		return fmt.Errorf("last_sealed_event_id must not be negative, got %d", r.LastSealedEventID)
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

	// A gap state must carry the numbers that describe the gap, and a
	// non-gap state must not claim one. Without both halves a report could
	// describe a gap while calling itself healthy, or call itself paused
	// while naming nothing an operator could act on.
	if r.HasGap() {
		if r.FirstUnsealedEventID == nil {
			return fmt.Errorf("seal_state is %q, so first_unsealed_event_id is required", r.SealState)
		}
		if *r.FirstUnsealedEventID <= r.LastSealedEventID+1 {
			return fmt.Errorf(
				"seal_state is %q but first_unsealed_event_id %d follows last_sealed_event_id %d "+
					"with no gap between them",
				r.SealState, *r.FirstUnsealedEventID, r.LastSealedEventID)
		}
		if r.GapAgeSeconds == nil {
			return fmt.Errorf("seal_state is %q, so gap_age_seconds is required", r.SealState)
		}
		if *r.GapAgeSeconds < 0 {
			return fmt.Errorf("gap_age_seconds must not be negative, got %d", *r.GapAgeSeconds)
		}
	} else if r.GapAgeSeconds != nil {
		return fmt.Errorf("gap_age_seconds was sent with seal_state %q, which describes no gap",
			r.SealState)
	}

	if r.UnsealedEventCount > 0 && r.FirstUnsealedEventID == nil {
		return fmt.Errorf("%d events are unsealed, so first_unsealed_event_id is required",
			r.UnsealedEventCount)
	}
	if r.UnsealedEventCount == 0 && r.FirstUnsealedEventID != nil {
		return fmt.Errorf("first_unsealed_event_id %d was sent with no unsealed events",
			*r.FirstUnsealedEventID)
	}

	if r.SealState == SealStateEmpty && r.UnsealedEventCount != 0 {
		return fmt.Errorf("seal_state is %q but %d events are unsealed",
			SealStateEmpty, r.UnsealedEventCount)
	}
	return validateLabel("gateway_version", r.GatewayVersion, MaxVersionLen)
}

// HasGap reports whether this state describes a gap in audit event IDs.
func (r *GatewayStatusReport) HasGap() bool {
	return r.SealState == SealStateWaitingOnGap || r.SealState == SealStatePausedAtGap
}

// GatewayStatusResponse acknowledges a status report.
type GatewayStatusResponse struct {
	GatewayID  string    `json:"gateway_id"`
	ReceivedAt Timestamp `json:"received_at"`
	SealState  SealState `json:"seal_state"`
}
