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

// Package controlplanev1 is the public wire protocol between an AEGIS gateway
// and a control plane that aggregates checkpoint attestations across gateways.
//
// # What this protocol carries
//
// Two messages travel over it: a gateway registration, and a checkpoint
// submission. A checkpoint submission is the contents of one audit_checkpoints
// row as produced by internal/audit/checkpoint's sealer: a Merkle root over a
// contiguous range of audit event IDs, the chain hash binding it to its
// predecessor, and the parameters an independent verifier needs to recompute
// both. See docs/AUDIT-INTEGRITY.md for the hash construction these fields
// describe.
//
// # What this protocol does not carry
//
// No prompt text. No response text. No audit event rows. No field of any
// message in this package holds request or response payload, and none holds an
// individual decision record.
//
// A checkpoint attests that a range of audit events existed in a particular
// form at a particular time. It does not contain those events. Verifying a
// single event against a checkpoint requires an inclusion proof from the
// gateway that sealed it, which is served by the gateway and not by this
// protocol. That is the point: a control plane can hold every checkpoint an
// organization has ever produced and still hold no governed content.
//
// This property is verifiable by reading the types below. It is not a
// configuration setting and there is no message variant that relaxes it.
//
// # Versioning
//
// The package path carries the major version. Once these types ship in a
// tagged release they are a contract: fields may be added, existing fields may
// not change name, type, or meaning. A change that would break a deployed
// gateway or a deployed control plane requires a new versioned package.
//
// JSON Schema documents for both messages live in the schema subdirectory and
// are embedded via [Schemas]. They are the normative description of the wire
// format for implementations that are not written in Go.
//
// # Fields the gateway does not yet populate
//
// [CheckpointSubmission] declares ConfigHash, PolicyBundles, CoveredFrom, and
// CoveredTo as optional. Nothing in the gateway computes them today, so a
// current gateway omits them. They are declared now, with fixed meanings, so
// that a gateway which learns to compute them can start sending them without a
// protocol version bump. A control plane must treat their absence as normal.
package controlplanev1
