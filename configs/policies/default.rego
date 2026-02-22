package aegis.policy

import rego.v1

# Default policy: allow all requests unless explicitly denied.

default allow := true

default reason := ""

# Block RESTRICTED data from being sent to external providers.
deny contains msg if {
	input.request.classification == "RESTRICTED"
	input.request.provider_type == "external"
	msg := "RESTRICTED data cannot be sent to external providers"
}

# Override allow if any deny rule fires.
allow := false if {
	count(deny) > 0
}

reason := concat("; ", deny) if {
	count(deny) > 0
}
