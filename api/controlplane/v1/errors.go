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

// ErrorCode is a stable, machine-readable classification of a rejected
// request. Codes are part of the wire contract: an emitter branches on them,
// so a code's meaning may not change once released.
type ErrorCode string

const (
	// ErrCodeInvalidRequest is a malformed or self-inconsistent message.
	ErrCodeInvalidRequest ErrorCode = "invalid_request"

	// ErrCodeUnauthorized is a missing, malformed, expired, or revoked token.
	ErrCodeUnauthorized ErrorCode = "unauthorized"

	// ErrCodeNotFound is a resource that does not exist within the caller's
	// organization. A resource that exists in another organization is also
	// reported as not found: distinguishing the two would confirm to a caller
	// that some other tenant holds a given identifier.
	ErrCodeNotFound ErrorCode = "not_found"

	// ErrCodeChainGap is a submission whose checkpoint ID skips one or more
	// checkpoints that were never received.
	//
	// A gap is reported rather than absorbed. The whole value of an
	// aggregated chain is that it says what it does not have.
	ErrCodeChainGap ErrorCode = "chain_gap"

	// ErrCodeChainFork is a submission reusing a checkpoint ID that is already
	// stored with different attested content. Two different checkpoints at the
	// same sequence cannot both be the record.
	ErrCodeChainFork ErrorCode = "chain_fork"

	// ErrCodeChainPrevMismatch is a submission whose prev_checkpoint_hash or
	// prev_checkpoint_id does not match the stored predecessor. The
	// predecessor is present and the link to it is wrong, which is a different
	// fault from a gap and needs a different answer from an operator.
	ErrCodeChainPrevMismatch ErrorCode = "chain_prev_mismatch"

	// ErrCodeChainGenesisConflict is a submission claiming to be genesis for a
	// gateway that already has one, or a non-genesis first submission.
	ErrCodeChainGenesisConflict ErrorCode = "chain_genesis_conflict"

	// ErrCodeInternal is a fault on the control plane side.
	ErrCodeInternal ErrorCode = "internal_error"
)

// Error is the body of every non-2xx response.
//
// Chain errors carry Detail so that the discontinuity is stated in numbers an
// operator can act on, not just classified. "checkpoint 41 is missing" is
// something a person can go and look for; "chain error" is not.
type Error struct {
	Code ErrorCode `json:"code"`

	// Message is a single sentence naming what is wrong, in terms of the
	// values involved.
	Message string `json:"message"`

	// Detail carries the specifics of a chain discontinuity. It is nil for
	// errors that are not about the chain.
	Detail *ChainErrorDetail `json:"detail,omitempty"`

	// RequestID identifies this request in the control plane's logs.
	RequestID string `json:"request_id,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	return string(e.Code) + ": " + e.Message
}

// ChainErrorDetail names a chain discontinuity precisely enough to be acted
// on. Every field is optional; which ones are set depends on the code.
type ChainErrorDetail struct {
	// GatewayID is the gateway whose chain is affected.
	GatewayID string `json:"gateway_id,omitempty"`

	// SubmittedCheckpointID is the checkpoint ID that was offered.
	SubmittedCheckpointID *int64 `json:"submitted_checkpoint_id,omitempty"`

	// ExpectedCheckpointID is the checkpoint ID the chain was ready to
	// receive: one past the highest stored, or 1 for an empty chain.
	ExpectedCheckpointID *int64 `json:"expected_checkpoint_id,omitempty"`

	// MissingCheckpointIDs enumerates the checkpoints between the stored head
	// and the submission that were never received. Set for a gap.
	MissingCheckpointIDs []int64 `json:"missing_checkpoint_ids,omitempty"`

	// StoredCheckpointHash is the hash already on record at the conflicting
	// sequence. Set for a fork.
	StoredCheckpointHash HashHex `json:"stored_checkpoint_hash,omitempty"`

	// SubmittedCheckpointHash is the hash offered at that sequence. Set for a
	// fork.
	SubmittedCheckpointHash HashHex `json:"submitted_checkpoint_hash,omitempty"`

	// ExpectedPrevCheckpointHash is the stored predecessor's checkpoint hash.
	// Set for a prev mismatch.
	ExpectedPrevCheckpointHash HashHex `json:"expected_prev_checkpoint_hash,omitempty"`

	// SubmittedPrevCheckpointHash is the predecessor hash the submission
	// bound. Set for a prev mismatch.
	SubmittedPrevCheckpointHash HashHex `json:"submitted_prev_checkpoint_hash,omitempty"`

	// ExpectedPrevCheckpointID is the stored predecessor's checkpoint ID. Set
	// for a prev mismatch where the identity, not the hash, is wrong.
	ExpectedPrevCheckpointID *int64 `json:"expected_prev_checkpoint_id,omitempty"`

	// SubmittedPrevCheckpointID is the predecessor ID the submission claimed.
	SubmittedPrevCheckpointID *int64 `json:"submitted_prev_checkpoint_id,omitempty"`
}
