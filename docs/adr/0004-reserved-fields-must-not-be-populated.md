# 0004. Undecided protocol fields are reserved, not optional

Status:   Accepted
Date:     2026-08-22
Decision: `config_hash` and `policy_bundles` are reserved: their semantics are unspecified, no v1 emitter may populate them, and their definition is deferred to v2.

## Context

The original design called for the checkpoint payload to carry a configuration
hash and the policy bundle versions in force. Nothing in the gateway computes
either. There is no configuration digest anywhere in the repository, and while
Rego bundles are compiled from a configuration directory, they are neither
versioned nor digested.

The first version of this package declared both as optional, with
documentation saying a current gateway omits them.

That was the wrong strength. Optional means "populate it if you have it", which
invites a fork, or a future emitter written by someone who has not read this
record, to fill the field with something plausible. The meaning of the field
would then be set by whoever populated it first, in a package that is supposed
to be a contract.

Two positions were considered.

**Declare them optional, reserved for later.** Avoids a version bump when the
gateway learns to compute them. This is what shipped first.

**Omit them from v1 entirely.** Cleanest contract; costs a version bump later.

The position that stands is a third one: keep the names, remove the permission.

## Decision

The fields remain declared, and both the Go documentation and the JSON Schema
state that their semantics are unspecified in v1, that no v1 emitter may
populate them, and that their definition is deferred to v2. The control plane
rejects a submission that populates them.

The package documentation states that a v1 checkpoint attests event integrity
and not policy provenance. That sentence is what turns an unpopulated field
into a declared scope boundary rather than a gap in the evidence.

## Consequences

- The names are claimed, so a v2 that defines them is an addition rather than a
  collision with whatever someone else put there.
- The meaning is not claimed, so nothing can be read into a v1 submission about
  the configuration or policy in force when its events were produced.
- A gateway that learns to compute a configuration digest still needs a v2 to
  transmit it. That is the cost, and it is the right one: a field whose meaning
  is decided later should be transmitted under a version that says what it
  means.
- Contrast with 0005, which covers a field that looked similar and is not.
