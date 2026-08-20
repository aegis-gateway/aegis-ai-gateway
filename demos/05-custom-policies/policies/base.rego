package aegis.policy

import rego.v1

# Base policy: defaults and the allow/reason rules that aggregate all deny entries.
# Individual policy files only need to add `deny contains msg if { ... }` rules.

default allow := true

default reason := ""

allow := false if {
	count(deny) > 0
}

reason := concat("; ", deny) if {
	count(deny) > 0
}
