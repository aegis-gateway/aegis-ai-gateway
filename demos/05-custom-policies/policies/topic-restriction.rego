package aegis.policy

import rego.v1

financial_terms := ["portfolio", "stock price", "buy shares", "trading strategy"]

deny contains msg if {
	input.user.team != "finance"
	some m in input.messages
	some term in financial_terms
	contains(lower(m.content), term)
	msg := "financial topic restricted to finance team"
}
