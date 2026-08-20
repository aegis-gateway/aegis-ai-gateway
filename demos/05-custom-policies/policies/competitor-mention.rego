package aegis.policy

import rego.v1

competitors := ["portkey", "litellm", "kong ai"]

deny contains msg if {
	some m in input.messages
	some name in competitors
	contains(lower(m.content), name)
	msg := sprintf("competitor mention detected: %s", [name])
}
