package aegis.policy

import rego.v1

# ---------------------------------------------------------------------------
# WARNING: a deny message is stored, sealed and exported. Never interpolate
# request content into one.
#
# input.messages[_].content and input.messages[_].parts carry the caller's
# prompt text in full, so that a rule can decide about it. Deciding about it is
# fine. Quoting it back in the message is not.
#
# The string a deny rule produces is joined into `reason`, written to
# audit_events.reason as up to 512 characters, covered by the leaf hash the
# sealer computes over that row, sealed into the Merkle chain, and served by
# GET /aegis/v1/audit/events in JSON and CSV. Nothing downstream filters it,
# and once a checkpoint covers the row it cannot be edited without breaking
# chain verification.
#
# So this is a retention violation wearing the shape of a helpful error:
#
#     msg := sprintf("blocked: %s", [input.messages[0].content])       # NEVER
#
# and this is the same rule written correctly:
#
#     msg := sprintf("blocked by rule %q (%d match(es))", ["no-ssn", count(hits)])
#
# Match on content, report an identifier. Rule names, enumerated categories,
# counts, offsets, model aliases and classification tiers are all safe: they are
# operator-authored or drawn from a fixed set. Anything reachable from
# input.messages is not.
#
# The rule below follows that: it interpolates input.request.model, which is an
# alias name from configs/models.yaml, not caller text.
#
# See docs/evidence/known-limitations.md section 2.12.
# ---------------------------------------------------------------------------

# Default policy: allow all requests unless explicitly denied.

default allow := true

default reason := ""

# Aliases an operator has explicitly cleared to carry RESTRICTED data.
#
# Empty as shipped, because nothing in configs/models.yaml declares a route with
# a classification_ceiling of RESTRICTED. Add an alias here only once a route
# exists that is approved for it, and keep the two in step.
restricted_cleared_aliases := set()

# Keep RESTRICTED data off any alias that has not been cleared for it.
#
# This tests the model alias, not input.request.provider_type, and that is
# deliberate rather than an oversight. provider_type is populated from
# adapter.Name(), which reports the adapter implementation ("openai",
# "anthropic") and never a trust boundary: azure_openai and internal_vllm both
# route through the OpenAI adapter and both report "openai". A rule written as
# `input.request.provider_type == "external"` compiles, reads correctly, and can
# never fire. That is what this file contained before, so the shipped bundle
# could not deny anything at all.
#
# Alias names are operator-controlled and map one to one onto routes in
# configs/models.yaml, so they carry the distinction provider_type does not.
#
# Reachability, stated precisely, because it is easy to get wrong.
#
# Policy is evaluated AFTER routing, at internal/gateway/handler.go, because it
# needs the resolved provider. On the configuration as shipped, no route declares
# a classification_ceiling of RESTRICTED, so routeEligible skips every route and
# ResolveRoute fails first: a RESTRICTED request gets a 503 "No provider
# available" and never reaches this rule.
#
# This rule therefore fires only once an operator adds a route whose ceiling
# admits RESTRICTED, at which point routing succeeds and the alias allowlist
# below decides. That is a real difference from the rule this replaced, which
# tested provider_type == "external" and could not fire under ANY configuration,
# but it is not the same as firing today. Do not describe it as turning the 503
# into a 451: it does not, and an earlier version of this comment said so
# wrongly.
deny contains msg if {
	input.request.classification == "RESTRICTED"
	not input.request.model in restricted_cleared_aliases
	msg := sprintf("RESTRICTED data cannot be routed through alias %q: it is not cleared for RESTRICTED", [input.request.model])
}

# Override allow if any deny rule fires.
allow := false if {
	count(deny) > 0
}

reason := concat("; ", deny) if {
	count(deny) > 0
}
