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
# The router already refuses these requests, because routeEligible skips every
# route whose ceiling sits below the caller's classification. This rule is
# defence in depth and, more usefully, it turns a confusing
# "503 No provider available" into a 451 that names the reason.
deny contains msg if {
	input.request.classification == "RESTRICTED"
	not input.request.model in restricted_cleared_aliases
	msg := sprintf("RESTRICTED data cannot be routed through alias %q: no alias is cleared for it", [input.request.model])
}

# Override allow if any deny rule fires.
allow := false if {
	count(deny) > 0
}

reason := concat("; ", deny) if {
	count(deny) > 0
}
