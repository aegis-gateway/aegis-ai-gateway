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

package controlplanev1

import "embed"

// Schemas holds the JSON Schema documents for this protocol version.
//
// They are embedded so that a consumer gets them from the module rather than
// from a copy on disk. A control plane validating against a vendored copy of a
// schema is validating against whatever it last remembered to sync, which is
// how a wire contract drifts without anyone noticing.
//
//go:embed schema/*.json
var Schemas embed.FS

const (
	// SchemaGatewayRegistration is the path within [Schemas] of the gateway
	// registration schema.
	SchemaGatewayRegistration = "schema/gateway-registration-v1.schema.json"

	// SchemaCheckpointSubmission is the path within [Schemas] of the
	// checkpoint submission schema.
	SchemaCheckpointSubmission = "schema/checkpoint-submission-v1.schema.json"
)
