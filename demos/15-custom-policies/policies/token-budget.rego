package aegis.policy

import rego.v1

total_chars := sum([count(m.content) | some m in input.messages])

deny contains msg if {
	input.user.team == "external"
	total_chars > 500
	msg := "prompt too long for external team (limit: 500 chars)"
}

allow := false if {
	count(deny) > 0
}

reason := concat("; ", deny) if {
	count(deny) > 0
}
