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

import (
	"fmt"
	"time"
)

// CheckpointSubmission is one sealed audit checkpoint, offered to a control
// plane for durable attestation.
//
// Every required field below is a column of audit_checkpoints as written by
// internal/audit/checkpoint. Together they are sufficient to recompute
// CheckpointHash without access to the gateway, which is what makes an
// aggregated chain independently checkable:
//
//	checkpoint_hash = SHA-256(
//	    merkle_root                    || -- 32 bytes
//	    prev_checkpoint_hash           || -- 32 bytes
//	    uint64_le(range_start)         ||
//	    uint64_le(range_end)           ||
//	    uint32_le(event_count)         ||
//	    uint32_le(hash_schema_version) ||
//	    int64_le(sealed_at_unix_micros))
//
// It contains no audit event. MerkleRoot attests a range of events; it does
// not reveal one. Proving that a specific event falls under a specific root
// requires an inclusion proof, which the sealing gateway serves and this
// message does not carry.
type CheckpointSubmission struct {
	// GatewayID is the identifier returned by registration. The control plane
	// checks that it belongs to the bearer token's organization; a gateway ID
	// from another tenant is not found rather than forbidden.
	GatewayID string `json:"gateway_id"`

	// CheckpointID is audit_checkpoints.id on the gateway: a per-gateway
	// sequence that increases by one for each checkpoint sealed.
	//
	// This is the sequence number of the chain. The gateway's schema has no
	// separate sequence column; id is that sequence, and the sealer's
	// single-writer advisory lock is what makes it dense rather than merely
	// increasing.
	CheckpointID int64 `json:"checkpoint_id"`

	// RangeStart and RangeEnd are the first and last audit event ID covered,
	// both inclusive. The gateway seals contiguous runs only, so a checkpoint
	// covers every event ID in this closed interval.
	RangeStart int64 `json:"range_start"`
	RangeEnd   int64 `json:"range_end"`

	// EventCount is how many events the Merkle tree was built over. For a
	// checkpoint sealed by a current gateway it equals RangeEnd-RangeStart+1;
	// it is transmitted separately rather than derived because it is an input
	// to the checkpoint hash and a verifier must use the sealed value.
	EventCount int32 `json:"event_count"`

	// MerkleRoot is the RFC 6962 Merkle root over the covered events, as
	// lowercase hex. This is what attests the events.
	MerkleRoot HashHex `json:"merkle_root"`

	// PrevCheckpointID is the gateway's checkpoint ID of the predecessor, or
	// nil for the first checkpoint in the chain.
	//
	// At hash_schema_version 1 and 2 it is sent alongside PrevCheckpointHash
	// because the hash alone does not pin identity: those inputs do not cover
	// the predecessor's id, so a chain can be repointed at an earlier
	// checkpoint with every hash still verifying. The gateway's own
	// verify-chain makes the same distinction.
	//
	// At version 3 it is also inside the digest, so a repointed chain fails
	// [VerifyCheckpointHash] on its own. It stays a separate field because a
	// receiver still needs the ordering to check the chain, and because the
	// field is part of a shipped wire contract.
	PrevCheckpointID *int64 `json:"prev_checkpoint_id"`

	// PrevCheckpointHash is the predecessor's CheckpointHash, or
	// [GenesisPrevHash] for the first checkpoint. This is the link the chain
	// is made of, and it binds the predecessor's whole hash rather than its
	// Merkle root, so altering any attested field of an earlier checkpoint
	// invalidates every checkpoint after it.
	PrevCheckpointHash HashHex `json:"prev_checkpoint_hash"`

	// CheckpointHash is this checkpoint's own chain hash, computed as shown in
	// the type documentation above.
	CheckpointHash HashHex `json:"checkpoint_hash"`

	// HashAlgorithm is the digest used for leaf, node, and chain hashing.
	HashAlgorithm HashAlgorithm `json:"hash_algorithm"`

	// HashSchemaVersion selects which set of audit event columns the leaf
	// hashes covered. It is an input to the checkpoint hash. A verifier reads
	// it to know which serialization rules apply to a checkpoint sealed under
	// an older schema.
	HashSchemaVersion int32 `json:"hash_schema_version"`

	// CanonicalizationSpec identifies the byte-level serialization applied to
	// each event before leaf hashing, for example [CanonicalizationRFC8785V1].
	CanonicalizationSpec CanonicalizationSpec `json:"canonicalization_spec"`

	// SealedAt is when the checkpoint was sealed, at microsecond precision.
	// It is an input to the checkpoint hash, so it cannot be adjusted after
	// sealing without breaking the chain, and it cannot be rounded in transit
	// without making the hash unrecomputable.
	SealedAt Timestamp `json:"sealed_at"`

	// SealerVersion is the build version of the sealer that produced this
	// checkpoint. The gateway treats it as unauthenticated debug metadata and
	// excludes it from the hash; it is forwarded for the same purpose and
	// carries no attestation weight.
	SealerVersion string `json:"sealer_version"`

	// GatewayVersion is the build version of the gateway at submission time.
	// It can differ from SealerVersion when a checkpoint is forwarded after an
	// upgrade.
	GatewayVersion string `json:"gateway_version"`

	// CoveredFrom and CoveredTo are the earliest and latest audit event
	// timestamp in the covered range.
	//
	// Required. An event ID range states extent in the gateway's own terms;
	// this states it in terms anyone can act on. "Does this evidence cover the
	// third quarter" is the first question asked of an evidence artifact, and
	// an ID range cannot answer it without a lookup against every gateway that
	// contributed.
	//
	// These are not hash inputs, and do not need to be: the leaf hash of every
	// audit event covers that event's timestamp, so the interval is provable
	// against MerkleRoot by inclusion proof. They index something the tree
	// already attests rather than making a fresh claim.
	//
	// Consecutive checkpoints may overlap. Event IDs are allocated at insert
	// and become visible at commit, so a long transaction carries an older
	// timestamp and commits later, and CoveredFrom is the minimum over the
	// batch rather than the first event's timestamp.
	//
	// See [CoverageSemantics] for what that means for anything answering a
	// coverage question. Getting it wrong is not a rounding error: it produces
	// a statement about what evidence covers that is wrong in the direction of
	// claiming more.
	CoveredFrom Timestamp `json:"covered_from"`
	CoveredTo   Timestamp `json:"covered_to"`

	// CoveredRangeSource says how the interval above was arrived at.
	//
	// Required, because a range computed by the sealer over the exact set of
	// events it hashed and one reconstructed afterwards from surviving rows
	// are not equally strong, and a consumer presenting them identically is
	// overstating the weaker one. See [CoverageSealed] and
	// [CoverageVerifiedBackfill].
	CoveredRangeSource CoverageSource `json:"covered_range_source"`

	// ConfigHash is reserved. Its semantics are unspecified in v1, no v1
	// emitter may populate it, and its definition is deferred to v2.
	//
	// The name is claimed so that a v2 defining it is an addition rather than
	// a collision with whatever someone else put there. The meaning is not
	// claimed, so nothing may be read into a v1 submission about the
	// configuration in force when its events were produced. A v1 checkpoint
	// attests event integrity, not policy provenance.
	ConfigHash HashHex `json:"config_hash,omitempty"`

	// PolicyBundles is reserved on the same terms as ConfigHash: semantics
	// unspecified in v1, MUST NOT be populated by a v1 emitter, definition
	// deferred to v2.
	PolicyBundles []PolicyBundleRef `json:"policy_bundles,omitempty"`
}

// CoverageSource says how a checkpoint's covered time range was obtained.
//
// The distinction exists because a reconstructed range can be wrong in a way
// that is invisible once written: if events were purged before the
// reconstruction ran, the interval computed from the survivors is narrower
// than what the checkpoint attests, and it looks exactly like a correct one.
type CoverageSource string

const (
	// CoverageSealed means the sealer computed the interval at seal time, over
	// the exact set of events it hashed. It cannot be narrower than the truth.
	CoverageSealed CoverageSource = "sealed"

	// CoverageVerifiedBackfill means the interval was reconstructed after the
	// fact and proven complete: the surviving event count matched the
	// checkpoint's own count and no purge overlapped its range.
	//
	// Weaker than [CoverageSealed] in one respect worth stating. It proves the
	// events were all present when the reconstruction ran, not that the
	// reconstruction saw the same bytes the sealer hashed.
	CoverageVerifiedBackfill CoverageSource = "verified_backfill"
)

// Valid reports whether s is a known source.
func (s CoverageSource) Valid() bool {
	return s == CoverageSealed || s == CoverageVerifiedBackfill
}

// CoverageSemantics documents how a consumer must reason about coverage
// intervals. It is a documentation anchor rather than a value.
//
// # Intervals are unioned, never partitioned
//
// Checkpoint intervals can overlap, so they do not partition time. Event IDs
// are allocated at insert and become visible at commit, so an event written
// during a long transaction carries a timestamp earlier than events already
// sealed into the previous checkpoint. Checkpoint N+1 can therefore begin
// earlier in time than checkpoint N ended.
//
// Two rules follow, and both are easy to get wrong in the direction of
// claiming more coverage than exists:
//
//   - A coverage query unions the intervals of the checkpoints it selects. It
//     never subtracts, never assumes adjacency, and never treats one
//     checkpoint's CoveredTo as the next one's CoveredFrom.
//   - Ordering by CheckpointID does not order by time. Sorting by ID and
//     reading the first and last interval gives a range that may exclude
//     covered instants and include uncovered ones.
//
// # What a coverage statement must contain
//
// Anything stating coverage for a period, an evidence bundle above all, states
// four things and not fewer:
//
//  1. the period requested
//  2. the checkpoints whose intervals intersect it
//  3. the union of those intervals
//  4. any gap inside the requested period, named explicitly
//
// A statement of the form "covered from X to Y" is not sufficient, because it
// cannot express a hole.
//
// # An instant may fall in more than one checkpoint
//
// Because intervals overlap, one instant can be inside several. Those
// checkpoints are not duplicates and counting them is not double counting:
// each attests a different set of events. Anything reporting intersecting
// checkpoints says so, or a reader will reasonably suspect the count is
// inflated.
//
// # Coverage is asserted, not attested
//
// CoveredFrom and CoveredTo are not inputs to CheckpointHash. See
// [CheckpointSubmission.CoveredFrom] and the note on verification below.
//
// While the underlying audit events are retained, the interval is checkable:
// every leaf hash covers its event's timestamp, so the true minimum and
// maximum can be re-derived from the events and their inclusion proofs and
// compared. Once those events are purged, nothing can check it, and the
// interval becomes an unverifiable assertion by the party being audited.
//
// A consumer must therefore describe coverage as gateway-asserted rather than
// attested, in those words, until a hash schema version binds it.
const CoverageSemantics = "see the documentation on this constant"

// MaxCoverageClockSkew bounds how far CoveredTo may exceed SealedAt.
//
// Events are written before the checkpoint that seals them, so any excess is
// disagreement between the clock that stamped the event and the clock that
// stamped the seal. Under NTP those agree to within milliseconds; five seconds
// is generous for that and small enough that it still rejects a genuinely
// wrong timestamp rather than absorbing it.
//
// Exceeding SealedAt at all is an anomaly worth recording even when it is
// inside this bound. See [CheckpointSubmission.ClockSkew].
const MaxCoverageClockSkew = 5 * time.Second

// ClockSkew returns how far CoveredTo exceeds SealedAt, or zero when it does
// not.
//
// A non-zero value is within tolerance, because Validate rejects anything
// beyond it. It is still worth recording and reporting: a deployment whose
// clocks disagree by four seconds is one configuration change from producing
// evidence that fails validation, and an auditor reading a coverage range is
// entitled to know its endpoints were not perfectly ordered.
func (c *CheckpointSubmission) ClockSkew() time.Duration {
	if skew := c.CoveredTo.Sub(c.SealedAt.Time); skew > 0 {
		return skew
	}
	return 0
}

// PolicyBundleRef identifies one policy bundle by name and content digest.
//
// The digest, not the name, is what makes the reference meaningful: a name can
// be reused for changed content, and an evidence bundle assembled years later
// needs to say which bytes were in force. This type carries no policy source.
type PolicyBundleRef struct {
	// Name is the operator-facing bundle name.
	Name string `json:"name"`

	// Digest is a hash over the compiled bundle contents.
	Digest HashHex `json:"digest"`

	// HashAlgorithm is the digest function used for Digest.
	HashAlgorithm HashAlgorithm `json:"hash_algorithm"`
}

// IsGenesis reports whether this submission claims to be the first checkpoint
// in its gateway's chain.
func (c *CheckpointSubmission) IsGenesis() bool {
	return c.PrevCheckpointID == nil
}

// Validate checks the submission against the wire contract. It is a shape and
// self-consistency check: it verifies that fields are present and well formed
// and that the claims inside one message agree with each other. It does not
// recompute the checkpoint hash and it knows nothing about the chain this
// checkpoint belongs to. Both of those are the control plane's job, because
// both need state this message does not carry.
func (c *CheckpointSubmission) Validate() error {
	if c.GatewayID == "" {
		return fmt.Errorf("gateway_id is required")
	}
	if c.CheckpointID <= 0 {
		return fmt.Errorf("checkpoint_id must be positive, got %d", c.CheckpointID)
	}
	if c.RangeStart <= 0 {
		return fmt.Errorf("range_start must be positive, got %d", c.RangeStart)
	}
	if c.RangeEnd < c.RangeStart {
		return fmt.Errorf("range_end %d precedes range_start %d", c.RangeEnd, c.RangeStart)
	}
	if c.EventCount <= 0 {
		return fmt.Errorf("event_count must be positive, got %d", c.EventCount)
	}
	// The sealer stops at the first gap inside a batch, so a checkpoint always
	// covers a dense interval. A count that disagrees with the interval means
	// the submission is describing something the sealer cannot produce.
	if span := c.RangeEnd - c.RangeStart + 1; int64(c.EventCount) != span {
		return fmt.Errorf(
			"event_count %d does not match the covered range %d-%d, which spans %d events",
			c.EventCount, c.RangeStart, c.RangeEnd, span)
	}

	if c.HashAlgorithm != HashAlgorithmSHA256 {
		return fmt.Errorf("hash_algorithm %q is not supported; expected %q",
			c.HashAlgorithm, HashAlgorithmSHA256)
	}
	digestLen := digestLenSHA256

	if err := c.MerkleRoot.Validate("merkle_root", digestLen); err != nil {
		return err
	}
	if err := c.PrevCheckpointHash.Validate("prev_checkpoint_hash", digestLen); err != nil {
		return err
	}
	if err := c.CheckpointHash.Validate("checkpoint_hash", digestLen); err != nil {
		return err
	}

	// A genesis checkpoint must carry the genesis constant, and a non-genesis
	// checkpoint must not: the two are the same 32 bytes only by accident, and
	// letting either pass would let a gateway silently restart its chain.
	if c.IsGenesis() {
		if c.PrevCheckpointHash != GenesisPrevHash {
			return fmt.Errorf(
				"prev_checkpoint_id is null, so prev_checkpoint_hash must be the genesis constant %s",
				GenesisPrevHash)
		}
	} else {
		if *c.PrevCheckpointID <= 0 {
			return fmt.Errorf("prev_checkpoint_id must be positive when present, got %d", *c.PrevCheckpointID)
		}
		if *c.PrevCheckpointID >= c.CheckpointID {
			return fmt.Errorf("prev_checkpoint_id %d does not precede checkpoint_id %d",
				*c.PrevCheckpointID, c.CheckpointID)
		}
		if c.PrevCheckpointHash == GenesisPrevHash {
			return fmt.Errorf(
				"prev_checkpoint_hash is the genesis constant but prev_checkpoint_id is %d",
				*c.PrevCheckpointID)
		}
	}

	if c.HashSchemaVersion <= 0 {
		return fmt.Errorf("hash_schema_version must be positive, got %d", c.HashSchemaVersion)
	}
	if err := validateLabel("canonicalization_spec", string(c.CanonicalizationSpec), MaxVersionLen); err != nil {
		return err
	}
	if c.SealedAt.IsZero() {
		return fmt.Errorf("sealed_at is required")
	}
	if err := validateLabel("sealer_version", c.SealerVersion, MaxVersionLen); err != nil {
		return err
	}
	if err := validateLabel("gateway_version", c.GatewayVersion, MaxVersionLen); err != nil {
		return err
	}

	if c.CoveredFrom.IsZero() {
		return fmt.Errorf("covered_from is required")
	}
	if c.CoveredTo.IsZero() {
		return fmt.Errorf("covered_to is required")
	}
	if !c.CoveredRangeSource.Valid() {
		return fmt.Errorf("covered_range_source %q is not a known source; expected %q or %q",
			c.CoveredRangeSource, CoverageSealed, CoverageVerifiedBackfill)
	}
	if c.CoveredTo.Before(c.CoveredFrom.Time) {
		return fmt.Errorf("covered_to %s precedes covered_from %s",
			c.CoveredTo.Format(TimestampFormat), c.CoveredFrom.Format(TimestampFormat))
	}
	// The events were sealed after they were written, so neither bound of the
	// covered interval can meaningfully exceed sealed_at. The invariant is
	// enforced rather than weakened, with a small tolerance for clock skew
	// between whatever wrote the event and whatever sealed it.
	//
	// Small on purpose. A tolerance wide enough to absorb an unsynchronised
	// clock is not an invariant, it is a suggestion. Hosts under NTP agree to
	// within milliseconds, so seconds is already generous and anything past it
	// is a real anomaly worth seeing rather than absorbing.
	//
	// Both bounds get the same tolerance, and covered_from is checked first.
	// Giving it none made the allowance unreachable for the case that needs it
	// most: a single-event checkpoint has covered_from equal to covered_to, so
	// a skew well inside the bound passed the covered_to check and was then
	// rejected here anyway. A tolerance that only applies when at least one
	// event precedes the seal is not the rule this documents. Checking the
	// start bound first also reports the more specific failure when the whole
	// interval sits after the seal, rather than blaming its end.
	if skew := c.CoveredFrom.Sub(c.SealedAt.Time); skew > MaxCoverageClockSkew {
		return fmt.Errorf(
			"covered_from %s is %s after sealed_at %s, beyond the %s tolerance; the checkpoint "+
				"claims to cover events written after it was sealed",
			c.CoveredFrom.Format(TimestampFormat), skew.Round(time.Millisecond),
			c.SealedAt.Format(TimestampFormat), MaxCoverageClockSkew)
	}
	if skew := c.CoveredTo.Sub(c.SealedAt.Time); skew > MaxCoverageClockSkew {
		return fmt.Errorf(
			"covered_to %s is %s after sealed_at %s, beyond the %s tolerance; the checkpoint "+
				"claims to cover events written after it was sealed",
			c.CoveredTo.Format(TimestampFormat), skew.Round(time.Millisecond),
			c.SealedAt.Format(TimestampFormat), MaxCoverageClockSkew)
	}

	// The reserved fields carry no agreed meaning in v1, so a v1 message that
	// populates them is rejected rather than stored under a definition that
	// does not exist yet. See docs/adr/0004-reserved-fields-must-not-be-populated.md.
	if c.ConfigHash != "" {
		return fmt.Errorf("config_hash is reserved in v1: its semantics are unspecified and " +
			"no v1 emitter may populate it")
	}
	if len(c.PolicyBundles) > 0 {
		return fmt.Errorf("policy_bundles is reserved in v1: its semantics are unspecified and " +
			"no v1 emitter may populate it; a v1 checkpoint attests event integrity, " +
			"not policy provenance")
	}
	return nil
}

// ChainStatus describes how one stored checkpoint sits in its gateway's chain.
type ChainStatus string

const (
	// ChainStatusGenesis is the first checkpoint of a gateway's chain.
	ChainStatusGenesis ChainStatus = "genesis"

	// ChainStatusLinked means the checkpoint's predecessor is stored and the
	// stored predecessor's checkpoint hash matches what this checkpoint binds.
	ChainStatusLinked ChainStatus = "linked"

	// ChainStatusBroken means the checkpoint is stored but its predecessor
	// link does not resolve to the checkpoint that precedes it. A control
	// plane reports this rather than hiding it: a break an auditor is not told
	// about is worse than one they are.
	ChainStatusBroken ChainStatus = "broken"
)

// HashVerification is the outcome of recomputing a stored checkpoint's hash
// from its own attested fields.
//
// A control plane that aggregates checkpoints can recompute each one on
// arrival. Reporting the outcome, rather than only storing checkpoints that
// passed, is deliberate: a checkpoint that does not verify is itself evidence,
// and a flag an auditor can see says more than a gap that looks like a gateway
// which stopped reporting.
type HashVerification string

const (
	// HashVerificationVerified means the stated hash was reproduced exactly.
	HashVerificationVerified HashVerification = "verified"

	// HashVerificationMismatch means recomputation produced a different
	// digest. It does not on its own mean tampering: it equally means the two
	// implementations disagree about the construction.
	HashVerificationMismatch HashVerification = "mismatch"

	// HashVerificationUnsupported means the algorithm or schema version is one
	// the verifying implementation cannot recompute. Expected for a checkpoint
	// sealed under a later version, and not a fault.
	HashVerificationUnsupported HashVerification = "unsupported"
)

// CheckpointRecord is one stored checkpoint as returned by a list request.
//
// It repeats the attested fields of the submission so that a reader can
// recompute the checkpoint hash from the response alone, and adds what the
// control plane knows and the gateway does not: when the checkpoint arrived,
// and how it sits in the chain as assembled from every submission received.
type CheckpointRecord struct {
	GatewayID            string               `json:"gateway_id"`
	CheckpointID         int64                `json:"checkpoint_id"`
	RangeStart           int64                `json:"range_start"`
	RangeEnd             int64                `json:"range_end"`
	EventCount           int32                `json:"event_count"`
	MerkleRoot           HashHex              `json:"merkle_root"`
	PrevCheckpointID     *int64               `json:"prev_checkpoint_id"`
	PrevCheckpointHash   HashHex              `json:"prev_checkpoint_hash"`
	CheckpointHash       HashHex              `json:"checkpoint_hash"`
	HashAlgorithm        HashAlgorithm        `json:"hash_algorithm"`
	HashSchemaVersion    int32                `json:"hash_schema_version"`
	CanonicalizationSpec CanonicalizationSpec `json:"canonicalization_spec"`
	SealedAt             Timestamp            `json:"sealed_at"`
	SealerVersion        string               `json:"sealer_version"`
	GatewayVersion       string               `json:"gateway_version"`

	CoveredFrom        Timestamp      `json:"covered_from"`
	CoveredTo          Timestamp      `json:"covered_to"`
	CoveredRangeSource CoverageSource `json:"covered_range_source"`

	// ConfigHash and PolicyBundles are reserved and never populated by a v1
	// control plane, because no v1 emitter may send them.
	ConfigHash    HashHex           `json:"config_hash,omitempty"`
	PolicyBundles []PolicyBundleRef `json:"policy_bundles,omitempty"`

	// ReceivedAt is when the control plane accepted this checkpoint. It is not
	// attested by any hash and is not evidence of when the events occurred.
	// Its use is to bound how long a gateway went unattested.
	ReceivedAt Timestamp `json:"received_at"`

	// ChainStatus is evaluated against the checkpoints actually stored.
	ChainStatus ChainStatus `json:"chain_status"`

	// ChainNote explains a ChainStatusBroken value. It is empty otherwise.
	ChainNote string `json:"chain_note,omitempty"`

	// HashVerification is whether the storing implementation could reproduce
	// CheckpointHash from this record's own attested fields when it arrived.
	//
	// A reader that wants to check this for itself has everything needed:
	// every input to the hash is on this record. The field reports what the
	// storing implementation found, and is not a substitute for recomputing.
	HashVerification HashVerification `json:"hash_verification"`

	// RecomputedHash is the digest the storing implementation produced when it
	// disagreed with CheckpointHash. Empty when the two matched.
	RecomputedHash HashHex `json:"recomputed_hash,omitempty"`
}

// CheckpointSubmissionResponse is returned for an accepted submission.
type CheckpointSubmissionResponse struct {
	GatewayID    string    `json:"gateway_id"`
	CheckpointID int64     `json:"checkpoint_id"`
	ReceivedAt   Timestamp `json:"received_at"`

	// Duplicate is true when this exact checkpoint was already stored and the
	// submission changed nothing. Submitting a checkpoint twice is a success,
	// not an error: a gateway that cannot tell whether its last request landed
	// must be able to retry safely.
	Duplicate bool `json:"duplicate"`

	// ChainStatus is how the stored checkpoint sits in the chain.
	ChainStatus ChainStatus `json:"chain_status"`
}

// CheckpointListResponse is returned by a list request.
type CheckpointListResponse struct {
	GatewayID   string             `json:"gateway_id"`
	Checkpoints []CheckpointRecord `json:"checkpoints"`

	// NextAfter is the checkpoint ID to pass as the `after` parameter to
	// continue the listing. It is nil when the last page has been returned.
	NextAfter *int64 `json:"next_after,omitempty"`
}
