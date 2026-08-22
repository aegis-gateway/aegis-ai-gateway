# 0008. Sealing status is three states, judged against a gateway-declared window

Status:   Accepted
Date:     2026-08-22
Decision: Report advancing, waiting-on-gap and paused-at-gap. The gateway owns the lag window, carries it in the message, and judges its own state against it.

## Context

The first version of the sealing signal had two gap states: a gap existed or it
did not, and a gap meant `paused_at_gap`.

That produced a false positive for a healthy gateway. BIGSERIAL hands out an id
at insert and the row becomes visible at commit, so a transaction in flight
leaves a gap that resolves itself moments later. The sealer's lag window exists
precisely to let that happen: it does not consider events younger than the
window, so it has not attempted to seal past such a gap and nothing is stuck.

Reporting that as paused is worse than reporting nothing. A signal that fires
on a healthy system teaches an operator to ignore it, and this signal exists to
be believed the one time it means something.

## Decision

**Three states.** `advancing` when nothing blocks the sealer. `waiting_on_gap`
when a gap exists but the events beyond it are still inside the lag window, so
it may fill on its own. `paused_at_gap` when it persisted past the window and
the sealer has stopped.

**The gateway owns the threshold.** It carries `seal_lag_seconds` in the status
message and judges its own state against it. A control plane must not hold a
threshold describing a system it does not operate, and must not synthesise a
state it was not told: the state is the gateway's judgement, the window is the
number it judged against, and both travel together so a reader can check one
against the other.

**Gap age is required and threshold-free.** `gap_age_seconds` travels alongside,
measured from the first event beyond the gap, because that event's id was
allocated after the missing ones so the gap has existed at least since it was
written. That is a lower bound and it is objective. A consumer can reason about
an age without agreeing with the gateway about what counts as too long, which
matters because the declared window may itself be misconfigured.

**`last_sealed_event_id` and `first_unsealed_event_id` are required in every
state**, not only when paused. "How far behind is this gateway" is worth
answering while it is still healthy.

## The default window, and where the number comes from

`DefaultSealLagSeconds` is 300, and it is not chosen here. It is what the sealer
already uses: `aegis-migrate seal` takes `-lag-seconds 300`, and
`docs/AUDIT-INTEGRITY.md` section 6 specifies `audit.seal_lag_seconds` with a
five minute default. Reading it from the component whose behaviour it describes
is the only way the two can be relied on to agree; a number picked independently
would drift the first time either moved.

A gateway running a non-default window passes the same value it runs the sealer
with. `aegis-migrate submit -lag-seconds` mirrors `aegis-migrate seal
-lag-seconds` for that reason, and disagreement between them produces a state
describing a gateway that does not exist.

## Consequences

- A healthy gateway with an in-flight transaction reports `waiting_on_gap` and
  logs at info. Only `paused_at_gap` warns.
- The control plane stores the declared window next to the declared state, so
  an evidence bundle can show both. A gateway declaring a two-day window is
  visible as such rather than silently redefining what healthy means.
- The states are the gateway's assertions. Nothing stops one lying about its
  own health, and nothing here claims otherwise: this signal explains an
  absence of checkpoints, it does not attest anything.
- `SealOptions.LagSeconds` has no default applied in `applyDefaults`, unlike
  `BatchSize`. The 300 comes from the CLI flag, so a caller constructing
  `SealOptions` directly gets zero lag, which is the configuration the
  specification warns about. The comment now says so. Changing it would need a
  way to distinguish unset from explicitly zero, and several tests rely on zero
  meaning zero.
