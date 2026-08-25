package aegis.policy

import rego.v1

# Blocks mentions of unreleased internal projects.
#
# The pattern this demonstrates is a denylist of terms the organization does not
# want sent to a third-party model: unreleased codenames, acquisition targets,
# an embargoed product name. The rule is the same whatever the terms are, so the
# demo uses invented codenames rather than real company names.
restricted_terms := ["project ironwood", "project halcyon", "northwind"]

deny contains msg if {
	some m in input.messages
	some term in restricted_terms
	contains(lower(m.content), term)
	msg := sprintf("restricted term detected: %s", [term])
}
