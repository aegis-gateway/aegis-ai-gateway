package aegis.policy

import rego.v1

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
