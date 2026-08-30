// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gateway

// modelAllowed reports whether an API key's allowlist permits a model alias.
//
// The two callers are ListModels, which decides what a key may see, and
// ChatCompletions, which decides what it may use. Those answers must agree: a
// model advertised to a key and then refused, or hidden from a key and then
// served, is a worse outcome than either rule on its own. One function is what
// keeps them from drifting.
//
// An EMPTY allowlist permits everything. That is what ListModels has always
// done, and it is the semantics stored keys rely on: allowed_models defaults to
// '[]' in the api_keys schema, so tightening it here would revoke every model
// from every key that never set one.
//
// That default is only safe because an empty list can no longer mean "the
// stored value could not be read". internal/auth/store.go used to discard the
// JSON decode error, so a malformed allowed_models silently produced an empty
// slice and this function then granted every model: a fail-open on an access
// control. The lookup now refuses rather than returning a key whose
// restrictions are unknown, so empty here means empty in the database.
//
// Matching is exact string equality against the models.yaml alias, not the
// resolved provider model. A key allowlisted for "aegis-balanced" is therefore
// refused "aegis-gpt4" even though that alias is configured as a deprecated
// spelling of it, because config.ModelMapping.DeprecatedAlias is declared and
// never read: nothing rewrites one to the other at any point in the request
// path, and ListModels shows them as two separate models.
func modelAllowed(allowed []string, model string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, m := range allowed {
		if m == model {
			return true
		}
	}
	return false
}
