package aegis.policy

import rego.v1

default allow := true

default reason := ""

allow := false if {
	count(deny) > 0
}

reason := concat("; ", deny) if {
	count(deny) > 0
}
