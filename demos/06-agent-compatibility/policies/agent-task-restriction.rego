package aegis.policy

import rego.v1

# Block requests that ask the agent to exfiltrate data to an external URL.
# This simulates a governance rule a security team might deploy to prevent
# prompt-injected exfiltration attempts.

exfiltration_patterns := ["curl http", "wget http", "send to", "upload to", "POST to http"]

deny contains msg if {
	some m in input.messages
	some pattern in exfiltration_patterns
	contains(lower(m.content), lower(pattern))
	msg := sprintf("agent task blocked: possible data exfiltration pattern '%s'", [pattern])
}
